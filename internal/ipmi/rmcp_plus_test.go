package ipmi

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/bmc"
	"github.com/tjst-t/qemu-bmc/internal/machine"
)

func TestOpenSession(t *testing.T) {
	sm := NewSessionManager()

	// Build Open Session Request
	req := buildOpenSessionRequest(0x01, 0x12345678)
	data := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, req)

	resp, err := HandleRMCPPlusMessage(data, sm, "admin", "password", nil, bmc.NewState("admin", "password"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Parse response - skip RMCP+ header (12 bytes)
	assert.Equal(t, uint8(AuthTypeRMCPPlus), resp[0])
	assert.Equal(t, uint8(PayloadTypeOpenSessionResponse), resp[1])

	// Check status code (byte 13 = message tag, byte 14 = status)
	assert.Equal(t, uint8(0x01), resp[12]) // message tag echoed
	assert.Equal(t, uint8(0x00), resp[13]) // success
}

func TestRAKPAuthentication(t *testing.T) {
	sm := NewSessionManager()
	user := "admin"
	pass := "password"

	// Step 1: Open Session
	openReq := buildOpenSessionRequest(0x01, 0xAAAABBBB)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	// Extract ManagedSystemSessionID from response (bytes 20-23)
	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	// Step 2: RAKP Message 1
	rakp1 := buildRAKPMessage1(0x02, managedSessionID, user)
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1)
	rakp2Resp, err := HandleRMCPPlusMessage(rakp1Data, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	// Check RAKP2 status (byte 13)
	assert.Equal(t, uint8(0x00), rakp2Resp[13], "RAKP2 should be success")

	// Extract server random and GUID from RAKP2
	serverRandom := rakp2Resp[20:36]
	serverGUID := rakp2Resp[36:52]

	// Step 3: Build and send RAKP Message 3
	session, ok := sm.GetSession(managedSessionID)
	require.True(t, ok)

	rakp3AuthBuf := buildRAKP3AuthBuf(session.ManagedSystemRandomNumber[:], session.RemoteConsoleSessionID, session.RequestedPrivilegeLevel, session.UserNameLength, session.UserName)
	mac := hmac.New(sha1.New, []byte(pass))
	mac.Write(rakp3AuthBuf)
	rakp3AuthCode := mac.Sum(nil)

	rakp3 := buildRAKPMessage3(0x03, managedSessionID, rakp3AuthCode)
	rakp3Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage3, 0, 0, rakp3)
	rakp4Resp, err := HandleRMCPPlusMessage(rakp3Data, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	// Check RAKP4 status (byte 13)
	assert.Equal(t, uint8(0x00), rakp4Resp[13], "RAKP4 should be success")

	// Verify session is authenticated
	assert.True(t, session.Authenticated)
	assert.NotNil(t, session.SessionIntegrityKey)
	assert.NotNil(t, session.IntegrityKey)
	assert.NotNil(t, session.ConfidentialityKey)

	_ = serverRandom
	_ = serverGUID
}

func TestRAKPAuthentication_WrongPassword(t *testing.T) {
	sm := NewSessionManager()
	user := "admin"
	pass := "password"

	// Open Session
	openReq := buildOpenSessionRequest(0x01, 0xAAAABBBB)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	// RAKP1
	rakp1 := buildRAKPMessage1(0x02, managedSessionID, user)
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1)
	_, err = HandleRMCPPlusMessage(rakp1Data, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	// RAKP3 with wrong auth code (simulating wrong password)
	wrongAuthCode := make([]byte, 20)
	rakp3 := buildRAKPMessage3(0x03, managedSessionID, wrongAuthCode)
	rakp3Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage3, 0, 0, rakp3)
	rakp4Resp, err := HandleRMCPPlusMessage(rakp3Data, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	// RAKP4 should indicate failure (status != 0x00)
	assert.NotEqual(t, uint8(0x00), rakp4Resp[13], "RAKP4 should fail with wrong password")
}

func TestRAKP_AuthWithBMCStateUser(t *testing.T) {
	sm := NewSessionManager()
	state := bmc.NewState("admin", "password")

	// Create a new user via BMC state (simulating in-band creation)
	state.SetUserName(3, "maas")
	state.SetUserPassword(3, "maas-secret")
	state.SetUserAccess(1, 3, bmc.UserAccess{Enabled: true, PrivilegeLimit: 4, IPMIMessaging: true, LinkAuth: true})

	// Step 1: Open Session
	openReq := buildOpenSessionRequest(0x01, 0x12345678)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// Extract managed system session ID from response (bytes 20-23, after 12-byte header)
	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	// Step 2: RAKP Message 1 with "maas" user
	rakp1Req := buildRAKPMessage1(0x02, managedSessionID, "maas")
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1Req)
	rakp2Resp, err := HandleRMCPPlusMessage(rakp1Data, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// Check RAKP2 status = success (byte 13)
	assert.Equal(t, uint8(0x00), rakp2Resp[13], "RAKP2 should succeed for BMC state user")

	// Step 3: RAKP Message 3 (compute proper auth code with "maas-secret")
	session, ok := sm.GetSession(managedSessionID)
	require.True(t, ok)

	rakp3AuthBuf := buildRAKP3AuthBuf(session.ManagedSystemRandomNumber[:], session.RemoteConsoleSessionID, session.RequestedPrivilegeLevel, session.UserNameLength, session.UserName)
	mac := hmac.New(sha1.New, []byte("maas-secret"))
	mac.Write(rakp3AuthBuf)
	rakp3AuthCode := mac.Sum(nil)

	rakp3 := buildRAKPMessage3(0x03, managedSessionID, rakp3AuthCode)
	rakp3Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage3, 0, 0, rakp3)
	rakp4Resp, err := HandleRMCPPlusMessage(rakp3Data, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// Check RAKP4 status = success (byte 13)
	assert.Equal(t, uint8(0x00), rakp4Resp[13], "RAKP4 should succeed for BMC state user")

	// Verify session is authenticated
	assert.True(t, session.Authenticated)
	assert.NotNil(t, session.SessionIntegrityKey)
	assert.NotNil(t, session.IntegrityKey)
	assert.NotNil(t, session.ConfidentialityKey)
}

func TestRAKP_AuthWithBMCStateUser_WrongPassword(t *testing.T) {
	sm := NewSessionManager()
	state := bmc.NewState("admin", "password")

	// Create a new user via BMC state
	state.SetUserName(3, "maas")
	state.SetUserPassword(3, "maas-secret")
	state.SetUserAccess(1, 3, bmc.UserAccess{Enabled: true, PrivilegeLimit: 4, IPMIMessaging: true, LinkAuth: true})

	// Open Session
	openReq := buildOpenSessionRequest(0x01, 0x12345678)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	// RAKP1 with "maas" user
	rakp1Req := buildRAKPMessage1(0x02, managedSessionID, "maas")
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1Req)
	_, err = HandleRMCPPlusMessage(rakp1Data, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// RAKP3 with wrong password ("wrong-password" instead of "maas-secret")
	session, ok := sm.GetSession(managedSessionID)
	require.True(t, ok)

	rakp3AuthBuf := buildRAKP3AuthBuf(session.ManagedSystemRandomNumber[:], session.RemoteConsoleSessionID, session.RequestedPrivilegeLevel, session.UserNameLength, session.UserName)
	mac := hmac.New(sha1.New, []byte("wrong-password"))
	mac.Write(rakp3AuthBuf)
	rakp3AuthCode := mac.Sum(nil)

	rakp3 := buildRAKPMessage3(0x03, managedSessionID, rakp3AuthCode)
	rakp3Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage3, 0, 0, rakp3)
	rakp4Resp, err := HandleRMCPPlusMessage(rakp3Data, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// RAKP4 should indicate failure
	assert.NotEqual(t, uint8(0x00), rakp4Resp[13], "RAKP4 should fail with wrong password for BMC state user")
}

func TestRAKP_AuthWithUnknownUser(t *testing.T) {
	sm := NewSessionManager()
	state := bmc.NewState("admin", "password")

	// Open Session
	openReq := buildOpenSessionRequest(0x01, 0x12345678)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	// RAKP1 with unknown user (not in BMC state, not hardcoded)
	rakp1Req := buildRAKPMessage1(0x02, managedSessionID, "unknown")
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1Req)
	rakp2Resp, err := HandleRMCPPlusMessage(rakp1Data, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// RAKP2 should indicate invalid username (0x0D)
	assert.Equal(t, uint8(0x0D), rakp2Resp[13], "RAKP2 should fail with invalid username")
}

func TestEncryptDecryptAESCBC(t *testing.T) {
	key := make([]byte, 20)
	for i := range key {
		key[i] = byte(i)
	}

	plaintext := []byte("Hello, IPMI World!")
	encrypted, err := encryptAESCBC(key, plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, encrypted)

	decrypted, err := decryptAESCBC(key, encrypted)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestPadUnpad(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5}
	padded := padPayload(data)
	assert.Equal(t, 0, len(padded)%16, "padded data should be aligned to 16 bytes")

	unpadded, err := unpadPayload(padded)
	require.NoError(t, err)
	assert.Equal(t, data, unpadded)
}

// Helper functions for building test messages

func wrapRMCPPlusPayload(payloadType uint8, sessionID uint32, seq uint32, payload []byte) []byte {
	buf := make([]byte, 12+len(payload))
	buf[0] = AuthTypeRMCPPlus
	buf[1] = payloadType
	binary.LittleEndian.PutUint32(buf[2:], sessionID)
	binary.LittleEndian.PutUint32(buf[6:], seq)
	binary.LittleEndian.PutUint16(buf[10:], uint16(len(payload)))
	copy(buf[12:], payload)
	return buf
}

func buildOpenSessionRequest(tag uint8, remoteSessionID uint32) []byte {
	req := make([]byte, 32)
	req[0] = tag  // message tag
	req[1] = 0x04 // max privilege (admin)
	binary.LittleEndian.PutUint32(req[4:], remoteSessionID)
	// Auth payload
	req[8] = 0x00  // type
	req[11] = 0x08 // length
	req[12] = 0x01 // RAKP-HMAC-SHA1
	// Integrity payload
	req[16] = 0x01
	req[19] = 0x08
	req[20] = 0x01 // HMAC-SHA1-96
	// Confidentiality payload
	req[24] = 0x02
	req[27] = 0x08
	req[28] = 0x01 // AES-CBC-128
	return req
}

func buildRAKPMessage1(tag uint8, managedSessionID uint32, user string) []byte {
	buf := make([]byte, 48)
	buf[0] = tag
	binary.LittleEndian.PutUint32(buf[4:], managedSessionID)
	// Random number at bytes 8-23
	for i := 8; i < 24; i++ {
		buf[i] = byte(i)
	}
	buf[24] = 0x04 // privilege level (admin)
	buf[27] = byte(len(user))
	copy(buf[28:], []byte(user))
	return buf
}

func buildRAKP3AuthBuf(managedSystemRandom []byte, remoteConsoleSessionID uint32, privilegeLevel uint8, userNameLength uint8, userName []byte) []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, managedSystemRandom...)
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, remoteConsoleSessionID)
	buf = append(buf, b...)
	buf = append(buf, privilegeLevel)
	buf = append(buf, userNameLength)
	buf = append(buf, userName...)
	return buf
}

