package config

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	SerialSocket   string // internal UNIX socket QEMU's serial chardev connects to (process mode)
	TLSCert        string
	TLSKey         string
	VMBootMode     string
	VMIPMIAddr     string   // VM IPMI chardev listen address
	QEMUBinary     string   // QEMU binary path for process management mode
	PowerOnAtStart bool     // Power on VM at container start
	VNCAddr        string   // VNC TCP address for noVNC proxy
	VMMemoryMiB    int      // VM memory in MiB (VM_MEMORY), reported via Redfish; 0 if unparsable
	VMCPUs         int      // VM CPU count (VM_CPUS), reported via Redfish; 0 if unparsable
	SystemUUID     string   // Fixed system UUID (QEMU -uuid, Redfish UUID, IPMI GUID)
	SystemUUIDFile string   // File where a derived system UUID is persisted
	VMMACs         []string // Per-NIC MAC addresses (VM_MACS); empty entries are derived from the system UUID
	VMSecureBoot   bool     // Initial SecureBootEnable (VM_SECURE_BOOT)

	// System identity put into the guest's SMBIOS (process management mode)
	// and reported on Redfish Systems/1 and Chassis/1. An empty
	// VMSerialNumber is derived from the system UUID; an empty VMSKU is
	// not reported.
	VMManufacturer string // VM_MANUFACTURER
	VMModel        string // VM_MODEL
	VMSerialNumber string // VM_SERIAL_NUMBER
	VMSKU          string // VM_SKU

	// The serial console keeps the last SerialBufferBytes of output
	// (SERIAL_BUFFER_BYTES, 0 = none); with SerialReplay (SERIAL_REPLAY) a
	// new SERIAL_ADDR connection gets it first
	SerialBufferBytes int
	SerialReplay      bool

	// GracefulTimeout bounds how long GracefulShutdown/GracefulRestart
	// report PoweringOff before going back to On (GRACEFUL_TIMEOUT, seconds)
	GracefulTimeout time.Duration

	// VirtualMedia http(s) images are downloaded to VMediaDir (VMEDIA_DIR),
	// skipping certificate verification if VMediaInsecureTLS
	// (VMEDIA_INSECURE_TLS); a power on waits up to VMediaTimeout
	// (VMEDIA_TIMEOUT, seconds) for a download in progress
	VMediaDir         string
	VMediaInsecureTLS bool
	VMediaTimeout     time.Duration

	// EventInsecureTLS (EVENT_INSECURE_TLS) skips certificate verification
	// when delivering Redfish events to https subscription destinations
	EventInsecureTLS bool

	// BiosAttributes (BIOS_ATTRIBUTES) is a JSON object replacing the
	// defaults of the Redfish Bios attributes (saved to BiosFile)
	BiosAttributes string

	// Fault injection: the fault API listens on FaultAPIAddr
	// (FAULT_API_ADDR, disabled if empty) and needs the bearer token
	// FaultAPIToken (FAULT_API_TOKEN) if set; Faults (FAULTS) is a JSON
	// array of the faults active at start
	FaultAPIAddr  string
	FaultAPIToken string
	Faults        string
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
		SerialSocket:   filepath.Join(filepath.Dir(getEnv("QMP_SOCK", "/var/run/qemu/qmp.sock")), "serial.sock"),
		TLSCert:        getEnv("TLS_CERT", ""),
		TLSKey:         getEnv("TLS_KEY", ""),
		VMBootMode:     getEnv("VM_BOOT_MODE", "bios"),
		VMIPMIAddr:     getEnv("VM_IPMI_ADDR", ""),
		QEMUBinary:     getEnv("QEMU_BINARY", "qemu-system-x86_64"),
		PowerOnAtStart: getBoolEnv("POWER_ON_AT_START", false),
		VNCAddr:        getEnv("VNC_ADDR", "localhost:5900"),
		SystemUUID:     getEnv("SYSTEM_UUID", ""),
		SystemUUIDFile: getEnv("SYSTEM_UUID_FILE", ""),
		VMMACs:         getListEnv("VM_MACS"),
		VMSecureBoot:   getBoolEnv("VM_SECURE_BOOT", false),
		VMManufacturer: getEnv("VM_MANUFACTURER", "QEMU"),
		VMModel:        getEnv("VM_MODEL", "qemu-bmc"),
		VMSerialNumber: getEnv("VM_SERIAL_NUMBER", ""),
		VMSKU:          getEnv("VM_SKU", ""),

		SerialBufferBytes: getSizeEnv("SERIAL_BUFFER_BYTES", 256*1024),
		SerialReplay:      getBoolEnv("SERIAL_REPLAY", false),

		GracefulTimeout: getSecondsEnv("GRACEFUL_TIMEOUT", 120*time.Second),

		VMediaDir:         getEnv("VMEDIA_DIR", defaultVMediaDir("/vm")),
		VMediaInsecureTLS: getBoolEnv("VMEDIA_INSECURE_TLS", true),
		VMediaTimeout:     getSecondsEnv("VMEDIA_TIMEOUT", 10*time.Minute),

		BiosAttributes: getEnv("BIOS_ATTRIBUTES", ""),

		EventInsecureTLS: getBoolEnv("EVENT_INSECURE_TLS", true),

		FaultAPIAddr:  getEnv("FAULT_API_ADDR", ""),
		FaultAPIToken: getEnv("FAULT_API_TOKEN", ""),
		Faults:        getEnv("FAULTS", ""),
		// entrypoint.sh passes these to QEMU as -m / -smp, so parse them the
		// same way QEMU does
		VMMemoryMiB: qemu.ParseMemoryMiB(getEnv("VM_MEMORY", "2048")),
		VMCPUs:      qemu.ParseSMPCPUs(getEnv("VM_CPUS", "2")),
	}
}

