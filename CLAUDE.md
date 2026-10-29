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
  machine/                     # VM state management (wraps QMP + ProcessManager), event bus (events.go)
  redfish/                     # HTTP server with gorilla/mux, 15+ endpoints, EventService (events.go)
  ipmi/                        # UDP server + VM chardev server, RMCP/RMCP+, RAKP auth, chassis commands, SOL (sol.go, handler_sol.go)
  bmc/                         # BMC configuration state (users, LAN, channels)
  config/                      # Environment variable config
  fault/                       # Fault injection: store, fault API, Redfish middleware, IPMI/power hooks
  serial/                      # Serial console mux (Console interface, QEMU UNIX socket, TCP server, ring buffer)
  bios/                        # BIOS attributes (definitions, pending settings, ResetBios, JSON file)
  firmware/                    # Simulated BMC/BIOS firmware versions (image check, download, pending BIOS, JSON file)
docker/
  Dockerfile                   # Multi-stage build (Go builder + Debian runtime)
  entrypoint.sh                # Environment variables → QEMU args conversion
  setup-network.sh             # TAP/bridge network setup for VM passthrough
tests/                         # Bash-based container integration tests (142 tests)
  run_tests.sh                 # Test runner (category-based)
  test_helper.sh               # Assertions, container mgmt, IPMI/Redfish helpers
  test_*.sh                    # Test categories (container, ipmi, redfish, power, boot, fault, events, serial, sol, bios, etc.)
  sol_console.py               # expect-like pty driver for ipmitool sol activate (test_sol.sh)
  bios_sushy.py                # sushy client for test_bios.sh (attributes, set, reset, registry)
  update_sushy.py              # sushy client for test_update.sh (firmware inventory, simple_update)
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

# Container integration tests (all 142 tests across 14 categories)
make container-test-all

# Run specific test categories
./tests/run_tests.sh --build container ipmi redfish power boot

