package ipmi

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// RMCPHeader is the RMCP message header (4 bytes)
type RMCPHeader struct {
	Version  uint8
	Reserved uint8
	Sequence uint8
	Class    uint8
}

// IPMISessionHeader is the IPMI v1.5 session wrapper
type IPMISessionHeader struct {
	AuthType       uint8
	SequenceNumber uint32
	SessionID      uint32
}

// IPMIMessage is an IPMI message
type IPMIMessage struct {
	TargetAddress uint8
	TargetLun     uint8 // NetFn (upper 6 bits) + LUN (lower 2 bits)
	Checksum      uint8
	SourceAddress uint8
	SourceLun     uint8 // Sequence (upper 6 bits) + LUN (lower 2 bits)
	Command       uint8
	Data          []byte
}

// GetNetFn returns the network function from the message
func (m *IPMIMessage) GetNetFn() uint8 {
	return (m.TargetLun >> 2) & 0x3F
}

// ParseRMCPMessage parses a raw RMCP message
func ParseRMCPMessage(data []byte) (*RMCPHeader, []byte, error) {
	if len(data) < 4 {
		return nil, nil, fmt.Errorf("RMCP message too short: %d bytes", len(data))
	}

	header := &RMCPHeader{}
	buf := bytes.NewBuffer(data)
	if err := binary.Read(buf, binary.LittleEndian, header); err != nil {
		return nil, nil, fmt.Errorf("parsing RMCP header: %w", err)
	}

	if header.Version != RMCPVersion1 {
		return nil, nil, fmt.Errorf("unsupported RMCP version: %d", header.Version)
	}

	return header, data[4:], nil
}

// SerializeRMCPMessage creates an RMCP-framed message
func SerializeRMCPMessage(class uint8, payload []byte) []byte {
	header := RMCPHeader{
		Version:  RMCPVersion1,
		Reserved: 0x00,
		Sequence: 0xFF,
		Class:    class,
	}

	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, &header)
	buf.Write(payload)
	return buf.Bytes()
}

// ParseIPMI15Message parses an IPMI v1.5 session wrapper + message
func ParseIPMI15Message(data []byte) (*IPMISessionHeader, *IPMIMessage, error) {
	if len(data) < 10 {
		return nil, nil, fmt.Errorf("IPMI session message too short")
	}

	buf := bytes.NewBuffer(data)

	session := &IPMISessionHeader{}
	if err := binary.Read(buf, binary.LittleEndian, &session.AuthType); err != nil {
		return nil, nil, err
	}
	if err := binary.Read(buf, binary.LittleEndian, &session.SequenceNumber); err != nil {
		return nil, nil, err
	}
	if err := binary.Read(buf, binary.LittleEndian, &session.SessionID); err != nil {
		return nil, nil, err
	}

	// If auth type is RMCP+, this will be handled by rmcp_plus.go
	if session.AuthType == AuthTypeRMCPPlus {
		return session, nil, nil
	}

	// Skip 16-byte auth code for authenticated sessions
	if session.AuthType != AuthTypeNone {
		if buf.Len() < 16 {
			return nil, nil, fmt.Errorf("IPMI auth code truncated")
		}
		buf.Next(16)
	}

	// Read message length
	var msgLen uint8
	if err := binary.Read(buf, binary.LittleEndian, &msgLen); err != nil {
		return nil, nil, err
	}

	if buf.Len() < int(msgLen) {
		return nil, nil, fmt.Errorf("IPMI message truncated")
	}

	msgData := make([]byte, msgLen)
	copy(msgData, buf.Bytes()[:msgLen])

	msg, err := ParseIPMIMessageBytes(msgData)
	if err != nil {
		return nil, nil, err
	}

	return session, msg, nil
}

// ParseIPMIMessageBytes parses IPMI message bytes
func ParseIPMIMessageBytes(data []byte) (*IPMIMessage, error) {
	if len(data) < 7 {
		return nil, fmt.Errorf("IPMI message too short: %d", len(data))
	}

	msg := &IPMIMessage{
		TargetAddress: data[0],
		TargetLun:     data[1],
		Checksum:      data[2],
		SourceAddress: data[3],
		SourceLun:     data[4],
		Command:       data[5],
	}

	if len(data) > 7 {
		msg.Data = data[6 : len(data)-1] // exclude trailing checksum
	}

	return msg, nil
}

// SerializeIPMIResponse creates an IPMI v1.5 response
func SerializeIPMIResponse(session *IPMISessionHeader, netFn uint8, cmd uint8, code CompletionCode, data []byte) []byte {
	// Build IPMI message
	msg := buildIPMIResponseMessage(netFn, cmd, code, data)

	// Wrap in session
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, session.AuthType)
	binary.Write(buf, binary.LittleEndian, session.SequenceNumber)
	binary.Write(buf, binary.LittleEndian, session.SessionID)

	// Include 16-byte auth code for authenticated sessions
	if session.AuthType != AuthTypeNone {
		authCode := make([]byte, 16)
		buf.Write(authCode)
	}

	binary.Write(buf, binary.LittleEndian, uint8(len(msg)))
	buf.Write(msg)

	return buf.Bytes()
}

func buildIPMIResponseMessage(netFn uint8, cmd uint8, code CompletionCode, data []byte) []byte {
	return buildIPMIResponseMessageWithSeq(netFn, cmd, code, data, 0x00)
}

func buildIPMIResponseMessageWithSeq(netFn uint8, cmd uint8, code CompletionCode, data []byte, reqSeqLun uint8) []byte {
	targetAddr := uint8(0x81) // remote console
	sourceAddr := uint8(0x20) // BMC
	targetLun := (netFn << 2) | 0x00
	sourceLun := reqSeqLun // echo request's sequence number and LUN

	// Header checksum
	headerSum := uint32(targetAddr) + uint32(targetLun)
	headerChecksum := uint8(0x100 - (headerSum & 0xFF))

	// Build message
	var buf bytes.Buffer
	buf.WriteByte(targetAddr)
	buf.WriteByte(targetLun)
	buf.WriteByte(headerChecksum)
	buf.WriteByte(sourceAddr)
	buf.WriteByte(sourceLun)
	buf.WriteByte(cmd)
	buf.WriteByte(uint8(code))
	buf.Write(data)

	// Data checksum
	dataSum := uint32(sourceAddr) + uint32(sourceLun) + uint32(cmd) + uint32(code)
	for _, b := range data {
		dataSum += uint32(b)
	}
	dataChecksum := uint8(0x100 - (dataSum & 0xFF))
	buf.WriteByte(dataChecksum)

	return buf.Bytes()
}

// Checksum calculates a two's complement checksum
func Checksum(data ...uint8) uint8 {
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	return uint8(0x100 - (sum & 0xFF))
}
