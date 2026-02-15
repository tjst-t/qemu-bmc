package qemu

import (
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sleepFactory returns a CommandFactory that runs "sleep" for testing.
func sleepFactory(binary string, args []string) *exec.Cmd {
	return exec.Command("sleep", "3600")
}

// trackingFactory records the args passed to Start and runs "sleep".
type trackingFactory struct {
	lastArgs []string
}

func (f *trackingFactory) create(binary string, args []string) *exec.Cmd {
	f.lastArgs = make([]string, len(args))
	copy(f.lastArgs, args)
	return exec.Command("sleep", "3600")
}

func TestProcessManager_Start_IsRunning(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, sleepFactory)
	require.NoError(t, pm.Start(""))
	defer pm.Kill()

	assert.True(t, pm.IsRunning())
}

func TestProcessManager_Start_AlreadyRunning_Noop(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, sleepFactory)
	require.NoError(t, pm.Start(""))
	defer pm.Kill()

	// Second start is a no-op
	require.NoError(t, pm.Start(""))
	assert.True(t, pm.IsRunning())
}

func TestProcessManager_Stop_TerminatesProcess(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, sleepFactory)
	require.NoError(t, pm.Start(""))

	err := pm.Stop(5 * time.Second)
	require.NoError(t, err)

	// Give the monitor goroutine time to update state
	time.Sleep(100 * time.Millisecond)
	assert.False(t, pm.IsRunning())
}

func TestProcessManager_Kill_TerminatesImmediately(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, sleepFactory)
	require.NoError(t, pm.Start(""))

	err := pm.Kill()
	require.NoError(t, err)

	// Give the monitor goroutine time to update state
	time.Sleep(100 * time.Millisecond)
	assert.False(t, pm.IsRunning())
}

func TestProcessManager_IsRunning_AfterExit(t *testing.T) {
	// Use a command that exits immediately
	factory := func(binary string, args []string) *exec.Cmd {
		return exec.Command("true")
	}
	pm := NewProcessManager("qemu-system-x86_64", []string{}, factory)
	require.NoError(t, pm.Start(""))

	// Wait for the process to exit naturally
	time.Sleep(200 * time.Millisecond)
	assert.False(t, pm.IsRunning())
}

func TestProcessManager_WaitForExit_Timeout(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, sleepFactory)
	require.NoError(t, pm.Start(""))
	defer pm.Kill()

	err := pm.WaitForExit(100 * time.Millisecond)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestProcessManager_ExitCh_ClosedOnExit(t *testing.T) {
	factory := func(binary string, args []string) *exec.Cmd {
		return exec.Command("true")
	}
	pm := NewProcessManager("qemu-system-x86_64", []string{}, factory)
	require.NoError(t, pm.Start(""))

	select {
	case <-pm.ExitCh():
		// Channel closed as expected
	case <-time.After(2 * time.Second):
		t.Fatal("ExitCh was not closed after process exit")
	}
}

func TestProcessManager_Start_AppliesBootOverride(t *testing.T) {
	f := &trackingFactory{}
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, f.create)
	require.NoError(t, pm.Start("Pxe"))
	defer pm.Kill()

	assert.Contains(t, f.lastArgs, "-boot")
	assert.Contains(t, f.lastArgs, "n")
}

func TestProcessManager_Start_NoBootOverride(t *testing.T) {
	f := &trackingFactory{}
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, f.create)
	require.NoError(t, pm.Start(""))
	defer pm.Kill()

	assert.NotContains(t, f.lastArgs, "-boot")
}

func TestProcessManager_Stop_NotRunning_Noop(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{}, sleepFactory)
	err := pm.Stop(time.Second)
	assert.NoError(t, err)
}

func TestProcessManager_Kill_NotRunning_Noop(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{}, sleepFactory)
	err := pm.Kill()
	assert.NoError(t, err)
}

func TestProcessManager_Restart_AfterStop(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, sleepFactory)

	// Start
	require.NoError(t, pm.Start(""))
	assert.True(t, pm.IsRunning())

	// Stop
	require.NoError(t, pm.Stop(5*time.Second))
	time.Sleep(100 * time.Millisecond)
	assert.False(t, pm.IsRunning())

	// Re-start
	require.NoError(t, pm.Start(""))
	defer pm.Kill()
	assert.True(t, pm.IsRunning())
}
