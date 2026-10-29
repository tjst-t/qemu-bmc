package qemu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBootOverrideRevert_CdUEFI(t *testing.T) {
	before := []string{"-drive", "if=pflash,format=raw,readonly=on,file=/OVMF_CODE.fd",
		"-device", "ide-cd,drive=" + CDDriveID + ",id=" + CDDeviceID,
		"-device", "virtio-net-pci,netdev=net0"}
	after := ApplyBootOverride(append([]string(nil), before...), "Cd")
	r := bootOverrideRevert(before, after)
	assert.Equal(t, []string{CDDeviceID}, r.Devices)
	assert.Equal(t, "cad", r.Order) // -boot d added, no -boot before
	assert.True(t, r.DisksFirst)

	// BIOS: -boot is enough
	before = before[2:]
	after = ApplyBootOverride(append([]string(nil), before...), "Cd")
	assert.False(t, bootOverrideRevert(before, after).DisksFirst)
}

func TestBootOverrideRevert_KeepsUserBootOrder(t *testing.T) {
	before := []string{"-boot", "order=c,menu=on", "-device", "ide-cd,drive=" + CDDriveID + ",id=" + CDDeviceID}
	after := ApplyBootOverride(append([]string(nil), before...), "Cd")
	assert.Equal(t, "c", bootOverrideRevert(before, after).Order)

	before = []string{"-boot", "c"}
	after = ApplyBootOverride(append([]string(nil), before...), "Pxe")
	assert.Equal(t, BootRevert{Order: "c"}, bootOverrideRevert(before, after))
}

func TestBootOverrideRevert_NICGetsID(t *testing.T) {
	before := []string{"-drive", "if=pflash,format=raw,readonly=on,file=/OVMF_CODE.fd",
		"-netdev", "tap,id=net0", "-device", "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01"}
	after := ApplyBootOverride(append([]string(nil), before...), "Pxe")
	r := bootOverrideRevert(before, after)
	assert.Equal(t, []string{"qbmc-boot5"}, r.Devices)
	assert.Equal(t, "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01,bootindex=0,id=qbmc-boot5", after[5])
}

func TestBootOverrideRevert_HTTPBootImage(t *testing.T) {
	before := []string{"-drive", "if=pflash,format=raw,readonly=on,file=/OVMF_CODE.fd"}
	after := ApplyHTTPBoot(ApplyBootOverride(append([]string(nil), before...), "UefiHttp"), "/vm/h.iso")
	assert.Equal(t, []string{HTTPBootDeviceID}, bootOverrideRevert(before, after).Devices)
}

func TestBootOverrideRevert_NoOverride(t *testing.T) {
	before := []string{"-boot", "c", "-device", "ide-cd,drive=" + CDDriveID + ",id=" + CDDeviceID}
	after := ApplyBootOverride(append([]string(nil), before...), "None")
	assert.True(t, bootOverrideRevert(before, after).Empty())

	// The user's own bootindex=0 is left alone
	before = []string{"-device", "virtio-blk-pci,drive=d0,bootindex=0", "-device", "ide-cd,drive=" + CDDriveID + ",id=" + CDDeviceID}
	after = ApplyBootOverride(append([]string(nil), before...), "Cd")
	r := bootOverrideRevert(before, after)
	assert.Empty(t, r.Devices)
}
