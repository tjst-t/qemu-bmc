package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMProtocol_EscapeBytes(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{
			name:     "no escaping needed",
			input:    []byte{0x01, 0x02, 0x03},
			expected: []byte{0x01, 0x02, 0x03},
		},
		{
			name:     "escape 0xA0",
			input:    []byte{0xA0},
			expected: []byte{0xAA, 0xB0},
		},
		{
			name:     "escape 0xA1",
			input:    []byte{0xA1},
			expected: []byte{0xAA, 0xB1},
		},
		{
			name:     "escape 0xAA",
			input:    []byte{0xAA},
			expected: []byte{0xAA, 0xBA},
		},
		{
			name:     "mixed data with special bytes",
			input:    []byte{0x01, 0xA0, 0x02, 0xAA},
			expected: []byte{0x01, 0xAA, 0xB0, 0x02, 0xAA, 0xBA},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := vmEscapeBytes(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestVMProtocol_UnescapeBytes(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{
			name:     "no escaping",
			input:    []byte{0x01, 0x02},
			expected: []byte{0x01, 0x02},
		},
		{
			name:     "unescape 0xB0 to 0xA0",
			input:    []byte{0xAA, 0xB0},
			expected: []byte{0xA0},
		},
		{
			name:     "unescape 0xB1 to 0xA1",
			input:    []byte{0xAA, 0xB1},
			expected: []byte{0xA1},
		},
		{
			name:     "unescape 0xBA to 0xAA",
			input:    []byte{0xAA, 0xBA},
			expected: []byte{0xAA},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := vmUnescapeBytes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestVMProtocol_UnescapeBytes_TrailingEscape(t *testing.T) {
	_, err := vmUnescapeBytes([]byte{0x01, 0xAA})
	assert.Error(t, err)
}

func TestVMProtocol_VMChecksum(t *testing.T) {
	// Same algorithm as Checksum() in rmcp.go
	// Checksum of {0x20, 0x18} should be 0xC8
	assert.Equal(t, uint8(0xC8), vmChecksum([]byte{0x20, 0x18}))
	// Checksum of {0} should be 0
	assert.Equal(t, uint8(0x00), vmChecksum([]byte{0x00}))
	// Checksum of empty should be 0
	assert.Equal(t, uint8(0x00), vmChecksum([]byte{}))
}

func TestVMProtocol_ParseIPMIMessage(t *testing.T) {
	// Construct a valid Get Device ID request:
	// seq=0x01, netfn=App(0x06), lun=0x00, cmd=GetDeviceID(0x01), no data
	seq := uint8(0x01)
	netFnLun := uint8((NetFnApp << 2) | 0x00) // 0x18
	cmd := uint8(CmdGetDeviceID)               // 0x01

	// Calculate checksum over seq, netFnLun, cmd
	sum := uint32(seq) + uint32(netFnLun) + uint32(cmd)
	checksum := uint8(0x100 - (sum & 0xFF))

	data := []byte{seq, netFnLun, cmd, checksum}

	req, err := vmParseIPMIRequest(data)
	require.NoError(t, err)
	assert.Equal(t, uint8(0x01), req.Seq)
	assert.Equal(t, uint8(NetFnApp), req.NetFn)
	assert.Equal(t, uint8(0x00), req.LUN)
	assert.Equal(t, uint8(CmdGetDeviceID), req.Cmd)
	assert.Empty(t, req.Data)
}

func TestVMProtocol_ParseIPMIMessage_WithData(t *testing.T) {
	// Construct a request with data payload
	seq := uint8(0x02)
	netFnLun := uint8((NetFnChassis << 2) | 0x01) // NetFn=0x00, LUN=1
	cmd := uint8(CmdSetBootOptions)                // 0x08
	payload := []byte{0x05, 0x80, 0x14, 0x00, 0x00}

	// Calculate checksum over all bytes before checksum
	var sum uint32
	sum += uint32(seq) + uint32(netFnLun) + uint32(cmd)
	for _, b := range payload {
		sum += uint32(b)
	}
	checksum := uint8(0x100 - (sum & 0xFF))

	data := []byte{seq, netFnLun, cmd}
	data = append(data, payload...)
	data = append(data, checksum)

	req, err := vmParseIPMIRequest(data)
	require.NoError(t, err)
	assert.Equal(t, uint8(0x02), req.Seq)
	assert.Equal(t, uint8(NetFnChassis), req.NetFn)
	assert.Equal(t, uint8(0x01), req.LUN)
	assert.Equal(t, uint8(CmdSetBootOptions), req.Cmd)
	assert.Equal(t, payload, req.Data)
}

func TestVMProtocol_ParseIPMIMessage_BadChecksum(t *testing.T) {
	seq := uint8(0x01)
	netFnLun := uint8((NetFnApp << 2) | 0x00)
	cmd := uint8(CmdGetDeviceID)
	badChecksum := uint8(0xFF) // wrong checksum

	data := []byte{seq, netFnLun, cmd, badChecksum}

	_, err := vmParseIPMIRequest(data)
	assert.Error(t, err)
}

func TestVMProtocol_ParseIPMIMessage_TooShort(t *testing.T) {
	// Minimum is 4 bytes (seq + netfn/lun + cmd + checksum)
	data := []byte{0x01, 0x18, 0x01} // only 3 bytes
	_, err := vmParseIPMIRequest(data)
	assert.Error(t, err)
}

func TestVMProtocol_BuildIPMIResponse(t *testing.T) {
	seq := uint8(0x01)
	netFn := uint8(NetFnAppResponse) // 0x07
	lun := uint8(0x00)
	cmd := uint8(CmdGetDeviceID)
	cc := CompletionCodeOK
	payload := []byte{0x20, 0x01}

	result := vmBuildIPMIResponse(seq, netFn, lun, cmd, cc, payload)

	// Verify structure: [seq] [netfn<<2|lun] [cmd] [cc] [data...] [checksum]
	require.True(t, len(result) >= 5)
	assert.Equal(t, seq, result[0])
	assert.Equal(t, uint8((netFn<<2)|lun), result[1])
	assert.Equal(t, cmd, result[2])
	assert.Equal(t, uint8(cc), result[3])
	assert.Equal(t, payload, result[4:len(result)-1])

	// Verify checksum
	var sum uint32
	for _, b := range result[:len(result)-1] {
		sum += uint32(b)
	}
	checksumByte := result[len(result)-1]
	totalSum := (sum + uint32(checksumByte)) & 0xFF
	assert.Equal(t, uint8(0x00), uint8(totalSum), "checksum should make total sum zero mod 256")
}

func TestVMProtocol_BuildIPMIResponse_NoData(t *testing.T) {
	seq := uint8(0x03)
	netFn := uint8(NetFnChassisResponse)
	lun := uint8(0x00)
	cmd := uint8(CmdGetChassisStatus)
	cc := CompletionCodeOK

	result := vmBuildIPMIResponse(seq, netFn, lun, cmd, cc, nil)

	// Structure: [seq] [netfn<<2|lun] [cmd] [cc] [checksum]
	assert.Len(t, result, 5)
	assert.Equal(t, seq, result[0])
	assert.Equal(t, uint8((netFn<<2)|lun), result[1])
	assert.Equal(t, cmd, result[2])
	assert.Equal(t, uint8(cc), result[3])

	// Verify checksum
	var sum uint32
	for _, b := range result[:len(result)-1] {
		sum += uint32(b)
	}
	checksumByte := result[len(result)-1]
	totalSum := (sum + uint32(checksumByte)) & 0xFF
	assert.Equal(t, uint8(0x00), uint8(totalSum))
}

func TestVMProtocol_ParseControlCommand(t *testing.T) {
	tests := []struct {
		name        string
		input       []byte
		expectedCmd uint8
		expectedLen int
	}{
		{
			name:        "version command",
			input:       []byte{VMCmdVersion, VMProtocolVer},
			expectedCmd: VMCmdVersion,
			expectedLen: 1,
		},
		{
			name:        "capabilities command",
			input:       []byte{VMCmdCapabilities, 0x3F},
			expectedCmd: VMCmdCapabilities,
			expectedLen: 1,
		},
		{
			name:        "noattn command no data",
			input:       []byte{VMCmdNoAttn},
			expectedCmd: VMCmdNoAttn,
			expectedLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, data, err := vmParseControlCommand(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedCmd, cmd)
			assert.Len(t, data, tt.expectedLen)
		})
	}
}

func TestVMProtocol_ParseControlCommand_Empty(t *testing.T) {
	_, _, err := vmParseControlCommand([]byte{})
	assert.Error(t, err)
}

func TestVMProtocol_BuildControlCommand(t *testing.T) {
	// Build NOATTN command (no data)
	result := vmBuildControlCommand(VMCmdNoAttn)
	assert.Equal(t, []byte{VMCmdNoAttn}, result)

	// Build POWEROFF command (no data)
	result = vmBuildControlCommand(VMCmdPowerOff)
	assert.Equal(t, []byte{VMCmdPowerOff}, result)

	// Build VERSION command with version byte
	result = vmBuildControlCommand(VMCmdVersion, VMProtocolVer)
	assert.Equal(t, []byte{VMCmdVersion, VMProtocolVer}, result)

	// Build CAPABILITIES command with capability flags
	caps := uint8(VMCapPower | VMCapReset | VMCapGracefulShutdown)
	result = vmBuildControlCommand(VMCmdCapabilities, caps)
	assert.Equal(t, []byte{VMCmdCapabilities, caps}, result)
}

func TestVMProtocol_EscapeUnescapeRoundtrip(t *testing.T) {
	testCases := [][]byte{
		{0x01, 0x02, 0x03},
		{0xA0, 0xA1, 0xAA},
		{0x00, 0xA0, 0xFF, 0xA1, 0x55, 0xAA, 0x99},
		{},
		{0xA0},
		{0xAA, 0xAA, 0xAA},
	}

	for _, original := range testCases {
		escaped := vmEscapeBytes(original)
		unescaped, err := vmUnescapeBytes(escaped)
		require.NoError(t, err)
		assert.Equal(t, original, unescaped, "roundtrip failed for %v", original)
	}
}