# Ironic end-to-end test (real Ironic deploys Debian; Redfish vmedia/https, IPMI PXE UEFI+BIOS)
make ironic-test                       # or IRONIC_FLOWS=redfish-vmedia (redfish-bios, redfish-https, ipmi-pxe), IRONIC_ARGS="--image ghcr.io/..."
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
| `SERIAL_ADDR` | `localhost:9002` | Process management mode only: qemu-bmc serves the guest serial console as raw TCP here (`serial.Server`, max 8 connections), see Serial Console below. Legacy mode: not used. Port 9002 collides with the `VM_IPMI_ADDR` example — use another port when enabling both |
| `SERIAL_BUFFER_BYTES` | `262144` | Recent serial output kept (ring buffer; Redfish SerialConsole log, replay); `0` = none |
| `SERIAL_QUEUE_BYTES` | `4194304` | Bytes of output a serial reader may fall behind before it is dropped (counted in bytes, not writes; replay not counted); `0` = default |
| `SERIAL_REPLAY` | `false` | New `SERIAL_ADDR` connections get the buffered output first |
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
| `EVENT_INSECURE_TLS` | `true` | Skip certificate verification when POSTing Redfish events to https subscription destinations |
| `BMC_FIRMWARE_VERSION` | (qemu-bmc version) | Initial BMC firmware version: FirmwareInventory/BMC, Managers/1 FirmwareVersion (`startFirmware`) |
| `BIOS_FIRMWARE_VERSION` | `QBMC-BIOS-1.0.0` | Initial BIOS firmware version: FirmwareInventory/BIOS, Systems/1 BiosVersion. Updated versions saved to `<VMEDIA_DIR>/firmware.json` (`Config.FirmwareFile`) win over both |
| `FIRMWARE_UPDATE_SECONDS` | `10` | Seconds a simulated update task takes from 0 to 100 % |
| `BIOS_ATTRIBUTES` | (empty) | JSON object replacing the defaults of writable Bios attributes (`bios.NewStore`; invalid → exit). State saved to `<VMEDIA_DIR>/bios-settings.json` (`Config.BiosFile`, both modes); saved values win over the defaults |
| `FAULT_API_ADDR` | (empty, disabled) | Fault injection API listen address (plain HTTP, e.g. `:8623`), see Fault Injection below |
| `FAULT_API_TOKEN` | (empty) | Bearer token required by the fault API (none if empty) |
| `FAULTS` | (empty) | JSON array of faults active from startup (works without `FAULT_API_ADDR`) |

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
| GET | `/redfish`, `/redfish/v1` | Version document, ServiceRoot (no auth; root has SessionService, EventService, Registries, Tasks, UpdateService (when `SetFirmware`) and Links.Sessions) |
| GET/PATCH | `/redfish/v1/SessionService` | SessionTimeout (default 1800 s, 30–86400) |
| GET/POST | `/redfish/v1/SessionService/Sessions` | Session collection / log in (POST needs no auth; 201 with X-Auth-Token and Location; 401 bad credentials; 503 over 64 sessions) |
| GET/DELETE | `/redfish/v1/SessionService/Sessions/{id}` | Session / log out (204). Sessions are in memory and expire after SessionTimeout idle |
| GET | `/redfish/v1/Systems` | System collection |
| GET | `/redfish/v1/Systems/1` | Computer system (`#ComputerSystem.v1_13_0`: Manufacturer, Model, SerialNumber, SKU (VM_* identity), BiosVersion (BIOS firmware, when SetFirmware), PowerState, Boot, MemorySummary, ProcessorSummary, Processors, Storage, SimpleStorage, EthernetInterfaces, VirtualMedia → Systems/1/VirtualMedia, SecureBoot (UEFI process mode only), Bios (when SetBios), SerialConsole (process mode: MaxConcurrentSessions 1, IPMI {ServiceEnabled, Port = IPMI_PORT, HotKeySequenceDisplay}), Actions, Status, Links.ManagedBy → Managers/1, Links.Chassis → Chassis/1) |
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
| GET | `/redfish/v1/Systems/1/LogServices` | LogService collection: SerialConsole when `Server.SetSerialLog` was called (process mode), else no members. Linked from Systems/1 |
| GET | `/redfish/v1/Systems/1/LogServices/SerialConsole[/Entries[/1]]` | `#LogService.v1_1_0` (LogEntryType OEM, MaxNumberOfRecords 1, WrapsWhenFull), `#LogEntryCollection` (members expanded: entries are contained resources), `#LogEntry.v1_7_0` (EntryType Oem, OemRecordFormat SerialConsoleText, AdditionalDataURI, AdditionalDataSizeBytes). A LogEntry per line would be far too large |
| GET | `/redfish/v1/Systems/1/LogServices/SerialConsole/Entries/1/attachment` | The ring buffer as `text/plain` (qemu-bmc specific; escapes included) |
| GET | `/redfish/v1/Systems/1/VirtualMedia` | VirtualMedia collection (recommended location; same single CD drive as Managers/1/VirtualMedia, shared state) |
| GET | `/redfish/v1/Systems/1/VirtualMedia/CD1` | VirtualMedia resource, same as Managers/1/VirtualMedia/CD1; `@odata.id` and action targets follow the URI requested. InsertMedia/EjectMedia actions below work under both |
| PATCH | `/redfish/v1/Systems/1` | Boot device override (with ETag support; ETag includes HttpBootUri). Targets None/Pxe/Hdd/Cd/BiosSetup/UefiHttp. Cd gets the managed CD `bootindex=0`; Pxe with OVMF gets the first NIC `bootindex=0` (OVMF ignores `-boot n` and would boot the disk), unless a device already has bootindex=0. `Boot.HttpBootUri` (always in GET, null when unset): http(s) URL, or null/"" to clear (else 400); kept after Once is used until cleared. UefiHttp with a URI: image downloaded like VirtualMedia (VMEDIA_*, starts at PATCH, power on/reset waits; failure → that boot goes on without it) and booted from a hidden CD (`httpboot0` on its own ich9-ahci, bootindex=0; not VirtualMedia). Without a URI: network boot (-boot n); with OVMF also fw_cfg IPv4/IPv6PXESupport=n and first NIC `romfile=,bootindex=0` (`qemu.ApplyHTTPBoot`). Only for boots the override applies to. Legacy mode: UefiHttp / a URI → 400. Once: after the boot that used it, `Machine.revertBootOverride` undoes the override in the running QEMU (`qemu.BootRevert` from the process manager: qom-set bootindex=-1 on the devices that got bootindex=0, HMP boot_set to the original order; UEFI disk-first: disks from query-block get bootindex 1..) so a guest-initiated reboot boots the default order; a ForceRestart with Once pending restarts QEMU. After a UEFI override (`uefiOrderDirty`), the next power on without an override sets disk bootindex and system_resets once (OVMF stores the override order in its VARS) |
| GET/PATCH | `/redfish/v1/Systems/1/SecureBoot` | Only in process mode with OVMF pflash drives (`qemu.FindSecureBootFirmware`: `-drive if=pflash` with an `OVMF_CODE*.fd` plus a VARS pflash); else no `SecureBoot` link on Systems/1 and 404. `#SecureBoot.v1_1_0`: SecureBootEnable (next boot), SecureBootCurrentBoot (running/last started QEMU used the secboot OVMF), SecureBootMode `UserMode` (ms VARS have a PK), ETag/If-Match. PATCH `SecureBootEnable` bool only (else 400; true → 400 if `OVMF_CODE_4M.secboot.fd` or `OVMF_VARS_4M.ms.fd` is missing). Enabled boots: CODE → `OVMF_CODE_4M.secboot.fd` (same dir; always 4M — Debian's 2M secboot build hangs), VARS → `<vars>.secboot.fd` next to the configured VARS, copied from `OVMF_VARS_4M.ms.fd` if missing or of another size; `smm=on` on `-machine`, `-global driver=cfi.pflash01,property=secure,value=on`. ForceRestart restarts QEMU (not system_reset) when the setting changed or keys were reset |
| GET | `/redfish/v1/Systems/1/Bios` | `#Bios.v1_2_0` (when `Server.SetBios`; main always): AttributeRegistry `BiosAttributeRegistry.1.0.0`, Attributes (current), ResetBiosToDefaultsPending, `@Redfish.Settings` (SettingsObject → Bios/Settings, SupportedApplyTimes [OnReset]; no ETag/Time/Messages), Actions `#Bios.ResetBios`, ETag. See BIOS below |
| GET/PATCH | `/redfish/v1/Systems/1/Bios/Settings` | Pending attributes (`@Redfish.SettingsApplyTime` OnReset). PATCH `{"Attributes": {...}}` (+ optional `@Redfish.SettingsApplyTime` OnReset, `@odata.*` ignored, other keys 400 PropertyUnknown) merged into pending; invalid → 400 `Base.1.8.GeneralError` with one `@Message.ExtendedInfo` per attribute (PropertyUnknown/PropertyNotWritable/PropertyValueTypeError/PropertyValueNotInList/StringValueTooLong, RelatedProperties `#/Attributes/<name>`), nothing changed. ETag/If-Match. 200 + Settings body |
| POST | `/redfish/v1/Systems/1/Bios/Actions/Bios.ResetBios` | Body ignored (sushy posts none first). 204. Drops pending, sets ResetBiosToDefaultsPending |
| GET | `/redfish/v1/Registries`, `/redfish/v1/Registries/BiosAttributeRegistry` | `#MessageRegistryFileCollection` (linked from ServiceRoot; empty without SetBios); `#MessageRegistryFile.v1_1_0` (Registry `BiosAttributeRegistry.1.0`, Languages [en], Location Uri below) |
| GET | `/redfish/v1/Registries/BiosAttributeRegistry/BiosAttributeRegistry.1.0.0.json` | `#AttributeRegistry.v1_3_6` (Id = Bios AttributeRegistry, so sushy `get_attribute_registry` matches it; Language en, OwningEntity qemu-bmc, SupportedSystems SystemId `qemubmc` (validator: alphanumeric), RegistryEntries.Attributes: AttributeName, DisplayName, HelpText, Type, Value [{ValueName, ValueDisplayName}], DefaultValue (omitted if empty), ReadOnly, ResetRequired, MinLength/MaxLength for String) |
| POST | `/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys` | `ResetKeysType` `ResetAllKeysToDefault` only (else 400): deletes the secboot VARS, recopied from the template at the next QEMU start (204) |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | Power control: On, ForceOff, GracefulShutdown, ForceRestart, GracefulRestart, PowerCycle (process mode: QEMU stopped and restarted so boot override/VirtualMedia apply; legacy: system_reset; off: On), ForceOn (= On), Nmi (QMP inject-nmi; 409 when off; does not end a graceful action), PushPowerButton (off: On, on: GracefulShutdown) |
| GET | `/redfish/v1/Managers` | Manager collection |
| GET | `/redfish/v1/Managers/1` | BMC manager (`#Manager.v1_7_0`, the first with Manufacturer; ManagerType BMC, Manufacturer/Model `qemu-bmc`, FirmwareVersion = BMC firmware version (SetFirmware; else SetFirmwareVersion), Status, SerialConsole (process mode, `Server.SetIPMISerialConsole`: ServiceEnabled, MaxConcurrentSessions 1, ConnectTypesSupported ["IPMI"]; Manager.SerialConsole is deprecated from v1_10, so the version stays), Links.ManagerForServers → Systems/1, Links.ManagerInChassis → Chassis/1) |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia collection (deprecated location, kept for gofish/bmclib) |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia resource (ImageName omitted when nothing is inserted; Image stays as "") |
| POST | `.../VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia` | Insert ISO (container path `/...` or `file://`, or http(s) URL downloaded in the background with optional UserName/Password Basic auth; 204, 400 if missing). Kept while off, attached at QEMU start; power on/reset during a download runs once it ends (reports PoweringOn meanwhile), a failed download ejects |
| POST | `.../VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia` | Eject ISO (204) |
| GET/PATCH | `/redfish/v1/EventService` | `#EventService.v1_10_0`: ServiceEnabled, DeliveryRetryAttempts (3, 0–100), DeliveryRetryIntervalSeconds (5, 1–3600) are PATCHable (else 400 PropertyNotWritable), ETag/If-Match; EventFormatTypes [Event], RegistryPrefixes [ResourceEvent, Base], ResourceTypes, ServerSentEventUri, SSEFilterPropertiesSupported. ServiceEnabled false → no events at all. Linked from ServiceRoot |
| POST | `/redfish/v1/EventService/Actions/EventService.SubmitTestEvent` | MessageId required; EventType (default Other), MessageSeverity/Severity (OK), Message (default from the ResourceEvent table), MessageArgs, OriginOfCondition (string or {"@odata.id"}), EventId, EventTimestamp (RFC 3339), EventGroupId. 204; 503 when disabled |
| GET | `/redfish/v1/EventService/SSE` | text/event-stream `id: <EventId>` / `data: <Event JSON>` (no Context), `: keep-alive` every 15 s. `$filter`: `Prop eq 'v'` on EventType/MessageId/OriginResource/RegistryPrefix/ResourceType with and/or, no parentheses (`parseSSEFilter`); else 400 |
| GET/POST | `/redfish/v1/EventService/Subscriptions` | `#EventDestination.v1_13_0`. POST: Destination http(s) + Protocol Redfish required; Context, EventTypes, RegistryPrefixes, ResourceTypes (values checked, 400), MessageIds (version optional), OriginResources (+SubordinateResources), HttpHeaders (sent, GET returns []), DeliveryRetryPolicy (default SuspendRetries). 201 + Location. Max 32 (503). Memory only |
| GET/PATCH/DELETE | `/redfish/v1/EventService/Subscriptions/{id}` | PATCH Context, DeliveryRetryPolicy only; Status.State Disabled while suspended. Actions `EventDestination.SuspendSubscription` / `ResumeSubscription` (204) |
| GET | `/redfish/v1/UpdateService` | `#UpdateService.v1_8_0` (when `Server.SetFirmware`; else 404): ServiceEnabled, FirmwareInventory, MultipartHttpPushUri `/redfish/v1/UpdateService/upload`, MaxImageSizeBytes, Actions `#UpdateService.SimpleUpdate` (TransferProtocol@Redfish.AllowableValues HTTP/HTTPS). See Firmware updates below |
| GET | `/redfish/v1/UpdateService/FirmwareInventory[/BMC\|/BIOS]` | `#SoftwareInventoryCollection` / `#SoftwareInventory.v1_2_0` (Version, Updateable true, Manufacturer, SoftwareId `QBMC-FW-<target>`, Status, RelatedItem Managers/1 / Systems/1). Pending BIOS version: Status.State `StandbyOffline`, pending version in Description |
| POST | `/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate` | ImageURI (http(s); TransferProtocol HTTP/HTTPS only, gives the scheme to a scheme-less URI, else ignored: sushy sends HTTP for https), Targets, Username/Password. Downloads synchronously (`firmware.Download`, VMEDIA_INSECURE_TLS / VMEDIA_TIMEOUT), then 202 + Location (task monitor) + Task body; broken image / Targets mismatch / firmware-image-rejected → 400 `Update.1.0.VerificationFailed`; download error → 400 `Update.1.0.TransferFailed` |
| POST | `/redfish/v1/UpdateService/upload` | Multipart push: `UpdateParameters` (optional JSON: Targets, `@Redfish.OperationApplyTime` Immediate/OnReset), `UpdateFile` (required; read streaming by `firmware.ReadImage`). Same answers as SimpleUpdate |
| GET | `/redfish/v1/TaskService`, `.../Tasks`, `.../Tasks/{id}` | `#TaskService.v1_1_0` (CompletedTaskOverWritePolicy Oldest, LifeCycleEventOnTaskStateChange), `#TaskCollection`, `#Task.v1_4_3` (TaskState, TaskStatus, PercentComplete, StartTime, EndTime, TaskMonitor, Messages). Always 200 (Ironic reads TaskState) |
| GET | `/redfish/v1/TaskService/TaskMonitors/{id}` | 202 + Location + Retry-After 1 + Task while New/Running, then 200 + Task |
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
| Close Session | App | 0x3C | Session teardown: RMCP+ removes the session (data = session ID, 0 = own; 0x87 unknown) and SOL active in it |
| Activate / Deactivate Payload | App | 0x48 / 0x49 | SOL only (payload 1, instance 1), see IPMI SOL below |
| Get Payload Activation Status / Instance Info | App | 0x4A / 0x4B | SOL active?; session ID holding it, port 1 |
| Set / Get User Payload Access | App | 0x4C / 0x4D | SOL bit per user (memory only); Activate → 0xD4 if disabled |
| Get Channel Payload Support / Version | App | 0x4E / 0x4F | IPMI (+SOL with a console) + session setup 0x3F; version 0x10 (0x80 for others) |
| Get / Set SOL Configuration Parameters | Transport (0x0C) | 0x22 / 0x21 | Params 0-8 (revision 0x11, bit 7 of channel = revision only, 0x80 unsupported, 7/8 read-only 0x82); channel 1 or 0x0E |
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

RMCP+ session packets (IPMI and SOL) go through `openSessionPayload` (session must be authenticated; with an integrity algorithm the packet must be authenticated and its AuthCode is verified) and `sealSessionPayload` (outbound sequence number atomic: SOL output comes from another goroutine). Sessions without a packet for `SessionIdleTimeout` (60 s) are removed every 5 s (`Server.closeIdleSessions`, `SessionManager.RemoveIdle`).

## IPMI SOL

- `internal/ipmi/sol.go` (`solManager`, `solInstance`), commands in `handler_sol.go` (`Server.handleServerCommand`: the commands needing the session or server state). Enabled by `Server.SetSerialConsole(serial.Console)` (main, process mode); without it Activate → 0x81 and Channel Payload Support has no SOL.
- One instance: Activate (6 bytes; aux bit 7/6 = encryption/authentication, forced by param 2 bits 7/6; privilege from RAKP 1 vs param 2) → 0x80 if active (any session). Response exactly 12 bytes: payload sizes 255/255, UDP port = the server's local port (ipmitool requires it to equal the port it uses), VLAN 0xFFFF. Subscribes to the console (`Subscribe(false)`), so TCP clients keep working.
- Output (`solInstance.run`): sent after the accumulate interval (param 3, 5 ms units) or at the send threshold, up to 251 bytes, one packet in flight, seq 1-15 (a new seq per new data: ipmitool drops data of a packet repeating the last seq), resent every retry interval (param 4) retry count times then dropped; partial ACK → rest first in a new packet; NACK → resent at the retry interval. Buffer max 64 KiB (oldest dropped). Subscription ended → deactivated with notice.
- Input (`solInstance.receive`): written to the console, ACK (seq 0) with accepted count; same seq again → ACK repeated, not written; VM off → NACK + transfer unavailable (0x60). Packets of other sessions ignored. Operation bits ignored.
- Deactivate by the holder: stop. By another session: "SOL deactivating" (0x10) packet to the holder and its session closed (ipmitool exits with "SOL session closed by BMC" at the next key / keepalive). Also on Close Session, idle timeout, SOL Enable set to 0.
- Faults: SOL packets are `fault.IPMIRequest{SOL: true}` (matched only by faults without netfn/cmd).
- Container tests: `tests/test_sol.sh` (category `sol`; Debian guest via `prepare_debian_disk` in test_helper.sh; ipmitool runs in the container with `docker exec -it` under `tests/sol_console.py` so the port is 623; -C 3 and -C 17 root login + command output, `~.`, second activate refused, `sol deactivate` from another session). Ironic: `tests/ironic/run.sh` ipmi-pxe uses `--console-interface ipmitool-socat` (Ironic image + socat, `OS_DEFAULT__ENABLED_CONSOLE_INTERFACES`, `OS_CONSOLE__PORT_RANGE`) and logs in through the console URL (`check_console`).

## BIOS

- `internal/bios` (`bios.Store`): `Definitions` (BootMode, SecureBoot read-only; ProcVirtualization, ProcHyperThreading, SriovGlobalEnable, PxeDev1EnDis, BootRetry, PowerProfile, NumLock, SerialConsoleRedirection: Enumeration; AssetTag: String max 64). Values are display only (they do not change the VM); read-only values come from `SetReadOnlyValue` (main: BootMode from `cfg.RedfishBootMode()`, SecureBoot from `GetSecureBoot().Enable`).
- `SetPending` validates everything first (`bios.Errors`, all invalid attributes), then merges; `ResetToDefaults` drops pending and sets ResetPending; `Apply` (reset → defaults, then pending) clears both. Every change is saved (tmp + rename) as `{"Attributes", "Pending", "ResetPending"}`; on load invalid/unknown/read-only entries are dropped with a warning, an unreadable file is ignored.
- Applied by `machine.SetBootHook` (main `startBios`, set before POWER_ON_AT_START): `Machine.booting()` runs under resetMu right before QEMU `Start` (process On, also from ForceRestart restart / PowerCycle / GracefulRestart / QEMU gone) and before `system_reset` (process ForceRestart, legacy On from off / ForceRestart / PowerCycle). Guest-initiated reboots are not seen.
- Container tests: `tests/test_bios.sh` (category `bios`; image `qemu-bmc-sushy-test` = python:3.12-slim + pip sushy, built once, `--network host`, driven by `tests/bios_sushy.py`; BIOS_ATTRIBUTES defaults, registry, invalid values 400, applied at ForceOff/On and ForceRestart, kept in a new container on the VMEDIA_DIR volume, reset_bios + PowerCycle). Ironic: `tests/ironic/run.sh` flow `redfish-bios` (bios setting list, manual cleaning `--disable-ramdisk` apply_configuration ProcVirtualization=Disabled then factory_reset, no "BIOS settings are not supported" in the Ironic log).

## Firmware updates

- `internal/firmware` (`firmware.Store`): versions of `BMC` and `BIOS` (defaults from main `startFirmware`), `Install` (BMC: version at once; BIOS: `Pending`, replaced by a newer one), `Apply` (pending → version; run by the boot hook next to `bios.Store.Apply`, main `setBootHook`, i.e. the same moments as BIOS settings), `OnChange` hooks (Redfish `ResourceChanged` on FirmwareInventory/<target>). Saved (tmp + rename) as `{"Versions", "Pending"}` with only updated versions, so the file wins over the env; invalid entries dropped on load.
- Images (`ReadImage`): first line `QBMC-FW <BMC|BIOS> <version>` (exactly 3 space-separated fields, version 1-64 printable ASCII without spaces), ended by `\n` (`\r` trimmed) or EOF, max 256 bytes; any bytes after; whole image ≤ `MaxImageBytes` (256 MiB) — else `ErrInvalidImage`.
- Tasks (`internal/redfish/tasks.go`, `taskService`): memory only, max 32 (oldest finished dropped, all running → 503). `runUpdate` (handler_update_service.go): Running, PercentComplete +1 every `FIRMWARE_UPDATE_SECONDS`/100, then `Install`. Messages (Update.1.0): TargetDetermined, InstallingOnComponent, then UpdateSuccessful (BMC) / AwaitToActivate + Resolution "next system boot" (BIOS) / ApplyFailed (fault). State changes publish `TaskEvent.1.0.TaskStarted` / `TaskCompletedOK` / `TaskAborted` (EventType StatusChange, origin the Task, resource type Task).
- sushy `simple_update` uses the 202 body's `@odata.id` (the Task) as its monitor, so its `wait()` ends at once; poll `get_task().task_state` (tests/update_sushy.py), as Ironic does.
- Container tests: `tests/test_update.sh` (category `update`; python:3.12-slim image server on its own docker network, addressed by IP; SimpleUpdate + multipart push for BMC and BIOS (BIOS applied at ForceOff/On and ForceRestart), broken images, both faults, sushy simple_update, versions and a pending BIOS kept in a new container on the VMEDIA_DIR volume).

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

## Fault Injection

`internal/fault` (`fault.Store`). Enabled by `FAULT_API_ADDR` and/or `FAULTS`; otherwise main wires nothing (nil store). API on its own port, not Redfish (no Oem either): `GET /faults`, `POST /faults` (201 + `id`), `DELETE /faults/{id}`, `DELETE /faults`; Bearer `FAULT_API_TOKEN` if set.

Fault JSON: `{"kind", "target" (optional, derived from kind), conditions..., "count" (uses before removal), "durationSeconds"}`. Checked in insertion order, first match applies; each use logs `fault: <id> <kind> applied to <request>`. GET adds `applied` and `expiresAt`.

| Kind | Effect | Conditions |
|------|--------|------------|
| `redfish-delay` | `delayMs` later | `method`, `path` (prefix, or `path.Match` glob with `*?[`) |
| `redfish-error` | `status` (default 500) + Redfish error body, handler not run | same |
| `redfish-hang` | no response until client gives up / fault deleted / `hangSeconds` (300), then conn closed | same |
| `redfish-drop` | hijack + close (SetLinger 0 on the TCP conn under TLS) | same |
| `ipmi-drop` / `ipmi-delay` / `ipmi-error` | no response / response sent `delayMs` later (`time.AfterFunc`, serve loop not blocked) / `completionCode` (0xFF) without running the command | `netfn`, `cmd`, `includeSession` (session open/close messages: ASF ping, Open Session/RAKP, App 0x38/0x39/0x3A/0x3B/0x3C/0x54). Inbound SOL packets only match without `netfn`/`cmd` (no error, not a command) |
| `power-action-fail` | Redfish 500, IPMI `completionCode` (0xFF) via `*fault.PowerActionError` | `action` (requested ResetType; IPMI chassis control → ForceOff/On/PowerCycle/ForceRestart/GracefulShutdown/Nmi) |
| `power-action-ignored` | 204 / cc 0, nothing done | `action` |
| `graceful-ignored` | no ACPI powerdown (`machine.SetGracefulFilter`), GRACEFUL_TIMEOUT behaviour unchanged | `action` |
| `bmc-unavailable` | Redfish held open until the fault ends then closed, IPMI dropped (session messages too) | — |
| `firmware-image-rejected` | received firmware image rejected like a broken one (400, no task) via `Store.RejectFirmwareImage` | `target` BMC/BIOS (omitted or `firmware` = both) |
| `firmware-update-fail` | update task → Exception at `atPercent` (default 50, 0-100), version unchanged; taken when the task is created (`Store.FirmwareUpdateFailure`) | `target`, `atPercent` |

Wiring (`cmd/qemu-bmc/main.go`): `faults.Middleware` wraps the Redfish handler (Redfish then HTTP/1.1 only so connections can be hijacked); `ipmi.Server.SetFaults`; `redfish.Server.SetFirmwareFaults`; `faultMachine` wraps `*machine.Machine` so Redfish/IPMI (and VM IPMI) `Reset` go through `Store.Reset`. Container tests: `tests/test_fault.sh` (category `fault`).

## Serial Console

- `internal/serial`: `serial.Console` interface (`Subscribe(replay)`, `Write` input, `Recent`) implemented by `*serial.Mux`; IPMI SOL uses it (a WebSocket console should too). `Subscription.Output()` chunks are shared (read-only), closed when the subscription ends; `Done()` closes immediately. A subscriber more than 256 chunks behind is dropped (`publish` never blocks). Replay = buffer snapshot + register under one lock (no gap/overlap). `Write` without QEMU → `ErrNotConnected`.
- Process mode only (`startSerial` in main, before QEMU can start): QEMU gets `-chardev socket,id=serial0,path=<dir of QMP_SOCK>/serial.sock,server=off -serial chardev:serial0`, i.e. QEMU is the client and connects at every start before the guest runs (no boot output lost); `Mux.ServeQEMU` attaches each connection, a newer one replaces the old. Socket mode 0600, stale socket removed.
- `serial.Server` on `SERIAL_ADDR`: raw TCP, 8 connections (the 9th gets a message and is closed), output to all, input from any; a dropped (slow) client is reset (SetLinger 0) so it is not left waiting on queued data. Connections stay across QEMU power off/on.
- Legacy mode: nothing (external QEMU owns its serial). `tests/ironic/run.sh` serial logger still works (one more client).
- Container tests: `tests/test_serial.sh` (category `serial`): Debian nocloud image (cached in `~/.cache/qemu-bmc-ironic-e2e`, downloaded if missing) as a qcow2 overlay in a docker volume; two clients in the container both get the login prompt and command output, Redfish attachment, power off/on keeps them connected.

## Events

- `machine.Machine.SubscribeEvents()` (`internal/machine/events.go`): small bus, buffered channels (64, drop when full). `EventPowerState` is published by `checkPower` (synchronously after each Reset, setTransition, process ForceOff/start, so Off within a PowerCycle is seen; and by a 1 s poller running while there are subscribers, catching guest power off / QEMU exit / graceful timeout). The Reset-start clearing of the transition does not check, so a PoweringOff ended by ForceOff is PoweringOff → Off. `EventResourceChanged` for Boot (SetBootOverride, ConsumeBootOnce when Once, HTTP boot URI change), VirtualMedia (insert/eject/failed download), SecureBoot.
- Redfish (`internal/redfish/events.go`): `Server.SetEventSource(m)` (main) converts them (firmware: TaskEvent and FirmwareInventory ResourceChanged, see Firmware updates); MessageIds `ResourceEvent.1.3.*` (ResourcePoweredOn/Off, ResourcePoweringOn/Off with arg `/redfish/v1/Systems/1`, ResourceChanged; sessions ResourceCreated/ResourceRemoved), EventType StatusChange / ResourceUpdated / ResourceAdded / ResourceRemoved, origin Systems/1, Systems/1/VirtualMedia/CD1, Systems/1/SecureBoot, the session, SessionService, EventService.
- Delivery: one goroutine per subscription (queue 256), POST JSON `#Event.v1_7_0` with one record, 2xx = success, 10 s timeout, 1 + DeliveryRetryAttempts tries `DeliveryRetryIntervalSeconds` apart (`retryUnit`, ms in tests). Then SuspendRetries → suspended (events dropped until ResumeSubscription), TerminateAfterRetries → deleted; RetryForever(WithBackoff) retries until delivered (backoff doubles, max 10 min).
- Container tests: `tests/test_events.sh` (category `events`; python:3.12-slim receiver on its own docker network, SSE with curl -N).

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
