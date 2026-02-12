package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/tjst-t/qemu-bmc/internal/config"
	"github.com/tjst-t/qemu-bmc/internal/ipmi"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
	"github.com/tjst-t/qemu-bmc/internal/redfish"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("qemu-bmc starting...")

	cfg := config.Load()

	// Connect to QMP
	qmpClient, err := qmp.NewClient(cfg.QMPSocket)
	if err != nil {
		log.Fatalf("Failed to connect to QMP socket %s: %v", cfg.QMPSocket, err)
	}
	defer qmpClient.Close()
	log.Println("Connected to QMP socket")

	// Create machine
	m := machine.New(qmpClient)

	// Start IPMI server
	ipmiServer := ipmi.NewServer(m, cfg.IPMIUser, cfg.IPMIPass)
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
}
