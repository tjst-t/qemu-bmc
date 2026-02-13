# Integration Test Infrastructure - Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create Docker Compose based integration tests that validate qemu-bmc against real QEMU.

**Architecture:** 3-container Docker Compose (qemu, bmc, test). Go test code with HTTP client for Redfish and ipmitool exec for IPMI. Makefile for all build/test orchestration.

**Tech Stack:** Go 1.23, Docker Compose, qemu-system-x86_64, ipmitool, testify

---

### Task 1: Makefile

**Files:**
- Create: `Makefile`

**Step 1: Create Makefile with all targets**

```makefile
.PHONY: build test test-race vet coverage docker-build integration integration-up integration-down ci clean

BINARY := qemu-bmc
DOCKER_IMAGE := qemu-bmc

build:
	go build -o $(BINARY) ./cmd/qemu-bmc

test:
	go test ./... -count=1

test-race:
	go test ./... -race -count=1

vet:
	go vet ./...

coverage:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

docker-build:
	docker build -t $(DOCKER_IMAGE) .

integration:
	docker compose -f integration/docker-compose.yml run --rm --build test
	docker compose -f integration/docker-compose.yml down -v

integration-up:
	docker compose -f integration/docker-compose.yml up --build -d qemu bmc
	@echo "QEMU + BMC running. Use 'make integration-down' to stop."

integration-down:
	docker compose -f integration/docker-compose.yml down -v

ci: vet test-race integration

clean:
	rm -f $(BINARY) coverage.out
	docker compose -f integration/docker-compose.yml down -v --rmi local 2>/dev/null || true
```

**Step 2: Verify Makefile works for unit tests**

Run: `make test`
Expected: All unit tests pass (same as `go test ./... -count=1`)

**Step 3: Commit**

```bash
git add Makefile
git commit -m "feat: add Makefile for build, test, and integration targets"
```

---

### Task 2: Docker Compose Infrastructure

**Files:**
- Create: `integration/docker-compose.yml`
- Create: `integration/Dockerfile.test`

**Step 1: Create docker-compose.yml**

```yaml
services:
  qemu:
    image: debian:bookworm-slim
    command:
      - /bin/bash
      - -c
      - |
        apt-get update && apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils
        mkdir -p /shared
        exec qemu-system-x86_64 \
          -machine q35 \
          -nographic \
          -no-shutdown \
          -qmp unix:/shared/qmp.sock,server,nowait \
          -drive id=cd0,if=none,media=cdrom \
          -device ide-cd,drive=cd0,bus=ide.1 \
          -m 128
    volumes:
      - qmp-sock:/shared
    healthcheck:
      test: ["CMD-SHELL", "test -S /shared/qmp.sock"]
      interval: 1s
      timeout: 5s
      retries: 10

  bmc:
    build:
      context: ..
      dockerfile: Dockerfile
    entrypoint: ["/usr/local/bin/qemu-bmc"]
    environment:
      QMP_SOCK: /shared/qmp.sock
      IPMI_USER: admin
      IPMI_PASS: password
      REDFISH_PORT: "443"
      IPMI_PORT: "623"
    volumes:
      - qmp-sock:/shared
    depends_on:
      qemu:
        condition: service_healthy
    ports:
      - "8443:443"
      - "8623:623/udp"

  test:
    build:
      context: ..
      dockerfile: integration/Dockerfile.test
    environment:
      BMC_REDFISH_URL: http://bmc:443
      BMC_IPMI_HOST: bmc
      BMC_USER: admin
      BMC_PASS: password
    depends_on:
      - bmc

volumes:
  qmp-sock:
```

Note: `BMC_REDFISH_URL` uses `http://` because qemu-bmc falls back to HTTP when no TLS cert is configured. In the Docker Compose test environment, no TLS cert is mounted.

**Step 2: Create Dockerfile.test**

```dockerfile
FROM golang:1.23-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
    ipmitool \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

CMD ["go", "test", "-tags=integration", "-v", "-count=1", "-timeout=120s", "./integration/..."]
```

**Step 3: Commit**

```bash
git add integration/docker-compose.yml integration/Dockerfile.test
git commit -m "feat: add Docker Compose infrastructure for integration tests"
```

---

### Task 3: Test Helpers (testutil.go)

**Files:**
- Create: `integration/testutil.go`

**Step 1: Create testutil.go with all helpers**

