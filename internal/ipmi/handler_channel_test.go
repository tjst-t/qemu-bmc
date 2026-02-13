package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleGetChannelAccess(t *testing.T) {
	state := newTestBMCState()
	// Channel 1, non-volatile access type (0x40 in bits 7:6 of byte 1)
	reqData := []byte{0x01, 0x40}
	code, data := handleGetChannelAccess(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 2)

	// Channel 1 defaults: AccessMode=2, UserLevelAuth=true, PerMsgAuth=true, AlertingEnabled=false
	// Byte 0: [alerting_disabled(7)][per_msg_auth(6)][user_level_auth(5)][reserved(4:3)][access_mode(2:0)]
	accessMode := data[0] & 0x07
	assert.Equal(t, byte(2), accessMode, "AccessMode should be 2 (AlwaysAvailable)")

	// Byte 1: [reserved(4)][privilege_limit(4)]
	privLimit := data[1] & 0x0F
	assert.Equal(t, byte(4), privLimit, "PrivilegeLimit should be 4 (Admin)")
}

func TestHandleGetChannelAccess_InvalidData(t *testing.T) {
	state := newTestBMCState()
	code, _ := handleGetChannelAccess([]byte{}, state)
	assert.Equal(t, CompletionCodeInvalidField, code)
}

func TestHandleSetChannelAccess(t *testing.T) {
	state := newTestBMCState()
	// Request (3 bytes):
	// Byte 0: [access_set_mode(2)][reserved(2)][channel(4)] = 0x01 (channel 1)
	// Byte 1: [alerting_disabled(1)][per_msg_auth(1)][user_level_auth(1)][reserved(2)][access_mode(3)]
	//   alerting_disabled=0 (alerting enabled), per_msg_auth=1, user_level_auth=1, access_mode=3
	// Byte 2: [priv_set_mode(2)][reserved(2)][privilege_limit(4)] = 0x03
	reqData := []byte{
		0x01, // channel 1
		0x63, // alerting_disabled=0, per_msg_auth=1, user_level_auth=1, access_mode=3
		0x03, // privilege_limit=3
	}
	code, data := handleSetChannelAccess(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Nil(t, data)

	// Verify the state was updated
	access := state.GetChannelAccess(1)
	assert.Equal(t, uint8(3), access.AccessMode)
	assert.True(t, access.UserLevelAuth)
	assert.True(t, access.PerMsgAuth)
	assert.True(t, access.AlertingEnabled) // alerting_disabled=0 means alerting IS enabled
	assert.Equal(t, uint8(3), access.PrivilegeLimit)
}

func TestHandleSetChannelAccess_InvalidData(t *testing.T) {
	state := newTestBMCState()
	// Only 2 bytes, need at least 3
	code, _ := handleSetChannelAccess([]byte{0x01, 0x02}, state)
	assert.Equal(t, CompletionCodeInvalidField, code)
}

func TestHandleGetChannelInfo(t *testing.T) {
	state := newTestBMCState()
	reqData := []byte{0x01} // channel 1
	code, data := handleGetChannelInfo(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 9)

	assert.Equal(t, byte(0x01), data[0], "channel number")
	assert.Equal(t, byte(0x04), data[1], "channel medium = 802.3 LAN")
	assert.Equal(t, byte(0x01), data[2], "channel protocol = IPMB-1.0")
	// Byte 3: session support (bits 7:6) | active sessions (bits 5:0)
	// SessionSupport=0x02 → bits 7:6 = 0x02 → shifted = 0x80
	sessionSupport := (data[3] >> 6) & 0x03
	assert.Equal(t, byte(0x02), sessionSupport, "session support = multi-session")
	activeSessions := data[3] & 0x3F
	assert.Equal(t, byte(0), activeSessions, "active sessions = 0")
	// Bytes 4-6: vendor ID (zeros)
	assert.Equal(t, byte(0), data[4])
	assert.Equal(t, byte(0), data[5])
	assert.Equal(t, byte(0), data[6])
	// Bytes 7-8: aux channel info (zeros)
	assert.Equal(t, byte(0), data[7])
	assert.Equal(t, byte(0), data[8])
}

func TestHandleGetChannelInfo_CurrentChannel(t *testing.T) {
	state := newTestBMCState()
	reqData := []byte{0x0E} // 0x0E = current channel, resolves to 1
	code, data := handleGetChannelInfo(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 9)

	// Should resolve to channel 1
	assert.Equal(t, byte(0x01), data[0], "current channel should resolve to 1")
	assert.Equal(t, byte(0x04), data[1], "channel medium = 802.3 LAN")
	assert.Equal(t, byte(0x01), data[2], "channel protocol = IPMB-1.0")
}

func TestHandleGetChannelInfo_InvalidData(t *testing.T) {
	state := newTestBMCState()
	code, _ := handleGetChannelInfo([]byte{}, state)
	assert.Equal(t, CompletionCodeInvalidField, code)
}
