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
- **IPMI over LAN** - RMCP/RMCP+, RAKP HMAC-SHA1 authentication, AES-CBC-128 encryption
- **VM IPMI (In-Band)** - Guest OS IPMI via QEMU `ipmi-bmc-extern` KCS interface for MaaS commissioning
- **noVNC** - Browser-based VNC console served on the Redfish HTTP port (no extra port needed)
- **QMP Control** - Power operations, boot device changes, VirtualMedia mount
- **Compatibility** - MAAS, Tinkerbell/Rufio

## Quick Start

### Docker Compose (Process Management Mode)

qemu-bmc manages the QEMU process directly inside the container:

```bash
# Create VM disk
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

- In process management mode (the container), the VM always has a CD drive. It is empty unless `VM_CDROM` is set, in which case that ISO is the initially inserted medium.
- `Image` is a path inside the container, `/iso/boot.iso` or `file:///iso/boot.iso` (bind-mount the ISO, e.g. `-v ./iso:/iso:ro`), or an `http://` / `https://` URL. A missing `Image`, a nonexistent file or an unsupported scheme returns `400`; InsertMedia and EjectMedia return `204 No Content` on success.
- An HTTP(S) image is downloaded by qemu-bmc into `VMEDIA_DIR`, then attached (QEMU in the image cannot read URLs itself). InsertMedia returns `204` without waiting for the download; meanwhile `GET` already reports the URL as `Image` with `Inserted: true`. `UserName` / `Password` in the InsertMedia request are sent as HTTP Basic auth. Certificates are not verified unless `VMEDIA_INSECURE_TLS=false` (self-signed servers are common).
- A power on or reset (Redfish `On` / `ForceRestart`, IPMI `chassis power on` / `cycle` / `reset`) during a download is carried out once the download finishes (at most `VMEDIA_TIMEOUT` later), so the VM boots from the ISO. The request itself returns at once; the power state is `PoweringOn` meanwhile, and another power action (e.g. `ForceOff`) cancels it. If the download fails (e.g. `404`), the medium is ejected (`Inserted: false`) and the reason is logged. A download finishing while the VM runs is attached over QMP. Inserting another image or ejecting abandons the download and deletes the previously downloaded file.
- The medium is kept by qemu-bmc while the VM is powered off and attached when QEMU starts; while the VM is running it is swapped live over QMP.
- Boot target `Cd` (Redfish `BootSourceOverrideTarget`, IPMI `chassis bootdev cdrom`) boots the inserted ISO in both BIOS and UEFI mode (the CD drive gets `bootindex=0`, which OVMF requires).
- `GET .../VirtualMedia/CD1` reports `Image`, `ImageName`, `Inserted`, `ConnectedVia` (`URI` / `NotConnected`), `WriteProtected` (always `true`) and `MediaTypes`.
- Legacy mode (external QEMU) only changes the medium over QMP; the path refers to the external QEMU's filesystem. The CD drive is located with `query-block` (first removable drive) in both modes.

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

## Redfish Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/redfish/v1` | ServiceRoot |
| GET | `/redfish/v1/Systems` | System collection |
| GET | `/redfish/v1/Systems/1` | Computer system |
| PATCH | `/redfish/v1/Systems/1` | Boot device override |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | Power control |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces` | NIC collection |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces/{n}` | NIC (MACAddress, LinkStatus) |
| GET | `/redfish/v1/Managers` | Manager collection |
| GET | `/redfish/v1/Managers/1` | BMC manager |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia collection |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia resource |
| POST | `.../VirtualMedia.InsertMedia` | Insert an ISO (container path or HTTP(S) URL; 204) |
| POST | `.../VirtualMedia.EjectMedia` | Eject media (204) |
| GET | `/redfish/v1/Chassis` | Chassis collection |
| GET | `/redfish/v1/Chassis/1` | Chassis resource |
| GET | `/novnc/` | Redirect to noVNC UI |
| GET | `/novnc/vnc.html` | Browser-based VNC console |
| GET | `/websockify` | WebSocket-to-VNC proxy |

### Reset types

`ComputerSystem.Reset` returns 204 as soon as the action is started.

| ResetType | Behavior | `PowerState` |
|-----------|----------|--------------|
| `On` | Power on | `On` |
| `ForceOff` | Power off immediately | `Off` |
| `ForceRestart` | Hard reset | `On` |
| `GracefulShutdown` | Press the ACPI power button. The VM is never forced off | `PoweringOff` until the guest powers off, then `Off`. If the guest has not powered off after `GRACEFUL_TIMEOUT` seconds (e.g. it ignores ACPI), back to `On` (logged) |
| `GracefulRestart` | Press the ACPI power button, then power the VM back on once the guest has powered off | `PoweringOff`, then `PoweringOn` while powering on, then `On`. Back to `On` after `GRACEFUL_TIMEOUT` seconds if the guest does not power off |

