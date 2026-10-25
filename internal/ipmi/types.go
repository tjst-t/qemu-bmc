package ipmi

import (
	"github.com/tjst-t/qemu-bmc/internal/machine"
)

// MachineInterface defines what the IPMI server needs from the machine layer
type MachineInterface interface {
	GetPowerState() (machine.PowerState, error)
	Reset(resetType string) error
	GetBootOverride() machine.BootOverride
	SetBootOverride(override machine.BootOverride) error
}

// RMCP constants
const (
	RMCPVersion1  = 0x06
	RMCPClassASF  = 0x06
	RMCPClassIPMI = 0x07
)

// Authentication types
const (
	AuthTypeNone     = 0x00
	AuthTypeMD2      = 0x01
	AuthTypeMD5      = 0x02
	AuthTypePassword = 0x04
	AuthTypeOEM      = 0x05
	AuthTypeRMCPPlus = 0x06
)

// IPMI Network Functions
const (
	NetFnChassis           = 0x00
	NetFnChassisResponse   = 0x01
	NetFnSensorEvent       = 0x04
	NetFnSensorEventResp   = 0x05
	NetFnApp               = 0x06
	NetFnAppResponse       = 0x07
	NetFnStorage           = 0x0A
	NetFnStorageResponse   = 0x0B
	NetFnTransport         = 0x0C
	NetFnTransportResponse = 0x0D
)

// IPMI Transport Commands
const (
	CmdSetLANConfigParams = 0x01
	CmdGetLANConfigParams = 0x02
	CmdSetSOLConfigParams = 0x21
	CmdGetSOLConfigParams = 0x22
)

// IPMI App Commands
const (
	CmdGetDeviceID                = 0x01
	CmdGetSelfTestResults         = 0x04
	CmdGetBMCGlobalEnables        = 0x2F
	CmdGetSystemGUID              = 0x37
	CmdGetChannelAuthCapabilities = 0x38
	CmdGetSessionChallenge        = 0x39
	CmdActivateSession            = 0x3A
	CmdSetSessionPrivilege        = 0x3B
	CmdCloseSession               = 0x3C
	CmdGetChannelCipherSuites     = 0x54
)

// IPMI App Commands - RMCP+ payloads (IPMI v2.0 chapter 24)
const (
	CmdActivatePayload            = 0x48
	CmdDeactivatePayload          = 0x49
	CmdGetPayloadActivationStatus = 0x4A
	CmdGetPayloadInstanceInfo     = 0x4B
	CmdSetUserPayloadAccess       = 0x4C
	CmdGetUserPayloadAccess       = 0x4D
	CmdGetChannelPayloadSupport   = 0x4E
	CmdGetChannelPayloadVersion   = 0x4F
)

// IPMI App Commands - User Management
const (
	CmdSetChannelAccess = 0x40
	CmdGetChannelAccess = 0x41
	CmdGetChannelInfo   = 0x42
	CmdSetUserAccess    = 0x43
	CmdGetUserAccess    = 0x44
	CmdSetUserName      = 0x45
	CmdGetUserName      = 0x46
	CmdSetUserPassword  = 0x47
)

// IPMI Storage Commands
const (
	CmdGetFRUInventoryAreaInfo = 0x10
	CmdReadFRUData             = 0x11
	CmdGetSDRRepositoryInfo    = 0x20
	CmdReserveSDRRepository    = 0x22
	CmdGetSDR                  = 0x23
	CmdGetSELInfo              = 0x40
	CmdReserveSEL              = 0x42
	CmdGetSELEntry             = 0x43
	CmdClearSEL                = 0x47
)

// IPMI Sensor/Event Commands
const (
	CmdGetDeviceSDRInfo           = 0x20
	CmdGetDeviceSDR               = 0x21
	CmdReserveDeviceSDRRepository = 0x22
)

// IPMI Chassis Commands
const (
	CmdGetChassisStatus = 0x01
	CmdChassisControl   = 0x02
	CmdChassisIdentify  = 0x04
	CmdSetBootOptions   = 0x08
	CmdGetBootOptions   = 0x09
)

// Chassis Control values
const (
	ChassisControlPowerDown  = 0x00
	ChassisControlPowerUp    = 0x01
	ChassisControlPowerCycle = 0x02
	ChassisControlHardReset  = 0x03
	ChassisControlPulse      = 0x04
	ChassisControlSoftOff    = 0x05
)

