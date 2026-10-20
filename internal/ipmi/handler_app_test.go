package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tjst-t/qemu-bmc/internal/bmc"
)

func TestHandleGetDeviceID(t *testing.T) {
	code, data := handleGetDeviceID()
	assert.Equal(t, CompletionCodeOK, code)
	assert.NotEmpty(t, data)
	assert.Equal(t, byte(0x20), data[0]) // Device ID
	assert.Equal(t, byte(0x02), data[4]) // IPMI 2.0
}

func TestHandleGetChannelAuthCapabilities(t *testing.T) {
	code, data := handleGetChannelAuthCapabilities([]byte{0x0e, 0x04})
	assert.Equal(t, CompletionCodeOK, code)
	assert.NotEmpty(t, data)
	assert.Equal(t, byte(0x01), data[0])     // channel
	assert.NotZero(t, data[1]&0x80)          // RMCP+ supported
	assert.Equal(t, byte(0x02), data[3]&0x02) // IPMI 2.0 extended
}

func TestHandleSetSessionPrivilege(t *testing.T) {
	code, data := handleSetSessionPrivilege([]byte{0x04})
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, byte(0x04), data[0])
}

func TestHandleSetSessionPrivilege_Empty(t *testing.T) {
	code, _ := handleSetSessionPrivilege([]byte{})
	assert.Equal(t, CompletionCodeInvalidField, code)
}

func TestHandleGetSystemGUID(t *testing.T) {
	state := bmc.NewState("admin", "password")
	guid := []byte{0xff, 0xee, 0xdd, 0xcc, 0xbb, 0xaa, 0x99, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0x00}
	state.SetSystemGUID(guid)

	code, data := handleAppCommand(&IPMIMessage{TargetLun: NetFnApp << 2, Command: CmdGetSystemGUID}, nil, state)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, guid, data)
}

func TestHandleGetSystemGUID_Unset(t *testing.T) {
	code, _ := handleAppCommand(&IPMIMessage{TargetLun: NetFnApp << 2, Command: CmdGetSystemGUID}, nil, bmc.NewState("admin", "password"))
	assert.Equal(t, CompletionCodeInvalidCommand, code)

	code, _ = handleAppCommand(&IPMIMessage{TargetLun: NetFnApp << 2, Command: CmdGetSystemGUID}, nil, nil)
	assert.Equal(t, CompletionCodeInvalidCommand, code)
}
