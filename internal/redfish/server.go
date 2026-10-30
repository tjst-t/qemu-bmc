package redfish

import (
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/tjst-t/qemu-bmc/internal/audit"
	"github.com/tjst-t/qemu-bmc/internal/firmware"
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
	GetHTTPBootURI() string
	SetHTTPBootURI(uri string) error
	HTTPBootSupported() bool
	GetMedia() machine.Media
	InsertMedia(image, username, password string) error
	EjectMedia() error
	SecureBootSupported() bool
	GetSecureBoot() machine.SecureBoot
	SetSecureBootEnable(enable bool) error
	ResetSecureBootKeys() error
}

// Server is the Redfish HTTP server
type Server struct {
	router       *mux.Router
	machine      MachineInterface
	user         string
	pass         string
	novncHandler *novnc.Handler
	systemInfo   SystemInfo
	systemUUID   string
	firmware     string
	sessions     *sessionStore
	users        UserStore
	events       *eventService
	serialLog    SerialLog
	bios         BiosStore // nil = no Systems/1/Bios
	solPort      int       // IPMI port SOL is served on, 0 if none
	tasks        *taskService
	fw           FirmwareStore // nil = no UpdateService
	fwFaults     FirmwareFaults
	fwDownload   firmware.DownloadOptions
	fwDuration   time.Duration
}

// UserStore authenticates BMC users in addition to the configured
// IPMI_USER/IPMI_PASS (implemented by *bmc.State).
type UserStore interface {
	Authenticate(name, password string) bool
}

// SystemInfo is the static VM hardware configuration reported on Systems/1.
// Zero values are treated as unknown and omitted from responses.
type SystemInfo struct {
	MemoryMiB    int
	CPUs         int
	MACAddresses []string // per-NIC MAC, in QEMU command-line order
	Disks        []Disk   // guest hard disks, in QEMU command-line order

	// System identity, the same as in the guest's SMBIOS; reported on
	// Systems/1 and Chassis/1 (SKU on Systems/1 only)
	Manufacturer string
	Model        string
	SerialNumber string
	SKU          string
}

// Disk is a guest hard disk reported under Systems/1 Storage and
// SimpleStorage.
type Disk struct {
	CapacityBytes int64  // 0 if unknown (omitted)
	Protocol      string // Redfish Protocol, empty if unknown (omitted)
}

// NewServer creates a new Redfish server
func NewServer(m MachineInterface, user, pass, vncAddr string) *Server {
	s := &Server{
		router:       mux.NewRouter(),
		machine:      m,
		user:         user,
		pass:         pass,
		novncHandler: novnc.NewHandler(vncAddr),
		sessions:     newSessionStore(),
		events:       newEventService(),
		tasks:        newTaskService(),
		fwDownload:   firmware.DownloadOptions{InsecureTLS: true, Timeout: machine.DefaultMediaTimeout},
		fwDuration:   DefaultFirmwareUpdateDuration,
	}
	s.setupRoutes()
	return s
}

// SetSystemUUID sets the UUID reported in Systems/1.
func (s *Server) SetSystemUUID(uuid string) {
	s.systemUUID = uuid
}

// SetUserStore makes Redfish authentication also accept the enabled users of
// the given store (e.g. users created in-band via the VM IPMI interface).
func (s *Server) SetUserStore(users UserStore) {
	s.users = users
}

// SetFirmwareVersion sets the qemu-bmc version reported as the Managers/1
// FirmwareVersion when no firmware store is set (SetFirmware).
func (s *Server) SetFirmwareVersion(version string) {
	s.firmware = version
}

