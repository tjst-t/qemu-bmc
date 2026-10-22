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
  qemu/                        # QEMU process management (args, process lifecycle)
  machine/                     # VM state management (wraps QMP + ProcessManager)
  redfish/                     # HTTP server with gorilla/mux, 15+ endpoints
  ipmi/                        # UDP server + VM chardev server, RMCP/RMCP+, RAKP auth, chassis commands
  bmc/                         # BMC configuration state (users, LAN, channels)
  config/                      # Environment variable config
docker/
  Dockerfile                   # Multi-stage build (Go builder + Debian runtime)
  entrypoint.sh                # Environment variables → QEMU args conversion
  setup-network.sh             # TAP/bridge network setup for VM passthrough
tests/                         # Bash-based container integration tests (89 tests)
  run_tests.sh                 # Test runner (category-based)
  test_helper.sh               # Assertions, container mgmt, IPMI/Redfish helpers
  test_*.sh                    # Test categories (container, ipmi, redfish, power, boot, etc.)
containerlab/
  example.yml                  # 2 nodes booting their own disk, BMC published on the host
  mgmt-network.yml             # Separate BMC mgmt / VM data networks + operator container
  README.md                    # Deploy and verification steps
integration/                   # Go-based integration tests (legacy mode)
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

# Download noVNC static files (required once on a fresh clone; go build/test
# fail with an embed error on internal/novnc/static without them)
make download-novnc

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

# Docker build
make docker-build

# Container integration tests (quick smoke tests)
make container-test

# Container integration tests (all 89 tests across 9 categories)
make container-test-all

