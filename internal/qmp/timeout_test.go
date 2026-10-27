package qmp

import (
	"bufio"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangingQMPServer answers the greeting and qmp_capabilities, then stops
// answering while hang is set (a QEMU whose main loop is stuck).
type hangingQMPServer struct {
	l     net.Listener
	hang  atomic.Bool
	conns atomic.Int32
}

func newHangingQMPServer(t *testing.T, path string) *hangingQMPServer {
	t.Helper()
	os.Remove(path)
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	s := &hangingQMPServer{l: l}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			s.conns.Add(1)
			go s.handle(conn)
		}
	}()
	return s
}

func (s *hangingQMPServer) handle(conn net.Conn) {
	defer conn.Close()
	conn.Write([]byte(`{"QMP": {"version": {}, "capabilities": []}}` + "\n"))
	sc := bufio.NewScanner(conn)
	first := true
	for sc.Scan() {
		if !first && s.hang.Load() {
			continue // never answer
		}
		first = false
		conn.Write([]byte(`{"return": {"status": "running", "running": true}}` + "\n"))
	}
}

func withCommandTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := CommandTimeout
	CommandTimeout = d
	t.Cleanup(func() { CommandTimeout = old })
}

func TestCommandTimeout_HungQEMU(t *testing.T) {
	withCommandTimeout(t, 200*time.Millisecond)
	path := testSocketPath(t)
	srv := newHangingQMPServer(t, path)

	client, err := NewClient(path)
	require.NoError(t, err)
	defer client.Close()

	status, err := client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, status)

	srv.hang.Store(true)
	start := time.Now()
	err = client.Quit()
	require.Error(t, err, "a command QEMU does not answer must fail")
	assert.Less(t, time.Since(start), 2*time.Second)

	// Within the retry interval the client fails at once without dialing
	start = time.Now()
	_, err = client.QueryStatus()
	assert.ErrorIs(t, err, ErrNotConnected)
	assert.Less(t, time.Since(start), 100*time.Millisecond)
}

func TestCommandTimeout_Reconnects(t *testing.T) {
	withCommandTimeout(t, 200*time.Millisecond)
	path := testSocketPath(t)
	srv := newHangingQMPServer(t, path)

	client, err := NewClient(path)
	require.NoError(t, err)
	defer client.Close()

	srv.hang.Store(true)
	_, err = client.QueryStatus()
	require.Error(t, err)

	// QEMU answers again: the first command after the retry interval
	// reconnects
	srv.hang.Store(false)
	_, err = client.QueryStatus()
	assert.ErrorIs(t, err, ErrNotConnected)
	time.Sleep(2*CommandTimeout + 50*time.Millisecond)
	status, err := client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, status)
	assert.Equal(t, int32(2), srv.conns.Load())
}

func TestClose_DoesNotReconnect(t *testing.T) {
	path := testSocketPath(t)
	newHangingQMPServer(t, path)
	client, err := NewClient(path)
	require.NoError(t, err)
	require.NoError(t, client.Close())
	_, err = client.QueryStatus()
	assert.ErrorIs(t, err, ErrNotConnected)
}
