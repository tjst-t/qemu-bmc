package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleGetLANConfigParams_SetInProgress(t *testing.T) {
	state := newTestBMCState()
	// Request: [channel=1] [param=0 (Set In Progress)] [set_selector=0] [block_selector=0]
	reqData := []byte{0x01, 0x00, 0x00, 0x00}
	code, data := handleGetLANConfigParams(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 2)
	assert.Equal(t, byte(0x11), data[0], "revision should be 1.1")
	assert.Equal(t, byte(0x00), data[1], "Set In Progress should be 0x00 (set complete)")
}

func TestHandleGetLANConfigParams_AuthTypeSupport(t *testing.T) {
	state := newTestBMCState()
	// Request: [channel=1] [param=1 (Auth Type Support)] [set_selector=0] [block_selector=0]
	reqData := []byte{0x01, 0x01, 0x00, 0x00}
	code, data := handleGetLANConfigParams(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 2)
	assert.Equal(t, byte(0x11), data[0], "revision should be 1.1")
	assert.Equal(t, byte(0x97), data[1], "Auth Type Support should be 0x97")
}

func TestHandleGetLANConfigParams_IPAddress(t *testing.T) {
	state := newTestBMCState()
	// Set an IP address first
	state.SetLANConfig(3, []byte{192, 168, 1, 100})

	// Request: [channel=1] [param=3 (IP Address)] [set_selector=0] [block_selector=0]
	reqData := []byte{0x01, 0x03, 0x00, 0x00}
	code, data := handleGetLANConfigParams(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 5) // 1 byte revision + 4 bytes IP
	assert.Equal(t, byte(0x11), data[0], "revision should be 1.1")
	assert.Equal(t, byte(192), data[1])
	assert.Equal(t, byte(168), data[2])
	assert.Equal(t, byte(1), data[3])
	assert.Equal(t, byte(100), data[4])
}

func TestHandleGetLANConfigParams_SubnetMask(t *testing.T) {
	state := newTestBMCState()
	// Set a subnet mask first
	state.SetLANConfig(6, []byte{255, 255, 255, 0})

	// Request: [channel=1] [param=6 (Subnet Mask)] [set_selector=0] [block_selector=0]
	reqData := []byte{0x01, 0x06, 0x00, 0x00}
	code, data := handleGetLANConfigParams(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 5) // 1 byte revision + 4 bytes mask
	assert.Equal(t, byte(0x11), data[0], "revision should be 1.1")
	assert.Equal(t, byte(255), data[1])
	assert.Equal(t, byte(255), data[2])
	assert.Equal(t, byte(255), data[3])
	assert.Equal(t, byte(0), data[4])
}

func TestHandleGetLANConfigParams_UnknownParam(t *testing.T) {
	state := newTestBMCState()
	// Request: [channel=1] [param=0xFE (unknown)] [set_selector=0] [block_selector=0]
	reqData := []byte{0x01, 0xFE, 0x00, 0x00}
	code, _ := handleGetLANConfigParams(reqData, state)
	assert.Equal(t, CompletionCodeParameterOutOfRange, code)
}

func TestHandleGetLANConfigParams_InvalidData(t *testing.T) {
	state := newTestBMCState()
	// Too short - less than 4 bytes
	code, _ := handleGetLANConfigParams([]byte{0x01}, state)
	assert.Equal(t, CompletionCodeInvalidField, code)

	code, _ = handleGetLANConfigParams([]byte{}, state)
	assert.Equal(t, CompletionCodeInvalidField, code)
}

