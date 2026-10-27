package qmp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// CommandTimeout bounds how long a QMP command (writing it and reading its
// response) may take. A QEMU that stops answering QMP would otherwise block
// every caller for good: power actions, and the power state that Redfish
// and IPMI report. When it expires the connection is closed, so later
// commands fail with ErrNotConnected (a forced power off then kills QEMU)
// until Connect is called again.
var CommandTimeout = 15 * time.Second

// ErrNotConnected is returned when a command is issued on a disconnected client.
var ErrNotConnected = errors.New("QMP client not connected")

// qmpClient implements the Client interface
type qmpClient struct {
	socketPath string
	conn       net.Conn
	scanner    *bufio.Scanner
	connected  bool
	dropped    bool      // the connection was closed after a failed command
	retryAfter time.Time // no reconnect attempt before this
	mu         sync.Mutex
}

// NewClient creates a new QMP client connected to the given UNIX socket
func NewClient(socketPath string) (Client, error) {
	c := NewDisconnectedClient(socketPath)
	if err := c.Connect(); err != nil {
		return nil, err
	}
	return c, nil
}

// NewDisconnectedClient creates a QMP client that is not yet connected.
// Call Connect() to establish the connection.
func NewDisconnectedClient(socketPath string) Client {
	return &qmpClient{
		socketPath: socketPath,
	}
}

// Connect establishes (or re-establishes) the QMP connection.
func (c *qmpClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked()
}

// connectLocked is Connect. Caller must hold c.mu.
func (c *qmpClient) connectLocked() error {
	c.dropped = false

	// Close existing connection if any
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
		c.scanner = nil
		c.connected = false
	}

	conn, err := net.DialTimeout("unix", c.socketPath, CommandTimeout)
	if err != nil {
		return fmt.Errorf("connecting to QMP socket: %w", err)
	}

	c.conn = conn
	c.scanner = bufio.NewScanner(conn)
	conn.SetDeadline(time.Now().Add(CommandTimeout))

	// Read QMP greeting
	if !c.scanner.Scan() {
		conn.Close()
		c.conn = nil
		c.scanner = nil
		return fmt.Errorf("reading QMP greeting: connection closed")
	}

	var greeting qmpGreeting
	if err := json.Unmarshal(c.scanner.Bytes(), &greeting); err != nil {
		conn.Close()
		c.conn = nil
		c.scanner = nil
		return fmt.Errorf("parsing QMP greeting: %w", err)
	}

	// Send qmp_capabilities
	if err := c.executeLocked("qmp_capabilities", nil); err != nil {
		conn.Close()
		c.conn = nil
		c.scanner = nil
		return fmt.Errorf("QMP capabilities negotiation: %w", err)
	}

	conn.SetDeadline(time.Time{})
	c.connected = true
	return nil
}

// checkConnected fails with ErrNotConnected unless the client is connected.
// A connection closed after a failed command is re-established first, at
// most once per 2×CommandTimeout (in between, commands fail at once instead
// of waiting on a QEMU that does not answer), so that a QEMU that answers
// again is used again. Caller
// must hold c.mu.
func (c *qmpClient) checkConnected() error {
	if !c.connected && c.dropped && !time.Now().Before(c.retryAfter) {
		if err := c.connectLocked(); err != nil {
			c.dropped = true
			c.retryAfter = time.Now().Add(2 * CommandTimeout)
			return fmt.Errorf("%w (reconnecting: %v)", ErrNotConnected, err)
		}
	}
	if !c.connected {
		return ErrNotConnected
	}
	return nil
}

// executeLocked sends a command and reads the response. Caller must hold c.mu.
func (c *qmpClient) executeLocked(command string, arguments interface{}) error {
	raw, err := c.roundTripLocked(command, arguments)
	if err != nil {
		return err
	}
	var resp qmpResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("QMP error: %s: %s", resp.Error.Class, resp.Error.Desc)
	}
	return nil
}

