package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds the application configuration
type Config struct {
	QMPSocket      string
	IPMIUser       string
	IPMIPass       string
	RedfishPort    string
	IPMIPort       string
	SerialAddr     string
	SerialLogFile  string
	TLSCert        string
	TLSKey         string
	VMBootMode     string
	VMIPMIAddr     string // VM IPMI chardev listen address
	QEMUBinary     string // QEMU binary path for process management mode
	PowerOnAtStart bool   // Power on VM at container start
	VNCAddr        string // VNC TCP address for noVNC proxy
	// Debug enables verbose logging of state changes (e.g. BIOS settings
	// applied/queued via Redfish). Driven by the DEBUG env var, which
	// docker/entrypoint.sh also reads for its own startup diagnostics.
	Debug bool
	// Redfish ComputerSystem inventory — required by clients like metal-operator
	// that key server discovery on ComputerSystem.UUID.
	SystemUUID         string
	SystemManufacturer string
	SystemModel        string
	SystemSerial       string
	// Redfish Manager inventory — clients like metal-operator (bmc_controller.go)
	// read Manager.Model and Manager.FirmwareVersion, not ComputerSystem's.
	ManagerModel          string
	SystemFirmwareVersion string
	ManagerManufacturer   string
	ManagerSerial         string
	ManagerPartNumber     string
	// Additional ComputerSystem inventory read by metal-operator's GetSystemInfo.
	SystemSKU         string
	SystemBiosVersion string
	// CPU/memory inventory surfaced via ComputerSystem.MemorySummary and the
	// Processors collection. Reuses VM_CPUS/VM_MEMORY (already consumed by
	// docker/entrypoint.sh to size the real QEMU VM) rather than separate env
	// vars, so the reported inventory can't drift from the actual VM.
	CPUModel  string
	CPUCount  int
	MemoryMiB int
	// GracefulShutdownTimeout bounds how long GracefulShutdown/GracefulRestart
	// wait for the guest to exit on its own before falling back to a hard
	// kill. Guests without a working ACPI power-button handler never react,
	// so this is the worst-case latency of every graceful reset against them.
	GracefulShutdownTimeout time.Duration
}

// Load reads configuration from environment variables with defaults
func Load() *Config {
	systemModel := getEnv("SYSTEM_MODEL", "Standard PC (Q35 + ICH9, 2009)")
	systemManufacturer := getEnv("SYSTEM_MANUFACTURER", "QEMU")
	systemSerial := getEnv("SYSTEM_SERIAL", "")
	return &Config{
		QMPSocket:               getEnv("QMP_SOCK", "/var/run/qemu/qmp.sock"),
		IPMIUser:                getEnv("IPMI_USER", "admin"),
		IPMIPass:                getEnv("IPMI_PASS", "password"),
		RedfishPort:             getEnv("REDFISH_PORT", "443"),
		IPMIPort:                getEnv("IPMI_PORT", "623"),
		SerialAddr:              getEnv("SERIAL_ADDR", "localhost:9002"),
		SerialLogFile:           getEnv("CONSOLE_LOG", "/vm/console.log"),
		TLSCert:                 getEnv("TLS_CERT", ""),
		TLSKey:                  getEnv("TLS_KEY", ""),
		VMBootMode:              getEnv("VM_BOOT_MODE", "bios"),
		VMIPMIAddr:              getEnv("VM_IPMI_ADDR", ""),
		QEMUBinary:              getEnv("QEMU_BINARY", "qemu-system-x86_64"),
		PowerOnAtStart:          getBoolEnv("POWER_ON_AT_START", false),
		VNCAddr:                 getEnv("VNC_ADDR", "localhost:5900"),
		Debug:                   getBoolEnv("DEBUG", false),
		SystemUUID:              getEnv("SYSTEM_UUID", ""),
		SystemManufacturer:      systemManufacturer,
		SystemModel:             systemModel,
		SystemSerial:            systemSerial,
		ManagerModel:            getEnv("SYSTEM_MANAGER_MODEL", systemModel),
		SystemFirmwareVersion:   getEnv("SYSTEM_FIRMWARE_VERSION", "1.0.0"),
		ManagerManufacturer:     getEnv("SYSTEM_MANAGER_MANUFACTURER", systemManufacturer),
		ManagerSerial:           getEnv("SYSTEM_MANAGER_SERIAL", systemSerial),
		ManagerPartNumber:       getEnv("SYSTEM_MANAGER_PART_NUMBER", ""),
		SystemSKU:               getEnv("SYSTEM_SKU", ""),
		SystemBiosVersion:       getEnv("SYSTEM_BIOS_VERSION", "1.0.0"),
		CPUModel:                getEnv("SYSTEM_CPU_MODEL", "QEMU Virtual CPU"),
		CPUCount:                getIntEnv("VM_CPUS", 2),
		MemoryMiB:               getIntEnv("VM_MEMORY", 2048),
		GracefulShutdownTimeout: getDurationEnv("GRACEFUL_SHUTDOWN_TIMEOUT", 120*time.Second),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getIntEnv(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

func getDurationEnv(key string, defaultValue time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return defaultValue
	}
	return parsed
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