```go
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

// test environment config
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

// RedfishClient is an HTTP client for Redfish API testing
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

// readJSON reads response body and decodes JSON into a map
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

// runIPMITool executes ipmitool with IPMI 1.5 interface
func runIPMITool(host, user, pass string, args ...string) (string, error) {
	cmdArgs := []string{"-I", "lan", "-H", host, "-U", user, "-P", pass}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("ipmitool", cmdArgs...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// runIPMIToolLANPlus executes ipmitool with RMCP+ (lanplus) interface
func runIPMIToolLANPlus(host, user, pass string, args ...string) (string, error) {
	cmdArgs := []string{"-I", "lanplus", "-H", host, "-U", user, "-P", pass}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("ipmitool", cmdArgs...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// waitForBMCReady waits until both Redfish and IPMI endpoints are responsive
func waitForBMCReady(env testEnv, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)

	for time.Now().Before(deadline) {
		// Check Redfish
		resp, err := client.Get("/redfish/v1")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				// Check IPMI
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

// waitForPowerState polls Redfish until PowerState matches expected value
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

// ensurePowerOn makes sure the VM is powered on before a test
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
	// Power on
	_, err = client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "On",
	})
	if err != nil {
		return err
	}
	return waitForPowerState(client, "On", 10*time.Second)
}
```

**Step 2: Commit**

```bash
git add integration/testutil.go
git commit -m "feat: add integration test helpers (Redfish client, ipmitool wrapper, polling)"
```

---

### Task 4: Redfish Integration Tests

**Files:**
- Create: `integration/redfish_test.go`

**Step 1: Create redfish_test.go**

```go
//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedfish_ServiceRoot(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)

	resp, err := client.Get("/redfish/v1")
	require.NoError(t, err)
	data, err := readJSON(resp)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "#ServiceRoot.v1_0_0.ServiceRoot", data["@odata.type"])
	assert.Equal(t, "/redfish/v1", data["@odata.id"])
	assert.NotEmpty(t, data["Name"])
}

func TestRedfish_BasicAuth(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)

	resp, err := client.GetNoAuth("/redfish/v1/Systems")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestRedfish_PowerState(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	resp, err := client.Get("/redfish/v1/Systems/1")
	require.NoError(t, err)
	data, err := readJSON(resp)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "On", data["PowerState"])
	assert.Equal(t, "QEMU Virtual Machine", data["Name"])
}

func TestRedfish_PowerOffOn(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	// GracefulShutdown
	resp, err := client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "GracefulShutdown",
	})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Wait for power off
	require.NoError(t, waitForPowerState(client, "Off", 10*time.Second))

	// Power on
	resp, err = client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "On",
	})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Wait for power on
	require.NoError(t, waitForPowerState(client, "On", 10*time.Second))
}

func TestRedfish_ForceRestart(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	resp, err := client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "ForceRestart",
	})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Should still be On after restart
	require.NoError(t, waitForPowerState(client, "On", 10*time.Second))
}

func TestRedfish_BootOverride(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	// Get current ETag
	resp, err := client.Get("/redfish/v1/Systems/1")
	require.NoError(t, err)
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	require.NotEmpty(t, etag)

	// PATCH boot override
	patchBody := map[string]any{
		"Boot": map[string]string{
			"BootSourceOverrideTarget":  "Pxe",
			"BootSourceOverrideEnabled": "Once",
		},
	}
	resp, err = client.PatchWithETag("/redfish/v1/Systems/1", patchBody, etag)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify boot override is set
	resp, err = client.Get("/redfish/v1/Systems/1")
	require.NoError(t, err)
	data, err := readJSON(resp)
	require.NoError(t, err)

	boot, ok := data["Boot"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Pxe", boot["BootSourceOverrideTarget"])
	assert.Equal(t, "Once", boot["BootSourceOverrideEnabled"])
}

func TestRedfish_VirtualMedia(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)

	// Insert media
	resp, err := client.Post(
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
		map[string]string{"Image": "http://example.com/test.iso"},
	)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify inserted
	resp, err = client.Get("/redfish/v1/Managers/1/VirtualMedia/CD1")
	require.NoError(t, err)
	data, err := readJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, true, data["Inserted"])
	assert.Equal(t, "http://example.com/test.iso", data["Image"])

	// Eject media
	resp, err = client.Post(
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia",
		map[string]string{},
	)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify ejected
	resp, err = client.Get("/redfish/v1/Managers/1/VirtualMedia/CD1")
	require.NoError(t, err)
	data, err = readJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, false, data["Inserted"])
}
```

**Step 2: Commit**

```bash
git add integration/redfish_test.go
git commit -m "feat: add Redfish integration tests (service root, auth, power, boot, media)"
```

---

### Task 5: IPMI Integration Tests

**Files:**
- Create: `integration/ipmi_test.go`

**Step 1: Create ipmi_test.go**

