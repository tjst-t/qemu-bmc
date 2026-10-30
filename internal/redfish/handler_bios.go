package redfish

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/tjst-t/qemu-bmc/internal/bios"
)

const (
	biosPath          = systemPath + "/Bios"
	biosSettingsPath  = biosPath + "/Settings"
	biosResetBiosPath = biosPath + "/Actions/Bios.ResetBios"

	// biosAttributeRegistry is the Id of the BIOS AttributeRegistry, the
	// Bios AttributeRegistry property; sushy finds the registry by it.
	biosAttributeRegistry = "BiosAttributeRegistry.1.0.0"

	// Settings apply at the next boot only
	biosApplyTime = "OnReset"

	// baseRegistry prefixes the Base message registry MessageIds of errors
	baseRegistry = "Base.1.8."
)

// BiosStore keeps the BIOS attributes (implemented by *bios.Store).
type BiosStore interface {
	Attributes() map[string]string
	Pending() map[string]string
	ResetPending() bool
	SetPending(attrs map[string]json.RawMessage) error
	ResetToDefaults() error
	Registry() []bios.Attribute
}

// SetBios makes the BIOS attributes available as Systems/1/Bios, with the
// attribute registry under /redfish/v1/Registries.
func (s *Server) SetBios(b BiosStore) {
	s.bios = b
}

// BiosResource is Systems/1/Bios and its Settings resource.
type BiosResource struct {
	ODataType                  string              `json:"@odata.type"`
	ODataID                    string              `json:"@odata.id"`
	ODataEtag                  string              `json:"@odata.etag,omitempty"`
	ID                         string              `json:"Id"`
	Name                       string              `json:"Name"`
	Description                string              `json:"Description,omitempty"`
	AttributeRegistry          string              `json:"AttributeRegistry"`
	Attributes                 map[string]string   `json:"Attributes"`
	ResetBiosToDefaultsPending *bool               `json:"ResetBiosToDefaultsPending,omitempty"`
	Settings                   *SettingsObject     `json:"@Redfish.Settings,omitempty"`
	SettingsApplyTime          *PreferredApplyTime `json:"@Redfish.SettingsApplyTime,omitempty"`
	Actions                    *BiosActions        `json:"Actions,omitempty"`
}

// SettingsObject is the @Redfish.Settings annotation of a resource with
// future settings.
type SettingsObject struct {
	ODataType           string   `json:"@odata.type"`
	SettingsObject      ODataID  `json:"SettingsObject"`
	SupportedApplyTimes []string `json:"SupportedApplyTimes"`
}

// PreferredApplyTime is the @Redfish.SettingsApplyTime annotation.
type PreferredApplyTime struct {
	ODataType string `json:"@odata.type,omitempty"`
	ApplyTime string `json:"ApplyTime,omitempty"`
}

// BiosActions are the actions of the Bios resource
type BiosActions struct {
	ResetBios ActionTarget `json:"#Bios.ResetBios"`
}

// biosNotFound answers a Bios request when no BIOS store is set.
func (s *Server) biosNotFound(w http.ResponseWriter) bool {
	if s.bios == nil {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "BIOS settings are not available")
		return true
	}
	return false
}

func (s *Server) handleGetBios(w http.ResponseWriter, r *http.Request) {
	if s.biosNotFound(w) {
		return
	}
	attrs := s.bios.Attributes()
	reset := s.bios.ResetPending()
	etag := biosETag("current", attrs, reset)
	resource := BiosResource{
		ODataType:                  "#Bios.v1_2_0.Bios",
		ODataID:                    biosPath,
		ODataEtag:                  etag,
		ID:                         "Bios",
		Name:                       "BIOS Configuration Current Settings",
		Description:                "BIOS attributes (reported only, they do not change the VM)",
		AttributeRegistry:          biosAttributeRegistry,
		Attributes:                 attrs,
		ResetBiosToDefaultsPending: &reset,
		Settings: &SettingsObject{
			ODataType:           "#Settings.v1_3_0.Settings",
			SettingsObject:      ODataID{ODataID: biosSettingsPath},
			SupportedApplyTimes: []string{biosApplyTime},
		},
		Actions: &BiosActions{ResetBios: ActionTarget{Target: biosResetBiosPath}},
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, resource)
}

// pendingSettings returns the Bios Settings resource and its ETag.
func (s *Server) pendingSettings() (BiosResource, string) {
	pending := s.bios.Pending()
	etag := biosETag("pending", pending, s.bios.ResetPending())
	return BiosResource{
		ODataType:         "#Bios.v1_2_0.Bios",
		ODataID:           biosSettingsPath,
		ODataEtag:         etag,
		ID:                "Settings",
		Name:              "BIOS Configuration Pending Settings",
		Description:       "BIOS attributes applied at the next boot",
		AttributeRegistry: biosAttributeRegistry,
		Attributes:        pending,
		SettingsApplyTime: &PreferredApplyTime{
			ODataType: "#Settings.v1_3_0.PreferredApplyTime",
			ApplyTime: biosApplyTime,
		},
	}, etag
}

func (s *Server) handleGetBiosSettings(w http.ResponseWriter, r *http.Request) {
	if s.biosNotFound(w) {
		return
	}
	resource, etag := s.pendingSettings()
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, resource)
}