func TestHandleSetLANConfigParams_IPAddress(t *testing.T) {
	state := newTestBMCState()
	// Request: [channel=1] [param=3 (IP Address)] [192] [168] [1] [50]
	reqData := []byte{0x01, 0x03, 192, 168, 1, 50}
	code, data := handleSetLANConfigParams(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Nil(t, data)

	// Verify via state
	ip := state.GetLANConfig(3)
	require.Len(t, ip, 4)
	assert.Equal(t, byte(192), ip[0])
	assert.Equal(t, byte(168), ip[1])
	assert.Equal(t, byte(1), ip[2])
	assert.Equal(t, byte(50), ip[3])
}

func TestHandleSetLANConfigParams_AuthTypeEnables(t *testing.T) {
	state := newTestBMCState()
	// Request: [channel=1] [param=2 (Auth Type Enables)] [5 bytes data]
	reqData := []byte{0x01, 0x02, 0x15, 0x15, 0x15, 0x15, 0x01}
	code, data := handleSetLANConfigParams(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Nil(t, data)

	// Verify via state
	enables := state.GetLANConfig(2)
	require.Len(t, enables, 5)
	assert.Equal(t, byte(0x15), enables[0])
	assert.Equal(t, byte(0x15), enables[1])
	assert.Equal(t, byte(0x15), enables[2])
	assert.Equal(t, byte(0x15), enables[3])
	assert.Equal(t, byte(0x01), enables[4])
}

func TestHandleSetLANConfigParams_ReadOnly(t *testing.T) {
	state := newTestBMCState()
	// Request: [channel=1] [param=1 (Auth Type Support - read-only)] [0x97]
	reqData := []byte{0x01, 0x01, 0x97}
	code, _ := handleSetLANConfigParams(reqData, state)
	assert.Equal(t, CompletionCodeInvalidField, code)
}

func TestHandleSetLANConfigParams_SetInProgress(t *testing.T) {
	state := newTestBMCState()
	// Request: [channel=1] [param=0 (Set In Progress)] [0x01]
	reqData := []byte{0x01, 0x00, 0x01}
	code, data := handleSetLANConfigParams(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Nil(t, data)
}

func TestHandleSetLANConfigParams_InvalidData(t *testing.T) {
	state := newTestBMCState()
	// Too short - less than 2 bytes
	code, _ := handleSetLANConfigParams([]byte{0x01}, state)
	assert.Equal(t, CompletionCodeInvalidField, code)

	code, _ = handleSetLANConfigParams([]byte{}, state)
	assert.Equal(t, CompletionCodeInvalidField, code)
}

func TestHandleSetLANConfigParams_UnknownParam(t *testing.T) {
	state := newTestBMCState()
	// Request: [channel=1] [param=0xFE (unknown)] [0x01]
	reqData := []byte{0x01, 0xFE, 0x01}
	code, _ := handleSetLANConfigParams(reqData, state)
	assert.Equal(t, CompletionCodeParameterOutOfRange, code)
}

func TestHandleTransportCommand(t *testing.T) {
	state := newTestBMCState()

	// Test Get LAN Config routing
	msg := &IPMIMessage{
		Command: CmdGetLANConfigParams,
		Data:    []byte{0x01, 0x01, 0x00, 0x00}, // get Auth Type Support
	}
	code, data := handleTransportCommand(msg, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 2)
	assert.Equal(t, byte(0x11), data[0])
	assert.Equal(t, byte(0x97), data[1])

	// Test Set LAN Config routing
	msg = &IPMIMessage{
		Command: CmdSetLANConfigParams,
		Data:    []byte{0x01, 0x03, 10, 0, 0, 1}, // set IP to 10.0.0.1
	}
	code, data = handleTransportCommand(msg, state)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Nil(t, data)

	// Verify the IP was set
	ip := state.GetLANConfig(3)
	assert.Equal(t, []byte{10, 0, 0, 1}, ip)
}

func TestHandleTransportCommand_Unknown(t *testing.T) {
	state := newTestBMCState()
	msg := &IPMIMessage{
		Command: 0xFF, // unknown command
		Data:    []byte{0x01},
	}
	code, _ := handleTransportCommand(msg, state)
	assert.Equal(t, CompletionCodeInvalidCommand, code)
}
