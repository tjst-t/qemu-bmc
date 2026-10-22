package qemu

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFile creates a file with the given content, failing the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// ovmfDirs creates an OVMF directory holding the firmware files named and a
// VM directory, and returns both.
func ovmfDirs(t *testing.T, files ...string) (ovmf, vm string) {
	t.Helper()
	ovmf, vm = t.TempDir(), t.TempDir()
	for _, f := range files {
		writeFile(t, filepath.Join(ovmf, f), f)
	}
	return ovmf, vm
}

func pflashArgs(code, vars string) []string {
	return []string{
		"-machine", "q35,accel=kvm",
		"-drive", "if=pflash,format=raw,readonly=on,file=" + code,
		"-drive", "if=pflash,format=raw,file=" + vars,
		"-m", "2048",
	}
}

func TestFindSecureBootFirmware_2M(t *testing.T) {
	fw, ok := FindSecureBootFirmware(pflashArgs("/usr/share/OVMF/OVMF_CODE.fd", "/vm/OVMF_VARS.fd"))
	require.True(t, ok)
	// The 4M build also for 2M code (Debian's 2M secboot build hangs)
	assert.Equal(t, "/usr/share/OVMF/OVMF_CODE_4M.secboot.fd", fw.Code)
	assert.Equal(t, "/usr/share/OVMF/OVMF_VARS_4M.ms.fd", fw.VarsTemplate)
	assert.Equal(t, "/vm/OVMF_VARS.secboot.fd", fw.Vars)
}

func TestFindSecureBootFirmware_4M(t *testing.T) {
	fw, ok := FindSecureBootFirmware(pflashArgs("/usr/share/OVMF/OVMF_CODE_4M.fd", "/vm/vars.fd"))
	require.True(t, ok)
	assert.Equal(t, "/usr/share/OVMF/OVMF_CODE_4M.secboot.fd", fw.Code)
	assert.Equal(t, "/usr/share/OVMF/OVMF_VARS_4M.ms.fd", fw.VarsTemplate)
	assert.Equal(t, "/vm/vars.secboot.fd", fw.Vars)
}

func TestFindSecureBootFirmware_NotOVMF(t *testing.T) {
	for name, args := range map[string][]string{
		"bios":       {"-m", "2048", "-drive", "file=/vm/disk.qcow2,if=virtio"},
		"no vars":    {"-drive", "if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE.fd"},
		"other code": pflashArgs("/fw/edk2-x86_64-code.fd", "/vm/vars.fd"),
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := FindSecureBootFirmware(args)
			assert.False(t, ok)
		})
	}
}

func TestSecureBootFirmware_Check(t *testing.T) {
	ovmf, vm := ovmfDirs(t, "OVMF_CODE.fd", "OVMF_VARS.fd", "OVMF_CODE_4M.secboot.fd", "OVMF_VARS_4M.ms.fd")
	fw, ok := FindSecureBootFirmware(pflashArgs(filepath.Join(ovmf, "OVMF_CODE.fd"), filepath.Join(vm, "OVMF_VARS.fd")))
	require.True(t, ok)
	assert.NoError(t, fw.Check())

	// Only the 2M secboot files exist
	ovmf2M, _ := ovmfDirs(t, "OVMF_CODE.fd", "OVMF_CODE.secboot.fd", "OVMF_VARS.ms.fd")
	fw2M, ok := FindSecureBootFirmware(pflashArgs(filepath.Join(ovmf2M, "OVMF_CODE.fd"), filepath.Join(vm, "OVMF_VARS.fd")))
	require.True(t, ok)
	err := fw2M.Check()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OVMF_CODE_4M.secboot.fd")
}

func TestSecureBootFirmware_PrepareVars_CopiesTemplateOnce(t *testing.T) {
	ovmf, vm := ovmfDirs(t, "OVMF_CODE_4M.secboot.fd", "OVMF_VARS_4M.ms.fd")
	writeFile(t, filepath.Join(vm, "OVMF_VARS.fd"), "plain vars")
	fw, ok := FindSecureBootFirmware(pflashArgs(filepath.Join(ovmf, "OVMF_CODE.fd"), filepath.Join(vm, "OVMF_VARS.fd")))
	require.True(t, ok)

	require.NoError(t, fw.PrepareVars())
	data, err := os.ReadFile(filepath.Join(vm, "OVMF_VARS.secboot.fd"))
	require.NoError(t, err)
	assert.Equal(t, "OVMF_VARS_4M.ms.fd", string(data))

	// An existing copy (with the guest's changes) is kept
	writeFile(t, fw.Vars, "changed by guest!!") // same size as the template
	require.NoError(t, fw.PrepareVars())
	data, _ = os.ReadFile(fw.Vars)
	assert.Equal(t, "changed by guest!!", string(data))

	// The plain VARS is untouched
	data, _ = os.ReadFile(filepath.Join(vm, "OVMF_VARS.fd"))
	assert.Equal(t, "plain vars", string(data))

	// ResetVars makes the next PrepareVars copy the template again
	require.NoError(t, fw.ResetVars())
	require.NoError(t, fw.ResetVars()) // already removed
	require.NoError(t, fw.PrepareVars())
	data, _ = os.ReadFile(fw.Vars)
	assert.Equal(t, "OVMF_VARS_4M.ms.fd", string(data))
}

