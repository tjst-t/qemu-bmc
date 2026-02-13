package bmc

import (
	"fmt"
	"sync"
)

const maxUsers = 15

// UserAccess holds access settings for a single user slot.
type UserAccess struct {
	PrivilegeLimit uint8
	Enabled        bool
	IPMIMessaging  bool
	LinkAuth       bool
	CallinCallback bool
}

// userSlot holds the credentials and access for a single user slot.
type userSlot struct {
	name     string
	password string
	access   UserAccess
}

// State manages BMC configuration state, starting with user accounts.
// All methods are safe for concurrent use.
type State struct {
	mu    sync.RWMutex
	users [maxUsers + 1]userSlot // index 0 unused, 1-15 valid
}

// NewState creates a new State with a default admin user in slot 2.
// Slot 1 is reserved as the null user (empty).
func NewState(defaultUser, defaultPass string) *State {
	s := &State{}
	s.users[2] = userSlot{
		name:     defaultUser,
		password: defaultPass,
		access: UserAccess{
			PrivilegeLimit: 4,
			Enabled:        true,
			IPMIMessaging:  true,
			LinkAuth:       true,
		},
	}
	return s
}

func validateUserID(userID uint8) error {
	if userID < 1 || userID > maxUsers {
		return fmt.Errorf("user ID %d out of range (1-%d)", userID, maxUsers)
	}
	return nil
}

// MaxUsers returns the maximum number of user slots (15).
func (s *State) MaxUsers() uint8 {
	return maxUsers
}

// GetUserName returns the name for the given user slot.
func (s *State) GetUserName(userID uint8) (string, error) {
	if err := validateUserID(userID); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.users[userID].name, nil
}

// SetUserName sets the name for the given user slot.
func (s *State) SetUserName(userID uint8, name string) error {
	if err := validateUserID(userID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[userID].name = name
	return nil
}

// SetUserPassword sets the password for the given user slot.
func (s *State) SetUserPassword(userID uint8, password string) error {
	if err := validateUserID(userID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[userID].password = password
	return nil
}

// CheckPassword verifies the password for the given user slot.
// Returns false for invalid user IDs or if no password has been set.
func (s *State) CheckPassword(userID uint8, password string) bool {
	if validateUserID(userID) != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored := s.users[userID].password
	if stored == "" {
		return false
	}
	return stored == password
}

// GetUserAccess returns the access settings for the given user slot.
// The channel parameter is accepted for API compatibility but currently unused.
func (s *State) GetUserAccess(channel, userID uint8) (UserAccess, error) {
	if err := validateUserID(userID); err != nil {
		return UserAccess{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.users[userID].access, nil
}

// SetUserAccess sets the access settings for the given user slot.
// The channel parameter is accepted for API compatibility but currently unused.
func (s *State) SetUserAccess(channel, userID uint8, access UserAccess) error {
	if err := validateUserID(userID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[userID].access = access
	return nil
}

// EnabledUserCount returns the number of users with Enabled=true.
func (s *State) EnabledUserCount() uint8 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count uint8
	for i := 1; i <= maxUsers; i++ {
		if s.users[i].access.Enabled {
			count++
		}
	}
	return count
}

// LookupUserByName finds a user by name and returns the user ID.
// Returns (0, false) if the name is empty or not found.
func (s *State) LookupUserByName(name string) (uint8, bool) {
	if name == "" {
		return 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := 1; i <= maxUsers; i++ {
		if s.users[i].name == name {
			return uint8(i), true
		}
	}
	return 0, false
}
