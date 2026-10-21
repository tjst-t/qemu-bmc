package machine

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// mockQMPClient implements qmp.Client for testing
type mockQMPClient struct {
	mu         sync.Mutex
	status     qmp.Status
	calls      []string
	connectErr error
	queryErr   error
}

func newMockQMPClient(status qmp.Status) *mockQMPClient {
	return &mockQMPClient{status: status}
}

func (m *mockQMPClient) Connect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "Connect")
	if m.connectErr != nil {
		return m.connectErr
	}
	return nil
}

func (m *mockQMPClient) QueryStatus() (qmp.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "QueryStatus")
	if m.queryErr != nil {
		return "", m.queryErr
	}
	return m.status, nil
}

func (m *mockQMPClient) SystemPowerdown() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "SystemPowerdown")
	return nil
}

func (m *mockQMPClient) SystemReset() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "SystemReset")
	if m.status == qmp.StatusShutdown {
		m.status = qmp.StatusPaused // like QEMU: reset leaves a shut-down VM paused
	}
	return nil
}

func (m *mockQMPClient) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "Stop")
	m.status = qmp.StatusPaused
	return nil
}

func (m *mockQMPClient) Cont() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "Cont")
	m.status = qmp.StatusRunning
	return nil
}

func (m *mockQMPClient) Quit() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "Quit")
	m.status = qmp.StatusShutdown
	return nil
}

func (m *mockQMPClient) BlockdevChangeMedium(device, filename string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "BlockdevChangeMedium")
	return nil
}

func (m *mockQMPClient) BlockdevRemoveMedium(device string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "BlockdevRemoveMedium")
	return nil
}

func (m *mockQMPClient) Close() error {
	return nil
}

func (m *mockQMPClient) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func (m *mockQMPClient) setStatus(status qmp.Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = status
}

// mockProcessManager implements ProcessManager for testing
type mockProcessManager struct {
	mu         sync.Mutex
	running    bool
	startCalls []string // boot targets passed to Start
	calls      []string
	exitCh     chan struct{}
}

func newMockProcessManager(running bool) *mockProcessManager {
	ch := make(chan struct{})
	if !running {
		close(ch)
	}
	return &mockProcessManager{
		running: running,
		exitCh:  ch,
	}
}

func (m *mockProcessManager) Start(bootTarget string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "Start")
	m.startCalls = append(m.startCalls, bootTarget)
	m.running = true
	m.exitCh = make(chan struct{})
	return nil
}

func (m *mockProcessManager) Stop(timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "Stop")
	m.running = false
	select {
	case <-m.exitCh:
	default:
		close(m.exitCh)
	}
	return nil
}

func (m *mockProcessManager) Kill() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "Kill")
	m.running = false
	select {
	case <-m.exitCh:
	default:
		close(m.exitCh)
	}
	return nil
}

func (m *mockProcessManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *mockProcessManager) WaitForExit(timeout time.Duration) error {
	m.mu.Lock()
	m.calls = append(m.calls, "WaitForExit")
	ch := m.exitCh
	m.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
		return errors.New("timeout")
	}
}

func (m *mockProcessManager) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func (m *mockProcessManager) StartCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.startCalls...)
}

// exit simulates QEMU exiting by itself (e.g. the guest powered off).
func (m *mockProcessManager) exit() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
	select {
	case <-m.exitCh:
	default:
		close(m.exitCh)
	}
}

// newFastMachine shortens the background GracefulRestart wait for tests.
func newFastMachine(m *Machine, timeout time.Duration) *Machine {
	m.gracefulTimeout = timeout
	m.pollInterval = 5 * time.Millisecond
	return m
}

// --- Legacy mode tests (existing) ---

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
	assert.Equal(t, []string{"SystemPowerdown"}, mock.Calls())

	// A guest ignoring ACPI keeps running: no forced stop
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state)

	// A guest honoring ACPI powers off (QEMU -no-shutdown)
	mock.setStatus(qmp.StatusShutdown)
	state, err = m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)
}

