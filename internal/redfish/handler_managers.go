package redfish

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleManagerCollection(w http.ResponseWriter, r *http.Request) {
	col := ManagerCollection{
		ODataType:    "#ManagerCollection.ManagerCollection",
		ODataID:      "/redfish/v1/Managers",
		Name:         "Manager Collection",
		MembersCount: 1,
		Members:      []ODataID{{ODataID: "/redfish/v1/Managers/1"}},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(col)
}

func (s *Server) handleGetManager(w http.ResponseWriter, r *http.Request) {
	mgr := Manager{
		ODataType:    "#Manager.v1_3_0.Manager",
		ODataID:      "/redfish/v1/Managers/1",
		ODataContext: "/redfish/v1/$metadata#Manager.Manager",
		ID:           "1",
		Name:         "QEMU BMC",
		ManagerType:  "BMC",
		VirtualMedia: ODataID{ODataID: "/redfish/v1/Managers/1/VirtualMedia"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mgr)
}

func (s *Server) handleVirtualMediaCollection(w http.ResponseWriter, r *http.Request) {
	col := VirtualMediaCollection{
		ODataType:    "#VirtualMediaCollection.VirtualMediaCollection",
		ODataID:      "/redfish/v1/Managers/1/VirtualMedia",
		Name:         "Virtual Media Collection",
		MembersCount: 1,
		Members:      []ODataID{{ODataID: "/redfish/v1/Managers/1/VirtualMedia/CD1"}},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(col)
}

func (s *Server) handleGetVirtualMedia(w http.ResponseWriter, r *http.Request) {
	vm := VirtualMedia{
		ODataType:    "#VirtualMedia.v1_2_0.VirtualMedia",
		ODataID:      "/redfish/v1/Managers/1/VirtualMedia/CD1",
		ODataContext: "/redfish/v1/$metadata#VirtualMedia.VirtualMedia",
		ID:           "CD1",
		Name:         "Virtual CD",
		MediaTypes:   []string{"CD", "DVD"},
		Image:        s.currentMedia,
		Inserted:     s.currentMedia != "",
		ConnectedVia: func() string {
			if s.currentMedia != "" {
				return "URI"
			}
			return "NotConnected"
		}(),
		Actions: VirtualMediaActions{
			InsertMedia: VirtualMediaAction{Target: "/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia"},
			EjectMedia:  VirtualMediaAction{Target: "/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia"},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vm)
}

func (s *Server) handleInsertMedia(w http.ResponseWriter, r *http.Request) {
	var req InsertMediaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "Invalid request body")
		return
	}

	if req.Image == "" {
		writeError(w, http.StatusBadRequest, "PropertyMissing", "Image URL is required")
		return
	}

	if err := s.machine.InsertMedia(req.Image); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	s.currentMedia = req.Image
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleEjectMedia(w http.ResponseWriter, r *http.Request) {
	if err := s.machine.EjectMedia(); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	s.currentMedia = ""
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