// roundTripLocked sends a command and returns its raw response, skipping
// async events, within CommandTimeout. On an I/O error or timeout the
// connection is closed (the stream is no longer in step with QEMU). Caller
// must hold c.mu.
func (c *qmpClient) roundTripLocked(command string, arguments interface{}) (json.RawMessage, error) {
	data, err := json.Marshal(qmpCommand{Execute: command, Arguments: arguments})
	if err != nil {
		return nil, fmt.Errorf("marshaling command: %w", err)
	}
	data = append(data, '\n')

	c.conn.SetDeadline(time.Now().Add(CommandTimeout))
	raw, err := c.exchangeLocked(data)
	if err != nil {
		c.dropLocked()
		return nil, fmt.Errorf("QMP %s: %w", command, err)
	}
	c.conn.SetDeadline(time.Time{})
	return raw, nil
}

func (c *qmpClient) exchangeLocked(data []byte) (json.RawMessage, error) {
	if _, err := c.conn.Write(data); err != nil {
		return nil, fmt.Errorf("writing command: %w", err)
	}
	for {
		if !c.scanner.Scan() {
			if err := c.scanner.Err(); err != nil {
				return nil, fmt.Errorf("reading response: %w", err)
			}
			return nil, fmt.Errorf("reading response: connection closed")
		}
		raw := make([]byte, len(c.scanner.Bytes()))
		copy(raw, c.scanner.Bytes())

		var resp qmpResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("parsing response: %w", err)
		}
		// Skip async events (they have "event" field, not "return"/"error")
		if resp.Event != "" {
			continue
		}
		return json.RawMessage(raw), nil
	}
}

// dropLocked closes the connection after a failed exchange. Caller must
// hold c.mu.
func (c *qmpClient) dropLocked() {
	if c.conn != nil {
		c.conn.Close()
	}
	c.conn = nil
	c.scanner = nil
	c.connected = false
	c.dropped = true
	c.retryAfter = time.Now().Add(2 * CommandTimeout)
}

func (c *qmpClient) execute(command string, arguments interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.checkConnected(); err != nil {
		return err
	}

	return c.executeLocked(command, arguments)
}

func (c *qmpClient) executeWithResponse(command string, arguments interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.checkConnected(); err != nil {
		return nil, err
	}
	return c.roundTripLocked(command, arguments)
}

func (c *qmpClient) QueryStatus() (Status, error) {
	raw, err := c.executeWithResponse("query-status", nil)
	if err != nil {
		return "", err
	}

	var resp qmpStatusResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parsing status response: %w", err)
	}

	return Status(resp.Return.Status), nil
}

func (c *qmpClient) SystemPowerdown() error {
	return c.execute("system_powerdown", nil)
}

func (c *qmpClient) SystemReset() error {
	return c.execute("system_reset", nil)
}

func (c *qmpClient) Stop() error {
	return c.execute("stop", nil)
}

func (c *qmpClient) Cont() error {
	return c.execute("cont", nil)
}

func (c *qmpClient) Quit() error {
	return c.execute("quit", nil)
}

func (c *qmpClient) InjectNMI() error {
	return c.execute("inject-nmi", nil)
}

func (c *qmpClient) QueryBlock() ([]BlockInfo, error) {
	raw, err := c.executeWithResponse("query-block", nil)
	if err != nil {
		return nil, err
	}

	var resp qmpQueryBlockResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parsing query-block response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("QMP error: %s: %s", resp.Error.Class, resp.Error.Desc)
	}
	return resp.Return, nil
}

func (c *qmpClient) BlockdevChangeMedium(id, filename, format string) error {
	return c.execute("blockdev-change-medium", blockdevChangeMediumArgs{
		ID:       id,
		Filename: filename,
		Format:   format,
	})
}

func (c *qmpClient) BlockdevOpenTray(id string, force bool) error {
	return c.execute("blockdev-open-tray", blockdevOpenTrayArgs{ID: id, Force: force})
}

func (c *qmpClient) BlockdevCloseTray(id string) error {
	return c.execute("blockdev-close-tray", blockdevIDArgs{ID: id})
}

func (c *qmpClient) BlockdevRemoveMedium(id string) error {
	return c.execute("blockdev-remove-medium", blockdevIDArgs{ID: id})
}

func (c *qmpClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.connected = false
	c.dropped = false
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
