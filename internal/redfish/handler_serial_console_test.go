package redfish

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func TestSerialConsole_IPMI(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	srv.SetIPMISerialConsole(623)

	mgr := getResource(t, srv, "/redfish/v1/Managers/1")
	assert.Equal(t, map[string]interface{}{
		"ServiceEnabled":        true,
		"MaxConcurrentSessions": float64(1),
		"ConnectTypesSupported": []interface{}{"IPMI"},
	}, mgr["SerialConsole"])

	sys := getResource(t, srv, "/redfish/v1/Systems/1")
	assert.Equal(t, "#ComputerSystem.v1_13_0.ComputerSystem", sys["@odata.type"], "SerialConsole needs v1_13_0")
	assert.Equal(t, map[string]interface{}{
		"MaxConcurrentSessions": float64(1),
		"IPMI": map[string]interface{}{
			"ServiceEnabled":        true,
			"Port":                  float64(623),
			"HotKeySequenceDisplay": "Press ~. to exit the console (ipmitool)",
		},
	}, sys["SerialConsole"])
}

func TestSerialConsole_OmittedWithoutSOL(t *testing.T) {
	// Legacy mode: the external QEMU owns its serial console
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	assert.NotContains(t, getResource(t, srv, "/redfish/v1/Managers/1"), "SerialConsole")
	assert.NotContains(t, getResource(t, srv, "/redfish/v1/Systems/1"), "SerialConsole")
}
