package ipmi

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"sync"
	"time"
)

// SessionIdleTimeout is how long a session may go without a packet before
// it is closed (the usual BMC session inactivity timeout). ipmitool's SOL
// keepalive comes every 15 s.
const SessionIdleTimeout = 60 * time.Second

// Session represents an RMCP+ session
type Session struct {
	RemoteConsoleSessionID    uint32
	ManagedSystemSessionID    uint32
	RemoteConsoleRandomNumber [16]byte
	ManagedSystemRandomNumber [16]byte
	RequestedPrivilegeLevel   uint8
	ManagedSystemGUID         [16]byte
	UserName                  []byte
	UserNameLength            uint8
	SessionIntegrityKey       []byte // SIK - 20 bytes (SHA1) or 32 bytes (SHA256)
	IntegrityKey              []byte // K1 - 20 bytes (SHA1) or 32 bytes (SHA256)
	ConfidentialityKey        []byte // K2 - 20 bytes (SHA1) or 32 bytes (SHA256)
	Authenticated             bool
	// Negotiated algorithms (set during Open Session exchange)
	AuthAlgorithm            uint8
	IntegrityAlgorithm       uint8
	ConfidentialityAlgorithm uint8
	// OutboundSequenceNumber is the BMC's sequence number for authenticated responses.
	// Incremented with each encrypted/authenticated response (IPMI 2.0 spec §13.29).
	OutboundSequenceNumber uint32

	mu           sync.Mutex
	addr         net.Addr  // where the console sends from
	lastActivity time.Time // last packet
}

// touch records activity on the session.
func (s *Session) touch() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
}

// setAddr records the address the console sends from.
func (s *Session) setAddr(addr net.Addr) {
	if addr == nil {
		return
	}
	s.mu.Lock()
	s.addr = addr
	s.mu.Unlock()
}

// Addr returns the address the console last sent from (nil if unknown).
func (s *Session) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// SessionManager manages RMCP+ sessions
type SessionManager struct {
	sessions map[uint32]*Session
	mu       sync.RWMutex
}

// NewSessionManager creates a new session manager
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[uint32]*Session),
	}
}

// CreateSession creates a new session with a random managed system session ID
func (sm *SessionManager) CreateSession(remoteConsoleSessionID uint32) (*Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var sessionID uint32
	// 0 means "no session"; an ID in use would replace that session
	for sessionID == 0 || sm.sessions[sessionID] != nil {
		var err error
		if sessionID, err = generateRandomUint32(); err != nil {
			return nil, err
		}
	}

	session := &Session{
		RemoteConsoleSessionID: remoteConsoleSessionID,
		ManagedSystemSessionID: sessionID,
		lastActivity:           time.Now(),
	}

	// Generate managed system random number
	if _, err := rand.Read(session.ManagedSystemRandomNumber[:]); err != nil {
		return nil, err
	}

	// Generate managed system GUID
	if _, err := rand.Read(session.ManagedSystemGUID[:]); err != nil {
		return nil, err
	}

	sm.sessions[sessionID] = session
	return session, nil
}

// GetSession retrieves a session by managed system session ID
func (sm *SessionManager) GetSession(sessionID uint32) (*Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	session, ok := sm.sessions[sessionID]
	return session, ok
}

// RemoveSession removes a session
func (sm *SessionManager) RemoveSession(sessionID uint32) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, sessionID)
}

// RemoveIdle removes and returns the sessions without a packet for longer
// than maxIdle.
func (sm *SessionManager) RemoveIdle(maxIdle time.Duration) []*Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	var removed []*Session
	for id, s := range sm.sessions {
		s.mu.Lock()
		idle := time.Since(s.lastActivity) > maxIdle
		s.mu.Unlock()
		if idle {
			delete(sm.sessions, id)
			removed = append(removed, s)
		}
	}
	return removed
}

func generateRandomUint32() (uint32, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func generateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}
