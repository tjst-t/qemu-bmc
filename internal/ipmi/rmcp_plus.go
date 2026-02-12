package ipmi

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
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

// RAKPMessage1 from client
type RAKPMessage1 struct {
	MessageTag             uint8
	Reserved               [3]byte
	ManagedSystemSessionID uint32
	RemoteConsoleRandom    [16]byte
	PrivilegeLevel         uint8
	Reserved2              [2]byte
	UserNameLength         uint8
	UserName               [20]byte
}

// RAKPMessage3 from client
type RAKPMessage3 struct {
	MessageTag             uint8
	StatusCode             uint8
	Reserved               [2]byte
	ManagedSystemSessionID uint32
	KeyExchangeAuthCode    [20]byte // HMAC-SHA1
}

// HandleRMCPPlusMessage processes an RMCP+ message and returns a response
func HandleRMCPPlusMessage(data []byte, sessionMgr *SessionManager, user, pass string, machine MachineInterface) ([]byte, error) {
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
		return handleRAKPMessage1(buf.Bytes(), header, sessionMgr, user, pass)
	case PayloadTypeRAKPMessage3:
		return handleRAKPMessage3(buf.Bytes(), header, sessionMgr, pass)
	case PayloadTypeIPMI:
		return handleEncryptedIPMI(data, header, sessionMgr, machine)
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

	session, err := sessionMgr.CreateSession(req.RemoteConsoleSessionID)
	if err != nil {
		return nil, err
	}

	// Build response
	resp := new(bytes.Buffer)
	binary.Write(resp, binary.LittleEndian, req.MessageTag)
	binary.Write(resp, binary.LittleEndian, uint8(0x00)) // status: success
	binary.Write(resp, binary.LittleEndian, uint8(0x04)) // max privilege: admin
	binary.Write(resp, binary.LittleEndian, uint8(0x00)) // reserved
	binary.Write(resp, binary.LittleEndian, req.RemoteConsoleSessionID)
	binary.Write(resp, binary.LittleEndian, session.ManagedSystemSessionID)

	// Auth algorithm payload
	binary.Write(resp, binary.LittleEndian, uint8(0x00)) // type
	binary.Write(resp, binary.LittleEndian, [2]byte{})   // reserved
	binary.Write(resp, binary.LittleEndian, uint8(0x08)) // length
	binary.Write(resp, binary.LittleEndian, req.AuthPayloadAlgorithm)
	binary.Write(resp, binary.LittleEndian, [3]byte{}) // reserved

	// Integrity algorithm payload
	binary.Write(resp, binary.LittleEndian, uint8(0x01))
	binary.Write(resp, binary.LittleEndian, [2]byte{})
	binary.Write(resp, binary.LittleEndian, uint8(0x08))
	binary.Write(resp, binary.LittleEndian, req.IntegrityPayloadAlgorithm)
	binary.Write(resp, binary.LittleEndian, [3]byte{})

	// Confidentiality algorithm payload
	binary.Write(resp, binary.LittleEndian, uint8(0x02))
	binary.Write(resp, binary.LittleEndian, [2]byte{})
	binary.Write(resp, binary.LittleEndian, uint8(0x08))
	binary.Write(resp, binary.LittleEndian, req.ConfPayloadAlgorithm)
	binary.Write(resp, binary.LittleEndian, [3]byte{})

	return wrapRMCPPlusResponse(PayloadTypeOpenSessionResponse, 0, 0, resp.Bytes()), nil
}

func handleRAKPMessage1(payload []byte, header *RMCPPlusSessionHeader, sessionMgr *SessionManager, user, pass string) ([]byte, error) {
	var req RAKPMessage1
	buf := bytes.NewBuffer(payload)
	if err := binary.Read(buf, binary.LittleEndian, &req); err != nil {
		return nil, fmt.Errorf("parsing RAKP message 1: %w", err)
	}

	session, ok := sessionMgr.GetSession(req.ManagedSystemSessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: 0x%08x", req.ManagedSystemSessionID)
	}

	// Store values in session
	session.RemoteConsoleRandomNumber = req.RemoteConsoleRandom
	session.RequestedPrivilegeLevel = req.PrivilegeLevel
	session.UserNameLength = req.UserNameLength
	session.UserName = make([]byte, req.UserNameLength)
	copy(session.UserName, req.UserName[:req.UserNameLength])

	// Validate username
	if string(session.UserName) != user {
		// Return error status
		resp := new(bytes.Buffer)
		binary.Write(resp, binary.LittleEndian, req.MessageTag)
		binary.Write(resp, binary.LittleEndian, uint8(0x0D)) // invalid username
		binary.Write(resp, binary.LittleEndian, [2]byte{})
		binary.Write(resp, binary.LittleEndian, session.RemoteConsoleSessionID)
		return wrapRMCPPlusResponse(PayloadTypeRAKPMessage2, 0, 0, resp.Bytes()), nil
	}

	// Build RAKP Message 2 auth code: HMAC-SHA1(password, data)
	authBuf := new(bytes.Buffer)
	binary.Write(authBuf, binary.LittleEndian, session.RemoteConsoleSessionID)
	binary.Write(authBuf, binary.LittleEndian, session.ManagedSystemSessionID)
	authBuf.Write(session.RemoteConsoleRandomNumber[:])
	authBuf.Write(session.ManagedSystemRandomNumber[:])
	authBuf.Write(session.ManagedSystemGUID[:])
	binary.Write(authBuf, binary.LittleEndian, session.RequestedPrivilegeLevel)
	binary.Write(authBuf, binary.LittleEndian, session.UserNameLength)
	authBuf.Write(session.UserName)

	mac := hmac.New(sha1.New, []byte(pass))
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
	resp.Write(authCode[:20])

	return wrapRMCPPlusResponse(PayloadTypeRAKPMessage2, 0, 0, resp.Bytes()), nil
}

