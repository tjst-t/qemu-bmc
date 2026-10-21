package qmp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_QueryStatus_PowerOn(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	mockQMP.SetStatus(StatusRunning)
	defer mockQMP.Close()

	// Give the server a moment to start listening
	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	status, err := client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, status)
}

func TestClient_QueryStatus_PowerOff(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	mockQMP.SetStatus(StatusShutdown)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	status, err := client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusShutdown, status)
}

func TestClient_SystemPowerdown(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.SystemPowerdown()
	require.NoError(t, err)
	assert.Equal(t, "system_powerdown", mockQMP.LastCommand())
}

func TestClient_SystemReset(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.SystemReset()
	require.NoError(t, err)
	assert.Equal(t, "system_reset", mockQMP.LastCommand())
}

func TestClient_Quit(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.Quit()
	require.NoError(t, err)
	assert.Equal(t, "quit", mockQMP.LastCommand())
}

func TestClient_BlockdevChangeMedium(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.BlockdevChangeMedium("ide0-cd0", "/path/to/image.iso", "raw")
	require.NoError(t, err)
	assert.Equal(t, "blockdev-change-medium", mockQMP.LastCommand())
	assert.Equal(t, map[string]interface{}{
		"id": "ide0-cd0", "filename": "/path/to/image.iso", "format": "raw",
	}, mockQMP.LastArgs())
}

func TestClient_BlockdevOpenCloseTray(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, client.BlockdevOpenTray("ide2-cd0", true))
	assert.Equal(t, "blockdev-open-tray", mockQMP.LastCommand())
	assert.Equal(t, map[string]interface{}{"id": "ide2-cd0", "force": true}, mockQMP.LastArgs())

	require.NoError(t, client.BlockdevCloseTray("ide2-cd0"))
	assert.Equal(t, "blockdev-close-tray", mockQMP.LastCommand())
	assert.Equal(t, map[string]interface{}{"id": "ide2-cd0"}, mockQMP.LastArgs())
}

func TestClient_QueryBlock(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()
	mockQMP.SetBlocks([]BlockInfo{
		{Device: "virtio0", QDev: "/machine/peripheral-anon/device[0]/virtio-backend",
			Inserted: &BlockInserted{File: "/vm/disk.qcow2"}},
		{Device: "ide2-cd0", QDev: "/machine/unattached/device[23]", Removable: true,
			Inserted: &BlockInserted{File: "/iso/a.iso", ReadOnly: true}},
	})

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	blocks, err := client.QueryBlock()
	require.NoError(t, err)
	assert.Equal(t, "query-block", mockQMP.LastCommand())
	require.Len(t, blocks, 2)
	assert.False(t, blocks[0].Removable)
	assert.Equal(t, "ide2-cd0", blocks[1].Device)
	assert.Equal(t, "/machine/unattached/device[23]", blocks[1].QDev)
	assert.True(t, blocks[1].Removable)
	require.NotNil(t, blocks[1].Inserted)
	assert.Equal(t, "/iso/a.iso", blocks[1].Inserted.File)
	assert.True(t, blocks[1].Inserted.ReadOnly)
}

func TestClient_BlockdevRemoveMedium(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.BlockdevRemoveMedium("ide0-cd0")
	require.NoError(t, err)
	assert.Equal(t, "blockdev-remove-medium", mockQMP.LastCommand())
}

func TestNewDisconnectedClient_NotConnected(t *testing.T) {
	client := NewDisconnectedClient("/tmp/nonexistent.sock")
	defer client.Close()

	_, err := client.QueryStatus()
	assert.ErrorIs(t, err, ErrNotConnected)
}

func TestDisconnectedClient_Connect(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	mockQMP.SetStatus(StatusRunning)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client := NewDisconnectedClient(socketPath)
	defer client.Close()

	// Should fail before connect
	_, err := client.QueryStatus()
	assert.ErrorIs(t, err, ErrNotConnected)

	// Connect
	err = client.Connect()
	require.NoError(t, err)

	// Should work after connect
	status, err := client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, status)
}

func TestClient_Close_ThenConnect(t *testing.T) {
	socketPath := testSocketPath(t)
	mockQMP := newMockQMPServer(t, socketPath)
	mockQMP.SetStatus(StatusRunning)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)

	// Verify connected
	status, err := client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, status)

	// Close
	err = client.Close()
	require.NoError(t, err)

	// Reconnect
	err = client.Connect()
	require.NoError(t, err)

	// Should work again
	status, err = client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, status)

	client.Close()
}
