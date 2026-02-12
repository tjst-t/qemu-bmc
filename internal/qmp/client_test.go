package qmp

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_QueryStatus_PowerOn(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
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
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
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
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
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
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
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
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
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
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.BlockdevChangeMedium("ide0-cd0", "/path/to/image.iso")
	require.NoError(t, err)
	assert.Equal(t, "blockdev-change-medium", mockQMP.LastCommand())
}

func TestClient_BlockdevRemoveMedium(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
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