Another power action (Redfish or IPMI) during `PoweringOff` / `PoweringOn` ends the graceful action; the state then follows the new action. IPMI `chassis power soft` behaves like `GracefulShutdown`; `chassis power status` reports `PoweringOff` / `PoweringOn` as on.

## IPMI Commands

| Command | Description |
|---------|-------------|
| Get Device ID | BMC identity |
| Get Channel Auth Capabilities | Auth type negotiation |
| Get Channel Cipher Suites | Cipher suite list (suite 3 only) |
| Get Chassis Status | Power state query |
| Chassis Control | Power on/off/cycle/reset |
| Set/Get Boot Options | Boot device override |

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
| `VMEDIA_DIR` | `/vm/vmedia` if `/vm` is writable, else `/tmp/vmedia` | Where VirtualMedia HTTP(S) images are downloaded. The previous download is deleted when the medium is replaced or ejected |
| `VMEDIA_INSECURE_TLS` | `true` | Skip certificate verification when downloading HTTPS images |
| `VMEDIA_TIMEOUT` | `600` | Seconds a power on or reset requested during a VirtualMedia download waits for it before booting without the image |
| `SYSTEM_UUID` | (derived) | Fixed system UUID, passed to QEMU as `-uuid` and reported by Redfish `Systems/1` `UUID` and IPMI `mc guid`. If unset, it is read from `SYSTEM_UUID_FILE`, or derived from the hostname and saved there. Set it (or use a fixed `--hostname` / a mounted `/vm`) to keep it across container re-creation |
| `SYSTEM_UUID_FILE` | `<VM_DISK dir>/system-uuid` | Where a derived UUID is persisted |
| `VM_MACS` | (derived) | Comma-separated MAC addresses of the VM NICs, in order (an empty entry is derived). Unset MACs are derived from the system UUID, so they stay fixed per node and do not overlap between nodes. Reported by Redfish `Systems/1/EthernetInterfaces` |

### Container Configuration

These variables are used by `docker/entrypoint.sh` to construct QEMU arguments:

| Variable | Default | Description |
|----------|---------|-------------|
| `VM_MEMORY` | `2048` | VM memory in MB |
| `VM_CPUS` | `2` | Number of VM CPUs |
| `ENABLE_KVM` | `true` | Use KVM acceleration (falls back to TCG) |
| `VNC_PORT` | `5900` | VNC display port |
| `VM_DISK` | `/vm/disk.qcow2` | VM disk image path |
| `VM_CDROM` | (empty) | ISO initially inserted in the VirtualMedia CD drive (the drive exists, empty, without it) |
| `VM_BOOT` | `c` | Boot device (c=disk, d=cdrom, n=network) |
| `VM_BOOT_MENU_TIMEOUT` | `0` | Boot menu timeout in ms (0=disabled) |
| `VM_NETWORKS` | (empty) | Comma-separated host interfaces for TAP passthrough (waits up to 30s for interfaces to appear, useful for containerlab; the container exits with an error if one does not appear). Without it, eth2+ are passed through. Requires `/dev/net/tun` (`--device /dev/net/tun`) and `NET_ADMIN`; the container exits with an error if the TUN device is unusable |
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
make container-test-all     # All 82 tests (9 categories)
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
docker/
  Dockerfile                   # Multi-stage build (Go builder + Debian runtime)
  entrypoint.sh                # Environment variables → QEMU args
  setup-network.sh             # TAP/bridge network setup
tests/                         # Bash-based container integration tests
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
- **IPMI over LAN** - RMCP/RMCP+, RAKP HMAC-SHA1 認証, AES-CBC-128 暗号化
- **VM IPMI（イン・バンド）** - QEMU `ipmi-bmc-extern` KCS インターフェースによるゲスト OS IPMI（MaaS コミッショニング対応）
- **noVNC** - Redfish HTTP ポートでブラウザから VNC コンソールにアクセス（追加ポート不要）
- **QMP 制御** - 電源操作、ブートデバイス変更、VirtualMedia マウント
- **互換性** - MAAS, Tinkerbell/Rufio

## クイックスタート

### Docker Compose（プロセス管理モード）

qemu-bmc がコンテナ内で QEMU プロセスを直接管理します:

