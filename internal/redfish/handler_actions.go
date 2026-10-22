package redfish

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tjst-t/qemu-bmc/internal/machine"
)

var validResetTypes = map[string]bool{
	"On":               true,
	"ForceOff":         true,
	"GracefulShutdown": true,
	"ForceRestart":     true,
	"GracefulRestart":  true,
	"PowerCycle":       true,
	"ForceOn":          true,
	"Nmi":              true,
	"PushPowerButton":  true,
}

func (s *Server) handleResetAction(w http.ResponseWriter, r *http.Request) {
	var req ResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "invalid request body")
		return
	}

	if !validResetTypes[req.ResetType] {
		writeError(w, http.StatusBadRequest, "ActionParameterNotSupported",
			"invalid ResetType: "+req.ResetType)
		return
	}

	if err := s.machine.Reset(req.ResetType); err != nil {
		if errors.Is(err, machine.ErrPoweredOff) {
			writeError(w, http.StatusConflict, "ActionNotSupported",
				req.ResetType+" requires the system to be powered on")
			return
		}
		writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
