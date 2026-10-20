package redfish

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
)

const ethernetInterfacesPath = "/redfish/v1/Systems/1/EthernetInterfaces"

func (s *Server) handleEthernetInterfaceCollection(w http.ResponseWriter, r *http.Request) {
	members := make([]ODataID, 0, len(s.systemInfo.MACAddresses))
	for i := range s.systemInfo.MACAddresses {
		members = append(members, ODataID{ODataID: fmt.Sprintf("%s/%d", ethernetInterfacesPath, i+1)})
	}
	col := EthernetInterfaceCollection{
		ODataType:    "#EthernetInterfaceCollection.EthernetInterfaceCollection",
		ODataID:      ethernetInterfacesPath,
		Name:         "Ethernet Interface Collection",
		MembersCount: len(members),
		Members:      members,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(col)
}

// handleGetEthernetInterface serves the NIC with 1-based index {nicid}, in
// QEMU command-line order.
func (s *Server) handleGetEthernetInterface(w http.ResponseWriter, r *http.Request) {
	nicID := mux.Vars(r)["nicid"]
	idx, err := strconv.Atoi(nicID)
	if err != nil || idx < 1 || idx > len(s.systemInfo.MACAddresses) {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "ethernet interface not found")
		return
	}

	status, err := s.machine.GetQMPStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", "failed to get system status")
		return
	}
	// The virtual link is up exactly while the VM runs
	linkStatus := "LinkDown"
	if mapQMPStatusToRedfish(status) == "On" {
		linkStatus = "LinkUp"
	}

	mac := s.systemInfo.MACAddresses[idx-1]
	nic := EthernetInterface{
		ODataType:           "#EthernetInterface.v1_4_0.EthernetInterface",
		ODataID:             fmt.Sprintf("%s/%d", ethernetInterfacesPath, idx),
		ODataContext:        "/redfish/v1/$metadata#EthernetInterface.EthernetInterface",
		ID:                  strconv.Itoa(idx),
		Name:                fmt.Sprintf("Ethernet Interface %d", idx),
		MACAddress:          mac,
		PermanentMACAddress: mac,
		LinkStatus:          linkStatus,
		Status:              ResourceStatus{State: "Enabled", Health: "OK"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nic)
}
