// Package serial shares the VM serial console between its users.
//
// In process management mode QEMU's serial chardev connects to a UNIX
// socket that only qemu-bmc listens on (ServeQEMU). The Mux sends what the
// VM writes to every subscriber and keeps the recent output in a ring
// buffer; any subscriber can write input to the VM. TCP clients on
// SERIAL_ADDR (Server), and later IPMI SOL and the Redfish serial console,
// are subscribers.
package serial

import (
	"errors"
	"io"
	"log"
	"net"
	"sync"
)

// DefaultBufferBytes is the default size of the recent output buffer
// (SERIAL_BUFFER_BYTES).
const DefaultBufferBytes = 256 * 1024

// DefaultQueueBytes is the default for how much output a subscriber may
// fall behind before it is dropped (SERIAL_QUEUE_BYTES).
const DefaultQueueBytes = 4 * 1024 * 1024

// ErrNotConnected is returned by Write when no VM is connected (QEMU is not
// running).
var ErrNotConnected = errors.New("serial: VM not connected")

// Console is the VM serial console as its users see it.
type Console interface {
	// Subscribe starts receiving the VM output. With replay the recent
	// output buffer is sent first, without gap or overlap with what
	// follows.
	Subscribe(replay bool) *Subscription
	// Write sends input to the VM. Input written while no VM is connected
	// is discarded with ErrNotConnected.
	Write(p []byte) (int, error)
	// Recent returns the buffered recent output, oldest first.
	Recent() []byte
}

// Subscription receives the VM output.
//
// What the VM writes is queued per subscription and handed to the reader
// by a goroutine of its own, joining what queued up meanwhile into one
// chunk. How far a subscriber may fall behind is counted in bytes, not
// in writes: a guest that writes a byte at a time does not get a slow
// reader dropped any sooner than one writing in large blocks.
type Subscription struct {
	mux  *Mux
	ch   chan []byte
	done chan struct{}
	once sync.Once

	qmu     sync.Mutex
	pending []queuedOutput // queued output, oldest first
	queued  int            // bytes queued, including the chunk being handed over
	writes  int            // VM writes queued, including the chunk being handed over
	wake    chan struct{}  // pending is not empty
}

// Output delivers the VM output in chunks. It is closed when the
// subscription ends: Close, or the subscriber fell too far behind.
// Chunks are shared between subscribers and must not be modified.
func (s *Subscription) Output() <-chan []byte { return s.ch }

// Done is closed as soon as the subscription ends; unlike Output, it does
// not wait for queued chunks to be read.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Close ends the subscription.
func (s *Subscription) Close() {
	s.mux.mu.Lock()
	defer s.mux.mu.Unlock()
	s.mux.unsubscribe(s)
}

// queuedOutput is output queued for a subscriber; the replay is not counted
// against the limit.
type queuedOutput struct {
	b       []byte
	counted bool
}

// end ends the subscription once; Output is closed by the pump. The
// caller holds mux.mu.
func (s *Subscription) end() {
	s.once.Do(func() { close(s.done) })
}

// enqueue queues p, unless it would bring the queue over limit (> 0)
// bytes; then it returns false and the queue size.
func (s *Subscription) enqueue(p []byte, limit int, counted bool) (ok bool, queued, writes int) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if counted && limit > 0 && s.queued+len(p) > limit {
		return false, s.queued, s.writes
	}
	s.pending = append(s.pending, queuedOutput{p, counted})
	if counted {
		s.queued += len(p)
		s.writes++
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return true, s.queued, s.writes
}

// pump hands the queued output to the reader, joined into one chunk per
// handover, until the subscription ends; then it closes Output. What is
// still queued then is dropped.
func (s *Subscription) pump() {
	defer close(s.ch)
	for {
		select {
		case <-s.wake:
		case <-s.done:
			return
		}
		s.qmu.Lock()
		pending := s.pending
		s.pending = nil
		s.qmu.Unlock()
		if len(pending) == 0 {
			continue
		}
		n, countedBytes, countedWrites := 0, 0, 0
		for _, q := range pending {
			n += len(q.b)
			if q.counted {
				countedBytes += len(q.b)
				countedWrites++
			}
		}
		chunk := pending[0].b
		if len(pending) > 1 {
			chunk = make([]byte, 0, n)
			for _, q := range pending {
				chunk = append(chunk, q.b...)
			}
		}
		select {
		case s.ch <- chunk:
		case <-s.done:
			return
		}
		s.qmu.Lock()
		s.queued -= countedBytes
		s.writes -= countedWrites
		s.qmu.Unlock()
	}
}

