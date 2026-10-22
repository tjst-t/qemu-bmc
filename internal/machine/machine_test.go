package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	blocks     []qmp.BlockInfo
	mediumErr  error
	mediumArgs []string // "<command> <id> [<filename>]" of medium commands
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

func (m *mockQMPClient) InjectNMI() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "InjectNMI")
	return nil
}

func (m *mockQMPClient) QueryBlock() ([]qmp.BlockInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "QueryBlock")
	return append([]qmp.BlockInfo(nil), m.blocks...), nil
}

func (m *mockQMPClient) BlockdevChangeMedium(id, filename, format string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "BlockdevChangeMedium")
	m.mediumArgs = append(m.mediumArgs, "change "+id+" "+filename+" "+format)
	return m.mediumErr
}

func (m *mockQMPClient) BlockdevOpenTray(id string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "BlockdevOpenTray")
	m.mediumArgs = append(m.mediumArgs, fmt.Sprintf("open %s force=%v", id, force))
	return m.mediumErr
}

func (m *mockQMPClient) BlockdevCloseTray(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "BlockdevCloseTray")
	m.mediumArgs = append(m.mediumArgs, "close "+id)
	return m.mediumErr
}

func (m *mockQMPClient) BlockdevRemoveMedium(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "BlockdevRemoveMedium")
	m.mediumArgs = append(m.mediumArgs, "remove "+id)
	return m.mediumErr
}

func (m *mockQMPClient) MediumArgs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.mediumArgs...)
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
	startMedia []string // CD images passed to Start
	calls      []string
	exitCh     chan struct{}
	startGate  chan struct{} // if set, Start blocks until it is closed
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

func (m *mockProcessManager) Start(bootTarget, cdImage string) error {
	m.mu.Lock()
	gate := m.startGate
	m.mu.Unlock()
	if gate != nil {
		<-gate
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "Start")
	m.startCalls = append(m.startCalls, bootTarget)
	m.startMedia = append(m.startMedia, cdImage)
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

	// Until the guest powers off, the VM is powering off (never forced)
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PoweringOff, state)

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

// q35 with -cdrom: a virtio disk plus the removable ide2-cd0
var q35Blocks = []qmp.BlockInfo{
	{Device: "virtio0", QDev: "/machine/peripheral-anon/device[1]/virtio-backend",
		Inserted: &qmp.BlockInserted{File: "/vm/disk.qcow2"}},
	{Device: "floppy0", QDev: "/machine/unattached/device[20]", Removable: true},
	{Device: "ide2-cd0", QDev: "/machine/unattached/device[23]", Removable: true},
}

func testISO(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "boot.iso")
	require.NoError(t, os.WriteFile(path, []byte("iso"), 0o644))
	return path
}

func TestInsertMedia_Legacy_UsesQueryBlock(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	mock.blocks = q35Blocks
	m := New(mock)

	// Legacy mode: the path is in the external QEMU's filesystem
	require.NoError(t, m.InsertMedia("/iso/boot.iso", "", ""))
	assert.Equal(t, []string{"change /machine/unattached/device[23] /iso/boot.iso raw"}, mock.MediumArgs())
	assert.Equal(t, Media{Image: "/iso/boot.iso", Inserted: true}, m.GetMedia())
}

func TestInsertMedia_Legacy_QMPError(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	mock.blocks = q35Blocks
	mock.mediumErr = errors.New("Could not open")
	m := New(mock)

	assert.Error(t, m.InsertMedia("/iso/boot.iso", "", ""))
	assert.False(t, m.GetMedia().Inserted)
}

func TestInsertMedia_NoCDDrive(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	mock.blocks = q35Blocks[:1]
	m := New(mock)

	assert.Error(t, m.InsertMedia("/iso/boot.iso", "", ""))
	assert.Empty(t, mock.MediumArgs())
}

