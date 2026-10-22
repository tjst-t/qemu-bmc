package ipmi

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"

	"github.com/tjst-t/qemu-bmc/internal/bmc"
)

// RMCPPlusSessionHeader is the RMCP+ session header
type RMCPPlusSessionHeader struct {
	AuthType        uint8  // Always 0x06
	PayloadType     uint8  // encrypted(1b) + authenticated(1b) + type(6b)
	SessionID       uint32
	SessionSequence uint32
	PayloadLength   uint16
}

// OpenSessionRequest from client
type OpenSessionRequest struct {
	MessageTag                uint8
	MaxPrivilegeLevel         uint8
	Reserved                  [2]byte
	RemoteConsoleSessionID    uint32
	AuthPayloadType           uint8
	AuthReserved              [2]byte
	AuthPayloadLength         uint8
	AuthPayloadAlgorithm      uint8
	AuthReserved2             [3]byte
	IntegrityPayloadType      uint8
	IntegrityReserved         [2]byte
	IntegrityPayloadLength    uint8
	IntegrityPayloadAlgorithm uint8
	IntegrityReserved2        [3]byte
	ConfPayloadType           uint8
	ConfReserved              [2]byte
	ConfPayloadLength         uint8
	ConfPayloadAlgorithm      uint8
	ConfReserved2             [3]byte
}

// RAKPMessage1 from client (parsed manually due to variable-length UserName)
type RAKPMessage1 struct {
	MessageTag             uint8
	Reserved               [3]byte
	ManagedSystemSessionID uint32
	RemoteConsoleRandom    [16]byte
	PrivilegeLevel         uint8
	Reserved2              [2]byte
	UserNameLength         uint8
	UserName               []byte
}

// RAKPMessage3 from client (parsed manually: the auth code is 20 bytes for
// RAKP-HMAC-SHA1 and 32 bytes for RAKP-HMAC-SHA256)
type RAKPMessage3 struct {
	MessageTag             uint8
	StatusCode             uint8
	Reserved               [2]byte
	ManagedSystemSessionID uint32
	KeyExchangeAuthCode    []byte
}

// cipherSuite is a supported combination of RMCP+ algorithms.
type cipherSuite struct {
	ID              uint8
	Auth            uint8
	Integrity       uint8
	Confidentiality uint8
}

// supportedCipherSuites lists the cipher suites accepted by handleOpenSession,
// in the order reported by Get Channel Cipher Suites.
var supportedCipherSuites = []cipherSuite{
	// Suite 3: RAKP-HMAC-SHA1 + HMAC-SHA1-96 + AES-CBC-128
	{3, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96, ConfAlgorithmAESCBC128},
	// Suite 17: RAKP-HMAC-SHA256 + HMAC-SHA256-128 + AES-CBC-128
	{17, AuthAlgorithmHMACSHA256, IntegrityAlgorithmHMACSHA256_128, ConfAlgorithmAESCBC128},
}

// authHash returns the hash used by the RAKP authentication algorithm for the
// RAKP 2/3 auth codes, the RAKP 4 ICV and the SIK/K1/K2 derivation.
func authHash(authAlg uint8) func() hash.Hash {
	if authAlg == AuthAlgorithmHMACSHA256 {
		return sha256.New
	}
	return sha1.New
}

// authCodeLength returns the length of the RAKP 2/3 key exchange auth code.
func authCodeLength(authAlg uint8) int {
	return authHash(authAlg)().Size()
}

// rakp4ICVLength returns the length of the RAKP 4 integrity check value:
// HMAC-SHA1-96 for RAKP-HMAC-SHA1, HMAC-SHA256-128 for RAKP-HMAC-SHA256.
func rakp4ICVLength(authAlg uint8) int {
	if authAlg == AuthAlgorithmHMACSHA256 {
		return 16
	}
	return 12
}

// integrityHash returns the hash and the truncated AuthCode length of the
// packet integrity algorithm (HMAC-SHA1-96 or HMAC-SHA256-128).
func integrityHash(integrityAlg uint8) (func() hash.Hash, int) {
	if integrityAlg == IntegrityAlgorithmHMACSHA256_128 {
		return sha256.New, 16
	}
	return sha1.New, 12
}

