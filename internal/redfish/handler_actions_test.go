package redfish

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func TestResetAction(t *testing.T) {
	tests := []struct {
		name           string
		resetType      string
		expectedStatus int
		expectedCall   string
	}{
		{"On", "On", http.StatusNoContent, "On"},
		{"ForceOff", "ForceOff", http.StatusNoContent, "ForceOff"},
		{"GracefulShutdown", "GracefulShutdown", http.StatusNoContent, "GracefulShutdown"},
		{"ForceRestart", "ForceRestart", http.StatusNoContent, "ForceRestart"},
		{"GracefulRestart", "GracefulRestart", http.StatusNoContent, "GracefulRestart"},
		{"PowerCycle", "PowerCycle", http.StatusNoContent, "PowerCycle"},
		{"ForceOn", "ForceOn", http.StatusNoContent, "ForceOn"},
		{"Nmi", "Nmi", http.StatusNoContent, "Nmi"},
		{"PushPowerButton", "PushPowerButton", http.StatusNoContent, "PushPowerButton"},
		{"InvalidType returns 400", "InvalidType", http.StatusBadRequest, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockMachine(qmp.StatusRunning)
			srv := NewServer(mock, "", "", "")

			body := `{"ResetType":"` + tt.resetType + `"}`
			req := httptest.NewRequest("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.expectedCall != "" {
				require.NotEmpty(t, mock.Calls())
				assert.Equal(t, tt.expectedCall, mock.Calls()[0])
			}
		})
	}
}

func TestResetAction_NmiPoweredOff_Conflict(t *testing.T) {
	mock := newMockMachine(qmp.StatusShutdown)
	mock.resetErr = fmt.Errorf("nmi: %w", machine.ErrPoweredOff)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", strings.NewReader(`{"ResetType":"Nmi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, body, "error")
}

func TestResetAction_AllowableValues(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var system ComputerSystem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &system))
	assert.ElementsMatch(t, []string{"On", "ForceOff", "GracefulShutdown", "ForceRestart", "GracefulRestart",
		"PowerCycle", "ForceOn", "Nmi", "PushPowerButton"}, system.Actions.Reset.AllowableValues)
	for _, v := range system.Actions.Reset.AllowableValues {
		assert.True(t, validResetTypes[v], v)
	}
}
