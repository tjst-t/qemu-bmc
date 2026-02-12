package qmp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
)

// qmpClient implements the Client interface
type qmpClient struct {
	conn    net.Conn
	scanner *bufio.Scanner
	mu      sync.Mutex
}

// NewClient creates a new QMP client connected to the given UNIX socket
func NewClient(socketPath string) (Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connecting to QMP socket: %w", err)
	}

	c := &qmpClient{
		conn:    conn,
		scanner: bufio.NewScanner(conn),
	}

	// Read QMP greeting
	if !c.scanner.Scan() {
		conn.Close()
		return nil, fmt.Errorf("reading QMP greeting: connection closed")
	}

	var greeting qmpGreeting
	if err := json.Unmarshal(c.scanner.Bytes(), &greeting); err != nil {
		conn.Close()
		return nil, fmt.Errorf("parsing QMP greeting: %w", err)
	}

	// Send qmp_capabilities
	if err := c.execute("qmp_capabilities", nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("QMP capabilities negotiation: %w", err)
	}

	return c, nil
}

func (c *qmpClient) execute(command string, arguments interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cmd := qmpCommand{
		Execute:   command,
		Arguments: arguments,
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("marshaling command: %w", err)
	}

	data = append(data, '\n')
	if _, err := c.conn.Write(data); err != nil {
		return fmt.Errorf("writing command: %w", err)
	}

	if !c.scanner.Scan() {
		return fmt.Errorf("reading response: connection closed")
	}

	var resp qmpResponse
	if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	if resp.Error != nil {
		return fmt.Errorf("QMP error: %s: %s", resp.Error.Class, resp.Error.Desc)
	}

	return nil
}

func (c *qmpClient) executeWithResponse(command string, arguments interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cmd := qmpCommand{
		Execute:   command,
		Arguments: arguments,
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshaling command: %w", err)
	}

	data = append(data, '\n')
	if _, err := c.conn.Write(data); err != nil {
		return nil, fmt.Errorf("writing command: %w", err)
	}

	if !c.scanner.Scan() {
		return nil, fmt.Errorf("reading response: connection closed")
	}

	return json.RawMessage(c.scanner.Bytes()), nil
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

func (c *qmpClient) Quit() error {
	return c.execute("quit", nil)
}

func (c *qmpClient) BlockdevChangeMedium(device, filename string) error {
	return c.execute("blockdev-change-medium", blockdevChangeMediumArgs{
		Device:   device,
		Filename: filename,
	})
}

func (c *qmpClient) BlockdevRemoveMedium(device string) error {
	return c.execute("blockdev-remove-medium", blockdevRemoveMediumArgs{
		Device: device,
	})
}

func (c *qmpClient) Close() error {
	return c.conn.Close()
}
