package ipmi

import (
	"bytes"
	"crypto/hmac"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/bmc"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/serial"
)

// solTestBMC is an IPMI server on a UDP socket with a serial console whose
// VM side the test plays.
type solTestBMC struct {
	server *Server
	addr   net.Addr
	mux    *serial.Mux
	vm     net.Conn    // the VM's end of the serial console
	input  chan []byte // what the VM reads from it
}

func newSOLTestBMC(t *testing.T) *solTestBMC {
	t.Helper()
	server := NewServer(newIPMIMockMachine(machine.PowerOn), bmc.NewState("admin", "password"), "admin", "password")
	mux := serial.NewMux(4096)
	server.SetSerialConsole(mux)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	go server.Serve(conn)
	t.Cleanup(func() { server.Close() })

	vm, bmcEnd := net.Pipe()
	go mux.Attach(bmcEnd)
	t.Cleanup(func() { vm.Close() })
	require.Eventually(t, mux.Connected, time.Second, 5*time.Millisecond)
	// A pipe write blocks until read: the VM reads all the time, like QEMU
	input := make(chan []byte, 100)
	go func() {
		for {
			buf := make([]byte, 1024)
			n, err := vm.Read(buf)
			if err != nil {
				return
			}
			input <- buf[:n]
		}
	}()
	return &solTestBMC{server: server, addr: conn.LocalAddr(), mux: mux, vm: vm, input: input}
}

// vmWrite writes VM output (the mux reads it from the pipe).
func (b *solTestBMC) vmWrite(t *testing.T, s string) {
	t.Helper()
	_, err := b.vm.Write([]byte(s))
	require.NoError(t, err)
}

// vmRead reads n bytes of VM input.
func (b *solTestBMC) vmRead(t *testing.T, n int) string {
	t.Helper()
	var got []byte
	for len(got) < n {
		select {
		case p := <-b.input:
			got = append(got, p...)
		case <-time.After(2 * time.Second):
			t.Fatalf("VM input: got %q, want %d bytes", got, n)
		}
	}
	return string(got)
}

// solTestConsole is a remote console with an RMCP+ session to the BMC.
type solTestConsole struct {
	t       *testing.T
	conn    net.Conn
	session *Session // the console's view of the session: IDs and keys
	rqSeq   uint8
}

func newSOLTestConsole(t *testing.T, b *solTestBMC, authAlg, intAlg uint8) *solTestConsole {
	t.Helper()
	conn, err := net.Dial("udp", b.addr.String())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	c := &solTestConsole{t: t, conn: conn}

	openReq := buildOpenSessionRequestWithAlgorithms(0x01, 0xAAAABBBB, authAlg, intAlg, ConfAlgorithmAESCBC128)
	resp := c.roundTrip(wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq))
	require.Equal(t, uint8(0), resp[13], "open session status")
	managedID := binary.LittleEndian.Uint32(resp[20:24])

	resp = c.roundTrip(wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, buildRAKPMessage1(0x02, managedID, "admin")))
	require.Equal(t, uint8(0), resp[13], "RAKP 2 status")

	// The keys, derived like a real console does (the BMC's session is
	// owned by the server goroutine)
	rakp2 := resp
	managedRandom := rakp2[20:36]
	var consoleRandom []byte
	for i := 8; i < 24; i++ { // buildRAKPMessage1
		consoleRandom = append(consoleRandom, byte(i))
	}
	const priv = 0x04
	user := []byte("admin")
	h := authHash(authAlg)
	mac := hmac.New(h, []byte("password"))
	mac.Write(buildRAKP3AuthBuf(managedRandom, 0xAAAABBBB, priv, uint8(len(user)), user))
	resp = c.roundTrip(wrapRMCPPlusPayload(PayloadTypeRAKPMessage3, 0, 0, buildRAKPMessage3(0x03, managedID, mac.Sum(nil))))
	require.Equal(t, uint8(0), resp[13], "RAKP 4 status")

	sik := hmac.New(h, []byte("password"))
	sik.Write(consoleRandom)
	sik.Write(managedRandom)
	sik.Write([]byte{priv, uint8(len(user))})
	sik.Write(user)
	key := func(c byte) []byte {
		m := hmac.New(h, sik.Sum(nil))
		m.Write(bytes.Repeat([]byte{c}, 20))
		return m.Sum(nil)
	}
	c.session = &Session{
		RemoteConsoleSessionID: 0xAAAABBBB,
		ManagedSystemSessionID: managedID,
		IntegrityAlgorithm:     intAlg,
		IntegrityKey:           key(0x01),
		ConfidentialityKey:     key(0x02),
	}
	return c
}

