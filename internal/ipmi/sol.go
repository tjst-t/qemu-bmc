package ipmi

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/tjst-t/qemu-bmc/internal/serial"
)

// SOL (Serial over LAN, IPMI v2.0 chapter 15) payload: one instance per
// BMC, activated by an RMCP+ session with Activate Payload. The VM serial
// console output is sent to the console in SOL packets, which it ACKs; its
// input packets are written to the VM and ACKed.

// SOL packet status bits, BMC to console (IPMI v2.0 table 15-2)
const (
	solStatusNACK                = 0x40
	solStatusTransferUnavailable = 0x20
	solStatusDeactivating        = 0x10
)

// solMaxPayload is the SOL payload size reported by Activate Payload, in
// both directions. The accepted character count is one byte, so a packet
// carries at most 255 characters.
const solMaxPayload = 255

// solMaxData is the most characters the BMC sends in one packet.
const solMaxData = solMaxPayload - 4

// solMaxBuffered is how much VM output waits for the console at most; the
// oldest is dropped beyond.
const solMaxBuffered = 64 * 1024

// solPacket is a SOL payload.
type solPacket struct {
	// Seq is the packet sequence number (1-15), 0 for an ACK-only packet
	Seq uint8
	// AckSeq is the sequence number of the packet ACKed or NACKed, 0 for none
	AckSeq uint8
	// Accepted is the number of characters accepted from the ACKed packet
	Accepted uint8
	// Status is the operation (console to BMC) or status (BMC to console)
	Status uint8
	Data   []byte
}

func parseSOLPacket(b []byte) (solPacket, error) {
	if len(b) < 4 {
		return solPacket{}, fmt.Errorf("SOL packet too short: %d bytes", len(b))
	}
	return solPacket{
		Seq:      b[0] & 0x0F,
		AckSeq:   b[1] & 0x0F,
		Accepted: b[2],
		Status:   b[3],
		Data:     b[4:],
	}, nil
}

func (p solPacket) bytes() []byte {
	b := make([]byte, 4, 4+len(p.Data))
	b[0], b[1], b[2], b[3] = p.Seq&0x0F, p.AckSeq&0x0F, p.Accepted, p.Status
	return append(b, p.Data...)
}

// nack reports whether the packet NACKs AckSeq.
func (p solPacket) nack() bool { return p.Status&solStatusNACK != 0 }

// nextSOLSeq returns the sequence number after seq: 1-15, never 0.
func nextSOLSeq(seq uint8) uint8 {
	if seq >= 15 {
		return 1
	}
	return seq + 1
}

// solConfig holds the SOL configuration parameters (Set/Get SOL
// Configuration Parameters). They are kept in memory only.
type solConfig struct {
	setInProgress uint8
	enabled       bool
	// auth: force encryption (bit 7), force authentication (bit 6),
	// privilege level (bits 3:0)
	auth uint8
	// accumulateInterval (5 ms units) and sendThreshold (characters): VM
	// output is sent when the interval has passed since the first
	// character or the threshold is reached
	accumulateInterval uint8
	sendThreshold      uint8
	// retryCount: resends of an unACKed packet before it is dropped;
	// retryInterval: 10 ms units
	retryCount    uint8
	retryInterval uint8
	// bit rates: 0 = serial setting, 6 = 9600 ... 0x0A = 115200
	nonVolatileBitRate uint8
	volatileBitRate    uint8
}

func defaultSOLConfig() solConfig {
	return solConfig{
		enabled:            true,
		auth:               privilegeUser,
		accumulateInterval: 12, // 60 ms
		sendThreshold:      96,
		retryCount:         7,
		retryInterval:      10, // 100 ms
		nonVolatileBitRate: 0x0A,
		volatileBitRate:    0x0A,
	}
}

func (c solConfig) accumulateDelay() time.Duration {
	return time.Duration(c.accumulateInterval) * 5 * time.Millisecond
}

func (c solConfig) retryDelay() time.Duration {
	d := time.Duration(c.retryInterval) * 10 * time.Millisecond
	if d < 10*time.Millisecond {
		// 0 means back to back; still give the console a moment
		d = 10 * time.Millisecond
	}
	return d
}

// Privilege levels
const (
	privilegeCallback = 0x01
	privilegeUser     = 0x02
	privilegeOperator = 0x03
	privilegeAdmin    = 0x04
	privilegeOEM      = 0x05
)

