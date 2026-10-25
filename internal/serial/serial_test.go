package serial

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRing(t *testing.T) {
	r := newRing(8)
	assert.Empty(t, r.Bytes())
	r.Write([]byte("abc"))
	assert.Equal(t, "abc", string(r.Bytes()))
	r.Write([]byte("defgh"))
	assert.Equal(t, "abcdefgh", string(r.Bytes()))
	r.Write([]byte("ij"))
	assert.Equal(t, "cdefghij", string(r.Bytes()))
	r.Write([]byte("klmno"))
	assert.Equal(t, "hijklmno", string(r.Bytes()))
	r.Write([]byte("0123456789"))
	assert.Equal(t, "23456789", string(r.Bytes()))
	r.Write([]byte("x"))
	assert.Equal(t, "3456789x", string(r.Bytes()))

	none := newRing(0)
	none.Write([]byte("abc"))
	assert.Empty(t, none.Bytes())
}

// fakeQEMU connects to the mux's QEMU socket like QEMU's serial chardev.
type fakeQEMU struct {
	conn net.Conn
	r    *bufio.Reader
}

func startQEMUSocket(t *testing.T, m *Mux) string {
	t.Helper()
	// t.TempDir() can exceed the UNIX socket path limit
	dir, err := os.MkdirTemp("/tmp", "serial-test-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "serial.sock")
	l, err := ListenUnix(path)
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	go m.ServeQEMU(l)
	return path
}

func connectQEMU(t *testing.T, m *Mux, path string) *fakeQEMU {
	t.Helper()
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	require.Eventually(t, m.Connected, 2*time.Second, 5*time.Millisecond)
	return &fakeQEMU{conn: conn, r: bufio.NewReader(conn)}
}

func recvUntil(t *testing.T, sub *Subscription, want string) string {
	t.Helper()
	var got bytes.Buffer
	timeout := time.After(2 * time.Second)
	for !strings.Contains(got.String(), want) {
		select {
		case b, ok := <-sub.Output():
			if !ok {
				t.Fatalf("subscription ended, got %q", got.String())
			}
			got.Write(b)
		case <-timeout:
			t.Fatalf("timed out waiting for %q, got %q", want, got.String())
		}
	}
	return got.String()
}

func readUntil(t *testing.T, r io.Reader, want string) string {
	t.Helper()
	if c, ok := r.(net.Conn); ok {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		defer c.SetReadDeadline(time.Time{})
	}
	var got bytes.Buffer
	buf := make([]byte, 1024)
	for !strings.Contains(got.String(), want) {
		n, err := r.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			t.Fatalf("waiting for %q: %v (got %q)", want, err, got.String())
		}
	}
	return got.String()
}

func TestMux_OutputToAllSubscribersAndInputFromAny(t *testing.T) {
	m := NewMux(1024)
	q := connectQEMU(t, m, startQEMUSocket(t, m))

	a := m.Subscribe(false)
	b := m.Subscribe(false)
	defer a.Close()
	defer b.Close()

	q.conn.Write([]byte("login: "))
	recvUntil(t, a, "login: ")
	recvUntil(t, b, "login: ")

	_, err := m.Write([]byte("root\r"))
	require.NoError(t, err)
	_, err = m.Write([]byte("ls\r"))
	require.NoError(t, err)
	readUntil(t, q.conn, "root\rls\r")
}

func TestMux_ReplaySendsRecentOutputFirst(t *testing.T) {
	m := NewMux(8)
	q := connectQEMU(t, m, startQEMUSocket(t, m))

	watcher := m.Subscribe(false)
	defer watcher.Close()
	q.conn.Write([]byte("0123456789"))
	recvUntil(t, watcher, "0123456789")
	assert.Equal(t, "23456789", string(m.Recent()))

	replay := m.Subscribe(true)
	defer replay.Close()
	plain := m.Subscribe(false)
	defer plain.Close()
	q.conn.Write([]byte("AB"))
	assert.Equal(t, "23456789AB", recvUntil(t, replay, "AB"))
	assert.Equal(t, "AB", recvUntil(t, plain, "AB"))
}

func TestMux_SlowSubscriberIsDropped(t *testing.T) {
	m := NewMux(16)
	q := connectQEMU(t, m, startQEMUSocket(t, m))

	slow := m.Subscribe(false) // never read
	fast := m.Subscribe(false)
	defer fast.Close()

	// Each write is read as its own chunk since fast waits for it
	for i := 0; i <= subscriptionQueue+1; i++ {
		q.conn.Write([]byte{'x'})
		recvUntil(t, fast, "x")
	}
	select {
	case <-slow.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("slow subscriber was not dropped")
	}
	assert.Equal(t, 1, m.Subscribers())

	q.conn.Write([]byte("still here"))
	recvUntil(t, fast, "still here")
}

func TestMux_ReconnectsWhenQEMURestarts(t *testing.T) {
	m := NewMux(1024)
	path := startQEMUSocket(t, m)
	sub := m.Subscribe(false)
	defer sub.Close()

	first := connectQEMU(t, m, path)
	first.conn.Write([]byte("first boot"))
	recvUntil(t, sub, "first boot")

	// QEMU exits: input is discarded until the next one connects
	first.conn.Close()
	require.Eventually(t, func() bool { return !m.Connected() }, 2*time.Second, 5*time.Millisecond)
	_, err := m.Write([]byte("lost"))
	assert.ErrorIs(t, err, ErrNotConnected)

	second := connectQEMU(t, m, path)
	second.conn.Write([]byte("second boot"))
	recvUntil(t, sub, "second boot")
	_, err = m.Write([]byte("hello"))
	require.NoError(t, err)
	readUntil(t, second.conn, "hello")
	assert.Contains(t, string(m.Recent()), "first bootsecond boot")
}