func (c *solTestConsole) send(pkt []byte) {
	c.t.Helper()
	_, err := c.conn.Write(SerializeRMCPMessage(RMCPClassIPMI, pkt))
	require.NoError(c.t, err)
}

// recv returns the next packet (without RMCP header), nil on timeout.
func (c *solTestConsole) recv(timeout time.Duration) []byte {
	c.t.Helper()
	buf := make([]byte, 2048)
	require.NoError(c.t, c.conn.SetReadDeadline(time.Now().Add(timeout)))
	n, err := c.conn.Read(buf)
	if err != nil {
		return nil
	}
	return buf[4:n]
}

func (c *solTestConsole) roundTrip(pkt []byte) []byte {
	c.t.Helper()
	c.send(pkt)
	resp := c.recv(2 * time.Second)
	require.NotNil(c.t, resp, "no response")
	return resp
}

// seal builds an encrypted and authenticated packet of the session.
func (c *solTestConsole) seal(payloadType uint8, plain []byte) []byte {
	c.t.Helper()
	enc, err := encryptIPMISpecAESCBC(c.session.ConfidentialityKey, plain)
	require.NoError(c.t, err)
	pkt := buildAuthenticatedRMCPPlusPacket(c.session, enc)
	pkt[1] = 0xC0 | payloadType
	// The AuthCode covers the payload type
	_, n := integrityHash(c.session.IntegrityAlgorithm)
	copy(pkt[len(pkt)-n:], integrityAuthCode(c.session, pkt[:len(pkt)-n]))
	return pkt
}

// open checks the integrity of a packet from the BMC and returns its
// payload type and decrypted payload.
func (c *solTestConsole) open(pkt []byte) (uint8, []byte) {
	c.t.Helper()
	require.GreaterOrEqual(c.t, len(pkt), 12)
	require.Equal(c.t, uint8(AuthTypeRMCPPlus), pkt[0])
	require.Equal(c.t, uint8(0xC0), pkt[1]&0xC0, "encrypted and authenticated")
	require.Equal(c.t, c.session.RemoteConsoleSessionID, binary.LittleEndian.Uint32(pkt[2:6]))
	_, n := integrityHash(c.session.IntegrityAlgorithm)
	require.True(c.t, hmac.Equal(pkt[len(pkt)-n:], integrityAuthCode(c.session, pkt[:len(pkt)-n])), "integrity")
	length := int(binary.LittleEndian.Uint16(pkt[10:12]))
	plain, err := decryptAESCBC(c.session.ConfidentialityKey, pkt[12:12+length])
	require.NoError(c.t, err)
	return pkt[1] & 0x3F, plain
}

// command sends an IPMI request in the session and returns the completion
// code and response data. SOL packets coming in between are skipped.
func (c *solTestConsole) command(netFn, cmd uint8, data []byte) (CompletionCode, []byte) {
	c.t.Helper()
	c.rqSeq++
	msg := buildTestIPMIRequest(netFn, cmd, data)
	msg[4] = c.rqSeq << 2
	// recompute the data checksum for the new sequence number
	msg[len(msg)-1] = Checksum(append([]byte{msg[3], msg[4], msg[5]}, data...)...)
	c.send(c.seal(PayloadTypeIPMI, msg))
	for {
		pkt := c.recv(2 * time.Second)
		require.NotNil(c.t, pkt, "no response to netfn 0x%02x cmd 0x%02x", netFn, cmd)
		typ, plain := c.open(pkt)
		if typ != PayloadTypeIPMI {
			continue
		}
		require.GreaterOrEqual(c.t, len(plain), 8)
		require.Equal(c.t, c.rqSeq<<2, plain[4], "request sequence echoed")
		return CompletionCode(plain[6]), plain[7 : len(plain)-1]
	}
}

