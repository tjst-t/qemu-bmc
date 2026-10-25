# qemu-bmc

A Go single binary that controls QEMU VMs via both Redfish API (HTTPS) and IPMI over LAN (UDP).

```
┌──────────────────────────────────────────┐
│ OCI Container                            │
│                                          │
│  ┌───────────┐   ┌──────────────────┐   │
│  │ qemu-bmc  │   │      QEMU        │   │
│  │ (Go)      │   │                  │   │
│  │           │   │                  │   │
│  │ :443/tcp  │   │                  │   │
│  │  Redfish  ├──►│  QMP Socket      │   │
│  │           │   │                  │   │
│  │ :623/udp  │   │                  │   │
│  │  IPMI     ├──►│                  │   │
│  │           │   │                  │   │
│  │ :9002/tcp │   │  ipmi-bmc-extern │   │
│  │  VM IPMI  │◄──┤  (KCS)          │   │
│  └───────────┘   └──────────────────┘   │
└──────────────────────────────────────────┘
```

## Features

- **Redfish API** - ServiceRoot, Systems, Managers, VirtualMedia, Chassis (gofish compatible)
- **IPMI over LAN** - RMCP/RMCP+, cipher suites 3 (RAKP-HMAC-SHA1) and 17 (RAKP-HMAC-SHA256, ipmitool `-C 17`), AES-CBC-128 encryption
- **VM IPMI (In-Band)** - Guest OS IPMI via QEMU `ipmi-bmc-extern` KCS interface for MaaS commissioning
- **noVNC** - Browser-based VNC console served on the Redfish HTTP port (no extra port needed)
- **QMP Control** - Power operations, boot device changes, VirtualMedia mount
- **UEFI HTTP boot and Secure Boot** - `UefiHttp` boot target with `Boot.HttpBootUri`; `Systems/1/SecureBoot` switches OVMF to its Secure Boot variant (Microsoft keys)
- **Fault injection** - Unresponsive, slow or failing Redfish/IPMI and power actions that fail or do nothing, through a separate API, for testing management tools
- **Compatibility** - MAAS, Tinkerbell/Rufio

## Quick Start

### Docker Compose (Process Management Mode)

qemu-bmc manages the QEMU process directly inside the container:

```bash
# Create VM disk (or set VM_DISK_SIZE=20G to let the container create it)
mkdir -p vm
qemu-img create -f qcow2 vm/disk.qcow2 20G

# Start
docker compose up -d

# Check status
ipmitool -I lanplus -H localhost -U admin -P password mc info
curl -k -u admin:password https://localhost/redfish/v1/Systems/1
```

### Docker Build

```bash
docker build -t qemu-bmc -f docker/Dockerfile .
docker run --rm --device /dev/kvm --device /dev/net/tun --cap-add NET_ADMIN \
  -p 5900:5900 -p 623:623/udp -p 443:443 \
  -v ./vm:/vm \
  -e VM_MEMORY=4096 -e VM_CPUS=4 \
  qemu-bmc
```

### Binary (Legacy Mode)

Connect to an existing QEMU instance via QMP socket. Start QEMU with `-no-shutdown` (and a QMP socket, e.g. `-qmp unix:/var/run/qemu/qmp.sock,server,nowait`): without it, QEMU exits when the guest powers off (e.g. after `GracefulShutdown`), and qemu-bmc can no longer report the power state or power the VM back on.

```bash
make download-novnc
go build -o qemu-bmc ./cmd/qemu-bmc

export QMP_SOCK=/var/run/qemu/qmp.sock
export IPMI_USER=admin
export IPMI_PASS=password
./qemu-bmc
```

## Usage

### Redfish

```bash
# Service root
curl -k -u admin:password https://localhost/redfish/v1/

# Check power state
curl -k -u admin:password https://localhost/redfish/v1/Systems/1

# Power on
curl -k -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"ResetType": "On"}' \
  https://localhost/redfish/v1/Systems/1/Actions/ComputerSystem.Reset

# Force power off
curl -k -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"ResetType": "ForceOff"}' \
  https://localhost/redfish/v1/Systems/1/Actions/ComputerSystem.Reset

# Set PXE boot
curl -k -u admin:password -X PATCH \
  -H 'Content-Type: application/json' \
  -d '{"Boot": {"BootSourceOverrideEnabled": "Once", "BootSourceOverrideTarget": "Pxe"}}' \
  https://localhost/redfish/v1/Systems/1

# Insert an ISO into the virtual CD drive (path inside the container), boot from it once
curl -k -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"Image": "/iso/boot.iso", "Inserted": true}' \
  https://localhost/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia
curl -k -u admin:password -X PATCH \
  -H 'Content-Type: application/json' \
  -d '{"Boot": {"BootSourceOverrideEnabled": "Once", "BootSourceOverrideTarget": "Cd"}}' \
  https://localhost/redfish/v1/Systems/1

# Or let qemu-bmc download an ISO over HTTP(S) (UserName/Password are optional, sent as Basic auth)
curl -k -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"Image": "http://192.168.0.10/boot.iso", "UserName": "user", "Password": "secret"}' \
  https://localhost/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia

# Eject it
curl -k -u admin:password -X POST \
  https://localhost/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia
```

#### VirtualMedia

- The CD drive is `/redfish/v1/Systems/1/VirtualMedia/CD1` (recommended since Redfish 2021.1; Ironic/sushy looks here first) and also `/redfish/v1/Managers/1/VirtualMedia/CD1` (deprecated location, kept for gofish, bmclib and existing users). Both are the same drive: inserting through one is seen through the other. `@odata.id` and the action targets follow the URI you read.
- In process management mode (the container), the VM always has a CD drive. It is empty unless `VM_CDROM` is set, in which case that ISO is the initially inserted medium.
- `Image` is a path inside the container, `/iso/boot.iso` or `file:///iso/boot.iso` (bind-mount the ISO, e.g. `-v ./iso:/iso:ro`), or an `http://` / `https://` URL. A missing `Image`, a nonexistent file or an unsupported scheme returns `400`; InsertMedia and EjectMedia return `204 No Content` on success.
- An HTTP(S) image is downloaded by qemu-bmc into `VMEDIA_DIR`, then attached (QEMU in the image cannot read URLs itself). InsertMedia returns `204` without waiting for the download; meanwhile `GET` already reports the URL as `Image` with `Inserted: true`. `UserName` / `Password` in the InsertMedia request are sent as HTTP Basic auth. Credentials can also be put in the URL (`https://user:pass@host/boot.iso`); the password is then reported as `xxxxx` in `Image` and in the logs. Certificates are not verified unless `VMEDIA_INSECURE_TLS=false` (self-signed servers are common); to verify against your own CA, also mount it and set `SSL_CERT_FILE` (e.g. `-e SSL_CERT_FILE=/certs/ca.pem`).
- A power on or reset (Redfish `On` / `ForceOn` / `ForceRestart` / `PowerCycle`, IPMI `chassis power on` / `cycle` / `reset`) during a download is carried out once the download finishes (at most `VMEDIA_TIMEOUT` later), so the VM boots from the ISO. The request itself returns at once; the power state is `PoweringOn` meanwhile, and another power action (e.g. `ForceOff`) cancels it. If the download fails (e.g. `404`), the medium is ejected (`Inserted: false`) and the reason is logged. A download finishing while the VM runs is attached over QMP. Inserting another image or ejecting abandons the download and deletes the previously downloaded file.
- The medium is kept by qemu-bmc while the VM is powered off and attached when QEMU starts; while the VM is running it is swapped live over QMP.
- Boot target `Cd` (Redfish `BootSourceOverrideTarget`, IPMI `chassis bootdev cdrom`) boots the inserted ISO in both BIOS and UEFI mode (the CD drive gets `bootindex=0`, which OVMF requires).
- Boot target `Pxe` (Redfish, IPMI `chassis bootdev pxe`) boots from the network in UEFI mode too, even when the disk holds an OS: OVMF ignores `-boot n`, so the first NIC gets `bootindex=0` (BIOS uses `-boot n`).
- `GET .../VirtualMedia/CD1` reports `Image`, `ImageName`, `Inserted`, `ConnectedVia` (`URI` / `NotConnected`), `WriteProtected` (always `true`) and `MediaTypes`.
- Legacy mode (external QEMU) only changes the medium over QMP; the path refers to the external QEMU's filesystem. The CD drive is located with `query-block` (first removable drive) in both modes.

#### UEFI HTTP boot

Boot target `UefiHttp` (as used by Ironic/Metal3 `redfish-https` boot) works in two ways, depending on `Boot.HttpBootUri`:

```bash
curl -k -u admin:password -X PATCH https://localhost/redfish/v1/Systems/1 \
  -H 'Content-Type: application/json' \
  -d '{"Boot": {"BootSourceOverrideTarget": "UefiHttp", "HttpBootUri": "https://10.0.0.1/boot.iso", "BootSourceOverrideEnabled": "Once", "BootSourceOverrideMode": "UEFI"}}'
# Clear the URI after use
curl -k -u admin:password -X PATCH https://localhost/redfish/v1/Systems/1 \
  -H 'Content-Type: application/json' -d '{"Boot": {"HttpBootUri": null}}'
```