// BiosFile is where the Redfish Bios attributes are saved: next to the
// VirtualMedia downloads.
func (c *Config) BiosFile() string {
	return filepath.Join(c.VMediaDir, "bios-settings.json")
}

// defaultVMediaDir returns <base>/vmedia if base is a writable directory,
// else /tmp/vmedia.
func defaultVMediaDir(base string) string {
	if fi, err := os.Stat(base); err == nil && fi.IsDir() && syscall.Access(base, 2 /* W_OK */) == nil {
		return filepath.Join(base, "vmedia")
	}
	return "/tmp/vmedia"
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getListEnv returns the comma-separated entries of key, trimmed, or nil if
// it is unset or empty. Empty entries are kept to preserve positions.
func getListEnv(key string) []string {
	value := os.Getenv(key)
	if strings.TrimSpace(value) == "" {
		return nil
	}
	items := strings.Split(value, ",")
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
	}
	return items
}

// getSecondsEnv returns key as a positive number of seconds, or
// defaultValue if it is unset or invalid.
func getSecondsEnv(key string, defaultValue time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		log.Printf("Invalid %s=%q, using %s", key, value, defaultValue)
		return defaultValue
	}
	return time.Duration(n) * time.Second
}

// getSizeEnv returns key as a non-negative number of bytes, or
// defaultValue if it is unset or invalid.
func getSizeEnv(key string, defaultValue int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		log.Printf("Invalid %s=%q, using %d", key, value, defaultValue)
		return defaultValue
	}
	return n
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

// RedfishBootMode maps VM_BOOT_MODE ("bios"/"uefi") to the Redfish
// BootSourceOverrideMode value ("Legacy"/"UEFI"). Unknown values map to
// "Legacy", matching entrypoint.sh, which falls back to BIOS.
func (c *Config) RedfishBootMode() string {
	if strings.EqualFold(c.VMBootMode, "uefi") {
		return "UEFI"
	}
	return "Legacy"
}
