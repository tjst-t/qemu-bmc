package ipmi

import (
	"encoding/binary"
	"fmt"
)

// solConfigRevision is the parameter revision of Get SOL Configuration
// Parameters (1.1).
const solConfigRevision = 0x11

// lanChannel is the channel number of the LAN interface (Get Channel Auth
// Capabilities reports channel 1).
const lanChannel = 0x01

// validLANChannel reports whether c (bits 3:0) is the LAN channel or the
// current channel (0x0E).
func validLANChannel(c uint8) bool {
	c &= 0x0F
	return c == lanChannel || c == 0x0E
}

// handleServerCommand handles the commands that need the server: payloads
// (SOL), SOL configuration and Close Session in an RMCP+ session. ok is
// false for the other commands.
func (s *Server) handleServerCommand(msg *IPMIMessage, session *Session) (code CompletionCode, resp []byte, ok bool) {
	switch msg.GetNetFn() {
	case NetFnApp:
		switch msg.Command {
		case CmdCloseSession:
			if session == nil {
				return 0, nil, false
			}
			code, resp = s.handleCloseSession(msg.Data, session)
		case CmdActivatePayload:
			code, resp = s.handleActivatePayload(msg.Data, session)
		case CmdDeactivatePayload:
			code, resp = s.handleDeactivatePayload(msg.Data, session)
		case CmdGetPayloadActivationStatus:
			code, resp = s.handleGetPayloadActivationStatus(msg.Data)
		case CmdGetPayloadInstanceInfo:
			code, resp = s.handleGetPayloadInstanceInfo(msg.Data)
		case CmdSetUserPayloadAccess:
			code, resp = s.handleSetUserPayloadAccess(msg.Data)
		case CmdGetUserPayloadAccess:
			code, resp = s.handleGetUserPayloadAccess(msg.Data)
		case CmdGetChannelPayloadSupport:
			code, resp = s.handleGetChannelPayloadSupport(msg.Data)
		case CmdGetChannelPayloadVersion:
			code, resp = s.handleGetChannelPayloadVersion(msg.Data)
		default:
			return 0, nil, false
		}
		return code, resp, true
	case NetFnTransport:
		switch msg.Command {
		case CmdGetSOLConfigParams:
			code, resp = s.sol.getConfig(msg.Data)
		case CmdSetSOLConfigParams:
			code, resp = s.sol.setConfig(msg.Data)
		default:
			return 0, nil, false
		}
		return code, resp, true
	}
	return 0, nil, false
}

// closeSession removes session; SOL active in it is deactivated.
func (s *Server) closeSession(session *Session, reason string) {
	s.sessionMgr.RemoveSession(session.ManagedSystemSessionID)
	s.sol.sessionClosed(session, reason)
}

// handleCloseSession closes the session given in the request (the
// requesting one if 0).
func (s *Server) handleCloseSession(data []byte, session *Session) (CompletionCode, []byte) {
	if len(data) < 4 {
		return CompletionCodeReqDataLenInvalid, nil
	}
	target := session
	if id := binary.LittleEndian.Uint32(data[0:4]); id != 0 && id != session.ManagedSystemSessionID {
		other, ok := s.sessionMgr.GetSession(id)
		if !ok {
			return completionCodeInvalidSessionID, nil
		}
		target = other
	}
	s.closeSession(target, "session closed")
	return CompletionCodeOK, nil
}

// handleActivatePayload activates SOL (instance 1) in the requesting
// session. Request: payload type, instance, 4 bytes aux data (bit 7:
// encryption, bit 6: authentication).
func (s *Server) handleActivatePayload(data []byte, session *Session) (CompletionCode, []byte) {
	if len(data) < 6 {
		return CompletionCodeReqDataLenInvalid, nil
	}
	if data[0]&0x3F != PayloadTypeSOL || data[1]&0x0F != 1 {
		return CompletionCodeInvalidField, nil
	}
	if session == nil {
		// Payloads only exist in RMCP+ sessions
		return CompletionCodeNotInPresentState, nil
	}
	var userID uint8
	if s.bmcState != nil {
		userID, _ = s.bmcState.LookupUserByName(string(session.UserName))
	}
	if code := s.sol.activate(session, userID, data[2]); code != CompletionCodeOK {
		return code, nil
	}

	resp := make([]byte, 12)
	// 0:4 aux data (none)
	binary.LittleEndian.PutUint16(resp[4:6], solMaxPayload) // inbound payload size
	binary.LittleEndian.PutUint16(resp[6:8], solMaxPayload) // outbound payload size
	binary.LittleEndian.PutUint16(resp[8:10], s.sol.udpPort())
	binary.LittleEndian.PutUint16(resp[10:12], 0xFFFF) // no VLAN
	return CompletionCodeOK, resp
}