func TestInsertMedia_UnsupportedImage(t *testing.T) {
	m := New(newMockQMPClient(qmp.StatusRunning))
	for _, image := range []string{"boot.iso", "http://", "ftp://example.com/boot.iso", "file://host/boot.iso", "file:boot.iso"} {
		err := m.InsertMedia(image, "", "")
		assert.ErrorIs(t, err, ErrInvalidMedia, image)
	}
}

func TestInsertMedia_PoweredOff_KeptAndAttachedOnStart(t *testing.T) {
	iso := testISO(t)
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	require.NoError(t, m.InsertMedia("file://"+iso, "", ""))
	assert.Empty(t, qmpMock.Calls(), "no QMP while QEMU is not running")
	assert.Equal(t, Media{Image: "file://" + iso, Inserted: true}, m.GetMedia())

	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Cd", Mode: "Legacy"}))
	qmpMock.blocks = []qmp.BlockInfo{{Device: "vmedia0", QDev: "vmedia-cd0", Removable: true,
		Inserted: &qmp.BlockInserted{File: iso}}}
	require.NoError(t, m.Reset("On"))
	assert.Equal(t, []string{"Cd"}, pm.StartCalls())
	assert.Equal(t, []string{iso}, pm.startMedia)
	assert.Empty(t, qmpMock.MediumArgs(), "the drive already holds the image")
}

func TestInsertMedia_PoweredOff_MissingFile(t *testing.T) {
	m := NewWithProcess(newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))

	err := m.InsertMedia("/nonexistent/boot.iso", "", "")
	assert.ErrorIs(t, err, ErrInvalidMedia)
	err = m.InsertMedia(t.TempDir(), "", "")
	assert.ErrorIs(t, err, ErrInvalidMedia, "directory")
	assert.False(t, m.GetMedia().Inserted)
}

func TestInsertMedia_Running_ChangesMedium(t *testing.T) {
	iso := testISO(t)
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.blocks = []qmp.BlockInfo{{Device: "vmedia0", QDev: "vmedia-cd0", Removable: true}}
	m := NewWithProcess(qmpMock, newMockProcessManager(true))

	require.NoError(t, m.InsertMedia(iso, "", ""))
	assert.Equal(t, []string{"change vmedia-cd0 " + iso + " raw"}, qmpMock.MediumArgs())
	assert.True(t, m.GetMedia().Inserted)
}

func TestEjectMedia_Running(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.blocks = q35Blocks
	m := NewWithProcess(qmpMock, newMockProcessManager(true))
	m.SetInitialMedia("/iso/boot.iso")

	require.NoError(t, m.EjectMedia())
	id := "/machine/unattached/device[23]"
	assert.Equal(t, []string{"open " + id + " force=true", "remove " + id, "close " + id}, qmpMock.MediumArgs())
	assert.Equal(t, Media{}, m.GetMedia())
}

func TestEjectMedia_PoweredOff(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)
	m.SetInitialMedia("/iso/boot.iso")
	assert.Equal(t, Media{Image: "/iso/boot.iso", Inserted: true}, m.GetMedia())

	require.NoError(t, m.EjectMedia())
	assert.Empty(t, qmpMock.Calls())
	assert.False(t, m.GetMedia().Inserted)

	require.NoError(t, m.Reset("On"))
	assert.Equal(t, []string{""}, pm.startMedia)
}

func TestPowerOn_SyncsUserCDDrive(t *testing.T) {
	iso := testISO(t)
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	// A user-defined CD drive does not get the image on the command line
	qmpMock.blocks = []qmp.BlockInfo{{Device: "cd", QDev: "mycd", Removable: true}}
	m := NewWithProcess(qmpMock, newMockProcessManager(false))
	require.NoError(t, m.InsertMedia(iso, "", ""))

	require.NoError(t, m.Reset("On"))
	assert.Equal(t, []string{"change mycd " + iso + " raw"}, qmpMock.MediumArgs())
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
	// The background watcher may already have polled QueryStatus
	calls := qmpMock.Calls()
	require.NotEmpty(t, calls)
	assert.Equal(t, "SystemPowerdown", calls[0])
	for _, c := range calls[1:] {
		assert.Equal(t, "QueryStatus", c)
	}
	assert.Empty(t, pm.Calls()) // no wait, no Stop/Kill

	// Powering off until the guest honors ACPI
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PoweringOff, state)

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

