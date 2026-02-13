//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIPMI_ChassisStatus(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "status")
	require.NoError(t, err, "ipmitool output: %s", out)
	assert.Contains(t, out, "System Power         : on")
}

func TestIPMI_PowerOffOn(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	// Power off
	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "power", "off")
	require.NoError(t, err, "ipmitool output: %s", out)

	require.NoError(t, waitForPowerState(client, "Off", 10*time.Second))

	// Verify via ipmitool
	out, err = runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "status")
	require.NoError(t, err, "ipmitool output: %s", out)
	assert.Contains(t, out, "System Power         : off")

	// Power on
	out, err = runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "power", "on")
	require.NoError(t, err, "ipmitool output: %s", out)

	require.NoError(t, waitForPowerState(client, "On", 10*time.Second))

	// Verify via ipmitool
	out, err = runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "status")
	require.NoError(t, err, "ipmitool output: %s", out)
	assert.Contains(t, out, "System Power         : on")
}

func TestIPMI_PowerCycle(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "power", "cycle")
	require.NoError(t, err, "ipmitool output: %s", out)

	require.NoError(t, waitForPowerState(client, "On", 10*time.Second))
}

func TestIPMI_BootDevice(t *testing.T) {
	env := loadTestEnv()

	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "bootdev", "pxe")
	require.NoError(t, err, "ipmitool output: %s", out)

	out, err = runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "bootparam", "get", "5")
	require.NoError(t, err, "ipmitool output: %s", out)
	assert.True(t, strings.Contains(out, "PXE") || strings.Contains(out, "pxe"),
		"expected PXE in output: %s", out)
}

func TestIPMI_LANPlus(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	out, err := runIPMIToolLANPlus(env.IPMIHost, env.User, env.Pass, "chassis", "status")
	require.NoError(t, err, "ipmitool lanplus output: %s", out)
	assert.Contains(t, out, "System Power         : on")

	out, err = runIPMIToolLANPlus(env.IPMIHost, env.User, env.Pass, "chassis", "bootdev", "pxe")
	require.NoError(t, err, "ipmitool lanplus output: %s", out)
}
