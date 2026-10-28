package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any env vars that might be set
	for _, key := range []string{"QMP_SOCK", "IPMI_USER", "IPMI_PASS", "REDFISH_PORT", "IPMI_PORT", "SERIAL_ADDR", "TLS_CERT", "TLS_KEY", "VM_BOOT_MODE", "VM_IPMI_ADDR", "QEMU_BINARY", "POWER_ON_AT_START", "SYSTEM_UUID", "SYSTEM_UUID_FILE", "VM_MACS", "GRACEFUL_TIMEOUT", "VMEDIA_DIR", "VMEDIA_INSECURE_TLS", "VMEDIA_TIMEOUT", "VM_SECURE_BOOT", "VM_MANUFACTURER", "VM_MODEL", "VM_SERIAL_NUMBER", "VM_SKU", "SERIAL_BUFFER_BYTES", "SERIAL_REPLAY"} {
		os.Unsetenv(key)
	}

	cfg := Load()
	assert.Equal(t, "/var/run/qemu/qmp.sock", cfg.QMPSocket)
	assert.Equal(t, "admin", cfg.IPMIUser)
	assert.Equal(t, "password", cfg.IPMIPass)
	assert.Equal(t, "443", cfg.RedfishPort)
	assert.Equal(t, "623", cfg.IPMIPort)
	assert.Equal(t, "localhost:9002", cfg.SerialAddr)
	assert.Equal(t, "/var/run/qemu/serial.sock", cfg.SerialSocket)
	assert.Equal(t, 256*1024, cfg.SerialBufferBytes)
	assert.False(t, cfg.SerialReplay)
	assert.Equal(t, "", cfg.TLSCert)
	assert.Equal(t, "", cfg.TLSKey)
	assert.Equal(t, "bios", cfg.VMBootMode)
	assert.Equal(t, "", cfg.VMIPMIAddr)
	assert.Equal(t, "qemu-system-x86_64", cfg.QEMUBinary)
	assert.Equal(t, false, cfg.PowerOnAtStart)
	assert.Equal(t, "", cfg.SystemUUID)
	assert.Equal(t, "", cfg.SystemUUIDFile)
	assert.Nil(t, cfg.VMMACs)
	assert.False(t, cfg.VMSecureBoot)
	assert.Equal(t, "QEMU", cfg.VMManufacturer)
	assert.Equal(t, "qemu-bmc", cfg.VMModel)
	assert.Equal(t, "", cfg.VMSerialNumber)
	assert.Equal(t, "", cfg.VMSKU)
	assert.Equal(t, 120*time.Second, cfg.GracefulTimeout)
	assert.Equal(t, defaultVMediaDir("/vm"), cfg.VMediaDir)
	assert.True(t, cfg.VMediaInsecureTLS)
	assert.Equal(t, 10*time.Minute, cfg.VMediaTimeout)
	assert.True(t, cfg.EventInsecureTLS)
}

func TestLoad_SystemIdentity(t *testing.T) {
	t.Setenv("VM_MANUFACTURER", "Acme, Inc.")
	t.Setenv("VM_MODEL", "R100")
	t.Setenv("VM_SERIAL_NUMBER", "SN-1")
	t.Setenv("VM_SKU", "SKU-1")
	cfg := Load()
	assert.Equal(t, "Acme, Inc.", cfg.VMManufacturer)
	assert.Equal(t, "R100", cfg.VMModel)
	assert.Equal(t, "SN-1", cfg.VMSerialNumber)
	assert.Equal(t, "SKU-1", cfg.VMSKU)
}

func TestLoad_Serial(t *testing.T) {
	t.Setenv("QMP_SOCK", "/run/bmc/qmp.sock")
	t.Setenv("SERIAL_BUFFER_BYTES", "4096")
	t.Setenv("SERIAL_REPLAY", "true")
	cfg := Load()
	assert.Equal(t, "/run/bmc/serial.sock", cfg.SerialSocket)
	assert.Equal(t, 4096, cfg.SerialBufferBytes)
	assert.True(t, cfg.SerialReplay)

	t.Setenv("SERIAL_BUFFER_BYTES", "0")
	assert.Equal(t, 0, Load().SerialBufferBytes)
	t.Setenv("SERIAL_BUFFER_BYTES", "-1")
	assert.Equal(t, 256*1024, Load().SerialBufferBytes)
	t.Setenv("SERIAL_BUFFER_BYTES", "big")
	assert.Equal(t, 256*1024, Load().SerialBufferBytes)
}

func TestLoad_VMedia(t *testing.T) {
	t.Setenv("VMEDIA_DIR", "/data/iso")
	t.Setenv("VMEDIA_INSECURE_TLS", "false")
	t.Setenv("VMEDIA_TIMEOUT", "30")
	cfg := Load()
	assert.Equal(t, "/data/iso", cfg.VMediaDir)
	assert.False(t, cfg.VMediaInsecureTLS)
	assert.Equal(t, 30*time.Second, cfg.VMediaTimeout)
}

