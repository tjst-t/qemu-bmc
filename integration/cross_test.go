//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCross_IPMIPowerOff_RedfishVerify(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	// Power off via IPMI
	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "power", "off")
	require.NoError(t, err, "ipmitool output: %s", out)

	// Verify via Redfish
	require.NoError(t, waitForPowerState(client, "Off", 10*time.Second))

	// Restore power
	_, err = client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "On",
	})
	require.NoError(t, err)
	require.NoError(t, waitForPowerState(client, "On", 10*time.Second))
}

func TestCross_RedfishBoot_IPMIVerify(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)

	patchBody := map[string]any{
		"Boot": map[string]string{
			"BootSourceOverrideTarget":  "Pxe",
			"BootSourceOverrideEnabled": "Once",
		},
	}
	resp, err := client.Patch("/redfish/v1/Systems/1", patchBody)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "bootparam", "get", "5")
	require.NoError(t, err, "ipmitool output: %s", out)
	assert.True(t, strings.Contains(out, "PXE") || strings.Contains(out, "pxe"),
		"expected PXE in output: %s", out)
}
