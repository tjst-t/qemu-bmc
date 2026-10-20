package config

import (
	"os"

	"github.com/tjst-t/qemu-bmc/internal/qemu"
)

// Config holds the application configuration
type Config struct {
	QMPSocket      string
	IPMIUser       string
	IPMIPass       string
	RedfishPort    string
	IPMIPort       string
	SerialAddr     string
	TLSCert        string
	TLSKey         string
	VMBootMode     string
	VMIPMIAddr     string // VM IPMI chardev listen address
	QEMUBinary     string // QEMU binary path for process management mode
	PowerOnAtStart bool   // Power on VM at container start
	VNCAddr        string // VNC TCP address for noVNC proxy
	VMMemoryMiB    int    // VM memory in MiB (VM_MEMORY), reported via Redfish; 0 if unparsable
	VMCPUs         int    // VM CPU count (VM_CPUS), reported via Redfish; 0 if unparsable
	SystemUUID     string // Fixed system UUID (QEMU -uuid, Redfish UUID, IPMI GUID)
	SystemUUIDFile string // File where a derived system UUID is persisted
}

// Load reads configuration from environment variables with defaults
func Load() *Config {
	return &Config{
		QMPSocket:      getEnv("QMP_SOCK", "/var/run/qemu/qmp.sock"),
		IPMIUser:       getEnv("IPMI_USER", "admin"),
		IPMIPass:       getEnv("IPMI_PASS", "password"),
		RedfishPort:    getEnv("REDFISH_PORT", "443"),
		IPMIPort:       getEnv("IPMI_PORT", "623"),
		SerialAddr:     getEnv("SERIAL_ADDR", "localhost:9002"),
		TLSCert:        getEnv("TLS_CERT", ""),
		TLSKey:         getEnv("TLS_KEY", ""),
		VMBootMode:     getEnv("VM_BOOT_MODE", "bios"),
		VMIPMIAddr:     getEnv("VM_IPMI_ADDR", ""),
		QEMUBinary:     getEnv("QEMU_BINARY", "qemu-system-x86_64"),
		PowerOnAtStart: getBoolEnv("POWER_ON_AT_START", false),
		VNCAddr:        getEnv("VNC_ADDR", "localhost:5900"),
		SystemUUID:     getEnv("SYSTEM_UUID", ""),
		SystemUUIDFile: getEnv("SYSTEM_UUID_FILE", ""),
		// entrypoint.sh passes these to QEMU as -m / -smp, so parse them the
		// same way QEMU does
		VMMemoryMiB: qemu.ParseMemoryMiB(getEnv("VM_MEMORY", "2048")),
		VMCPUs:      qemu.ParseSMPCPUs(getEnv("VM_CPUS", "2")),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getBoolEnv(key string, defaultValue bool) bool {
	value := os.Getenv(key)
	switch value {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return defaultValue
	}
}