// handleDeactivatePayload deactivates SOL. Deactivated from another
// session, the console is told and the session SOL was active in is closed
// (it would otherwise go on as if nothing happened).
func (s *Server) handleDeactivatePayload(data []byte, session *Session) (CompletionCode, []byte) {
	if len(data) < 2 {
		return CompletionCodeReqDataLenInvalid, nil
	}
	if data[0]&0x3F != PayloadTypeSOL || data[1]&0x0F != 1 {
		return CompletionCodeInvalidField, nil
	}
	holder := s.sol.activeSession()
	if holder == nil {
		return completionCodePayloadInactive, nil
	}
	if holder == session {
		s.sol.deactivate(nil, false, "Deactivate Payload")
		return CompletionCodeOK, nil
	}
	reason := "Deactivate Payload from another session"
	if session != nil {
		reason = fmt.Sprintf("Deactivate Payload from session 0x%08x", session.ManagedSystemSessionID)
	}
	s.sol.deactivate(nil, true, reason)
	s.closeSession(holder, reason)
	return CompletionCodeOK, nil
}

func (s *Server) handleGetPayloadActivationStatus(data []byte) (CompletionCode, []byte) {
	if len(data) < 1 {
		return CompletionCodeReqDataLenInvalid, nil
	}
	if data[0]&0x3F != PayloadTypeSOL {
		return CompletionCodeInvalidField, nil
	}
	if !s.sol.available() {
		return CompletionCodeOK, []byte{0x00, 0x00, 0x00}
	}
	var active uint8
	if s.sol.activeSession() != nil {
		active = 0x01 // instance 1
	}
	return CompletionCodeOK, []byte{0x01, active, 0x00}
}

// handleGetPayloadInstanceInfo returns the session SOL is active in (0 if
// none) and, for SOL, the serial port number (1).
func (s *Server) handleGetPayloadInstanceInfo(data []byte) (CompletionCode, []byte) {
	if len(data) < 2 {
		return CompletionCodeReqDataLenInvalid, nil
	}
	if data[0]&0x3F != PayloadTypeSOL || data[1]&0x0F != 1 {
		return CompletionCodeInvalidField, nil
	}
	resp := make([]byte, 12)
	if holder := s.sol.activeSession(); holder != nil {
		binary.LittleEndian.PutUint32(resp[0:4], holder.ManagedSystemSessionID)
		resp[4] = 0x01
	}
	return CompletionCodeOK, resp
}

// payloadAccessUser parses the channel and user ID of Set/Get User Payload
// Access.
func (s *Server) payloadAccessUser(data []byte, minLen int) (uint8, CompletionCode) {
	if len(data) < minLen {
		return 0, CompletionCodeReqDataLenInvalid
	}
	userID := data[1] & 0x3F
	maxUsers := uint8(15)
	if s.bmcState != nil {
		maxUsers = s.bmcState.MaxUsers()
	}
	if !validLANChannel(data[0]) || userID == 0 || userID > maxUsers {
		return 0, CompletionCodeInvalidField
	}
	return userID, CompletionCodeOK
}

// handleSetUserPayloadAccess enables (operation 0) or disables (1) the SOL
// payload for a user; the other payloads are ignored.
func (s *Server) handleSetUserPayloadAccess(data []byte) (CompletionCode, []byte) {
	userID, code := s.payloadAccessUser(data, 6)
	if code != CompletionCodeOK {
		return code, nil
	}
	op := data[1] >> 6
	if op > 1 {
		return CompletionCodeInvalidField, nil
	}
	if data[2]&0x02 != 0 {
		s.sol.mu.Lock()
		if op == 1 {
			s.sol.disabledUsers[userID] = true
		} else {
			delete(s.sol.disabledUsers, userID)
		}
		s.sol.mu.Unlock()
	}
	return CompletionCodeOK, nil
}

func (s *Server) handleGetUserPayloadAccess(data []byte) (CompletionCode, []byte) {
	userID, code := s.payloadAccessUser(data, 2)
	if code != CompletionCodeOK {
		return code, nil
	}
	resp := make([]byte, 4)
	s.sol.mu.Lock()
	if !s.sol.disabledUsers[userID] {
		resp[0] = 0x02 // payload 1: SOL
	}
	s.sol.mu.Unlock()
	return CompletionCodeOK, resp
}

// handleGetChannelPayloadSupport: IPMI and SOL (if there is a serial
// console) and the session setup payloads.
func (s *Server) handleGetChannelPayloadSupport(data []byte) (CompletionCode, []byte) {
	if len(data) < 1 {
		return CompletionCodeReqDataLenInvalid, nil
	}
	if !validLANChannel(data[0]) {
		return CompletionCodeInvalidField, nil
	}
	standard := uint8(0x01) // IPMI
	if s.sol.available() {
		standard |= 0x02 // SOL
	}
	// Session setup: Open Session Request/Response, RAKP 1-4
	return CompletionCodeOK, []byte{standard, 0x00, 0x3F, 0x00, 0x00, 0x00, 0x00, 0x00}
}

