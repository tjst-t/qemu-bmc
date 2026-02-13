# Integration Test Design

## Overview

Docker Compose based integration test infrastructure that tests qemu-bmc against a real QEMU instance.
Validates the full chain: Client (curl/ipmitool) → qemu-bmc (Redfish/IPMI) → QMP → real QEMU.

## Architecture

### Docker Compose (3 containers)

| Service | Role | Details |
|---------|------|---------|
| `qemu` | Real QEMU VM | `qemu-system-x86_64` minimal config, no OS, QMP socket only |
| `bmc` | qemu-bmc | Connects to QMP socket, exposes Redfish (443) + IPMI (623) |
| `test` | Test runner | Go test + ipmitool, runs against bmc ports |

`qemu` and `bmc` share a volume for the QMP UNIX socket.

### File Structure

```
Makefile                          # Build, test, integration targets
integration/
  docker-compose.yml              # 3-container orchestration
  Dockerfile.test                 # Go + ipmitool test container
  integration_test.go             # Test cases (//go:build integration)
  testutil.go                     # Helpers (HTTP client, ipmitool wrapper, polling)
```

## QEMU Configuration

```bash
qemu-system-x86_64 \
  -machine q35 \
  -nographic \
  -no-shutdown \
  -qmp unix:/shared/qmp.sock,server,nowait \
  -drive id=cd0,if=none,media=cdrom \
  -device ide-cd,drive=cd0,bus=ide.1 \
  -m 128
```

- `-no-shutdown`: Keeps QEMU process alive after powerdown (enables OFF→ON test cycle)
- `-drive` + `-device`: CD-ROM for VirtualMedia InsertMedia/EjectMedia tests
- No disk, no OS: QMP responses and state changes are sufficient for validation

## Test Cases

### Redfish Tests (HTTP client)

| Test | Description |
|------|-------------|
| `TestRedfish_ServiceRoot` | GET `/redfish/v1` returns 200 with OData fields |
| `TestRedfish_PowerState` | GET `/Systems/1` shows initial PowerState `On` |
| `TestRedfish_PowerOffOn` | GracefulShutdown → Off → ForceOn → On |
| `TestRedfish_ForceRestart` | ForceRestart → remains On |
| `TestRedfish_BootOverride` | PATCH boot source → GET confirms, ETag works |
| `TestRedfish_VirtualMedia` | InsertMedia → Inserted=true → EjectMedia → Inserted=false |
| `TestRedfish_BasicAuth` | No auth → 401 |

### IPMI Tests (ipmitool exec)

| Test | Description |
|------|-------------|
| `TestIPMI_ChassisStatus` | `chassis status` shows power on |
| `TestIPMI_PowerOffOn` | `power off` → off → `power on` → on |
| `TestIPMI_PowerCycle` | `power cycle` succeeds |
| `TestIPMI_BootDevice` | `bootdev pxe` → `bootparam get 5` confirms PXE |
| `TestIPMI_LANPLUS` | `-I lanplus` (RMCP+) commands work |

### Cross-Protocol Tests

| Test | Description |
|------|-------------|
| `TestCross_IPMIPowerOff_RedfishVerify` | IPMI power off → Redfish shows Off |
| `TestCross_RedfishBoot_IPMIVerify` | Redfish set boot device → ipmitool confirms |

## Test Helpers (testutil.go)

```go
func waitForBMCReady(redfishURL, ipmiHost string, timeout time.Duration) error
func waitForPowerState(client *RedfishClient, state string, timeout time.Duration) error

type RedfishClient struct { ... }
func (c *RedfishClient) Get(path string) (*http.Response, error)
func (c *RedfishClient) Patch(path string, body any) (*http.Response, error)
func (c *RedfishClient) Post(path string, body any) (*http.Response, error)

func runIPMITool(host, user, pass string, args ...string) (string, error)      // IPMI 1.5
func runIPMIToolLAN(host, user, pass string, args ...string) (string, error)   // RMCP+ lanplus
```

Key: `system_powerdown` is async (ACPI signal), so `waitForPowerState` polls until state changes.

## Makefile Targets

```makefile
# Build & test
make build            # Go binary build
make test             # Unit tests
make test-race        # With race detector
make vet              # Static analysis
make coverage         # Coverage report

# Docker
make docker-build     # Docker image build

# Integration
make integration      # docker compose run --rm test && docker compose down
make integration-up   # Start infra only (for manual debugging)
make integration-down # Cleanup

# CI
make ci               # vet + test-race + integration
make clean            # Remove artifacts
```

## Environment Variables (test config)

| Variable | Default | Purpose |
|----------|---------|---------|
| `BMC_REDFISH_URL` | `https://bmc:443` | Redfish endpoint |
| `BMC_IPMI_HOST` | `bmc` | IPMI host |
| `BMC_USER` | `admin` | Auth username |
| `BMC_PASS` | `password` | Auth password |
