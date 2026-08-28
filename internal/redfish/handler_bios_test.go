package redfish

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func TestGetRegistryCollection(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Registries", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var col RegistryFileCollection
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &col))
	require.Len(t, col.Members, 1)
	assert.Equal(t, "/redfish/v1/Registries/"+biosAttributeRegistryID, col.Members[0].ODataID)
}

func TestGetRegistryFile(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Registries/"+biosAttributeRegistryID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var file MessageRegistryFile
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &file))
	require.Len(t, file.Location, 1)
	assert.Equal(t, "/redfish/v1/Registries/"+biosAttributeRegistryID+".json", file.Location[0].Uri)
}

func TestGetRegistryFile_NotFound(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Registries/DoesNotExist", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetRegistryContent(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Registries/"+biosAttributeRegistryID+".json", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var reg AttributeRegistry
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reg))

	byName := make(map[string]RegistryEntryAttribute, len(reg.RegistryEntries.Attributes))
	for _, attr := range reg.RegistryEntries.Attributes {
		byName[attr.AttributeName] = attr
	}

	adminPhone, ok := byName["AdminPhone"]
	require.True(t, ok)
	assert.Equal(t, "String", adminPhone.Type)
	assert.False(t, adminPhone.ResetRequired)

	bootMode, ok := byName["BootMode"]
	require.True(t, ok)
	assert.Equal(t, "Enumeration", bootMode.Type)
	assert.True(t, bootMode.ResetRequired)
	require.Len(t, bootMode.Value, 2)
}

func TestGetBios(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1/Bios", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var bios Bios
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &bios))
	assert.Equal(t, "Uefi", bios.Attributes["BootMode"])
	require.NotNil(t, bios.RedfishSettings)
	assert.Equal(t, "/redfish/v1/Systems/1/Bios/Settings", bios.RedfishSettings.SettingsObject.ODataID)
}

func TestPatchBiosSettings(t *testing.T) {
	t.Run("no-reboot attribute applies immediately", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		body := `{"Attributes":{"AdminPhone":"+1-555-0100"}}`
		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1/Bios/Settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)

		biosReq := httptest.NewRequest("GET", "/redfish/v1/Systems/1/Bios", nil)
		w2 := httptest.NewRecorder()
		srv.ServeHTTP(w2, biosReq)
		var bios Bios
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &bios))
		assert.Equal(t, "+1-555-0100", bios.Attributes["AdminPhone"])

		settingsReq := httptest.NewRequest("GET", "/redfish/v1/Systems/1/Bios/Settings", nil)
		w3 := httptest.NewRecorder()
		srv.ServeHTTP(w3, settingsReq)
		var settings Bios
		require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &settings))
		assert.Empty(t, settings.Attributes)
	})

	t.Run("reboot-required attribute stays pending until reset", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		body := `{"Attributes":{"BootMode":"Bios"}}`
		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1/Bios/Settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNoContent, w.Code)

		// Not yet applied to the live resource.
		biosReq := httptest.NewRequest("GET", "/redfish/v1/Systems/1/Bios", nil)
		w2 := httptest.NewRecorder()
		srv.ServeHTTP(w2, biosReq)
		var bios Bios
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &bios))
		assert.Equal(t, "Uefi", bios.Attributes["BootMode"])

		// Present as pending.
		settingsReq := httptest.NewRequest("GET", "/redfish/v1/Systems/1/Bios/Settings", nil)
		w3 := httptest.NewRecorder()
		srv.ServeHTTP(w3, settingsReq)
		var settings Bios
		require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &settings))
		assert.Equal(t, "Bios", settings.Attributes["BootMode"])

		// A reset that leaves the VM powered on applies the pending change.
		resetBody := `{"ResetType":"PowerCycle"}`
		resetReq := httptest.NewRequest("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", strings.NewReader(resetBody))
		resetReq.Header.Set("Content-Type", "application/json")
		w4 := httptest.NewRecorder()
		srv.ServeHTTP(w4, resetReq)
		assert.Equal(t, http.StatusNoContent, w4.Code)

		biosReq2 := httptest.NewRequest("GET", "/redfish/v1/Systems/1/Bios", nil)
		w5 := httptest.NewRecorder()
		srv.ServeHTTP(w5, biosReq2)
		var bios2 Bios
		require.NoError(t, json.Unmarshal(w5.Body.Bytes(), &bios2))
		assert.Equal(t, "Bios", bios2.Attributes["BootMode"])

		settingsReq2 := httptest.NewRequest("GET", "/redfish/v1/Systems/1/Bios/Settings", nil)
		w6 := httptest.NewRecorder()
		srv.ServeHTTP(w6, settingsReq2)
		var settings2 Bios
		require.NoError(t, json.Unmarshal(w6.Body.Bytes(), &settings2))
		assert.Empty(t, settings2.Attributes)
	})

	t.Run("empty Attributes returns 400", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1/Bios/Settings", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestPatchBiosSettings_DebugLogging(t *testing.T) {
	captureLog := func(t *testing.T) *bytes.Buffer {
		t.Helper()
		var buf bytes.Buffer
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })
		return &buf
	}

	patch := func(t *testing.T, srv *Server, body string) {
		t.Helper()
		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1/Bios/Settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		require.Equal(t, http.StatusNoContent, w.Code)
	}

	t.Run("debug enabled logs applied and pending attributes", func(t *testing.T) {
		buf := captureLog(t)
		srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
		srv.SetDebug(true)

		patch(t, srv, `{"Attributes":{"AdminPhone":"+1-555-0100","BootMode":"Bios"}}`)

		out := buf.String()
		assert.Contains(t, out, "AdminPhone")
		assert.Contains(t, out, "+1-555-0100")
		assert.Contains(t, out, "applied immediately")
		assert.Contains(t, out, "BootMode")
		assert.Contains(t, out, "Bios")
		assert.Contains(t, out, "queued as pending")
	})

	t.Run("debug enabled logs pending promotion on reset", func(t *testing.T) {
		buf := captureLog(t)
		srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
		srv.SetDebug(true)

		patch(t, srv, `{"Attributes":{"BootMode":"Bios"}}`)
		buf.Reset()

		resetReq := httptest.NewRequest("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", strings.NewReader(`{"ResetType":"PowerCycle"}`))
		resetReq.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, resetReq)
		require.Equal(t, http.StatusNoContent, w.Code)

		out := buf.String()
		assert.Contains(t, out, "applied 1 pending setting")
		assert.Contains(t, out, "BootMode")
	})

	t.Run("debug disabled stays quiet", func(t *testing.T) {
		buf := captureLog(t)
		srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

		patch(t, srv, `{"Attributes":{"AdminPhone":"+1-555-0100"}}`)

		assert.NotContains(t, buf.String(), "BIOS PATCH")
	})
}

func TestPatchBiosSettings_ResetTypeThatStaysOff(t *testing.T) {
	// ForceOff leaves the VM powered off, so pending settings must not apply.
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	body := `{"Attributes":{"BootMode":"Bios"}}`
	req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1/Bios/Settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	resetBody := `{"ResetType":"ForceOff"}`
	resetReq := httptest.NewRequest("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", strings.NewReader(resetBody))
	resetReq.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, resetReq)
	require.Equal(t, http.StatusNoContent, w2.Code)

	settingsReq := httptest.NewRequest("GET", "/redfish/v1/Systems/1/Bios/Settings", nil)
	w3 := httptest.NewRecorder()
	srv.ServeHTTP(w3, settingsReq)
	var settings Bios
	require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &settings))
	assert.Equal(t, "Bios", settings.Attributes["BootMode"], "pending change must survive a reset that leaves the VM off")
}