func TestReset_On_AfterGuestShutdown_ResetsBeforeCont(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusShutdown)
	m := New(mock)

	require.NoError(t, m.Reset("On"))
	calls := mock.Calls()
	assert.Equal(t, []string{"SystemReset", "Cont"}, calls[len(calls)-2:])
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state)
}

func TestReset_ForceRestart(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("ForceRestart")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "SystemReset")
}

func TestReset_GracefulRestart_GuestPowersOff(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)

	require.NoError(t, m.Reset("GracefulRestart"))
	assert.Equal(t, []string{"SystemPowerdown"}, mock.Calls())

	// Guest handles ACPI and powers off; the VM is then powered back on
	mock.setStatus(qmp.StatusShutdown)
	assert.Eventually(t, func() bool {
		calls := mock.Calls()
		return len(calls) >= 2 && calls[len(calls)-1] == "Cont"
	}, 2*time.Second, 5*time.Millisecond)
	assert.Contains(t, mock.Calls(), "SystemReset")
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state)
}

func TestReset_GracefulRestart_GuestIgnoresACPI(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 50*time.Millisecond)

	require.NoError(t, m.Reset("GracefulRestart"))
	time.Sleep(150 * time.Millisecond)

	for _, c := range mock.Calls() {
		assert.NotContains(t, []string{"Stop", "SystemReset", "Cont", "Quit"}, c)
	}
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state)
}

func TestReset_GracefulRestart_SupersededByForceOff(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)

	require.NoError(t, m.Reset("GracefulRestart"))
	require.NoError(t, m.Reset("ForceOff"))
	mock.setStatus(qmp.StatusShutdown)
	time.Sleep(100 * time.Millisecond)

	assert.NotContains(t, mock.Calls(), "Cont")
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)
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

// --- Process mode tests ---

func TestProcessMode_GetPowerState_ProcessNotRunning(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusShutdown)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)
	// Should not call QMP when process is not running
	assert.NotContains(t, qmpMock.Calls(), "QueryStatus")
}

func TestProcessMode_GetPowerState_ProcessRunning(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state)
}

func TestProcessMode_GetPowerState_ProcessRunning_QMPError(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.queryErr = errors.New("QMP not ready")
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state) // Process running = PowerOn even if QMP fails
}

func TestProcessMode_GetPowerState_GuestShutdown(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusShutdown)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)
	assert.Contains(t, pm.calls, "Stop") // Process should be stopped
}

func TestProcessMode_GetQMPStatus_ProcessNotRunning(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusShutdown)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	status, err := m.GetQMPStatus()
	require.NoError(t, err)
	assert.Equal(t, qmp.StatusShutdown, status)
}

func TestProcessMode_GetQMPStatus_ProcessRunning_QMPError(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.queryErr = errors.New("QMP not ready")
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	status, err := m.GetQMPStatus()
	require.NoError(t, err)
	assert.Equal(t, qmp.StatusRunning, status) // synthetic running status
}

func TestProcessMode_Reset_On_StartsProcess(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	err := m.Reset("On")
	require.NoError(t, err)
	assert.Contains(t, pm.calls, "Start")
	assert.Contains(t, qmpMock.Calls(), "Connect")
}

func TestProcessMode_Reset_On_AlreadyRunning_Noop(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	err := m.Reset("On")
	require.NoError(t, err)
	assert.NotContains(t, pm.calls, "Start")
}

func TestProcessMode_Reset_On_WithBootOverride(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"})
	err := m.Reset("On")
	require.NoError(t, err)

	require.Len(t, pm.startCalls, 1)
	assert.Equal(t, "Pxe", pm.startCalls[0])
}

func TestProcessMode_Reset_On_ConsumesBootOnce(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"})
	err := m.Reset("On")
	require.NoError(t, err)

	boot := m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "None", boot.Target)
}

func TestProcessMode_Reset_ForceOff_Quits(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	// Simulate the process exiting shortly after Quit is sent
	go func() {
		time.Sleep(50 * time.Millisecond)
		pm.exit()
	}()

	err := m.Reset("ForceOff")
	require.NoError(t, err)
	assert.Contains(t, qmpMock.Calls(), "Quit")
	assert.Contains(t, pm.calls, "WaitForExit")
}

