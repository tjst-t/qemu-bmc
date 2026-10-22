package redfish

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tjst-t/qemu-bmc/internal/machine"
)

const (
	secureBootPath          = systemPath + "/SecureBoot"
	secureBootResetKeysPath = secureBootPath + "/Actions/SecureBoot.ResetKeys"

	// secureBootMode is the mode of the Secure Boot VARS template: the
	// Microsoft keys (with a PK) are enrolled, which puts the firmware in
	// User Mode; Deployed Mode is only entered on request from User Mode.
	secureBootMode = "UserMode"
)

// handleGetSecureBoot serves Systems/1/SecureBoot, which only exists when
// the VM boots OVMF in process management mode.
func (s *Server) handleGetSecureBoot(w http.ResponseWriter, r *http.Request) {
	if !s.machine.SecureBootSupported() {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "Secure Boot is not available: "+machine.ErrSecureBootUnsupported.Error())
		return
	}
	sb := s.machine.GetSecureBoot()
	etag := secureBootETag(sb)
	current := "Disabled"
	if sb.CurrentBoot {
		current = "Enabled"
	}
	resource := SecureBootResource{
		ODataType:             "#SecureBoot.v1_1_0.SecureBoot",
		ODataID:               secureBootPath,
		ODataEtag:             etag,
		ID:                    "SecureBoot",
		Name:                  "UEFI Secure Boot",
		SecureBootEnable:      sb.Enable,
		SecureBootCurrentBoot: current,
		SecureBootMode:        secureBootMode,
		Actions: SecureBootActions{
			ResetKeys: ResetKeysAction{
				Target:          secureBootResetKeysPath,
				AllowableValues: []string{"ResetAllKeysToDefault"},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	json.NewEncoder(w).Encode(resource)
}

// handlePatchSecureBoot sets SecureBootEnable, which applies from the next
// time QEMU starts (a reset restarts it if the setting changed).
func (s *Server) handlePatchSecureBoot(w http.ResponseWriter, r *http.Request) {
	if !s.machine.SecureBootSupported() {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "Secure Boot is not available: "+machine.ErrSecureBootUnsupported.Error())
		return
	}
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && ifMatch != secureBootETag(s.machine.GetSecureBoot()) {
		writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "ETag mismatch")
		return
	}

	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "invalid request body")
		return
	}
	raw, ok := req["SecureBootEnable"]
	if !ok {
		writeError(w, http.StatusBadRequest, "PropertyMissing", "no patchable properties provided: only SecureBootEnable can be changed")
		return
	}
	var enable *bool
	if err := json.Unmarshal(raw, &enable); err != nil || enable == nil {
		writeError(w, http.StatusBadRequest, "PropertyValueTypeError", "SecureBootEnable must be a boolean")
		return
	}
	if err := s.machine.SetSecureBootEnable(*enable); err != nil {
		writeError(w, http.StatusBadRequest, "PropertyValueError", err.Error())
		return
	}

	s.handleGetSecureBoot(w, r)
}

// handleResetKeys resets the Secure Boot keys to the defaults (the VARS
// template), for the next time QEMU starts if it is running.
func (s *Server) handleResetKeys(w http.ResponseWriter, r *http.Request) {
	if !s.machine.SecureBootSupported() {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "Secure Boot is not available: "+machine.ErrSecureBootUnsupported.Error())
		return
	}
	var req ResetKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "invalid request body")
		return
	}
	if req.ResetKeysType != "ResetAllKeysToDefault" {
		writeError(w, http.StatusBadRequest, "ActionParameterNotSupported",
			"invalid ResetKeysType: "+req.ResetKeysType+" (only ResetAllKeysToDefault is supported)")
		return
	}
	if err := s.machine.ResetSecureBootKeys(); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// secureBootETag creates the ETag of the SecureBoot resource.
func secureBootETag(sb machine.SecureBoot) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%t-%t-%s", sb.Enable, sb.CurrentBoot, secureBootMode)))
	return fmt.Sprintf(`"%x"`, hash[:8])
}
