package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRMCPMessage(t *testing.T) {
	// RMCP header with IPMI class
	raw := []byte{0x06, 0x00, 0xff, 0x07, 0x00, 0x00, 0x00, 0x00}
	header, payload, err := ParseRMCPMessage(raw)
	require.NoError(t, err)
	assert.Equal(t, uint8(RMCPVersion1), header.Version)
	assert.Equal(t, uint8(RMCPClassIPMI), header.Class)
	assert.Equal(t, uint8(0xff), header.Sequence)
	assert.NotEmpty(t, payload)
}

func TestParseRMCPMessage_TooShort(t *testing.T) {
	raw := []byte{0x06, 0x00}
	_, _, err := ParseRMCPMessage(raw)
	assert.Error(t, err)
}

func TestParseRMCPMessage_BadVersion(t *testing.T) {
	raw := []byte{0x05, 0x00, 0xff, 0x07}
	_, _, err := ParseRMCPMessage(raw)
	assert.Error(t, err)
}

func TestSerializeRMCPMessage(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03}
	msg := SerializeRMCPMessage(RMCPClassIPMI, payload)
	assert.Equal(t, byte(0x06), msg[0]) // version
	assert.Equal(t, byte(0x00), msg[1]) // reserved
	assert.Equal(t, byte(0xff), msg[2]) // sequence
	assert.Equal(t, byte(0x07), msg[3]) // class
	assert.Equal(t, payload, msg[4:])
}

func TestChecksum(t *testing.T) {
	// Checksum of 0x20 + 0x18 should be 0xC8 (0x100 - 0x38)
	assert.Equal(t, uint8(0xC8), Checksum(0x20, 0x18))
	// Checksum of 0 should be 0
	assert.Equal(t, uint8(0x00), Checksum(0x00))
}
