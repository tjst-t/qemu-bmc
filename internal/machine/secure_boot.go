package machine

import (
	"errors"
	"fmt"

	"github.com/tjst-t/qemu-bmc/internal/qemu"
)

// ErrSecureBootUnsupported is returned by the Secure Boot methods when the
// VM does not boot OVMF from pflash drives in process management mode.
var ErrSecureBootUnsupported = errors.New("Secure Boot needs process management mode and OVMF pflash drives (VM_BOOT_MODE=uefi)")

// SecureBoot is the Secure Boot state reported on Systems/1/SecureBoot.
type SecureBoot struct {
	// Enable is the setting for the next boot.
	Enable bool
	// CurrentBoot reports whether the running (or last started) QEMU booted
	// the Secure Boot firmware.
	CurrentBoot bool
}

// secureBootState is the Secure Boot setting of a machine supporting it.
type secureBootState struct {
	fw     *qemu.SecureBootFirmware // nil = unsupported
	enable bool
	// running reports whether the running (or last started) QEMU booted the
	// Secure Boot firmware; keysReset that its VARS were reset since.
	running   bool
	keysReset bool
}

// SetSecureBootFirmware makes Secure Boot available (process management
// mode only), with fw the Secure Boot variant of the VM's OVMF.
func (m *Machine) SetSecureBootFirmware(fw qemu.SecureBootFirmware) error {
	if m.processManager == nil {
		return ErrSecureBootUnsupported
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secureBoot.fw = &fw
	return nil
}

// SecureBootSupported reports whether Secure Boot can be used.
func (m *Machine) SecureBootSupported() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.secureBoot.fw != nil
}

// GetSecureBoot returns the Secure Boot state.
func (m *Machine) GetSecureBoot() SecureBoot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return SecureBoot{Enable: m.secureBoot.enable, CurrentBoot: m.secureBoot.running}
}

// SetSecureBootEnable sets whether QEMU boots the Secure Boot firmware. It
// applies from the next time QEMU is started (a reset of a running VM
// restarts QEMU if it changed). Enabling fails if the Secure Boot firmware
// files are missing.
func (m *Machine) SetSecureBootEnable(enable bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.secureBoot.fw == nil {
		return ErrSecureBootUnsupported
	}
	if enable {
		if err := m.secureBoot.fw.Check(); err != nil {
			return err
		}
	}
	m.secureBoot.enable = enable
	return nil
}

// ResetSecureBootKeys resets the Secure Boot VARS to the template (with the
// default keys). A running QEMU booted with them keeps its copy until it is
// reset, which then restarts it.
func (m *Machine) ResetSecureBootKeys() error {
	// Serialize with power actions so that a starting QEMU gets either VARS
	m.resetMu.Lock()
	defer m.resetMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.secureBoot.fw == nil {
		return ErrSecureBootUnsupported
	}
	if err := m.secureBoot.fw.ResetVars(); err != nil {
		return fmt.Errorf("resetting Secure Boot keys: %w", err)
	}
	if m.secureBoot.running && m.processManager.IsRunning() {
		m.secureBoot.keysReset = true
	}
	return nil
}
