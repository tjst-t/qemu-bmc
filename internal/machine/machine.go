package machine

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
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

const (
	// gracefulRestartTimeout bounds how long a GracefulRestart waits in the
	// background for the guest to power off before giving up.
	gracefulRestartTimeout = 120 * time.Second
	gracefulPollInterval   = 500 * time.Millisecond
)

// Machine manages the state of a QEMU VM
type Machine struct {
	qmpClient      qmp.Client
	processManager ProcessManager // nil = legacy mode
	bootOverride   BootOverride
	// runningBootTarget is the boot target the current QEMU process was
	// started with (process management mode only).
	runningBootTarget string
	mu                sync.RWMutex

	// resetMu serializes power actions, including the deferred power-on of
	// a GracefulRestart. resetGen counts Reset calls so that a pending
	// GracefulRestart is abandoned once another power action is requested.
	resetMu  sync.Mutex
	resetGen atomic.Uint64

	gracefulTimeout time.Duration
	pollInterval    time.Duration
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
		gracefulTimeout: gracefulRestartTimeout,
		pollInterval:    gracefulPollInterval,
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
		gracefulTimeout: gracefulRestartTimeout,
		pollInterval:    gracefulPollInterval,
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

// Reset performs a reset action on the VM.
//
// GracefulShutdown and GracefulRestart only send the ACPI power button event
// and return; like a physical BMC, the guest powers off afterwards (or not, if
// it ignores ACPI — the VM is never forced off). GracefulRestart powers the VM
// back on in the background once the guest has powered off.
func (m *Machine) Reset(resetType string) error {
	m.resetMu.Lock()
	defer m.resetMu.Unlock()
	m.resetGen.Add(1)

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
		if status, err := m.qmpClient.QueryStatus(); err == nil && status == qmp.StatusShutdown {
			// The guest powered off (QEMU -no-shutdown); QEMU must be reset
			// before it can run again.
			if err := m.qmpClient.SystemReset(); err != nil {
				return err
			}
		}
		return m.qmpClient.Cont()
	case "ForceOff":
		return m.qmpClient.Stop()
	case "GracefulShutdown":
		return m.qmpClient.SystemPowerdown()
	case "ForceRestart":
		return m.qmpClient.SystemReset()
	case "GracefulRestart":
		if err := m.qmpClient.SystemPowerdown(); err != nil {
			return err
		}
		go m.powerOnAfterGuestPowerOff(m.resetGen.Load())
		return nil
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
		target := m.nextBootTarget()
		m.mu.RUnlock()

		if err := m.processManager.Start(target); err != nil {
			return fmt.Errorf("starting QEMU: %w", err)
		}
		m.mu.Lock()
		m.runningBootTarget = target
		m.mu.Unlock()

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
		if m.processManager.IsRunning() && m.bootTargetChanged() {
			// -boot is fixed at QEMU startup, so system_reset would boot the
			// old device. Restart the process to apply the new boot target.
			if err := m.processManager.Stop(30 * time.Second); err != nil {
				return fmt.Errorf("stopping QEMU: %w", err)
			}
			return m.resetProcessMode("On")
		}
		// The running process already has the requested boot target, so
		// this reset uses it; consume Once so the next reset reverts.
		if err := m.qmpClient.SystemReset(); err != nil {
			return err
		}
		m.ConsumeBootOnce()
		return nil

	case "GracefulShutdown":
		// QEMU exits by itself once the guest powers off
		return m.qmpClient.SystemPowerdown()

	case "GracefulRestart":
		if err := m.qmpClient.SystemPowerdown(); err != nil {
			return err
		}
		go m.powerOnAfterGuestPowerOff(m.resetGen.Load())
		return nil

	default:
		return fmt.Errorf("unsupported reset type: %s", resetType)
	}
}

// powerOnAfterGuestPowerOff completes a GracefulRestart: it waits for the
// guest to power off and then powers the VM on again. It gives up, leaving the
// VM running, if the guest does not power off in time or another power action
// (identified by gen) is requested meanwhile.
func (m *Machine) powerOnAfterGuestPowerOff(gen uint64) {
	deadline := time.Now().Add(m.gracefulTimeout)
	for !m.guestPoweredOff() {
		if m.resetGen.Load() != gen {
			return
		}
		if time.Now().After(deadline) {
			log.Printf("GracefulRestart: guest did not power off within %s, leaving it running", m.gracefulTimeout)
			return
		}
		time.Sleep(m.pollInterval)
	}

	m.resetMu.Lock()
	defer m.resetMu.Unlock()
	if m.resetGen.Load() != gen {
		return
	}

	log.Printf("GracefulRestart: guest powered off, powering on")
	var err error
	if m.processManager != nil {
		// With -no-shutdown QEMU stays alive after the guest powers off
		if err = m.processManager.Stop(30 * time.Second); err == nil {
			err = m.resetProcessMode("On")
		}
	} else {
		err = m.resetLegacy("On")
	}
	if err != nil {
		log.Printf("GracefulRestart: powering on failed: %v", err)
	}
}

// guestPoweredOff reports whether the guest has powered off.
func (m *Machine) guestPoweredOff() bool {
	if m.processManager != nil && !m.processManager.IsRunning() {
		return true
	}
	status, err := m.qmpClient.QueryStatus()
	return err == nil && status == qmp.StatusShutdown
}

// bootTargetChanged reports whether the boot target for the next boot differs
// from the one the running QEMU process was started with.
func (m *Machine) bootTargetChanged() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	normalize := func(t string) string {
		if t == "" {
			return "None"
		}
		return t
	}
	return normalize(m.nextBootTarget()) != normalize(m.runningBootTarget)
}

// nextBootTarget returns the boot target for the next boot: the override
// target when the override is Once or Continuous, otherwise "None" (the
// default boot device). Callers must hold m.mu.
func (m *Machine) nextBootTarget() string {
	if m.bootOverride.Enabled == "Disabled" {
		return "None"
	}
	return m.bootOverride.Target
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
