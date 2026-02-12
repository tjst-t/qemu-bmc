package redfish

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleChassisCollection(w http.ResponseWriter, r *http.Request) {
	col := ChassisCollection{
		ODataType:    "#ChassisCollection.ChassisCollection",
		ODataID:      "/redfish/v1/Chassis",
		Name:         "Chassis Collection",
		MembersCount: 1,
		Members:      []ODataID{{ODataID: "/redfish/v1/Chassis/1"}},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(col)
}

func (s *Server) handleGetChassis(w http.ResponseWriter, r *http.Request) {
	chassis := Chassis{
		ODataType:    "#Chassis.v1_0_0.Chassis",
		ODataID:      "/redfish/v1/Chassis/1",
		ODataContext: "/redfish/v1/$metadata#Chassis.Chassis",
		ID:           "1",
		Name:         "QEMU Virtual Machine Chassis",
		ChassisType:  "RackMount",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chassis)
}