func TestProcessMode_Reset_ForceRestart_SystemReset(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	err := m.Reset("ForceRestart")
	require.NoError(t, err)
	assert.Contains(t, qmpMock.Calls(), "SystemReset")
}

func TestProcessMode_Reset_ForceRestart_BootOnceRestartsProcess(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	// Power on with the default boot device
	require.NoError(t, m.Reset("On"))
	require.Equal(t, []string{"None"}, pm.startCalls)

	// Once Pxe, then reset while powered on: QEMU must be restarted with Pxe
	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"}))
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []string{"None", "Pxe"}, pm.startCalls)
	assert.Contains(t, pm.calls, "Stop")
	assert.NotContains(t, qmpMock.Calls(), "SystemReset")
	assert.True(t, pm.running)

	boot := m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "None", boot.Target)

	// Next reset must go back to the original boot device
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []string{"None", "Pxe", "None"}, pm.startCalls)
	assert.NotContains(t, qmpMock.Calls(), "SystemReset")

	// Further resets with no override change use system_reset
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []string{"None", "Pxe", "None"}, pm.startCalls)
	assert.Contains(t, qmpMock.Calls(), "SystemReset")
}

func TestProcessMode_Reset_ForceRestart_BootOnceSameTarget_ConsumedBySystemReset(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	// QEMU is running with Pxe (Once consumed at start)
	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"}))
	require.NoError(t, m.Reset("On"))
	require.Equal(t, []string{"Pxe"}, pm.startCalls)

	// Once Pxe again: same target, so system_reset boots PXE and consumes Once
	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"}))
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []string{"Pxe"}, pm.startCalls)
	assert.Contains(t, qmpMock.Calls(), "SystemReset")

	boot := m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "None", boot.Target)

	// Next reset must restart QEMU with the original boot device
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []string{"Pxe", "None"}, pm.startCalls)
}

func TestProcessMode_Reset_ForceRestart_ContinuousSameTarget_SystemReset(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Continuous", Target: "Pxe", Mode: "UEFI"}))
	require.NoError(t, m.Reset("On"))

	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []string{"Pxe"}, pm.startCalls)
	assert.Contains(t, qmpMock.Calls(), "SystemReset")
}

func TestProcessMode_Reset_On_DisabledIgnoresTarget(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Disabled", Target: "Pxe", Mode: "UEFI"}))
	require.NoError(t, m.Reset("On"))
	assert.Equal(t, []string{"None"}, pm.startCalls)

	// The stored setting is left as-is
	boot := m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "Pxe", boot.Target)
}

func TestProcessMode_Reset_On_ContinuousUsesTarget(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Continuous", Target: "Pxe", Mode: "UEFI"}))
	require.NoError(t, m.Reset("On"))
	assert.Equal(t, []string{"Pxe"}, pm.startCalls)

	boot := m.GetBootOverride()
	assert.Equal(t, "Continuous", boot.Enabled)
	assert.Equal(t, "Pxe", boot.Target)
}

func TestProcessMode_Reset_ForceRestart_DisabledTargetUsesSystemReset(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	// Running with the default boot device
	require.NoError(t, m.Reset("On"))
	require.Equal(t, []string{"None"}, pm.startCalls)

	// Disabled with a target is not an override: no QEMU restart with Pxe
	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Disabled", Target: "Pxe", Mode: "UEFI"}))
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []string{"None"}, pm.startCalls)
	assert.Contains(t, qmpMock.Calls(), "SystemReset")
}

func TestProcessMode_Reset_ForceRestart_DisabledAfterContinuousRestartsWithDefault(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Continuous", Target: "Pxe", Mode: "UEFI"}))
	require.NoError(t, m.Reset("On"))
	require.Equal(t, []string{"Pxe"}, pm.startCalls)

	// Turning the override off (target kept) must restart with the default
	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Disabled", Target: "Pxe", Mode: "UEFI"}))
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []string{"Pxe", "None"}, pm.startCalls)
	assert.NotContains(t, qmpMock.Calls(), "SystemReset")
}

