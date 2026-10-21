package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tjst-t/qemu-bmc/internal/bmc"
	"github.com/tjst-t/qemu-bmc/internal/config"
	"github.com/tjst-t/qemu-bmc/internal/ipmi"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qemu"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
	"github.com/tjst-t/qemu-bmc/internal/redfish"
	"github.com/tjst-t/qemu-bmc/internal/sysuuid"
)

var version = "dev"

func main() {
	flag.Parse()

	if len(os.Args) == 2 && os.Args[1] == "-v" {
		fmt.Println(version)
		os.Exit(0)
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("qemu-bmc %s starting...", version)

	cfg := config.Load()
	qemuArgs := flag.Args()

	var qmpClient qmp.Client
	var m *machine.Machine
	systemInfo := redfish.SystemInfo{MemoryMiB: cfg.VMMemoryMiB, CPUs: cfg.VMCPUs}
	var systemUUID string

	if len(qemuArgs) > 0 {
		// Process management mode
		log.Printf("Process management mode: managing QEMU lifecycle")

		systemUUID = resolveSystemUUID(cfg, qemuArgs)

		cmdArgs, err := qemu.BuildCommandLine(qemuArgs, qemu.BuildOptions{
			QMPSocketPath: cfg.QMPSocket,
			SerialAddr:    cfg.SerialAddr,
			SystemUUID:    systemUUID,
			MACAddresses:  cfg.VMMACs,
			// NICs without a configured MAC get one derived from the UUID,
			// so it is stable per node and differs between nodes
			DeriveMAC: sysuuid.MustParse(systemUUID).MAC,
		})
		if err != nil {
			log.Fatalf("Invalid QEMU arguments: %v", err)
		}
		// The QEMU command line is authoritative for the hardware we report
		hw := qemu.HardwareFromArgs(cmdArgs)
		systemInfo = redfish.SystemInfo{MemoryMiB: hw.MemoryMiB, CPUs: hw.CPUs, MACAddresses: hw.MACAddresses}
		for i, mac := range hw.MACAddresses {
			log.Printf("NIC %d MAC: %s", i, mac)
		}

		qmpClient = qmp.NewDisconnectedClient(cfg.QMPSocket)
		pm := qemu.NewProcessManager(cfg.QEMUBinary, cmdArgs, qemu.DefaultCommandFactory)
		m = machine.NewWithProcess(qmpClient, pm)
		// VM_CDROM (-cdrom) is the initial VirtualMedia content
		if image := qemu.InitialCDImage(cmdArgs); image != "" {
			log.Printf("VirtualMedia: initial image %s", image)
			m.SetInitialMedia(image)
		}
		// Report the firmware QEMU is started with (entrypoint.sh picks
		// OVMF only for VM_BOOT_MODE=uefi)
		if err := m.SetBootMode(cfg.RedfishBootMode()); err != nil {
			log.Fatalf("Invalid VM_BOOT_MODE: %v", err)
		}

		if cfg.PowerOnAtStart {
			log.Printf("Starting QEMU: %s %v", cfg.QEMUBinary, cmdArgs)
			if err := m.Reset("On"); err != nil {
				log.Fatalf("Failed to start QEMU: %v", err)
			}
		} else {
			log.Printf("POWER_ON_AT_START=false: QEMU will not start until powered on via IPMI/Redfish")
		}
	} else {
		// Legacy mode
		log.Printf("Legacy mode: connecting to existing QEMU instance")
		var err error
		qmpClient, err = qmp.NewClient(cfg.QMPSocket)
		if err != nil {
			log.Fatalf("Failed to connect to QMP socket %s: %v", cfg.QMPSocket, err)
		}
		log.Println("Connected to QMP socket")

		m = machine.New(qmpClient)

		// The external QEMU's UUID is unknown here, so only report one
		// when explicitly configured (it must match QEMU's -uuid).
		if cfg.SystemUUID != "" {
			u, err := sysuuid.Parse(cfg.SystemUUID)
			if err != nil {
				log.Fatalf("Invalid SYSTEM_UUID: %v", err)
			}
			systemUUID = u.String()
		}

		// Likewise, NICs are only reported when their MACs are configured
		// (they must match the external QEMU's NICs).
		for _, mac := range cfg.VMMACs {
			if mac == "" {
				continue
			}
			mac, err := qemu.NormalizeMAC(mac)
			if err != nil {
				log.Fatalf("Invalid VM_MACS: %v", err)
			}
			systemInfo.MACAddresses = append(systemInfo.MACAddresses, mac)
		}
	}
	defer qmpClient.Close()
	m.SetGracefulTimeout(cfg.GracefulTimeout)

	// Create BMC state
	bmcState := bmc.NewState(cfg.IPMIUser, cfg.IPMIPass)
	if systemUUID != "" {
		bmcState.SetSystemGUID(sysuuid.MustParse(systemUUID).IPMIGUID())
	}

	// Start VM IPMI server (only if configured)
	if cfg.VMIPMIAddr != "" {
		vmServer := ipmi.NewVMServer(m, bmcState)
		go func() {
			log.Printf("Starting VM IPMI server on %s", cfg.VMIPMIAddr)
			if err := vmServer.ListenAndServe(cfg.VMIPMIAddr); err != nil {
				log.Fatalf("VM IPMI server error: %v", err)
			}
		}()
	}

	// Start IPMI server
	ipmiServer := ipmi.NewServer(m, bmcState, cfg.IPMIUser, cfg.IPMIPass)
	go func() {
		addr := fmt.Sprintf(":%s", cfg.IPMIPort)
		log.Printf("Starting IPMI server on %s", addr)
		if err := ipmiServer.ListenAndServe(addr); err != nil {
			log.Fatalf("IPMI server error: %v", err)
		}
	}()

	// Start Redfish server
	redfishServer := redfish.NewServer(m, cfg.IPMIUser, cfg.IPMIPass, cfg.VNCAddr)
	redfishServer.SetSystemInfo(systemInfo)
	redfishServer.SetSystemUUID(systemUUID)
	addr := fmt.Sprintf(":%s", cfg.RedfishPort)
	log.Printf("Starting Redfish server on %s", addr)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: redfishServer,
	}

	go func() {
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			if err := httpServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Redfish server error: %v", err)
			}
		} else {
			log.Println("No TLS cert/key provided, generating self-signed certificate")
			cert, err := generateSelfSignedCert()
			if err != nil {
				log.Fatalf("Failed to generate self-signed certificate: %v", err)
			}
			httpServer.TLSConfig = &tls.Config{
				MinVersion:   tls.VersionTLS12,
				Certificates: []tls.Certificate{cert},
			}
			if err := httpServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Redfish server error: %v", err)
			}
		}
	}()

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received signal %s, shutting down...", sig)

	// Shutdown QEMU in process mode
	if len(qemuArgs) > 0 {
		log.Println("Stopping QEMU process...")
		if err := m.Reset("ForceOff"); err != nil {
			log.Printf("Error during QEMU shutdown: %v", err)
		}
		// Give process time to exit
		time.Sleep(500 * time.Millisecond)
	}
}

// resolveSystemUUID determines the system UUID for process management mode:
// SYSTEM_UUID, then -uuid in the QEMU args, then SYSTEM_UUID_FILE, then a
// UUID derived from the hostname (persisted to SYSTEM_UUID_FILE).
func resolveSystemUUID(cfg *config.Config, qemuArgs []string) string {
	explicit := cfg.SystemUUID
	if explicit == "" {
		explicit, _ = qemu.ArgValue(qemuArgs, "-uuid")
	}
	hostname, err := os.Hostname()
	if err != nil {
		log.Printf("WARN: could not determine hostname: %v", err)
	}
	u, src, err := sysuuid.Resolve(sysuuid.Options{
		Explicit: explicit,
		File:     cfg.SystemUUIDFile,
		Hostname: hostname,
	})
	if err != nil {
		log.Fatalf("Invalid system UUID: %v", err)
	}
	log.Printf("System UUID: %s (source: %s)", u, src)
	return u.String()
}