func (c *solTestConsole) activate() (CompletionCode, []byte) {
	return c.command(NetFnApp, CmdActivatePayload, []byte{PayloadTypeSOL, 1, 0xC6, 0, 0, 0})
}

// sendSOL sends a SOL packet.
func (c *solTestConsole) sendSOL(p solPacket) {
	c.t.Helper()
	c.send(c.seal(PayloadTypeSOL, p.bytes()))
}

// recvSOL returns the next SOL packet, false on timeout.
func (c *solTestConsole) recvSOL(timeout time.Duration) (solPacket, bool) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		pkt := c.recv(time.Until(deadline))
		if pkt == nil {
			return solPacket{}, false
		}
		typ, plain := c.open(pkt)
		if typ != PayloadTypeSOL {
			continue
		}
		p, err := parseSOLPacket(plain)
		require.NoError(c.t, err)
		return p, true
	}
}

func (c *solTestConsole) mustRecvSOL() solPacket {
	c.t.Helper()
	p, ok := c.recvSOL(2 * time.Second)
	require.True(c.t, ok, "no SOL packet")
	return p
}

var solTestSuites = []struct {
	name            string
	authAlg, intAlg uint8
}{
	{"suite3", AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96},
	{"suite17", AuthAlgorithmHMACSHA256, IntegrityAlgorithmHMACSHA256_128},
}

func TestSOLPacket_RoundTrip(t *testing.T) {
	p := solPacket{Seq: 3, AckSeq: 2, Accepted: 5, Status: solStatusNACK, Data: []byte("abc")}
	got, err := parseSOLPacket(p.bytes())
	require.NoError(t, err)
	assert.Equal(t, p, got)
	assert.Equal(t, []byte{0x03, 0x02, 0x05, 0x40, 'a', 'b', 'c'}, p.bytes())

	_, err = parseSOLPacket([]byte{1, 2, 3})
	assert.Error(t, err)
}

func TestNextSOLSeq(t *testing.T) {
	assert.Equal(t, uint8(1), nextSOLSeq(0))
	assert.Equal(t, uint8(2), nextSOLSeq(1))
	assert.Equal(t, uint8(15), nextSOLSeq(14))
	assert.Equal(t, uint8(1), nextSOLSeq(15), "sequence numbers are 1-15, 0 is for ACK-only packets")
}

func TestSOL_ActivateOutputAndInput(t *testing.T) {
	for _, suite := range solTestSuites {
		t.Run(suite.name, func(t *testing.T) {
			b := newSOLTestBMC(t)
			c := newSOLTestConsole(t, b, suite.authAlg, suite.intAlg)

			code, resp := c.activate()
			require.Equal(t, CompletionCodeOK, code)
			require.Len(t, resp, 12, "ipmitool requires exactly 12 bytes")
			assert.Equal(t, uint16(solMaxPayload), binary.LittleEndian.Uint16(resp[4:6]), "inbound payload size")
			assert.Equal(t, uint16(solMaxPayload), binary.LittleEndian.Uint16(resp[6:8]), "outbound payload size")
			assert.Equal(t, uint16(b.addr.(*net.UDPAddr).Port), binary.LittleEndian.Uint16(resp[8:10]), "SOL runs on the IPMI port")
			assert.Equal(t, uint16(0xFFFF), binary.LittleEndian.Uint16(resp[10:12]), "no VLAN")

			// VM output
			b.vmWrite(t, "login: ")
			p := c.mustRecvSOL()
			assert.Equal(t, uint8(1), p.Seq)
			assert.Equal(t, uint8(0), p.AckSeq)
			assert.Equal(t, "login: ", string(p.Data))
			c.sendSOL(solPacket{AckSeq: p.Seq, Accepted: uint8(len(p.Data))})

			b.vmWrite(t, "again")
			p = c.mustRecvSOL()
			assert.Equal(t, uint8(2), p.Seq, "a new packet has the next sequence number")
			assert.Equal(t, "again", string(p.Data))
			c.sendSOL(solPacket{AckSeq: p.Seq, Accepted: uint8(len(p.Data))})

			// Console input
			c.sendSOL(solPacket{Seq: 1, Data: []byte("root\r")})
			ack := c.mustRecvSOL()
			assert.Equal(t, solPacket{AckSeq: 1, Accepted: 5, Data: []byte{}}, ack)
			assert.Equal(t, "root\r", b.vmRead(t, 5))
		})
	}
}