```bash
# VM ディスク作成
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

- プロセス管理モード（コンテナ）では、VM に CD ドライブが常に付く。`VM_CDROM` が無ければ空、あればその ISO が最初に差さった状態になる。
- `Image` はコンテナ内のパス（`/iso/boot.iso` または `file:///iso/boot.iso`。ISO はバインドマウントする（例: `-v ./iso:/iso:ro`））か、`http://` / `https://` の URL。`Image` が空・ファイルが無い・未対応の形式なら `400`。InsertMedia・EjectMedia は成功すると `204 No Content`。
- HTTP(S) の URL は qemu-bmc が `VMEDIA_DIR` へダウンロードしてから差す（イメージの QEMU は URL を直接読めない）。InsertMedia はダウンロードを待たずに `204` を返し、ダウンロード中も `GET` は `Image` に URL、`Inserted: true` を返す。InsertMedia の `UserName` / `Password` は HTTP の Basic 認証に使う。自己署名のサーバが多いので、`VMEDIA_INSECURE_TLS=false` にしない限り証明書は検証しない。
- ダウンロード中に電源オン・リセット（Redfish の `On` / `ForceRestart`、IPMI の `chassis power on` / `cycle` / `reset`）が来たら、ダウンロードが終わってから（最大 `VMEDIA_TIMEOUT` 待つ）実行するので、ISO から起動できる。要求そのものはすぐ返り、その間の電源状態は `PoweringOn`。別の電源操作（`ForceOff` など）が来たら取りやめる。ダウンロードに失敗したら（`404` など）中身を外し（`Inserted: false`）、理由をログに出す。VM が動いている間に終わったら QMP で差す。別のイメージを差す・Eject すると、ダウンロードはやめ、前に落としたファイルは消す。
- 差した中身は電源オフ中も qemu-bmc が覚えていて、QEMU を起動するときに付ける。VM が動いていれば QMP でその場で差し替える。
- 起動先 `Cd`（Redfish の `BootSourceOverrideTarget`、IPMI の `chassis bootdev cdrom`）で、差した ISO から起動する。BIOS・UEFI のどちらでも動く（UEFI（OVMF）のため CD ドライブに `bootindex=0` を付ける）。
- `GET .../VirtualMedia/CD1` は `Image`・`ImageName`・`Inserted`・`ConnectedVia`（`URI` / `NotConnected`）・`WriteProtected`（常に `true`）・`MediaTypes` を返す。
- レガシーモード（外部の QEMU）は QMP で差し替えるだけ。パスは外部 QEMU 側のファイルシステムのもの。どちらのモードも CD ドライブは `query-block` で探す（最初の取り外し可能なドライブ）。

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

## Redfish エンドポイント

