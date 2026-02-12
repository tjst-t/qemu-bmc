package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
