package qemu

import (
	"fmt"
	"os/exec"
	"sync"
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
	require.NoError(t, pm.Start("", "", ""))
	defer pm.Kill()

	assert.True(t, pm.IsRunning())
}

func TestProcessManager_Start_AlreadyRunning_Noop(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, sleepFactory)
	require.NoError(t, pm.Start("", "", ""))
	defer pm.Kill()

	// Second start is a no-op
	require.NoError(t, pm.Start("", "", ""))
	assert.True(t, pm.IsRunning())
}

func TestProcessManager_Stop_TerminatesProcess(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, sleepFactory)
	require.NoError(t, pm.Start("", "", ""))

	err := pm.Stop(5 * time.Second)
	require.NoError(t, err)

	// Give the monitor goroutine time to update state
	time.Sleep(100 * time.Millisecond)
	assert.False(t, pm.IsRunning())
}

func TestProcessManager_Kill_TerminatesImmediately(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, sleepFactory)
	require.NoError(t, pm.Start("", "", ""))

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
	require.NoError(t, pm.Start("", "", ""))

	// Wait for the process to exit naturally
	time.Sleep(200 * time.Millisecond)
	assert.False(t, pm.IsRunning())
}

func TestProcessManager_WaitForExit_Timeout(t *testing.T) {
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, sleepFactory)
	require.NoError(t, pm.Start("", "", ""))
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
	require.NoError(t, pm.Start("", "", ""))

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
	require.NoError(t, pm.Start("Pxe", "", ""))
	defer pm.Kill()

	assert.Contains(t, f.lastArgs, "-boot")
	assert.Contains(t, f.lastArgs, "n")
}

func TestProcessManager_Start_NoBootOverride(t *testing.T) {
	f := &trackingFactory{}
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, f.create)
	require.NoError(t, pm.Start("", "", ""))
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
	require.NoError(t, pm.Start("", "", ""))
	assert.True(t, pm.IsRunning())

	// Stop
	require.NoError(t, pm.Stop(5*time.Second))
	time.Sleep(100 * time.Millisecond)
	assert.False(t, pm.IsRunning())

	// Re-start
	require.NoError(t, pm.Start("", "", ""))
	defer pm.Kill()
	assert.True(t, pm.IsRunning())
}

func TestProcessManager_Start_LogsQEMUStderr(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	factory := func(binary string, args []string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo 'could not open /dev/net/tun' >&2; printf 'partial' >&2; exit 1")
	}
	pm := NewProcessManager("qemu-system-x86_64", nil, factory)
	pm.(*processManager).logf = func(format string, v ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, v...))
	}
	require.NoError(t, pm.Start("", "", ""))
	require.NoError(t, pm.WaitForExit(5*time.Second))

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, lines, "qemu: could not open /dev/net/tun")
	assert.Contains(t, lines, "qemu: partial")
	assert.Contains(t, lines, "QEMU process exited: exit status 1")
}

func TestLineLogger_SplitsLines(t *testing.T) {
	var lines []string
	l := &lineLogger{logf: func(format string, v ...any) { lines = append(lines, fmt.Sprintf(format, v...)) }}
	l.Write([]byte("a\nb"))
	l.Write([]byte("c\r\n\n"))
	l.Flush()
	assert.Equal(t, []string{"qemu: a", "qemu: bc"}, lines)
}

func TestProcessManager_Start_UefiHttp_WithImage_HiddenCD(t *testing.T) {
	f := &trackingFactory{}
	base := EnsureCDROM([]string{"-m", "2048"})
	pm := NewProcessManager("qemu-system-x86_64", base, f.create)
	require.NoError(t, pm.Start("UefiHttp", "/vm/cd.iso", "/vm/vmedia/httpboot-1.iso"))
	defer pm.Kill()

	assert.Contains(t, f.lastArgs, "if=none,id=httpboot0,media=cdrom,readonly=on,format=raw,file=/vm/vmedia/httpboot-1.iso")
	assert.Contains(t, f.lastArgs, "ide-cd,drive=httpboot0,id=httpboot-cd0,bootindex=0")
	// The VirtualMedia drive keeps its own image and no boot priority
	assert.Contains(t, f.lastArgs, "ide-cd,drive=vmedia0,id=vmedia-cd0")
	assert.Contains(t, f.lastArgs, "if=none,id=vmedia0,media=cdrom,readonly=on,format=raw,file=/vm/cd.iso")
	assert.NotContains(t, f.lastArgs, "-fw_cfg")
}

func TestProcessManager_Start_HTTPBootImageIgnoredForOtherTargets(t *testing.T) {
	f := &trackingFactory{}
	pm := NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, f.create)
	require.NoError(t, pm.Start("Hdd", "", "/vm/vmedia/httpboot-1.iso"))
	defer pm.Kill()

	for _, a := range f.lastArgs {
		assert.NotContains(t, a, "httpboot")
	}
}