```go
//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIPMI_ChassisStatus(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "status")
	require.NoError(t, err, "ipmitool output: %s", out)
	assert.Contains(t, out, "System Power         : on")
}

func TestIPMI_PowerOffOn(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	// Power off
	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "power", "off")
	require.NoError(t, err, "ipmitool output: %s", out)

	// Wait for power off via Redfish (more reliable than ipmitool polling)
	require.NoError(t, waitForPowerState(client, "Off", 10*time.Second))

	// Verify via ipmitool
	out, err = runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "status")
	require.NoError(t, err, "ipmitool output: %s", out)
	assert.Contains(t, out, "System Power         : off")

	// Power on
	out, err = runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "power", "on")
	require.NoError(t, err, "ipmitool output: %s", out)

	require.NoError(t, waitForPowerState(client, "On", 10*time.Second))

	// Verify via ipmitool
	out, err = runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "status")
	require.NoError(t, err, "ipmitool output: %s", out)
	assert.Contains(t, out, "System Power         : on")
}

func TestIPMI_PowerCycle(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "power", "cycle")
	require.NoError(t, err, "ipmitool output: %s", out)

	// After cycle, should be back on
	require.NoError(t, waitForPowerState(client, "On", 10*time.Second))
}

func TestIPMI_BootDevice(t *testing.T) {
	env := loadTestEnv()

	// Set boot device to PXE
	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "bootdev", "pxe")
	require.NoError(t, err, "ipmitool output: %s", out)

	// Verify boot device
	out, err = runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "bootparam", "get", "5")
	require.NoError(t, err, "ipmitool output: %s", out)
	assert.True(t, strings.Contains(out, "PXE") || strings.Contains(out, "pxe"),
		"expected PXE in output: %s", out)
}

func TestIPMI_LANPlus(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	// RMCP+ chassis status
	out, err := runIPMIToolLANPlus(env.IPMIHost, env.User, env.Pass, "chassis", "status")
	require.NoError(t, err, "ipmitool lanplus output: %s", out)
	assert.Contains(t, out, "System Power         : on")

	// RMCP+ boot device
	out, err = runIPMIToolLANPlus(env.IPMIHost, env.User, env.Pass, "chassis", "bootdev", "pxe")
	require.NoError(t, err, "ipmitool lanplus output: %s", out)
}
```

**Step 2: Commit**

```bash
git add integration/ipmi_test.go
git commit -m "feat: add IPMI integration tests (chassis status, power, boot device, RMCP+)"
```

---

### Task 6: Cross-Protocol Tests

**Files:**
- Create: `integration/cross_test.go`

**Step 1: Create cross_test.go**

```go
//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCross_IPMIPowerOff_RedfishVerify(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)
	require.NoError(t, ensurePowerOn(client))

	// Power off via IPMI
	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "power", "off")
	require.NoError(t, err, "ipmitool output: %s", out)

	// Verify via Redfish
	require.NoError(t, waitForPowerState(client, "Off", 10*time.Second))

	// Restore power
	_, err = client.Post("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{
		"ResetType": "On",
	})
	require.NoError(t, err)
	require.NoError(t, waitForPowerState(client, "On", 10*time.Second))
}

func TestCross_RedfishBoot_IPMIVerify(t *testing.T) {
	env := loadTestEnv()
	client := NewRedfishClient(env.RedfishURL, env.User, env.Pass)

	// Set boot device via Redfish
	patchBody := map[string]any{
		"Boot": map[string]string{
			"BootSourceOverrideTarget":  "Pxe",
			"BootSourceOverrideEnabled": "Once",
		},
	}
	resp, err := client.Patch("/redfish/v1/Systems/1", patchBody)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	// Verify via IPMI
	out, err := runIPMITool(env.IPMIHost, env.User, env.Pass, "chassis", "bootparam", "get", "5")
	require.NoError(t, err, "ipmitool output: %s", out)
	assert.True(t, strings.Contains(out, "PXE") || strings.Contains(out, "pxe"),
		"expected PXE in output: %s", out)
}
```

**Step 2: Commit**

```bash
git add integration/cross_test.go
git commit -m "feat: add cross-protocol integration tests (IPMI↔Redfish verification)"
```

---

### Task 7: Test Setup (TestMain) and Smoke Test

**Files:**
- Create: `integration/main_test.go`

**Step 1: Create main_test.go with TestMain for BMC readiness check**

```go
//go:build integration

package integration

import (
	"log"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	env := loadTestEnv()
	log.Println("Waiting for BMC to be ready...")
	if err := waitForBMCReady(env, 30*time.Second); err != nil {
		log.Fatalf("BMC failed to become ready: %v", err)
	}
	log.Println("BMC is ready, running tests...")
	os.Exit(m.Run())
}
```

**Step 2: Commit**

```bash
git add integration/main_test.go
git commit -m "feat: add TestMain with BMC readiness check for integration tests"
```

---

### Task 8: End-to-End Verification

**Step 1: Run full integration test suite**

Run: `make integration`
Expected: All containers build, QEMU starts, BMC connects, tests pass, containers are cleaned up.

**Step 2: Verify individual make targets**

Run: `make vet && make test`
Expected: Static analysis and unit tests still pass (integration tests excluded by build tag).

**Step 3: Final commit if any fixes needed**

Fix any issues discovered during verification and commit.
