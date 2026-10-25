package serial

import (
	"errors"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// DefaultMaxConns is the default limit of simultaneous TCP connections.
const DefaultMaxConns = 8

// writeTimeout bounds a single write to a TCP client; a client that does
// not read for that long is disconnected.
const writeTimeout = 30 * time.Second

// Server serves the serial console to raw TCP clients (SERIAL_ADDR): each
// connection gets the VM output and its input goes to the VM.
type Server struct {
	console  Console
	maxConns int
	replay   bool

	mu    sync.Mutex
	conns int
}

// NewServer returns a server for console accepting up to maxConns
// connections. With replay a new connection first gets the recent output
// buffer.
func NewServer(console Console, maxConns int, replay bool) *Server {
	return &Server{console: console, maxConns: maxConns, replay: replay}
}

// Serve accepts connections on l until it fails.
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		if !s.acquire() {
			log.Printf("serial: refusing %s: %d connections already", conn.RemoteAddr(), s.maxConns)
			conn.SetWriteDeadline(time.Now().Add(time.Second))
			conn.Write([]byte("qemu-bmc: too many serial console connections\r\n"))
			conn.Close()
			continue
		}
		go func() {
			defer s.release()
			s.handle(conn)
		}()
	}
}

func (s *Server) acquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxConns > 0 && s.conns >= s.maxConns {
		return false
	}
	s.conns++
	return true
}

func (s *Server) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns--
}

func (s *Server) handle(conn net.Conn) {
	log.Printf("serial: %s connected", conn.RemoteAddr())
	sub := s.console.Subscribe(s.replay)

	// Output: the subscription ending (client gone, or too slow) closes
	// the connection, which also unblocks a write in progress. The
	// connection is reset: what is still queued for a slow client is
	// dropped instead of trickling out after the close.
	go func() {
		<-sub.Done()
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetLinger(0)
		}
		conn.Close()
	}()
	go func() {
		defer sub.Close()
		for chunk := range sub.Output() {
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		}
	}()

	// Input; while QEMU is not running it is discarded
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if _, werr := s.console.Write(buf[:n]); werr != nil && !errors.Is(werr, ErrNotConnected) {
				log.Printf("serial: writing to the VM: %v", werr)
			}
		}
		if err != nil {
			break
		}
	}
	sub.Close()
	log.Printf("serial: %s disconnected", conn.RemoteAddr())
}

// ListenUnix listens on the UNIX socket path for QEMU's serial chardev,
// replacing a stale socket, and makes it accessible to the owner only.
func ListenUnix(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
