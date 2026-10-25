package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tjst-t/qemu-bmc/internal/config"
	"github.com/tjst-t/qemu-bmc/internal/qemu"
)

func TestSystemIdentity_DerivesSerialFromUUID(t *testing.T) {
	cfg := &config.Config{VMManufacturer: "QEMU", VMModel: "qemu-bmc"}
	id := systemIdentity(cfg, "0a1b2c3d-4e5f-4061-8273-8495a6b7c8d9")
	assert.Equal(t, qemu.SMBIOS{Manufacturer: "QEMU", Product: "qemu-bmc", Serial: "QBMC-0A1B2C3D4E"}, id)
}

func TestSystemIdentity_ExplicitSerial(t *testing.T) {
	cfg := &config.Config{VMSerialNumber: "SN-1", VMSKU: "SKU-1"}
	id := systemIdentity(cfg, "0a1b2c3d-4e5f-4061-8273-8495a6b7c8d9")
	assert.Equal(t, "SN-1", id.Serial)
	assert.Equal(t, "SKU-1", id.SKU)
}

func TestSystemIdentity_NoUUIDNoSerial(t *testing.T) {
	assert.Empty(t, systemIdentity(&config.Config{}, "").Serial)
}
