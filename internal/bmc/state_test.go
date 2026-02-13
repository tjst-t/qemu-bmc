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
