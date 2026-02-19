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

	if len(qemuArgs) > 0 {
		// Process management mode
		log.Printf("Process management mode: managing QEMU lifecycle")

		cmdArgs, err := qemu.BuildCommandLine(qemuArgs, qemu.BuildOptions{
			QMPSocketPath: cfg.QMPSocket,
			SerialAddr:    cfg.SerialAddr,
		})
		if err != nil {
			log.Fatalf("Invalid QEMU arguments: %v", err)
		}

		qmpClient = qmp.NewDisconnectedClient(cfg.QMPSocket)
		pm := qemu.NewProcessManager(cfg.QEMUBinary, cmdArgs, qemu.DefaultCommandFactory)
		m = machine.NewWithProcess(qmpClient, pm)

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
	}
	defer qmpClient.Close()

	// Create BMC state
	bmcState := bmc.NewState(cfg.IPMIUser, cfg.IPMIPass)

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
	redfishServer := redfish.NewServer(m, cfg.IPMIUser, cfg.IPMIPass)
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
			// Generate self-signed cert for development
			log.Println("No TLS cert/key provided, generating self-signed certificate")
			tlsConfig := &tls.Config{
				MinVersion: tls.VersionTLS12,
			}
			// Fall back to HTTP for development
			httpServer.TLSConfig = tlsConfig
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