// integrityAuthCode computes the AuthCode of an authenticated RMCP+ packet over
// data (AuthType through Next Header) with the session's integrity key K1.
func integrityAuthCode(session *Session, data []byte) []byte {
	h, n := integrityHash(session.IntegrityAlgorithm)
	mac := hmac.New(h, session.IntegrityKey)
	mac.Write(data)
	return mac.Sum(nil)[:n]
}

// HandleRMCPPlusMessage processes an RMCP+ message and returns a response
func HandleRMCPPlusMessage(data []byte, sessionMgr *SessionManager, user, pass string, machine MachineInterface, state *bmc.State) ([]byte, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("RMCP+ message too short")
	}

	buf := bytes.NewBuffer(data)
	header := &RMCPPlusSessionHeader{}
	if err := binary.Read(buf, binary.LittleEndian, header); err != nil {
		return nil, fmt.Errorf("parsing RMCP+ header: %w", err)
	}

	payloadType := header.PayloadType & 0x3F

	switch payloadType {
	case PayloadTypeOpenSessionRequest:
		return handleOpenSession(buf.Bytes(), header, sessionMgr)
	case PayloadTypeRAKPMessage1:
		return handleRAKPMessage1(buf.Bytes(), header, sessionMgr, user, pass, state)
	case PayloadTypeRAKPMessage3:
		return handleRAKPMessage3(buf.Bytes(), header, sessionMgr, pass, state)
	case PayloadTypeIPMI:
		if header.SessionID == 0 {
			return handlePreSessionIPMI(data, header, machine, state)
		}
		return handleEncryptedIPMI(data, header, sessionMgr, machine, state)
	default:
		return nil, fmt.Errorf("unsupported RMCP+ payload type: 0x%02x", payloadType)
	}
}

func handleOpenSession(payload []byte, header *RMCPPlusSessionHeader, sessionMgr *SessionManager) ([]byte, error) {
	var req OpenSessionRequest
	buf := bytes.NewBuffer(payload)
	if err := binary.Read(buf, binary.LittleEndian, &req); err != nil {
		return nil, fmt.Errorf("parsing open session request: %w", err)
	}

	// Validate proposed algorithms against the supported cipher suites (3 and 17).
	// Reject unsupported suites with the appropriate IPMI 2.0 error status code so that
	// strict clients (e.g. FreeIPMI) get a proper error instead of a false success that
	// later causes a session timeout.
	var suite *cipherSuite
	for i := range supportedCipherSuites {
		if supportedCipherSuites[i].Auth == req.AuthPayloadAlgorithm {
			suite = &supportedCipherSuites[i]
			break
		}
	}
	if suite == nil {
		return buildOpenSessionError(req.MessageTag, req.RemoteConsoleSessionID, OpenSessionStatusInvalidAuthAlgorithm), nil
	}
	if req.IntegrityPayloadAlgorithm != suite.Integrity {
		return buildOpenSessionError(req.MessageTag, req.RemoteConsoleSessionID, OpenSessionStatusInvalidIntegrityAlgorithm), nil
	}
	if req.ConfPayloadAlgorithm != suite.Confidentiality {
		return buildOpenSessionError(req.MessageTag, req.RemoteConsoleSessionID, OpenSessionStatusInvalidConfAlgorithm), nil
	}

	session, err := sessionMgr.CreateSession(req.RemoteConsoleSessionID)
	if err != nil {
		return nil, err
	}

	// Store the negotiated algorithms in the session for use in later RAKP steps.
	session.AuthAlgorithm = suite.Auth
	session.IntegrityAlgorithm = suite.Integrity
	session.ConfidentialityAlgorithm = suite.Confidentiality

	// Build response - return BMC's chosen algorithms (not an echo-back of the client's
	// proposal). Responding with the BMC's own values satisfies strict clients like
	// FreeIPMI which validate that the BMC independently selected a supported algorithm.
	resp := new(bytes.Buffer)
	binary.Write(resp, binary.LittleEndian, req.MessageTag)
	binary.Write(resp, binary.LittleEndian, uint8(OpenSessionStatusSuccess))
	binary.Write(resp, binary.LittleEndian, uint8(0x04)) // max privilege: admin
	binary.Write(resp, binary.LittleEndian, uint8(0x00)) // reserved
	binary.Write(resp, binary.LittleEndian, req.RemoteConsoleSessionID)
	binary.Write(resp, binary.LittleEndian, session.ManagedSystemSessionID)

	// Auth algorithm payload
	binary.Write(resp, binary.LittleEndian, uint8(0x00)) // type
	binary.Write(resp, binary.LittleEndian, [2]byte{})   // reserved
	binary.Write(resp, binary.LittleEndian, uint8(0x08)) // length
	binary.Write(resp, binary.LittleEndian, session.AuthAlgorithm)
	binary.Write(resp, binary.LittleEndian, [3]byte{}) // reserved

	// Integrity algorithm payload
	binary.Write(resp, binary.LittleEndian, uint8(0x01))
	binary.Write(resp, binary.LittleEndian, [2]byte{})
	binary.Write(resp, binary.LittleEndian, uint8(0x08))
	binary.Write(resp, binary.LittleEndian, session.IntegrityAlgorithm)
	binary.Write(resp, binary.LittleEndian, [3]byte{})

	// Confidentiality algorithm payload
	binary.Write(resp, binary.LittleEndian, uint8(0x02))
	binary.Write(resp, binary.LittleEndian, [2]byte{})
	binary.Write(resp, binary.LittleEndian, uint8(0x08))
	binary.Write(resp, binary.LittleEndian, session.ConfidentialityAlgorithm)
	binary.Write(resp, binary.LittleEndian, [3]byte{})

	return wrapRMCPPlusResponse(PayloadTypeOpenSessionResponse, 0, 0, resp.Bytes()), nil
}