// handleGetChannelPayloadVersion: every supported payload is version 1.0.
func (s *Server) handleGetChannelPayloadVersion(data []byte) (CompletionCode, []byte) {
	if len(data) < 2 {
		return CompletionCodeReqDataLenInvalid, nil
	}
	if !validLANChannel(data[0]) {
		return CompletionCodeInvalidField, nil
	}
	switch t := data[1]; {
	case t == PayloadTypeIPMI,
		t == PayloadTypeSOL && s.sol.available(),
		t >= PayloadTypeOpenSessionRequest && t <= PayloadTypeRAKPMessage4:
		return CompletionCodeOK, []byte{0x10}
	}
	return completionCodePayloadNotAvailable, nil
}

func (m *solManager) udpPort() uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.port
}

// getConfig handles Get SOL Configuration Parameters. Request: channel
// (bit 7: revision only), parameter, set selector, block selector.
func (m *solManager) getConfig(data []byte) (CompletionCode, []byte) {
	if len(data) < 4 {
		return CompletionCodeReqDataLenInvalid, nil
	}
	if !validLANChannel(data[0]) {
		return CompletionCodeInvalidField, nil
	}
	m.mu.Lock()
	c := m.cfg
	port := m.port
	enabled := c.enabled && m.console != nil
	m.mu.Unlock()

	var value []byte
	switch data[1] {
	case 0: // Set In Progress
		value = []byte{c.setInProgress}
	case 1: // SOL Enable
		value = []byte{0x00}
		if enabled {
			value[0] = 0x01
		}
	case 2: // SOL Authentication
		value = []byte{c.auth}
	case 3: // Character Accumulate Interval & Send Threshold
		value = []byte{c.accumulateInterval, c.sendThreshold}
	case 4: // SOL Retry
		value = []byte{c.retryCount, c.retryInterval}
	case 5: // SOL non-volatile bit rate
		value = []byte{c.nonVolatileBitRate}
	case 6: // SOL volatile bit rate
		value = []byte{c.volatileBitRate}
	case 7: // SOL Payload Channel
		value = []byte{lanChannel}
	case 8: // SOL Payload Port Number
		value = binary.LittleEndian.AppendUint16(nil, port)
	default:
		return completionCodeParamNotSupported, nil
	}
	if data[0]&0x80 != 0 {
		return CompletionCodeOK, []byte{solConfigRevision}
	}
	return CompletionCodeOK, append([]byte{solConfigRevision}, value...)
}

// validBitRate: serial setting, 9600, 19200, 38400, 57600, 115200
func validBitRate(v uint8) bool {
	return v == 0 || (v >= 6 && v <= 0x0A)
}

// setConfig handles Set SOL Configuration Parameters. Request: channel,
// parameter, data. Disabling SOL deactivates it.
func (m *solManager) setConfig(data []byte) (CompletionCode, []byte) {
	if len(data) < 3 {
		return CompletionCodeReqDataLenInvalid, nil
	}
	if !validLANChannel(data[0]) {
		return CompletionCodeInvalidField, nil
	}
	param, v := data[1], data[2:]
	need := map[uint8]int{3: 2, 4: 2}[param]
	if len(v) < need {
		return CompletionCodeReqDataLenInvalid, nil
	}
	code, stop := m.applyConfig(param, v)
	if stop != nil {
		m.deactivate(stop, true, "SOL disabled")
	}
	return code, nil
}

// applyConfig sets SOL configuration parameter param. It returns the SOL
// instance to deactivate, if any.
func (m *solManager) applyConfig(param uint8, v []byte) (CompletionCode, *solInstance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var stop *solInstance
	switch param {
	case 0:
		switch v[0] & 0x03 {
		case 0, 2: // set complete, commit write (nothing to commit)
			m.cfg.setInProgress = 0
		case 1:
			m.cfg.setInProgress = 1
		default:
			return CompletionCodeInvalidField, nil
		}
	case 1:
		m.cfg.enabled = v[0]&0x01 != 0
		if !m.cfg.enabled {
			stop = m.active
		}
	case 2:
		if priv := v[0] & 0x0F; priv < privilegeUser || priv > privilegeOEM {
			return CompletionCodeInvalidField, nil
		}
		m.cfg.auth = v[0] & 0xCF
	case 3:
		if v[0] == 0 {
			return CompletionCodeInvalidField, nil
		}
		m.cfg.accumulateInterval, m.cfg.sendThreshold = v[0], v[1]
	case 4:
		m.cfg.retryCount, m.cfg.retryInterval = v[0]&0x07, v[1]
	case 5, 6:
		rate := v[0] & 0x0F
		if !validBitRate(rate) {
			return CompletionCodeInvalidField, nil
		}
		if param == 5 {
			m.cfg.nonVolatileBitRate = rate
		} else {
			m.cfg.volatileBitRate = rate
		}
	case 7, 8:
		return completionCodeParamReadOnly, nil
	default:
		return completionCodeParamNotSupported, nil
	}
	return CompletionCodeOK, stop
}