// RMCP+ Payload Types
const (
	PayloadTypeIPMI                = 0x00
	PayloadTypeSOL                 = 0x01
	PayloadTypeOpenSessionRequest  = 0x10
	PayloadTypeOpenSessionResponse = 0x11
	PayloadTypeRAKPMessage1        = 0x12
	PayloadTypeRAKPMessage2        = 0x13
	PayloadTypeRAKPMessage3        = 0x14
	PayloadTypeRAKPMessage4        = 0x15
)

// RMCP+ Authentication Algorithm values (IPMI 2.0 Table 13-17)
const (
	AuthAlgorithmNone       = 0x00
	AuthAlgorithmHMACSHA1   = 0x01
	AuthAlgorithmHMACMD5    = 0x02
	AuthAlgorithmHMACSHA256 = 0x03
)

// RMCP+ Integrity Algorithm values (IPMI 2.0 Table 13-18)
const (
	IntegrityAlgorithmNone           = 0x00
	IntegrityAlgorithmHMACSHA1_96    = 0x01
	IntegrityAlgorithmHMACMD5_128    = 0x02
	IntegrityAlgorithmMD5_128        = 0x03
	IntegrityAlgorithmHMACSHA256_128 = 0x04
)

// RMCP+ Confidentiality Algorithm values (IPMI 2.0 Table 13-19)
const (
	ConfAlgorithmNone      = 0x00
	ConfAlgorithmAESCBC128 = 0x01
	ConfAlgorithmXRC4_128  = 0x02
	ConfAlgorithmXRC4_40   = 0x03
)

// Open Session Response status codes (IPMI 2.0 Table 13-16)
const (
	OpenSessionStatusSuccess                    = 0x00
	OpenSessionStatusInsufficientResources      = 0x01
	OpenSessionStatusInvalidSessionID           = 0x02
	OpenSessionStatusInvalidPayloadType         = 0x03
	OpenSessionStatusInvalidAuthAlgorithm       = 0x11
	OpenSessionStatusInvalidIntegrityAlgorithm  = 0x12
	OpenSessionStatusNoMatchingAuthPayload      = 0x13
	OpenSessionStatusNoMatchingIntegrityPayload = 0x14
	OpenSessionStatusInvalidConfAlgorithm       = 0x18
	OpenSessionStatusNoCipherSuiteMatch         = 0x19
	OpenSessionStatusIllegalParameter           = 0x20
)

// CompletionCode represents an IPMI completion code
type CompletionCode uint8

const (
	CompletionCodeOK                    CompletionCode = 0x00
	CompletionCodeNodeBusy              CompletionCode = 0xC0
	CompletionCodeInvalidCommand        CompletionCode = 0xC1
	CompletionCodeInvalidForLUN         CompletionCode = 0xC2
	CompletionCodeTimeout               CompletionCode = 0xC3
	CompletionCodeOutOfSpace            CompletionCode = 0xC4
	CompletionCodeReqDataLenInvalid     CompletionCode = 0xC7
	CompletionCodeNotPresent            CompletionCode = 0xCB
	CompletionCodeInvalidField          CompletionCode = 0xCC
	CompletionCodeParameterOutOfRange   CompletionCode = 0xC9
	CompletionCodeInsufficientPrivilege CompletionCode = 0xD4
	CompletionCodeNotInPresentState     CompletionCode = 0xD5
	CompletionCodeUnspecified           CompletionCode = 0xFF
)

// Command-specific completion codes
const (
	// Activate Payload
	completionCodePayloadActive   CompletionCode = 0x80 // already active on another session
	completionCodePayloadDisabled CompletionCode = 0x81
	completionCodeCannotEncrypt   CompletionCode = 0x83
	// Deactivate Payload
	completionCodePayloadInactive CompletionCode = 0x80 // already deactivated
	// Get Channel Payload Version
	completionCodePayloadNotAvailable CompletionCode = 0x80
	// Set/Get SOL Configuration Parameters
	completionCodeParamNotSupported CompletionCode = 0x80
	completionCodeParamReadOnly     CompletionCode = 0x82
	// Close Session
	completionCodeInvalidSessionID CompletionCode = 0x87
)

// Boot device mapping for IPMI boot option parameter 5
const (
	BootDeviceNone   = 0x00
	BootDevicePXE    = 0x04
	BootDeviceDisk   = 0x08
	BootDeviceSafe   = 0x0C
	BootDeviceDiag   = 0x10
	BootDeviceCDROM  = 0x14
	BootDeviceBIOS   = 0x18
	BootDeviceFloppy = 0x3C
)
