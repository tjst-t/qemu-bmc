package qemu

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	emptyCDDrive  = "if=none,id=vmedia0,media=cdrom,readonly=on"
	cdDeviceValue = "ide-cd,drive=vmedia0,id=vmedia-cd0"
)

func TestEnsureCDROM_AddsEmptyDrive(t *testing.T) {
	result := EnsureCDROM([]string{"-m", "4096"})
	assert.Equal(t, []string{
		"-m", "4096",
		"-drive", emptyCDDrive,
		"-device", cdDeviceValue,
	}, result)
}

func TestEnsureCDROM_ReplacesCdrom(t *testing.T) {
	result := EnsureCDROM([]string{"-cdrom", "/iso/a.iso", "-m", "4096"})
	assert.Equal(t, []string{
		"-m", "4096",
		"-drive", emptyCDDrive + ",format=raw,file=/iso/a.iso",
		"-device", cdDeviceValue,
	}, result)
}

func TestEnsureCDROM_EscapesCommas(t *testing.T) {
	result := EnsureCDROM([]string{"-cdrom", "/iso/a,b.iso"})
	assert.Contains(t, result, emptyCDDrive+",format=raw,file=/iso/a,,b.iso")
	assert.Equal(t, "/iso/a,b.iso", InitialCDImage(result))
}

func TestEnsureCDROM_KeepsUserCDDrive(t *testing.T) {
	for _, args := range [][]string{
		{"-drive", "if=none,id=cd,media=cdrom", "-device", "ide-cd,drive=cd"},
		{"-drive", "file=/iso/a.iso,media=cdrom"},
		{"-device", "scsi-cd,drive=x"},
		{"-device", "driver=ide-cd,drive=x"},
	} {
		assert.Equal(t, args, EnsureCDROM(args), "%v", args)
	}
}

func TestBuildCommandLine_AddsCDDrive(t *testing.T) {
	result, err := BuildCommandLine([]string{"-m", "4096"}, BuildOptions{QMPSocketPath: "/tmp/qmp.sock"})
	require.NoError(t, err)
	assert.Contains(t, result, emptyCDDrive)
	assert.Contains(t, result, cdDeviceValue)
}

func TestInitialCDImage(t *testing.T) {
	assert.Equal(t, "", InitialCDImage(EnsureCDROM(nil)))
	assert.Equal(t, "/iso/a.iso", InitialCDImage(EnsureCDROM([]string{"-cdrom", "/iso/a.iso"})))
	assert.Equal(t, "/iso/b.iso", InitialCDImage([]string{"-drive", "media=cdrom,file=/iso/b.iso"}))
	assert.Equal(t, "", InitialCDImage([]string{"-drive", "file=/vm/disk.qcow2"}))
}

func TestSetCDImage(t *testing.T) {
	base := EnsureCDROM([]string{"-m", "4096"})

	inserted := SetCDImage(base, "/iso/a.iso")
	assert.Contains(t, inserted, emptyCDDrive+",format=raw,file=/iso/a.iso")
	assert.Contains(t, base, emptyCDDrive, "base args must not be modified")

	ejected := SetCDImage(inserted, "")
	assert.Equal(t, base, ejected)
}

func TestSetCDImage_NoManagedDrive(t *testing.T) {
	args := []string{"-drive", "media=cdrom,file=/iso/b.iso"}
	assert.Equal(t, args, SetCDImage(args, "/iso/a.iso"))
}

func TestApplyBootOverride_CdSetsBootIndex(t *testing.T) {
	base := EnsureCDROM([]string{"-boot", "c"})
	result := ApplyBootOverride(base, "Cd")
	assert.Contains(t, result, cdDeviceValue+",bootindex=0")
	assert.Contains(t, result, "d")
	assert.Contains(t, base, cdDeviceValue, "base args must not be modified")
}

func TestApplyBootOverride_OtherTargetsNoBootIndex(t *testing.T) {
	base := EnsureCDROM(nil)
	for _, target := range []string{"None", "Pxe", "Hdd", "BiosSetup"} {
		assert.Contains(t, ApplyBootOverride(base, target), cdDeviceValue, target)
	}
}

func TestApplyBootOverride_CdBootIndexConflict(t *testing.T) {
	base := EnsureCDROM([]string{"-device", "virtio-blk-pci,drive=d,bootindex=0"})
	result := ApplyBootOverride(base, "Cd")
	assert.Contains(t, result, cdDeviceValue)
}

func TestProcessManager_Start_SetsCDImage(t *testing.T) {
	f := &trackingFactory{}
	pm := NewProcessManager("qemu-system-x86_64", EnsureCDROM([]string{"-m", "2048"}), f.create)
	require.NoError(t, pm.Start("Cd", "/iso/a.iso", ""))
	defer pm.Kill()

	assert.Contains(t, f.lastArgs, emptyCDDrive+",format=raw,file=/iso/a.iso")
	assert.Contains(t, f.lastArgs, cdDeviceValue+",bootindex=0")
}