# Run specific test categories
./tests/run_tests.sh --build container ipmi redfish power boot
```

## Environment Variables

### BMC Configuration (Go binary)

| Variable | Default | Description |
|----------|---------|-------------|
| `QMP_SOCK` | `/var/run/qemu/qmp.sock` | QMP socket path |
| `IPMI_USER` | `admin` | IPMI/Redfish auth username |
| `IPMI_PASS` | `password` | IPMI/Redfish auth password |
| `REDFISH_PORT` | `443` | Redfish HTTPS port |
| `IPMI_PORT` | `623` | IPMI UDP port |
| `SERIAL_ADDR` | `localhost:9002` | Process management mode only: QEMU guest serial console is exposed as a TCP server at this address. SOL is not implemented (nothing in qemu-bmc connects to it). Port 9002 collides with the `VM_IPMI_ADDR` example — use another port when enabling both |
| `TLS_CERT` | (auto-generated) | TLS certificate path; if unset, a self-signed ECDSA cert is generated automatically |
| `TLS_KEY` | (auto-generated) | TLS key path; if unset, generated together with `TLS_CERT` |
| `VM_BOOT_MODE` | `bios` | Default boot mode |
| `VM_IPMI_ADDR` | (empty, disabled) | VM IPMI chardev listen address (e.g., `:9002`) |
| `POWER_ON_AT_START` | `false` | Power on VM automatically at container start |
| `GRACEFUL_TIMEOUT` | `120` | Seconds GracefulShutdown/GracefulRestart (IPMI soft off) report `PoweringOff` before going back to `On` if the guest does not power off. GracefulRestart reports `PoweringOn` while powering back on |
| `VM_MEMORY` | `2048` | Legacy mode only: VM memory reported as Systems/1 `MemorySummary.TotalSystemMemoryGiB`. Parsed like QEMU `-m` (`2048` = MiB, `4G`, `512M`, `size=...`); omitted if unparsable. In process management mode the QEMU `-m` argument is used |
| `VM_CPUS` | `2` | Legacy mode only: CPU count reported as Systems/1 `ProcessorSummary.Count`. Parsed like QEMU `-smp`; omitted if unparsable. In process management mode the QEMU `-smp` argument is used |
| `SYSTEM_UUID` | (derived) | Fixed system UUID: passed to QEMU as `-uuid`, returned as Redfish `Systems/1` `UUID` and IPMI Get System GUID. If unset (process mode): `-uuid` in QEMU args, else `SYSTEM_UUID_FILE`, else a v5 UUID derived from the hostname (then written to `SYSTEM_UUID_FILE`). Legacy mode reports a UUID only when `SYSTEM_UUID` is set |
| `SYSTEM_UUID_FILE` | (empty; entrypoint.sh sets `<VM_DISK dir>/system-uuid`) | File holding the persisted derived UUID |
| `VM_MACS` | (derived) | Comma-separated per-NIC MACs in QEMU command-line order (empty entry = derive). Process mode: injected as `mac=` (`macaddr=` for `-net nic`) into NICs without one; a conflicting user `mac=` is rejected. Unset entries are `52:54:` + 4 bytes of SHA-256(system UUID, NIC index), so they are stable per node and differ between nodes. Reported as Systems/1 EthernetInterfaces. Legacy mode reports NICs only from `VM_MACS` |
| `VMEDIA_DIR` | `/vm/vmedia` (if `/vm` is writable, else `/tmp/vmedia`) | Where VirtualMedia http(s) images are downloaded; the previous download is deleted on replace/eject |
| `VMEDIA_INSECURE_TLS` | `true` | Skip certificate verification for https VirtualMedia downloads |
| `VMEDIA_TIMEOUT` | `600` | Seconds a power on/reset (Redfish and IPMI) waits for a VirtualMedia download in progress |

### Container Configuration (entrypoint.sh)

| Variable | Default | Description |
|----------|---------|-------------|
| `VM_MEMORY` | `2048` | VM memory in MB |
| `VM_CPUS` | `2` | Number of VM CPUs |
| `ENABLE_KVM` | `true` | Use KVM acceleration (falls back to TCG) |
| `VNC_PORT` | `5900` | VNC display port |
| `VM_DISK` | `/vm/disk.qcow2` | VM disk image path |
| `VM_CDROM` | (empty) | ISO initially inserted in the VirtualMedia CD drive (qemu-bmc always adds an empty CD drive; `-cdrom` is turned into it) |
| `VM_BOOT` | `c` | Boot device (c=disk, d=cdrom, n=network) |
| `VM_BOOT_MENU_TIMEOUT` | `0` | Boot menu timeout in ms (0=disabled) |
| `VM_NETWORKS` | (empty) | Comma-separated host interfaces for TAP passthrough (default: eth2+). Needs `/dev/net/tun` (`--device /dev/net/tun`); entrypoint creates it with mknod if missing, else exits with an error |
| `QEMU_EXTRA_ARGS` | (empty) | Additional QEMU arguments |
| `DEBUG` | `false` | Enable debug output |

## Redfish Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/redfish`, `/redfish/v1` | Version document, ServiceRoot (no auth; root has SessionService and Links.Sessions) |
| GET/PATCH | `/redfish/v1/SessionService` | SessionTimeout (default 1800 s, 30–86400) |
| GET/POST | `/redfish/v1/SessionService/Sessions` | Session collection / log in (POST needs no auth; 201 with X-Auth-Token and Location; 401 bad credentials; 503 over 64 sessions) |
| GET/DELETE | `/redfish/v1/SessionService/Sessions/{id}` | Session / log out (204). Sessions are in memory and expire after SessionTimeout idle |
| GET | `/redfish/v1/Systems` | System collection |
| GET | `/redfish/v1/Systems/1` | Computer system (PowerState, Boot, MemorySummary, ProcessorSummary, EthernetInterfaces, Actions, Status, Links.ManagedBy → Managers/1, Links.Chassis → Chassis/1) |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces` | NIC collection (one per guest NIC, command-line order) |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces/{n}` | NIC (MACAddress, LinkStatus: LinkUp while the VM runs) |
| PATCH | `/redfish/v1/Systems/1` | Boot device override (with ETag support) |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | Power control |
| GET | `/redfish/v1/Managers` | Manager collection |
| GET | `/redfish/v1/Managers/1` | BMC manager (ManagerType BMC, FirmwareVersion = qemu-bmc version via SetFirmwareVersion, Status, Links.ManagerForServers → Systems/1, Links.ManagerInChassis → Chassis/1) |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia collection |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia resource (ImageName omitted when nothing is inserted; Image stays as "") |
| POST | `.../VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia` | Insert ISO (container path `/...` or `file://`, or http(s) URL downloaded in the background with optional UserName/Password Basic auth; 204, 400 if missing). Kept while off, attached at QEMU start; power on/reset during a download runs once it ends (reports PoweringOn meanwhile), a failed download ejects |
| POST | `.../VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia` | Eject ISO (204) |
| GET | `/redfish/v1/Chassis` | Chassis collection |
| GET | `/redfish/v1/Chassis/1` | Chassis resource (PowerState same as Systems/1 incl. PoweringOff/PoweringOn, Status, Links.ComputerSystems → Systems/1, Links.ManagedBy → Managers/1) |