// Mux multiplexes the VM serial console. The zero value is not usable; use
// NewMux.
type Mux struct {
	mu     sync.Mutex
	recent *ring
	subs   map[*Subscription]struct{}
	limit  int                // bytes a subscriber may fall behind before it is dropped
	vm     io.ReadWriteCloser // current VM connection, nil if none

	writeMu sync.Mutex // serializes input so writes do not interleave
}

var _ Console = (*Mux)(nil)

// NewMux returns a Mux keeping the last bufferBytes of output (none if 0).
func NewMux(bufferBytes int) *Mux {
	return &Mux{recent: newRing(bufferBytes), subs: map[*Subscription]struct{}{}, limit: DefaultQueueBytes}
}

// SetQueueBytes sets how many bytes of output a subscriber may fall behind
// before it is dropped (DefaultQueueBytes if n <= 0). The recent output
// sent on Subscribe with replay is not counted.
func (m *Mux) SetQueueBytes(n int) {
	if n <= 0 {
		n = DefaultQueueBytes
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.limit = n
}

// QueueBytes returns the limit set by SetQueueBytes.
func (m *Mux) QueueBytes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.limit
}

// Subscribe implements Console.
func (m *Mux) Subscribe(replay bool) *Subscription {
	s := &Subscription{mux: m, ch: make(chan []byte), done: make(chan struct{}), wake: make(chan struct{}, 1)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if replay {
		if b := m.recent.Bytes(); len(b) > 0 {
			s.enqueue(b, 0, false)
		}
	}
	m.subs[s] = struct{}{}
	go s.pump()
	return s
}

// Subscribers returns the number of active subscriptions.
func (m *Mux) Subscribers() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subs)
}

func (m *Mux) unsubscribe(s *Subscription) {
	delete(m.subs, s)
	s.end()
}

// Recent implements Console.
func (m *Mux) Recent() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recent.Bytes()
}

// Write implements Console.
func (m *Mux) Write(p []byte) (int, error) {
	m.mu.Lock()
	vm := m.vm
	m.mu.Unlock()
	if vm == nil {
		return 0, ErrNotConnected
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	return vm.Write(p)
}

// Connected reports whether a VM is connected.
func (m *Mux) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.vm != nil
}

// publish buffers p and queues it to every subscriber, dropping those
// that would fall more than the queue limit behind so that one slow
// subscriber does not hold up the others (or the VM).
func (m *Mux) publish(p []byte) {
	chunk := append([]byte(nil), p...)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recent.Write(chunk)
	for s := range m.subs {
		if ok, queued, writes := s.enqueue(chunk, m.limit, true); !ok {
			log.Printf("serial: dropping a subscriber that does not keep up (%d bytes in %d writes queued, %d more would exceed SERIAL_QUEUE_BYTES=%d)", queued, writes, len(chunk), m.limit)
			m.unsubscribe(s)
		}
	}
}

// Attach makes conn the VM connection, replacing (and closing) the
// previous one, and reads the VM output from it until it fails. Attach
// returns when conn is closed.
func (m *Mux) Attach(conn io.ReadWriteCloser) {
	m.mu.Lock()
	old := m.vm
	m.vm = conn
	m.mu.Unlock()
	if old != nil {
		old.Close()
	}

	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			m.publish(buf[:n])
		}
		if err != nil {
			break
		}
	}

	m.mu.Lock()
	if m.vm == conn {
		m.vm = nil
	}
	m.mu.Unlock()
	conn.Close()
}

// ServeQEMU accepts QEMU's serial chardev connections on l (a new QEMU
// connects at every start) and attaches them. It returns when l fails.
func (m *Mux) ServeQEMU(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go m.Attach(conn)
	}
}
