package bmc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewState_InitializesDefaultUser(t *testing.T) {
	s := NewState("admin", "password")

	// User1 is reserved (null user), should be empty
	name1, err := s.GetUserName(1)
	require.NoError(t, err)
	assert.Equal(t, "", name1)
	assert.False(t, s.CheckPassword(1, ""))

	// User2 has admin credentials
	name2, err := s.GetUserName(2)
	require.NoError(t, err)
	assert.Equal(t, "admin", name2)
	assert.True(t, s.CheckPassword(2, "password"))
	assert.False(t, s.CheckPassword(2, "wrong"))

	// User2 access: Enabled, IPMIMessaging, LinkAuth, PrivilegeLimit=4
	access, err := s.GetUserAccess(1, 2)
	require.NoError(t, err)
	assert.True(t, access.Enabled)
	assert.True(t, access.IPMIMessaging)
	assert.True(t, access.LinkAuth)
	assert.False(t, access.CallinCallback)
	assert.Equal(t, uint8(4), access.PrivilegeLimit)

	// User1 access should be default (all false/zero)
	access1, err := s.GetUserAccess(1, 1)
	require.NoError(t, err)
	assert.False(t, access1.Enabled)
	assert.Equal(t, uint8(0), access1.PrivilegeLimit)
}

func TestSetGetUserName(t *testing.T) {
	s := NewState("admin", "password")

	err := s.SetUserName(3, "operator")
	require.NoError(t, err)

	name, err := s.GetUserName(3)
	require.NoError(t, err)
	assert.Equal(t, "operator", name)
}

func TestSetUserName_InvalidSlot(t *testing.T) {
	s := NewState("admin", "password")

	// Slot 0 is invalid
	err := s.SetUserName(0, "test")
	assert.Error(t, err)

	_, err = s.GetUserName(0)
	assert.Error(t, err)

	// Slot 16 is out of range
	err = s.SetUserName(16, "test")
	assert.Error(t, err)

	_, err = s.GetUserName(16)
	assert.Error(t, err)
}

func TestSetUserPassword_and_CheckPassword(t *testing.T) {
	s := NewState("admin", "password")

	// Set password for user 3
	err := s.SetUserPassword(3, "secret123")
	require.NoError(t, err)

	assert.True(t, s.CheckPassword(3, "secret123"))
	assert.False(t, s.CheckPassword(3, "wrongpass"))

	// Invalid slot
	err = s.SetUserPassword(0, "test")
	assert.Error(t, err)

	err = s.SetUserPassword(16, "test")
	assert.Error(t, err)

	// CheckPassword on invalid slot returns false
	assert.False(t, s.CheckPassword(0, ""))
	assert.False(t, s.CheckPassword(16, ""))
}

func TestSetGetUserAccess(t *testing.T) {
	s := NewState("admin", "password")

	access := UserAccess{
		PrivilegeLimit: 3,
		Enabled:        true,
		IPMIMessaging:  true,
		LinkAuth:       false,
		CallinCallback: true,
	}

	err := s.SetUserAccess(1, 5, access)
	require.NoError(t, err)

	got, err := s.GetUserAccess(1, 5)
	require.NoError(t, err)
	assert.Equal(t, access, got)

	// Invalid slot
	err = s.SetUserAccess(1, 0, access)
	assert.Error(t, err)

	_, err = s.GetUserAccess(1, 0)
	assert.Error(t, err)

	err = s.SetUserAccess(1, 16, access)
	assert.Error(t, err)

	_, err = s.GetUserAccess(1, 16)
	assert.Error(t, err)
}

func TestGetMaxUsers(t *testing.T) {
	s := NewState("admin", "password")
	assert.Equal(t, uint8(15), s.MaxUsers())
}

func TestGetEnabledUserCount(t *testing.T) {
	s := NewState("admin", "password")

	// Starts at 1 (User2 is enabled by default)
	assert.Equal(t, uint8(1), s.EnabledUserCount())

	// Enable user 3
	err := s.SetUserAccess(1, 3, UserAccess{Enabled: true})
	require.NoError(t, err)
	assert.Equal(t, uint8(2), s.EnabledUserCount())

	// Enable user 4
	err = s.SetUserAccess(1, 4, UserAccess{Enabled: true})
	require.NoError(t, err)
	assert.Equal(t, uint8(3), s.EnabledUserCount())

	// Disable user 3
	err = s.SetUserAccess(1, 3, UserAccess{Enabled: false})
	require.NoError(t, err)
	assert.Equal(t, uint8(2), s.EnabledUserCount())
}

func TestLookupUserByName(t *testing.T) {
	s := NewState("admin", "password")

	// Find existing user
	id, found := s.LookupUserByName("admin")
	assert.True(t, found)
	assert.Equal(t, uint8(2), id)

	// Non-existent user
	_, found = s.LookupUserByName("nobody")
	assert.False(t, found)

	// Add a user and find it
	err := s.SetUserName(5, "operator")
	require.NoError(t, err)

	id, found = s.LookupUserByName("operator")
	assert.True(t, found)
	assert.Equal(t, uint8(5), id)

	// Empty name should not match (User1 has empty name, but we should not match empty lookups)
	_, found = s.LookupUserByName("")
	assert.False(t, found)
}

// --- LAN Configuration Tests ---

