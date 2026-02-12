package ipmi

// handleAppCommand handles Application network function commands
func handleAppCommand(msg *IPMIMessage, machine MachineInterface) (CompletionCode, []byte) {
	switch msg.Command {
	case CmdGetDeviceID:
		return handleGetDeviceID()
	case CmdGetChannelAuthCapabilities:
		return handleGetChannelAuthCapabilities(msg.Data)
	case CmdSetSessionPrivilege:
		return handleSetSessionPrivilege(msg.Data)
	case CmdCloseSession:
		return CompletionCodeOK, nil
	default:
		return CompletionCodeInvalidCommand, nil
	}
}

func handleGetDeviceID() (CompletionCode, []byte) {
	// Static response for a virtual BMC
	data := []byte{
		0x20,                   // Device ID
		0x01,                   // Device Revision
		0x02,                   // Firmware Revision 1
		0x00,                   // Firmware Revision 2
		0x02,                   // IPMI Version (2.0)
		0xBF,                   // Additional Device Support
		0x00, 0x00, 0x00,       // Manufacturer ID (3 bytes)
		0x00, 0x00,             // Product ID (2 bytes)
		0x00, 0x00, 0x00, 0x00, // Aux Firmware Rev
	}
	return CompletionCodeOK, data
}

func handleGetChannelAuthCapabilities(reqData []byte) (CompletionCode, []byte) {
	data := []byte{
		0x01, // Channel number
		0x97, // Auth type support: RMCP+ (0x80) + password (0x10) + MD5 (0x04) + MD2 (0x02) + none (0x01)
		0x06, // Auth status: non-null users + null users
		0x02, // Extended capabilities: Channel 20 (IPMI 2.0)
		0x00, 0x00, 0x00, // OEM ID
		0x00, // OEM Aux
	}
	return CompletionCodeOK, data
}

func handleSetSessionPrivilege(reqData []byte) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}
	return CompletionCodeOK, []byte{reqData[0]}
}