// solManager owns the SOL payload of the BMC.
type solManager struct {
	mu      sync.Mutex
	console serial.Console // nil: no serial console, SOL unavailable
	port    uint16         // UDP port SOL runs on (the IPMI port)
	cfg     solConfig
	active  *solInstance
	// users whose SOL payload access was disabled (Set User Payload Access)
	disabledUsers map[uint8]bool
	// send writes a packet (without RMCP header) to addr
	send func(pkt []byte, addr net.Addr)
}

func newSOLManager() *solManager {
	return &solManager{port: 623, cfg: defaultSOLConfig(), disabledUsers: map[uint8]bool{}}
}

func (m *solManager) available() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.console != nil
}

// activeSession returns the session SOL is active in, nil if inactive.
func (m *solManager) activeSession() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return nil
	}
	return m.active.session
}

// activate activates SOL for session, sending to addr, with encryption
// and authentication as requested in the aux data of Activate Payload.
func (m *solManager) activate(session *Session, userID uint8, aux uint8) CompletionCode {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.console == nil || !m.cfg.enabled {
		return completionCodePayloadDisabled
	}
	if m.active != nil {
		return completionCodePayloadActive
	}
	if userID != 0 && m.disabledUsers[userID] {
		return CompletionCodeInsufficientPrivilege
	}
	if priv := session.RequestedPrivilegeLevel & 0x0F; priv != 0 && priv < m.cfg.auth&0x0F {
		return CompletionCodeInsufficientPrivilege
	}
	encrypt := aux&0x80 != 0 || m.cfg.auth&0x80 != 0
	authenticate := aux&0x40 != 0 || m.cfg.auth&0x40 != 0
	if encrypt && session.ConfidentialityAlgorithm == ConfAlgorithmNone {
		return completionCodeCannotEncrypt
	}
	if authenticate && session.IntegrityAlgorithm == IntegrityAlgorithmNone {
		authenticate = false
	}
	var flags uint8
	if encrypt {
		flags |= 0x80
	}
	if authenticate {
		flags |= 0x40
	}

	inst := &solInstance{
		mgr:     m,
		session: session,
		flags:   flags,
		console: m.console,
		cfg:     m.cfg,
		acks:    make(chan solPacket, 16),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	inst.sub = m.console.Subscribe(false)
	m.active = inst
	go inst.run()
	log.Printf("IPMI SOL: activated by session 0x%08x (%s)", session.ManagedSystemSessionID, session.Addr())
	return CompletionCodeOK
}

// deactivate deactivates SOL if it is active in inst (any instance if inst
// is nil). With notify the console is told with a "SOL deactivating"
// packet. It returns the deactivated instance, nil if SOL was not active.
func (m *solManager) deactivate(inst *solInstance, notify bool, reason string) *solInstance {
	m.mu.Lock()
	if m.active == nil || (inst != nil && m.active != inst) {
		m.mu.Unlock()
		return nil
	}
	inst = m.active
	m.active = nil
	m.mu.Unlock()

	inst.close()
	if notify {
		inst.sendPacket(solPacket{Status: solStatusDeactivating})
	}
	log.Printf("IPMI SOL: deactivated (session 0x%08x): %s", inst.session.ManagedSystemSessionID, reason)
	return inst
}

// sessionClosed deactivates SOL if it is active in session.
func (m *solManager) sessionClosed(session *Session, reason string) {
	m.mu.Lock()
	inst := m.active
	m.mu.Unlock()
	if inst != nil && inst.session == session {
		m.deactivate(inst, false, reason)
	}
}

// receive handles a SOL packet from session and returns the packet to
// answer with, or nil.
func (m *solManager) receive(session *Session, b []byte) []byte {
	p, err := parseSOLPacket(b)
	if err != nil {
		log.Printf("IPMI SOL: %v", err)
		return nil
	}
	m.mu.Lock()
	inst := m.active
	m.mu.Unlock()
	if inst == nil || inst.session != session {
		// Not active in this session: ignored (IPMI v2.0 15.9)
		return nil
	}
	return inst.receive(p)
}

// solInstance is an active SOL payload.
type solInstance struct {
	mgr     *solManager
	session *Session
	flags   uint8 // encrypted (0x80) / authenticated (0x40) outbound packets
	console serial.Console
	sub     *serial.Subscription
	cfg     solConfig

	acks      chan solPacket // ACKs/NACKs of the output packets
	stop      chan struct{}
	stopOnce  sync.Once
	done      chan struct{} // closed when run returns
	mu        sync.Mutex
	lastInSeq uint8     // last input packet written to the VM
	lastAck   solPacket // and its ACK, repeated if the packet comes again
}

func (i *solInstance) close() {
	i.stopOnce.Do(func() {
		close(i.stop)
		i.sub.Close()
	})
}

// sendPacket sends a SOL packet to the console.
func (i *solInstance) sendPacket(p solPacket) {
	pkt, err := sealSessionPayload(i.session, i.flags|PayloadTypeSOL, p.bytes())
	if err != nil {
		log.Printf("IPMI SOL: %v", err)
		return
	}
	addr := i.session.Addr()
	if addr == nil || i.mgr.send == nil {
		return
	}
	i.mgr.send(pkt, addr)
}

// receive handles a packet from the console: an ACK/NACK of an output
// packet goes to run, input is written to the VM and ACKed (once: a
// packet sent again because the ACK was lost is only ACKed again).
func (i *solInstance) receive(p solPacket) []byte {
	if p.AckSeq != 0 {
		select {
		case i.acks <- p:
		default:
		}
	}
	if p.Seq == 0 {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if p.Seq == i.lastInSeq && !i.lastAck.nack() {
		return i.lastAck.bytes()
	}
	// Break, CTS pause, DCD/DSR deassert and flushes (operation bits) are
	// ignored
	ack := solPacket{AckSeq: p.Seq}
	if data := p.Data; len(data) > 0 {
		if len(data) > 255 {
			data = data[:255]
		}
		if _, err := i.console.Write(data); err != nil {
			// The VM is not running
			ack.Status = solStatusNACK | solStatusTransferUnavailable
		} else {
			ack.Accepted = uint8(len(data))
		}
	}
	i.lastInSeq, i.lastAck = p.Seq, ack
	return ack.bytes()
}

// run sends the VM output to the console: one packet at a time, resent
// until ACKed (retryCount times, then dropped), the rest of a partly
// accepted packet in the next one.
func (i *solInstance) run() {
	defer close(i.done)
	var (
		pending  []byte // output not sent yet
		inflight []byte // sent, not ACKed yet
		seq      uint8
		tries    uint8
	)
	accumulate := time.NewTimer(time.Hour)
	accumulate.Stop()
	accumulating := false
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	defer accumulate.Stop()
	defer retry.Stop()

	sendNext := func() {
		accumulate.Stop()
		accumulating = false
		if len(pending) == 0 {
			return
		}
		n := min(len(pending), solMaxData)
		inflight = append([]byte(nil), pending[:n]...)
		pending = pending[n:]
		seq = nextSOLSeq(seq)
		tries = 0
		i.sendPacket(solPacket{Seq: seq, Data: inflight})
		retry.Reset(i.cfg.retryDelay())
	}

	output := i.sub.Output()
	for {
		select {
		case <-i.stop:
			return
		case chunk, ok := <-output:
			if !ok {
				i.mgr.deactivate(i, true, "serial console subscription ended")
				return
			}
			pending = append(pending, chunk...)
			if over := len(pending) - solMaxBuffered; over > 0 {
				pending = pending[over:]
			}
			if inflight != nil {
				break
			}
			if len(pending) >= int(i.cfg.sendThreshold) || i.cfg.accumulateInterval == 0 {
				sendNext()
			} else if !accumulating {
				accumulating = true
				accumulate.Reset(i.cfg.accumulateDelay())
			}
		case <-accumulate.C:
			accumulating = false
			if inflight == nil {
				sendNext()
			}
		case a := <-i.acks:
			if inflight == nil || a.AckSeq != seq {
				break
			}
			if a.nack() {
				// The console cannot take it now; resent after the
				// retry interval
				break
			}
			retry.Stop()
			accepted := min(int(a.Accepted), len(inflight))
			pending = append(inflight[accepted:], pending...)
			inflight = nil
			sendNext()
		case <-retry.C:
			if inflight == nil {
				break
			}
			if tries >= i.cfg.retryCount&0x07 {
				log.Printf("IPMI SOL: no ACK for %d characters, dropped", len(inflight))
				inflight = nil
				sendNext()
				break
			}
			tries++
			i.sendPacket(solPacket{Seq: seq, Data: inflight})
			retry.Reset(i.cfg.retryDelay())
		}
	}
}