- **With `HttpBootUri`**: qemu-bmc downloads the image like a VirtualMedia image (`VMEDIA_DIR`, `VMEDIA_INSECURE_TLS`, `VMEDIA_TIMEOUT`; the download starts when the URI is PATCHed) and attaches it as a hidden CD drive, separate from VirtualMedia `CD1` (not listed under VirtualMedia, `CD1` is unchanged), which the VM boots first (`bootindex=0`). A power on or reset during the download waits for it like for VirtualMedia. If the download fails (logged) or is not done within `VMEDIA_TIMEOUT`, that boot goes ahead without it, as without an override. The image is handled as an ISO (CD); a bare EFI executable (e.g. `.efi`) is not supported. Same in BIOS mode.
- **Without `HttpBootUri`**: the VM boots from the network (`-boot n` as for `Pxe`). With UEFI firmware (OVMF), PXE is turned off over fw_cfg (`opt/org.tianocore/IPv4PXESupport` / `IPv6PXESupport` = `n`) and the first NIC gets `bootindex=0` and no option ROM (`romfile=`, otherwise its iPXE would boot before OVMF's HTTP boot), so OVMF only tries HTTP boot. The boot URL comes from your DHCP server (its answer to the `HTTPClient` vendor class). In BIOS mode it is the same as `Pxe`.
- `HttpBootUri` takes an `http://` / `https://` URL, or `null` / `""` to clear it (anything else is `400`). It is reported in `Boot` at all times (`null` when unset, so that sushy sends it) and is part of the ETag. It stays set after a `Once` override has been used (the target goes back to `None`) until it is cleared; setting it again downloads it again, and changing or clearing it deletes the downloaded file. Leftovers (`httpboot-*.iso`) are deleted at startup.
- The hidden CD and the fw_cfg/NIC changes are only applied to a boot the `UefiHttp` override applies to (not after a `Once` override has been used).
- IPMI reports `UefiHttp` as PXE in Get Boot Options.
- Not available in legacy mode (external QEMU), since qemu-bmc cannot change its arguments: PATCHing `UefiHttp` or a `HttpBootUri` returns `400` (clearing with `null` is accepted).

#### Secure Boot

With UEFI firmware (`VM_BOOT_MODE=uefi`: OVMF as `-drive if=pflash` drives) in process management mode, `Systems/1/SecureBoot` turns UEFI Secure Boot on and off (as used by Ironic's `redfish` management interface):

```bash
# Boot with Secure Boot from the next power on / reset
curl -k -u admin:password -X PATCH https://localhost/redfish/v1/Systems/1/SecureBoot \
  -H 'Content-Type: application/json' -d '{"SecureBootEnable": true}'
curl -k -u admin:password -X POST https://localhost/redfish/v1/Systems/1/Actions/ComputerSystem.Reset \
  -H 'Content-Type: application/json' -d '{"ResetType": "ForceRestart"}'
# SecureBootCurrentBoot is now "Enabled"
curl -k -u admin:password https://localhost/redfish/v1/Systems/1/SecureBoot
# Reset the keys to the defaults (Microsoft keys)
curl -k -u admin:password -X POST https://localhost/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys \
  -H 'Content-Type: application/json' -d '{"ResetKeysType": "ResetAllKeysToDefault"}'
```

- With `SecureBootEnable`, QEMU is started with the 4 MB Secure Boot OVMF `OVMF_CODE_4M.secboot.fd` from the directory of the configured code, for a 2 MB `OVMF_CODE.fd` too (Debian's 2 MB `OVMF_CODE.secboot.fd` hangs before any output under QEMU). Its VARS is a separate file next to the configured one (e.g. `/vm/OVMF_VARS.secboot.fd` for `/vm/OVMF_VARS.fd`), copied from `OVMF_VARS_4M.ms.fd` (Microsoft keys enrolled) when missing or of another size. `-machine` gets `smm=on` and `-global driver=cfi.pflash01,property=secure,value=on` is added, as the Secure Boot OVMF requires SMM. Turning it off boots the original code and VARS again; both VARS files are kept.
- `SecureBootEnable` applies from the next time QEMU starts (as in Redfish, from the next reset): power on, `PowerCycle`, `GracefulRestart`, and `ForceRestart`, which restarts QEMU instead of a plain `system_reset` when the setting changed. `SecureBootCurrentBoot` is `Enabled` when the running (or last started) QEMU booted the Secure Boot OVMF. `SecureBootMode` is `UserMode` (a PK is enrolled).
- `SecureBootEnable` takes `true` / `false` only (else `400`), and enabling returns `400` if the Secure Boot code or the `.ms.fd` VARS template is missing. The resource has an ETag (`If-Match` is checked if sent).
- `ResetKeys` only takes `ResetAllKeysToDefault` (else `400`). It deletes the Secure Boot VARS, which is copied from the template again at the next start; a VM running with Secure Boot keeps its keys until the next reset, which restarts QEMU.
- `VM_SECURE_BOOT` sets the initial `SecureBootEnable`. A PATCHed value is kept in memory only: after qemu-bmc restarts it is `VM_SECURE_BOOT` again (the VARS files stay).
- In BIOS mode and legacy mode `Systems/1` has no `SecureBoot` link and `/SecureBoot` returns `404`.

#### System identity (SMBIOS)

The guest's SMBIOS and Redfish show the same manufacturer, model and serial number, so a management tool can match the machine it sees over Redfish with what the OS sees (`dmidecode`, Ironic inspection).

- `VM_MANUFACTURER`, `VM_MODEL`, `VM_SERIAL_NUMBER` and `VM_SKU` are reported as `Manufacturer`, `Model`, `SerialNumber` and `SKU` on `Systems/1` (`Chassis/1` has the first three).
- In process management mode qemu-bmc starts QEMU with `-smbios type=1` (system), `type=2` (baseboard) and `type=3` (chassis) holding the same values. Commas in values are escaped (`,,`) as QEMU requires.
- An `-smbios type=N` you pass yourself (e.g. in `QEMU_EXTRA_ARGS`) wins: qemu-bmc then does not add that type. Redfish still reports the environment variables, so set them to the same values or the two will differ.
- In legacy mode the external QEMU's SMBIOS cannot be changed; the values are only reported via Redfish.

### Authentication and sessions

Every request needs credentials (`IPMI_USER` / `IPMI_PASS`), except `GET /redfish`, `GET /redfish/v1` and the login request. Use HTTP Basic auth, or log in once and send the session token in `X-Auth-Token`:

```bash
# Log in: the token is in the X-Auth-Token header, the session URI in Location
curl -sk -D - -o /dev/null -H 'Content-Type: application/json' \
  -d '{"UserName": "admin", "Password": "password"}' \
  https://localhost/redfish/v1/SessionService/Sessions

curl -sk -H "X-Auth-Token: $TOKEN" https://localhost/redfish/v1/Systems/1

# Log out
curl -sk -X DELETE -H "X-Auth-Token: $TOKEN" https://localhost/redfish/v1/SessionService/Sessions/1
```

- A session expires after `SessionTimeout` seconds without use (1800 by default; `PATCH /redfish/v1/SessionService` with `{"SessionTimeout": n}`, 30–86400).
- Sessions are kept in memory: they are lost when qemu-bmc restarts. Up to 64 sessions; creating more returns `503` until one is deleted or expires.

### IPMI

```bash
# Check power state
ipmitool -I lanplus -H localhost -U admin -P password chassis status

# Power on
ipmitool -I lanplus -H localhost -U admin -P password chassis power on

# Power off
ipmitool -I lanplus -H localhost -U admin -P password chassis power off

# Set PXE boot
ipmitool -I lanplus -H localhost -U admin -P password chassis bootdev pxe
```

### noVNC

A browser-based VNC console is available on the same HTTP port as the Redfish API. No extra port or VNC viewer software is required.

Open a browser and navigate to:

```
https://localhost/novnc/
```

The browser will prompt for Basic Auth (same credentials as Redfish). After authentication, the noVNC UI loads and connects to QEMU's VNC server automatically.

| URL | Description |
|-----|-------------|
| `GET /novnc/` | Redirects to noVNC UI (`/novnc/vnc.html`) |
| `GET /novnc/vnc.html` | noVNC web UI |
| `GET /websockify` | WebSocket-to-VNC proxy (used internally by noVNC) |

The VNC target address is controlled by the `VNC_ADDR` environment variable (default: `localhost:5900`).

### VM IPMI (In-Band)

qemu-bmc supports QEMU's `ipmi-bmc-extern` device for in-band IPMI from the guest OS. This enables MaaS commissioning scripts to configure BMC users, LAN settings, and channel access from within the VM. Users created in-band are automatically available for out-of-band IPMI and Redfish authentication.

Set `VM_IPMI_ADDR` to enable the VM IPMI chardev listener:

```bash
export VM_IPMI_ADDR=:9002
```

Configure QEMU to connect to qemu-bmc:

```bash
qemu-system-x86_64 \
  -chardev socket,id=ipmi0,host=localhost,port=9002,reconnect=10 \
  -device ipmi-bmc-extern,id=bmc0,chardev=ipmi0 \
  -device isa-ipmi-kcs,bmc=bmc0 \
  ...
```

The guest OS can then use standard `ipmitool` commands via the KCS interface:

```bash
# Inside the VM
ipmitool user set name 3 newuser
ipmitool user set password 3 newpass
ipmitool channel setaccess 1 3 callin=on ipmi=on link=on privilege=4
ipmitool user enable 3
```

### Fault injection

For testing how management tools cope with a misbehaving BMC, qemu-bmc can inject faults: no response, delays, errors, and power actions that fail or do nothing. Faults are managed through a separate HTTP API (not Redfish, so that Redfish itself can be made unresponsive), enabled by `FAULT_API_ADDR` (e.g. `:8623`; publish the port too). It is plain HTTP; set `FAULT_API_TOKEN` to require `Authorization: Bearer <token>`. Nothing about faults is shown over Redfish or IPMI.

```bash
docker run ... -p 8623:8623 -e FAULT_API_ADDR=:8623 -e FAULT_API_TOKEN=secret ghcr.io/tjst-t/qemu-bmc
T='Authorization: Bearer secret'

# Systems/... answers 503 (Redfish error body)
curl -H "$T" -d '{"kind":"redfish-error","status":503,"path":"/redfish/v1/Systems"}' http://localhost:8623/faults
# → 201 {"id":"1","kind":"redfish-error","target":"redfish",...}

# The next 3 chassis control requests (IPMI) get no response
curl -H "$T" -d '{"kind":"ipmi-drop","netfn":"0x00","cmd":"0x02","count":3}' http://localhost:8623/faults

# ForceOff is accepted (204 / cc 0x00) but the VM keeps running
curl -H "$T" -d '{"kind":"power-action-ignored","action":"ForceOff"}' http://localhost:8623/faults

# The BMC "reboots": neither Redfish nor IPMI answers for 60 s
curl -H "$T" -d '{"kind":"bmc-unavailable","durationSeconds":60}' http://localhost:8623/faults

curl -H "$T" http://localhost:8623/faults                # list the active faults
curl -H "$T" -X DELETE http://localhost:8623/faults/1    # delete one
curl -H "$T" -X DELETE http://localhost:8623/faults      # delete all
```

| Method | Path | Description |
|--------|------|-------------|
| GET | `/faults` | Active faults (with `id`, `applied` = times applied, remaining `count`, `expiresAt`) |
| POST | `/faults` | Add a fault (JSON below). 201 with the fault and its `id`; 400 if invalid |
| DELETE | `/faults/{id}` | Delete a fault (204; 404 if unknown) |
| DELETE | `/faults` | Delete all faults (204) |

`FAULTS` takes a JSON array of faults active from startup (it works without `FAULT_API_ADDR` too), e.g. `FAULTS='[{"kind":"graceful-ignored"}]'`.

A fault is `{"kind": ..., conditions..., "count": N, "durationSeconds": S}`: `count` is how many times it applies before it is removed, `durationSeconds` how long it lasts (both unlimited if omitted). `target` (`redfish`, `ipmi`, `power`, `bmc`) is optional and filled in from `kind`. Faults are checked in the order added and the first matching one applies. Each application is logged as `fault: <id> <kind> applied to <request>`.

| Kind | Effect | Conditions / parameters |
|------|--------|-------------------------|
| `redfish-delay` | Respond `delayMs` later | `delayMs` (required), `method`, `path` |
| `redfish-error` | Respond with HTTP `status` (default 500) and a Redfish error body; the request is not carried out | `status`, `method`, `path` |
| `redfish-hang` | No response: the connection stays open until the client gives up, the fault is deleted or after `hangSeconds` (default 300), then it is closed | `hangSeconds`, `method`, `path` |
| `redfish-drop` | Close the TCP connection at once (reset) | `method`, `path` |
| `ipmi-drop` | No response to the IPMI request | `netfn`, `cmd`, `includeSession` |
| `ipmi-delay` | Respond `delayMs` later (other requests are not held back) | `delayMs` (required), `netfn`, `cmd`, `includeSession` |
| `ipmi-error` | Respond with `completionCode` (default `0xFF`) without carrying the command out | `completionCode`, `netfn`, `cmd`, `includeSession` |
| `power-action-fail` | A power action (Redfish `ComputerSystem.Reset`, IPMI chassis control) fails: Redfish 500, IPMI `completionCode` (default `0xFF`); nothing is done | `action`, `completionCode` |
| `power-action-ignored` | A power action reports success (204, cc 0x00) but nothing is done — "the BMC took it, the machine did not move" | `action` |
| `graceful-ignored` | `GracefulShutdown` / `GracefulRestart` (IPMI `power soft`) does not send the ACPI power button event to the VM, like an OS that does not shut down: `PoweringOff`, then back to `On` after `GRACEFUL_TIMEOUT` | `action` |
| `bmc-unavailable` | Neither Redfish nor IPMI responds (Redfish connections are held open until the fault ends, then closed), like a rebooting BMC | `durationSeconds` |

- `method`: HTTP method, any if omitted. `path`: a path prefix (`/redfish/v1/Systems`), or a glob if it has `*`, `?` or `[` (`/redfish/v1/Systems/*/Actions/*`; `*` does not match `/`); any if omitted. No paths are exempt (the service root, sessions, noVNC included) — narrow it with `path` as needed.
- `netfn`, `cmd`: number or string (`"0x06"`); any if omitted. Without `includeSession: true`, the messages opening/closing a session (ASF ping, RMCP+ Open Session/RAKP, Get Channel Auth Capabilities, Get Session Challenge, Activate Session, Set Session Privilege, Close Session, Get Channel Cipher Suites) are not affected, so that e.g. `ipmitool` gets a session and then times out on the command. With `netfn`/`cmd`, Open Session/RAKP and ASF ping never match (they are no IPMI commands).
- `action`: the Redfish `ResetType` as requested (`On`, `ForceOff`, `GracefulShutdown`, `ForceRestart`, `GracefulRestart`, `PowerCycle`, `ForceOn`, `Nmi`, `PushPowerButton`); IPMI chassis control maps to `ForceOff` (off), `On` (on), `PowerCycle` (cycle), `ForceRestart` (reset), `GracefulShutdown` (soft), `Nmi` (diag). Any if omitted.
- With `FAULT_API_ADDR` or `FAULTS` set, Redfish serves HTTP/1.1 only (an HTTP/2 connection cannot be dropped as a whole).

## Redfish Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/redfish` | Protocol versions (no auth) |
| GET | `/redfish/v1` | ServiceRoot (no auth) |
| GET | `/redfish/v1/SessionService` | Session service (SessionTimeout) |
| PATCH | `/redfish/v1/SessionService` | Change SessionTimeout (30–86400 s) |
| GET | `/redfish/v1/SessionService/Sessions` | Session collection |
| POST | `/redfish/v1/SessionService/Sessions` | Log in (no auth; 201, X-Auth-Token and Location headers) |
| GET | `/redfish/v1/SessionService/Sessions/{id}` | Session |
| DELETE | `/redfish/v1/SessionService/Sessions/{id}` | Log out (204) |
| GET | `/redfish/v1/Systems` | System collection |
| GET | `/redfish/v1/Systems/1` | Computer system (Manufacturer, Model, SerialNumber, SKU (if `VM_SKU` is set), Status, Processors, Storage, SimpleStorage, VirtualMedia; Links: ManagedBy Managers/1, Chassis Chassis/1) |
| PATCH | `/redfish/v1/Systems/1` | Boot device override (`BootSourceOverrideTarget` incl. `UefiHttp`, `Boot.HttpBootUri`; see [UEFI HTTP boot](#uefi-http-boot)) |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | Power control |
| GET/PATCH | `/redfish/v1/Systems/1/SecureBoot` | Secure Boot (`SecureBootEnable`, `SecureBootCurrentBoot`, `SecureBootMode`; UEFI in process management mode only, else `404`; see [Secure Boot](#secure-boot)) |
| POST | `/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys` | Reset the Secure Boot keys (`ResetAllKeysToDefault`) |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces` | NIC collection |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces/{n}` | NIC (MACAddress, LinkStatus) |
| GET | `/redfish/v1/Systems/1/Processors` | Processor collection (CPU1; empty if the CPU count is unknown) |
| GET | `/redfish/v1/Systems/1/Processors/CPU1` | Processor (x86, x86-64; TotalCores/TotalThreads = VM CPU count) |
| GET | `/redfish/v1/Systems/1/Storage` | Storage collection (Storage/1; empty when no disk is known, e.g. legacy mode) |
| GET | `/redfish/v1/Systems/1/Storage/1` | Storage with one drive per guest disk, and Volumes |
| GET | `/redfish/v1/Chassis/1/Drives` | Drive collection (the guest disks; `Chassis/1` links to it when there are disks) |
| GET | `/redfish/v1/Chassis/1/Drives/Disk{n}` | Drive (CapacityBytes from `qemu-img info` at start, MediaType HDD, Protocol). Also served at the deprecated `/redfish/v1/Systems/1/Storage/1/Drives/Disk{n}` |
| GET | `/redfish/v1/Systems/1/Storage/1/Volumes` | Volume collection (one volume per guest disk; empty when no disk is known) |
| GET | `/redfish/v1/Systems/1/Storage/1/Volumes/{n}` | Volume (CapacityBytes, Name, Status, Links.Drives → Disk{n}; no RAIDType). Read by Ironic inspection (sushy `volumes_sizes_bytes`) |
| GET | `/redfish/v1/Systems/1/SimpleStorage` | SimpleStorage collection (deprecated, read by Ironic inspection) |
| GET | `/redfish/v1/Systems/1/SimpleStorage/1` | Devices: Name and CapacityBytes of each guest disk |
| GET | `/redfish/v1/Systems/1/VirtualMedia` | VirtualMedia collection (same CD drive as Managers/1/VirtualMedia) |
| GET | `/redfish/v1/Systems/1/VirtualMedia/CD1` | VirtualMedia resource (same as Managers/1/VirtualMedia/CD1) |
| POST | `/redfish/v1/Systems/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia` | Insert an ISO (204) |
| POST | `/redfish/v1/Systems/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia` | Eject media (204) |
| GET | `/redfish/v1/Managers` | Manager collection |
| GET | `/redfish/v1/Managers/1` | BMC manager (ManagerType BMC, Manufacturer and Model `qemu-bmc`, FirmwareVersion = qemu-bmc version, Status; Links: ManagerForServers Systems/1, ManagerInChassis Chassis/1) |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia collection (deprecated location; same drive as Systems/1/VirtualMedia) |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia resource (ImageName only while inserted) |
| POST | `.../VirtualMedia.InsertMedia` | Insert an ISO (container path or HTTP(S) URL; 204) |
| POST | `.../VirtualMedia.EjectMedia` | Eject media (204) |
| GET | `/redfish/v1/Chassis` | Chassis collection |
| GET | `/redfish/v1/Chassis/1` | Chassis resource (PowerState same as Systems/1, Manufacturer, Model and SerialNumber same as Systems/1, Status; Links: ComputerSystems Systems/1, ManagedBy Managers/1) |
| GET | `/novnc/` | Redirect to noVNC UI |
| GET | `/novnc/vnc.html` | Browser-based VNC console |
| GET | `/websockify` | WebSocket-to-VNC proxy |

### Reset types

`ComputerSystem.Reset` returns 204 as soon as the action is started.

| ResetType | Behavior | `PowerState` |
|-----------|----------|--------------|
| `On` | Power on | `On` |
| `ForceOff` | Power off immediately | `Off` |
| `ForceOn` | Same as `On` | `On` |
| `ForceRestart` | Hard reset | `On` |
| `PowerCycle` | Power off immediately, then on again. In process management mode QEMU is restarted, so a boot override and VirtualMedia apply as on a power on (legacy mode: hard reset). If off, power on | `On` |
| `Nmi` | Inject an NMI into the guest (QMP `inject-nmi`, e.g. Ironic `inject_nmi` for a crash dump). Does not end a `GracefulShutdown` in progress. 409 if the VM is off | unchanged |
| `PushPowerButton` | If off, same as `On`; if on, same as `GracefulShutdown` | as `On` / `GracefulShutdown` |
| `GracefulShutdown` | Press the ACPI power button. The VM is never forced off | `PoweringOff` until the guest powers off, then `Off`. If the guest has not powered off after `GRACEFUL_TIMEOUT` seconds (e.g. it ignores ACPI), back to `On` (logged) |
| `GracefulRestart` | Press the ACPI power button, then power the VM back on once the guest has powered off | `PoweringOff`, then `PoweringOn` while powering on, then `On`. Back to `On` after `GRACEFUL_TIMEOUT` seconds if the guest does not power off |

Another power action (Redfish or IPMI) during `PoweringOff` / `PoweringOn` ends the graceful action; the state then follows the new action. IPMI `chassis power soft` behaves like `GracefulShutdown`, `chassis power cycle` like `PowerCycle` and `chassis power diag` like `Nmi`; `chassis power status` reports `PoweringOff` / `PoweringOn` as on.

## IPMI Commands

| Command | Description |
|---------|-------------|
| Get Device ID | BMC identity |
| Get Self Test Results | Always "passed" (`chassis selftest`, `mc selftest`) |
| Get BMC Global Enables | Fixed value: event message buffer and system event logging enabled (`mc getenables`) |
| Get Channel Auth Capabilities | Auth type negotiation |
| Get Channel Cipher Suites | Cipher suite list (suites 3 and 17) |
| Get Chassis Status | Power state query |
| Chassis Control | Power on/off/cycle/reset, soft off, diag (NMI; `0xD5` if the VM is off) |
| Set/Get Boot Options | Boot device override |
| Get SDR Repository Info / Reserve SDR Repository / Get SDR | One record only: the BMC's own MC Device Locator, so `sdr list` is empty (ipmitool rebuilds an empty repository and prints errors) |
| Get Device SDR Info / Reserve Device SDR Repository / Get Device SDR | No device SDRs |
| Get SEL Info / Reserve SEL / Get SEL Entry / Clear SEL | Always empty SEL (`sel info`, `sel list`, `sel clear`) |
| Get FRU Inventory Area Info / Read FRU Data | FRU 0 only: Product Manufacturer `QEMU`, Product Name `qemu-bmc VM`, Product Serial = system UUID (`fru print`) |

## Environment Variables

### BMC Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `QMP_SOCK` | `/var/run/qemu/qmp.sock` | QMP socket path |
| `IPMI_USER` | `admin` | Authentication username |
| `IPMI_PASS` | `password` | Authentication password |
| `REDFISH_PORT` | `443` | Redfish HTTPS port |
| `IPMI_PORT` | `623` | IPMI UDP port |
| `SERIAL_ADDR` | `localhost:9002` | Process management mode only: QEMU guest serial console is exposed as a TCP server at this address. SOL is **not implemented** (qemu-bmc itself does not connect to it). The default port 9002 collides with the `VM_IPMI_ADDR` example — use another port (e.g. `localhost:9003`) when enabling both |
| `TLS_CERT` | (auto-generated) | TLS certificate path; if unset, a self-signed ECDSA cert is generated automatically |
| `TLS_KEY` | (auto-generated) | TLS key path; if unset, generated together with `TLS_CERT` |
| `VM_BOOT_MODE` | `bios` | Firmware the container starts QEMU with (`bios` or `uefi`, chosen by `entrypoint.sh`). In process management mode it is also the initial Redfish `BootSourceOverrideMode` (`Legacy` / `UEFI`) and IPMI boot flags EFI bit |
| `VM_IPMI_ADDR` | (empty, disabled) | VM IPMI chardev listen address (e.g., `:9002`) |
| `VNC_ADDR` | `localhost:5900` | QEMU VNC TCP address for noVNC proxy |
| `POWER_ON_AT_START` | `false` | Power on VM automatically at startup (useful for non-MAAS setups) |
| `GRACEFUL_TIMEOUT` | `120` | Seconds `GracefulShutdown` / `GracefulRestart` (and IPMI `chassis power soft`) report `PoweringOff` before going back to `On` when the guest does not power off |
| `VMEDIA_DIR` | `/vm/vmedia` if `/vm` is writable, else `/tmp/vmedia` | Where VirtualMedia HTTP(S) images are downloaded. Also used for the UEFI HTTP boot image (`Boot.HttpBootUri`). The previous download is deleted when the medium (or URI) is replaced or ejected, and leftovers from an earlier run (`cd1-*.iso`, `httpboot-*.iso`) are deleted at startup. In legacy mode the external QEMU must be able to read this directory (same filesystem) |
| `VMEDIA_INSECURE_TLS` | `true` | Skip certificate verification when downloading HTTPS images. With `false`, the system CAs are used; add your own CA with `SSL_CERT_FILE` |
| `VMEDIA_TIMEOUT` | `600` | Seconds a power on or reset requested during a VirtualMedia (or UEFI HTTP boot image) download waits for it before booting without the image |
| `FAULT_API_ADDR` | (empty, disabled) | Listen address of the fault injection API (plain HTTP, e.g. `:8623`). See [Fault injection](#fault-injection) |
| `FAULT_API_TOKEN` | (empty) | Bearer token required by the fault API; no authentication if empty |
| `FAULTS` | (empty) | JSON array of faults active from startup |
| `SYSTEM_UUID` | (derived) | Fixed system UUID, passed to QEMU as `-uuid` and reported by Redfish `Systems/1` `UUID` and IPMI `mc guid`. If unset, it is read from `SYSTEM_UUID_FILE`, or derived from the hostname and saved there. Set it (or use a fixed `--hostname` / a mounted `/vm`) to keep it across container re-creation |
| `SYSTEM_UUID_FILE` | `<VM_DISK dir>/system-uuid` | Where a derived UUID is persisted |
| `VM_MACS` | (derived) | Comma-separated MAC addresses of the VM NICs, in order (an empty entry is derived). Unset MACs are derived from the system UUID, so they stay fixed per node and do not overlap between nodes. Reported by Redfish `Systems/1/EthernetInterfaces` |
| `VM_MANUFACTURER` | `QEMU` | System manufacturer. In process management mode put into the guest SMBIOS (`-smbios type=1/2/3`); reported as Redfish `Systems/1` / `Chassis/1` `Manufacturer` |
| `VM_MODEL` | `qemu-bmc` | System model: SMBIOS product (type 1 and 2), Redfish `Model` |
| `VM_SERIAL_NUMBER` | (derived) | System serial number: SMBIOS serial (type 1, 2 and 3), Redfish `SerialNumber`. Unset: `QBMC-` + the first 10 hex digits of the system UUID in upper case (stable as long as the UUID is; legacy mode without `SYSTEM_UUID` reports none) |
| `VM_SKU` | (empty) | System SKU: SMBIOS sku (type 1 and 3), Redfish `Systems/1` `SKU`. Empty = not set |
| `VM_SECURE_BOOT` | `false` | Initial Redfish `SecureBootEnable` (`true` boots the Secure Boot OVMF; needs `VM_BOOT_MODE=uefi`, else qemu-bmc exits with an error; ignored in legacy mode). A value PATCHed over Redfish is kept in memory only and reverts to this one when qemu-bmc restarts. See [Secure Boot](#secure-boot) |

### Container Configuration

These variables are used by `docker/entrypoint.sh` to construct QEMU arguments:

| Variable | Default | Description |
|----------|---------|-------------|
| `VM_MEMORY` | `2048` | VM memory in MB |
| `VM_CPUS` | `2` | Number of VM CPUs |
| `ENABLE_KVM` | `true` | Use KVM acceleration (falls back to TCG) |
| `VNC_PORT` | `5900` | VNC display port |
| `VM_DISK` | `/vm/disk.qcow2` | VM disk image path |
| `VM_DISK_SIZE` | (empty) | If set (e.g. `20G`) and `VM_DISK` does not exist, create it as qcow2 of this size at start (an existing disk is not touched) |
| `VM_CDROM` | (empty) | ISO initially inserted in the VirtualMedia CD drive (the drive exists, empty, without it) |
| `VM_BOOT` | `c` | Boot device (c=disk, d=cdrom, n=network) |
| `VM_BOOT_MENU_TIMEOUT` | `0` | Boot menu timeout in ms (0=disabled) |
| `VM_NETWORKS` | (empty) | Comma-separated host interfaces for TAP passthrough (waits up to 30s for interfaces to appear, useful for containerlab; the container exits with an error if one does not appear). Without it, eth2+ are passed through. Requires `/dev/net/tun` (`--device /dev/net/tun`) and `NET_ADMIN`; the container exits with an error if the TUN device is unusable |
| `VM_NET_MTU` | (empty) | MTU for the VM NICs, e.g. `9000` for FCoE or other jumbo frames. Sets the TAP devices and bridges to it, raises the passed-through container interfaces to it if smaller (larger ones are kept), and adds `host_mtu=` to the virtio-net NICs so the guest uses it. Empty leaves all MTUs alone. The container exits with an error if it is not a number (68-65535) or cannot be applied |
| `QEMU_EXTRA_ARGS` | (empty) | Additional QEMU arguments |
| `DEBUG` | `false` | Enable debug output |

## Development

```bash
# Download noVNC static files (required once before go build/test)
make download-novnc

# Build
go build ./cmd/qemu-bmc
make docker-build

# Unit tests
go test ./... -count=1
go test ./... -race -count=1

# Static analysis
go vet ./...

# Container integration tests
make container-test         # Quick smoke tests
make container-test-all     # All 115 tests (10 categories)

# Ironic end-to-end test: deploy Debian with a real Ironic (Redfish + IPMI drivers, ~20 min)
make ironic-test            # see tests/ironic/README.md
```

## Architecture

```
cmd/qemu-bmc/main.go          # Entrypoint
internal/
  qmp/                         # QMP socket client
  qemu/                        # QEMU process management
  machine/                     # VM state management
  redfish/                     # Redfish HTTP server (gorilla/mux)
  ipmi/                        # IPMI UDP server + VM chardev server (RMCP/RMCP+)
  novnc/                       # noVNC static files (embedded) + WebSocket-to-VNC proxy
  bmc/                         # BMC configuration state (users, LAN, channels)
  config/                      # Environment variable config
  fault/                       # Fault injection (fault API, Redfish/IPMI/power faults)
docker/
  Dockerfile                   # Multi-stage build (Go builder + Debian runtime)
  entrypoint.sh                # Environment variables → QEMU args
  setup-network.sh             # TAP/bridge network setup
tests/                         # Bash-based container integration tests
  ironic/run.sh                # Ironic end-to-end deploy test (make ironic-test)
containerlab/                  # containerlab topology examples (see containerlab/README.md)
```

## License

qemu-bmc is licensed under the [Apache License 2.0](LICENSE).

The binary and container image embed third-party files that keep their own licenses (see [NOTICE](NOTICE)):

| Component | License |
|-----------|---------|
| [noVNC](https://github.com/novnc/noVNC) 1.5.0 (`internal/novnc/static/`) | MPL-2.0 (core JS); HTML/CSS: BSD-2-Clause; fonts: OFL-1.1; images: CC BY-SA 3.0 |
| [pako](https://github.com/nodeca/pako) (bundled in noVNC `vendor/pako/`) | MIT |

---

# qemu-bmc (日本語)

QEMU VM を Redfish API (HTTPS) と IPMI over LAN (UDP) の両方で制御する Go シングルバイナリ。

```
┌──────────────────────────────────────────┐
│ OCI コンテナ                              │
│                                          │
│  ┌───────────┐   ┌──────────────────┐   │
│  │ qemu-bmc  │   │      QEMU        │   │
│  │ (Go)      │   │                  │   │
│  │           │   │                  │   │
│  │ :443/tcp  │   │                  │   │
│  │  Redfish  ├──►│  QMP Socket      │   │
│  │           │   │                  │   │
│  │ :623/udp  │   │                  │   │
│  │  IPMI     ├──►│                  │   │
│  │           │   │                  │   │
│  │ :9002/tcp │   │  ipmi-bmc-extern │   │
│  │  VM IPMI  │◄──┤  (KCS)          │   │
│  └───────────┘   └──────────────────┘   │
└──────────────────────────────────────────┘
```

## 機能

- **Redfish API** - ServiceRoot, Systems, Managers, VirtualMedia, Chassis (gofish 互換)
- **IPMI over LAN** - RMCP/RMCP+, 暗号スイート 3（RAKP-HMAC-SHA1）と 17（RAKP-HMAC-SHA256、ipmitool の `-C 17`）, AES-CBC-128 暗号化
- **VM IPMI（イン・バンド）** - QEMU `ipmi-bmc-extern` KCS インターフェースによるゲスト OS IPMI（MaaS コミッショニング対応）
- **noVNC** - Redfish HTTP ポートでブラウザから VNC コンソールにアクセス（追加ポート不要）
- **QMP 制御** - 電源操作、ブートデバイス変更、VirtualMedia マウント
- **UEFI HTTP ブートと Secure Boot** - 起動先 `UefiHttp` と `Boot.HttpBootUri`。`Systems/1/SecureBoot` で OVMF を Secure Boot 用（Microsoft の鍵入り）に切り替える
- **故障の注入** - 応答しない・遅い・エラーを返す Redfish・IPMI や、失敗する・何もしない電源操作を別の API で起こせる（管理ツールの異常系の試験に）
- **互換性** - MAAS, Tinkerbell/Rufio

## クイックスタート

### Docker Compose（プロセス管理モード）

qemu-bmc がコンテナ内で QEMU プロセスを直接管理します:

```bash
# VM ディスク作成（VM_DISK_SIZE=20G を設定すればコンテナが作る）
mkdir -p vm
qemu-img create -f qcow2 vm/disk.qcow2 20G

# 起動
docker compose up -d

# 状態確認
ipmitool -I lanplus -H localhost -U admin -P password mc info
curl -k -u admin:password https://localhost/redfish/v1/Systems/1
```

### Docker ビルド

```bash
docker build -t qemu-bmc -f docker/Dockerfile .
docker run --rm --device /dev/kvm --device /dev/net/tun --cap-add NET_ADMIN \
  -p 5900:5900 -p 623:623/udp -p 443:443 \
  -v ./vm:/vm \
  -e VM_MEMORY=4096 -e VM_CPUS=4 \
  qemu-bmc
```

### バイナリ（レガシーモード）

既存の QEMU インスタンスに QMP ソケット経由で接続する。QEMU は `-no-shutdown`（と `-qmp unix:/var/run/qemu/qmp.sock,server,nowait` などの QMP ソケット）を付けて起動すること。付けないと、ゲストが電源を切ったとき（`GracefulShutdown` のあとなど）に QEMU が終了し、qemu-bmc から電源状態を返せず、電源を入れ直すこともできない:

```bash
make download-novnc
go build -o qemu-bmc ./cmd/qemu-bmc

export QMP_SOCK=/var/run/qemu/qmp.sock
export IPMI_USER=admin
export IPMI_PASS=password
./qemu-bmc
```

## 使い方

### Redfish

```bash
# サービスルート
curl -k -u admin:password https://localhost/redfish/v1/

# 電源状態の確認
curl -k -u admin:password https://localhost/redfish/v1/Systems/1

# 電源オン
curl -k -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"ResetType": "On"}' \
  https://localhost/redfish/v1/Systems/1/Actions/ComputerSystem.Reset

# 電源オフ（強制）
curl -k -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"ResetType": "ForceOff"}' \
  https://localhost/redfish/v1/Systems/1/Actions/ComputerSystem.Reset

# PXE ブート設定
curl -k -u admin:password -X PATCH \
  -H 'Content-Type: application/json' \
  -d '{"Boot": {"BootSourceOverrideEnabled": "Once", "BootSourceOverrideTarget": "Pxe"}}' \
  https://localhost/redfish/v1/Systems/1

# 仮想 CD ドライブに ISO を差し（コンテナ内のパス）、一度だけ CD から起動する
curl -k -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"Image": "/iso/boot.iso", "Inserted": true}' \
  https://localhost/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia
curl -k -u admin:password -X PATCH \
  -H 'Content-Type: application/json' \
  -d '{"Boot": {"BootSourceOverrideEnabled": "Once", "BootSourceOverrideTarget": "Cd"}}' \
  https://localhost/redfish/v1/Systems/1

# 取り出す
curl -k -u admin:password -X POST \
  https://localhost/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia
```

#### VirtualMedia

- CD ドライブは `/redfish/v1/Systems/1/VirtualMedia/CD1`（Redfish 2021.1 以降の推奨の場所。Ironic/sushy はまずここを見る）と `/redfish/v1/Managers/1/VirtualMedia/CD1`（非推奨の場所。gofish・bmclib・既存の利用者のため残す）の両方にある。どちらも同じ1つのドライブで、片方から差せばもう片方でも差さって見える。`@odata.id` と Actions の target は読んだ URI に合わせる。
- プロセス管理モード（コンテナ）では、VM に CD ドライブが常に付く。`VM_CDROM` が無ければ空、あればその ISO が最初に差さった状態になる。
- `Image` はコンテナ内のパス（`/iso/boot.iso` または `file:///iso/boot.iso`。ISO はバインドマウントする（例: `-v ./iso:/iso:ro`））か、`http://` / `https://` の URL。`Image` が空・ファイルが無い・未対応の形式なら `400`。InsertMedia・EjectMedia は成功すると `204 No Content`。
- HTTP(S) の URL は qemu-bmc が `VMEDIA_DIR` へダウンロードしてから差す（イメージの QEMU は URL を直接読めない）。InsertMedia はダウンロードを待たずに `204` を返し、ダウンロード中も `GET` は `Image` に URL、`Inserted: true` を返す。InsertMedia の `UserName` / `Password` は HTTP の Basic 認証に使う。URL に認証情報を書いてもよい（`https://user:pass@host/boot.iso`）。そのときパスワードは `Image` とログでは `xxxxx` に伏せる。自己署名のサーバが多いので、`VMEDIA_INSECURE_TLS=false` にしない限り証明書は検証しない。自前の CA で検証するなら、CA をマウントして `SSL_CERT_FILE` を指す（例 `-e SSL_CERT_FILE=/certs/ca.pem`）。
- ダウンロード中に電源オン・リセット（Redfish の `On` / `ForceOn` / `ForceRestart` / `PowerCycle`、IPMI の `chassis power on` / `cycle` / `reset`）が来たら、ダウンロードが終わってから（最大 `VMEDIA_TIMEOUT` 待つ）実行するので、ISO から起動できる。要求そのものはすぐ返り、その間の電源状態は `PoweringOn`。別の電源操作（`ForceOff` など）が来たら取りやめる。ダウンロードに失敗したら（`404` など）中身を外し（`Inserted: false`）、理由をログに出す。VM が動いている間に終わったら QMP で差す。別のイメージを差す・Eject すると、ダウンロードはやめ、前に落としたファイルは消す。
- 差した中身は電源オフ中も qemu-bmc が覚えていて、QEMU を起動するときに付ける。VM が動いていれば QMP でその場で差し替える。
- 起動先 `Cd`（Redfish の `BootSourceOverrideTarget`、IPMI の `chassis bootdev cdrom`）で、差した ISO から起動する。BIOS・UEFI のどちらでも動く（UEFI（OVMF）のため CD ドライブに `bootindex=0` を付ける）。
- 起動先 `Pxe`（Redfish、IPMI の `chassis bootdev pxe`）は、ディスクに OS が入っていても UEFI でネットワークから起動する（OVMF は `-boot n` を見ないので、最初の NIC に `bootindex=0` を付ける。BIOS は `-boot n`）。
- `GET .../VirtualMedia/CD1` は `Image`・`ImageName`・`Inserted`・`ConnectedVia`（`URI` / `NotConnected`）・`WriteProtected`（常に `true`）・`MediaTypes` を返す。
- レガシーモード（外部の QEMU）は QMP で差し替えるだけ。パスは外部 QEMU 側のファイルシステムのもの。どちらのモードも CD ドライブは `query-block` で探す（最初の取り外し可能なドライブ）。

#### UEFI HTTP ブート

起動先 `UefiHttp`（Ironic/Metal3 の `redfish-https` ブートが使う）は、`Boot.HttpBootUri` があるかで動きが変わる。

```bash
curl -k -u admin:password -X PATCH https://localhost/redfish/v1/Systems/1 \
  -H 'Content-Type: application/json' \
  -d '{"Boot": {"BootSourceOverrideTarget": "UefiHttp", "HttpBootUri": "https://10.0.0.1/boot.iso", "BootSourceOverrideEnabled": "Once", "BootSourceOverrideMode": "UEFI"}}'
# 使い終わったら URI を消す
curl -k -u admin:password -X PATCH https://localhost/redfish/v1/Systems/1 \
  -H 'Content-Type: application/json' -d '{"Boot": {"HttpBootUri": null}}'
```

- **`HttpBootUri` あり**：qemu-bmc が VirtualMedia と同じ仕組み（`VMEDIA_DIR`・`VMEDIA_INSECURE_TLS`・`VMEDIA_TIMEOUT`。URI を PATCH した時にダウンロードを始める）でイメージを落とし、VirtualMedia の `CD1` とは別の隠れた CD ドライブとして付け、そこから最初に起動する（`bootindex=0`）。VirtualMedia の一覧には出ず、`CD1` の状態も変わらない。ダウンロード中の電源オン・リセットは VirtualMedia と同じく終わるまで待つ。ダウンロードに失敗した（ログに出す）・`VMEDIA_TIMEOUT` までに終わらないときは、その起動は上書きが無いのと同じく、それなしで進む。中身は ISO（CD）として扱う。EFI の実行ファイル単体（`.efi` など）は対象外。BIOS モードでも同じ。
- **`HttpBootUri` なし**：ネットワークから起動する（`Pxe` と同じく `-boot n`）。UEFI（OVMF）では fw_cfg で PXE を切り（`opt/org.tianocore/IPv4PXESupport` / `IPv6PXESupport` を `n`）、最初の NIC に `bootindex=0` を付けて option ROM を外す（`romfile=`。外さないと iPXE が OVMF の HTTP ブートより先に動く）ので、OVMF は HTTP ブートだけを試す。起動する URL は DHCP サーバ（vendor class `HTTPClient` への応答）が渡す。BIOS モードでは `Pxe` と同じ。
- `HttpBootUri` は `http://` / `https://` の URL か、消すための `null` / `""` を受ける（それ以外は `400`）。`Boot` にいつも出し（未設定は `null`。sushy はこのキーがあるときだけ送る）、ETag にも含める。`Once` を使ったあと（起動先は `None` に戻る）も消すまで残る。もう一度設定すると落とし直し、変える・消すと落としたファイルは消す。前の起動の残り（`httpboot-*.iso`）は起動時に消す。
- 隠れた CD と fw_cfg・NIC の変更は、`UefiHttp` の上書きが効く起動にだけ付ける（`Once` を使ったあとは付かない）。
- IPMI の Get Boot Options では `UefiHttp` を PXE として答える。
- レガシーモード（外部の QEMU）では QEMU の引数を変えられないので使えない。`UefiHttp` や `HttpBootUri` を PATCH すると `400`（`null` で消すのは受け付ける）。

#### Secure Boot

プロセス管理モードで UEFI（`VM_BOOT_MODE=uefi`。OVMF を `-drive if=pflash` で使う）のとき、`Systems/1/SecureBoot` で UEFI Secure Boot を切り替えられる（Ironic の `redfish` management が使う）。

```bash
# 次の電源オン・リセットから Secure Boot で起動する
curl -k -u admin:password -X PATCH https://localhost/redfish/v1/Systems/1/SecureBoot \
  -H 'Content-Type: application/json' -d '{"SecureBootEnable": true}'
curl -k -u admin:password -X POST https://localhost/redfish/v1/Systems/1/Actions/ComputerSystem.Reset \
  -H 'Content-Type: application/json' -d '{"ResetType": "ForceRestart"}'
# SecureBootCurrentBoot が "Enabled" になる
curl -k -u admin:password https://localhost/redfish/v1/Systems/1/SecureBoot
# 鍵を既定（Microsoft の鍵）に戻す
curl -k -u admin:password -X POST https://localhost/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys \
  -H 'Content-Type: application/json' -d '{"ResetKeysType": "ResetAllKeysToDefault"}'
```

- `SecureBootEnable` が有効なら、指定した CODE と同じディレクトリの 4MB 版の Secure Boot 用 OVMF `OVMF_CODE_4M.secboot.fd` で QEMU を起動する（2MB の `OVMF_CODE.fd` のときも。Debian の 2MB 版 `OVMF_CODE.secboot.fd` は QEMU で何も出さずに止まる）。VARS は指定した VARS と同じディレクトリの別ファイル（`/vm/OVMF_VARS.fd` なら `/vm/OVMF_VARS.secboot.fd`）を使い、無いか大きさが違えば `OVMF_VARS_4M.ms.fd`（Microsoft の鍵入り）からコピーする。Secure Boot 用の OVMF は SMM が要るので、`-machine` に `smm=on` を足し、`-global driver=cfi.pflash01,property=secure,value=on` を足す。無効に戻すと元の CODE と VARS で起動する（VARS は両方とも残す）。
- `SecureBootEnable` は次に QEMU を起動するときから効く（Redfish でも「次のリセットから」）：電源オン・`PowerCycle`・`GracefulRestart`、それに `ForceRestart`（設定が変わっていれば `system_reset` ではなく QEMU を起こし直す）。`SecureBootCurrentBoot` は、動いている（または最後に起動した）QEMU が Secure Boot 用の OVMF なら `Enabled`。`SecureBootMode` は `UserMode`（PK が入っている）。
- `SecureBootEnable` は `true` / `false` だけ受ける（それ以外は `400`）。Secure Boot 用の CODE か `.ms.fd` の VARS の雛形が無いと、有効にする PATCH は `400`。ETag があり、`If-Match` を送れば確かめる。
- `ResetKeys` は `ResetAllKeysToDefault` だけ受ける（それ以外は `400`）。Secure Boot 用の VARS を消し、次の起動で雛形からコピーし直す。Secure Boot で動いている VM は次のリセットまで今の鍵のままで、そのリセットで QEMU を起こし直す。
- `VM_SECURE_BOOT` が起動時の `SecureBootEnable`。PATCH した値はメモリにだけ持つので、qemu-bmc を起動し直すと `VM_SECURE_BOOT` の値に戻る（VARS のファイルは残る）。
- BIOS モードとレガシーモードでは `Systems/1` に `SecureBoot` のリンクを出さず、`/SecureBoot` は `404`。

#### システムの識別情報（SMBIOS）

ゲストの SMBIOS と Redfish に同じメーカー・型番・シリアル番号を出す。管理ツールが Redfish で見た機械と、OS の中から見た機械（`dmidecode`、Ironic の inspect）を突き合わせられる。

- `VM_MANUFACTURER`・`VM_MODEL`・`VM_SERIAL_NUMBER`・`VM_SKU` を `Systems/1` の `Manufacturer`・`Model`・`SerialNumber`・`SKU` で返す（`Chassis/1` は前の3つ）。
- プロセス管理モードでは、QEMU を `-smbios type=1`（システム）・`type=2`（ベースボード）・`type=3`（シャーシ）に同じ値を入れて起動する。値の中の `,` は QEMU の流儀で `,,` にエスケープする。
- 自分で `-smbios type=N` を渡すと（`QEMU_EXTRA_ARGS` など）そちらが勝ち、qemu-bmc はその type を足さない。Redfish は環境変数の値を返すので、食い違わないよう同じ値を環境変数にも入れること。
- レガシーモードでは外の QEMU の SMBIOS は変えられないので、Redfish に出すだけ。

### 認証とセッション

`GET /redfish`・`GET /redfish/v1` とログインの要求のほかは、認証情報（`IPMI_USER` / `IPMI_PASS`）が要る。HTTP の Basic 認証か、一度ログインして得たセッションのトークンを `X-Auth-Token` で送る：

```bash
# ログイン：トークンは X-Auth-Token ヘッダ、セッションの URI は Location に入る
curl -sk -D - -o /dev/null -H 'Content-Type: application/json' \
  -d '{"UserName": "admin", "Password": "password"}' \
  https://localhost/redfish/v1/SessionService/Sessions

curl -sk -H "X-Auth-Token: $TOKEN" https://localhost/redfish/v1/Systems/1

# ログアウト
curl -sk -X DELETE -H "X-Auth-Token: $TOKEN" https://localhost/redfish/v1/SessionService/Sessions/1
```

- セッションは `SessionTimeout` 秒使われないと切れる（既定 1800。`PATCH /redfish/v1/SessionService` に `{"SessionTimeout": n}` で 30〜86400 に変えられる）。
- セッションはメモリにだけあり、qemu-bmc を起動し直すと消える。同時に 64 個まで。それを超えて作ると、どれかが消えるか切れるまで `503` を返す。

### IPMI

```bash
# 電源状態の確認
ipmitool -I lanplus -H localhost -U admin -P password chassis status

# 電源オン
ipmitool -I lanplus -H localhost -U admin -P password chassis power on

# 電源オフ
ipmitool -I lanplus -H localhost -U admin -P password chassis power off

# PXE ブート設定
ipmitool -I lanplus -H localhost -U admin -P password chassis bootdev pxe
```

### noVNC

Redfish API と同じ HTTP ポートでブラウザベースの VNC コンソールを利用できます。追加ポートや VNC ビューアソフトウェアは不要です。

ブラウザで以下の URL を開きます:

```
https://localhost/novnc/
```

Basic 認証のプロンプトが表示されます（Redfish と同じ認証情報）。認証後、noVNC UI が表示され、QEMU の VNC サーバーに自動的に接続します。

| URL | 説明 |
|-----|------|
| `GET /novnc/` | noVNC UI へリダイレクト (`/novnc/vnc.html`) |
| `GET /novnc/vnc.html` | noVNC Web UI |
| `GET /websockify` | WebSocket-to-VNC プロキシ（noVNC が内部で使用） |

VNC の接続先アドレスは `VNC_ADDR` 環境変数で制御します（デフォルト: `localhost:5900`）。

### VM IPMI（イン・バンド）

qemu-bmc は QEMU の `ipmi-bmc-extern` デバイスを使ったゲスト OS からのイン・バンド IPMI をサポートします。MaaS コミッショニングスクリプトが VM 内から BMC ユーザー、LAN 設定、チャネルアクセスを設定できます。イン・バンドで作成されたユーザーは、アウト・オブ・バンド IPMI および Redfish 認証でも自動的に利用可能です。

`VM_IPMI_ADDR` を設定して VM IPMI chardev リスナーを有効にします:

```bash
export VM_IPMI_ADDR=:9002
```

QEMU を qemu-bmc に接続するよう設定します:

```bash
qemu-system-x86_64 \
  -chardev socket,id=ipmi0,host=localhost,port=9002,reconnect=10 \
  -device ipmi-bmc-extern,id=bmc0,chardev=ipmi0 \
  -device isa-ipmi-kcs,bmc=bmc0 \
  ...
```

ゲスト OS 内で標準の `ipmitool` コマンドが KCS インターフェース経由で使用できます:

```bash
# VM 内
ipmitool user set name 3 newuser
ipmitool user set password 3 newpass
ipmitool channel setaccess 1 3 callin=on ipmi=on link=on privilege=4
ipmitool user enable 3
```

### 故障の注入

管理ツールが調子の悪い BMC をどう扱うかを試すため、qemu-bmc は故障を起こせる（応答なし・遅延・エラー、失敗する・何もしない電源操作）。故障は Redfish とは別の HTTP API で操作する（Redfish 自体を応答なしにできるように）。`FAULT_API_ADDR`（例 `:8623`。ポートの公開も要る）で有効になる。TLS なしの HTTP で、`FAULT_API_TOKEN` を設定すると `Authorization: Bearer <token>` が要る。Redfish・IPMI には故障のことは何も出さない。

```bash
docker run ... -p 8623:8623 -e FAULT_API_ADDR=:8623 -e FAULT_API_TOKEN=secret ghcr.io/tjst-t/qemu-bmc
T='Authorization: Bearer secret'

# Systems/... を 503 にする（Redfish の error の本文）
curl -H "$T" -d '{"kind":"redfish-error","status":503,"path":"/redfish/v1/Systems"}' http://localhost:8623/faults
# → 201 {"id":"1","kind":"redfish-error","target":"redfish",...}

# 次の 3 回の chassis control（IPMI）に応えない
curl -H "$T" -d '{"kind":"ipmi-drop","netfn":"0x00","cmd":"0x02","count":3}' http://localhost:8623/faults

# ForceOff を受け付けたように返す（204・cc 0x00）が VM は動いたまま
curl -H "$T" -d '{"kind":"power-action-ignored","action":"ForceOff"}' http://localhost:8623/faults

# BMC の再起動中: 60 秒間 Redfish も IPMI も応えない
curl -H "$T" -d '{"kind":"bmc-unavailable","durationSeconds":60}' http://localhost:8623/faults

curl -H "$T" http://localhost:8623/faults                # 効いている故障の一覧
curl -H "$T" -X DELETE http://localhost:8623/faults/1    # 1 件消す
curl -H "$T" -X DELETE http://localhost:8623/faults      # 全部消す
```

| メソッド | パス | 説明 |
|--------|------|-------------|
| GET | `/faults` | 効いている故障（`id`、効いた回数 `applied`、残りの `count`、`expiresAt` 付き） |
| POST | `/faults` | 故障を 1 件足す（JSON は下）。201 で `id` 付きの故障を返す。不正なら 400 |
| DELETE | `/faults/{id}` | 1 件消す（204。無ければ 404） |
| DELETE | `/faults` | 全部消す（204） |

`FAULTS` に JSON の配列を渡すと、起動時から効く（`FAULT_API_ADDR` なしでも効く）。例 `FAULTS='[{"kind":"graceful-ignored"}]'`。

故障の形は `{"kind": ..., 条件..., "count": N, "durationSeconds": S}`。`count` は何回効いたら消えるか、`durationSeconds` は何秒で消えるか（どちらも省略で無限）。`target`（`redfish`・`ipmi`・`power`・`bmc`）は省略でき、`kind` から決まる。故障は足した順に照合し、最初に合ったものが効く。効くたびにログに `fault: <id> <kind> applied to <要求>` を出す。

| kind | 効き目 | 条件・パラメータ |
|------|--------|-------------------------|
| `redfish-delay` | `delayMs` 遅れて応える | `delayMs`（必須）、`method`、`path` |
| `redfish-error` | HTTP `status`（既定 500）と Redfish の error の本文で返す。要求は実行しない | `status`、`method`、`path` |
| `redfish-hang` | 応えない。接続はクライアントが諦めるか、故障が消えるか、`hangSeconds`（既定 300）経つまで開いたままで、その後閉じる | `hangSeconds`、`method`、`path` |
| `redfish-drop` | TCP をすぐ閉じる（リセット） | `method`、`path` |
| `ipmi-drop` | IPMI の要求に応えない | `netfn`、`cmd`、`includeSession` |
| `ipmi-delay` | `delayMs` 遅れて応える（ほかの要求は待たせない） | `delayMs`（必須）、`netfn`、`cmd`、`includeSession` |
| `ipmi-error` | コマンドを実行せず `completionCode`（既定 `0xFF`）を返す | `completionCode`、`netfn`、`cmd`、`includeSession` |
| `power-action-fail` | 電源操作（Redfish の `ComputerSystem.Reset`、IPMI の chassis control）が失敗する。Redfish は 500、IPMI は `completionCode`（既定 `0xFF`）。何もしない | `action`、`completionCode` |
| `power-action-ignored` | 電源操作に成功（204・cc 0x00）と返すが、何もしない（「BMC は受けたのに機械が動かない」） | `action` |
| `graceful-ignored` | `GracefulShutdown` / `GracefulRestart`（IPMI の `power soft`）で ACPI の電源ボタンを VM に送らない（OS が止まらない機械）。`PoweringOff` のあと `GRACEFUL_TIMEOUT` で `On` に戻る | `action` |
| `bmc-unavailable` | Redfish も IPMI も応えない（BMC の再起動中）。Redfish の接続は故障が終わるまで開いたままで、その後閉じる | `durationSeconds` |

- `method`: HTTP メソッド。省略で全部。`path`: パスの前方一致（`/redfish/v1/Systems`）、`*`・`?`・`[` を含めば glob（`/redfish/v1/Systems/*/Actions/*`。`*` は `/` に合わない）。省略で全部。例外は作らない（サービスルート・セッション・noVNC も含む）ので、必要なら `path` で絞る。
- `netfn`・`cmd`: 数か文字列（`"0x06"`）。省略で全部。`includeSession: true` でなければ、セッションを開く・閉じるメッセージ（ASF ping、RMCP+ の Open Session・RAKP、Get Channel Auth Capabilities、Get Session Challenge、Activate Session、Set Session Privilege、Close Session、Get Channel Cipher Suites）には効かない。たとえば `ipmitool` はセッションを開けて、コマンドでタイムアウトする。`netfn`・`cmd` を指定すると、Open Session・RAKP と ASF ping には効かない（IPMI のコマンドではないので）。
- `action`: 要求された Redfish の `ResetType`（`On`・`ForceOff`・`GracefulShutdown`・`ForceRestart`・`GracefulRestart`・`PowerCycle`・`ForceOn`・`Nmi`・`PushPowerButton`）。IPMI の chassis control は `ForceOff`（off）・`On`（on）・`PowerCycle`（cycle）・`ForceRestart`（reset）・`GracefulShutdown`（soft）・`Nmi`（diag）になる。省略で全部。
- `FAULT_API_ADDR` か `FAULTS` を設定すると、Redfish は HTTP/1.1 だけになる（HTTP/2 の接続はまるごと閉じられないので）。

## Redfish エンドポイント

| メソッド | パス | 説明 |
|---------|------|------|
| GET | `/redfish` | プロトコルのバージョン（認証なし） |
| GET | `/redfish/v1` | サービスルート（認証なし） |
| GET | `/redfish/v1/SessionService` | セッションサービス（SessionTimeout） |
| PATCH | `/redfish/v1/SessionService` | SessionTimeout を変える（30〜86400 秒） |
| GET | `/redfish/v1/SessionService/Sessions` | セッション一覧 |
| POST | `/redfish/v1/SessionService/Sessions` | ログイン（認証なし。201、X-Auth-Token と Location ヘッダ） |
| GET | `/redfish/v1/SessionService/Sessions/{id}` | セッション |
| DELETE | `/redfish/v1/SessionService/Sessions/{id}` | ログアウト（204） |
| GET | `/redfish/v1/Systems` | システムコレクション |
| GET | `/redfish/v1/Systems/1` | コンピュータシステム（Manufacturer、Model、SerialNumber、SKU（`VM_SKU` があれば）、Status、Processors、Storage、SimpleStorage、VirtualMedia。Links: ManagedBy に Managers/1、Chassis に Chassis/1） |
| PATCH | `/redfish/v1/Systems/1` | ブートデバイス変更（`BootSourceOverrideTarget`（`UefiHttp` も）、`Boot.HttpBootUri`。「UEFI HTTP ブート」を参照） |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | 電源制御 |
| GET/PATCH | `/redfish/v1/Systems/1/SecureBoot` | Secure Boot（`SecureBootEnable`・`SecureBootCurrentBoot`・`SecureBootMode`。プロセス管理モードの UEFI だけ、それ以外は `404`。「Secure Boot」を参照） |
| POST | `/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys` | Secure Boot の鍵を戻す（`ResetAllKeysToDefault`） |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces` | NIC 一覧 |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces/{n}` | NIC（MACAddress・LinkStatus） |
| GET | `/redfish/v1/Systems/1/Processors` | プロセッサ一覧（CPU1。CPU 数が分からないときは空） |
| GET | `/redfish/v1/Systems/1/Processors/CPU1` | プロセッサ（x86・x86-64。TotalCores・TotalThreads は VM の CPU 数） |
| GET | `/redfish/v1/Systems/1/Storage` | ストレージ一覧（Storage/1。レガシーモードなどディスクが分からないときは空） |
| GET | `/redfish/v1/Systems/1/Storage/1` | ストレージ（ゲストのディスクごとに Drive を1つ。Volumes へのリンク） |
| GET | `/redfish/v1/Chassis/1/Drives` | ドライブ一覧（ゲストのディスク。ディスクがあれば `Chassis/1` からリンク） |
| GET | `/redfish/v1/Chassis/1/Drives/Disk{n}` | ドライブ（CapacityBytes は起動時の `qemu-img info`、MediaType HDD、Protocol）。非推奨の `/redfish/v1/Systems/1/Storage/1/Drives/Disk{n}` でも返す |
| GET | `/redfish/v1/Systems/1/Storage/1/Volumes` | ボリューム一覧（ゲストのディスクごとに1つ。ディスクが分からないときは空） |
| GET | `/redfish/v1/Systems/1/Storage/1/Volumes/{n}` | ボリューム（CapacityBytes、Name、Status、Links.Drives に Disk{n}。RAIDType は無し）。Ironic の inspection（sushy の `volumes_sizes_bytes`）が読む |
| GET | `/redfish/v1/Systems/1/SimpleStorage` | SimpleStorage 一覧（非推奨だが Ironic の inspection が読む） |
| GET | `/redfish/v1/Systems/1/SimpleStorage/1` | Devices：各ディスクの Name と CapacityBytes |
| GET | `/redfish/v1/Systems/1/VirtualMedia` | VirtualMedia コレクション（Managers/1/VirtualMedia と同じ CD ドライブ） |
| GET | `/redfish/v1/Systems/1/VirtualMedia/CD1` | VirtualMedia リソース（Managers/1/VirtualMedia/CD1 と同じ） |
| POST | `/redfish/v1/Systems/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia` | ISO を差す（204） |
| POST | `/redfish/v1/Systems/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia` | メディア取り出し（204） |
| GET | `/redfish/v1/Managers` | マネージャコレクション |
| GET | `/redfish/v1/Managers/1` | BMC マネージャ（ManagerType は BMC、Manufacturer と Model は `qemu-bmc`、FirmwareVersion は qemu-bmc のバージョン、Status。Links: ManagerForServers に Systems/1、ManagerInChassis に Chassis/1） |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia コレクション（非推奨の場所。Systems/1/VirtualMedia と同じドライブ） |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia リソース（ImageName は差しているときだけ） |
| POST | `.../VirtualMedia.InsertMedia` | ISO を差す（コンテナ内のパスか HTTP(S) の URL、204） |
| POST | `.../VirtualMedia.EjectMedia` | メディア取り出し（204） |
| GET | `/redfish/v1/Chassis` | シャーシコレクション |
| GET | `/redfish/v1/Chassis/1` | シャーシリソース（PowerState、Manufacturer、Model、SerialNumber は Systems/1 と同じ値、Status。Links: ComputerSystems に Systems/1、ManagedBy に Managers/1） |
| GET | `/novnc/` | noVNC UI へリダイレクト |
| GET | `/novnc/vnc.html` | ブラウザ VNC コンソール |
| GET | `/websockify` | WebSocket-to-VNC プロキシ |

### Reset の種類

`ComputerSystem.Reset` は操作を始めたらすぐ 204 を返す。

| ResetType | 動作 | `PowerState` |
|-----------|------|--------------|
| `On` | 電源を入れる | `On` |
| `ForceOff` | すぐに電源を切る | `Off` |
| `ForceOn` | `On` と同じ | `On` |
| `ForceRestart` | ハードリセット | `On` |
| `PowerCycle` | すぐに電源を切って入れ直す。プロセス管理モードでは QEMU を起こし直すので、起動先の上書きと VirtualMedia が電源オンと同じように効く（レガシーモードはハードリセット）。切れていれば電源を入れる | `On` |
| `Nmi` | ゲストに NMI を送る（QMP の `inject-nmi`。Ironic の `inject_nmi` でクラッシュダンプを取るときなど）。途中の `GracefulShutdown` は打ち切らない。電源が切れていれば 409 | 変わらない |
| `PushPowerButton` | 切れていれば `On`、入っていれば `GracefulShutdown` と同じ | `On` / `GracefulShutdown` と同じ |
| `GracefulShutdown` | ACPI の電源ボタンを押す。強制的には切らない | ゲストが電源を切るまで `PoweringOff`、切れたら `Off`。`GRACEFUL_TIMEOUT` 秒たっても切れなければ（ACPI に応えないゲストなど）`On` に戻す（ログに出す） |
| `GracefulRestart` | ACPI の電源ボタンを押し、ゲストが電源を切ったら入れ直す | `PoweringOff` → 入れ直す間 `PoweringOn` → `On`。`GRACEFUL_TIMEOUT` 秒たっても切れなければ `On` に戻す |

`PoweringOff` / `PoweringOn` の間に別の電源操作（Redfish でも IPMI でも）が来たら途中の状態は打ち切り、その操作の結果に従う。IPMI の `chassis power soft` は `GracefulShutdown`、`chassis power cycle` は `PowerCycle`、`chassis power diag` は `Nmi` と同じ。`chassis power status` は `PoweringOff` / `PoweringOn` を on として返す。

## IPMI コマンド

| コマンド | 説明 |
|---------|------|
| Get Device ID | BMC 識別情報 |
| Get Self Test Results | 常に「passed」（`chassis selftest`・`mc selftest`） |
| Get BMC Global Enables | 固定値：Event Message Buffer と System Event Logging が有効（`mc getenables`） |
| Get Channel Auth Capabilities | 認証方式ネゴシエーション |
| Get Channel Cipher Suites | 暗号スイート一覧（3 と 17） |
| Get Chassis Status | 電源状態取得 |
| Chassis Control | 電源オン/オフ/サイクル/リセット、ソフトオフ、diag（NMI。電源が切れていれば `0xD5`） |
| Set/Get Boot Options | ブートデバイス変更 |
| Get SDR Repository Info / Reserve SDR Repository / Get SDR | レコードは BMC 自身の MC Device Locator の1件だけ。`sdr list` は空（ipmitool はリポジトリが空だと作り直そうとしてエラーを出すため） |
| Get Device SDR Info / Reserve Device SDR Repository / Get Device SDR | Device SDR は無い |
| Get SEL Info / Reserve SEL / Get SEL Entry / Clear SEL | SEL は常に空（`sel info`・`sel list`・`sel clear`） |
| Get FRU Inventory Area Info / Read FRU Data | FRU 0 だけ。Product Manufacturer `QEMU`、Product Name `qemu-bmc VM`、Product Serial はシステムの UUID（`fru print`） |

## 環境変数

### BMC 設定

| 変数 | デフォルト | 説明 |
|------|-----------|------|
| `QMP_SOCK` | `/var/run/qemu/qmp.sock` | QMP ソケットパス |
| `IPMI_USER` | `admin` | 認証ユーザー名 |
| `IPMI_PASS` | `password` | 認証パスワード |
| `REDFISH_PORT` | `443` | Redfish HTTPS ポート |
| `IPMI_PORT` | `623` | IPMI UDP ポート |
| `SERIAL_ADDR` | `localhost:9002` | プロセス管理モードのみ: QEMU のゲストシリアルコンソールをこのアドレスの TCP サーバーとして公開。SOL は**未実装**（qemu-bmc 自身は接続しない）。既定ポート 9002 は `VM_IPMI_ADDR` の例と重なるため、両方使う場合は別ポート（例: `localhost:9003`）を指定 |
| `TLS_CERT` | (自動生成) | TLS 証明書パス。未設定時は ECDSA 自己署名証明書を動的生成 |
| `TLS_KEY` | (自動生成) | TLS 鍵パス。未設定時は `TLS_CERT` と同時に生成 |
| `VM_BOOT_MODE` | `bios` | コンテナが QEMU を起動するファームウェア (`bios` または `uefi`、`entrypoint.sh` が選ぶ)。プロセス管理モードでは Redfish `BootSourceOverrideMode` (`Legacy` / `UEFI`) と IPMI ブートフラグの EFI ビットの初期値にもなる |
| `VM_IPMI_ADDR` | (空、無効) | VM IPMI chardev リッスンアドレス (例: `:9002`) |
| `VNC_ADDR` | `localhost:5900` | noVNC プロキシが接続する QEMU VNC アドレス |
| `POWER_ON_AT_START` | `false` | 起動時に VM を自動的に電源オンにする（MAAS を使わない構成で有用） |
| `GRACEFUL_TIMEOUT` | `120` | `GracefulShutdown` / `GracefulRestart`（と IPMI `chassis power soft`）でゲストが電源を切らないとき、`PoweringOff` から `On` に戻すまでの秒数 |
| `VMEDIA_DIR` | `/vm` が書ければ `/vm/vmedia`、無理なら `/tmp/vmedia` | VirtualMedia の HTTP(S) イメージの保存先（UEFI HTTP ブートの `Boot.HttpBootUri` のイメージも）。差し替え・Eject（URI の変更・削除）で前に落としたファイルは消し、前の起動の残り（`cd1-*.iso`・`httpboot-*.iso`）は起動時に消す。レガシーモードでは外の QEMU がこのディレクトリを読める（同じファイルシステム）必要がある |
| `VMEDIA_INSECURE_TLS` | `true` | HTTPS のイメージを落とすとき証明書を検証しない。`false` ならシステムの CA で検証する。自前の CA は `SSL_CERT_FILE` で足す |
| `VMEDIA_TIMEOUT` | `600` | ダウンロード中（VirtualMedia・UEFI HTTP ブートのイメージ）に電源オン・リセットが来たとき、終わるまで待つ秒数（過ぎたら ISO 無しで起動） |
| `FAULT_API_ADDR` | (空、無効) | 故障の注入の API の待ち受けアドレス（TLS なしの HTTP。例 `:8623`）。[故障の注入](#故障の注入) を参照 |
| `FAULT_API_TOKEN` | (空) | 故障の API に要る Bearer トークン。空なら認証なし |
| `FAULTS` | (空) | 起動時から効く故障の JSON の配列 |
| `SYSTEM_UUID` | （導出） | システム UUID を固定する。QEMU に `-uuid` で渡し、Redfish `Systems/1` の `UUID` と IPMI `mc guid` で返す。未指定なら `SYSTEM_UUID_FILE` から読み、無ければホスト名から導出してそこへ保存する。コンテナを作り直しても保つには、これを指定するか、`--hostname` を固定するか、`/vm` をマウントする |
| `SYSTEM_UUID_FILE` | `<VM_DISK のディレクトリ>/system-uuid` | 導出した UUID の保存先 |
| `VM_MACS` | （導出） | VM の NIC の MAC アドレスを順にカンマ区切りで指定する（空の項目は導出）。未指定の MAC はシステム UUID から導出するので、ノードごとに固定され、ノード間で重ならない。Redfish `Systems/1/EthernetInterfaces` で返す |
| `VM_MANUFACTURER` | `QEMU` | システムのメーカー。プロセス管理モードではゲストの SMBIOS（`-smbios type=1/2/3`）に入れる。Redfish `Systems/1`・`Chassis/1` の `Manufacturer` で返す |
| `VM_MODEL` | `qemu-bmc` | システムの型番。SMBIOS の product（type 1 と 2）、Redfish の `Model` |
| `VM_SERIAL_NUMBER` | （導出） | システムのシリアル番号。SMBIOS の serial（type 1・2・3）、Redfish の `SerialNumber`。未指定なら `QBMC-` + システム UUID の16進の頭 10 文字を大文字にしたもの（UUID が同じなら同じ値。レガシーモードで `SYSTEM_UUID` が無ければ出さない） |
| `VM_SKU` | （空） | システムの SKU。SMBIOS の sku（type 1 と 3）、Redfish `Systems/1` の `SKU`。空なら入れない・出さない |
| `VM_SECURE_BOOT` | `false` | 起動時の Redfish `SecureBootEnable`（`true` で Secure Boot 用の OVMF で起動する。`VM_BOOT_MODE=uefi` が要り、そうでなければ qemu-bmc はエラーで終わる。レガシーモードでは無視）。Redfish で PATCH した値はメモリだけで、qemu-bmc を起動し直すとこの値に戻る。「Secure Boot」を参照 |

### コンテナ設定

`docker/entrypoint.sh` が QEMU 引数を構成するために使用:

| 変数 | デフォルト | 説明 |
|------|-----------|------|
| `VM_MEMORY` | `2048` | VM メモリ (MB) |
| `VM_CPUS` | `2` | VM CPU 数 |
| `ENABLE_KVM` | `true` | KVM アクセラレーション使用 (TCG にフォールバック) |
| `VNC_PORT` | `5900` | VNC ディスプレイポート |
| `VM_DISK` | `/vm/disk.qcow2` | VM ディスクイメージパス |
| `VM_DISK_SIZE` | (空) | 設定する（例 `20G`）と、`VM_DISK` が無いとき起動時にこの大きさの qcow2 を作る（既にあるディスクは触らない） |
| `VM_CDROM` | (空) | VirtualMedia の CD ドライブに最初から差しておく ISO（無くても空の CD ドライブは付く） |
| `VM_BOOT` | `c` | ブートデバイス (c=ディスク, d=CD-ROM, n=ネットワーク) |
| `VM_BOOT_MENU_TIMEOUT` | `0` | ブートメニュータイムアウト (ms、0=無効) |
| `VM_NETWORKS` | (空) | TAP パススルー用ホストインターフェース (カンマ区切り、インターフェース出現まで最大30秒待機、containerlab 対応。現れなければエラーで終了する)。未指定時は eth2 以降をパススルー。`/dev/net/tun` (`--device /dev/net/tun`) と `NET_ADMIN` が必要で、TUN デバイスが使えないとエラーで終了する |
| `VM_NET_MTU` | (空) | VM の NIC の MTU。FCoE やジャンボフレームを流すときに使う（例 `9000`）。TAP とブリッジをその MTU にし、パススルーするコンテナ側のインタフェースはそれより小さければ上げ（大きければそのまま）、virtio-net の NIC に `host_mtu=` を付けてゲストに知らせる。空なら MTU は触らない。数字（68〜65535）でないか設定できなければエラーで終了する |
| `QEMU_EXTRA_ARGS` | (空) | 追加 QEMU 引数 |
| `DEBUG` | `false` | デバッグ出力を有効化 |

## 開発

```bash
# noVNC 静的ファイルの取得（go build/test の前に一度必要）
make download-novnc

# ビルド
go build ./cmd/qemu-bmc
make docker-build

# ユニットテスト
go test ./... -count=1
go test ./... -race -count=1

# 静的解析
go vet ./...

# コンテナ統合テスト
make container-test         # クイックスモークテスト
make container-test-all     # 全 115 テスト (10 カテゴリ)

# Ironic 通し試験: 本物の Ironic で Debian を展開 (Redfish・IPMI ドライバ、約 20 分)
make ironic-test            # tests/ironic/README.md 参照
```

## アーキテクチャ

```
cmd/qemu-bmc/main.go          # エントリポイント
internal/
  qmp/                         # QMP ソケットクライアント
  qemu/                        # QEMU プロセス管理
  machine/                     # VM 状態管理
  redfish/                     # Redfish HTTP サーバー (gorilla/mux)
  ipmi/                        # IPMI UDP サーバー + VM chardev サーバー (RMCP/RMCP+)
  novnc/                       # noVNC 静的ファイル（埋め込み）+ WebSocket-to-VNC プロキシ
  bmc/                         # BMC 設定状態 (ユーザー、LAN、チャネル)
  config/                      # 環境変数設定
  fault/                       # 故障の注入（故障の API、Redfish・IPMI・電源の故障）
docker/
  Dockerfile                   # マルチステージビルド (Go ビルダー + Debian ランタイム)
  entrypoint.sh                # 環境変数 → QEMU 引数変換
  setup-network.sh             # TAP/ブリッジネットワーク設定
tests/                         # Bash ベースのコンテナ統合テスト
  ironic/run.sh                # Ironic 通し試験 (make ironic-test)
containerlab/                  # containerlab トポロジ例 (containerlab/README.md 参照)
```

## ライセンス

qemu-bmc は [Apache License 2.0](LICENSE) で提供されます。

バイナリおよびコンテナイメージには、それぞれ独自のライセンスを持つ第三者のファイルが同梱されています（詳細は [NOTICE](NOTICE) を参照）:

| コンポーネント | ライセンス |
|----------------|------------|
| [noVNC](https://github.com/novnc/noVNC) 1.5.0 (`internal/novnc/static/`) | MPL-2.0（コア JS）、HTML/CSS: BSD-2-Clause、フォント: OFL-1.1、画像: CC BY-SA 3.0 |
| [pako](https://github.com/nodeca/pako)（noVNC の `vendor/pako/` に同梱） | MIT |
