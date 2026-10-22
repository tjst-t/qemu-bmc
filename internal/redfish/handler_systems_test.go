package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func TestGetSystems(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var collection SystemCollection
	err := json.Unmarshal(w.Body.Bytes(), &collection)
	require.NoError(t, err)

	assert.Equal(t, 1, collection.MembersCount)
	assert.Len(t, collection.Members, 1)
	assert.Equal(t, "/redfish/v1/Systems/1", collection.Members[0].ODataID)
}

func TestGetSystem_PowerState(t *testing.T) {
	tests := []struct {
		name          string
		qmpStatus     qmp.Status
		expectedPower string
	}{
		{"running maps to On", qmp.StatusRunning, "On"},
		{"shutdown maps to Off", qmp.StatusShutdown, "Off"},
		{"paused maps to Off", qmp.StatusPaused, "Off"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockMachine(tt.qmpStatus)
			srv := NewServer(mock, "", "", "")

			req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var system ComputerSystem
			err := json.Unmarshal(w.Body.Bytes(), &system)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedPower, system.PowerState)
		})
	}
}

func TestGetSystem_PowerState_Transitions(t *testing.T) {
	for _, state := range []machine.PowerState{machine.PoweringOff, machine.PoweringOn} {
		t.Run(string(state), func(t *testing.T) {
			mock := newMockMachine(qmp.StatusRunning)
			mock.powerState = state
			srv := NewServer(mock, "", "", "")

			req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)

			var system ComputerSystem
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &system))
			assert.Equal(t, string(state), system.PowerState)
		})
	}
}

func TestGetSystem_UUID(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")
	srv.SetSystemUUID("12345678-9abc-4def-8123-456789abcdef")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var system ComputerSystem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &system))
	assert.Equal(t, "12345678-9abc-4def-8123-456789abcdef", system.UUID)
}

func TestGetSystem_UUIDOmittedWhenUnset(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.NotContains(t, w.Body.String(), `"UUID"`)
}

func TestGetSystem_ETag(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify ETag header is present and non-empty
	etag := w.Header().Get("ETag")
	assert.NotEmpty(t, etag)

	// Verify @odata.etag field matches header
	var system ComputerSystem
	err := json.Unmarshal(w.Body.Bytes(), &system)
	require.NoError(t, err)
	assert.Equal(t, etag, system.ODataEtag)
}

func TestGetSystem_BootOverride(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	mock.bootOverride = machine.BootOverride{
		Enabled: "Once",
		Target:  "Pxe",
		Mode:    "UEFI",
	}
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var system ComputerSystem
	err := json.Unmarshal(w.Body.Bytes(), &system)
	require.NoError(t, err)

	assert.Equal(t, "Once", system.Boot.BootSourceOverrideEnabled)
	assert.Equal(t, "Pxe", system.Boot.BootSourceOverrideTarget)
	assert.Equal(t, "UEFI", system.Boot.BootSourceOverrideMode)
	assert.Contains(t, system.Boot.AllowableValues, "Pxe")
	assert.Contains(t, system.Boot.AllowableValues, "Hdd")
	assert.Contains(t, system.Boot.AllowableValues, "Cd")
}

func TestPatchBootDevice(t *testing.T) {
	t.Run("PXE Once returns 200", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		body := `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Once"}}`
		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var system ComputerSystem
		err := json.Unmarshal(w.Body.Bytes(), &system)
		require.NoError(t, err)

		assert.Equal(t, "Pxe", system.Boot.BootSourceOverrideTarget)
		assert.Equal(t, "Once", system.Boot.BootSourceOverrideEnabled)

		// Verify the mock was updated
		assert.Equal(t, "Pxe", mock.bootOverride.Target)
		assert.Equal(t, "Once", mock.bootOverride.Enabled)
	})

	t.Run("ETag mismatch returns 412", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		body := `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Once"}}`
		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", `"wrong-etag"`)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusPreconditionFailed, w.Code)
	})

	t.Run("No ETag returns 200", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		body := `{"Boot":{"BootSourceOverrideTarget":"Hdd","BootSourceOverrideEnabled":"Continuous"}}`
		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var system ComputerSystem
		err := json.Unmarshal(w.Body.Bytes(), &system)
		require.NoError(t, err)

		assert.Equal(t, "Hdd", system.Boot.BootSourceOverrideTarget)
		assert.Equal(t, "Continuous", system.Boot.BootSourceOverrideEnabled)
	})
}

func TestGetSystem_HardwareSummary(t *testing.T) {
	mock := newMockMachine(qmp.StatusShutdown)
	srv := NewServer(mock, "", "", "")
	srv.SetSystemInfo(SystemInfo{MemoryMiB: 3072, CPUs: 4})

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))

	mem, ok := raw["MemorySummary"].(map[string]interface{})
	require.True(t, ok, "MemorySummary must be an object")
	assert.Equal(t, 3.0, mem["TotalSystemMemoryGiB"])

	proc, ok := raw["ProcessorSummary"].(map[string]interface{})
	require.True(t, ok, "ProcessorSummary must be an object")
	assert.Equal(t, 4.0, proc["Count"])
}

func TestGetSystem_HardwareSummary_FractionalGiB(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")
	srv.SetSystemInfo(SystemInfo{MemoryMiB: 1536, CPUs: 1})

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var system ComputerSystem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &system))
	require.NotNil(t, system.MemorySummary)
	assert.Equal(t, 1.5, system.MemorySummary.TotalSystemMemoryGiB)
	require.NotNil(t, system.ProcessorSummary)
	assert.Equal(t, 1, system.ProcessorSummary.Count)
}

