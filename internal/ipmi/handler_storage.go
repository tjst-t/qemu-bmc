package ipmi

import (
	"encoding/binary"

	"github.com/tjst-t/qemu-bmc/internal/bmc"
	"github.com/tjst-t/qemu-bmc/internal/sysuuid"
)

// The BMC has no sensors and logs no events. The SDR repository holds only a
// Management Controller Device Locator record for the BMC itself: with an
// empty repository ipmitool tries to "rebuild" it (Clear SDR Repository, then
// reading Device SDRs) and prints errors. "sdr list" does not show this
// record type, so its output is empty. The SEL is always empty, and FRU 0 is
// a minimal FRU with a Product Info Area.

const (
	sdrVersion        = 0x51 // IPMI v1.5/v2.0 SDR and SEL version
	reservationID     = 0x0001
	mcLocatorRecordID = 0x0001
	timestampNone     = 0xFFFFFFFF // "unspecified" timestamp
)

// mcLocatorRecord is the SDR Type 12h record describing the BMC (IPMI v2.0
// section 43.9).
var mcLocatorRecord = buildMCLocatorRecord("qemu-bmc")

func buildMCLocatorRecord(name string) []byte {
	body := []byte{
		0x20,             // Device slave address (BMC)
		0x00,             // Channel number
		0x00,             // Power state notification / global initialization
		0x0E,             // Device capabilities: FRU inventory, SEL, SDR repository
		0x00, 0x00, 0x00, // Reserved
		0x06,                   // Entity ID: system management module
		0x01,                   // Entity instance
		0x00,                   // OEM
		0xC0 | byte(len(name)), // ID string type/length: 8-bit ASCII
	}
	body = append(body, name...)

	rec := make([]byte, 5, 5+len(body))
	binary.LittleEndian.PutUint16(rec[0:2], mcLocatorRecordID)
	rec[2] = sdrVersion
	rec[3] = 0x12 // Record type: MC Device Locator
	rec[4] = byte(len(body))
	return append(rec, body...)
}

// handleStorageCommand handles Storage network function commands
func handleStorageCommand(msg *IPMIMessage, state *bmc.State) (CompletionCode, []byte) {
	switch msg.Command {
	case CmdGetFRUInventoryAreaInfo:
		return handleGetFRUInventoryAreaInfo(msg.Data, state)
	case CmdReadFRUData:
		return handleReadFRUData(msg.Data, state)
	case CmdGetSDRRepositoryInfo:
		return handleGetSDRRepositoryInfo()
	case CmdReserveSDRRepository, CmdReserveSEL:
		return CompletionCodeOK, reservationResponse()
	case CmdGetSDR:
		return handleGetSDR(msg.Data)
	case CmdGetSELInfo:
		return handleGetSELInfo()
	case CmdGetSELEntry:
		return CompletionCodeNotPresent, nil
	case CmdClearSEL:
		return handleClearSEL(msg.Data)
	default:
		return CompletionCodeInvalidCommand, nil
	}
}

// handleSensorEventCommand handles Sensor/Event network function commands.
// The BMC has no device SDRs.
func handleSensorEventCommand(msg *IPMIMessage) (CompletionCode, []byte) {
	switch msg.Command {
	case CmdGetDeviceSDRInfo:
		return CompletionCodeOK, []byte{
			0x00, // Number of sensors in LUN 0
			0x00, // Static sensor population, no LUN has sensors
		}
	case CmdReserveDeviceSDRRepository:
		return CompletionCodeOK, reservationResponse()
	case CmdGetDeviceSDR:
		return CompletionCodeNotPresent, nil
	default:
		return CompletionCodeInvalidCommand, nil
	}
}

func reservationResponse() []byte {
	resp := make([]byte, 2)
	binary.LittleEndian.PutUint16(resp, reservationID)
	return resp
}

func handleGetSDRRepositoryInfo() (CompletionCode, []byte) {
	resp := make([]byte, 14)
	resp[0] = sdrVersion
	binary.LittleEndian.PutUint16(resp[1:3], 1) // Record count
	binary.LittleEndian.PutUint16(resp[3:5], 0) // Free space
	binary.LittleEndian.PutUint32(resp[5:9], timestampNone)
	binary.LittleEndian.PutUint32(resp[9:13], timestampNone)
	resp[13] = 0x02 // Operation support: Reserve SDR Repository
	return CompletionCodeOK, resp
}