func TestSOL_InputResentIsWrittenOnce(t *testing.T) {
	b := newSOLTestBMC(t)
	c := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := c.activate()
	require.Equal(t, CompletionCodeOK, code)

	c.sendSOL(solPacket{Seq: 4, Data: []byte("ab")})
	assert.Equal(t, uint8(2), c.mustRecvSOL().Accepted)
	assert.Equal(t, "ab", b.vmRead(t, 2))

	// The ACK got lost: the console sends the packet again
	c.sendSOL(solPacket{Seq: 4, Data: []byte("ab")})
	ack := c.mustRecvSOL()
	assert.Equal(t, uint8(4), ack.AckSeq)
	assert.Equal(t, uint8(2), ack.Accepted)

	c.sendSOL(solPacket{Seq: 5, Data: []byte("c")})
	assert.Equal(t, uint8(5), c.mustRecvSOL().AckSeq)
	assert.Equal(t, "c", b.vmRead(t, 1), "the resent packet must not be written again")
}

func TestSOL_OutputResentUntilACKed(t *testing.T) {
	b := newSOLTestBMC(t)
	c := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := c.activate()
	require.Equal(t, CompletionCodeOK, code)

	b.vmWrite(t, "abcdef")
	first := c.mustRecvSOL()
	assert.Equal(t, "abcdef", string(first.Data))

	// Not ACKed: the same packet again
	again := c.mustRecvSOL()
	assert.Equal(t, first.Seq, again.Seq)
	assert.Equal(t, "abcdef", string(again.Data))

	// Output while waiting goes in the next packet
	b.vmWrite(t, "gh")
	time.Sleep(50 * time.Millisecond)
	// Partly accepted: the rest comes first in a new packet
	c.sendSOL(solPacket{AckSeq: first.Seq, Accepted: 2})
	var next solPacket
	for {
		next = c.mustRecvSOL()
		if next.Seq != first.Seq {
			break
		}
	}
	assert.Equal(t, nextSOLSeq(first.Seq), next.Seq)
	assert.Equal(t, "cdefgh", string(next.Data))
}

func TestSOL_OutputDroppedAfterRetries(t *testing.T) {
	b := newSOLTestBMC(t)
	b.server.sol.mu.Lock()
	b.server.sol.cfg.retryCount = 1
	b.server.sol.cfg.retryInterval = 2 // 20 ms
	b.server.sol.mu.Unlock()
	c := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := c.activate()
	require.Equal(t, CompletionCodeOK, code)

	b.vmWrite(t, "lost")
	p1 := c.mustRecvSOL()
	p2 := c.mustRecvSOL()
	assert.Equal(t, p1, p2, "one retry")
	_, ok := c.recvSOL(150 * time.Millisecond)
	assert.False(t, ok, "dropped after the retries")

	b.vmWrite(t, "next")
	p := c.mustRecvSOL()
	assert.Equal(t, "next", string(p.Data))
	assert.Equal(t, nextSOLSeq(p1.Seq), p.Seq)
}

