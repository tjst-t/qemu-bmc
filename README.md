# qemu-bmc

A Go single binary that controls QEMU VMs via both Redfish API (HTTPS) and IPMI over LAN (UDP).

Replaces [docker-qemu-bmc](https://github.com/tjst-t/docker-qemu-bmc) (shell scripts + ipmi_sim + supervisord) with a single Go binary.

```
┌─────────────────────────────────────┐
│ OCI Container                       │
│                                     │
│  ┌───────────┐   ┌──────────────┐  │
│  │ qemu-bmc  │   │    QEMU      │  │
│  │ (Go)      │   │              │  │
│  │           │   │              │  │
│  │ :443/tcp  │   │              │  │
│  │  Redfish  ├──►│              │  │
│  │           │   │  QMP Socket  │  │
│  │ :623/udp  │   │              │  │
│  │  IPMI     ├──►│              │  │
│  └───────────┘   └──────────────┘  │
└─────────────────────────────────────┘
```

## Features

- **Redfish API** - ServiceRoot, Systems, Managers, VirtualMedia, Chassis (gofish compatible)
- **IPMI over LAN** - RMCP/RMCP+, RAKP HMAC-SHA1 authentication, AES-CBC-128 encryption
- **QMP Control** - Power operations, boot device changes, VirtualMedia mount
- **Compatibility** - MAAS, Tinkerbell/Rufio, Cybozu placemat

## Quick Start

### Docker

```bash
docker build -t qemu-bmc .
docker run --rm \
  -v /var/run/qemu/qmp.sock:/var/run/qemu/qmp.sock \
  -p 443:443 -p 623:623/udp \
  qemu-bmc
```

### Binary

```bash
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

# Mount VirtualMedia
curl -k -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"Image": "http://example.com/boot.iso", "Inserted": true}' \
  https://localhost/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia

# Unmount VirtualMedia
curl -k -u admin:password -X POST \
  https://localhost/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia
```

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

## Redfish Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/redfish/v1` | ServiceRoot |
| GET | `/redfish/v1/Systems` | System collection |
| GET | `/redfish/v1/Systems/1` | Computer system |
| PATCH | `/redfish/v1/Systems/1` | Boot device override |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | Power control |
| GET | `/redfish/v1/Managers` | Manager collection |
| GET | `/redfish/v1/Managers/1` | BMC manager |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia collection |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia resource |
| POST | `.../VirtualMedia.InsertMedia` | Insert media |
| POST | `.../VirtualMedia.EjectMedia` | Eject media |
| GET | `/redfish/v1/Chassis` | Chassis collection |
| GET | `/redfish/v1/Chassis/1` | Chassis resource |

## IPMI Commands

| Command | Description |
|---------|-------------|
| Get Device ID | BMC identity |
| Get Channel Auth Capabilities | Auth type negotiation |
| Get Chassis Status | Power state query |
| Chassis Control | Power on/off/cycle/reset |
| Set/Get Boot Options | Boot device override |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `QMP_SOCK` | `/var/run/qemu/qmp.sock` | QMP socket path |
| `IPMI_USER` | `admin` | Authentication username |
| `IPMI_PASS` | `password` | Authentication password |
| `REDFISH_PORT` | `443` | Redfish HTTPS port |
| `IPMI_PORT` | `623` | IPMI UDP port |
| `SERIAL_ADDR` | `localhost:9002` | SOL bridge target |
| `TLS_CERT` | (auto) | TLS certificate path |
| `TLS_KEY` | (auto) | TLS key path |
| `VM_BOOT_MODE` | `bios` | Default boot mode |

## Development

```bash
# Test
go test ./... -count=1

# Test with race detector
go test ./... -race -count=1

# Static analysis
go vet ./...

# Build
go build ./cmd/qemu-bmc
```

## Architecture

```
cmd/qemu-bmc/main.go          # Entrypoint
internal/
  qmp/                         # QMP socket client
  machine/                     # VM state management
  redfish/                     # Redfish HTTP server (gorilla/mux)
  ipmi/                        # IPMI UDP server (RMCP/RMCP+)
  config/                      # Environment variable config
```

## License

TBD

---

# qemu-bmc (日本語)

QEMU VM を Redfish API (HTTPS) と IPMI over LAN (UDP) の両方で制御する Go シングルバイナリ。

[docker-qemu-bmc](https://github.com/tjst-t/docker-qemu-bmc)（シェルスクリプト + ipmi_sim + supervisord）を Go 単体で置き換える。

```
┌─────────────────────────────────────┐
│ OCI コンテナ                         │
│                                     │
│  ┌───────────┐   ┌──────────────┐  │
│  │ qemu-bmc  │   │    QEMU      │  │
│  │ (Go)      │   │              │  │
│  │           │   │              │  │
│  │ :443/tcp  │   │              │  │
│  │  Redfish  ├──►│              │  │
│  │           │   │  QMP Socket  │  │
│  │ :623/udp  │   │              │  │
│  │  IPMI     ├──►│              │  │
│  └───────────┘   └──────────────┘  │
└─────────────────────────────────────┘
```

## 機能

- **Redfish API** - ServiceRoot, Systems, Managers, VirtualMedia, Chassis (gofish 互換)
- **IPMI over LAN** - RMCP/RMCP+, RAKP HMAC-SHA1 認証, AES-CBC-128 暗号化
- **QMP 制御** - 電源操作、ブートデバイス変更、VirtualMedia マウント
- **互換性** - MAAS, Tinkerbell/Rufio, Cybozu placemat

## クイックスタート

### Docker

```bash
docker build -t qemu-bmc .
docker run --rm \
  -v /var/run/qemu/qmp.sock:/var/run/qemu/qmp.sock \
  -p 443:443 -p 623:623/udp \
  qemu-bmc
```

### バイナリ

```bash
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

# VirtualMedia マウント
curl -k -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"Image": "http://example.com/boot.iso", "Inserted": true}' \
  https://localhost/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia

# VirtualMedia アンマウント
curl -k -u admin:password -X POST \
  https://localhost/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia
```

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

## Redfish エンドポイント

| メソッド | パス | 説明 |
|---------|------|------|
| GET | `/redfish/v1` | サービスルート |
| GET | `/redfish/v1/Systems` | システムコレクション |
| GET | `/redfish/v1/Systems/1` | コンピュータシステム |
| PATCH | `/redfish/v1/Systems/1` | ブートデバイス変更 |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | 電源制御 |
| GET | `/redfish/v1/Managers` | マネージャコレクション |
| GET | `/redfish/v1/Managers/1` | BMC マネージャ |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia コレクション |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia リソース |
| POST | `.../VirtualMedia.InsertMedia` | メディア挿入 |
| POST | `.../VirtualMedia.EjectMedia` | メディア取り出し |
| GET | `/redfish/v1/Chassis` | シャーシコレクション |
| GET | `/redfish/v1/Chassis/1` | シャーシリソース |

## IPMI コマンド

| コマンド | 説明 |
|---------|------|
| Get Device ID | BMC 識別情報 |
| Get Channel Auth Capabilities | 認証方式ネゴシエーション |
| Get Chassis Status | 電源状態取得 |
| Chassis Control | 電源オン/オフ/サイクル/リセット |
| Set/Get Boot Options | ブートデバイス変更 |

## 環境変数

| 変数 | デフォルト | 説明 |
|------|-----------|------|
| `QMP_SOCK` | `/var/run/qemu/qmp.sock` | QMP ソケットパス |
| `IPMI_USER` | `admin` | 認証ユーザー名 |
| `IPMI_PASS` | `password` | 認証パスワード |
| `REDFISH_PORT` | `443` | Redfish HTTPS ポート |
| `IPMI_PORT` | `623` | IPMI UDP ポート |
| `SERIAL_ADDR` | `localhost:9002` | SOL ブリッジ先 |
| `TLS_CERT` | (自動) | TLS 証明書パス |
| `TLS_KEY` | (自動) | TLS 鍵パス |
| `VM_BOOT_MODE` | `bios` | デフォルトブートモード |

## 開発

```bash
# テスト
go test ./... -count=1

# レースコンディション検出付きテスト
go test ./... -race -count=1

# 静的解析
go vet ./...

# ビルド
go build ./cmd/qemu-bmc
```

## アーキテクチャ

```
cmd/qemu-bmc/main.go          # エントリポイント
internal/
  qmp/                         # QMP ソケットクライアント
  machine/                     # VM 状態管理
  redfish/                     # Redfish HTTP サーバー (gorilla/mux)
  ipmi/                        # IPMI UDP サーバー (RMCP/RMCP+)
  config/                      # 環境変数設定
```

## ライセンス

TBD
