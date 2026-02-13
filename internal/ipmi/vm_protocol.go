package ipmi

import "fmt"

// VM protocol framing characters
const (
	VMMsgChar    = 0xA0
	VMCmdChar    = 0xA1
	VMEscapeChar = 0xAA
)

// VM hardware control commands (BMC -> VM direction)
const (
	VMCmdNoAttn           = 0x00
	VMCmdAttn             = 0x01
	VMCmdAttnIRQ          = 0x02
	VMCmdPowerOff         = 0x03
	VMCmdReset            = 0x04
	VMCmdEnableIRQ        = 0x05
	VMCmdDisableIRQ       = 0x06
	VMCmdSendNMI          = 0x07
	VMCmdCapabilities     = 0x08
	VMCmdGracefulShutdown = 0x09
)

// VM protocol version
const (
	VMCmdVersion  = 0xFF
	VMProtocolVer = 0x01
)

// VM capability flags
const (
	VMCapPower            = 0x01
	VMCapReset            = 0x02
	VMCapIRQ              = 0x04
	VMCapNMI              = 0x08
	VMCapAttn             = 0x10
	VMCapGracefulShutdown = 0x20
)

// VMIPMIRequest represents an IPMI request received over the VM protocol.
type VMIPMIRequest struct {
	Seq   uint8
	NetFn uint8
	LUN   uint8
	Cmd   uint8
	Data  []byte
}

// vmEscapeBytes escapes special bytes (0xA0, 0xA1, 0xAA) in data.
// For each special byte, output 0xAA followed by byte | 0x10.
func vmEscapeBytes(data []byte) []byte {
	result := make([]byte, 0, len(data))
	for _, b := range data {
		if b == VMMsgChar || b == VMCmdChar || b == VMEscapeChar {
			result = append(result, VMEscapeChar, b|0x10)
		} else {
			result = append(result, b)
		}
	}
	return result
}

// vmUnescapeBytes reverses the escape encoding.
// When encountering 0xAA, the next byte has bit 4 cleared (& ^0x10).
func vmUnescapeBytes(data []byte) ([]byte, error) {
	result := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == VMEscapeChar {
			i++
			if i >= len(data) {
				return nil, fmt.Errorf("trailing escape byte in VM protocol data")
			}
			result = append(result, data[i] & ^uint8(0x10))
		} else {
			result = append(result, data[i])
		}
	}
	return result, nil
}

// vmChecksum calculates the two's complement checksum of data.
// This is the same algorithm as Checksum() in rmcp.go.
func vmChecksum(data []byte) uint8 {
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	return uint8(0x100 - (sum & 0xFF))
}

// vmParseIPMIRequest parses an unescaped IPMI request from the VM protocol.
// Format: [seq] [netfn<<2|lun] [cmd] [data...] [checksum]
// Minimum 4 bytes: seq + netfn/lun + cmd + checksum.
func vmParseIPMIRequest(data []byte) (*VMIPMIRequest, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("VM IPMI request too short: %d bytes (minimum 4)", len(data))
	}

	// Verify checksum: two's complement sum of all bytes should be 0
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	if uint8(sum&0xFF) != 0 {
		return nil, fmt.Errorf("VM IPMI request checksum mismatch")
	}

	req := &VMIPMIRequest{
		Seq:   data[0],
		NetFn: (data[1] >> 2) & 0x3F,
		LUN:   data[1] & 0x03,
		Cmd:   data[2],
	}

	// Data is between cmd and checksum
	if len(data) > 4 {
		req.Data = make([]byte, len(data)-4)
		copy(req.Data, data[3:len(data)-1])
	}

	return req, nil
}

// vmBuildIPMIResponse builds an IPMI response for the VM protocol.
// Format: [seq] [netfn<<2|lun] [cmd] [cc] [data...] [checksum]
func vmBuildIPMIResponse(seq, netFn, lun, cmd uint8, cc CompletionCode, data []byte) []byte {
	// Calculate size: seq + netfn/lun + cmd + cc + data + checksum
	size := 4 + len(data) + 1
	result := make([]byte, 0, size)

	result = append(result, seq)
	result = append(result, (netFn<<2)|(lun&0x03))
	result = append(result, cmd)
	result = append(result, uint8(cc))
	result = append(result, data...)

	// Calculate checksum over all bytes so far
	checksum := vmChecksum(result)
	result = append(result, checksum)

	return result
}

// vmParseControlCommand parses a VM control command.
// Returns the command code, remaining data bytes, and any error.
func vmParseControlCommand(data []byte) (uint8, []byte, error) {
	if len(data) == 0 {
		return 0, nil, fmt.Errorf("VM control command is empty")
	}

	cmd := data[0]
	var rest []byte
	if len(data) > 1 {
		rest = data[1:]
	}

	return cmd, rest, nil
}

// vmBuildControlCommand builds a VM control command with optional data bytes.
// Format: [cmd] [data...]
func vmBuildControlCommand(cmd uint8, data ...byte) []byte {
	result := make([]byte, 0, 1+len(data))
	result = append(result, cmd)
	result = append(result, data...)
	return result
}
