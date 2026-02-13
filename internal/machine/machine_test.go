package machine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// mockQMPClient implements qmp.Client for testing
type mockQMPClient struct {
	status qmp.Status
	calls  []string
}

func newMockQMPClient(status qmp.Status) *mockQMPClient {
	return &mockQMPClient{status: status}
}

func (m *mockQMPClient) QueryStatus() (qmp.Status, error) {
	m.calls = append(m.calls, "QueryStatus")
	return m.status, nil
}

func (m *mockQMPClient) SystemPowerdown() error {
	m.calls = append(m.calls, "SystemPowerdown")
	return nil
}

func (m *mockQMPClient) SystemReset() error {
	m.calls = append(m.calls, "SystemReset")
	return nil
}

func (m *mockQMPClient) Stop() error {
	m.calls = append(m.calls, "Stop")
	m.status = qmp.StatusPaused
	return nil
}

func (m *mockQMPClient) Cont() error {
	m.calls = append(m.calls, "Cont")
	m.status = qmp.StatusRunning
	return nil
}

func (m *mockQMPClient) Quit() error {
	m.calls = append(m.calls, "Quit")
	m.status = qmp.StatusShutdown
	return nil
}

func (m *mockQMPClient) BlockdevChangeMedium(device, filename string) error {
	m.calls = append(m.calls, "BlockdevChangeMedium")
	return nil
}

func (m *mockQMPClient) BlockdevRemoveMedium(device string) error {
	m.calls = append(m.calls, "BlockdevRemoveMedium")
	return nil
}

func (m *mockQMPClient) Close() error {
	return nil
}

func (m *mockQMPClient) Calls() []string {
	return m.calls
}

func TestGetPowerState_Running(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state)
}

func TestGetPowerState_Shutdown(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusShutdown)
	m := New(mock)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)
}

func TestReset_ForceOff(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("ForceOff")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "Stop")
}

func TestReset_GracefulShutdown(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("GracefulShutdown")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "SystemPowerdown")
}

func TestReset_ForceRestart(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("ForceRestart")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "SystemReset")
}

func TestReset_GracefulRestart(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("GracefulRestart")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "SystemReset")
}

func TestReset_InvalidType(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("BadType")
	assert.Error(t, err)
}

func TestBootOverride(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	// Default should be Disabled
	boot := m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "None", boot.Target)

	// Set PXE Once
	err := m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"})
	require.NoError(t, err)

	boot = m.GetBootOverride()
	assert.Equal(t, "Once", boot.Enabled)
	assert.Equal(t, "Pxe", boot.Target)

	// Consume boot once
	m.ConsumeBootOnce()
	boot = m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "None", boot.Target)
}

func TestBootOverride_InvalidTarget(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Invalid", Mode: "UEFI"})
	assert.Error(t, err)
}

func TestInsertMedia(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.InsertMedia("http://example.com/boot.iso")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "BlockdevChangeMedium")
}

func TestEjectMedia(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.EjectMedia()
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "BlockdevRemoveMedium")
}
