package redfish

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/tjst-t/qemu-bmc/internal/machine"
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

// Server is the Redfish HTTP server
type Server struct {
	router  *mux.Router
	machine MachineInterface
	user    string
	pass    string
}

// NewServer creates a new Redfish server
func NewServer(m MachineInterface, user, pass string) *Server {
	s := &Server{
		router:  mux.NewRouter(),
		machine: m,
		user:    user,
		pass:    pass,
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
}

// ServeHTTP implements the http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}