// buildOpenSessionError builds an RMCP+ Open Session error response.
func buildOpenSessionError(messageTag uint8, remoteConsoleSessionID uint32, statusCode uint8) []byte {
	resp := new(bytes.Buffer)
	binary.Write(resp, binary.LittleEndian, messageTag)
	binary.Write(resp, binary.LittleEndian, statusCode)
	binary.Write(resp, binary.LittleEndian, uint8(0x00)) // reserved
	binary.Write(resp, binary.LittleEndian, uint8(0x00)) // reserved
	binary.Write(resp, binary.LittleEndian, remoteConsoleSessionID)
	return wrapRMCPPlusResponse(PayloadTypeOpenSessionResponse, 0, 0, resp.Bytes())
}

func handleRAKPMessage1(payload []byte, header *RMCPPlusSessionHeader, sessionMgr *SessionManager, user, pass string, state *bmc.State) ([]byte, error) {
	// Parse manually to handle variable-length UserName
	if len(payload) < 28 {
		return nil, fmt.Errorf("RAKP message 1 too short: %d bytes", len(payload))
	}

	var req RAKPMessage1
	req.MessageTag = payload[0]
	req.ManagedSystemSessionID = binary.LittleEndian.Uint32(payload[4:8])
	copy(req.RemoteConsoleRandom[:], payload[8:24])
	req.PrivilegeLevel = payload[24]
	req.UserNameLength = payload[27]

	if len(payload) < 28+int(req.UserNameLength) {
		return nil, fmt.Errorf("RAKP message 1 too short for username: need %d, have %d", 28+int(req.UserNameLength), len(payload))
	}
	req.UserName = make([]byte, req.UserNameLength)
	copy(req.UserName, payload[28:28+int(req.UserNameLength)])

	session, ok := sessionMgr.GetSession(req.ManagedSystemSessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: 0x%08x", req.ManagedSystemSessionID)
	}

	// Store values in session
	session.RemoteConsoleRandomNumber = req.RemoteConsoleRandom
	session.RequestedPrivilegeLevel = req.PrivilegeLevel
	session.UserNameLength = req.UserNameLength
	session.UserName = make([]byte, req.UserNameLength)
	copy(session.UserName, req.UserName)

	// Try BMC state first, fall back to hardcoded user
	var authPass string
	if state != nil {
		userID, found := state.LookupUserByName(string(session.UserName))
		if found {
			pw, err := state.GetUserPassword(userID)
			if err == nil {
				authPass = pw
			}
		}
	}
	if authPass == "" {
		// Fall back to hardcoded credentials
		if string(session.UserName) != user {
			// Return error status
			resp := new(bytes.Buffer)
			binary.Write(resp, binary.LittleEndian, req.MessageTag)
			binary.Write(resp, binary.LittleEndian, uint8(0x0D)) // invalid username
			binary.Write(resp, binary.LittleEndian, [2]byte{})
			binary.Write(resp, binary.LittleEndian, session.RemoteConsoleSessionID)
			return wrapRMCPPlusResponse(PayloadTypeRAKPMessage2, 0, 0, resp.Bytes()), nil
		}
		authPass = pass
	}

	// Build RAKP Message 2 auth code: HMAC-SHA1/SHA256(password, data)
	authBuf := new(bytes.Buffer)
	binary.Write(authBuf, binary.LittleEndian, session.RemoteConsoleSessionID)
	binary.Write(authBuf, binary.LittleEndian, session.ManagedSystemSessionID)
	authBuf.Write(session.RemoteConsoleRandomNumber[:])
	authBuf.Write(session.ManagedSystemRandomNumber[:])
	authBuf.Write(session.ManagedSystemGUID[:])
	binary.Write(authBuf, binary.LittleEndian, session.RequestedPrivilegeLevel)
	binary.Write(authBuf, binary.LittleEndian, session.UserNameLength)
	authBuf.Write(session.UserName)

	mac := hmac.New(authHash(session.AuthAlgorithm), []byte(authPass))
	mac.Write(authBuf.Bytes())
	authCode := mac.Sum(nil)

	// Build response
	resp := new(bytes.Buffer)
	binary.Write(resp, binary.LittleEndian, req.MessageTag)
	binary.Write(resp, binary.LittleEndian, uint8(0x00)) // success
	binary.Write(resp, binary.LittleEndian, [2]byte{})
	binary.Write(resp, binary.LittleEndian, session.RemoteConsoleSessionID)
	resp.Write(session.ManagedSystemRandomNumber[:])
	resp.Write(session.ManagedSystemGUID[:])
	resp.Write(authCode)

	return wrapRMCPPlusResponse(PayloadTypeRAKPMessage2, 0, 0, resp.Bytes()), nil
}

