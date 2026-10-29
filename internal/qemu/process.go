package qemu

import (
	"fmt"
	"log"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ProcessManager controls the lifecycle of a QEMU process.
type ProcessManager interface {
	// Start launches QEMU with the given boot target and the image in the
	// managed CD drive ("" = empty). For the UefiHttp target, httpBootImage
	// is the image to boot from a hidden CD drive ("" = boot from the
	// network); it is ignored for other targets. With secureBoot the
	// Secure Boot variant of the OVMF is booted (see
	// FindSecureBootFirmware).
	Start(bootTarget, cdImage, httpBootImage string, secureBoot bool) error
	// BootRevert returns what undoes, at run time, the boot override the
	// running QEMU was started with (see BootRevert).
	BootRevert() BootRevert
	Stop(timeout time.Duration) error
	Kill() error
	IsRunning() bool
	WaitForExit(timeout time.Duration) error
	ExitCh() <-chan struct{}
}

// CommandFactory creates exec.Cmd instances. Allows test injection.
type CommandFactory func(binary string, args []string) *exec.Cmd

// DefaultCommandFactory creates a standard exec.Cmd.
func DefaultCommandFactory(binary string, args []string) *exec.Cmd {
	return exec.Command(binary, args...)
}

type processManager struct {
	binary     string
	baseArgs   []string
	cmdFactory CommandFactory
	cmd        *exec.Cmd
	stderr     *lineLogger
	logf       func(format string, v ...any)
	running    bool
	exitCh     chan struct{}
	revert     BootRevert // undoes the boot override of the running QEMU
	mu         sync.RWMutex
}

// NewProcessManager creates a ProcessManager for the given QEMU binary and base arguments.
func NewProcessManager(binary string, baseArgs []string, factory CommandFactory) ProcessManager {
	return &processManager{
		binary:     binary,
		baseArgs:   baseArgs,
		cmdFactory: factory,
		logf:       log.Printf,
		exitCh:     make(chan struct{}),
	}
}

func (p *processManager) Start(bootTarget, cdImage, httpBootImage string, secureBoot bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return nil // already running, no-op
	}

	args := p.baseArgs
	if secureBoot {
		// First, as FindSecureBootFirmware's positions are in baseArgs
		fw, ok := FindSecureBootFirmware(args)
		if !ok {
			return fmt.Errorf("Secure Boot needs OVMF pflash drives (-drive if=pflash,...OVMF_CODE*.fd)")
		}
		if err := fw.Check(); err != nil {
			return err
		}
		if err := fw.PrepareVars(); err != nil {
			return fmt.Errorf("preparing Secure Boot VARS: %w", err)
		}
		args = fw.Apply(args)
	}
	args = SetCDImage(args, cdImage)
	before := append([]string(nil), args...)
	args = ApplyBootOverride(args, bootTarget)
	if bootTarget == "UefiHttp" {
		args = ApplyHTTPBoot(args, httpBootImage)
	}
	p.revert = bootOverrideRevert(before, args)
	p.cmd = p.cmdFactory(p.binary, args)
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p.stderr = nil
	if p.cmd.Stderr == nil {
		p.stderr = &lineLogger{logf: p.logf}
		p.cmd.Stderr = p.stderr
	}

	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("starting QEMU process: %w", err)
	}

	p.running = true
	p.exitCh = make(chan struct{})

	go p.monitor()
	return nil
}

func (p *processManager) monitor() {
	err := p.cmd.Wait()
	if p.stderr != nil {
		p.stderr.Flush()
	}
	if err != nil {
		p.logf("QEMU process exited: %v", err)
	}
	p.mu.Lock()
	p.running = false
	ch := p.exitCh
	p.mu.Unlock()
	close(ch)
}

func (p *processManager) Stop(timeout time.Duration) error {
	p.mu.RLock()
	if !p.running {
		p.mu.RUnlock()
		return nil
	}
	cmd := p.cmd
	p.mu.RUnlock()

	if cmd.Process == nil {
		return nil
	}

	// Send SIGTERM
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending SIGTERM: %w", err)
	}

	// Wait with timeout
	if err := p.WaitForExit(timeout); err != nil {
		// Timeout — force kill
		return p.Kill()
	}
	return nil
}

func (p *processManager) Kill() error {
	p.mu.RLock()
	if !p.running {
		p.mu.RUnlock()
		return nil
	}
	cmd := p.cmd
	p.mu.RUnlock()

	if cmd.Process == nil {
		return nil
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("sending SIGKILL: %w", err)
	}
	return nil
}

func (p *processManager) BootRevert() BootRevert {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.revert
}

func (p *processManager) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

func (p *processManager) WaitForExit(timeout time.Duration) error {
	p.mu.RLock()
	ch := p.exitCh
	p.mu.RUnlock()

	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for process exit after %s", timeout)
	}
}

func (p *processManager) ExitCh() <-chan struct{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exitCh
}
