//go:build integration

package integration

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type testEnv struct {
	RedfishURL string
	IPMIHost   string
	User       string
	Pass       string
}

func loadTestEnv() testEnv {
	return testEnv{
		RedfishURL: getEnvDefault("BMC_REDFISH_URL", "http://localhost:8443"),
		IPMIHost:   getEnvDefault("BMC_IPMI_HOST", "localhost"),
		User:       getEnvDefault("BMC_USER", "admin"),
		Pass:       getEnvDefault("BMC_PASS", "password"),
	}
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type RedfishClient struct {
	baseURL    string
	user, pass string
	client     *http.Client
}

func NewRedfishClient(baseURL, user, pass string) *RedfishClient {
	return &RedfishClient{
		baseURL: baseURL,
		user:    user,
		pass:    pass,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

func (c *RedfishClient) Get(path string) (*http.Response, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	return c.client.Do(req)
}

func (c *RedfishClient) GetNoAuth(path string) (*http.Response, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.client.Do(req)
}

func (c *RedfishClient) Post(path string, body any) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("Content-Type", "application/json")
	return c.client.Do(req)
}

func (c *RedfishClient) Patch(path string, body any) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("PATCH", c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("Content-Type", "application/json")
	return c.client.Do(req)
}

func (c *RedfishClient) PatchWithETag(path string, body any, etag string) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("PATCH", c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", etag)
	return c.client.Do(req)
}

func readJSON(resp *http.Response) (map[string]any, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("JSON decode error: %w (body: %s)", err, string(body))
	}
	return result, nil
}

// runIPMITool executes ipmitool with RMCP+ (lanplus) interface and cipher suite 3
func runIPMITool(host, user, pass string, args ...string) (string, error) {
	cmdArgs := []string{"-I", "lanplus", "-C", "3", "-H", host, "-U", user, "-P", pass}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("ipmitool", cmdArgs...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// runIPMIToolLANPlus is an alias for runIPMITool (both use lanplus)
func runIPMIToolLANPlus(host, user, pass string, args ...string) (string, error) {
	return runIPMITool(host, user, pass, args...)
}

func waitForBMCReady(env testEnv, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)

	for time.Now().Before(deadline) {
		resp, err := client.Get("/redfish/v1")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				conn, err := net.DialTimeout("udp", env.IPMIHost+":623", 2*time.Second)
				if err == nil {
					conn.Close()
					return nil
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("BMC not ready within %s", timeout)
}

func waitForPowerState(client *RedfishClient, expected string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get("/redfish/v1/Systems/1")
		if err == nil {
			data, err := readJSON(resp)
			if err == nil {
				if ps, ok := data["PowerState"].(string); ok && ps == expected {
					return nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("PowerState did not become %q within %s", expected, timeout)
}

func ensurePowerOn(client *RedfishClient) error {
	resp, err := client.Get("/redfish/v1/Systems/1")
	if err != nil {
		return err
	}
	data, err := readJSON(resp)
	if err != nil {
		return err
	}
	if ps, ok := data["PowerState"].(string); ok && ps == "On" {
		return nil
	}
	_, err = client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "On",
	})
	if err != nil {
		return err
	}
	return waitForPowerState(client, "On", 10*time.Second)
}