func handleRAKPMessage3(payload []byte, header *RMCPPlusSessionHeader, sessionMgr *SessionManager, pass string, state *bmc.State) ([]byte, error) {
	if len(payload) < 8 {
		return nil, fmt.Errorf("RAKP message 3 too short: %d bytes", len(payload))
	}
	var req RAKPMessage3
	req.MessageTag = payload[0]
	req.StatusCode = payload[1]
	req.ManagedSystemSessionID = binary.LittleEndian.Uint32(payload[4:8])

	session, ok := sessionMgr.GetSession(req.ManagedSystemSessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: 0x%08x", req.ManagedSystemSessionID)
	}

	codeLen := authCodeLength(session.AuthAlgorithm)
	if len(payload) < 8+codeLen {
		return nil, fmt.Errorf("RAKP message 3 too short for auth code: need %d, have %d", 8+codeLen, len(payload))
	}
	req.KeyExchangeAuthCode = payload[8 : 8+codeLen]

	// Resolve password: try BMC state first, fall back to hardcoded
	authPass := pass
	if state != nil {
		userID, found := state.LookupUserByName(string(session.UserName))
		if found {
			if pw, err := state.GetUserPassword(userID); err == nil && pw != "" {
				authPass = pw
			}
		}
	}

	// Verify client's auth code
	verifyBuf := new(bytes.Buffer)
	verifyBuf.Write(session.ManagedSystemRandomNumber[:])
	binary.Write(verifyBuf, binary.LittleEndian, session.RemoteConsoleSessionID)
	binary.Write(verifyBuf, binary.LittleEndian, session.RequestedPrivilegeLevel)
	binary.Write(verifyBuf, binary.LittleEndian, session.UserNameLength)
	verifyBuf.Write(session.UserName)

	h := authHash(session.AuthAlgorithm)
	mac := hmac.New(h, []byte(authPass))
	mac.Write(verifyBuf.Bytes())
	expectedAuthCode := mac.Sum(nil)

	if !hmac.Equal(req.KeyExchangeAuthCode, expectedAuthCode) {
		resp := new(bytes.Buffer)
		binary.Write(resp, binary.LittleEndian, req.MessageTag)
		binary.Write(resp, binary.LittleEndian, uint8(0x0F)) // invalid integrity check
		binary.Write(resp, binary.LittleEndian, [2]byte{})
		binary.Write(resp, binary.LittleEndian, session.RemoteConsoleSessionID)
		return wrapRMCPPlusResponse(PayloadTypeRAKPMessage4, 0, 0, resp.Bytes()), nil
	}

	// Derive Session Integrity Key (SIK)
	sikBuf := new(bytes.Buffer)
	sikBuf.Write(session.RemoteConsoleRandomNumber[:])
	sikBuf.Write(session.ManagedSystemRandomNumber[:])
	binary.Write(sikBuf, binary.LittleEndian, session.RequestedPrivilegeLevel)
	binary.Write(sikBuf, binary.LittleEndian, session.UserNameLength)
	sikBuf.Write(session.UserName)

	sikMac := hmac.New(h, []byte(authPass))
	sikMac.Write(sikBuf.Bytes())
	session.SessionIntegrityKey = sikMac.Sum(nil)

	// Derive K1 (Integrity Key) = HMAC(SIK, constant1). The constants are
	// 20 bytes for SHA256 as well (IPMI 2.0 errata, same as ipmitool/FreeIPMI).
	k1Const := make([]byte, 20)
	for i := range k1Const {
		k1Const[i] = 0x01
	}
	k1Mac := hmac.New(h, session.SessionIntegrityKey)
	k1Mac.Write(k1Const)
	session.IntegrityKey = k1Mac.Sum(nil)

	// Derive K2 (Confidentiality Key) = HMAC(SIK, constant2)
	k2Const := make([]byte, 20)
	for i := range k2Const {
		k2Const[i] = 0x02
	}
	k2Mac := hmac.New(h, session.SessionIntegrityKey)
	k2Mac.Write(k2Const)
	session.ConfidentialityKey = k2Mac.Sum(nil)

	session.Authenticated = true

	// Build RAKP Message 4 with integrity check value
	icvBuf := new(bytes.Buffer)
	icvBuf.Write(session.RemoteConsoleRandomNumber[:])
	binary.Write(icvBuf, binary.LittleEndian, session.ManagedSystemSessionID)
	icvBuf.Write(session.ManagedSystemGUID[:])

	icvMac := hmac.New(h, session.SessionIntegrityKey)
	icvMac.Write(icvBuf.Bytes())
	icv := icvMac.Sum(nil)[:rakp4ICVLength(session.AuthAlgorithm)] // HMAC-SHA1-96 / HMAC-SHA256-128

	resp := new(bytes.Buffer)
	binary.Write(resp, binary.LittleEndian, req.MessageTag)
	binary.Write(resp, binary.LittleEndian, uint8(0x00)) // success
	binary.Write(resp, binary.LittleEndian, [2]byte{})
	binary.Write(resp, binary.LittleEndian, session.RemoteConsoleSessionID)
	resp.Write(icv)

	return wrapRMCPPlusResponse(PayloadTypeRAKPMessage4, 0, 0, resp.Bytes()), nil
}

