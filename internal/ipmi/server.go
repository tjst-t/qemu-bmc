package ipmi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/tjst-t/qemu-bmc/internal/bmc"
	"github.com/tjst-t/qemu-bmc/internal/fault"
	"github.com/tjst-t/qemu-bmc/internal/serial"
)

// errNoResponse is returned for a request that is not answered (fault
// injection).
var errNoResponse = errors.New("no response (injected fault)")

// Server is the IPMI UDP server
type Server struct {
	machine    MachineInterface
	bmcState   *bmc.State
	sessionMgr *SessionManager
	user       string
	pass       string
	conn       net.PacketConn
	faults     *fault.Store
	sol        *solManager
	// idleTimeout closes sessions without a packet (SessionIdleTimeout)
	idleTimeout time.Duration
}

// NewServer creates a new IPMI server
func NewServer(m MachineInterface, state *bmc.State, user, pass string) *Server {
	return &Server{
		machine:     m,
		bmcState:    state,
		sessionMgr:  NewSessionManager(),
		user:        user,
		pass:        pass,
		sol:         newSOLManager(),
		idleTimeout: SessionIdleTimeout,
	}
}

// SetSerialConsole enables SOL (Serial over LAN) with the VM serial
// console. Without it SOL cannot be activated.
func (s *Server) SetSerialConsole(console serial.Console) {
	s.sol.mu.Lock()
	defer s.sol.mu.Unlock()
	s.sol.console = console
}

// SetFaults makes the server apply the ipmi-* and bmc-unavailable faults of
// the store.
func (s *Server) SetFaults(faults *fault.Store) {
	s.faults = faults
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
	s.sol.mu.Lock()
	if a, ok := s.conn.LocalAddr().(*net.UDPAddr); ok {
		s.sol.port = uint16(a.Port)
	}
	// SOL output is sent from the SOL goroutine
	s.sol.send = func(pkt []byte, addr net.Addr) {
		s.write(SerializeRMCPMessage(RMCPClassIPMI, pkt), addr)
	}
	s.sol.mu.Unlock()

	stop := make(chan struct{})
	defer close(stop)
	go s.closeIdleSessions(stop)

	buf := make([]byte, 1024)
	for {
		n, addr, err := s.conn.ReadFrom(buf)
		if err != nil {
			return err
		}

		// Make a copy of the data
		data := make([]byte, n)
		copy(data, buf[:n])

		resp, delay, err := s.handleMessage(data, addr)
		if errors.Is(err, errNoResponse) {
			continue
		}
		if err != nil {
			log.Printf("IPMI error: %v", err)
			continue
		}

		if resp != nil {
			if delay > 0 {
				// Later, without holding back the next requests
				time.AfterFunc(delay, func() { s.write(resp, addr) })
			} else {
				s.write(resp, addr)
			}
		}
	}
}

// closeIdleSessions closes the sessions idle for longer than idleTimeout
// until stop is closed.
func (s *Server) closeIdleSessions(stop <-chan struct{}) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			for _, session := range s.sessionMgr.RemoveIdle(s.idleTimeout) {
				s.sol.sessionClosed(session, "session timed out")
			}
		}
	}
}

func (s *Server) write(resp []byte, addr net.Addr) {
	if _, err := s.conn.WriteTo(resp, addr); err != nil {
		log.Printf("IPMI write error: %v", err)
	}
}

// HandleMessage processes a single IPMI/RMCP message and returns a response
func (s *Server) HandleMessage(data []byte) ([]byte, error) {
	resp, delay, err := s.handleMessage(data, nil)
	if delay > 0 {
		time.Sleep(delay)
	}
	return resp, err
}

// sessionCommands are the commands opening or closing a session, affected
// by the IPMI faults only with includeSession (NetFn App)
var sessionCommands = map[uint8]bool{
	CmdGetChannelAuthCapabilities: true,
	CmdGetSessionChallenge:        true,
	CmdActivateSession:            true,
	CmdSetSessionPrivilege:        true,
	CmdCloseSession:               true,
	CmdGetChannelCipherSuites:     true,
}

