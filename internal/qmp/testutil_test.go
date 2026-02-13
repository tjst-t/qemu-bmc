package qmp

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
)

// mockQMPServer simulates a QMP server over a UNIX socket
type mockQMPServer struct {
	listener    net.Listener
	status      Status
	lastCommand string
	mu          sync.Mutex
	t           *testing.T
	done        chan struct{}
}

func newMockQMPServer(t *testing.T, socketPath string) *mockQMPServer {
	t.Helper()
	// Remove any existing socket
	os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to create mock QMP server: %v", err)
	}

	m := &mockQMPServer{
		listener: listener,
		status:   StatusRunning,
		t:        t,
		done:     make(chan struct{}),
	}

	go m.serve()
	return m
}

func (m *mockQMPServer) serve() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			select {
			case <-m.done:
				return
			default:
				return
			}
		}
		go m.handleConnection(conn)
	}
}

func (m *mockQMPServer) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Send QMP greeting
	greeting := `{"QMP": {"version": {"qemu": {"micro": 0, "minor": 2, "major": 8}}, "capabilities": []}}` + "\n"
	conn.Write([]byte(greeting))

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()

		var cmd qmpCommand
		if err := json.Unmarshal([]byte(line), &cmd); err != nil {
			continue
		}

		m.mu.Lock()
		m.lastCommand = cmd.Execute
		m.mu.Unlock()

		var response string
		switch cmd.Execute {
		case "qmp_capabilities":
			response = `{"return": {}}` + "\n"
		case "query-status":
			m.mu.Lock()
			status := m.status
			running := status == StatusRunning
			m.mu.Unlock()
			resp := map[string]interface{}{
				"return": map[string]interface{}{
					"running": running,
					"status":  string(status),
				},
			}
			data, _ := json.Marshal(resp)
			response = string(data) + "\n"
		case "system_powerdown", "system_reset", "quit", "stop", "cont":
			m.mu.Lock()
			switch cmd.Execute {
			case "quit":
				m.status = StatusShutdown
			case "stop":
				m.status = StatusPaused
			case "cont":
				m.status = StatusRunning
			}
			m.mu.Unlock()
			response = `{"return": {}}` + "\n"
		case "blockdev-change-medium", "blockdev-remove-medium":
			response = `{"return": {}}` + "\n"
		default:
			response = `{"return": {}}` + "\n"
		}

		conn.Write([]byte(response))
	}
}

func (m *mockQMPServer) SetStatus(status Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = status
}

func (m *mockQMPServer) LastCommand() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastCommand
}

func (m *mockQMPServer) Close() {
	close(m.done)
	m.listener.Close()
}