func handlePreSessionIPMI(data []byte, header *RMCPPlusSessionHeader, machine MachineInterface, state *bmc.State) ([]byte, error) {
	// Pre-session IPMI messages (e.g., Get Channel Auth Capabilities) sent via RMCP+
	// with session ID 0, no encryption, no authentication
	payloadStart := 12
	payloadEnd := payloadStart + int(header.PayloadLength)
	if payloadEnd > len(data) {
		return nil, fmt.Errorf("payload length exceeds data")
	}
	ipmiData := data[payloadStart:payloadEnd]

	if len(ipmiData) < 7 {
		return nil, fmt.Errorf("pre-session IPMI data too short")
	}

	msg, err := ParseIPMIMessageBytes(ipmiData)
	if err != nil {
		return nil, err
	}

	responseCode, responseData := handleIPMICommand(msg, machine, state)
	respMsg := buildIPMIResponseMessageWithSeq(msg.GetNetFn()|0x01, msg.Command, responseCode, responseData, msg.SourceLun)

	return wrapRMCPPlusResponse(PayloadTypeIPMI, 0, 0, respMsg), nil
}

func handleEncryptedIPMI(data []byte, header *RMCPPlusSessionHeader, sessionMgr *SessionManager, machine MachineInterface, state *bmc.State) ([]byte, error) {
	session, ok := sessionMgr.GetSession(header.SessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: 0x%08x", header.SessionID)
	}

	isEncrypted := (header.PayloadType & 0x80) != 0
	isAuthenticated := (header.PayloadType & 0x40) != 0

	// Extract payload (after the 12-byte header)
	payloadStart := 12
	payloadEnd := payloadStart + int(header.PayloadLength)
	if payloadEnd > len(data) {
		return nil, fmt.Errorf("payload length exceeds data")
	}
	payload := data[payloadStart:payloadEnd]

	// Verify integrity if authenticated
	if isAuthenticated {
		// The integrity data starts after the payload
		integrityStart := payloadEnd
		// Need at least: pad(variable) + padLen(1) + nextHeader(1) + authCode(12 or 16)
		_, authCodeLen := integrityHash(session.IntegrityAlgorithm)
		if len(data) < integrityStart+2+authCodeLen {
			return nil, fmt.Errorf("RMCP+ data too short for integrity: %d bytes after payload", len(data)-integrityStart)
		}
	}

	// Decrypt if needed
	var ipmiData []byte
	if isEncrypted {
		var err error
		ipmiData, err = decryptAESCBC(session.ConfidentialityKey, payload)
		if err != nil {
			return nil, fmt.Errorf("decrypting payload: %w", err)
		}
	} else {
		ipmiData = payload
	}

	// Parse and handle the IPMI message
	if len(ipmiData) < 7 {
		return nil, fmt.Errorf("decrypted IPMI data too short: %d", len(ipmiData))
	}
	msg, err := ParseIPMIMessageBytes(ipmiData)
	if err != nil {
		return nil, err
	}

	// Route to handler
	responseCode, responseData := handleIPMICommand(msg, machine, state)

	// Build response IPMI message (echo request's sequence number)
	respMsg := buildIPMIResponseMessageWithSeq(msg.GetNetFn()|0x01, msg.Command, responseCode, responseData, msg.SourceLun)

	// Encrypt response
	var respPayload []byte
	if isEncrypted {
		respPayload, err = encryptAESCBC(session.ConfidentialityKey, respMsg)
		if err != nil {
			return nil, err
		}
	} else {
		respPayload = respMsg
	}

	// Build RMCP+ response with optional integrity
	respBuf := buildRMCPPlusEncryptedResponse(session, header.PayloadType, respPayload)

	if isAuthenticated {
		// Calculate integrity pad to align (header+payload+pad+padLen+nextHeader) to 4 bytes
		totalBeforePad := len(respBuf) // header + encrypted payload
		padNeeded := (4 - ((totalBeforePad + 1 + 1) % 4)) % 4
		for i := 0; i < padNeeded; i++ {
			respBuf = append(respBuf, 0xFF)
		}
		respBuf = append(respBuf, byte(padNeeded)) // Pad Length
		respBuf = append(respBuf, 0x07)             // Next Header

		// HMAC-SHA1-96 / HMAC-SHA256-128 over everything from AuthType to end of Next Header
		respBuf = append(respBuf, integrityAuthCode(session, respBuf)...)
	}

	return respBuf, nil
}