func TestLANConfig_Defaults(t *testing.T) {
	s := NewState("admin", "password")

	// Param 1: Auth Type Support (read-only)
	assert.Equal(t, []byte{0x97}, s.GetLANConfig(1))

	// Param 3: IP Address (default zeros)
	assert.Equal(t, []byte{0, 0, 0, 0}, s.GetLANConfig(3))

	// Param 4: IP Source (default Static = 0x01)
	assert.Equal(t, []byte{0x01}, s.GetLANConfig(4))

	// Unknown param returns nil
	assert.Nil(t, s.GetLANConfig(200))
}

func TestLANConfig_SetGet(t *testing.T) {
	s := NewState("admin", "password")

	// Set IP address (param 3)
	ip := []byte{10, 0, 0, 1}
	s.SetLANConfig(3, ip)
	assert.Equal(t, []byte{10, 0, 0, 1}, s.GetLANConfig(3))

	// Set subnet mask (param 6)
	subnet := []byte{255, 255, 255, 0}
	s.SetLANConfig(6, subnet)
	assert.Equal(t, []byte{255, 255, 255, 0}, s.GetLANConfig(6))

	// Set default gateway (param 12)
	gw := []byte{10, 0, 0, 254}
	s.SetLANConfig(12, gw)
	assert.Equal(t, []byte{10, 0, 0, 254}, s.GetLANConfig(12))

	// Verify no aliasing: modifying original slice doesn't affect stored value
	ip[0] = 192
	assert.Equal(t, []byte{10, 0, 0, 1}, s.GetLANConfig(3))

	// Verify no aliasing: modifying returned slice doesn't affect stored value
	got := s.GetLANConfig(3)
	got[0] = 172
	assert.Equal(t, []byte{10, 0, 0, 1}, s.GetLANConfig(3))
}

func TestLANConfig_IPSource(t *testing.T) {
	s := NewState("admin", "password")

	// Default is Static (0x01)
	assert.Equal(t, []byte{0x01}, s.GetLANConfig(4))

	// Set to DHCP (0x02)
	s.SetLANConfig(4, []byte{0x02})
	assert.Equal(t, []byte{0x02}, s.GetLANConfig(4))
}

func TestLANConfig_MACAddress(t *testing.T) {
	s := NewState("admin", "password")

	// Default is all zeros (6 bytes)
	assert.Equal(t, []byte{0, 0, 0, 0, 0, 0}, s.GetLANConfig(5))

	// Set MAC address
	mac := []byte{0x52, 0x54, 0x00, 0xAB, 0xCD, 0xEF}
	s.SetLANConfig(5, mac)
	assert.Equal(t, []byte{0x52, 0x54, 0x00, 0xAB, 0xCD, 0xEF}, s.GetLANConfig(5))
}

func TestLANConfig_AuthTypeEnables(t *testing.T) {
	s := NewState("admin", "password")

	// Default param 2: 5 bytes
	assert.Equal(t, []byte{0x14, 0x14, 0x14, 0x14, 0x00}, s.GetLANConfig(2))

	// Set new auth type enables
	newAuth := []byte{0x15, 0x15, 0x15, 0x15, 0x01}
	s.SetLANConfig(2, newAuth)
	assert.Equal(t, []byte{0x15, 0x15, 0x15, 0x15, 0x01}, s.GetLANConfig(2))
}

// --- Channel Access Tests ---

func TestChannelAccess_Defaults(t *testing.T) {
	s := NewState("admin", "password")

	access := s.GetChannelAccess(1)
	assert.Equal(t, uint8(2), access.AccessMode, "channel 1 should default to AlwaysAvailable")
	assert.True(t, access.UserLevelAuth, "channel 1 should have UserLevelAuth enabled")
	assert.True(t, access.PerMsgAuth, "channel 1 should have PerMsgAuth enabled")
	assert.False(t, access.AlertingEnabled, "channel 1 alerting should be disabled")
	assert.Equal(t, uint8(4), access.PrivilegeLimit, "channel 1 privilege limit should be Admin")

	// Channel 0 should be zero-value
	access0 := s.GetChannelAccess(0)
	assert.Equal(t, uint8(0), access0.AccessMode)
	assert.Equal(t, uint8(0), access0.PrivilegeLimit)
}

func TestChannelAccess_SetGet(t *testing.T) {
	s := NewState("admin", "password")

	access := ChannelAccess{
		AccessMode:      3,
		UserLevelAuth:   false,
		PerMsgAuth:      true,
		AlertingEnabled: true,
		PrivilegeLimit:  3,
	}

	s.SetChannelAccess(2, access)
	got := s.GetChannelAccess(2)
	assert.Equal(t, access, got)

	// Out-of-range get returns zero-value
	outOfRange := s.GetChannelAccess(16)
	assert.Equal(t, ChannelAccess{}, outOfRange)

	// Out-of-range set is silently ignored
	s.SetChannelAccess(16, access) // should not panic
	s.SetChannelAccess(255, access) // should not panic
}

func TestGetChannelInfo(t *testing.T) {
	s := NewState("admin", "password")

	info := s.GetChannelInfo(1)
	assert.Equal(t, uint8(1), info.ChannelNumber)
	assert.Equal(t, uint8(0x04), info.ChannelMedium, "should be 802.3 LAN")
	assert.Equal(t, uint8(0x01), info.ChannelProtocol, "should be IPMB-1.0")
	assert.Equal(t, uint8(0x02), info.SessionSupport, "should be multi-session")
	assert.Equal(t, uint8(0), info.ActiveSessions)

	// Different channel number
	info5 := s.GetChannelInfo(5)
	assert.Equal(t, uint8(5), info5.ChannelNumber)
	assert.Equal(t, uint8(0x04), info5.ChannelMedium)
}
