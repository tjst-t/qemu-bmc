package qemu

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ValidateArgs ---

func TestValidateArgs_RejectQMP(t *testing.T) {
	err := ValidateArgs([]string{"-qmp", "unix:/tmp/qmp.sock"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "-qmp")
}

func TestValidateArgs_RejectSerial(t *testing.T) {
	err := ValidateArgs([]string{"-serial", "chardev:serial0"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "-serial")
}

func TestValidateArgs_RejectChardevSerial0(t *testing.T) {
	err := ValidateArgs([]string{"-chardev", "socket,id=serial0,host=localhost,port=9002"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "-chardev")
}

func TestValidateArgs_AcceptChardevOtherID(t *testing.T) {
	err := ValidateArgs([]string{"-chardev", "socket,id=foo,host=localhost,port=9002"})
	assert.NoError(t, err)
}

func TestValidateArgs_RejectMonitorStdio(t *testing.T) {
	err := ValidateArgs([]string{"-monitor", "stdio"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "-monitor")
}

func TestValidateArgs_AcceptMonitorOther(t *testing.T) {
	err := ValidateArgs([]string{"-monitor", "tcp:localhost:4444"})
	assert.NoError(t, err)
}

func TestValidateArgs_RejectDaemonize(t *testing.T) {
	err := ValidateArgs([]string{"-daemonize"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "-daemonize")
}

func TestValidateArgs_AcceptValidArgs(t *testing.T) {
	err := ValidateArgs([]string{"-m", "4096", "-smp", "4", "-machine", "q35,accel=kvm"})
	assert.NoError(t, err)
}

// --- ApplyDefaults ---

func TestApplyDefaults_AddsMachineWhenMissing(t *testing.T) {
	result := ApplyDefaults([]string{"-m", "4096"})
	assert.Contains(t, result, "-machine")
	assert.Contains(t, result, "q35")
}

func TestApplyDefaults_SkipsMachineWhenPresent(t *testing.T) {
	args := []string{"-machine", "q35,accel=kvm"}
	result := ApplyDefaults(args)
	// Count occurrences of -machine
	count := 0
	for _, a := range result {
		if a == "-machine" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestApplyDefaults_AddsMemoryWhenMissing(t *testing.T) {
	result := ApplyDefaults([]string{"-machine", "q35"})
	assert.Contains(t, result, "-m")
	assert.Contains(t, result, "2048")
}

func TestApplyDefaults_SkipsMemoryWhenPresent(t *testing.T) {
	args := []string{"-m", "4096"}
	result := ApplyDefaults(args)
	count := 0
	for _, a := range result {
		if a == "-m" {
			count++
		}
	}
	assert.Equal(t, 1, count)
	assert.Contains(t, result, "4096")
}

func TestApplyDefaults_AddsSmpWhenMissing(t *testing.T) {
	result := ApplyDefaults([]string{"-machine", "q35"})
	assert.Contains(t, result, "-smp")
}

func TestApplyDefaults_SkipsSmpWhenPresent(t *testing.T) {
	args := []string{"-smp", "8"}
	result := ApplyDefaults(args)
	count := 0
	for _, a := range result {
		if a == "-smp" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestApplyDefaults_AddsVgaWhenMissing(t *testing.T) {
	result := ApplyDefaults([]string{"-machine", "q35"})
	assert.Contains(t, result, "-vga")
}

func TestApplyDefaults_SkipsVgaWhenPresent(t *testing.T) {
	args := []string{"-vga", "virtio"}
	result := ApplyDefaults(args)
	count := 0
	for _, a := range result {
		if a == "-vga" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestApplyDefaults_AllDefaultsAdded(t *testing.T) {
	result := ApplyDefaults([]string{})
	assert.Contains(t, result, "-machine")
	assert.Contains(t, result, "-m")
	assert.Contains(t, result, "-smp")
	assert.Contains(t, result, "-vga")
}

func TestApplyDefaults_NoDefaultsNeeded(t *testing.T) {
	args := []string{"-machine", "q35", "-m", "4096", "-smp", "4", "-vga", "virtio"}
	result := ApplyDefaults(args)
	assert.Equal(t, args, result)
}

// --- BuildCommandLine ---

func TestBuildCommandLine_InjectsQMP(t *testing.T) {
	result, err := BuildCommandLine([]string{"-m", "4096"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "-qmp")
	assert.Contains(t, result, "unix:/tmp/qmp.sock,server,nowait")
}

func TestBuildCommandLine_InjectsDisplayNone(t *testing.T) {
	result, err := BuildCommandLine([]string{"-m", "4096"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "-display")
	assert.Contains(t, result, "none")
}

func TestBuildCommandLine_InjectsSerial(t *testing.T) {
	result, err := BuildCommandLine([]string{"-m", "4096"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
		SerialAddr:    "localhost:9002",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "-chardev")
	assert.Contains(t, result, "socket,id=serial0,host=localhost,port=9002,server=on,wait=off")
	assert.Contains(t, result, "-serial")
	assert.Contains(t, result, "chardev:serial0")
}

func TestBuildCommandLine_UserArgsPreserved(t *testing.T) {
	result, err := BuildCommandLine([]string{"-m", "4096", "-smp", "8"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "-m")
	assert.Contains(t, result, "4096")
	assert.Contains(t, result, "-smp")
	assert.Contains(t, result, "8")
}

func TestBuildCommandLine_ValidationError(t *testing.T) {
	_, err := BuildCommandLine([]string{"-qmp", "unix:/tmp/bad.sock"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
	})
	assert.Error(t, err)
}

func TestBuildCommandLine_AppliesDefaults(t *testing.T) {
	result, err := BuildCommandLine([]string{}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "-machine")
	assert.Contains(t, result, "-m")
	assert.Contains(t, result, "-smp")
	assert.Contains(t, result, "-vga")
}

func TestBuildCommandLine_NoSerialWhenEmpty(t *testing.T) {
	result, err := BuildCommandLine([]string{"-m", "4096"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
		SerialAddr:    "",
	})
	require.NoError(t, err)
	// serial0 should not be injected
	for _, arg := range result {
		assert.NotContains(t, arg, "serial0")
	}
}

// --- ApplyBootOverride ---

func TestApplyBootOverride_None(t *testing.T) {
	args := []string{"-m", "4096"}
	result := ApplyBootOverride(args, "None")
	assert.Equal(t, args, result)
}

func TestApplyBootOverride_Empty(t *testing.T) {
	args := []string{"-m", "4096"}
	result := ApplyBootOverride(args, "")
	assert.Equal(t, args, result)
}

func TestApplyBootOverride_Pxe(t *testing.T) {
	result := ApplyBootOverride([]string{"-m", "4096"}, "Pxe")
	assert.Contains(t, result, "-boot")
	assert.Contains(t, result, "n")
}

func TestApplyBootOverride_Hdd(t *testing.T) {
	result := ApplyBootOverride([]string{"-m", "4096"}, "Hdd")
	assert.Contains(t, result, "-boot")
	assert.Contains(t, result, "c")
}

func TestApplyBootOverride_Cd(t *testing.T) {
	result := ApplyBootOverride([]string{"-m", "4096"}, "Cd")
	assert.Contains(t, result, "-boot")
	assert.Contains(t, result, "d")
}

func TestApplyBootOverride_BiosSetup(t *testing.T) {
	result := ApplyBootOverride([]string{"-m", "4096"}, "BiosSetup")
	assert.Contains(t, result, "-boot")
	assert.Contains(t, result, "menu=on")
}

func TestApplyBootOverride_ReplacesExisting(t *testing.T) {
	args := []string{"-m", "4096", "-boot", "c"}
	result := ApplyBootOverride(args, "Pxe")
	assert.Contains(t, result, "n")
	assert.NotContains(t, result, "c")
	// Only one -boot
	count := 0
	for _, a := range result {
		if a == "-boot" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestApplyBootOverride_AddsWhenMissing(t *testing.T) {
	args := []string{"-m", "4096"}
	result := ApplyBootOverride(args, "Pxe")
	assert.Contains(t, result, "-boot")
	assert.Contains(t, result, "n")
}
