package redfish

import (
	"log"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/novnc"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// MachineInterface defines what the Redfish server needs from the machine layer
type MachineInterface interface {
	GetPowerState() (machine.PowerState, error)
	GetQMPStatus() (qmp.Status, error)
	Reset(resetType string) error
	GetBootOverride() machine.BootOverride
	SetBootOverride(override machine.BootOverride) error
	InsertMedia(image string) error
	EjectMedia() error
}

// Inventory holds the static ComputerSystem/Manager/Processor inventory data
// surfaced by the Redfish API. It's set once via SetInventory before the server
// starts handling requests, so it needs no locking.
type Inventory struct {
	// ComputerSystem inventory surfaced at /redfish/v1/Systems/1.
	SystemUUID         string
	SystemManufacturer string
	SystemModel        string
	SystemSerial       string
	SystemSKU          string
	SystemBiosVersion  string
	// Manager inventory surfaced at /redfish/v1/Managers/1. Clients such as
	// metal-operator read Manager.Model/FirmwareVersion, not ComputerSystem's.
	ManagerModel           string
	ManagerFirmwareVersion string
	ManagerManufacturer    string
	ManagerSerial          string
	ManagerPartNumber      string
	// Processor inventory surfaced at /redfish/v1/Systems/1/Processors/1.
	CPUModel string
	CPUCount int
	// Memory inventory surfaced at /redfish/v1/Systems/1 (MemorySummary).
	MemoryMiB int
}

// Server is the Redfish HTTP server
type Server struct {
	router       *mux.Router
	machine      MachineInterface
	user         string
	pass         string
	novncHandler *novnc.Handler
	inventory    Inventory
	// debug, when set (via SetDebug from the DEBUG env var), makes the server
	// log state changes such as BIOS settings applied/queued. Set once before
	// serving, like inventory, so it needs no locking.
	debug bool

	// mu guards the runtime-mutable fields below. Chainsaw's own test runs are
	// serialized (--parallel 1), but the server itself may be polled/patched
	// concurrently by other real clients (Ironic, metal-operator, a human curl).
	mu               sync.RWMutex
	currentMedia     string
	indicatorLED     string
	lastResetTime    time.Time
	biosAttrs        map[string]any
	biosPendingAttrs map[string]any
}

// SetInventory populates the static ComputerSystem/Manager/Processor inventory
// returned by the Redfish API. Clients such as metal-operator require a
// non-empty ComputerSystem UUID for server discovery.
func (s *Server) SetInventory(inv Inventory) {
	s.inventory = inv
}

// SetDebug enables verbose logging of state changes (BIOS settings, etc.).
// Driven by the DEBUG env var; off by default so tests and normal runs stay quiet.
func (s *Server) SetDebug(enabled bool) {
	s.debug = enabled
}

// debugf logs a formatted line only when debug logging is enabled (DEBUG=true).
func (s *Server) debugf(format string, args ...any) {
	if !s.debug {
		return
	}
	log.Printf(format, args...)
}

func (s *Server) getCurrentMedia() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentMedia
}

func (s *Server) setCurrentMedia(image string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentMedia = image
}

func (s *Server) getIndicatorLED() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.indicatorLED
}

func (s *Server) setIndicatorLED(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indicatorLED = state
}

func (s *Server) getLastResetTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastResetTime
}

func (s *Server) setLastResetTime(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastResetTime = t
}

func (s *Server) getBiosAttributes() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]any, len(s.biosAttrs))
	maps.Copy(out, s.biosAttrs)
	return out
}

func (s *Server) setBiosAttribute(name string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.biosAttrs[name] = value
}

func (s *Server) getBiosPendingAttributes() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]any, len(s.biosPendingAttrs))
	maps.Copy(out, s.biosPendingAttrs)
	return out
}

func (s *Server) mergeBiosPendingAttributes(attrs map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(s.biosPendingAttrs, attrs)
}

// applyPendingBiosSettings copies any pending (reboot-required) BIOS
// attribute changes into the live attribute set and clears the pending set.
// Called after a reset that leaves the VM powered on, mirroring a real BMC
// applying settings during POST.
func (s *Server) applyPendingBiosSettings() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	applied := maps.Clone(s.biosPendingAttrs)
	maps.Copy(s.biosAttrs, s.biosPendingAttrs)
	s.biosPendingAttrs = map[string]any{}
	return applied
}