func TestMux_NewQEMUConnectionReplacesOld(t *testing.T) {
	m := NewMux(1024)
	path := startQEMUSocket(t, m)
	old := connectQEMU(t, m, path)
	// Let the first connection be attached before the second one
	_, err := m.Write([]byte("a"))
	require.NoError(t, err)
	readUntil(t, old.conn, "a")

	cur, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer cur.Close()

	// The old connection is closed by the mux
	old.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = io.ReadAll(old.conn)
	require.NoError(t, err)

	_, err = m.Write([]byte("b"))
	require.NoError(t, err)
	readUntil(t, cur, "b")
}

func startTCP(t *testing.T, m *Mux, maxConns int, replay bool) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	go NewServer(m, maxConns, replay).Serve(l)
	return l.Addr().String()
}

func dialTCP(t *testing.T, m *Mux, addr string, subscribers int) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	require.Eventually(t, func() bool { return m.Subscribers() == subscribers }, 2*time.Second, 5*time.Millisecond)
	return c
}

func TestServer_TwoClientsShareTheConsole(t *testing.T) {
	m := NewMux(1024)
	q := connectQEMU(t, m, startQEMUSocket(t, m))
	addr := startTCP(t, m, 8, false)

	a := dialTCP(t, m, addr, 1)
	b := dialTCP(t, m, addr, 2)

	q.conn.Write([]byte("debian login: "))
	readUntil(t, a, "debian login: ")
	readUntil(t, b, "debian login: ")

	a.Write([]byte("root\r"))
	readUntil(t, q.conn, "root\r")
	q.conn.Write([]byte("root@debian:~# "))
	readUntil(t, a, "root@debian:~# ")
	readUntil(t, b, "root@debian:~# ")

	b.Write([]byte("uname\r"))
	readUntil(t, q.conn, "uname\r")

	// A client leaving does not affect the other
	a.Close()
	require.Eventually(t, func() bool { return m.Subscribers() == 1 }, 2*time.Second, 5*time.Millisecond)
	q.conn.Write([]byte("Linux"))
	readUntil(t, b, "Linux")
}

func TestServer_Replay(t *testing.T) {
	m := NewMux(1024)
	q := connectQEMU(t, m, startQEMUSocket(t, m))
	w := m.Subscribe(false)
	defer w.Close()
	q.conn.Write([]byte("earlier output"))
	recvUntil(t, w, "earlier output")

	withReplay := dialTCP(t, m, startTCP(t, m, 8, true), 2)
	without := dialTCP(t, m, startTCP(t, m, 8, false), 3)
	q.conn.Write([]byte("|now"))
	assert.Equal(t, "earlier output|now", readUntil(t, withReplay, "|now"))
	assert.Equal(t, "|now", readUntil(t, without, "|now"))
}

func TestServer_ConnectionLimit(t *testing.T) {
	m := NewMux(1024)
	addr := startTCP(t, m, 2, false)
	dialTCP(t, m, addr, 1)
	second := dialTCP(t, m, addr, 2)

	third, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer third.Close()
	third.SetReadDeadline(time.Now().Add(2 * time.Second))
	out, err := io.ReadAll(third)
	require.NoError(t, err)
	assert.Contains(t, string(out), "too many serial console connections")

	// A free slot is reused
	second.Close()
	require.Eventually(t, func() bool { return m.Subscribers() == 1 }, 2*time.Second, 5*time.Millisecond)
	dialTCP(t, m, addr, 2)
}

func TestServer_SlowClientIsDisconnected(t *testing.T) {
	m := NewMux(16)
	q := connectQEMU(t, m, startQEMUSocket(t, m))
	addr := startTCP(t, m, 8, false)

	slow := dialTCP(t, m, addr, 1).(*net.TCPConn)
	slow.SetReadBuffer(4096)
	fast := dialTCP(t, m, addr, 2)

	// The slow client never reads; the VM output fills its socket buffers
	// and then its queue. The fast one reads each chunk before the next.
	chunk := append(bytes.Repeat([]byte("y"), 4095), '\n')
	fast.SetReadDeadline(time.Now().Add(30 * time.Second))
	r := bufio.NewReader(fast)
	for i := 0; m.Subscribers() == 2; i++ {
		require.Less(t, i, 8192, "slow client was not dropped")
		_, err := q.conn.Write(chunk)
		require.NoError(t, err)
		line, err := r.ReadString('\n')
		require.NoError(t, err)
		require.Len(t, line, len(chunk))
	}

	// The fast client is still served
	q.conn.Write([]byte("still here\n"))
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "still here\n", line)

	// The slow client's connection was reset
	slow.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.Copy(io.Discard, slow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection reset")
}

func TestServer_InputWithoutVMIsDiscarded(t *testing.T) {
	m := NewMux(1024)
	c := dialTCP(t, m, startTCP(t, m, 8, false), 1)
	_, err := c.Write([]byte("typed while off\r"))
	require.NoError(t, err)

	// The connection stays usable and gets the output of the next QEMU
	q := connectQEMU(t, m, startQEMUSocket(t, m))
	q.conn.Write([]byte("booting"))
	readUntil(t, c, "booting")
}
