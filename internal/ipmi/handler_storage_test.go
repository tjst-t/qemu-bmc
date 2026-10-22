package ipmi

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/bmc"
	"github.com/tjst-t/qemu-bmc/internal/sysuuid"
)

func storageCmd(state *bmc.State, cmd uint8, data ...byte) (CompletionCode, []byte) {
	return handleIPMICommand(&IPMIMessage{TargetLun: NetFnStorage << 2, Command: cmd, Data: data}, nil, state)
}

func TestGetSDRRepositoryInfo(t *testing.T) {
	code, data := storageCmd(nil, CmdGetSDRRepositoryInfo)
	require.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 14)
	assert.Equal(t, byte(0x51), data[0])
	assert.Equal(t, uint16(1), binary.LittleEndian.Uint16(data[1:3])) // only the MC locator
}

func TestReserveSDRRepository(t *testing.T) {
	code, data := storageCmd(nil, CmdReserveSDRRepository)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Len(t, data, 2)
}

func TestGetSDR_MCLocator(t *testing.T) {
	// Header only (5 bytes) of the first record
	code, data := storageCmd(nil, CmdGetSDR, 0x01, 0x00, 0x00, 0x00, 0x00, 0x05)
	require.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 7)
	assert.Equal(t, uint16(0xFFFF), binary.LittleEndian.Uint16(data[0:2])) // no next record
	assert.Equal(t, uint16(1), binary.LittleEndian.Uint16(data[2:4]))      // record ID
	assert.Equal(t, byte(0x51), data[4])
	assert.Equal(t, byte(0x12), data[5]) // MC Device Locator
	bodyLen := int(data[6])

	// Rest of the record by ID, as ipmitool reads it
	code, data = storageCmd(nil, CmdGetSDR, 0x01, 0x00, 0x01, 0x00, 0x05, 0xFF)
	require.Equal(t, CompletionCodeOK, code)
	body := data[2:]
	require.Len(t, body, bodyLen)
	assert.Equal(t, byte(0x20), body[0]) // BMC slave address
	assert.Equal(t, "qemu-bmc", string(body[11:11+int(body[10]&0x1F)]))
}

func TestGetSDR_NotPresent(t *testing.T) {
	code, _ := storageCmd(nil, CmdGetSDR, 0x01, 0x00, 0x02, 0x00, 0x00, 0xFF)
	assert.Equal(t, CompletionCodeNotPresent, code)

	code, _ = storageCmd(nil, CmdGetSDR, 0x01, 0x00, 0x01, 0x00, 0x40, 0x05)
	assert.Equal(t, CompletionCodeParameterOutOfRange, code)

	code, _ = storageCmd(nil, CmdGetSDR, 0x01, 0x00)
	assert.Equal(t, CompletionCodeInvalidField, code)
}

func TestSEL_Empty(t *testing.T) {
	code, data := storageCmd(nil, CmdGetSELInfo)
	require.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 14)
	assert.Equal(t, byte(0x51), data[0])
	assert.Equal(t, uint16(0), binary.LittleEndian.Uint16(data[1:3]))
	assert.Equal(t, byte(0), data[13]&0x01, "Get SEL Allocation Info is not supported")

	code, data = storageCmd(nil, CmdReserveSEL)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Len(t, data, 2)

	code, _ = storageCmd(nil, CmdGetSELEntry, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF)
	assert.Equal(t, CompletionCodeNotPresent, code)
}

func TestClearSEL(t *testing.T) {
	code, data := storageCmd(nil, CmdClearSEL, 0x01, 0x00, 'C', 'L', 'R', 0xAA)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, []byte{0x01}, data) // erasure completed

	code, data = storageCmd(nil, CmdClearSEL, 0x01, 0x00, 'C', 'L', 'R', 0x00)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, []byte{0x01}, data)

	code, _ = storageCmd(nil, CmdClearSEL, 0x01, 0x00, 'X', 'L', 'R', 0xAA)
	assert.Equal(t, CompletionCodeInvalidField, code)
}

