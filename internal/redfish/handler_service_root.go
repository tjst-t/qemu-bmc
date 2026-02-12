package redfish

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleServiceRoot(w http.ResponseWriter, r *http.Request) {
	root := ServiceRoot{
		ODataType:      "#ServiceRoot.v1_5_0.ServiceRoot",
		ODataID:        "/redfish/v1",
		ID:             "RootService",
		Name:           "Root Service",
		RedfishVersion: "1.0.0",
		Systems:        ODataID{ODataID: "/redfish/v1/Systems"},
		Managers:       ODataID{ODataID: "/redfish/v1/Managers"},
		Chassis:        ODataID{ODataID: "/redfish/v1/Chassis"},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(root)
}