// handlePatchBiosSettings adds attributes to the settings pending until the
// next boot.
func (s *Server) handlePatchBiosSettings(w http.ResponseWriter, r *http.Request) {
	action(r).Set("op", "settings").Set("attributes", "")
	if s.biosNotFound(w) {
		return
	}
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && ifMatch != "*" {
		if _, etag := s.pendingSettings(); ifMatch != etag {
			writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "ETag mismatch")
			return
		}
	}

	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req == nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "invalid request body")
		return
	}
	var attrs map[string]json.RawMessage
	for key, raw := range req {
		switch {
		case key == "Attributes":
			if err := json.Unmarshal(raw, &attrs); err != nil || attrs == nil {
				writeMessageError(w, http.StatusBadRequest, baseMessage("PropertyValueTypeError", "#/Attributes",
					fmt.Sprintf("The value %s for the property Attributes is of a different type than the property can accept.", raw),
					string(raw), "Attributes"))
				return
			}
		case key == "@Redfish.SettingsApplyTime":
			var at PreferredApplyTime
			if err := json.Unmarshal(raw, &at); err != nil || (at.ApplyTime != "" && at.ApplyTime != biosApplyTime) {
				writeMessageError(w, http.StatusBadRequest, baseMessage("PropertyValueNotInList", "#/@Redfish.SettingsApplyTime/ApplyTime",
					fmt.Sprintf("The value %s for the property ApplyTime is not in the list of acceptable values (only %s is supported).", at.ApplyTime, biosApplyTime),
					at.ApplyTime, "ApplyTime"))
				return
			}
		case strings.HasPrefix(key, "@odata."):
		default:
			writeMessageError(w, http.StatusBadRequest, baseMessage("PropertyUnknown", "#/"+key,
				fmt.Sprintf("The property %s is not in the list of valid properties for the resource.", key), key))
			return
		}
	}
	if attrs == nil {
		writeError(w, http.StatusBadRequest, "PropertyMissing", "no patchable properties provided: Attributes is required")
		return
	}
	names := make([]string, 0, len(attrs))
	for name := range attrs {
		names = append(names, name)
	}
	sort.Strings(names)
	action(r).Set("attributes", strings.Join(names, ","))

	if err := s.bios.SetPending(attrs); err != nil {
		var errs bios.Errors
		if !errors.As(err, &errs) {
			writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		var msgs []Message
		for _, e := range errs {
			msgs = append(msgs, baseMessage(e.MessageKey, "#/Attributes/"+e.Attribute, e.Message, e.Args...))
		}
		writeMessageError(w, http.StatusBadRequest, msgs...)
		return
	}

	resource, etag := s.pendingSettings()
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, resource)
}

// handleResetBios resets the attributes to their defaults at the next boot.
// The request body, if any, is ignored (the action has no parameters).
func (s *Server) handleResetBios(w http.ResponseWriter, r *http.Request) {
	action(r).Set("op", "reset")
	if s.biosNotFound(w) {
		return
	}
	if err := s.bios.ResetToDefaults(); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// biosETag creates the ETag of the Bios or Settings resource.
func biosETag(kind string, attrs map[string]string, reset bool) string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "%s-%t", kind, reset)
	for _, k := range keys {
		fmt.Fprintf(h, "\x00%s=%s", k, attrs[k])
	}
	return fmt.Sprintf(`"%x"`, h.Sum(nil)[:8])
}

// Message is a Redfish message, as in @Message.ExtendedInfo.
type Message struct {
	ODataType         string   `json:"@odata.type"`
	MessageID         string   `json:"MessageId"`
	Message           string   `json:"Message"`
	MessageArgs       []string `json:"MessageArgs,omitempty"`
	MessageSeverity   string   `json:"MessageSeverity"`
	RelatedProperties []string `json:"RelatedProperties,omitempty"`
	Resolution        string   `json:"Resolution,omitempty"`
}

// baseResolutions are the Resolutions of the Base registry messages used
var baseResolutions = map[string]string{
	"PropertyUnknown":        "Remove the unknown property from the request body and resubmit the request if the operation failed.",
	"PropertyValueTypeError": "Correct the value for the property in the request body and resubmit the request if the operation failed.",
	"PropertyValueNotInList": "Choose a value from the enumeration list that the implementation can support and resubmit the request if the operation failed.",
	"PropertyNotWritable":    "Remove the property from the request body and resubmit the request if the operation failed.",
	"StringValueTooLong":     "Resubmit the request with an appropriate string length.",
}

// baseMessage creates a Base registry message about the property at
// relatedProperty (a JSON pointer).
func baseMessage(key, relatedProperty, message string, args ...string) Message {
	return Message{
		ODataType:         "#Message.v1_1_1.Message",
		MessageID:         baseRegistry + key,
		Message:           message,
		MessageArgs:       args,
		MessageSeverity:   "Warning",
		RelatedProperties: []string{relatedProperty},
		Resolution:        baseResolutions[key],
	}
}

// writeMessageError writes a Redfish error with the messages as
// @Message.ExtendedInfo; the error message is the first one's.
func writeMessageError(w http.ResponseWriter, statusCode int, msgs ...Message) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(RedfishError{
		Error: RedfishErrorBody{
			Code:         baseRegistry + "GeneralError",
			Message:      msgs[0].Message,
			ExtendedInfo: msgs,
		},
	})
}
