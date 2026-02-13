package bmc

import (
	"crypto/subtle"
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

// ChannelAccess represents channel access settings.
type ChannelAccess struct {
	AccessMode      uint8 // 0=Disabled, 1=PreBoot, 2=AlwaysAvailable, 3=Shared
	UserLevelAuth   bool
	PerMsgAuth      bool
	AlertingEnabled bool
	PrivilegeLimit  uint8 // Max privilege for channel
}

// ChannelInfo holds static channel information.
type ChannelInfo struct {
	ChannelNumber   uint8
	ChannelMedium   uint8 // 0x04 = 802.3 LAN
	ChannelProtocol uint8 // 0x01 = IPMB-1.0
	SessionSupport  uint8 // 0x02 = multi-session
	ActiveSessions  uint8
}

// State manages BMC configuration state, starting with user accounts.
// All methods are safe for concurrent use.
type State struct {
	mu            sync.RWMutex
	users         [maxUsers + 1]userSlot // index 0 unused, 1-15 valid
	lanConfig     map[uint8][]byte       // parameter number → value
	channelAccess [16]ChannelAccess      // indexed by channel (0-15)
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

	// Initialize LAN configuration defaults
	s.lanConfig = map[uint8][]byte{
		1:  {0x97},                         // Auth Type Support (read-only)
		2:  {0x14, 0x14, 0x14, 0x14, 0x00}, // Auth Type Enables
		3:  {0, 0, 0, 0},                   // IP Address
		4:  {0x01},                          // IP Source: Static
		5:  {0, 0, 0, 0, 0, 0},             // MAC Address
		6:  {0, 0, 0, 0},                   // Subnet Mask
		12: {0, 0, 0, 0},                   // Default Gateway
	}

	// Initialize channel 1 defaults
	s.channelAccess[1] = ChannelAccess{
		AccessMode:     2, // AlwaysAvailable
		UserLevelAuth:  true,
		PerMsgAuth:     true,
		PrivilegeLimit: 4, // Admin
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

// GetUserPassword returns the password for the given user slot.
func (s *State) GetUserPassword(userID uint8) (string, error) {
	if err := validateUserID(userID); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.users[userID].password, nil
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
	return subtle.ConstantTimeCompare([]byte(stored), []byte(password)) == 1
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

// GetLANConfig returns a copy of the LAN configuration parameter value.
// Returns nil if the parameter is not found.
func (s *State) GetLANConfig(param uint8) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.lanConfig[param]
	if !ok {
		return nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out
}

// SetLANConfig stores a copy of the data for the given LAN configuration parameter.
func (s *State) SetLANConfig(param uint8, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := make([]byte, len(data))
	copy(stored, data)
	s.lanConfig[param] = stored
}

// GetChannelAccess returns the access settings for the given channel (0-15).
// Returns a zero-value ChannelAccess for out-of-range channels.
func (s *State) GetChannelAccess(channel uint8) ChannelAccess {
	if channel > 15 {
		return ChannelAccess{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channelAccess[channel]
}

// SetChannelAccess sets the access settings for the given channel (0-15).
// Silently ignores out-of-range channels.
func (s *State) SetChannelAccess(channel uint8, access ChannelAccess) {
	if channel > 15 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channelAccess[channel] = access
}

// GetChannelInfo returns static channel information for the given channel.
// All channels report as 802.3 LAN with IPMB-1.0 protocol and multi-session support.
func (s *State) GetChannelInfo(channel uint8) ChannelInfo {
	return ChannelInfo{
		ChannelNumber:   channel,
		ChannelMedium:   0x04, // 802.3 LAN
		ChannelProtocol: 0x01, // IPMB-1.0
		SessionSupport:  0x02, // multi-session
		ActiveSessions:  0,
	}
}
