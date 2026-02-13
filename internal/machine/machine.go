package machine

import (
	"fmt"
	"sync"

	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// PowerState represents the power state of the VM
type PowerState string

const (
	PowerOn  PowerState = "On"
	PowerOff PowerState = "Off"
)

// BootOverride represents boot source override settings
type BootOverride struct {
	Enabled string // "Disabled", "Once", "Continuous"
	Target  string // "None", "Pxe", "Hdd", "Cd", "BiosSetup"
	Mode    string // "UEFI", "Legacy"
}

// Machine manages the state of a QEMU VM
type Machine struct {
	qmpClient    qmp.Client
	bootOverride BootOverride
	mu           sync.RWMutex
}

// New creates a new Machine with the given QMP client
func New(client qmp.Client) *Machine {
	return &Machine{
		qmpClient: client,
		bootOverride: BootOverride{
			Enabled: "Disabled",
			Target:  "None",
			Mode:    "UEFI",
		},
	}
}

// GetPowerState returns the current power state of the VM
func (m *Machine) GetPowerState() (PowerState, error) {
	status, err := m.qmpClient.QueryStatus()
	if err != nil {
		return "", fmt.Errorf("querying VM status: %w", err)
	}

	switch status {
	case qmp.StatusRunning:
		return PowerOn, nil
	default:
		return PowerOff, nil
	}
}

// GetQMPStatus returns the raw QMP status string
func (m *Machine) GetQMPStatus() (qmp.Status, error) {
	return m.qmpClient.QueryStatus()
}

// Reset performs a reset action on the VM
func (m *Machine) Reset(resetType string) error {
	switch resetType {
	case "On":
		state, err := m.GetPowerState()
		if err != nil {
			return err
		}
		if state == PowerOn {
			return nil // already on, no-op
		}
		return m.qmpClient.Cont()
	case "ForceOff":
		return m.qmpClient.Stop()
	case "GracefulShutdown":
		// Send ACPI shutdown signal, then stop the VM.
		// The stop ensures the VM halts even without a guest OS.
		m.qmpClient.SystemPowerdown()
		return m.qmpClient.Stop()
	case "ForceRestart":
		return m.qmpClient.SystemReset()
	case "GracefulRestart":
		return m.qmpClient.SystemReset()
	default:
		return fmt.Errorf("unsupported reset type: %s", resetType)
	}
}

// GetBootOverride returns the current boot override settings
func (m *Machine) GetBootOverride() BootOverride {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bootOverride
}

// SetBootOverride sets the boot override settings
func (m *Machine) SetBootOverride(override BootOverride) error {
	// Validate target
	validTargets := map[string]bool{
		"None": true, "Pxe": true, "Hdd": true, "Cd": true, "BiosSetup": true,
	}
	if !validTargets[override.Target] {
		return fmt.Errorf("invalid boot target: %s", override.Target)
	}

	// Validate enabled
	validEnabled := map[string]bool{
		"Disabled": true, "Once": true, "Continuous": true,
	}
	if !validEnabled[override.Enabled] {
		return fmt.Errorf("invalid boot enabled: %s", override.Enabled)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.bootOverride = override
	return nil
}

// ConsumeBootOnce consumes a "Once" boot override (resets to Disabled after use)
func (m *Machine) ConsumeBootOnce() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bootOverride.Enabled == "Once" {
		m.bootOverride.Enabled = "Disabled"
		m.bootOverride.Target = "None"
	}
}

// InsertMedia inserts virtual media into the VM
func (m *Machine) InsertMedia(image string) error {
	return m.qmpClient.BlockdevChangeMedium("ide0-cd0", image)
}

// EjectMedia ejects virtual media from the VM
func (m *Machine) EjectMedia() error {
	return m.qmpClient.BlockdevRemoveMedium("ide0-cd0")
}
