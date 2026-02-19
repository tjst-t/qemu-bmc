package ipmi

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
)

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
	SessionIntegrityKey       []byte // SIK - 20 bytes
	IntegrityKey              []byte // K1 - 20 bytes
	ConfidentialityKey        []byte // K2 - 20 bytes
	Authenticated             bool
	// Negotiated algorithms (set during Open Session exchange)
	AuthAlgorithm            uint8
	IntegrityAlgorithm       uint8
	ConfidentialityAlgorithm uint8
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

	sessionID, err := generateRandomUint32()
	if err != nil {
		return nil, err
	}

	session := &Session{
		RemoteConsoleSessionID: remoteConsoleSessionID,
		ManagedSystemSessionID: sessionID,
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
