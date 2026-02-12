package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionManager_CreateAndGet(t *testing.T) {
	sm := NewSessionManager()

	session, err := sm.CreateSession(0x12345678)
	require.NoError(t, err)
	assert.Equal(t, uint32(0x12345678), session.RemoteConsoleSessionID)
	assert.NotZero(t, session.ManagedSystemSessionID)

	got, ok := sm.GetSession(session.ManagedSystemSessionID)
	assert.True(t, ok)
	assert.Equal(t, session, got)
}

func TestSessionManager_Remove(t *testing.T) {
	sm := NewSessionManager()

	session, err := sm.CreateSession(0x12345678)
	require.NoError(t, err)

	sm.RemoveSession(session.ManagedSystemSessionID)

	_, ok := sm.GetSession(session.ManagedSystemSessionID)
	assert.False(t, ok)
}

func TestSessionManager_GetNonExistent(t *testing.T) {
	sm := NewSessionManager()
	_, ok := sm.GetSession(0x99999999)
	assert.False(t, ok)
}
