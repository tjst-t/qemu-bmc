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
tests/                         # Bash-based container integration tests (103 tests)
  run_tests.sh                 # Test runner (category-based)
  test_helper.sh               # Assertions, container mgmt, IPMI/Redfish helpers
  test_*.sh                    # Test categories (container, ipmi, redfish, power, boot, etc.)
  ironic/run.sh                # Ironic end-to-end deploy test (make ironic-test; manual, not in CI)
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

# Container integration tests (all 103 tests across 9 categories)
make container-test-all

# Run specific test categories
./tests/run_tests.sh --build container ipmi redfish power boot

# Ironic end-to-end test (real Ironic deploys Debian; Redfish vmedia/https, IPMI PXE UEFI+BIOS)
make ironic-test                       # or IRONIC_FLOWS=redfish-vmedia, IRONIC_ARGS="--image ghcr.io/..."
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
| `VM_MANUFACTURER` | `QEMU` | System manufacturer: Systems/1 + Chassis/1 `Manufacturer`; process mode also SMBIOS type 1/2/3 manufacturer (`qemu.SMBIOSArgs`, `,` escaped as `,,`; a user `-smbios type=N` wins and that type is not added, Redfish still reports the env values) |
| `VM_MODEL` | `qemu-bmc` | Systems/1 + Chassis/1 `Model`; SMBIOS type 1/2 product |
| `VM_SERIAL_NUMBER` | (derived) | Systems/1 + Chassis/1 `SerialNumber`; SMBIOS type 1/2/3 serial. Unset: `QBMC-` + first 10 hex digits of the system UUID, upper case (`sysuuid.UUID.SerialNumber`); none in legacy mode without `SYSTEM_UUID` |
| `VM_SKU` | (empty) | Systems/1 `SKU` (omitted when empty); SMBIOS type 1/3 sku |
| `VM_SECURE_BOOT` | `false` | Initial Redfish `SecureBootEnable` (process mode with OVMF pflash only; otherwise qemu-bmc exits with an error, legacy mode ignores it). PATCHed values are kept in memory only and revert to this on restart |
| `VMEDIA_DIR` | `/vm/vmedia` (if `/vm` is writable, else `/tmp/vmedia`) | Where VirtualMedia http(s) images (`cd1-*.iso`) and the UEFI HTTP boot image (`httpboot-*.iso`) are downloaded; the previous download is deleted on replace/eject, leftovers at startup |
| `VMEDIA_INSECURE_TLS` | `true` | Skip certificate verification for https VirtualMedia downloads |
| `VMEDIA_TIMEOUT` | `600` | Seconds a power on/reset (Redfish and IPMI) waits for a VirtualMedia (or, for UefiHttp, HttpBootUri) download in progress |

### Container Configuration (entrypoint.sh)

