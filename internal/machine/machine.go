package machine

import (
	"fmt"
	"log"
	"sync"
	"time"

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

// ProcessManager controls the QEMU process lifecycle.
type ProcessManager interface {
	Start(bootTarget string) error
	Stop(timeout time.Duration) error
	Kill() error
	IsRunning() bool
	WaitForExit(timeout time.Duration) error
}

// Machine manages the state of a QEMU VM
type Machine struct {
	qmpClient      qmp.Client
	processManager ProcessManager // nil = legacy mode
	bootOverride   BootOverride
	mu             sync.RWMutex
}

// New creates a new Machine with the given QMP client (legacy mode)
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

// NewWithProcess creates a Machine in process management mode
func NewWithProcess(client qmp.Client, pm ProcessManager) *Machine {
	return &Machine{
		qmpClient:      client,
		processManager: pm,
		bootOverride: BootOverride{
			Enabled: "Disabled",
			Target:  "None",
			Mode:    "UEFI",
		},
	}
}

// GetPowerState returns the current power state of the VM
func (m *Machine) GetPowerState() (PowerState, error) {
	if m.processManager != nil {
		return m.getPowerStateProcess()
	}
	return m.getPowerStateLegacy()
}

func (m *Machine) getPowerStateLegacy() (PowerState, error) {
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

func (m *Machine) getPowerStateProcess() (PowerState, error) {
	if !m.processManager.IsRunning() {
		return PowerOff, nil
	}

	status, err := m.qmpClient.QueryStatus()
	if err != nil {
		// QMP not ready yet, but process is running
		return PowerOn, nil
	}

	switch status {
	case qmp.StatusRunning, qmp.StatusPaused:
		return PowerOn, nil
	case qmp.StatusShutdown:
		// Guest has shut down — stop the process
		m.processManager.Stop(30 * time.Second)
		return PowerOff, nil
	default:
		return PowerOn, nil
	}
}

// GetQMPStatus returns the raw QMP status string
func (m *Machine) GetQMPStatus() (qmp.Status, error) {
	if m.processManager != nil {
		return m.getQMPStatusProcess()
	}
	return m.qmpClient.QueryStatus()
}

func (m *Machine) getQMPStatusProcess() (qmp.Status, error) {
	if !m.processManager.IsRunning() {
		return qmp.StatusShutdown, nil
	}

	status, err := m.qmpClient.QueryStatus()
	if err != nil {
		// QMP not ready yet, but process is running — report running
		return qmp.StatusRunning, nil
	}
	return status, nil
}

// Reset performs a reset action on the VM
func (m *Machine) Reset(resetType string) error {
	if m.processManager != nil {
		return m.resetProcessMode(resetType)
	}
	return m.resetLegacy(resetType)
}

func (m *Machine) resetLegacy(resetType string) error {
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

func (m *Machine) resetProcessMode(resetType string) error {
	switch resetType {
	case "On":
		if m.processManager.IsRunning() {
			return nil // already running
		}
		m.mu.RLock()
		target := m.bootOverride.Target
		m.mu.RUnlock()

		if err := m.processManager.Start(target); err != nil {
			return fmt.Errorf("starting QEMU: %w", err)
		}

		if err := m.waitForQMP(30 * time.Second); err != nil {
			return fmt.Errorf("waiting for QMP: %w", err)
		}

		m.ConsumeBootOnce()
		return nil

	case "ForceOff":
		if err := m.qmpClient.Quit(); err != nil {
			// QMP may not be connected; force-kill the process
			log.Printf("QMP quit failed (%v), killing process", err)
			return m.processManager.Stop(30 * time.Second)
		}
		return m.processManager.WaitForExit(30 * time.Second)

	case "ForceRestart":
		return m.qmpClient.SystemReset()

	case "GracefulShutdown":
		if err := m.qmpClient.SystemPowerdown(); err != nil {
			return err
		}
		if err := m.processManager.WaitForExit(120 * time.Second); err != nil {
			log.Printf("Graceful shutdown timed out: %v", err)
		}
		return nil

	case "GracefulRestart":
		if err := m.qmpClient.SystemPowerdown(); err != nil {
			return err
		}
		if err := m.processManager.WaitForExit(120 * time.Second); err != nil {
			log.Printf("Graceful shutdown timed out, killing: %v", err)
			m.processManager.Kill()
			m.processManager.WaitForExit(5 * time.Second)
		}
		return m.resetProcessMode("On")

	default:
		return fmt.Errorf("unsupported reset type: %s", resetType)
	}
}

// waitForQMP polls qmpClient.Connect() until success or timeout.
func (m *Machine) waitForQMP(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := m.qmpClient.Connect(); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("QMP connection timed out after %s", timeout)
		}
		time.Sleep(100 * time.Millisecond)
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