func buildRMCPPlusEncryptedResponse(session *Session, payloadType uint8, payload []byte) []byte {
	session.OutboundSequenceNumber++
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, uint8(AuthTypeRMCPPlus))
	binary.Write(buf, binary.LittleEndian, payloadType)
	binary.Write(buf, binary.LittleEndian, session.RemoteConsoleSessionID)
	binary.Write(buf, binary.LittleEndian, session.OutboundSequenceNumber)
	binary.Write(buf, binary.LittleEndian, uint16(len(payload)))
	buf.Write(payload)
	return buf.Bytes()
}

func wrapRMCPPlusResponse(payloadType uint8, sessionID uint32, sequence uint32, payload []byte) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, uint8(AuthTypeRMCPPlus))
	binary.Write(buf, binary.LittleEndian, payloadType)
	binary.Write(buf, binary.LittleEndian, sessionID)
	binary.Write(buf, binary.LittleEndian, sequence)
	binary.Write(buf, binary.LittleEndian, uint16(len(payload)))
	buf.Write(payload)
	return buf.Bytes()
}

// AES-CBC-128 encryption
func encryptAESCBC(key []byte, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:16])
	if err != nil {
		return nil, err
	}

	// PKCS7-like padding
	padded := padPayload(plaintext)

	// Generate random IV
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}

	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, padded)

	// IV + ciphertext
	return append(iv, ciphertext...), nil
}