Middleware: auth (X-Auth-Token session or Basic; GET /redfish, GET /redfish/v1 and POST Sessions are exempt), ETag (If-Match), trailing slash normalization

## IPMI Commands

| Command | NetFn | Cmd | Description |
|---------|-------|-----|-------------|
| Get Device ID | App (0x06) | 0x01 | Static BMC identity |
| Get System GUID | App | 0x37 | System UUID (IPMI encoding, LSB first) |
| Get Channel Auth Capabilities | App | 0x38 | Auth type negotiation |
| Set Session Privilege | App | 0x3B | Privilege level |
| Close Session | App | 0x3C | Session teardown |
| Get Channel Cipher Suites | App | 0x54 | Cipher suite list (suite 3 only) |
| Get Chassis Status | Chassis (0x00) | 0x01 | Power state query |
| Chassis Control | Chassis | 0x02 | Power on/off/cycle/reset, soft off (ACPI power button, never forced); diag pulse is a no-op |
| Chassis Identify | Chassis | 0x04 | Identify LED (log only) |
| Set Boot Options | Chassis | 0x08 | Boot device override |
| Get Boot Options | Chassis | 0x09 | Boot device query |

Supports: RMCP v1.0, IPMI 1.5, RMCP+ with RAKP HMAC-SHA1 auth, AES-CBC-128 encryption

## VM IPMI (In-Band)

Supports QEMU's `ipmi-bmc-extern` device for in-band IPMI communication from the guest OS via KCS interface. This enables MaaS commissioning scripts to configure BMC users, LAN settings, and channel access from within the VM.

### QEMU Configuration

```bash
qemu-system-x86_64 \
  -chardev socket,id=ipmi0,host=localhost,port=9002,reconnect=10 \
  -device ipmi-bmc-extern,id=bmc0,chardev=ipmi0 \
  -device isa-ipmi-kcs,bmc=bmc0 \
  ...
```

### Supported In-Band Commands

| Command | NetFn | Cmd | Description |
|---------|-------|-----|-------------|
| Get User Access | App (0x06) | 0x44 | Read user privileges |
| Get User Name | App (0x06) | 0x46 | Read username |
| Set User Name | App (0x06) | 0x45 | Set username |
| Set User Password | App (0x06) | 0x47 | Set password |
| Set User Access | App (0x06) | 0x43 | Set user privileges |
| Get Channel Access | App (0x06) | 0x41 | Read channel settings |
| Set Channel Access | App (0x06) | 0x40 | Write channel settings |
| Get Channel Info | App (0x06) | 0x42 | Channel information |
| Get LAN Config | Transport (0x0C) | 0x02 | Read LAN parameters |
| Set LAN Config | Transport (0x0C) | 0x01 | Write LAN parameters |

Plus all existing chassis and app commands (Get Device ID, Chassis Status, Boot Options, etc.)

Uses the OpenIPMI VM wire protocol over TCP with framing (0xA0/0xA1), byte escaping (0xAA), and two's complement checksums.

## GitHub Pages

- Site URL: https://tjst-t.github.io/qemu-bmc/
- `gh-pages` branch (orphan) に `index.html` を配置
- main ブランチから `docs/gh-pages/` に git submodule として参照可能
- サイト更新手順:
  1. `docs/gh-pages/index.html` を編集
  2. submodule 内でコミット&プッシュ:
     ```bash
     cd docs/gh-pages
     git add -A && git commit -m "Update site" && git push origin gh-pages
     cd ../..
     ```
  3. main 側で submodule 参照を更新:
     ```bash
     git add docs/gh-pages && git commit -m "chore: update gh-pages submodule ref"
     ```

## Notes for Claude Code

- `grep`, `tail`, `head` are not available as bash commands in the Claude Code sandbox — use the dedicated Read/Grep tools instead

## Development Guidelines

- TDD: Write tests first, verify RED, implement, verify GREEN, refactor
- All Redfish JSON must include `@odata.type`, `@odata.id`, `@odata.context` for gofish compatibility
- ETag: Return in both header and `@odata.etag` field; accept PATCH without If-Match (MAAS compat)
- Trailing slashes: Both `/path` and `/path/` must work identically
- IPMI checksums use two's complement: `0x100 - (sum & 0xFF)`
- All multi-byte IPMI fields are little-endian