func TestSOL_ActivateTwice(t *testing.T) {
	b := newSOLTestBMC(t)
	a := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	o := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA256, IntegrityAlgorithmHMACSHA256_128)

	code, _ := a.activate()
	require.Equal(t, CompletionCodeOK, code)
	code, _ = o.activate()
	assert.Equal(t, CompletionCode(0x80), code, "already active on another session")
	code, _ = a.activate()
	assert.Equal(t, CompletionCode(0x80), code)

	code, resp := o.command(NetFnApp, CmdGetPayloadActivationStatus, []byte{PayloadTypeSOL})
	require.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, []byte{0x01, 0x01, 0x00}, resp)
	code, resp = o.command(NetFnApp, CmdGetPayloadInstanceInfo, []byte{PayloadTypeSOL, 1})
	require.Equal(t, CompletionCodeOK, code)
	require.Len(t, resp, 12)
	assert.Equal(t, a.session.ManagedSystemSessionID, binary.LittleEndian.Uint32(resp[0:4]))
}

func TestSOL_DeactivateOwn(t *testing.T) {
	b := newSOLTestBMC(t)
	c := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := c.activate()
	require.Equal(t, CompletionCodeOK, code)

	code, _ = c.command(NetFnApp, CmdDeactivatePayload, []byte{PayloadTypeSOL, 1, 0, 0, 0, 0})
	assert.Equal(t, CompletionCodeOK, code)
	code, _ = c.command(NetFnApp, CmdDeactivatePayload, []byte{PayloadTypeSOL, 1, 0, 0, 0, 0})
	assert.Equal(t, CompletionCode(0x80), code, "already deactivated")
	assert.Equal(t, 0, b.mux.Subscribers(), "the console subscription ends")

	// The session goes on and can activate again
	code, _ = c.activate()
	assert.Equal(t, CompletionCodeOK, code)
}

func TestSOL_DeactivateFromAnotherSession(t *testing.T) {
	b := newSOLTestBMC(t)
	a := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	o := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := a.activate()
	require.Equal(t, CompletionCodeOK, code)

	code, _ = o.command(NetFnApp, CmdDeactivatePayload, []byte{PayloadTypeSOL, 1, 0, 0, 0, 0})
	require.Equal(t, CompletionCodeOK, code)

	// The holder is told and its session closed
	p := a.mustRecvSOL()
	assert.Equal(t, uint8(solStatusDeactivating), p.Status&solStatusDeactivating)
	_, ok := b.server.sessionMgr.GetSession(a.session.ManagedSystemSessionID)
	assert.False(t, ok, "the session SOL was active in is closed")

	code, _ = o.activate()
	assert.Equal(t, CompletionCodeOK, code)
}

func TestSOL_CloseSessionDeactivates(t *testing.T) {
	b := newSOLTestBMC(t)
	c := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := c.activate()
	require.Equal(t, CompletionCodeOK, code)

	id := binary.LittleEndian.AppendUint32(nil, c.session.ManagedSystemSessionID)
	code, _ = c.command(NetFnApp, CmdCloseSession, id)
	require.Equal(t, CompletionCodeOK, code)
	_, ok := b.server.sessionMgr.GetSession(c.session.ManagedSystemSessionID)
	assert.False(t, ok)
	assert.Nil(t, b.server.sol.activeSession())
	assert.Equal(t, 0, b.mux.Subscribers())
}

func TestSOL_IdleSessionDeactivates(t *testing.T) {
	b := newSOLTestBMC(t)
	c := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := c.activate()
	require.Equal(t, CompletionCodeOK, code)

	for _, s := range b.server.sessionMgr.RemoveIdle(0) {
		b.server.sol.sessionClosed(s, "test")
	}
	assert.Nil(t, b.server.sol.activeSession())
}

func TestSOL_InputWhileVMOffIsNACKed(t *testing.T) {
	b := newSOLTestBMC(t)
	c := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := c.activate()
	require.Equal(t, CompletionCodeOK, code)

	b.vm.Close()
	require.Eventually(t, func() bool { return !b.mux.Connected() }, time.Second, 5*time.Millisecond)
	c.sendSOL(solPacket{Seq: 1, Data: []byte("x")})
	ack := c.mustRecvSOL()
	assert.Equal(t, uint8(1), ack.AckSeq)
	assert.Equal(t, uint8(0), ack.Accepted)
	assert.Equal(t, uint8(solStatusNACK|solStatusTransferUnavailable), ack.Status)
}

