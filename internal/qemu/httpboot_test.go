package qemu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

var uefiArgs = []string{
	"-drive", "if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE.fd",
	"-drive", "if=pflash,format=raw,file=/vm/OVMF_VARS.fd",
	"-netdev", "tap,id=net0", "-device", "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01",
	"-netdev", "tap,id=net1", "-device", "virtio-net-pci,netdev=net1,mac=52:54:00:00:00:02",
}

func TestApplyHTTPBoot_Image(t *testing.T) {
	result := ApplyHTTPBoot(uefiArgs, "/tmp/a,b.iso")
	assert.Equal(t, uefiArgs, result[:len(uefiArgs)], "existing args unchanged")
	assert.Equal(t, []string{
		"-drive", "if=none,id=httpboot0,media=cdrom,readonly=on,format=raw,file=/tmp/a,,b.iso",
		"-device", "ide-cd,drive=httpboot0,id=httpboot-cd0,bootindex=0",
	}, result[len(uefiArgs):])
	assert.NotContains(t, result, "-fw_cfg")
}

func TestApplyHTTPBoot_Image_BootIndex0Taken(t *testing.T) {
	args := []string{"-device", "virtio-blk-pci,drive=d0,bootindex=0"}
	result := ApplyHTTPBoot(args, "/tmp/a.iso")
	assert.Contains(t, result, "ide-cd,drive=httpboot0,id=httpboot-cd0")
}

func TestApplyHTTPBoot_NoImage_UEFI(t *testing.T) {
	result := ApplyHTTPBoot(uefiArgs, "")
	assert.Equal(t, "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01,romfile=,bootindex=0", result[7])
	assert.Equal(t, "virtio-net-pci,netdev=net1,mac=52:54:00:00:00:02", result[11], "only the first NIC")
	assert.Equal(t, []string{
		"-fw_cfg", "name=opt/org.tianocore/IPv4PXESupport,string=n",
		"-fw_cfg", "name=opt/org.tianocore/IPv6PXESupport,string=n",
	}, result[len(uefiArgs):])
	assert.Equal(t, "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01", uefiArgs[7], "input not modified")
	for _, a := range result {
		assert.NotContains(t, a, "httpboot")
	}
}

func TestApplyHTTPBoot_NoImage_UEFI_KeepsUserRomfileAndBootIndex(t *testing.T) {
	args := []string{
		"-drive", "if=pflash,format=raw,file=/OVMF_CODE.fd",
		"-device", "e1000,netdev=n0,romfile=/my.rom,bootindex=2",
	}
	result := ApplyHTTPBoot(args, "")
	assert.Equal(t, "e1000,netdev=n0,romfile=/my.rom,bootindex=2", result[3])
	assert.Contains(t, result, "-fw_cfg")
}

func TestApplyHTTPBoot_NoImage_BIOS_SameAsPxe(t *testing.T) {
	args := []string{"-netdev", "tap,id=net0", "-device", "virtio-net-pci,netdev=net0", "-device", "sga"}
	assert.Equal(t, args, ApplyHTTPBoot(args, ""))
	result := ApplyHTTPBoot(ApplyBootOverride(args, "UefiHttp"), "")
	assert.Equal(t, ApplyBootOverride(args, "Pxe"), result)
}

func TestIsUEFI(t *testing.T) {
	assert.True(t, IsUEFI(uefiArgs))
	assert.True(t, IsUEFI([]string{"-bios", "/usr/share/ovmf/OVMF.fd"}))
	assert.True(t, IsUEFI([]string{"-machine", "q35,pflash0=code"}))
	assert.False(t, IsUEFI([]string{"-machine", "q35", "-device", "sga"}))
	assert.False(t, IsUEFI([]string{"-bios", "/usr/share/seabios/bios.bin"}))
}