| Variable | Default | Description |
|----------|---------|-------------|
| `VM_MEMORY` | `2048` | VM memory in MB |
| `VM_CPUS` | `2` | Number of VM CPUs |
| `ENABLE_KVM` | `true` | Use KVM acceleration (falls back to TCG) |
| `VNC_PORT` | `5900` | VNC display port |
| `VM_DISK` | `/vm/disk.qcow2` | VM disk image path |
| `VM_DISK_SIZE` | (empty) | If set (e.g. `20G`) and `VM_DISK` does not exist, it is created as qcow2 of this size (directory too) before QEMU starts; an existing disk is left as is. Exits with an error if it cannot be created |
| `VM_CDROM` | (empty) | ISO initially inserted in the VirtualMedia CD drive (qemu-bmc always adds an empty CD drive; `-cdrom` is turned into it) |
| `VM_BOOT` | `c` | Boot device (c=disk, d=cdrom, n=network) |
| `VM_BOOT_MENU_TIMEOUT` | `0` | Boot menu timeout in ms (0=disabled) |
| `VM_NETWORKS` | (empty) | Comma-separated host interfaces for TAP passthrough (default: eth2+). Needs `/dev/net/tun` (`--device /dev/net/tun`); entrypoint creates it with mknod if missing, else exits with an error |
| `VM_NET_MTU` | (empty) | MTU for the VM NICs (e.g. `9000` for FCoE / jumbo frames): TAP and bridge set to it, container interface raised to it if smaller, `host_mtu=` added to virtio-net NICs. Empty = MTUs untouched. Not a number (68-65535) or failing to apply → exits with an error |
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
| GET | `/redfish/v1/Systems/1` | Computer system (Manufacturer, Model, SerialNumber, SKU (VM_* identity), PowerState, Boot, MemorySummary, ProcessorSummary, Processors, Storage, SimpleStorage, EthernetInterfaces, VirtualMedia → Systems/1/VirtualMedia, SecureBoot (UEFI process mode only), Actions, Status, Links.ManagedBy → Managers/1, Links.Chassis → Chassis/1) |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces` | NIC collection (one per guest NIC, command-line order) |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces/{n}` | NIC (MACAddress, LinkStatus: LinkUp while the VM runs) |
| GET | `/redfish/v1/Systems/1/Processors` | Processor collection: CPU1, or no members when the CPU count (SystemInfo.CPUs) is 0 |
| GET | `/redfish/v1/Systems/1/Processors/CPU1` | Processor (ProcessorType CPU, ProcessorArchitecture x86, InstructionSet x86-64, TotalCores = TotalThreads = CPU count, Status) |
| GET | `/redfish/v1/Systems/1/Storage` | Storage collection: Storage/1, or no members without disks (legacy mode) |
| GET | `/redfish/v1/Systems/1/Storage/1` | Storage (Drives → Disk{n}, Volumes, no StorageControllers) |
| GET | `/redfish/v1/Chassis/1/Drives`, `/redfish/v1/Chassis/1/Drives/Disk{n}` | Drive collection / drive (canonical URI; `Systems/1/Storage/1/Drives/Disk{n}` is a deprecated alias returning the same body). Drive (CapacityBytes, MediaType HDD, Protocol PCIe/SATA/SAS/NVMe/USB, Name, Status). Disks are found in the QEMU args (`qemu.DisksFromArgs`: `-drive file=` except media=cdrom/pflash/floppy/mtd and CD devices, `-hda`..`-hdd`) and sized at start with `qemu-img info --output=json -U`; unknown capacity/protocol is omitted |
| GET | `/redfish/v1/Systems/1/Storage/1/Volumes` | Volume collection: {n} per disk (read by sushy `volumes_sizes_bytes` in Ironic inspection), or no members without disks (legacy mode) |
| GET | `/redfish/v1/Systems/1/Storage/1/Volumes/{n}` | Volume (CapacityBytes, Name "Volume {n}", Status, Links.Drives → Disk{n}; no RAIDType). Volume ids are plain 1-based numbers |
| GET | `/redfish/v1/Systems/1/SimpleStorage` | SimpleStorage collection (deprecated, but Ironic inspection reads it first): SimpleStorage/1 or empty |
| GET | `/redfish/v1/Systems/1/SimpleStorage/1` | Devices: [{Name, CapacityBytes, Status}] per disk |
| GET | `/redfish/v1/Systems/1/VirtualMedia` | VirtualMedia collection (recommended location; same single CD drive as Managers/1/VirtualMedia, shared state) |
| GET | `/redfish/v1/Systems/1/VirtualMedia/CD1` | VirtualMedia resource, same as Managers/1/VirtualMedia/CD1; `@odata.id` and action targets follow the URI requested. InsertMedia/EjectMedia actions below work under both |
| PATCH | `/redfish/v1/Systems/1` | Boot device override (with ETag support; ETag includes HttpBootUri). Targets None/Pxe/Hdd/Cd/BiosSetup/UefiHttp. Cd gets the managed CD `bootindex=0`; Pxe with OVMF gets the first NIC `bootindex=0` (OVMF ignores `-boot n` and would boot the disk), unless a device already has bootindex=0. `Boot.HttpBootUri` (always in GET, null when unset): http(s) URL, or null/"" to clear (else 400); kept after Once is used until cleared. UefiHttp with a URI: image downloaded like VirtualMedia (VMEDIA_*, starts at PATCH, power on/reset waits; failure → that boot goes on without it) and booted from a hidden CD (`httpboot0` on its own ich9-ahci, bootindex=0; not VirtualMedia). Without a URI: network boot (-boot n); with OVMF also fw_cfg IPv4/IPv6PXESupport=n and first NIC `romfile=,bootindex=0` (`qemu.ApplyHTTPBoot`). Only for boots the override applies to. Legacy mode: UefiHttp / a URI → 400 |
| GET/PATCH | `/redfish/v1/Systems/1/SecureBoot` | Only in process mode with OVMF pflash drives (`qemu.FindSecureBootFirmware`: `-drive if=pflash` with an `OVMF_CODE*.fd` plus a VARS pflash); else no `SecureBoot` link on Systems/1 and 404. `#SecureBoot.v1_1_0`: SecureBootEnable (next boot), SecureBootCurrentBoot (running/last started QEMU used the secboot OVMF), SecureBootMode `UserMode` (ms VARS have a PK), ETag/If-Match. PATCH `SecureBootEnable` bool only (else 400; true → 400 if `OVMF_CODE_4M.secboot.fd` or `OVMF_VARS_4M.ms.fd` is missing). Enabled boots: CODE → `OVMF_CODE_4M.secboot.fd` (same dir; always 4M — Debian's 2M secboot build hangs), VARS → `<vars>.secboot.fd` next to the configured VARS, copied from `OVMF_VARS_4M.ms.fd` if missing or of another size; `smm=on` on `-machine`, `-global driver=cfi.pflash01,property=secure,value=on`. ForceRestart restarts QEMU (not system_reset) when the setting changed or keys were reset |
| POST | `/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys` | `ResetKeysType` `ResetAllKeysToDefault` only (else 400): deletes the secboot VARS, recopied from the template at the next QEMU start (204) |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | Power control: On, ForceOff, GracefulShutdown, ForceRestart, GracefulRestart, PowerCycle (process mode: QEMU stopped and restarted so boot override/VirtualMedia apply; legacy: system_reset; off: On), ForceOn (= On), Nmi (QMP inject-nmi; 409 when off; does not end a graceful action), PushPowerButton (off: On, on: GracefulShutdown) |
| GET | `/redfish/v1/Managers` | Manager collection |
| GET | `/redfish/v1/Managers/1` | BMC manager (ManagerType BMC, Manufacturer/Model `qemu-bmc`, FirmwareVersion = qemu-bmc version via SetFirmwareVersion, Status, Links.ManagerForServers → Systems/1, Links.ManagerInChassis → Chassis/1) |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia collection (deprecated location, kept for gofish/bmclib) |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia resource (ImageName omitted when nothing is inserted; Image stays as "") |
| POST | `.../VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia` | Insert ISO (container path `/...` or `file://`, or http(s) URL downloaded in the background with optional UserName/Password Basic auth; 204, 400 if missing). Kept while off, attached at QEMU start; power on/reset during a download runs once it ends (reports PoweringOn meanwhile), a failed download ejects |
| POST | `.../VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia` | Eject ISO (204) |
| GET | `/redfish/v1/Chassis` | Chassis collection |
| GET | `/redfish/v1/Chassis/1` | Chassis resource (PowerState same as Systems/1 incl. PoweringOff/PoweringOn, Manufacturer/Model/SerialNumber same as Systems/1, Status, Links.ComputerSystems → Systems/1, Links.ManagedBy → Managers/1) |

Middleware: auth (X-Auth-Token session or Basic; Basic and session login accept IPMI_USER/IPMI_PASS and any enabled bmc.State user with a name and password, e.g. created in-band; GET /redfish, GET /redfish/v1 and POST Sessions are exempt), ETag (If-Match), trailing slash normalization

## IPMI Commands

| Command | NetFn | Cmd | Description |
|---------|-------|-----|-------------|
| Get Device ID | App (0x06) | 0x01 | Static BMC identity |
| Get Self Test Results | App | 0x04 | 0x55 0x00 (passed) |
| Get BMC Global Enables | App | 0x2F | Fixed 0x0C (event message buffer + system event logging) |
| Get System GUID | App | 0x37 | System UUID (IPMI encoding, LSB first) |
| Get Channel Auth Capabilities | App | 0x38 | Auth type negotiation |
| Set Session Privilege | App | 0x3B | Privilege level |
| Close Session | App | 0x3C | Session teardown |
| Get Channel Cipher Suites | App | 0x54 | Cipher suite list (suites 3 and 17) |
| Get Chassis Status | Chassis (0x00) | 0x01 | Power state query |
| Chassis Control | Chassis | 0x02 | Power on/off/cycle/reset, soft off (ACPI power button, never forced); cycle = Redfish PowerCycle; diag = Nmi (0xD5 when off) |
| Chassis Identify | Chassis | 0x04 | Identify LED (log only) |
| Set Boot Options | Chassis | 0x08 | Boot device override |
| Get Boot Options | Chassis | 0x09 | Boot device query (UefiHttp reported as PXE) |
| Get FRU Inventory Area Info / Read FRU Data | Storage (0x0A) | 0x10 / 0x11 | FRU 0: Common Header + Product Info Area (QEMU, qemu-bmc VM, serial = system UUID) |
| Get SDR Repository Info / Reserve / Get SDR | Storage | 0x20 / 0x22 / 0x23 | One record: the BMC's MC Device Locator (an empty repository makes ipmitool try to rebuild it and print errors) |
| Get SEL Info / Reserve SEL / Get SEL Entry / Clear SEL | Storage | 0x40 / 0x42 / 0x43 / 0x47 | Always-empty SEL (Get SEL Entry → 0xCB) |
| Get Device SDR Info / Get Device SDR / Reserve | Sensor/Event (0x04) | 0x20 / 0x21 / 0x22 | No device SDRs (Get Device SDR → 0xCB) |

Supports: RMCP v1.0, IPMI 1.5, RMCP+ cipher suite 3 (RAKP-HMAC-SHA1, HMAC-SHA1-96) and 17 (RAKP-HMAC-SHA256, HMAC-SHA256-128), AES-CBC-128 encryption

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