// handleMessage processes a single IPMI/RMCP message from addr and returns
// the response and how long to wait before sending it, or errNoResponse.
func (s *Server) handleMessage(data []byte, addr net.Addr) ([]byte, time.Duration, error) {
	var delay time.Duration
	// apply applies the faults; false if the request is not answered
	apply := func(r fault.IPMIRequest) (fault.IPMIAction, bool) {
		a := s.faults.IPMI(r)
		delay = a.Delay
		return a, !a.Drop
	}
	handle := func(msg *IPMIMessage, session *Session) (CompletionCode, []byte, error) {
		netFn := msg.GetNetFn()
		a, ok := apply(fault.IPMIRequest{
			HasCommand: true,
			NetFn:      netFn,
			Cmd:        msg.Command,
			Session:    netFn == NetFnApp && sessionCommands[msg.Command],
		})
		if !ok {
			return 0, nil, errNoResponse
		}
		if a.Error {
			return CompletionCode(a.CompletionCode), nil, nil
		}
		if session != nil {
			session.setAddr(addr)
		}
		if code, resp, ok := s.handleServerCommand(msg, session); ok {
			return code, resp, nil
		}
		code, resp := handleIPMICommand(msg, s.machine, s.bmcState)
		return code, resp, nil
	}
	handlers := sessionHandlers{
		command: handle,
		sol: func(session *Session, packet []byte) []byte {
			session.setAddr(addr)
			return s.sol.receive(session, packet)
		},
	}

	resp, err := s.dispatch(data, handlers, apply)
	return resp, delay, err
}

func (s *Server) dispatch(data []byte, handlers sessionHandlers, apply func(fault.IPMIRequest) (fault.IPMIAction, bool)) ([]byte, error) {
	header, payload, err := ParseRMCPMessage(data)
	if err != nil {
		return nil, err
	}

	if header.Class == RMCPClassASF {
		if _, ok := apply(fault.IPMIRequest{Session: true}); !ok {
			return nil, errNoResponse
		}
		return handleASFPing(payload)
	}

	if header.Class != RMCPClassIPMI {
		return nil, fmt.Errorf("unsupported RMCP class: 0x%02x", header.Class)
	}

	// Check if this is RMCP+ (auth type 0x06 at first byte of payload)
	if len(payload) > 0 && payload[0] == AuthTypeRMCPPlus {
		if len(payload) > 1 {
			var r *fault.IPMIRequest
			switch payload[1] & 0x3F {
			case PayloadTypeIPMI:
			case PayloadTypeSOL:
				r = &fault.IPMIRequest{SOL: true}
			default: // Open Session / RAKP
				r = &fault.IPMIRequest{Session: true}
			}
			if r != nil {
				if _, ok := apply(*r); !ok {
					return nil, errNoResponse
				}
			}
		}
		resp, err := handleRMCPPlusMessage(payload, s.sessionMgr, s.user, s.pass, s.bmcState, handlers)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, nil
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

	code, respData, err := handlers.command(msg, nil)
	if err != nil {
		return nil, err
	}

	respPayload := SerializeIPMIResponse(session, msg.GetNetFn()|0x01, msg.Command, code, respData, msg.SourceLun, s.pass)
	return SerializeRMCPMessage(RMCPClassIPMI, respPayload), nil
}

// handleASFPing responds to ASF Presence Ping with a Pong
func handleASFPing(payload []byte) ([]byte, error) {
	// ASF message header: 4-byte IANA + 1-byte type + 1-byte tag + 1-byte reserved + 1-byte length
	if len(payload) < 8 {
		return nil, fmt.Errorf("ASF message too short")
	}

	msgType := payload[4]
	msgTag := payload[5]

	if msgType != 0x80 { // Only handle Presence Ping
		return nil, fmt.Errorf("unsupported ASF message type: 0x%02x", msgType)
	}

	// Build ASF Presence Pong
	resp := make([]byte, 28) // 4 RMCP + 8 ASF header + 16 pong data

	// RMCP header
	resp[0] = RMCPVersion1
	resp[1] = 0x00
	resp[2] = 0xFF
	resp[3] = RMCPClassASF

	// ASF header
	binary.BigEndian.PutUint32(resp[4:8], 0x000011BE) // IANA Enterprise Number for ASF
	resp[8] = 0x40                                    // Message Type: Presence Pong
	resp[9] = msgTag                                  // Echo back message tag
	resp[10] = 0x00                                   // Reserved
	resp[11] = 0x10                                   // Data Length: 16 bytes

	// Pong data
	binary.BigEndian.PutUint32(resp[12:16], 0x000011BE) // IANA Enterprise Number
	// OEM-defined: 4 bytes zeros (resp[16:20] already zero)
	resp[20] = 0x81 // Supported Entities: IPMI supported (bit 7) + ASF 1.0 (bit 0)
	resp[21] = 0x00 // Supported Interactions
	// Reserved: 6 bytes zeros (resp[22:28] already zero)

	return resp, nil
}

// Close stops the server
func (s *Server) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}