func (s *Server) setupRoutes() {
	// Apply middleware
	s.router.Use(s.trailingSlashMiddleware)
	if s.authEnabled() {
		s.router.Use(s.authMiddleware)
	}

	// Version document
	s.router.HandleFunc("/redfish", s.handleVersions).Methods("GET")
	s.router.HandleFunc("/redfish/", s.handleVersions).Methods("GET")

	// Service Root
	s.router.HandleFunc("/redfish/v1", s.handleServiceRoot).Methods("GET")
	s.router.HandleFunc("/redfish/v1/", s.handleServiceRoot).Methods("GET")

	// Sessions
	for _, p := range []string{"", "/"} {
		s.router.HandleFunc("/redfish/v1/SessionService"+p, s.handleGetSessionService).Methods("GET")
		s.router.HandleFunc("/redfish/v1/SessionService"+p, s.handlePatchSessionService).Methods("PATCH")
		s.router.HandleFunc(sessionsPath+p, s.handleSessionCollection).Methods("GET")
		s.router.HandleFunc(sessionsPath+p, s.handleCreateSession).Methods("POST")
		s.router.HandleFunc(sessionsPath+"/{sid}"+p, s.handleGetSession).Methods("GET")
		s.router.HandleFunc(sessionsPath+"/{sid}"+p, s.handleDeleteSession).Methods("DELETE")
	}

	// Events
	for _, p := range []string{"", "/"} {
		sub := subscriptionsPath + "/{subid}"
		s.router.HandleFunc(eventServicePath+p, s.handleGetEventService).Methods("GET")
		s.router.HandleFunc(eventServicePath+p, s.handlePatchEventService).Methods("PATCH")
		s.router.HandleFunc(eventServicePath+"/Actions/EventService.SubmitTestEvent"+p, s.handleSubmitTestEvent).Methods("POST")
		s.router.HandleFunc(ssePath+p, s.handleSSE).Methods("GET")
		s.router.HandleFunc(subscriptionsPath+p, s.handleSubscriptionCollection).Methods("GET")
		s.router.HandleFunc(subscriptionsPath+p, s.handleCreateSubscription).Methods("POST")
		s.router.HandleFunc(sub+p, s.handleGetSubscription).Methods("GET")
		s.router.HandleFunc(sub+p, s.handlePatchSubscription).Methods("PATCH")
		s.router.HandleFunc(sub+p, s.handleDeleteSubscription).Methods("DELETE")
		s.router.HandleFunc(sub+"/Actions/EventDestination.SuspendSubscription"+p, s.handleSuspendSubscription).Methods("POST")
		s.router.HandleFunc(sub+"/Actions/EventDestination.ResumeSubscription"+p, s.handleResumeSubscription).Methods("POST")
	}

	// Firmware updates and their tasks
	for _, p := range []string{"", "/"} {
		s.router.HandleFunc(updateServicePath+p, s.handleGetUpdateService).Methods("GET")
		s.router.HandleFunc(firmwareInventoryPath+p, s.handleFirmwareInventoryCollection).Methods("GET")
		s.router.HandleFunc(firmwareInventoryPath+"/{fwid}"+p, s.handleGetFirmwareInventory).Methods("GET")
		s.router.HandleFunc(simpleUpdatePath+p, s.logAction(audit.KindUpdate, s.handleSimpleUpdate)).Methods("POST").Name(actionRoutePrefix + audit.KindUpdate)
		s.router.HandleFunc(multipartPushPath+p, s.logAction(audit.KindUpdate, s.handleMultipartPush)).Methods("POST").Name(actionRoutePrefix + audit.KindUpdate)
		s.router.HandleFunc(taskServicePath+p, s.handleGetTaskService).Methods("GET")
		s.router.HandleFunc(tasksPath+p, s.handleTaskCollection).Methods("GET")
		s.router.HandleFunc(tasksPath+"/{taskid}"+p, s.handleGetTask).Methods("GET")
		s.router.HandleFunc(taskMonitorsPath+"/{taskid}"+p, s.handleTaskMonitor).Methods("GET")
	}

	// Systems
	s.router.HandleFunc("/redfish/v1/Systems", s.handleSystemCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/", s.handleSystemCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}", s.handleGetSystem).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/", s.handleGetSystem).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}", s.logAction(audit.KindBoot, s.handlePatchSystem)).Methods("PATCH").Name(actionRoutePrefix + audit.KindBoot)
	s.router.HandleFunc("/redfish/v1/Systems/{id}/", s.logAction(audit.KindBoot, s.handlePatchSystem)).Methods("PATCH").Name(actionRoutePrefix + audit.KindBoot)

	// Ethernet interfaces
	s.router.HandleFunc("/redfish/v1/Systems/{id}/EthernetInterfaces", s.handleEthernetInterfaceCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/EthernetInterfaces/", s.handleEthernetInterfaceCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/EthernetInterfaces/{nicid}", s.handleGetEthernetInterface).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/EthernetInterfaces/{nicid}/", s.handleGetEthernetInterface).Methods("GET")

	// Processors and storage
	for _, p := range []string{"", "/"} {
		sys := "/redfish/v1/Systems/{id}"
		s.router.HandleFunc(sys+"/Processors"+p, s.handleProcessorCollection).Methods("GET")
		s.router.HandleFunc(sys+"/Processors/{procid}"+p, s.handleGetProcessor).Methods("GET")
		s.router.HandleFunc(sys+"/Storage"+p, s.handleStorageCollection).Methods("GET")
		s.router.HandleFunc(sys+"/Storage/{stid}"+p, s.handleGetStorage).Methods("GET")
		s.router.HandleFunc(sys+"/Storage/{stid}/Drives/{driveid}"+p, s.handleGetDrive).Methods("GET")
		s.router.HandleFunc("/redfish/v1/Chassis/{id}/Drives"+p, s.handleChassisDriveCollection).Methods("GET")
		s.router.HandleFunc("/redfish/v1/Chassis/{id}/Drives/{driveid}"+p, s.handleGetDrive).Methods("GET")
		s.router.HandleFunc(sys+"/Storage/{stid}/Volumes"+p, s.handleVolumeCollection).Methods("GET")
		s.router.HandleFunc(sys+"/Storage/{stid}/Volumes/{volid}"+p, s.handleGetVolume).Methods("GET")
		s.router.HandleFunc(sys+"/SimpleStorage"+p, s.handleSimpleStorageCollection).Methods("GET")
		s.router.HandleFunc(sys+"/SimpleStorage/{stid}"+p, s.handleGetSimpleStorage).Methods("GET")
		s.router.HandleFunc(sys+"/LogServices"+p, s.handleLogServiceCollection).Methods("GET")
		s.router.HandleFunc(sys+"/LogServices/{logid}"+p, s.handleGetSerialLogService).Methods("GET")
		s.router.HandleFunc(sys+"/LogServices/{logid}/Entries"+p, s.handleSerialLogEntryCollection).Methods("GET")
		s.router.HandleFunc(sys+"/LogServices/{logid}/Entries/{entryid}"+p, s.handleGetSerialLogEntry).Methods("GET")
		s.router.HandleFunc(sys+"/LogServices/{logid}/Entries/{entryid}/attachment"+p, s.handleGetSerialLogAttachment).Methods("GET")
		s.router.HandleFunc(sys+"/SecureBoot"+p, s.handleGetSecureBoot).Methods("GET")
		s.router.HandleFunc(sys+"/SecureBoot"+p, s.logAction(audit.KindSecureBoot, s.handlePatchSecureBoot)).Methods("PATCH").Name(actionRoutePrefix + audit.KindSecureBoot)
		s.router.HandleFunc(sys+"/SecureBoot/Actions/SecureBoot.ResetKeys"+p, s.logAction(audit.KindSecureBoot, s.handleResetKeys)).Methods("POST").Name(actionRoutePrefix + audit.KindSecureBoot)
		s.router.HandleFunc(sys+"/Bios"+p, s.handleGetBios).Methods("GET")
		s.router.HandleFunc(sys+"/Bios/Settings"+p, s.handleGetBiosSettings).Methods("GET")
		s.router.HandleFunc(sys+"/Bios/Settings"+p, s.logAction(audit.KindBios, s.handlePatchBiosSettings)).Methods("PATCH").Name(actionRoutePrefix + audit.KindBios)
		s.router.HandleFunc(sys+"/Bios/Actions/Bios.ResetBios"+p, s.logAction(audit.KindBios, s.handleResetBios)).Methods("POST").Name(actionRoutePrefix + audit.KindBios)
		s.router.HandleFunc(registriesPath+p, s.handleRegistryCollection).Methods("GET")
		s.router.HandleFunc(registriesPath+"/{regid}"+p, s.handleGetRegistryFile).Methods("GET")
		s.router.HandleFunc(biosRegistryURI+p, s.handleGetBiosRegistry).Methods("GET")
	}

	// Actions
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Actions/ComputerSystem.Reset", s.logAction(audit.KindPower, s.handleResetAction)).Methods("POST").Name(actionRoutePrefix + audit.KindPower)
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Actions/ComputerSystem.Reset/", s.logAction(audit.KindPower, s.handleResetAction)).Methods("POST").Name(actionRoutePrefix + audit.KindPower)

	// Managers
	s.router.HandleFunc("/redfish/v1/Managers", s.handleManagerCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/", s.handleManagerCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}", s.handleGetManager).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/", s.handleGetManager).Methods("GET")

	// VirtualMedia: the same CD drive under Systems/1 (recommended since
	// Redfish 2021.1) and Managers/1 (deprecated, kept for existing clients)
	for _, owner := range []string{"Systems", "Managers"} {
		vm := "/redfish/v1/" + owner + "/{id}/VirtualMedia"
		for _, p := range []string{"", "/"} {
			s.router.HandleFunc(vm+p, s.handleVirtualMediaCollection).Methods("GET")
			s.router.HandleFunc(vm+"/{vmid}"+p, s.handleGetVirtualMedia).Methods("GET")
			s.router.HandleFunc(vm+"/{vmid}/Actions/VirtualMedia.InsertMedia"+p, s.logAction(audit.KindVMediaInsert, s.handleInsertMedia)).Methods("POST").Name(actionRoutePrefix + audit.KindVMediaInsert)
			s.router.HandleFunc(vm+"/{vmid}/Actions/VirtualMedia.EjectMedia"+p, s.logAction(audit.KindVMediaEject, s.handleEjectMedia)).Methods("POST").Name(actionRoutePrefix + audit.KindVMediaEject)
		}
	}

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

// SetSystemInfo sets the VM hardware configuration reported on Systems/1.
func (s *Server) SetSystemInfo(info SystemInfo) {
	s.systemInfo = info
}

// ServeHTTP implements the http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}