func TestProcessMode_Reset_GracefulRestart_DisabledIgnoresTarget(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := newFastMachine(NewWithProcess(qmpMock, pm), 5*time.Second)

	require.NoError(t, m.Reset("On"))
	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Disabled", Target: "Pxe", Mode: "UEFI"}))

	require.NoError(t, m.Reset("GracefulRestart"))
	// Simulate the guest powering off after the ACPI request
	pm.exit()
	assert.Eventually(t, func() bool {
		return len(pm.StartCalls()) == 2
	}, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"None", "None"}, pm.StartCalls())
}

func TestProcessMode_Reset_GracefulShutdown_ReturnsWithoutWaiting(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	start := time.Now()
	require.NoError(t, m.Reset("GracefulShutdown"))
	assert.Less(t, time.Since(start), time.Second)
	assert.Equal(t, []string{"SystemPowerdown"}, qmpMock.Calls())
	assert.Empty(t, pm.Calls()) // no wait, no Stop/Kill

	// Guest ignoring ACPI stays on
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state)

	// Guest honoring ACPI: QEMU exits by itself
	pm.exit()
	state, err = m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)
}

func TestProcessMode_Reset_GracefulRestart_GuestPowersOff(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := newFastMachine(NewWithProcess(qmpMock, pm), 5*time.Second)
	require.NoError(t, m.Reset("On"))

	start := time.Now()
	require.NoError(t, m.Reset("GracefulRestart"))
	assert.Less(t, time.Since(start), time.Second)
	assert.Contains(t, qmpMock.Calls(), "SystemPowerdown")
	assert.Equal(t, []string{"None"}, pm.StartCalls())

	pm.exit()
	assert.Eventually(t, func() bool {
		return len(pm.StartCalls()) == 2 && pm.IsRunning()
	}, 2*time.Second, 5*time.Millisecond)
	assert.NotContains(t, pm.Calls(), "Kill")
}

func TestProcessMode_Reset_GracefulRestart_NoShutdownStatus(t *testing.T) {
	// With -no-shutdown QEMU stays alive in "shutdown" status
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := newFastMachine(NewWithProcess(qmpMock, pm), 5*time.Second)
	require.NoError(t, m.Reset("On"))

	require.NoError(t, m.Reset("GracefulRestart"))
	qmpMock.setStatus(qmp.StatusShutdown)
	assert.Eventually(t, func() bool {
		return len(pm.StartCalls()) == 2
	}, 2*time.Second, 5*time.Millisecond)
	assert.Contains(t, pm.Calls(), "Stop")
}

func TestProcessMode_Reset_GracefulRestart_GuestIgnoresACPI(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := newFastMachine(NewWithProcess(qmpMock, pm), 50*time.Millisecond)
	require.NoError(t, m.Reset("On"))

	require.NoError(t, m.Reset("GracefulRestart"))
	time.Sleep(150 * time.Millisecond)

	assert.NotContains(t, pm.Calls(), "Stop")
	assert.NotContains(t, pm.Calls(), "Kill")
	assert.Equal(t, []string{"None"}, pm.StartCalls())
	assert.True(t, pm.IsRunning())
}

func TestProcessMode_Reset_GracefulRestart_SupersededByForceOff(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := newFastMachine(NewWithProcess(qmpMock, pm), 5*time.Second)
	require.NoError(t, m.Reset("On"))

	require.NoError(t, m.Reset("GracefulRestart"))
	go func() {
		time.Sleep(20 * time.Millisecond)
		pm.exit()
	}()
	require.NoError(t, m.Reset("ForceOff"))
	time.Sleep(100 * time.Millisecond)

	// The user's ForceOff wins: the VM is not powered back on
	assert.Equal(t, []string{"None"}, pm.StartCalls())
	assert.False(t, pm.IsRunning())
}

func TestLegacyMode_Reset_ForceOff_StopsQMP(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("ForceOff")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "Stop")
}