func TestGetSystem_HardwareSummary_Unknown(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	assert.NotContains(t, raw, "MemorySummary")
	assert.NotContains(t, raw, "ProcessorSummary")
}

// getResource GETs path and decodes the response into a generic map.
func getResource(t *testing.T, srv *Server, path string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

func TestGetSystem_LinksAndStatus(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	system := getResource(t, srv, "/redfish/v1/Systems/1")
	assert.Equal(t, map[string]interface{}{
		"ManagedBy": []interface{}{map[string]interface{}{"@odata.id": "/redfish/v1/Managers/1"}},
		"Chassis":   []interface{}{map[string]interface{}{"@odata.id": "/redfish/v1/Chassis/1"}},
	}, system["Links"])
	assert.Equal(t, map[string]interface{}{"State": "Enabled", "Health": "OK"}, system["Status"])
}

func patchSystem(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func getSystemBoot(t *testing.T, srv *Server) (map[string]any, string) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var system map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &system))
	return system["Boot"].(map[string]any), w.Header().Get("ETag")
}

func TestGetSystem_HttpBootUri(t *testing.T) {
	mock := newMockMachine(qmp.StatusShutdown)
	srv := NewServer(mock, "", "", "")

	boot, _ := getSystemBoot(t, srv)
	v, ok := boot["HttpBootUri"]
	assert.True(t, ok, "HttpBootUri is present when unset")
	assert.Nil(t, v)
	assert.Contains(t, boot["BootSourceOverrideTarget@Redfish.AllowableValues"], "UefiHttp")

	mock.httpBootURI = "https://host/boot.iso"
	boot, _ = getSystemBoot(t, srv)
	assert.Equal(t, "https://host/boot.iso", boot["HttpBootUri"])
}

func TestPatchSystem_UefiHttp(t *testing.T) {
	mock := newMockMachine(qmp.StatusShutdown)
	srv := NewServer(mock, "", "", "")
	_, etagBefore := getSystemBoot(t, srv)

	w := patchSystem(t, srv, `{"Boot":{"BootSourceOverrideTarget":"UefiHttp","HttpBootUri":"https://10.0.0.1/x.iso","BootSourceOverrideEnabled":"Once","BootSourceOverrideMode":"UEFI"}}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, machine.BootOverride{Enabled: "Once", Target: "UefiHttp", Mode: "UEFI"}, mock.bootOverride)
	assert.Equal(t, "https://10.0.0.1/x.iso", mock.httpBootURI)
	var system map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &system))
	assert.Equal(t, "https://10.0.0.1/x.iso", system["Boot"].(map[string]any)["HttpBootUri"])

	_, etagAfter := getSystemBoot(t, srv)
	assert.NotEqual(t, etagBefore, etagAfter)

	// The URI alone changes the ETag
	mock.bootOverride = machine.BootOverride{Enabled: "Disabled", Target: "None", Mode: "UEFI"}
	_, withURI := getSystemBoot(t, srv)
	mock.httpBootURI = ""
	_, withoutURI := getSystemBoot(t, srv)
	assert.NotEqual(t, withURI, withoutURI)
}

func TestPatchSystem_HttpBootUri_NullOrEmptyClears(t *testing.T) {
	for _, value := range []string{`null`, `""`} {
		t.Run(value, func(t *testing.T) {
			mock := newMockMachine(qmp.StatusShutdown)
			mock.httpBootURI = "http://host/a.iso"
			srv := NewServer(mock, "", "", "")

			w := patchSystem(t, srv, `{"Boot":{"HttpBootUri":`+value+`}}`)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, "", mock.httpBootURI)
			boot, _ := getSystemBoot(t, srv)
			assert.Nil(t, boot["HttpBootUri"])
		})
	}
}

func TestPatchSystem_HttpBootUri_AbsentKeepsIt(t *testing.T) {
	mock := newMockMachine(qmp.StatusShutdown)
	mock.httpBootURI = "http://host/a.iso"
	srv := NewServer(mock, "", "", "")

	w := patchSystem(t, srv, `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Once"}}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "http://host/a.iso", mock.httpBootURI)
	assert.NotContains(t, mock.calls, "SetHTTPBootURI:")
}

func TestPatchSystem_HttpBootUri_Invalid(t *testing.T) {
	for _, value := range []string{`"ftp://host/a.iso"`, `"/vm/a.iso"`, `"http://"`, `42`, `{"a":1}`} {
		t.Run(value, func(t *testing.T) {
			mock := newMockMachine(qmp.StatusShutdown)
			srv := NewServer(mock, "", "", "")

			w := patchSystem(t, srv, `{"Boot":{"BootSourceOverrideTarget":"UefiHttp","BootSourceOverrideEnabled":"Once","HttpBootUri":`+value+`}}`)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, "None", mock.bootOverride.Target, "nothing changed")
			assert.Equal(t, "", mock.httpBootURI)
		})
	}
}

func TestPatchSystem_UefiHttp_LegacyMode(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	mock.legacy = true
	srv := NewServer(mock, "", "", "")

	w := patchSystem(t, srv, `{"Boot":{"BootSourceOverrideTarget":"UefiHttp","BootSourceOverrideEnabled":"Once"}}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "process management mode")
	assert.Equal(t, "None", mock.bootOverride.Target)

	w = patchSystem(t, srv, `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Once","HttpBootUri":"http://host/a.iso"}}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "None", mock.bootOverride.Target, "nothing changed")
	assert.Equal(t, "", mock.httpBootURI)

	// Clearing (as Ironic does after use) is accepted
	w = patchSystem(t, srv, `{"Boot":{"HttpBootUri":null}}`)
	assert.Equal(t, http.StatusOK, w.Code)
}