// NewServer creates a new Redfish server
func NewServer(m MachineInterface, user, pass, vncAddr string) *Server {
	s := &Server{
		router:       mux.NewRouter(),
		machine:      m,
		user:         user,
		pass:         pass,
		novncHandler: novnc.NewHandler(vncAddr),
		indicatorLED: "Off",
		biosAttrs: map[string]any{
			"AdminPhone": "",
			"BootMode":   "Uefi",
		},
		biosPendingAttrs: map[string]any{},
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// Apply middleware
	s.router.Use(s.trailingSlashMiddleware)
	if s.user != "" && s.pass != "" {
		s.router.Use(s.basicAuthMiddleware)
	}

	// Service Root
	s.router.HandleFunc("/redfish/v1", s.handleServiceRoot).Methods("GET")
	s.router.HandleFunc("/redfish/v1/", s.handleServiceRoot).Methods("GET")

	// Systems
	s.router.HandleFunc("/redfish/v1/Systems", s.handleSystemCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/", s.handleSystemCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}", s.handleGetSystem).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/", s.handleGetSystem).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}", s.handlePatchSystem).Methods("PATCH")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/", s.handlePatchSystem).Methods("PATCH")

	// Actions
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Actions/ComputerSystem.Reset", s.handleResetAction).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Actions/ComputerSystem.Reset/", s.handleResetAction).Methods("POST")

	// Processors
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Processors", s.handleProcessorCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Processors/", s.handleProcessorCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Processors/{procid}", s.handleGetProcessor).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Processors/{procid}/", s.handleGetProcessor).Methods("GET")

	// Bios
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Bios", s.handleGetBios).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Bios/", s.handleGetBios).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Bios/Settings", s.handleGetBiosSettings).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Bios/Settings/", s.handleGetBiosSettings).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Bios/Settings", s.handlePatchBiosSettings).Methods("PATCH")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Bios/Settings/", s.handlePatchBiosSettings).Methods("PATCH")

	// Registries
	s.router.HandleFunc("/redfish/v1/Registries", s.handleRegistryCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Registries/", s.handleRegistryCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Registries/{id}.json", s.handleGetRegistryContent).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Registries/{id}.json/", s.handleGetRegistryContent).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Registries/{id}", s.handleGetRegistryFile).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Registries/{id}/", s.handleGetRegistryFile).Methods("GET")

	// Managers
	s.router.HandleFunc("/redfish/v1/Managers", s.handleManagerCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/", s.handleManagerCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}", s.handleGetManager).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/", s.handleGetManager).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/Actions/Manager.Reset", s.handleManagerReset).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/Actions/Manager.Reset/", s.handleManagerReset).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia", s.handleVirtualMediaCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/", s.handleVirtualMediaCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}", s.handleGetVirtualMedia).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/", s.handleGetVirtualMedia).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/Actions/VirtualMedia.InsertMedia", s.handleInsertMedia).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/Actions/VirtualMedia.InsertMedia/", s.handleInsertMedia).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/Actions/VirtualMedia.EjectMedia", s.handleEjectMedia).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/Actions/VirtualMedia.EjectMedia/", s.handleEjectMedia).Methods("POST")

	// Chassis
	s.router.HandleFunc("/redfish/v1/Chassis", s.handleChassisCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Chassis/", s.handleChassisCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Chassis/{id}", s.handleGetChassis).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Chassis/{id}/", s.handleGetChassis).Methods("GET")

	// noVNC: redirect /novnc/ → /novnc/vnc.html, serve static files, and WebSocket proxy
	s.router.HandleFunc("/novnc/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/novnc/vnc.html", http.StatusFound)
	}).Methods("GET")
	s.router.PathPrefix("/novnc/").Handler(
		http.StripPrefix("/novnc/", s.novncHandler.ServeFiles()),
	)
	s.router.HandleFunc("/websockify", s.novncHandler.ServeWebSocket)
}

// ServeHTTP implements the http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}
