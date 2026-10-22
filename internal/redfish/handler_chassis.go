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
	powerState, err := s.systemPowerState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", "failed to get system status")
		return
	}

	chassis := Chassis{
		ODataType:    "#Chassis.v1_14_0.Chassis",
		ODataID:      "/redfish/v1/Chassis/1",
		ODataContext: "/redfish/v1/$metadata#Chassis.Chassis",
		ID:           "1",
		Name:         "QEMU Virtual Machine Chassis",
		ChassisType:  "RackMount",
		PowerState:   powerState,
		Status:       statusOK,
		Links: ChassisLinks{
			ComputerSystems: []ODataID{{ODataID: systemPath}},
			ManagedBy:       []ODataID{{ODataID: managerPath}},
		},
	}
	if len(s.systemInfo.Disks) > 0 {
		chassis.Drives = &ODataID{ODataID: drivesPath}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chassis)
}
