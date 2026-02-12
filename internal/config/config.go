package config

import "os"

// Config holds the application configuration
type Config struct {
	QMPSocket   string
	IPMIUser    string
	IPMIPass    string
	RedfishPort string
	IPMIPort    string
	SerialAddr  string
	TLSCert     string
	TLSKey      string
	VMBootMode  string
}

// Load reads configuration from environment variables with defaults
func Load() *Config {
	return &Config{
		QMPSocket:   getEnv("QMP_SOCK", "/var/run/qemu/qmp.sock"),
		IPMIUser:    getEnv("IPMI_USER", "admin"),
		IPMIPass:    getEnv("IPMI_PASS", "password"),
		RedfishPort: getEnv("REDFISH_PORT", "443"),
		IPMIPort:    getEnv("IPMI_PORT", "623"),
		SerialAddr:  getEnv("SERIAL_ADDR", "localhost:9002"),
		TLSCert:     getEnv("TLS_CERT", ""),
		TLSKey:      getEnv("TLS_KEY", ""),
		VMBootMode:  getEnv("VM_BOOT_MODE", "bios"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
