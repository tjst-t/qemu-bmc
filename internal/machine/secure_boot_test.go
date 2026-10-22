package machine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qemu"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// secureBootFirmware returns the Secure Boot firmware of a VM whose OVMF
// is in a temporary directory, with the secboot files if present.
func secureBootFirmware(t *testing.T, present bool) qemu.SecureBootFirmware {
	t.Helper()
	ovmf, vm := t.TempDir(), t.TempDir()
	if present {
		for _, f := range []string{"OVMF_CODE_4M.secboot.fd", "OVMF_VARS_4M.ms.fd"} {
			require.NoError(t, os.WriteFile(filepath.Join(ovmf, f), []byte(f), 0o644))
		}
	}
	fw, ok := qemu.FindSecureBootFirmware([]string{
		"-drive", "if=pflash,format=raw,readonly=on,file=" + filepath.Join(ovmf, "OVMF_CODE.fd"),
		"-drive", "if=pflash,format=raw,file=" + filepath.Join(vm, "OVMF_VARS.fd"),
	})
	require.True(t, ok)
	return fw
}

func newSecureBootMachine(t *testing.T, running bool) (*Machine, *mockProcessManager, *mockQMPClient, qemu.SecureBootFirmware) {
	t.Helper()
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(running)
	m := NewWithProcess(qmpMock, pm)
	fw := secureBootFirmware(t, true)
	require.NoError(t, m.SetSecureBootFirmware(fw))
	return m, pm, qmpMock, fw
}

func startSB(pm *mockProcessManager) []bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return append([]bool(nil), pm.startSB...)
}

func TestSecureBoot_Unsupported(t *testing.T) {
	legacy := New(newMockQMPClient(qmp.StatusRunning))
	assert.ErrorIs(t, legacy.SetSecureBootFirmware(secureBootFirmware(t, true)), ErrSecureBootUnsupported)
	assert.False(t, legacy.SecureBootSupported())

	// Process mode without OVMF (BIOS)
	m := NewWithProcess(newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))
	assert.False(t, m.SecureBootSupported())
	assert.ErrorIs(t, m.SetSecureBootEnable(true), ErrSecureBootUnsupported)
	assert.ErrorIs(t, m.ResetSecureBootKeys(), ErrSecureBootUnsupported)
}

func TestSecureBoot_EnableNeedsFirmwareFiles(t *testing.T) {
	m := NewWithProcess(newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))
	require.NoError(t, m.SetSecureBootFirmware(secureBootFirmware(t, false)))
	assert.True(t, m.SecureBootSupported())

	err := m.SetSecureBootEnable(true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OVMF_CODE_4M.secboot.fd")
	assert.False(t, m.GetSecureBoot().Enable)
	assert.NoError(t, m.SetSecureBootEnable(false))
}

func TestSecureBoot_AppliesAtNextStart(t *testing.T) {
	m, pm, _, _ := newSecureBootMachine(t, false)
	assert.Equal(t, SecureBoot{}, m.GetSecureBoot())

	require.NoError(t, m.SetSecureBootEnable(true))
	assert.Equal(t, SecureBoot{Enable: true}, m.GetSecureBoot())

	require.NoError(t, m.Reset("On"))
	assert.Equal(t, []bool{true}, pm.startSB)
	assert.Equal(t, SecureBoot{Enable: true, CurrentBoot: true}, m.GetSecureBoot())

	// Disabling does not change the running QEMU
	require.NoError(t, m.SetSecureBootEnable(false))
	assert.Equal(t, SecureBoot{Enable: false, CurrentBoot: true}, m.GetSecureBoot())

	// CurrentBoot stays the last boot while powered off
	pm.exit()
	assert.True(t, m.GetSecureBoot().CurrentBoot)
	require.NoError(t, m.Reset("On"))
	assert.Equal(t, []bool{true, false}, pm.startSB)
	assert.False(t, m.GetSecureBoot().CurrentBoot)
}

func TestSecureBoot_ForceRestartAfterChangeRestartsQEMU(t *testing.T) {
	m, pm, qmpMock, _ := newSecureBootMachine(t, false)
	require.NoError(t, m.Reset("On"))

	require.NoError(t, m.SetSecureBootEnable(true))
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []bool{false, true}, pm.startSB)
	assert.Contains(t, pm.Calls(), "Stop")
	assert.NotContains(t, qmpMock.Calls(), "SystemReset")
	assert.True(t, m.GetSecureBoot().CurrentBoot)

	// Unchanged: system_reset
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []bool{false, true}, pm.startSB)
	assert.Contains(t, qmpMock.Calls(), "SystemReset")
}

func TestSecureBoot_PowerCycleAndGracefulRestartUseNewSetting(t *testing.T) {
	m, pm, qmpMock, _ := newSecureBootMachine(t, false)
	newFastMachine(m, 5*time.Second)
	require.NoError(t, m.Reset("On"))

	require.NoError(t, m.SetSecureBootEnable(true))
	go func() {
		assert.Eventually(t, func() bool {
			calls := qmpMock.Calls()
			return len(calls) > 0 && calls[len(calls)-1] == "Quit"
		}, 2*time.Second, time.Millisecond)
		pm.exit()
	}()
	require.NoError(t, m.Reset("PowerCycle"))
	assert.Equal(t, []bool{false, true}, startSB(pm))

	require.NoError(t, m.SetSecureBootEnable(false))
	qmpMock.setStatus(qmp.StatusRunning)
	require.NoError(t, m.Reset("GracefulRestart"))
	qmpMock.setStatus(qmp.StatusShutdown)
	require.Eventually(t, func() bool { return len(startSB(pm)) == 3 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, []bool{false, true, false}, startSB(pm))
	require.Eventually(t, func() bool { return powerState(t, m) != PoweringOn }, 5*time.Second, 5*time.Millisecond)
	assert.False(t, m.GetSecureBoot().CurrentBoot)
}

func TestSecureBoot_ResetKeys(t *testing.T) {
	m, pm, qmpMock, fw := newSecureBootMachine(t, false)
	require.NoError(t, fw.PrepareVars())
	require.NoError(t, os.WriteFile(fw.Vars, []byte("enrolled by the guest"), 0o644))

	// Powered off: the VARS are recreated from the template at the next start
	require.NoError(t, m.ResetSecureBootKeys())
	assert.NoFileExists(t, fw.Vars)

	// Running without Secure Boot: nothing to restart for
	require.NoError(t, m.Reset("On"))
	require.NoError(t, m.ResetSecureBootKeys())
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []bool{false}, pm.startSB)

	// Running with Secure Boot: the next reset restarts QEMU
	require.NoError(t, m.SetSecureBootEnable(true))
	require.NoError(t, m.Reset("ForceRestart"))
	require.Equal(t, []bool{false, true}, pm.startSB)
	require.NoError(t, m.ResetSecureBootKeys())
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []bool{false, true, true}, pm.startSB)
	n := len(qmpMock.Calls())
	require.NoError(t, m.Reset("ForceRestart"))
	assert.Equal(t, []bool{false, true, true}, pm.startSB)
	assert.Contains(t, qmpMock.Calls()[n:], "SystemReset")
}
