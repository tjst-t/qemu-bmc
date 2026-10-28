package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/tjst-t/qemu-bmc/internal/bios"
	"github.com/tjst-t/qemu-bmc/internal/bmc"
	"github.com/tjst-t/qemu-bmc/internal/config"
	"github.com/tjst-t/qemu-bmc/internal/fault"
	"github.com/tjst-t/qemu-bmc/internal/firmware"
	"github.com/tjst-t/qemu-bmc/internal/ipmi"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qemu"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
	"github.com/tjst-t/qemu-bmc/internal/redfish"
	"github.com/tjst-t/qemu-bmc/internal/serial"
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
	var serialConsole *serial.Mux // nil in legacy mode
	var biosStore *bios.Store
	// Simulated BMC and BIOS firmware versions (UpdateService)
	firmwareStore := startFirmware(cfg)

	if len(qemuArgs) > 0 {
		// Process management mode
		log.Printf("Process management mode: managing QEMU lifecycle")

		systemUUID = resolveSystemUUID(cfg, qemuArgs)
		identity = systemIdentity(cfg, systemUUID)

		// Listening before QEMU can start: its serial chardev connects to
		// the socket at every start
		serialConsole = startSerial(cfg)

		cmdArgs, err := qemu.BuildCommandLine(qemuArgs, qemu.BuildOptions{
			QMPSocketPath: cfg.QMPSocket,
			SerialSocket:  cfg.SerialSocket,
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

		// Before the first power on: pending BIOS settings and BIOS
		// firmware apply at boot
		biosStore = startBios(cfg, m)
		setBootHook(m, biosStore, firmwareStore)

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
		biosStore = startBios(cfg, m)
		setBootHook(m, biosStore, firmwareStore)
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

	// Fault injection (FAULT_API_ADDR, FAULTS); nil if disabled
	faults := startFaults(cfg)
	if faults != nil {
		m.SetGracefulFilter(faults.IgnoreGraceful)
	}
	// What the Redfish and IPMI servers control: power actions go through
	// the power-action-* faults
	var bmcMachine interface {
		redfish.MachineInterface
		ipmi.MachineInterface
	} = m
	if faults != nil {
		bmcMachine = faultMachine{m, faults}
	}

	// Create BMC state
	bmcState := bmc.NewState(cfg.IPMIUser, cfg.IPMIPass)
	if systemUUID != "" {
		bmcState.SetSystemGUID(sysuuid.MustParse(systemUUID).IPMIGUID())
	}

	// Start VM IPMI server (only if configured)
	if cfg.VMIPMIAddr != "" {
		vmServer := ipmi.NewVMServer(bmcMachine, bmcState)
		go func() {
			log.Printf("Starting VM IPMI server on %s", cfg.VMIPMIAddr)
			if err := vmServer.ListenAndServe(cfg.VMIPMIAddr); err != nil {
				log.Fatalf("VM IPMI server error: %v", err)
			}
		}()
	}

	// Start IPMI server
	ipmiServer := ipmi.NewServer(bmcMachine, bmcState, cfg.IPMIUser, cfg.IPMIPass)
	ipmiServer.SetFaults(faults)
	if serialConsole != nil {
		// IPMI SOL subscribes to the console like any other client
		ipmiServer.SetSerialConsole(serialConsole)
	}
	go func() {
		addr := fmt.Sprintf(":%s", cfg.IPMIPort)
		log.Printf("Starting IPMI server on %s", addr)
		if err := ipmiServer.ListenAndServe(addr); err != nil {
			log.Fatalf("IPMI server error: %v", err)
		}
	}()

	// Start Redfish server
	redfishServer := redfish.NewServer(bmcMachine, cfg.IPMIUser, cfg.IPMIPass, cfg.VNCAddr)
	redfishServer.SetSystemInfo(systemInfo)
	redfishServer.SetUserStore(bmcState)
	redfishServer.SetSystemUUID(systemUUID)
	redfishServer.SetFirmwareVersion(version)
	redfishServer.SetBios(biosStore)
	redfishServer.SetFirmware(firmwareStore)
	redfishServer.SetFirmwareFaults(faults)
	redfishServer.SetFirmwareUpdateDuration(cfg.FirmwareUpdateDuration)
	// SimpleUpdate images are downloaded like VirtualMedia images
	redfishServer.SetFirmwareDownload(firmware.DownloadOptions{
		InsecureTLS: cfg.VMediaInsecureTLS,
		Timeout:     cfg.VMediaTimeout,
	})
	if serialConsole != nil {
		redfishServer.SetSerialLog(serialConsole)
		if port, err := strconv.Atoi(cfg.IPMIPort); err == nil {
			redfishServer.SetIPMISerialConsole(port)
		}
	}
	// Power state and setting changes, from Redfish, IPMI or the guest, are
	// Redfish events
	redfishServer.SetEventInsecureTLS(cfg.EventInsecureTLS)
	redfishServer.SetEventSource(m)
	addr := fmt.Sprintf(":%s", cfg.RedfishPort)
	log.Printf("Starting Redfish server on %s", addr)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: redfishServer,
	}
	if faults != nil {
		httpServer.Handler = faults.Middleware(redfishServer)
		// HTTP/1.1 only: an HTTP/2 connection cannot be hijacked, so
		// redfish-drop could only reset the stream, not the connection
		httpServer.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
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

// faultMachine runs the power actions of the Redfish and IPMI servers
// through the power-action-fail and power-action-ignored faults.
type faultMachine struct {
	*machine.Machine
	faults *fault.Store
}

func (f faultMachine) Reset(resetType string) error {
	return f.faults.Reset(resetType, f.Machine.Reset)
}

// startFaults sets up fault injection: the faults of FAULTS and the fault
// API on FAULT_API_ADDR. It returns nil if both are unset.
func startFaults(cfg *config.Config) *fault.Store {
	if cfg.FaultAPIAddr == "" && cfg.Faults == "" {
		return nil
	}
	faults := fault.NewStore()
	if cfg.Faults != "" {
		list, err := fault.Parse(cfg.Faults)
		if err != nil {
			log.Fatalf("Invalid FAULTS: %v", err)
		}
		for _, f := range list {
			if _, err := faults.Add(f); err != nil {
				log.Fatalf("Invalid FAULTS: %v", err)
			}
		}
	}
	if cfg.FaultAPIAddr != "" {
		if cfg.FaultAPIToken == "" {
			log.Printf("WARN: FAULT_API_TOKEN is not set, the fault API needs no authentication")
		}
		srv := &http.Server{
			Addr:              cfg.FaultAPIAddr,
			Handler:           faults.APIHandler(cfg.FaultAPIToken),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			log.Printf("Starting fault API on %s (HTTP)", cfg.FaultAPIAddr)
			if err := srv.ListenAndServe(); err != nil {
				log.Fatalf("Fault API server error: %v", err)
			}
		}()
	}
	return faults
}

// startBios loads the BIOS attributes (BIOS_ATTRIBUTES defaults, saved
// values) and applies the pending settings each time the VM boots. BootMode
// reflects VM_BOOT_MODE, SecureBoot the SecureBootEnable setting.
func startBios(cfg *config.Config, m *machine.Machine) *bios.Store {
	store, err := bios.NewStore(cfg.BiosFile(), cfg.BiosAttributes)
	if err != nil {
		log.Fatalf("Invalid BIOS_ATTRIBUTES: %v", err)
	}
	bootMode := "Bios"
	if cfg.RedfishBootMode() == "UEFI" {
		bootMode = "Uefi"
	}
	store.SetReadOnlyValue("BootMode", func() string { return bootMode })
	store.SetReadOnlyValue("SecureBoot", func() string {
		if m.GetSecureBoot().Enable {
			return "Enabled"
		}
		return "Disabled"
	})
	log.Printf("BIOS attributes: %s", cfg.BiosFile())
	return store
}

// startFirmware loads the simulated firmware versions: BMC_FIRMWARE_VERSION
// (default: the qemu-bmc version) and BIOS_FIRMWARE_VERSION, overridden by
// the versions installed by updates (saved in FirmwareFile).
func startFirmware(cfg *config.Config) *firmware.Store {
	bmcVersion := cfg.BMCFirmwareVersion
	if bmcVersion == "" {
		bmcVersion = version
	}
	store := firmware.NewStore(cfg.FirmwareFile(), bmcVersion, cfg.BIOSFirmwareVersion)
	log.Printf("Firmware: BMC %s, BIOS %s (%s)", store.Version(firmware.TargetBMC), store.Version(firmware.TargetBIOS), cfg.FirmwareFile())
	return store
}

// setBootHook applies the pending BIOS settings and BIOS firmware each time
// the VM boots.
func setBootHook(m *machine.Machine, biosStore *bios.Store, fw *firmware.Store) {
	m.SetBootHook(func() {
		biosStore.Apply()
		fw.Apply()
	})
}

// startSerial sets up the serial console multiplexer (process management
// mode): QEMU's serial chardev connects to the internal UNIX socket
// SerialSocket, and the console is served to any number of TCP clients (up
// to serial.DefaultMaxConns) on SERIAL_ADDR.
func startSerial(cfg *config.Config) *serial.Mux {
	console := serial.NewMux(cfg.SerialBufferBytes)
	if err := os.MkdirAll(filepath.Dir(cfg.SerialSocket), 0o755); err != nil {
		log.Fatalf("Serial console: %v", err)
	}
	l, err := serial.ListenUnix(cfg.SerialSocket)
	if err != nil {
		log.Fatalf("Serial console: %v", err)
	}
	go func() {
		if err := console.ServeQEMU(l); err != nil {
			log.Fatalf("Serial console socket error: %v", err)
		}
	}()

	if cfg.SerialAddr != "" {
		tcp, err := net.Listen("tcp", cfg.SerialAddr)
		if err != nil {
			log.Fatalf("Serial console: %v", err)
		}
		srv := serial.NewServer(console, serial.DefaultMaxConns, cfg.SerialReplay)
		go func() {
			log.Printf("Starting serial console on %s (buffer %d bytes, replay %t)", cfg.SerialAddr, cfg.SerialBufferBytes, cfg.SerialReplay)
			if err := srv.Serve(tcp); err != nil {
				log.Fatalf("Serial console server error: %v", err)
			}
		}()
	}
	return console
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