| メソッド | パス | 説明 |
|---------|------|------|
| GET | `/redfish/v1` | サービスルート |
| GET | `/redfish/v1/Systems` | システムコレクション |
| GET | `/redfish/v1/Systems/1` | コンピュータシステム |
| PATCH | `/redfish/v1/Systems/1` | ブートデバイス変更 |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | 電源制御 |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces` | NIC 一覧 |
| GET | `/redfish/v1/Systems/1/EthernetInterfaces/{n}` | NIC（MACAddress・LinkStatus） |
| GET | `/redfish/v1/Managers` | マネージャコレクション |
| GET | `/redfish/v1/Managers/1` | BMC マネージャ |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia コレクション |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia リソース |
| POST | `.../VirtualMedia.InsertMedia` | ISO を差す（コンテナ内のパスか HTTP(S) の URL、204） |
| POST | `.../VirtualMedia.EjectMedia` | メディア取り出し（204） |
| GET | `/redfish/v1/Chassis` | シャーシコレクション |
| GET | `/redfish/v1/Chassis/1` | シャーシリソース |
| GET | `/novnc/` | noVNC UI へリダイレクト |
| GET | `/novnc/vnc.html` | ブラウザ VNC コンソール |
| GET | `/websockify` | WebSocket-to-VNC プロキシ |

### Reset の種類

`ComputerSystem.Reset` は操作を始めたらすぐ 204 を返す。

| ResetType | 動作 | `PowerState` |
|-----------|------|--------------|
| `On` | 電源を入れる | `On` |
| `ForceOff` | すぐに電源を切る | `Off` |
| `ForceRestart` | ハードリセット | `On` |
| `GracefulShutdown` | ACPI の電源ボタンを押す。強制的には切らない | ゲストが電源を切るまで `PoweringOff`、切れたら `Off`。`GRACEFUL_TIMEOUT` 秒たっても切れなければ（ACPI に応えないゲストなど）`On` に戻す（ログに出す） |
| `GracefulRestart` | ACPI の電源ボタンを押し、ゲストが電源を切ったら入れ直す | `PoweringOff` → 入れ直す間 `PoweringOn` → `On`。`GRACEFUL_TIMEOUT` 秒たっても切れなければ `On` に戻す |

`PoweringOff` / `PoweringOn` の間に別の電源操作（Redfish でも IPMI でも）が来たら途中の状態は打ち切り、その操作の結果に従う。IPMI の `chassis power soft` は `GracefulShutdown` と同じ。`chassis power status` は `PoweringOff` / `PoweringOn` を on として返す。

## IPMI コマンド

| コマンド | 説明 |
|---------|------|
| Get Device ID | BMC 識別情報 |
| Get Channel Auth Capabilities | 認証方式ネゴシエーション |
| Get Chassis Status | 電源状態取得 |
| Chassis Control | 電源オン/オフ/サイクル/リセット |
| Set/Get Boot Options | ブートデバイス変更 |

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
| `VMEDIA_DIR` | `/vm` が書ければ `/vm/vmedia`、無理なら `/tmp/vmedia` | VirtualMedia の HTTP(S) イメージの保存先。差し替え・Eject で前に落としたファイルは消す |
| `VMEDIA_INSECURE_TLS` | `true` | HTTPS のイメージを落とすとき証明書を検証しない |
| `VMEDIA_TIMEOUT` | `600` | ダウンロード中に電源オン・リセットが来たとき、終わるまで待つ秒数（過ぎたら ISO 無しで起動） |
| `SYSTEM_UUID` | （導出） | システム UUID を固定する。QEMU に `-uuid` で渡し、Redfish `Systems/1` の `UUID` と IPMI `mc guid` で返す。未指定なら `SYSTEM_UUID_FILE` から読み、無ければホスト名から導出してそこへ保存する。コンテナを作り直しても保つには、これを指定するか、`--hostname` を固定するか、`/vm` をマウントする |
| `SYSTEM_UUID_FILE` | `<VM_DISK のディレクトリ>/system-uuid` | 導出した UUID の保存先 |
| `VM_MACS` | （導出） | VM の NIC の MAC アドレスを順にカンマ区切りで指定する（空の項目は導出）。未指定の MAC はシステム UUID から導出するので、ノードごとに固定され、ノード間で重ならない。Redfish `Systems/1/EthernetInterfaces` で返す |

### コンテナ設定

`docker/entrypoint.sh` が QEMU 引数を構成するために使用:

| 変数 | デフォルト | 説明 |
|------|-----------|------|
| `VM_MEMORY` | `2048` | VM メモリ (MB) |
| `VM_CPUS` | `2` | VM CPU 数 |
| `ENABLE_KVM` | `true` | KVM アクセラレーション使用 (TCG にフォールバック) |
| `VNC_PORT` | `5900` | VNC ディスプレイポート |
| `VM_DISK` | `/vm/disk.qcow2` | VM ディスクイメージパス |
| `VM_CDROM` | (空) | VirtualMedia の CD ドライブに最初から差しておく ISO（無くても空の CD ドライブは付く） |
| `VM_BOOT` | `c` | ブートデバイス (c=ディスク, d=CD-ROM, n=ネットワーク) |
| `VM_BOOT_MENU_TIMEOUT` | `0` | ブートメニュータイムアウト (ms、0=無効) |
| `VM_NETWORKS` | (空) | TAP パススルー用ホストインターフェース (カンマ区切り、インターフェース出現まで最大30秒待機、containerlab 対応。現れなければエラーで終了する)。未指定時は eth2 以降をパススルー。`/dev/net/tun` (`--device /dev/net/tun`) と `NET_ADMIN` が必要で、TUN デバイスが使えないとエラーで終了する |
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
make container-test-all     # 全 82 テスト (9 カテゴリ)
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
docker/
  Dockerfile                   # マルチステージビルド (Go ビルダー + Debian ランタイム)
  entrypoint.sh                # 環境変数 → QEMU 引数変換
  setup-network.sh             # TAP/ブリッジネットワーク設定
tests/                         # Bash ベースのコンテナ統合テスト
containerlab/                  # containerlab トポロジ例 (containerlab/README.md 参照)
```

## ライセンス

qemu-bmc は [Apache License 2.0](LICENSE) で提供されます。

バイナリおよびコンテナイメージには、それぞれ独自のライセンスを持つ第三者のファイルが同梱されています（詳細は [NOTICE](NOTICE) を参照）:

| コンポーネント | ライセンス |
|----------------|------------|
| [noVNC](https://github.com/novnc/noVNC) 1.5.0 (`internal/novnc/static/`) | MPL-2.0（コア JS）、HTML/CSS: BSD-2-Clause、フォント: OFL-1.1、画像: CC BY-SA 3.0 |
| [pako](https://github.com/nodeca/pako)（noVNC の `vendor/pako/` に同梱） | MIT |