func TestLoad_EventInsecureTLS(t *testing.T) {
	t.Setenv("EVENT_INSECURE_TLS", "false")
	assert.False(t, Load().EventInsecureTLS)
}

func TestLoad_Bios(t *testing.T) {
	t.Setenv("VMEDIA_DIR", "/data/vm")
	t.Setenv("BIOS_ATTRIBUTES", `{"NumLock":"Off"}`)
	cfg := Load()
	assert.Equal(t, `{"NumLock":"Off"}`, cfg.BiosAttributes)
	assert.Equal(t, "/data/vm/bios-settings.json", cfg.BiosFile())
}

func TestLoad_Firmware(t *testing.T) {
	for _, key := range []string{"BMC_FIRMWARE_VERSION", "BIOS_FIRMWARE_VERSION", "FIRMWARE_UPDATE_SECONDS"} {
		t.Setenv(key, "")
	}
	cfg := Load()
	assert.Equal(t, "", cfg.BMCFirmwareVersion)
	assert.Equal(t, "QBMC-BIOS-1.0.0", cfg.BIOSFirmwareVersion)
	assert.Equal(t, 10*time.Second, cfg.FirmwareUpdateDuration)

	t.Setenv("VMEDIA_DIR", "/data/vm")
	t.Setenv("BMC_FIRMWARE_VERSION", "5.0")
	t.Setenv("BIOS_FIRMWARE_VERSION", "B-2")
	t.Setenv("FIRMWARE_UPDATE_SECONDS", "3")
	cfg = Load()
	assert.Equal(t, "5.0", cfg.BMCFirmwareVersion)
	assert.Equal(t, "B-2", cfg.BIOSFirmwareVersion)
	assert.Equal(t, 3*time.Second, cfg.FirmwareUpdateDuration)
	assert.Equal(t, "/data/vm/firmware.json", cfg.FirmwareFile())
}

func TestDefaultVMediaDir(t *testing.T) {
	dir := t.TempDir()
	assert.Equal(t, filepath.Join(dir, "vmedia"), defaultVMediaDir(dir))
	assert.Equal(t, "/tmp/vmedia", defaultVMediaDir(filepath.Join(dir, "missing")))
	if os.Geteuid() != 0 {
		readOnly := filepath.Join(dir, "ro")
		require.NoError(t, os.Mkdir(readOnly, 0o555))
		assert.Equal(t, "/tmp/vmedia", defaultVMediaDir(readOnly))
	}
}

func TestLoad_GracefulTimeout(t *testing.T) {
	defer os.Unsetenv("GRACEFUL_TIMEOUT")
	os.Setenv("GRACEFUL_TIMEOUT", "10")
	assert.Equal(t, 10*time.Second, Load().GracefulTimeout)
	for _, invalid := range []string{"abc", "0", "-5", "1.5"} {
		os.Setenv("GRACEFUL_TIMEOUT", invalid)
		assert.Equal(t, 120*time.Second, Load().GracefulTimeout, invalid)
	}
}

func TestLoad_VMMACs(t *testing.T) {
	// Entries are per NIC in order; an empty entry means "derive"
	os.Setenv("VM_MACS", " 52:54:00:11:22:33 ,,02:00:00:00:00:01")
	defer os.Unsetenv("VM_MACS")
	cfg := Load()
	assert.Equal(t, []string{"52:54:00:11:22:33", "", "02:00:00:00:00:01"}, cfg.VMMACs)
}

func TestLoad_VMSecureBoot(t *testing.T) {
	t.Setenv("VM_SECURE_BOOT", "true")
	assert.True(t, Load().VMSecureBoot)
}

func TestLoad_SystemUUID(t *testing.T) {
	os.Setenv("SYSTEM_UUID", "12345678-9abc-4def-8123-456789abcdef")
	os.Setenv("SYSTEM_UUID_FILE", "/vm/system-uuid")
	defer os.Unsetenv("SYSTEM_UUID")
	defer os.Unsetenv("SYSTEM_UUID_FILE")
	cfg := Load()
	assert.Equal(t, "12345678-9abc-4def-8123-456789abcdef", cfg.SystemUUID)
	assert.Equal(t, "/vm/system-uuid", cfg.SystemUUIDFile)
}

func TestLoad_QEMUBinary_Custom(t *testing.T) {
	os.Setenv("QEMU_BINARY", "/usr/bin/qemu-system-aarch64")
	defer os.Unsetenv("QEMU_BINARY")

	cfg := Load()
	assert.Equal(t, "/usr/bin/qemu-system-aarch64", cfg.QEMUBinary)
}

func TestLoad_CustomValues(t *testing.T) {
	os.Setenv("QMP_SOCK", "/tmp/test.sock")
	os.Setenv("IPMI_USER", "testuser")
	os.Setenv("IPMI_PASS", "testpass")
	defer func() {
		os.Unsetenv("QMP_SOCK")
		os.Unsetenv("IPMI_USER")
		os.Unsetenv("IPMI_PASS")
	}()

	cfg := Load()
	assert.Equal(t, "/tmp/test.sock", cfg.QMPSocket)
	assert.Equal(t, "testuser", cfg.IPMIUser)
	assert.Equal(t, "testpass", cfg.IPMIPass)
}