func TestSOL_PacketsOfOtherSessionsIgnored(t *testing.T) {
	b := newSOLTestBMC(t)
	a := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	o := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := a.activate()
	require.Equal(t, CompletionCodeOK, code)

	o.sendSOL(solPacket{Seq: 1, Data: []byte("x")})
	_, ok := o.recvSOL(200 * time.Millisecond)
	assert.False(t, ok)
}

func TestSOL_UnauthenticatedPacketRejected(t *testing.T) {
	b := newSOLTestBMC(t)
	c := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := c.activate()
	require.Equal(t, CompletionCodeOK, code)

	pkt := c.seal(PayloadTypeSOL, solPacket{Seq: 1, Data: []byte("x")}.bytes())
	pkt[len(pkt)-1] ^= 0xFF // bad AuthCode
	c.send(pkt)
	_, ok := c.recvSOL(200 * time.Millisecond)
	assert.False(t, ok)
}

func TestActivatePayload_Errors(t *testing.T) {
	server := NewServer(newIPMIMockMachine(machine.PowerOn), bmc.NewState("admin", "password"), "admin", "password")
	session := &Session{ConfidentialityAlgorithm: ConfAlgorithmAESCBC128, IntegrityAlgorithm: IntegrityAlgorithmHMACSHA1_96, RequestedPrivilegeLevel: 0x14}

	code, _ := server.handleActivatePayload([]byte{PayloadTypeSOL, 1, 0xC6, 0, 0, 0}, session)
	assert.Equal(t, CompletionCode(0x81), code, "no serial console (legacy mode): SOL disabled")

	server.SetSerialConsole(serial.NewMux(0))
	code, _ = server.handleActivatePayload([]byte{PayloadTypeSOL, 1}, session)
	assert.Equal(t, CompletionCodeReqDataLenInvalid, code)
	code, _ = server.handleActivatePayload([]byte{PayloadTypeIPMI, 1, 0, 0, 0, 0}, session)
	assert.Equal(t, CompletionCodeInvalidField, code)
	code, _ = server.handleActivatePayload([]byte{PayloadTypeSOL, 2, 0, 0, 0, 0}, session)
	assert.Equal(t, CompletionCodeInvalidField, code, "one instance")
	code, _ = server.handleActivatePayload([]byte{PayloadTypeSOL, 1, 0, 0, 0, 0}, nil)
	assert.Equal(t, CompletionCodeNotInPresentState, code, "RMCP+ sessions only")

	user := &Session{RequestedPrivilegeLevel: 0x02, ConfidentialityAlgorithm: ConfAlgorithmAESCBC128}
	server.sol.cfg.auth = privilegeAdmin
	code, _ = server.handleActivatePayload([]byte{PayloadTypeSOL, 1, 0, 0, 0, 0}, user)
	assert.Equal(t, CompletionCodeInsufficientPrivilege, code)
	server.sol.cfg.auth = privilegeUser

	noCrypt := &Session{RequestedPrivilegeLevel: 0x04}
	code, _ = server.handleActivatePayload([]byte{PayloadTypeSOL, 1, 0x80, 0, 0, 0}, noCrypt)
	assert.Equal(t, CompletionCode(0x83), code, "cannot activate with encryption")

	code, _ = server.sol.setConfig([]byte{0x01, 1, 0x00})
	require.Equal(t, CompletionCodeOK, code)
	code, _ = server.handleActivatePayload([]byte{PayloadTypeSOL, 1, 0xC6, 0, 0, 0}, session)
	assert.Equal(t, CompletionCode(0x81), code, "SOL disabled")
}