func TestSetBootMode(t *testing.T) {
	m := NewWithProcess(newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))
	assert.Equal(t, "UEFI", m.GetBootOverride().Mode)

	require.NoError(t, m.SetBootMode("Legacy"))
	b := m.GetBootOverride()
	assert.Equal(t, "Legacy", b.Mode)
	assert.Equal(t, "Disabled", b.Enabled)
	assert.Equal(t, "None", b.Target)

	assert.Error(t, m.SetBootMode("bios"))
	assert.Equal(t, "Legacy", m.GetBootOverride().Mode)
}

// --- Graceful power state transitions ---

func powerState(t *testing.T, m *Machine) PowerState {
	t.Helper()
	state, err := m.GetPowerState()
	require.NoError(t, err)
	return state
}

func TestReset_GracefulShutdown_PoweringOffThenOff(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)

	require.NoError(t, m.Reset("GracefulShutdown"))
	assert.Equal(t, PoweringOff, powerState(t, m))

	mock.setStatus(qmp.StatusShutdown)
	assert.Equal(t, PowerOff, powerState(t, m))
	// The watcher ends the transition; the state stays Off
	assert.Eventually(t, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return m.transition == ""
	}, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, PowerOff, powerState(t, m))
}

func TestReset_GracefulShutdown_TimeoutBackToOn(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 50*time.Millisecond)

	require.NoError(t, m.Reset("GracefulShutdown"))
	assert.Equal(t, PoweringOff, powerState(t, m))

	assert.Eventually(t, func() bool {
		return powerState(t, m) == PowerOn
	}, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"SystemPowerdown"}, filterPowerCalls(mock.Calls()))

	// A guest powering off later is still reported, as Off
	mock.setStatus(qmp.StatusShutdown)
	assert.Equal(t, PowerOff, powerState(t, m))
}

func TestReset_GracefulShutdown_SupersededByForceOff(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)

	require.NoError(t, m.Reset("GracefulShutdown"))
	require.NoError(t, m.Reset("ForceOff"))
	assert.Equal(t, PowerOff, powerState(t, m))
}

func TestReset_GracefulShutdown_SupersededByOn(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)

	require.NoError(t, m.Reset("GracefulShutdown"))
	require.NoError(t, m.Reset("On"))
	assert.Equal(t, PowerOn, powerState(t, m))
}

func TestReset_GracefulRestart_PoweringOffOnOn(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)

	require.NoError(t, m.Reset("GracefulRestart"))
	assert.Equal(t, PoweringOff, powerState(t, m))

	// Guest off: the VM is about to be powered back on. Hold resetMu so
	// the watcher cannot power it on before the state is checked.
	m.resetMu.Lock()
	mock.setStatus(qmp.StatusShutdown)
	assert.Equal(t, PoweringOn, powerState(t, m))
	m.resetMu.Unlock()

	assert.Eventually(t, func() bool {
		return powerState(t, m) == PowerOn
	}, 2*time.Second, 5*time.Millisecond)
	m.mu.RLock()
	assert.Equal(t, PowerState(""), m.transition)
	m.mu.RUnlock()
}

func TestReset_GracefulRestart_TimeoutBackToOn(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 50*time.Millisecond)

	require.NoError(t, m.Reset("GracefulRestart"))
	assert.Equal(t, PoweringOff, powerState(t, m))
	assert.Eventually(t, func() bool {
		return powerState(t, m) == PowerOn
	}, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"SystemPowerdown"}, filterPowerCalls(mock.Calls()))
}

func TestProcessMode_GracefulShutdown_PoweringOffThenOff(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := newFastMachine(NewWithProcess(qmpMock, pm), 5*time.Second)

	require.NoError(t, m.Reset("GracefulShutdown"))
	assert.Equal(t, PoweringOff, powerState(t, m))
	pm.exit()
	assert.Equal(t, PowerOff, powerState(t, m))
	assert.NotContains(t, pm.Calls(), "Kill")
}