func TestSecureBootFirmware_Apply(t *testing.T) {
	args := pflashArgs("/usr/share/OVMF/OVMF_CODE_4M.fd", "/vm/OVMF_VARS.fd")
	fw, ok := FindSecureBootFirmware(args)
	require.True(t, ok)

	got := fw.Apply(args)
	assert.Equal(t, []string{
		"-machine", "q35,accel=kvm,smm=on",
		"-drive", "if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.secboot.fd",
		"-drive", "if=pflash,format=raw,file=/vm/OVMF_VARS.secboot.fd",
		"-m", "2048",
		"-global", "driver=cfi.pflash01,property=secure,value=on",
	}, got)
	// args is not modified
	assert.Equal(t, pflashArgs("/usr/share/OVMF/OVMF_CODE_4M.fd", "/vm/OVMF_VARS.fd"), args)
}

func TestSecureBootFirmware_Apply_ReplacesSMMOff(t *testing.T) {
	args := append([]string{"-M", "q35,smm=off"}, pflashArgs("/o/OVMF_CODE.fd", "/vm/a,,b.fd")[2:]...)
	fw, ok := FindSecureBootFirmware(args)
	require.True(t, ok)
	assert.Equal(t, "/vm/a,b.secboot.fd", fw.Vars)

	got := fw.Apply(args)
	assert.Equal(t, "q35,smm=on", got[1])
	assert.Equal(t, "if=pflash,format=raw,file=/vm/a,,b.secboot.fd", got[5])
}

func TestProcessManager_Start_SecureBoot(t *testing.T) {
	ovmf, vm := ovmfDirs(t, "OVMF_CODE.fd", "OVMF_CODE_4M.secboot.fd", "OVMF_VARS_4M.ms.fd")
	f := &trackingFactory{}
	base := EnsureCDROM(pflashArgs(filepath.Join(ovmf, "OVMF_CODE.fd"), filepath.Join(vm, "OVMF_VARS.fd")))
	pm := NewProcessManager("qemu-system-x86_64", base, f.create)
	require.NoError(t, pm.Start("Cd", "/vm/cd.iso", "", true))
	defer pm.Kill()

	assert.Contains(t, f.lastArgs, "if=pflash,format=raw,readonly=on,file="+filepath.Join(ovmf, "OVMF_CODE_4M.secboot.fd"))
	assert.Contains(t, f.lastArgs, "if=pflash,format=raw,file="+filepath.Join(vm, "OVMF_VARS.secboot.fd"))
	assert.Contains(t, f.lastArgs, "q35,accel=kvm,smm=on")
	assert.Contains(t, f.lastArgs, "driver=cfi.pflash01,property=secure,value=on")
	// The other overrides still apply
	assert.Contains(t, f.lastArgs, "if=none,id=vmedia0,media=cdrom,readonly=on,format=raw,file=/vm/cd.iso")
	assert.FileExists(t, filepath.Join(vm, "OVMF_VARS.secboot.fd"))
}

func TestProcessManager_Start_SecureBootDisabled_ArgsUnchanged(t *testing.T) {
	f := &trackingFactory{}
	base := pflashArgs("/o/OVMF_CODE.fd", "/vm/OVMF_VARS.fd")
	pm := NewProcessManager("qemu-system-x86_64", base, f.create)
	require.NoError(t, pm.Start("", "", "", false))
	defer pm.Kill()

	assert.Contains(t, f.lastArgs, "if=pflash,format=raw,readonly=on,file=/o/OVMF_CODE.fd")
	assert.Contains(t, f.lastArgs, "if=pflash,format=raw,file=/vm/OVMF_VARS.fd")
	assert.NotContains(t, f.lastArgs, "-global")
}

func TestProcessManager_Start_SecureBootFirmwareMissing(t *testing.T) {
	ovmf, vm := ovmfDirs(t, "OVMF_CODE.fd")
	f := &trackingFactory{}
	pm := NewProcessManager("qemu-system-x86_64", pflashArgs(filepath.Join(ovmf, "OVMF_CODE.fd"), filepath.Join(vm, "OVMF_VARS.fd")), f.create)
	err := pm.Start("", "", "", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OVMF_CODE_4M.secboot.fd")
	assert.False(t, pm.IsRunning())
	assert.Nil(t, f.lastArgs)

	// BIOS
	pm = NewProcessManager("qemu-system-x86_64", []string{"-m", "2048"}, f.create)
	assert.Error(t, pm.Start("", "", "", true))
}

func TestSecureBootFirmware_PrepareVars_ReplacesVarsOfAnotherSize(t *testing.T) {
	ovmf, vm := ovmfDirs(t, "OVMF_CODE_4M.secboot.fd", "OVMF_VARS_4M.ms.fd")
	fw, ok := FindSecureBootFirmware(pflashArgs(filepath.Join(ovmf, "OVMF_CODE.fd"), filepath.Join(vm, "OVMF_VARS.fd")))
	require.True(t, ok)
	// e.g. copied from the 2M template by an earlier build
	writeFile(t, fw.Vars, "2M")
	require.NoError(t, fw.PrepareVars())
	data, err := os.ReadFile(fw.Vars)
	require.NoError(t, err)
	assert.Equal(t, "OVMF_VARS_4M.ms.fd", string(data))
}
