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

func TestGetSystem_ManagedByLink(t *testing.T) {
	// Ironic's redfish inspect interface requires Links/ManagedBy to point at the
	// managing BMC (Managers/1); without it, inspection refuses to start.
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var system ComputerSystem
	err := json.Unmarshal(w.Body.Bytes(), &system)
	require.NoError(t, err)

	require.Len(t, system.Links.ManagedBy, 1)
	assert.Equal(t, "/redfish/v1/Managers/1", system.Links.ManagedBy[0].ODataID)
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

func TestGetSystem_Inventory(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")
	srv.SetInventory(testInventory)

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var system ComputerSystem
	err := json.Unmarshal(w.Body.Bytes(), &system)
	require.NoError(t, err)

	assert.Equal(t, "sku-1", system.SKU)
	assert.Equal(t, "1.0.0", system.BiosVersion)
	assert.Equal(t, "Off", system.IndicatorLED)
	assert.InDelta(t, 2.0, system.MemorySummary.TotalSystemMemoryGiB, 0.001)
	assert.Equal(t, "/redfish/v1/Systems/1/Processors", system.Processors.ODataID)
}

func TestPatchIndicatorLED(t *testing.T) {
	t.Run("IndicatorLED alone succeeds and persists", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		body := `{"IndicatorLED":"Lit"}`
		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var system ComputerSystem
		err := json.Unmarshal(w.Body.Bytes(), &system)
		require.NoError(t, err)
		assert.Equal(t, "Lit", system.IndicatorLED)

		getReq := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
		w2 := httptest.NewRecorder()
		srv.ServeHTTP(w2, getReq)
		var system2 ComputerSystem
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &system2))
		assert.Equal(t, "Lit", system2.IndicatorLED)
	})

	t.Run("neither Boot nor IndicatorLED returns 400", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
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

func getSystem(t *testing.T, srv *Server) ComputerSystem {
	t.Helper()
	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var system ComputerSystem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &system))
	return system
}

func patchSystem(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestBootOrder(t *testing.T) {
	t.Run("GET returns the seeded persistent boot order", func(t *testing.T) {
		srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
		assert.Equal(t, []string{"Pxe", "Hdd", "Cd"}, getSystem(t, srv).Boot.BootOrder)
	})

	t.Run("PATCH BootOrder (gofish SetBoot shape) persists the new order", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		// Exactly what metal-operator's SetBootOrder -> gofish SetBoot emits.
		body := `{"Boot":{"BootSourceOverrideEnabled":"Continuous","BootSourceOverrideTarget":"None","BootOrder":["Hdd","Pxe","Cd"]}}`
		w := patchSystem(t, srv, body)
		assert.Equal(t, http.StatusOK, w.Code)

		assert.Equal(t, []string{"Hdd", "Pxe", "Cd"}, getSystem(t, srv).Boot.BootOrder)
		// Boot-order-only: the accompanying Continuous/None override is ignored,
		// so no spurious one-time override lands on the machine.
		assert.NotEqual(t, "Continuous", mock.bootOverride.Enabled)
	})

	t.Run("one-time override PATCH leaves BootOrder untouched", func(t *testing.T) {
		srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

		w := patchSystem(t, srv, `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Once"}}`)
		assert.Equal(t, http.StatusOK, w.Code)

		sys := getSystem(t, srv)
		assert.Equal(t, "Pxe", sys.Boot.BootSourceOverrideTarget)
		assert.Equal(t, []string{"Pxe", "Hdd", "Cd"}, sys.Boot.BootOrder)
	})

	t.Run("changing the boot order changes the system ETag", func(t *testing.T) {
		srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

		before := getSystem(t, srv).ODataEtag
		require.NotEmpty(t, before)

		patchSystem(t, srv, `{"Boot":{"BootOrder":["Cd","Hdd","Pxe"]}}`)

		assert.NotEqual(t, before, getSystem(t, srv).ODataEtag)
	})

	t.Run("boot order survives a subsequent reset action (persistent, not one-time)", func(t *testing.T) {
		srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

		patchSystem(t, srv, `{"Boot":{"BootOrder":["Cd","Hdd","Pxe"]}}`)

		reset := httptest.NewRequest("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", strings.NewReader(`{"ResetType":"PowerCycle"}`))
		reset.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		srv.ServeHTTP(rw, reset)
		require.Equal(t, http.StatusNoContent, rw.Code)

		assert.Equal(t, []string{"Cd", "Hdd", "Pxe"}, getSystem(t, srv).Boot.BootOrder)
	})
}