func TestUserPayloadAccess(t *testing.T) {
	server := NewServer(newIPMIMockMachine(machine.PowerOn), bmc.NewState("admin", "password"), "admin", "password")
	server.SetSerialConsole(serial.NewMux(0))

	code, resp := server.handleGetUserPayloadAccess([]byte{0x01, 0x02})
	require.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, []byte{0x02, 0, 0, 0}, resp, "SOL enabled by default")

	// ipmitool sol payload disable 1 2
	code, _ = server.handleSetUserPayloadAccess([]byte{0x01, 0x42, 0x02, 0, 0, 0})
	require.Equal(t, CompletionCodeOK, code)
	_, resp = server.handleGetUserPayloadAccess([]byte{0x01, 0x02})
	assert.Equal(t, []byte{0x00, 0, 0, 0}, resp)

	userID, ok := server.bmcState.LookupUserByName("admin")
	require.True(t, ok)
	require.Equal(t, uint8(2), userID)
	session := &Session{UserName: []byte("admin"), RequestedPrivilegeLevel: 0x14, ConfidentialityAlgorithm: ConfAlgorithmAESCBC128}
	code, _ = server.handleActivatePayload([]byte{PayloadTypeSOL, 1, 0xC6, 0, 0, 0}, session)
	assert.Equal(t, CompletionCodeInsufficientPrivilege, code)

	code, _ = server.handleSetUserPayloadAccess([]byte{0x0E, 0x02, 0x02, 0, 0, 0})
	require.Equal(t, CompletionCodeOK, code)
	_, resp = server.handleGetUserPayloadAccess([]byte{0x01, 0x02})
	assert.Equal(t, []byte{0x02, 0, 0, 0}, resp)

	code, _ = server.handleGetUserPayloadAccess([]byte{0x05, 0x02})
	assert.Equal(t, CompletionCodeInvalidField, code, "not the LAN channel")
	code, _ = server.handleSetUserPayloadAccess([]byte{0x01, 0x02})
	assert.Equal(t, CompletionCodeReqDataLenInvalid, code)
}

func TestChannelPayloadSupportAndVersion(t *testing.T) {
	server := NewServer(newIPMIMockMachine(machine.PowerOn), nil, "admin", "password")
	code, resp := server.handleGetChannelPayloadSupport([]byte{0x0E})
	require.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, []byte{0x01, 0x00, 0x3F, 0x00, 0, 0, 0, 0}, resp, "no SOL without serial console")
	code, _ = server.handleGetChannelPayloadVersion([]byte{0x01, PayloadTypeSOL})
	assert.Equal(t, CompletionCode(0x80), code)

	server.SetSerialConsole(serial.NewMux(0))
	_, resp = server.handleGetChannelPayloadSupport([]byte{0x01})
	assert.Equal(t, []byte{0x03, 0x00, 0x3F, 0x00, 0, 0, 0, 0}, resp)
	for _, typ := range []uint8{PayloadTypeIPMI, PayloadTypeSOL, PayloadTypeOpenSessionRequest, PayloadTypeRAKPMessage4} {
		code, resp = server.handleGetChannelPayloadVersion([]byte{0x01, typ})
		require.Equal(t, CompletionCodeOK, code, "payload 0x%02x", typ)
		assert.Equal(t, []byte{0x10}, resp)
	}
	code, _ = server.handleGetChannelPayloadVersion([]byte{0x01, 0x02})
	assert.Equal(t, CompletionCode(0x80), code)

	code, resp = server.handleGetPayloadActivationStatus([]byte{PayloadTypeSOL})
	require.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, []byte{0x01, 0x00, 0x00}, resp)
	code, resp = server.handleGetPayloadInstanceInfo([]byte{PayloadTypeSOL, 1})
	require.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, make([]byte, 12), resp, "not active: session ID 0")
}

