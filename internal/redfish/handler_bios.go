package redfish

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"
)

//go:embed data/bios_attribute_registry.json
var biosAttributeRegistryJSON []byte

const biosAttributeRegistryID = "BiosAttributeRegistry.v1_0_0"

// biosAttributeResetRequired maps each known BIOS attribute name to whether
// changing it requires a reboot.
var biosAttributeResetRequired map[string]bool

func init() {
	var reg AttributeRegistry
	if err := json.Unmarshal(biosAttributeRegistryJSON, &reg); err != nil {
		panic("redfish: invalid embedded BIOS attribute registry: " + err.Error())
	}
	biosAttributeResetRequired = make(map[string]bool, len(reg.RegistryEntries.Attributes))
	for _, attr := range reg.RegistryEntries.Attributes {
		biosAttributeResetRequired[attr.AttributeName] = attr.ResetRequired
	}
}

func (s *Server) handleRegistryCollection(w http.ResponseWriter, r *http.Request) {
	col := RegistryFileCollection{
		ODataType:    "#MessageRegistryFileCollection.MessageRegistryFileCollection",
		ODataID:      "/redfish/v1/Registries",
		Name:         "Registry File Collection",
		MembersCount: 1,
		Members:      []ODataID{{ODataID: "/redfish/v1/Registries/" + biosAttributeRegistryID}},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(col)
}

func (s *Server) handleGetRegistryFile(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if id != biosAttributeRegistryID {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "registry not found")
		return
	}

	file := MessageRegistryFile{
		ODataType: "#MessageRegistryFile.v1_1_4.MessageRegistryFile",
		ODataID:   "/redfish/v1/Registries/" + id,
		ID:        id,
		Name:      "BIOS Attribute Registry File",
		Languages: []string{"en"},
		Registry:  "BiosAttributeRegistry1.0",
		Location: []RegistryLocation{
			{Language: "en", Uri: "/redfish/v1/Registries/" + id + ".json"},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(file)
}

func (s *Server) handleGetRegistryContent(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if id != biosAttributeRegistryID {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "registry not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(biosAttributeRegistryJSON)
}

func biosSettingsObjectPath(systemID string) string {
	return "/redfish/v1/Systems/" + systemID + "/Bios/Settings"
}

func (s *Server) handleGetBios(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	bios := Bios{
		ODataType:         "#Bios.v1_2_2.Bios",
		ODataID:           "/redfish/v1/Systems/" + id + "/Bios",
		ID:                "Bios",
		Name:              "BIOS Configuration Current Settings",
		AttributeRegistry: biosAttributeRegistryID,
		Attributes:        s.getBiosAttributes(),
		RedfishSettings: &RedfishSettings{
			ODataType:      "#Settings.v1_3_5.Settings",
			SettingsObject: ODataID{ODataID: biosSettingsObjectPath(id)},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(bios)
}

func (s *Server) handleGetBiosSettings(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	bios := Bios{
		ODataType:         "#Bios.v1_2_2.Bios",
		ODataID:           biosSettingsObjectPath(id),
		ID:                "Settings",
		Name:              "BIOS Configuration Pending Settings",
		AttributeRegistry: biosAttributeRegistryID,
		Attributes:        s.getBiosPendingAttributes(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(bios)
}

// handlePatchBiosSettings applies attributes that don't require a reboot
// immediately (to the live /Bios resource) and stashes reboot-required
// attributes as pending, to be applied by applyPendingBiosSettings once the
// system is next reset. An attribute absent from the embedded registry is
// treated as reboot-required, matching how metal-operator's own client
// treats a missing/null ResetRequired.
func (s *Server) handlePatchBiosSettings(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	var req PatchBiosSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "invalid request body")
		return
	}
	if len(req.Attributes) == 0 {
		writeError(w, http.StatusBadRequest, "PropertyMissing", "no attributes provided")
		return
	}

	pending := make(map[string]any, len(req.Attributes))
	for name, value := range req.Attributes {
		if resetRequired, known := biosAttributeResetRequired[name]; known && !resetRequired {
			s.setBiosAttribute(name, value)
			s.debugf("BIOS PATCH system=%s: %s=%v applied immediately (no reset required)", id, name, value)
			continue
		}
		pending[name] = value
		s.debugf("BIOS PATCH system=%s: %s=%v queued as pending (reset required)", id, name, value)
	}
	if len(pending) > 0 {
		s.mergeBiosPendingAttributes(pending)
	}
	s.debugf("BIOS PATCH system=%s: current=%v pending=%v", id, s.getBiosAttributes(), s.getBiosPendingAttributes())

	w.WriteHeader(http.StatusNoContent)
}
