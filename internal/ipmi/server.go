package ipmi

import (
	"fmt"
	"log"
	"net"
)

// Server is the IPMI UDP server
type Server struct {
	machine    MachineInterface
	sessionMgr *SessionManager
	user       string
	pass       string
	conn       net.PacketConn
}

// NewServer creates a new IPMI server
func NewServer(m MachineInterface, user, pass string) *Server {
	return &Server{
		machine:    m,
		sessionMgr: NewSessionManager(),
		user:       user,
		pass:       pass,
	}
}

// ListenAndServe starts the IPMI UDP server
func (s *Server) ListenAndServe(addr string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	s.conn = conn
	log.Printf("IPMI server listening on %s", addr)
	return s.serve()
}

// Serve starts serving on an existing connection (useful for testing)
func (s *Server) Serve(conn net.PacketConn) error {
	s.conn = conn
	return s.serve()
}

func (s *Server) serve() error {
	buf := make([]byte, 1024)
	for {
		n, addr, err := s.conn.ReadFrom(buf)
		if err != nil {
			return err
		}

		// Make a copy of the data
		data := make([]byte, n)
		copy(data, buf[:n])

		resp, err := s.HandleMessage(data)
		if err != nil {
			log.Printf("IPMI error: %v", err)
			continue
		}

		if resp != nil {
			if _, err := s.conn.WriteTo(resp, addr); err != nil {
				log.Printf("IPMI write error: %v", err)
			}
		}
	}
}

// HandleMessage processes a single IPMI/RMCP message and returns a response
func (s *Server) HandleMessage(data []byte) ([]byte, error) {
	header, payload, err := ParseRMCPMessage(data)
	if err != nil {
		return nil, err
	}

	if header.Class != RMCPClassIPMI {
		return nil, fmt.Errorf("unsupported RMCP class: 0x%02x", header.Class)
	}

	// Check if this is RMCP+ (auth type 0x06 at first byte of payload)
	if len(payload) > 0 && payload[0] == AuthTypeRMCPPlus {
		resp, err := HandleRMCPPlusMessage(payload, s.sessionMgr, s.user, s.pass, s.machine)
		if err != nil {
			return nil, err
		}
		return SerializeRMCPMessage(RMCPClassIPMI, resp), nil
	}

	// IPMI 1.5 message
	session, msg, err := ParseIPMI15Message(payload)
	if err != nil {
		return nil, err
	}

	if msg == nil {
		return nil, fmt.Errorf("no IPMI message parsed")
	}

	code, respData := handleIPMICommand(msg, s.machine)

	respPayload := SerializeIPMIResponse(session, msg.GetNetFn()|0x01, msg.Command, code, respData)
	return SerializeRMCPMessage(RMCPClassIPMI, respPayload), nil
}

// Close stops the server
func (s *Server) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}
