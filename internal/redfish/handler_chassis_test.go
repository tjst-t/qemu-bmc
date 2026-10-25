package redfish

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func TestGetChassisCollection(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Chassis", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var col ChassisCollection
	err := json.Unmarshal(w.Body.Bytes(), &col)
	require.NoError(t, err)
	assert.Equal(t, 1, col.MembersCount)
	assert.Equal(t, "/redfish/v1/Chassis/1", col.Members[0].ODataID)
}

func TestGetChassis(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Chassis/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var chassis Chassis
	err := json.Unmarshal(w.Body.Bytes(), &chassis)
	require.NoError(t, err)
	assert.Equal(t, "#Chassis.v1_14_0.Chassis", chassis.ODataType)
	assert.Equal(t, "RackMount", chassis.ChassisType)
}

func TestGetChassis_LinksAndStatus(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	chassis := getResource(t, srv, "/redfish/v1/Chassis/1")
	assert.Equal(t, map[string]interface{}{"State": "Enabled", "Health": "OK"}, chassis["Status"])
	assert.Equal(t, map[string]interface{}{
		"ComputerSystems": []interface{}{map[string]interface{}{"@odata.id": "/redfish/v1/Systems/1"}},
		"ManagedBy":       []interface{}{map[string]interface{}{"@odata.id": "/redfish/v1/Managers/1"}},
	}, chassis["Links"])
}

func TestGetChassis_PowerState(t *testing.T) {
	tests := []struct {
		name       string
		qmpStatus  qmp.Status
		powerState machine.PowerState
		expected   string
	}{
		{"running", qmp.StatusRunning, "", "On"},
		{"shutdown", qmp.StatusShutdown, "", "Off"},
		{"powering off", qmp.StatusRunning, machine.PoweringOff, "PoweringOff"},
		{"powering on", qmp.StatusShutdown, machine.PoweringOn, "PoweringOn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockMachine(tt.qmpStatus)
			if tt.powerState != "" {
				mock.powerState = tt.powerState
			}
			srv := NewServer(mock, "", "", "")

			chassis := getResource(t, srv, "/redfish/v1/Chassis/1")
			assert.Equal(t, tt.expected, chassis["PowerState"])
			system := getResource(t, srv, "/redfish/v1/Systems/1")
			assert.Equal(t, system["PowerState"], chassis["PowerState"])
		})
	}
}

func TestGetChassis_PowerStateError(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	mock.statusErr = errors.New("QMP down")
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Chassis/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetChassis_Identity(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	srv.SetSystemInfo(SystemInfo{Manufacturer: "QEMU", Model: "qemu-bmc", SerialNumber: "QBMC-0123456789", SKU: "SKU-1"})

	chassis := getResource(t, srv, "/redfish/v1/Chassis/1")
	assert.Equal(t, "QEMU", chassis["Manufacturer"])
	assert.Equal(t, "qemu-bmc", chassis["Model"])
	assert.Equal(t, "QBMC-0123456789", chassis["SerialNumber"])
	assert.NotContains(t, chassis, "SKU")
}