// readFRU reads FRU 0 in chunks like ipmitool does.
func readFRU(t *testing.T, state *bmc.State) []byte {
	code, info := storageCmd(state, CmdGetFRUInventoryAreaInfo, 0x00)
	require.Equal(t, CompletionCodeOK, code)
	require.Len(t, info, 3)
	assert.Equal(t, byte(0x00), info[2]) // byte access
	size := int(binary.LittleEndian.Uint16(info[0:2]))

	var fru []byte
	for len(fru) < size {
		off := len(fru)
		code, data := storageCmd(state, CmdReadFRUData, 0x00, byte(off), byte(off>>8), 16)
		require.Equal(t, CompletionCodeOK, code)
		require.Equal(t, int(data[0]), len(data)-1)
		fru = append(fru, data[1:]...)
	}
	require.Len(t, fru, size)
	return fru
}

func sumBytes(b []byte) byte {
	var s byte
	for _, v := range b {
		s += v
	}
	return s
}

// productFields parses the Product Info Area fields of a FRU image.
func productFields(t *testing.T, fru []byte) []string {
	require.GreaterOrEqual(t, len(fru), 8)
	assert.Equal(t, byte(0x01), fru[0])
	assert.Zero(t, sumBytes(fru[:8]), "common header checksum")
	start := int(fru[4]) * 8
	require.NotZero(t, start)
	area := fru[start : start+int(fru[start+1])*8]
	assert.Zero(t, sumBytes(area), "product info area checksum")

	var fields []string
	for i := 3; area[i] != 0xC1; {
		n := int(area[i] & 0x3F)
		fields = append(fields, string(area[i+1:i+1+n]))
		i += 1 + n
	}
	return fields
}

func TestFRU_Product(t *testing.T) {
	state := bmc.NewState("admin", "password")
	state.SetSystemGUID(sysuuid.MustParse("12345678-9abc-def0-1234-56789abcdef0").IPMIGUID())

	fields := productFields(t, readFRU(t, state))
	require.Len(t, fields, 7)
	assert.Equal(t, "QEMU", fields[0])
	assert.Equal(t, "qemu-bmc VM", fields[1])
	assert.Equal(t, "12345678-9abc-def0-1234-56789abcdef0", fields[4])
}

func TestFRU_NoUUID(t *testing.T) {
	fields := productFields(t, readFRU(t, bmc.NewState("admin", "password")))
	require.Len(t, fields, 7)
	assert.Equal(t, "", fields[4])
}

func TestFRU_Errors(t *testing.T) {
	code, _ := storageCmd(nil, CmdGetFRUInventoryAreaInfo, 0x01)
	assert.Equal(t, CompletionCodeNotPresent, code)

	code, _ = storageCmd(nil, CmdReadFRUData, 0x01, 0x00, 0x00, 0x08)
	assert.Equal(t, CompletionCodeNotPresent, code)

	code, _ = storageCmd(nil, CmdReadFRUData, 0x00, 0x00, 0x10, 0x08)
	assert.Equal(t, CompletionCodeParameterOutOfRange, code)

	code, _ = storageCmd(nil, CmdReadFRUData, 0x00)
	assert.Equal(t, CompletionCodeInvalidField, code)
}

func TestSensorEvent_NoDeviceSDRs(t *testing.T) {
	cmd := func(c uint8, data ...byte) (CompletionCode, []byte) {
		return handleIPMICommand(&IPMIMessage{TargetLun: NetFnSensorEvent << 2, Command: c, Data: data}, nil, nil)
	}
	code, data := cmd(CmdGetDeviceSDRInfo)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, byte(0), data[0])

	code, _ = cmd(CmdReserveDeviceSDRRepository)
	assert.Equal(t, CompletionCodeOK, code)

	code, _ = cmd(CmdGetDeviceSDR, 0x01, 0x00, 0x00, 0x00, 0x00, 0xFF)
	assert.Equal(t, CompletionCodeNotPresent, code)

	code, _ = cmd(0x2D) // Get Sensor Reading
	assert.Equal(t, CompletionCodeInvalidCommand, code)
}