func TestSOLConfigParams(t *testing.T) {
	m := newSOLManager()
	m.console = serial.NewMux(0)

	get := func(param uint8) []byte {
		t.Helper()
		code, resp := m.getConfig([]byte{0x0E, param, 0, 0})
		require.Equal(t, CompletionCodeOK, code, "param %d", param)
		require.Equal(t, uint8(solConfigRevision), resp[0])
		return resp[1:]
	}
	assert.Equal(t, []byte{0x00}, get(0), "set complete")
	assert.Equal(t, []byte{0x01}, get(1), "enabled")
	assert.Equal(t, []byte{0x02}, get(2), "user privilege, not forced")
	assert.Equal(t, []byte{12, 96}, get(3))
	assert.Equal(t, []byte{7, 10}, get(4))
	assert.Equal(t, []byte{0x0A}, get(5), "115200")
	assert.Equal(t, []byte{0x0A}, get(6))
	assert.Equal(t, []byte{0x01}, get(7), "LAN channel")
	assert.Equal(t, []byte{0x6F, 0x02}, get(8), "port 623")

	code, resp := m.getConfig([]byte{0x81, 3, 0, 0})
	require.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, []byte{solConfigRevision}, resp, "revision only")
	code, _ = m.getConfig([]byte{0x01, 9, 0, 0})
	assert.Equal(t, CompletionCode(0x80), code, "parameter not supported")
	code, _ = m.getConfig([]byte{0x03, 1, 0, 0})
	assert.Equal(t, CompletionCodeInvalidField, code, "not the LAN channel")
	code, _ = m.getConfig([]byte{0x01, 1})
	assert.Equal(t, CompletionCodeReqDataLenInvalid, code)

	set := func(param uint8, v ...byte) CompletionCode {
		code, _ := m.setConfig(append([]byte{0x01, param}, v...))
		return code
	}
	assert.Equal(t, CompletionCodeOK, set(0, 1))
	assert.Equal(t, []byte{0x01}, get(0))
	assert.Equal(t, CompletionCodeOK, set(0, 2), "commit write")
	assert.Equal(t, []byte{0x00}, get(0))
	assert.Equal(t, CompletionCodeInvalidField, set(0, 3))

	assert.Equal(t, CompletionCodeOK, set(1, 0))
	assert.Equal(t, []byte{0x00}, get(1))
	assert.Equal(t, CompletionCodeOK, set(1, 1))

	assert.Equal(t, CompletionCodeOK, set(2, 0xC4))
	assert.Equal(t, []byte{0xC4}, get(2))
	assert.Equal(t, CompletionCodeInvalidField, set(2, 0x01), "callback privilege")

	assert.Equal(t, CompletionCodeOK, set(3, 2, 50))
	assert.Equal(t, []byte{2, 50}, get(3))
	assert.Equal(t, CompletionCodeInvalidField, set(3, 0, 50))
	assert.Equal(t, CompletionCodeReqDataLenInvalid, set(3, 2))

	assert.Equal(t, CompletionCodeOK, set(4, 0x0B, 20))
	assert.Equal(t, []byte{0x03, 20}, get(4), "retry count is 3 bits")

	assert.Equal(t, CompletionCodeOK, set(5, 0x06))
	assert.Equal(t, []byte{0x06}, get(5))
	assert.Equal(t, CompletionCodeOK, set(6, 0x09))
	assert.Equal(t, []byte{0x09}, get(6))
	assert.Equal(t, CompletionCodeInvalidField, set(6, 0x05))

	assert.Equal(t, CompletionCode(0x82), set(7, 2), "read only")
	assert.Equal(t, CompletionCode(0x82), set(8, 1, 2), "read only")
	assert.Equal(t, CompletionCode(0x80), set(12, 1))
}

func TestSOLConfig_DisableDeactivates(t *testing.T) {
	b := newSOLTestBMC(t)
	c := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := c.activate()
	require.Equal(t, CompletionCodeOK, code)

	code, _ = c.command(NetFnTransport, CmdSetSOLConfigParams, []byte{0x01, 1, 0x00})
	require.Equal(t, CompletionCodeOK, code)
	assert.Nil(t, b.server.sol.activeSession())
}

func TestSOL_OutputSplitIntoPackets(t *testing.T) {
	b := newSOLTestBMC(t)
	c := newSOLTestConsole(t, b, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96)
	code, _ := c.activate()
	require.Equal(t, CompletionCodeOK, code)

	want := bytes.Repeat([]byte("0123456789"), 60)
	b.vmWrite(t, string(want))
	var got []byte
	for len(got) < len(want) {
		p := c.mustRecvSOL()
		require.LessOrEqual(t, len(p.Data), solMaxData)
		got = append(got, p.Data...)
		c.sendSOL(solPacket{AckSeq: p.Seq, Accepted: uint8(len(p.Data))})
	}
	assert.Equal(t, string(want), string(got))
}