// AES-CBC-128 decryption
func decryptAESCBC(key []byte, data []byte) ([]byte, error) {
	if len(data) < aes.BlockSize {
		return nil, fmt.Errorf("encrypted data too short")
	}

	block, err := aes.NewCipher(key[:16])
	if err != nil {
		return nil, err
	}

	iv := data[:aes.BlockSize]
	ciphertext := data[aes.BlockSize:]

	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not aligned to block size")
	}

	plaintext := make([]byte, len(ciphertext))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(plaintext, ciphertext)

	// Remove padding
	return unpadPayload(plaintext)
}

func padPayload(data []byte) []byte {
	padSize := aes.BlockSize - (len(data) % aes.BlockSize)
	padding := make([]byte, padSize)
	for i := 0; i < padSize; i++ {
		padding[i] = byte(i + 1)
	}
	// Per IPMI 2.0 spec §13.28.3: the last byte is CPL (Confidentiality Pad Length),
	// which is the count of CPad bytes ONLY (not counting CPL itself).
	// CPL = padSize - 1 (since padSize = CPad bytes + 1 CPL byte).
	padding[padSize-1] = byte(padSize - 1)
	return append(data, padding...)
}

func unpadPayload(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	// Per IPMI 2.0 spec §13.28.3: the last byte is CPL (Confidentiality Pad Length),
	// the count of CPad bytes preceding it.  Total trailer = CPad + CPL = CPL+1 bytes.
	padLen := int(data[len(data)-1]) // CPL = count of CPad bytes only
	if padLen >= len(data) {
		return nil, fmt.Errorf("invalid padding length: %d", padLen)
	}
	return data[:len(data)-padLen-1], nil // strip CPad (padLen bytes) + CPL (1 byte)
}

// handleIPMICommand routes an IPMI message to the appropriate handler
func handleIPMICommand(msg *IPMIMessage, machine MachineInterface, state *bmc.State) (CompletionCode, []byte) {
	netFn := msg.GetNetFn()

	switch netFn {
	case NetFnApp:
		return handleAppCommand(msg, machine, state)
	case NetFnChassis:
		return handleChassisCommand(msg, machine)
	case NetFnTransport:
		return handleTransportCommand(msg, state)
	case NetFnStorage:
		return handleStorageCommand(msg, state)
	case NetFnSensorEvent:
		return handleSensorEventCommand(msg)
	default:
		return CompletionCodeInvalidCommand, nil
	}
}
