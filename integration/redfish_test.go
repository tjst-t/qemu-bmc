//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedfish_ServiceRoot(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)

	resp, err := client.Get("/redfish/v1")
	require.NoError(t, err)
	data, err := readJSON(resp)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "#ServiceRoot.v1_5_0.ServiceRoot", data["@odata.type"])
	assert.Equal(t, "/redfish/v1", data["@odata.id"])
	assert.NotEmpty(t, data["Name"])
}

func TestRedfish_BasicAuth(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)

	resp, err := client.GetNoAuth("/redfish/v1/Systems")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestRedfish_PowerState(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	resp, err := client.Get("/redfish/v1/Systems/1")
	require.NoError(t, err)
	data, err := readJSON(resp)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "On", data["PowerState"])
	assert.Equal(t, "QEMU Virtual Machine", data["Name"])
}

func TestRedfish_PowerOffOn(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	// GracefulShutdown only sends the ACPI power button event and returns at
	// once. This QEMU has no guest OS to honor it, so the VM stays on.
	start := time.Now()
	resp, err := client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "GracefulShutdown",
	})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Less(t, time.Since(start), 5*time.Second)
	time.Sleep(2 * time.Second)
	require.NoError(t, waitForPowerState(client, "On", time.Second))

	// ForceOff
	resp, err = client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "ForceOff",
	})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	require.NoError(t, waitForPowerState(client, "Off", 10*time.Second))

	// Power on
	resp, err = client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "On",
	})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	require.NoError(t, waitForPowerState(client, "On", 10*time.Second))
}

func TestRedfish_ForceRestart(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	resp, err := client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "ForceRestart",
	})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	require.NoError(t, waitForPowerState(client, "On", 10*time.Second))
}

func TestRedfish_BootOverride(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	// Get current ETag
	resp, err := client.Get("/redfish/v1/Systems/1")
	require.NoError(t, err)
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	require.NotEmpty(t, etag)

	// PATCH boot override
	patchBody := map[string]any{
		"Boot": map[string]string{
			"BootSourceOverrideTarget":  "Pxe",
			"BootSourceOverrideEnabled": "Once",
		},
	}
	resp, err = client.PatchWithETag("/redfish/v1/Systems/1", patchBody, etag)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify
	resp, err = client.Get("/redfish/v1/Systems/1")
	require.NoError(t, err)
	data, err := readJSON(resp)
	require.NoError(t, err)

	boot, ok := data["Boot"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Pxe", boot["BootSourceOverrideTarget"])
	assert.Equal(t, "Once", boot["BootSourceOverrideEnabled"])
}

func TestRedfish_VirtualMedia(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)

	// Insert media (a path in the external QEMU container)
	resp, err := client.Post(
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
		map[string]string{"Image": "/etc/hostname"},
	)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Verify inserted
	resp, err = client.Get("/redfish/v1/Managers/1/VirtualMedia/CD1")
	require.NoError(t, err)
	data, err := readJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, true, data["Inserted"])
	assert.Equal(t, "/etc/hostname", data["Image"])
	assert.Equal(t, "hostname", data["ImageName"])

	// Eject media
	resp, err = client.Post(
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia",
		map[string]string{},
	)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Verify ejected
	resp, err = client.Get("/redfish/v1/Managers/1/VirtualMedia/CD1")
	require.NoError(t, err)
	data, err = readJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, false, data["Inserted"])
}
