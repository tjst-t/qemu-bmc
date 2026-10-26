package redfish

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/tjst-t/qemu-bmc/internal/bios"
)

const (
	registriesPath = "/redfish/v1/Registries"
	// biosRegistryFileID is the MessageRegistryFile of the BIOS attribute
	// registry; the registry itself is served at biosRegistryURI
	biosRegistryFileID = "BiosAttributeRegistry"
	biosRegistryURI    = registriesPath + "/" + biosRegistryFileID + "/" + biosAttributeRegistry + ".json"
)

// MessageRegistryFile is a member of /redfish/v1/Registries
type MessageRegistryFile struct {
	ODataType   string             `json:"@odata.type"`
	ODataID     string             `json:"@odata.id"`
	ID          string             `json:"Id"`
	Name        string             `json:"Name"`
	Description string             `json:"Description"`
	Languages   []string           `json:"Languages"`
	Registry    string             `json:"Registry"`
	Location    []RegistryLocation `json:"Location"`
}

// RegistryLocation is where a registry file is served
type RegistryLocation struct {
	Language string `json:"Language"`
	URI      string `json:"Uri"`
}

// AttributeRegistry is the BIOS attribute registry
type AttributeRegistry struct {
	ODataType        string                   `json:"@odata.type"`
	ID               string                   `json:"Id"`
	Name             string                   `json:"Name"`
	Description      string                   `json:"Description"`
	Language         string                   `json:"Language"`
	OwningEntity     string                   `json:"OwningEntity"`
	RegistryVersion  string                   `json:"RegistryVersion"`
	SupportedSystems []SupportedSystem        `json:"SupportedSystems"`
	RegistryEntries  AttributeRegistryEntries `json:"RegistryEntries"`
}

// SupportedSystem is a system an attribute registry applies to
type SupportedSystem struct {
	ProductName     string `json:"ProductName,omitempty"`
	SystemID        string `json:"SystemId"`
	FirmwareVersion string `json:"FirmwareVersion,omitempty"`
}

// AttributeRegistryEntries are the entries of an attribute registry
type AttributeRegistryEntries struct {
	Attributes []RegistryAttribute `json:"Attributes"`
}

// RegistryAttribute is an attribute of an attribute registry
type RegistryAttribute struct {
	AttributeName string              `json:"AttributeName"`
	DisplayName   string              `json:"DisplayName"`
	HelpText      string              `json:"HelpText"`
	Type          string              `json:"Type"`
	DefaultValue  string              `json:"DefaultValue,omitempty"`
	ReadOnly      bool                `json:"ReadOnly"`
	ResetRequired bool                `json:"ResetRequired"`
	Immutable     bool                `json:"Immutable"`
	Value         []RegistryAttrValue `json:"Value,omitempty"`
	MinLength     *int                `json:"MinLength,omitempty"`
	MaxLength     *int                `json:"MaxLength,omitempty"`
}

// RegistryAttrValue is an allowed value of an Enumeration attribute
type RegistryAttrValue struct {
	ValueName        string `json:"ValueName"`
	ValueDisplayName string `json:"ValueDisplayName"`
}

func (s *Server) handleRegistryCollection(w http.ResponseWriter, r *http.Request) {
	members := []ODataID{}
	if s.bios != nil {
		members = append(members, ODataID{ODataID: registriesPath + "/" + biosRegistryFileID})
	}
	writeJSON(w, http.StatusOK, Collection{
		ODataType:    "#MessageRegistryFileCollection.MessageRegistryFileCollection",
		ODataID:      registriesPath,
		Name:         "Registry File Collection",
		MembersCount: len(members),
		Members:      members,
	})
}

func (s *Server) handleGetRegistryFile(w http.ResponseWriter, r *http.Request) {
	if s.bios == nil || mux.Vars(r)["regid"] != biosRegistryFileID {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "registry not found")
		return
	}
	writeJSON(w, http.StatusOK, MessageRegistryFile{
		ODataType:   "#MessageRegistryFile.v1_1_0.MessageRegistryFile",
		ODataID:     registriesPath + "/" + biosRegistryFileID,
		ID:          biosRegistryFileID,
		Name:        "BIOS Attribute Registry File",
		Description: "Attribute registry of the Systems/1/Bios attributes",
		Languages:   []string{"en"},
		Registry:    "BiosAttributeRegistry.1.0",
		Location:    []RegistryLocation{{Language: "en", URI: biosRegistryURI}},
	})
}

// handleGetBiosRegistry serves the BIOS attribute registry (the file a
// MessageRegistryFile Location Uri points to).
func (s *Server) handleGetBiosRegistry(w http.ResponseWriter, r *http.Request) {
	if s.bios == nil {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "registry not found")
		return
	}
	var attrs []RegistryAttribute
	for _, a := range s.bios.Registry() {
		ra := RegistryAttribute{
			AttributeName: a.Name,
			DisplayName:   a.DisplayName,
			HelpText:      a.HelpText,
			Type:          a.Type,
			DefaultValue:  a.Default,
			ReadOnly:      a.ReadOnly,
			ResetRequired: true,
		}
		for _, v := range a.Values {
			ra.Value = append(ra.Value, RegistryAttrValue{ValueName: v, ValueDisplayName: v})
		}
		if a.Type == bios.TypeString {
			minLen, maxLen := 0, a.MaxLength
			ra.MinLength, ra.MaxLength = &minLen, &maxLen
		}
		attrs = append(attrs, ra)
	}
	firmware := s.firmware
	if firmware == "" {
		firmware = "dev"
	}
	writeJSON(w, http.StatusOK, AttributeRegistry{
		ODataType:       "#AttributeRegistry.v1_3_6.AttributeRegistry",
		ID:              biosAttributeRegistry,
		Name:            "qemu-bmc BIOS Attribute Registry",
		Description:     "BIOS attributes of the qemu-bmc VM (reported only, they do not change the VM)",
		Language:        "en",
		OwningEntity:    "qemu-bmc",
		RegistryVersion: "1.0.0",
		SupportedSystems: []SupportedSystem{{
			ProductName:     s.systemInfo.Model,
			SystemID:        "qemubmc", // letters and digits only
			FirmwareVersion: firmware,
		}},
		RegistryEntries: AttributeRegistryEntries{Attributes: attrs},
	})
}
