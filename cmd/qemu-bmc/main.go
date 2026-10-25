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
	var identity qemu.SMBIOS

	if len(qemuArgs) > 0 {
		// Process management mode
		log.Printf("Process management mode: managing QEMU lifecycle")

		systemUUID = resolveSystemUUID(cfg, qemuArgs)
		identity = systemIdentity(cfg, systemUUID)

		cmdArgs, err := qemu.BuildCommandLine(qemuArgs, qemu.BuildOptions{
			QMPSocketPath: cfg.QMPSocket,
			SerialAddr:    cfg.SerialAddr,
			SystemUUID:    systemUUID,
			MACAddresses:  cfg.VMMACs,
			// NICs without a configured MAC get one derived from the UUID,
			// so it is stable per node and differs between nodes
			DeriveMAC: sysuuid.MustParse(systemUUID).MAC,
			// The guest sees the same identity as Redfish reports (a
			// -smbios type the user passes wins)
			SMBIOS: identity,
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
		systemInfo.Disks = probeDisks(cmdArgs)

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
		// Secure Boot switches the OVMF pflash drives to their Secure Boot
		// variant
		if fw, ok := qemu.FindSecureBootFirmware(cmdArgs); ok {
			if err := m.SetSecureBootFirmware(fw); err != nil {
				log.Fatalf("Secure Boot: %v", err)
			}
			if err := fw.Check(); err != nil {
				log.Printf("WARN: Secure Boot cannot be enabled: %v", err)
			}
		}
		if cfg.VMSecureBoot {
			if err := m.SetSecureBootEnable(true); err != nil {
				log.Fatalf("VM_SECURE_BOOT=true: %v", err)
			}
			log.Printf("Secure Boot enabled (VM_SECURE_BOOT)")
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
		if cfg.VMSecureBoot {
			log.Printf("WARN: VM_SECURE_BOOT is ignored in legacy mode: %v", machine.ErrSecureBootUnsupported)
		}

		// The external QEMU's UUID is unknown here, so only report one
		// when explicitly configured (it must match QEMU's -uuid).
		if cfg.SystemUUID != "" {
			u, err := sysuuid.Parse(cfg.SystemUUID)
			if err != nil {
				log.Fatalf("Invalid SYSTEM_UUID: %v", err)
			}
			systemUUID = u.String()
		}
		// The external QEMU's SMBIOS cannot be changed; only report it
		identity = systemIdentity(cfg, systemUUID)

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
	systemInfo.Manufacturer = identity.Manufacturer
	systemInfo.Model = identity.Product
	systemInfo.SerialNumber = identity.Serial
	systemInfo.SKU = identity.SKU
	log.Printf("System identity: manufacturer=%q model=%q serial=%q sku=%q",
		identity.Manufacturer, identity.Product, identity.Serial, identity.SKU)
	m.SetGracefulTimeout(cfg.GracefulTimeout)
	m.SetMediaOptions(machine.MediaOptions{
		Dir:         cfg.VMediaDir,
		InsecureTLS: cfg.VMediaInsecureTLS,
		Timeout:     cfg.VMediaTimeout,
	})
	m.CleanStaleDownloads()

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
	redfishServer.SetUserStore(bmcState)
	redfishServer.SetSystemUUID(systemUUID)
	redfishServer.SetFirmwareVersion(version)
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

// probeDisks finds the guest hard disks in the QEMU args and reads their
// size with qemu-img. A disk whose size cannot be read is reported without
// one.
func probeDisks(cmdArgs []string) []redfish.Disk {
	var disks []redfish.Disk
	for i, d := range qemu.DisksFromArgs(cmdArgs) {
		size, err := qemu.ImageVirtualSize("qemu-img", d.File, d.Format)
		if err != nil {
			log.Printf("WARN: disk %d size unknown: %v", i, err)
		} else {
			log.Printf("Disk %d: %s (%d bytes)", i, d.File, size)
		}
		disks = append(disks, redfish.Disk{CapacityBytes: size, Protocol: d.Protocol})
	}
	return disks
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

// systemIdentity returns the system identity from VM_MANUFACTURER, VM_MODEL,
// VM_SERIAL_NUMBER and VM_SKU. Without VM_SERIAL_NUMBER the serial number is
// derived from the system UUID (none if the UUID is unknown).
func systemIdentity(cfg *config.Config, systemUUID string) qemu.SMBIOS {
	id := qemu.SMBIOS{
		Manufacturer: cfg.VMManufacturer,
		Product:      cfg.VMModel,
		Serial:       cfg.VMSerialNumber,
		SKU:          cfg.VMSKU,
	}
	if id.Serial == "" && systemUUID != "" {
		id.Serial = sysuuid.MustParse(systemUUID).SerialNumber()
	}
	return id
}
