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

// subscriptionQueue is how many output chunks a subscriber may fall behind
// before it is dropped.
const subscriptionQueue = 256

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
type Subscription struct {
	mux  *Mux
	ch   chan []byte
	done chan struct{}
	once sync.Once
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

// end closes the channels once; the caller holds mux.mu.
func (s *Subscription) end() {
	s.once.Do(func() {
		close(s.done)
		close(s.ch)
	})
}

// Mux multiplexes the VM serial console. The zero value is not usable; use
// NewMux.
type Mux struct {
	mu     sync.Mutex
	recent *ring
	subs   map[*Subscription]struct{}
	vm     io.ReadWriteCloser // current VM connection, nil if none

	writeMu sync.Mutex // serializes input so writes do not interleave
}

var _ Console = (*Mux)(nil)

// NewMux returns a Mux keeping the last bufferBytes of output (none if 0).
func NewMux(bufferBytes int) *Mux {
	return &Mux{recent: newRing(bufferBytes), subs: map[*Subscription]struct{}{}}
}

// Subscribe implements Console.
func (m *Mux) Subscribe(replay bool) *Subscription {
	s := &Subscription{mux: m, ch: make(chan []byte, subscriptionQueue+1), done: make(chan struct{})}
	m.mu.Lock()
	defer m.mu.Unlock()
	if replay {
		if b := m.recent.Bytes(); len(b) > 0 {
			s.ch <- b
		}
	}
	m.subs[s] = struct{}{}
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
// whose queue is full so that one slow subscriber does not hold up the
// others (or the VM).
func (m *Mux) publish(p []byte) {
	chunk := append([]byte(nil), p...)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recent.Write(chunk)
	for s := range m.subs {
		select {
		case s.ch <- chunk:
		default:
			log.Printf("serial: dropping a subscriber that does not keep up")
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