func TestProcessMode_GracefulShutdown_TimeoutBackToOn(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := newFastMachine(NewWithProcess(qmpMock, pm), 50*time.Millisecond)

	require.NoError(t, m.Reset("GracefulShutdown"))
	assert.Equal(t, PoweringOff, powerState(t, m))
	assert.Eventually(t, func() bool {
		return powerState(t, m) == PowerOn
	}, 2*time.Second, 5*time.Millisecond)
	assert.True(t, pm.IsRunning())
	assert.NotContains(t, pm.Calls(), "Stop")
	assert.NotContains(t, pm.Calls(), "Kill")
}

func TestProcessMode_GracefulShutdown_SupersededByForceOff(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := newFastMachine(NewWithProcess(qmpMock, pm), 5*time.Second)

	require.NoError(t, m.Reset("GracefulShutdown"))
	assert.Equal(t, PoweringOff, powerState(t, m))
	go func() {
		time.Sleep(20 * time.Millisecond)
		pm.exit()
	}()
	require.NoError(t, m.Reset("ForceOff"))
	assert.Equal(t, PowerOff, powerState(t, m))
}

func TestProcessMode_GracefulRestart_PoweringOffOnOn(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := newFastMachine(NewWithProcess(qmpMock, pm), 5*time.Second)
	require.NoError(t, m.Reset("On"))

	gate := make(chan struct{})
	pm.mu.Lock()
	pm.startGate = gate
	pm.mu.Unlock()

	require.NoError(t, m.Reset("GracefulRestart"))
	assert.Equal(t, PoweringOff, powerState(t, m))

	pm.exit()
	assert.Equal(t, PoweringOn, powerState(t, m))
	// Still PoweringOn while QEMU is being started again
	assert.Eventually(t, func() bool {
		return contains(pm.Calls(), "Stop")
	}, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, PoweringOn, powerState(t, m))

	close(gate)
	assert.Eventually(t, func() bool {
		return powerState(t, m) == PowerOn && len(pm.StartCalls()) == 2
	}, 2*time.Second, 5*time.Millisecond)
}

func TestProcessMode_GracefulRestart_SupersededByForceOffWhilePoweringOn(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := newFastMachine(NewWithProcess(qmpMock, pm), 5*time.Second)
	require.NoError(t, m.Reset("On"))

	gate := make(chan struct{})
	pm.mu.Lock()
	pm.startGate = gate
	pm.mu.Unlock()

	require.NoError(t, m.Reset("GracefulRestart"))
	pm.exit()
	assert.Eventually(t, func() bool {
		return contains(pm.Calls(), "Stop")
	}, 2*time.Second, 5*time.Millisecond)

	// ForceOff waits for the power-on in progress, then wins
	done := make(chan error)
	go func() { done <- m.Reset("ForceOff") }()
	time.Sleep(20 * time.Millisecond)
	close(gate)
	assert.Eventually(t, func() bool {
		return contains(qmpMock.Calls(), "Quit")
	}, 2*time.Second, 5*time.Millisecond)
	pm.exit() // QEMU exits on quit
	require.NoError(t, <-done)
	assert.Equal(t, 2, len(pm.StartCalls()))
	assert.Equal(t, PowerOff, powerState(t, m))
}

func TestSetGracefulTimeout(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)
	m.SetGracefulTimeout(30 * time.Millisecond)

	require.NoError(t, m.Reset("GracefulShutdown"))
	assert.Eventually(t, func() bool {
		return powerState(t, m) == PowerOn
	}, 2*time.Second, 5*time.Millisecond)
}