func handleRAKPMessage3(payload []byte, header *RMCPPlusSessionHeader, sessionMgr *SessionManager, pass string) ([]byte, error) {
	var req RAKPMessage3
	buf := bytes.NewBuffer(payload)
	if err := binary.Read(buf, binary.LittleEndian, &req); err != nil {
		return nil, fmt.Errorf("parsing RAKP message 3: %w", err)
	}

	session, ok := sessionMgr.GetSession(req.ManagedSystemSessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: 0x%08x", req.ManagedSystemSessionID)
	}

	// Verify client's auth code
	verifyBuf := new(bytes.Buffer)
	verifyBuf.Write(session.ManagedSystemRandomNumber[:])
	binary.Write(verifyBuf, binary.LittleEndian, session.RemoteConsoleSessionID)
	binary.Write(verifyBuf, binary.LittleEndian, session.RequestedPrivilegeLevel)
	binary.Write(verifyBuf, binary.LittleEndian, session.UserNameLength)
	verifyBuf.Write(session.UserName)

	mac := hmac.New(sha1.New, []byte(pass))
	mac.Write(verifyBuf.Bytes())
	expectedAuthCode := mac.Sum(nil)

	if !hmac.Equal(req.KeyExchangeAuthCode[:], expectedAuthCode) {
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

	sikMac := hmac.New(sha1.New, []byte(pass))
	sikMac.Write(sikBuf.Bytes())
	session.SessionIntegrityKey = sikMac.Sum(nil)

	// Derive K1 (Integrity Key) = HMAC-SHA1(SIK, constant1)
	k1Const := make([]byte, 20)
	for i := range k1Const {
		k1Const[i] = 0x01
	}
	k1Mac := hmac.New(sha1.New, session.SessionIntegrityKey)
	k1Mac.Write(k1Const)
	session.IntegrityKey = k1Mac.Sum(nil)

	// Derive K2 (Confidentiality Key) = HMAC-SHA1(SIK, constant2)
	k2Const := make([]byte, 20)
	for i := range k2Const {
		k2Const[i] = 0x02
	}
	k2Mac := hmac.New(sha1.New, session.SessionIntegrityKey)
	k2Mac.Write(k2Const)
	session.ConfidentialityKey = k2Mac.Sum(nil)

	session.Authenticated = true

	// Build RAKP Message 4 with integrity check value
	icvBuf := new(bytes.Buffer)
	icvBuf.Write(session.RemoteConsoleRandomNumber[:])
	binary.Write(icvBuf, binary.LittleEndian, session.ManagedSystemSessionID)
	icvBuf.Write(session.ManagedSystemGUID[:])

	icvMac := hmac.New(sha1.New, session.SessionIntegrityKey)
	icvMac.Write(icvBuf.Bytes())
	icv := icvMac.Sum(nil)[:12] // HMAC-SHA1-96

	resp := new(bytes.Buffer)
	binary.Write(resp, binary.LittleEndian, req.MessageTag)
	binary.Write(resp, binary.LittleEndian, uint8(0x00)) // success
	binary.Write(resp, binary.LittleEndian, [2]byte{})
	binary.Write(resp, binary.LittleEndian, session.RemoteConsoleSessionID)
	resp.Write(icv)

	return wrapRMCPPlusResponse(PayloadTypeRAKPMessage4, 0, 0, resp.Bytes()), nil
}

func handleEncryptedIPMI(data []byte, header *RMCPPlusSessionHeader, sessionMgr *SessionManager, machine MachineInterface) ([]byte, error) {
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
		return nil, fmt.Errorf("decrypted IPMI data too short")
	}
	msg, err := ParseIPMIMessageBytes(ipmiData)
	if err != nil {
		return nil, err
	}

	// Route to handler
	responseCode, responseData := handleIPMICommand(msg, machine)

	// Build response IPMI message
	respMsg := buildIPMIResponseMessage(msg.GetNetFn()|0x01, msg.Command, responseCode, responseData)

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
		// Add integrity trailer and auth code
		trailer := []byte{0xff, 0xff, 0x02, 0x07}
		respBuf = append(respBuf, trailer...)

		// HMAC-SHA1-96 over everything from AuthType to end of trailer
		mac := hmac.New(sha1.New, session.IntegrityKey)
		mac.Write(respBuf)
		authCode := mac.Sum(nil)[:12]
		respBuf = append(respBuf, authCode...)
	}

	return respBuf, nil
}

func buildRMCPPlusEncryptedResponse(session *Session, payloadType uint8, payload []byte) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, uint8(AuthTypeRMCPPlus))
	binary.Write(buf, binary.LittleEndian, payloadType)
	binary.Write(buf, binary.LittleEndian, session.RemoteConsoleSessionID)
	binary.Write(buf, binary.LittleEndian, uint32(0)) // sequence
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
	padding[padSize-1] = byte(padSize - 1) // last byte is pad length
	return append(data, padding...)
}

func unpadPayload(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	padLen := int(data[len(data)-1])
	if padLen >= len(data) {
		return nil, fmt.Errorf("invalid padding length: %d", padLen)
	}
	return data[:len(data)-padLen-1], nil
}

// handleIPMICommand routes an IPMI message to the appropriate handler
func handleIPMICommand(msg *IPMIMessage, machine MachineInterface) (CompletionCode, []byte) {
	netFn := msg.GetNetFn()

	switch netFn {
	case NetFnApp:
		return handleAppCommand(msg, machine)
	case NetFnChassis:
		return handleChassisCommand(msg, machine)
	default:
		return CompletionCodeInvalidCommand, nil
	}
}
