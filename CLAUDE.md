# CLAUDE.md - qemu-bmc

## Project Overview

Go single binary that controls QEMU VMs via Redfish API (HTTPS) and IPMI over LAN (UDP:623).
Replaces [docker-qemu-bmc](https://github.com/tjst-t/docker-qemu-bmc) (shell scripts + ipmi_sim + supervisord).

Compatible with: MAAS, Tinkerbell/Rufio, Cybozu placemat

## Architecture

```
cmd/qemu-bmc/main.go          # Entrypoint, goroutines for Redfish + IPMI
internal/
  qmp/                         # QMP socket client (interface + implementation)
  machine/                     # VM state management (wraps QMP)
  redfish/                     # HTTP server with gorilla/mux, 15+ endpoints
  ipmi/                        # UDP server, RMCP/RMCP+, RAKP auth, chassis commands
  config/                      # Environment variable config
```

### Key Design Patterns

- `MachineInterface` - shared interface used by both Redfish and IPMI servers for testability
- `qmp.Client` - interface for QMP communication, mock implementation in `testutil_test.go`
- All tests use `testify/assert` and `testify/require`
- Mock QMP server (`qmp/testutil_test.go`) simulates full QMP protocol over UNIX socket
- Each Redfish endpoint handler is in a separate file (`handler_*.go`)

## Build & Test

```bash
# Go is not in default PATH in this environment
export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin

# Build
go build ./cmd/qemu-bmc

# Test (all packages)
go test ./... -count=1

# Test with race detector
go test ./... -race -count=1

# Test specific package
go test ./internal/redfish/... -v
go test ./internal/ipmi/... -v
go test ./internal/qmp/... -v

# Static analysis
go vet ./...

# Coverage
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `QMP_SOCK` | `/var/run/qemu/qmp.sock` | QMP socket path |
| `IPMI_USER` | `admin` | IPMI/Redfish auth username |
| `IPMI_PASS` | `password` | IPMI/Redfish auth password |
| `REDFISH_PORT` | `443` | Redfish HTTPS port |
| `IPMI_PORT` | `623` | IPMI UDP port |
| `SERIAL_ADDR` | `localhost:9002` | SOL bridge target |
| `TLS_CERT` | (empty, falls back to HTTP) | TLS certificate path |
| `TLS_KEY` | (empty) | TLS key path |
| `VM_BOOT_MODE` | `bios` | Default boot mode |

## Redfish Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/redfish/v1` | ServiceRoot |
| GET | `/redfish/v1/Systems` | System collection |
| GET | `/redfish/v1/Systems/1` | Computer system (PowerState, Boot, Actions) |
| PATCH | `/redfish/v1/Systems/1` | Boot device override (with ETag support) |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | Power control |
| GET | `/redfish/v1/Managers` | Manager collection |
| GET | `/redfish/v1/Managers/1` | BMC manager |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia collection |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia resource |
| POST | `.../VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia` | Insert ISO |
| POST | `.../VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia` | Eject ISO |
| GET | `/redfish/v1/Chassis` | Chassis collection |
| GET | `/redfish/v1/Chassis/1` | Chassis resource |

Middleware: Basic Auth, ETag (If-Match), trailing slash normalization

## IPMI Commands

| Command | NetFn | Cmd | Description |
|---------|-------|-----|-------------|
| Get Device ID | App (0x06) | 0x01 | Static BMC identity |
| Get Channel Auth Capabilities | App | 0x38 | Auth type negotiation |
| Set Session Privilege | App | 0x3B | Privilege level |
| Close Session | App | 0x3C | Session teardown |
| Get Chassis Status | Chassis (0x00) | 0x01 | Power state query |
| Chassis Control | Chassis | 0x02 | Power on/off/cycle/reset |
| Chassis Identify | Chassis | 0x04 | Identify LED (log only) |
| Set Boot Options | Chassis | 0x08 | Boot device override |
| Get Boot Options | Chassis | 0x09 | Boot device query |

Supports: RMCP v1.0, IPMI 1.5, RMCP+ with RAKP HMAC-SHA1 auth, AES-CBC-128 encryption

## Development Guidelines

- TDD: Write tests first, verify RED, implement, verify GREEN, refactor
- All Redfish JSON must include `@odata.type`, `@odata.id`, `@odata.context` for gofish compatibility
- ETag: Return in both header and `@odata.etag` field; accept PATCH without If-Match (MAAS compat)
- Trailing slashes: Both `/path` and `/path/` must work identically
- IPMI checksums use two's complement: `0x100 - (sum & 0xFF)`
- All multi-byte IPMI fields are little-endian
