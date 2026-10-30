package redfish

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tjst-t/qemu-bmc/internal/audit"
	"github.com/tjst-t/qemu-bmc/internal/fault"
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
	action(r).Set("reset_type", "")
	var req ResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "invalid request body")
		return
	}
	action(r).Set("reset_type", req.ResetType)

	if !validResetTypes[req.ResetType] {
		writeError(w, http.StatusBadRequest, "ActionParameterNotSupported",
			"invalid ResetType: "+req.ResetType)
		return
	}

	if err := s.machine.Reset(req.ResetType); err != nil {
		var ignored *fault.PowerActionIgnoredError
		if errors.As(err, &ignored) {
			// A no-op for the client
			action(r).Result(audit.Fault, "fault", ignored.FaultID)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var failed *fault.PowerActionError
		if errors.As(err, &failed) {
			action(r).Result(audit.Fault, "status", "500", "fault", failed.FaultID)
		}
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