func TestLoad_VMIPMIAddr(t *testing.T) {
	os.Setenv("VM_IPMI_ADDR", ":9002")
	defer os.Unsetenv("VM_IPMI_ADDR")
	cfg := Load()
	assert.Equal(t, ":9002", cfg.VMIPMIAddr)
}

func TestLoad_VMIPMIAddr_Default(t *testing.T) {
	os.Unsetenv("VM_IPMI_ADDR")
	cfg := Load()
	assert.Equal(t, "", cfg.VMIPMIAddr)
}

func TestLoad_PowerOnAtStart_Default(t *testing.T) {
	os.Unsetenv("POWER_ON_AT_START")
	cfg := Load()
	assert.Equal(t, false, cfg.PowerOnAtStart)
}

func TestLoad_PowerOnAtStart_True(t *testing.T) {
	os.Setenv("POWER_ON_AT_START", "true")
	defer os.Unsetenv("POWER_ON_AT_START")
	cfg := Load()
	assert.Equal(t, true, cfg.PowerOnAtStart)
}

func TestLoad_PowerOnAtStart_False(t *testing.T) {
	os.Setenv("POWER_ON_AT_START", "false")
	defer os.Unsetenv("POWER_ON_AT_START")
	cfg := Load()
	assert.Equal(t, false, cfg.PowerOnAtStart)
}

func TestLoad_VMHardware_Default(t *testing.T) {
	os.Unsetenv("VM_MEMORY")
	os.Unsetenv("VM_CPUS")
	cfg := Load()
	assert.Equal(t, 2048, cfg.VMMemoryMiB)
	assert.Equal(t, 2, cfg.VMCPUs)
}

func TestLoad_VMHardware_Custom(t *testing.T) {
	os.Setenv("VM_MEMORY", "8192")
	os.Setenv("VM_CPUS", "4")
	defer os.Unsetenv("VM_MEMORY")
	defer os.Unsetenv("VM_CPUS")
	cfg := Load()
	assert.Equal(t, 8192, cfg.VMMemoryMiB)
	assert.Equal(t, 4, cfg.VMCPUs)
}

func TestLoad_VMHardware_Invalid(t *testing.T) {
	// Unparsable values must not be reported as the 2048/2 defaults, which
	// would not match the VM; zero means unknown and is omitted by Redfish.
	os.Setenv("VM_MEMORY", "lots")
	os.Setenv("VM_CPUS", "-1")
	defer os.Unsetenv("VM_MEMORY")
	defer os.Unsetenv("VM_CPUS")
	cfg := Load()
	assert.Equal(t, 0, cfg.VMMemoryMiB)
	assert.Equal(t, 0, cfg.VMCPUs)
}

func TestLoad_VMHardware_QEMUSyntax(t *testing.T) {
	tests := []struct {
		memory    string
		cpus      string
		memoryMiB int
		cpuCount  int
	}{
		{"4G", "4", 4096, 4},
		{"512M", "cpus=6,sockets=1", 512, 6},
		{"size=8G,maxmem=16G", "sockets=2,cores=2", 8192, 4},
	}
	for _, tt := range tests {
		t.Run(tt.memory+"/"+tt.cpus, func(t *testing.T) {
			os.Setenv("VM_MEMORY", tt.memory)
			os.Setenv("VM_CPUS", tt.cpus)
			defer os.Unsetenv("VM_MEMORY")
			defer os.Unsetenv("VM_CPUS")
			cfg := Load()
			assert.Equal(t, tt.memoryMiB, cfg.VMMemoryMiB)
			assert.Equal(t, tt.cpuCount, cfg.VMCPUs)
		})
	}
}

func TestRedfishBootMode(t *testing.T) {
	for in, want := range map[string]string{"bios": "Legacy", "uefi": "UEFI", "UEFI": "UEFI", "": "Legacy", "other": "Legacy"} {
		c := &Config{VMBootMode: in}
		assert.Equal(t, want, c.RedfishBootMode(), "VM_BOOT_MODE=%q", in)
	}
}

func TestLoad_Faults(t *testing.T) {
	t.Setenv("FAULT_API_ADDR", "")
	t.Setenv("FAULT_API_TOKEN", "")
	t.Setenv("FAULTS", "")
	cfg := Load()
	assert.Equal(t, "", cfg.FaultAPIAddr, "disabled by default")
	assert.Equal(t, "", cfg.FaultAPIToken)
	assert.Equal(t, "", cfg.Faults)

	t.Setenv("FAULT_API_ADDR", ":8623")
	t.Setenv("FAULT_API_TOKEN", "secret")
	t.Setenv("FAULTS", `[{"kind":"ipmi-drop"}]`)
	cfg = Load()
	assert.Equal(t, ":8623", cfg.FaultAPIAddr)
	assert.Equal(t, "secret", cfg.FaultAPIToken)
	assert.Equal(t, `[{"kind":"ipmi-drop"}]`, cfg.Faults)
}