// handleGetSDR returns (part of) the only SDR record. Record ID 0000h means
// the first record and FFFFh the last.
func handleGetSDR(reqData []byte) (CompletionCode, []byte) {
	if len(reqData) < 6 {
		return CompletionCodeInvalidField, nil
	}
	recordID := binary.LittleEndian.Uint16(reqData[2:4])
	if recordID != 0x0000 && recordID != 0xFFFF && recordID != mcLocatorRecordID {
		return CompletionCodeNotPresent, nil
	}
	data, code := readRange(mcLocatorRecord, int(reqData[4]), int(reqData[5]))
	if code != CompletionCodeOK {
		return code, nil
	}
	resp := []byte{0xFF, 0xFF} // Next record ID: none
	return CompletionCodeOK, append(resp, data...)
}

// readRange returns up to count bytes of buf starting at offset; count 0xFF
// means up to the end.
func readRange(buf []byte, offset, count int) ([]byte, CompletionCode) {
	if offset >= len(buf) {
		return nil, CompletionCodeParameterOutOfRange
	}
	end := len(buf)
	if count != 0xFF && offset+count < end {
		end = offset + count
	}
	return buf[offset:end], CompletionCodeOK
}

func handleGetSELInfo() (CompletionCode, []byte) {
	resp := make([]byte, 14)
	resp[0] = sdrVersion
	binary.LittleEndian.PutUint16(resp[1:3], 0) // Entries
	binary.LittleEndian.PutUint16(resp[3:5], 0) // Free space
	binary.LittleEndian.PutUint32(resp[5:9], timestampNone)
	binary.LittleEndian.PutUint32(resp[9:13], timestampNone)
	resp[13] = 0x02 // Operation support: Reserve SEL
	return CompletionCodeOK, resp
}

// handleClearSEL accepts both "initiate erase" and "get erasure status";
// the empty SEL is always erased.
func handleClearSEL(reqData []byte) (CompletionCode, []byte) {
	if len(reqData) < 6 || string(reqData[2:5]) != "CLR" ||
		(reqData[5] != 0xAA && reqData[5] != 0x00) {
		return CompletionCodeInvalidField, nil
	}
	return CompletionCodeOK, []byte{0x01} // Erasure completed
}

func handleGetFRUInventoryAreaInfo(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}
	if reqData[0] != 0 {
		return CompletionCodeNotPresent, nil
	}
	fru := buildFRU(systemSerial(state))
	resp := make([]byte, 3)
	binary.LittleEndian.PutUint16(resp[0:2], uint16(len(fru)))
	resp[2] = 0x00 // Accessed by bytes
	return CompletionCodeOK, resp
}

func handleReadFRUData(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 4 {
		return CompletionCodeInvalidField, nil
	}
	if reqData[0] != 0 {
		return CompletionCodeNotPresent, nil
	}
	fru := buildFRU(systemSerial(state))
	// The FRU is shorter than 255 bytes, so readRange's 0xFF case is the same
	// as reading 255 bytes.
	data, code := readRange(fru, int(binary.LittleEndian.Uint16(reqData[1:3])), int(reqData[3]))
	if code != CompletionCodeOK {
		return code, nil
	}
	return CompletionCodeOK, append([]byte{byte(len(data))}, data...)
}

// systemSerial returns the system UUID in canonical form, or "" if unset.
func systemSerial(state *bmc.State) string {
	if state == nil {
		return ""
	}
	guid := state.SystemGUID()
	if len(guid) != 16 {
		return ""
	}
	var u sysuuid.UUID
	for i := range u {
		u[i] = guid[15-i] // IPMI encoding is LSB first
	}
	return u.String()
}

// buildFRU returns a FRU image (IPMI Platform Management FRU Information
// Storage Definition v1.0) holding a Common Header and a Product Info Area.
func buildFRU(serial string) []byte {
	area := []byte{
		0x01, // Format version
		0x00, // Area length (set below)
		0x00, // Language code: English
	}
	for _, field := range []string{
		"QEMU",        // Manufacturer name
		"qemu-bmc VM", // Product name
		"",            // Part/model number
		"",            // Version
		serial,        // Serial number
		"",            // Asset tag
		"",            // FRU file ID
	} {
		area = append(area, 0xC0|byte(len(field))) // 8-bit ASCII
		area = append(area, field...)
	}
	area = append(area, 0xC1) // End of fields
	for (len(area)+1)%8 != 0 {
		area = append(area, 0x00)
	}
	area = append(area, 0x00) // Checksum
	area[1] = byte(len(area) / 8)
	area[len(area)-1] = zeroChecksum(area[:len(area)-1])

	header := []byte{
		0x01, // Format version
		0x00, // Internal use area offset
		0x00, // Chassis info area offset
		0x00, // Board area offset
		0x01, // Product info area offset (in 8-byte units)
		0x00, // MultiRecord area offset
		0x00, // Pad
		0x00, // Checksum
	}
	header[7] = zeroChecksum(header[:7])
	return append(header, area...)
}

// zeroChecksum returns the byte that makes the sum of data plus it zero.
func zeroChecksum(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum += b
	}
	return -sum
}