// filterPowerCalls drops the QMP status queries from calls.
func filterPowerCalls(calls []string) []string {
	var out []string
	for _, c := range calls {
		if c != "QueryStatus" {
			out = append(out, c)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// --- PowerCycle, ForceOn, PushPowerButton, Nmi ---

func TestReset_PowerCycle_Legacy_On_HardReset(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	require.NoError(t, m.Reset("PowerCycle"))
	assert.Contains(t, mock.Calls(), "SystemReset")
	assert.Equal(t, PowerOn, powerState(t, m))
}

func TestReset_PowerCycle_Legacy_Off_PowersOn(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusPaused)
	m := New(mock)

	require.NoError(t, m.Reset("PowerCycle"))
	calls := mock.Calls()
	assert.Equal(t, "Cont", calls[len(calls)-1])
	assert.Equal(t, PowerOn, powerState(t, m))
}

func TestProcessMode_Reset_PowerCycle_RestartsProcess(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	require.NoError(t, m.Reset("On"))
	require.Equal(t, []string{"None"}, pm.StartCalls())

	// The boot override applies to the new process, as on a real power on
	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"}))
	go func() {
		assert.Eventually(t, func() bool {
			return len(qmpMock.Calls()) > 0 && qmpMock.Calls()[len(qmpMock.Calls())-1] == "Quit"
		}, 2*time.Second, time.Millisecond)
		pm.exit()
	}()
	require.NoError(t, m.Reset("PowerCycle"))

	assert.Equal(t, []string{"None", "Pxe"}, pm.StartCalls())
	assert.Contains(t, qmpMock.Calls(), "Quit")
	assert.NotContains(t, qmpMock.Calls(), "SystemReset")
	assert.True(t, pm.IsRunning())
	assert.Equal(t, "Disabled", m.GetBootOverride().Enabled)
}

func TestProcessMode_Reset_PowerCycle_Off_PowersOn(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	require.NoError(t, m.Reset("PowerCycle"))
	assert.Equal(t, []string{"Start"}, pm.Calls())
	assert.NotContains(t, qmpMock.Calls(), "Quit")
}

func TestReset_ForceOn(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	require.NoError(t, m.Reset("ForceOn"))
	assert.Equal(t, []string{"Start"}, pm.Calls())

	// Already on: no-op like On
	require.NoError(t, m.Reset("ForceOn"))
	assert.Equal(t, []string{"Start"}, pm.Calls())
}

func TestReset_PushPowerButton_Off_PowersOn(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusShutdown)
	m := New(mock)

	require.NoError(t, m.Reset("PushPowerButton"))
	calls := mock.Calls()
	assert.Equal(t, "Cont", calls[len(calls)-1])
	assert.NotContains(t, calls, "SystemPowerdown")
	assert.Equal(t, PowerOn, powerState(t, m))
}

func TestReset_PushPowerButton_On_GracefulShutdown(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)

	require.NoError(t, m.Reset("PushPowerButton"))
	assert.Contains(t, mock.Calls(), "SystemPowerdown")
	assert.NotContains(t, mock.Calls(), "Stop")
	assert.Equal(t, PoweringOff, powerState(t, m))

	mock.setStatus(qmp.StatusShutdown)
	assert.Equal(t, PowerOff, powerState(t, m))
}

func TestReset_PushPowerButton_GuestIgnoresACPI_BackToOn(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 50*time.Millisecond)

	require.NoError(t, m.Reset("PushPowerButton"))
	assert.Equal(t, PoweringOff, powerState(t, m))
	assert.Eventually(t, func() bool {
		return powerState(t, m) == PowerOn
	}, 2*time.Second, 5*time.Millisecond)
}

func TestReset_Nmi_On_InjectsNMI(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	require.NoError(t, m.Reset("Nmi"))
	assert.Contains(t, mock.Calls(), "InjectNMI")
}

func TestReset_Nmi_Off_Error(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	err := m.Reset("Nmi")
	assert.ErrorIs(t, err, ErrPoweredOff)
	assert.NotContains(t, qmpMock.Calls(), "InjectNMI")
}

func TestReset_Nmi_KeepsGracefulShutdown(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)

	require.NoError(t, m.Reset("GracefulShutdown"))
	require.NoError(t, m.Reset("Nmi"))
	assert.Equal(t, PoweringOff, powerState(t, m))
}