func buildRAKPMessage3(tag uint8, managedSessionID uint32, authCode []byte) []byte {
	buf := make([]byte, 28)
	buf[0] = tag
	buf[1] = 0x00 // status
	binary.LittleEndian.PutUint32(buf[4:], managedSessionID)
	copy(buf[8:], authCode[:20])
	return buf
}

// Mock machine for IPMI tests
type ipmiMockMachine struct {
	powerState   machine.PowerState
	calls        []string
	bootOverride machine.BootOverride
}

func newIPMIMockMachine(state machine.PowerState) *ipmiMockMachine {
	return &ipmiMockMachine{
		powerState: state,
		bootOverride: machine.BootOverride{
			Enabled: "Disabled",
			Target:  "None",
			Mode:    "UEFI",
		},
	}
}

func (m *ipmiMockMachine) GetPowerState() (machine.PowerState, error) {
	return m.powerState, nil
}
func (m *ipmiMockMachine) Reset(resetType string) error {
	m.calls = append(m.calls, resetType)
	if resetType == "ForceOff" || resetType == "GracefulShutdown" {
		m.powerState = machine.PowerOff
	} else if resetType == "On" {
		m.powerState = machine.PowerOn
	}
	return nil
}
func (m *ipmiMockMachine) GetBootOverride() machine.BootOverride {
	return m.bootOverride
}
func (m *ipmiMockMachine) SetBootOverride(override machine.BootOverride) error {
	m.bootOverride = override
	return nil
}
