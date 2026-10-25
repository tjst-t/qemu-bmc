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

func TestBuildCommandLine_InjectsUUID(t *testing.T) {
	result, err := BuildCommandLine([]string{"-m", "4096"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
		SystemUUID:    "12345678-9abc-4def-8123-456789abcdef",
	})
	require.NoError(t, err)
	val, ok := ArgValue(result, "-uuid")
	assert.True(t, ok)
	assert.Equal(t, "12345678-9abc-4def-8123-456789abcdef", val)
}

func TestBuildCommandLine_NoUUIDWhenEmpty(t *testing.T) {
	result, err := BuildCommandLine([]string{"-m", "4096"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
	})
	require.NoError(t, err)
	assert.NotContains(t, result, "-uuid")
}

func TestBuildCommandLine_UserUUIDMatching(t *testing.T) {
	result, err := BuildCommandLine([]string{"-uuid", "12345678-9ABC-4def-8123-456789abcdef"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
		SystemUUID:    "12345678-9abc-4def-8123-456789abcdef",
	})
	require.NoError(t, err)
	count := 0
	for _, a := range result {
		if a == "-uuid" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestBuildCommandLine_UserUUIDConflict(t *testing.T) {
	_, err := BuildCommandLine([]string{"-uuid", "11111111-1111-4111-8111-111111111111"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
		SystemUUID:    "12345678-9abc-4def-8123-456789abcdef",
	})
	assert.Error(t, err)
}

// --- ArgValue ---

func TestArgValue(t *testing.T) {
	args := []string{"-m", "4096", "-uuid", "abc"}
	val, ok := ArgValue(args, "-uuid")
	assert.True(t, ok)
	assert.Equal(t, "abc", val)

	_, ok = ArgValue(args, "-smp")
	assert.False(t, ok)

	_, ok = ArgValue([]string{"-uuid"}, "-uuid")
	assert.False(t, ok)
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

// OVMF ignores -boot n: without bootindex it boots an installed OS from the
// disk instead of the network (Ironic cleaning after undeploy hangs)
func TestApplyBootOverride_PxeUEFISetsNICBootIndex(t *testing.T) {
	result := ApplyBootOverride(uefiArgs, "Pxe")
	assert.Equal(t, "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01,bootindex=0", result[7])
	assert.Equal(t, "virtio-net-pci,netdev=net1,mac=52:54:00:00:00:02", result[11], "only the first NIC")
	assert.Contains(t, result, "n")
	assert.Equal(t, "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01", uefiArgs[7], "input not modified")
}

func TestApplyBootOverride_PxeUEFIBootIndexTaken(t *testing.T) {
	args := append([]string{"-device", "virtio-blk-pci,drive=d,bootindex=0"}, uefiArgs...)
	result := ApplyBootOverride(args, "Pxe")
	assert.Equal(t, "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01", result[9])
}

func TestApplyBootOverride_PxeUEFIKeepsUserBootIndex(t *testing.T) {
	args := []string{
		"-drive", "if=pflash,format=raw,file=/OVMF_CODE.fd",
		"-device", "e1000,netdev=n0,bootindex=2",
	}
	result := ApplyBootOverride(args, "Pxe")
	assert.Equal(t, "e1000,netdev=n0,bootindex=2", result[3])
}

func TestApplyBootOverride_PxeBIOSNoBootIndex(t *testing.T) {
	args := []string{"-netdev", "tap,id=net0", "-device", "virtio-net-pci,netdev=net0", "-device", "sga"}
	result := ApplyBootOverride(args, "Pxe")
	assert.Contains(t, result, "virtio-net-pci,netdev=net0")
}

func TestApplyBootOverride_OtherTargetsUEFINoNICBootIndex(t *testing.T) {
	for _, target := range []string{"None", "Hdd", "Cd", "BiosSetup", "UefiHttp"} {
		assert.Contains(t, ApplyBootOverride(uefiArgs, target), "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01", target)
	}
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

// --- HardwareFromArgs ---

func TestHardwareFromArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		memoryMiB int
		cpus      int
	}{
		{"plain MiB and count", []string{"-m", "2048", "-smp", "2"}, 2048, 2},
		{"G suffix", []string{"-m", "4G", "-smp", "8"}, 4096, 8},
		{"M suffix", []string{"-m", "512M"}, 512, 0},
		{"size= option", []string{"-m", "size=8G,slots=2,maxmem=16G"}, 8192, 0},
		{"T suffix", []string{"-m", "1T"}, 1024 * 1024, 0},
		{"smp cpus=", []string{"-smp", "cpus=6,sockets=1"}, 0, 6},
		{"smp count with topology", []string{"-smp", "4,sockets=2,cores=2"}, 0, 4},
		{"smp topology only", []string{"-smp", "sockets=2,cores=2,threads=2"}, 0, 8},
		{"missing", []string{"-machine", "q35"}, 0, 0},
		{"invalid", []string{"-m", "lots", "-smp", "many"}, 0, 0},
		{"last wins", []string{"-m", "1024", "-m", "3072"}, 3072, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hw := HardwareFromArgs(tt.args)
			assert.Equal(t, tt.memoryMiB, hw.MemoryMiB)
			assert.Equal(t, tt.cpus, hw.CPUs)
		})
	}
}

func TestHardwareFromArgs_Defaults(t *testing.T) {
	hw := HardwareFromArgs(ApplyDefaults(nil))
	assert.Equal(t, 2048, hw.MemoryMiB)
	assert.Equal(t, 2, hw.CPUs)
}
