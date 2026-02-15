# qemu-bmc コンテナ化 要望書

## 1. 背景

qemu-bmc にプロセス管理機能が実装されると、docker-qemu-bmc リポジトリの役割は Dockerfile + entrypoint.sh（環境変数→CLIフラグ変換）+ setup-network.sh だけになる。機能的に分離する理由がないため、コンテナ定義を qemu-bmc リポジトリに統合する。

docker-qemu-bmc リポジトリは非推奨（archived）とし、qemu-bmc から直接コンテナイメージを提供する。

## 2. 成果物

qemu-bmc リポジトリに以下を追加する:

```
qemu-bmc/
├── cmd/qemu-bmc/main.go          # 既存
├── internal/                      # 既存
├── docker/
│   ├── Dockerfile                 # コンテナイメージ定義
│   ├── entrypoint.sh              # 環境変数→CLIフラグ変換
│   └── setup-network.sh           # TAP/ブリッジ作成
├── docker-compose.yml             # 開発・テスト用
├── containerlab/
│   └── example.yml                # containerlab トポロジ例
└── .github/workflows/
    └── build-and-push.yml         # GHCR へのイメージ公開
```

## 3. Dockerfile

### 3.1 構成

マルチステージビルドで qemu-bmc バイナリをビルドし、ランタイムイメージに含める。

```dockerfile
# ビルドステージ
FROM golang:1.23 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /qemu-bmc ./cmd/qemu-bmc

# ランタイムステージ
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    qemu-system-x86 \
    qemu-utils \
    iproute2 \
    ovmf \
    ipmitool \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /vm /iso /var/run/qemu /var/log/qemu

COPY --from=builder /qemu-bmc /usr/local/bin/qemu-bmc
COPY docker/entrypoint.sh /scripts/
COPY docker/setup-network.sh /scripts/
RUN chmod +x /scripts/*.sh

EXPOSE 5900/tcp 623/udp 443/tcp

VOLUME ["/vm", "/iso"]

ENTRYPOINT ["/scripts/entrypoint.sh"]
```

### 3.2 パッケージ選定

| パッケージ | 目的 | 備考 |
|-----------|------|------|
| `qemu-system-x86` | VM 実行 | 必須 |
| `qemu-utils` | qemu-img 等 | ディスク作成時に有用 |
| `iproute2` | TAP/ブリッジ作成 | `ip` コマンド |
| `ovmf` | UEFI ファームウェア | UEFI ブートモード用 |
| `ipmitool` | IPMI クライアント | コンテナ内デバッグ・ヘルスチェック用 |

以下は**不要**になる（docker-qemu-bmc では必要だった）:

| パッケージ | 不要になる理由 |
|-----------|---------------|
| `openipmi` | qemu-bmc が IPMI を直接実装 |
| `supervisor` | qemu-bmc がプロセス管理 |
| `socat` | QMP 通信を Go で直接実装済み |

### 3.3 ヘルスチェック

```dockerfile
HEALTHCHECK --interval=30s --timeout=10s --start-period=15s --retries=3 \
    CMD ipmitool -I lanplus -H 127.0.0.1 -U ${IPMI_USER:-admin} -P ${IPMI_PASS:-password} mc info || exit 1
```

IPMI 経由で BMC に接続できることを確認する。qemu-bmc プロセスの死活だけでなく、IPMI プロトコルレベルでの正常性を検証できる。

## 4. entrypoint.sh

環境変数を読み取り、qemu-bmc の CLI フラグと `--qemu-args` を構築して `exec` する。

### 4.1 対応する環境変数

| 環境変数 | デフォルト | 用途 |
|---------|-----------|------|
| `VM_MEMORY` | `2048` | VM メモリ (MB) |
| `VM_CPUS` | `2` | VM CPU 数 |
| `VM_DISK` | `/vm/disk.qcow2` | メインディスクパス |
| `VM_CDROM` | （空） | CD-ROM ISO パス |
| `VM_BOOT` | `c` | ブートデバイス (c/d/n) |
| `VM_BOOT_MODE` | `bios` | ブートモード (bios/uefi) |
| `VM_BOOT_MENU_TIMEOUT` | `0` | ブートメニュー表示時間 (ms) |
| `ENABLE_KVM` | `true` | KVM 有効化 |
| `VNC_PORT` | `5900` | VNC ポート |
| `VM_NETWORKS` | （空） | VM パススルー NIC (カンマ区切り) |
| `IPMI_USER` | `admin` | IPMI/Redfish ユーザー |
| `IPMI_PASS` | `password` | IPMI/Redfish パスワード |
| `IPMI_PORT` | `623` | IPMI UDP ポート |
| `REDFISH_PORT` | `443` | Redfish HTTPS ポート |
| `TLS_CERT` | （空） | TLS 証明書パス |
| `TLS_KEY` | （空） | TLS 鍵パス |
| `VM_IPMI_ADDR` | （空） | VM IPMI アドレス |
| `DEBUG` | `false` | デバッグモード |
| `QEMU_EXTRA_ARGS` | （空） | 追加 QEMU 引数（上級者向け） |

### 4.2 実装

```bash
#!/bin/bash
set -e

# ランタイムディレクトリ作成
mkdir -p /var/run/qemu /var/log/qemu

# --- QEMU 引数の構築 ---
QEMU_ARGS=""

# マシンタイプとアクセラレーション
if [ "${ENABLE_KVM:-true}" = "true" ] && [ -e /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
    QEMU_ARGS="$QEMU_ARGS -machine q35,accel=kvm -cpu host"
else
    QEMU_ARGS="$QEMU_ARGS -machine q35,accel=tcg -cpu qemu64"
    [ "${ENABLE_KVM:-true}" = "true" ] && echo "WARN: KVM not available, falling back to TCG" >&2
fi

# リソース
QEMU_ARGS="$QEMU_ARGS -m ${VM_MEMORY:-2048} -smp ${VM_CPUS:-2}"

# ディスク
VM_DISK="${VM_DISK:-/vm/disk.qcow2}"
[ -n "$VM_DISK" ] && [ -f "$VM_DISK" ] && \
    QEMU_ARGS="$QEMU_ARGS -drive file=$VM_DISK,format=qcow2,if=virtio"
[ -n "$VM_CDROM" ] && [ -f "$VM_CDROM" ] && \
    QEMU_ARGS="$QEMU_ARGS -cdrom $VM_CDROM"

# ブートデバイス
BOOT_PARAM="${VM_BOOT:-c}"
if [ "${VM_BOOT_MENU_TIMEOUT:-0}" -gt 0 ] 2>/dev/null; then
    BOOT_PARAM="$BOOT_PARAM,menu=on,splash-time=${VM_BOOT_MENU_TIMEOUT}"
fi
QEMU_ARGS="$QEMU_ARGS -boot $BOOT_PARAM"

# VNC
VNC_DISPLAY=$(( ${VNC_PORT:-5900} - 5900 ))
QEMU_ARGS="$QEMU_ARGS -vnc :$VNC_DISPLAY"

# ネットワーク
source /scripts/setup-network.sh
NET_ARGS=$(build_network_args 2>/dev/null || true)
if [ -n "$NET_ARGS" ]; then
    QEMU_ARGS="$QEMU_ARGS $NET_ARGS"
else
    QEMU_ARGS="$QEMU_ARGS -nic none"
fi

# 追加引数
[ -n "$QEMU_EXTRA_ARGS" ] && QEMU_ARGS="$QEMU_ARGS $QEMU_EXTRA_ARGS"

# --- qemu-bmc CLI フラグの構築 ---
BMC_FLAGS=""
BMC_FLAGS="$BMC_FLAGS --ipmi-user ${IPMI_USER:-admin}"
BMC_FLAGS="$BMC_FLAGS --ipmi-pass ${IPMI_PASS:-password}"
BMC_FLAGS="$BMC_FLAGS --ipmi-port ${IPMI_PORT:-623}"
BMC_FLAGS="$BMC_FLAGS --redfish-port ${REDFISH_PORT:-443}"
BMC_FLAGS="$BMC_FLAGS --boot-mode ${VM_BOOT_MODE:-bios}"

[ -n "$TLS_CERT" ] && BMC_FLAGS="$BMC_FLAGS --tls-cert $TLS_CERT"
[ -n "$TLS_KEY" ] && BMC_FLAGS="$BMC_FLAGS --tls-key $TLS_KEY"
[ -n "$VM_IPMI_ADDR" ] && BMC_FLAGS="$BMC_FLAGS --vm-ipmi-addr $VM_IPMI_ADDR"

# デバッグ出力
if [ "${DEBUG:-false}" = "true" ]; then
    echo "=== qemu-bmc startup ==="
    echo "BMC_FLAGS: $BMC_FLAGS"
    echo "QEMU_ARGS: $QEMU_ARGS"
    echo "========================"
fi

# qemu-bmc を起動（PID 1 として exec）
exec qemu-bmc $BMC_FLAGS --qemu-args="$QEMU_ARGS"
```

### 4.3 設計意図

- **環境変数の知識はすべて entrypoint.sh に閉じる。** qemu-bmc は環境変数を一切読まない。
- **KVM 判定は entrypoint.sh が行う。** qemu-bmc は渡された `-machine` パラメータをそのまま使う。
- **ネットワーク設定（TAP/ブリッジ）も entrypoint.sh が行う。** QEMU に渡す `-netdev`/`-device` 引数を構築して `--qemu-args` に含める。

## 5. setup-network.sh

docker-qemu-bmc から移植する。主な機能:

### 5.1 インターフェース検出

```
VM_NETWORKS が指定されている場合: カンマ区切りをパース
未指定の場合: eth2 以降を自動検出（eth0=管理、eth1=IPMI をスキップ）
```

### 5.2 TAP/ブリッジ作成

各インターフェースに対して:
1. TAP デバイス作成 (`ip tuntap add`)
2. ブリッジ作成 (`ip link add type bridge`)
3. ホストインターフェースとTAPをブリッジに接続
4. ホストインターフェースのIPアドレスをフラッシュ（L2ブリッジ化）

### 5.3 MAC アドレス生成

```
プレフィックス: 52:54:00 (QEMU OUI)
残り3バイト: md5(interface_name) の先頭6文字
→ 同じインターフェース名なら常に同じ MAC（決定論的）
```

### 5.4 出力

`build_network_args` 関数が QEMU に渡す引数文字列を stdout に出力:
```
-netdev tap,id=net0,ifname=tap0,script=no,downscript=no
-device virtio-net-pci,netdev=net0,mac=52:54:00:xx:yy:zz
```

## 6. docker-compose.yml

```yaml
services:
  qemu-bmc:
    build:
      context: .
      dockerfile: docker/Dockerfile
    image: ghcr.io/tjst-t/qemu-bmc:latest
    container_name: qemu-bmc
    hostname: qemu-bmc
    restart: unless-stopped

    devices:
      - /dev/kvm:/dev/kvm
      - /dev/net/tun:/dev/net/tun

    cap_add:
      - NET_ADMIN
      - SYS_ADMIN

    security_opt:
      - apparmor=unconfined

    ports:
      - "5900:5900"       # VNC
      - "623:623/udp"     # IPMI
      - "443:443"         # Redfish

    volumes:
      - ./vm:/vm:rw
      - ./iso:/iso:ro

    environment:
      - VM_MEMORY=2048
      - VM_CPUS=2
      - VM_DISK=/vm/disk.qcow2
      - VM_BOOT_MODE=bios
      - ENABLE_KVM=true
      - VNC_PORT=5900
      - IPMI_USER=admin
      - IPMI_PASS=password

    healthcheck:
      test: ["CMD", "ipmitool", "-I", "lanplus", "-H", "127.0.0.1",
             "-U", "admin", "-P", "password", "mc", "info"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 15s
```

## 7. containerlab 統合

### 7.1 example.yml

docker-qemu-bmc のトポロジ例を移植。2ノード構成:

```yaml
name: qemu-bmc-lab

topology:
  nodes:
    node1:
      kind: linux
      image: ghcr.io/tjst-t/qemu-bmc:latest
      binds:
        - ../vm/node1:/vm
        - ../iso:/iso
      ports:
        - "5901:5900"
        - "6231:623/udp"
      env:
        VM_MEMORY: "2048"
        VM_CPUS: "2"
        VM_BOOT_MODE: "bios"
        IPMI_USER: "admin"
        IPMI_PASS: "password"

    node2:
      kind: linux
      image: ghcr.io/tjst-t/qemu-bmc:latest
      binds:
        - ../vm/node2:/vm
        - ../iso:/iso
      ports:
        - "5902:5900"
        - "6232:623/udp"
      env:
        VM_MEMORY: "2048"
        VM_CPUS: "2"

    mgmt-switch:
      kind: linux
      image: alpine:latest
      exec:
        - ip link add br0 type bridge
        - ip link set br0 up
        - ip link set eth1 master br0
        - ip link set eth2 master br0

  links:
    - endpoints: ["node1:eth1", "mgmt-switch:eth1"]
    - endpoints: ["node2:eth1", "mgmt-switch:eth2"]
    - endpoints: ["node1:eth2", "node2:eth2"]
```

## 8. CI/CD (GitHub Actions)

### 8.1 イメージ公開ワークフロー

```yaml
name: Build and Push Container Image

on:
  push:
    tags: ["v*"]
  workflow_dispatch:

permissions:
  contents: read
  packages: write

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: docker/setup-buildx-action@v3

      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - uses: docker/metadata-action@v5
        id: meta
        with:
          images: ghcr.io/${{ github.repository }}
          tags: |
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=raw,value=latest,enable={{is_default_branch}}

      - uses: docker/build-push-action@v5
        with:
          context: .
          file: docker/Dockerfile
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

### 8.2 イメージ名

```
ghcr.io/tjst-t/qemu-bmc:latest
ghcr.io/tjst-t/qemu-bmc:v2.0.0
ghcr.io/tjst-t/qemu-bmc:2.0
```

## 9. ホスト要件

### 9.1 必須デバイス・ケーパビリティ

```yaml
devices:
  - /dev/kvm:/dev/kvm           # KVM アクセラレーション（なければ TCG フォールバック）
  - /dev/net/tun:/dev/net/tun   # TAP デバイス作成（ネットワークパススルー時）

cap_add:
  - NET_ADMIN                    # TAP/ブリッジ作成・操作
  - SYS_ADMIN                    # デバイスアクセス

security_opt:
  - apparmor=unconfined          # QEMU のデバイスアクセスに必要
```

### 9.2 `--privileged` との関係

`--privileged` でも動作するが、上記の最小ケーパビリティ指定を推奨する。

## 10. docker-qemu-bmc リポジトリの扱い

### 10.1 移行方針

- docker-qemu-bmc リポジトリを archived にする
- README に qemu-bmc リポジトリへの誘導を記載
- 既存の GHCR イメージ (`ghcr.io/tjst-t/docker-qemu-bmc`) は残すが、更新は停止

### 10.2 イメージ名の変更

```
旧: ghcr.io/tjst-t/docker-qemu-bmc:latest
新: ghcr.io/tjst-t/qemu-bmc:latest
```

## 11. テストケース

コンテナ統合テストは bash ベースで実装する。テストランナーとヘルパー関数は docker-qemu-bmc から移植・改修する。

### 11.1 テスト実行基盤

```bash
# 全テスト実行
./tests/run_tests.sh all

# カテゴリ別実行
./tests/run_tests.sh container   # コンテナ基盤テスト
./tests/run_tests.sh ipmi        # IPMI テスト
./tests/run_tests.sh redfish     # Redfish テスト
./tests/run_tests.sh power       # 電源制御テスト
./tests/run_tests.sh network     # ネットワークテスト
./tests/run_tests.sh boot        # ブートデバイステスト
./tests/run_tests.sh cross       # クロスプロトコルテスト
./tests/run_tests.sh quick       # スモークテスト
```

テスト結果ログは `tests/evidence/` に保存する。

### 11.2 カテゴリ 1: コンテナ基盤 (container)

コンテナのビルド・起動・プロセス構成を検証する。

| # | テスト名 | 検証内容 |
|---|---------|---------|
| 1 | `test_docker_build` | Dockerfile がエラーなくビルドできること |
| 2 | `test_container_starts` | コンテナが起動し running 状態になること |
| 3 | `test_qemu_bmc_pid1` | qemu-bmc が PID 1 で動作していること（supervisord ではない） |
| 4 | `test_qemu_process_running` | QEMU プロセスが qemu-bmc の子プロセスとして動作していること |
| 5 | `test_no_supervisord` | supervisord プロセスが存在しないこと |
| 6 | `test_no_ipmi_sim` | ipmi_sim プロセスが存在しないこと |
| 7 | `test_vnc_port_listening` | VNC ポート (5900) が LISTEN していること |
| 8 | `test_healthcheck_passes` | Docker ヘルスチェックが healthy を返すこと |
| 9 | `test_graceful_shutdown` | `docker stop` でコンテナが正常終了すること（タイムアウトなし） |

### 11.3 カテゴリ 2: 環境変数と entrypoint.sh (entrypoint)

entrypoint.sh の環境変数→CLI フラグ変換を検証する。

| # | テスト名 | 検証内容 |
|---|---------|---------|
| 1 | `test_default_memory` | VM_MEMORY 未指定時に QEMU が `-m 2048` で起動すること |
| 2 | `test_custom_memory` | `VM_MEMORY=4096` で起動すると QEMU が `-m 4096` で動作すること |
| 3 | `test_default_cpus` | VM_CPUS 未指定時に QEMU が `-smp 2` で起動すること |
| 4 | `test_custom_cpus` | `VM_CPUS=4` で起動すると QEMU が `-smp 4` で動作すること |
| 5 | `test_kvm_enabled` | `ENABLE_KVM=true` かつ /dev/kvm が利用可能な場合、`accel=kvm` で起動すること |
| 6 | `test_kvm_fallback_tcg` | /dev/kvm が存在しない場合、`accel=tcg` にフォールバックすること |
| 7 | `test_custom_vnc_port` | `VNC_PORT=5901` で起動すると `-vnc :1` で動作すること |
| 8 | `test_disk_attached` | VM_DISK に存在するファイルを指定すると `-drive file=...` が含まれること |
| 9 | `test_disk_missing_no_error` | VM_DISK のファイルが存在しない場合、ディスクなしで起動すること |
| 10 | `test_cdrom_attached` | VM_CDROM に ISO を指定すると `-cdrom` が含まれること |
| 11 | `test_boot_device_default` | VM_BOOT 未指定時にデフォルトブートで起動すること |
| 12 | `test_custom_ipmi_credentials` | `IPMI_USER=test IPMI_PASS=test123` で認証が通ること |
| 13 | `test_qemu_extra_args` | `QEMU_EXTRA_ARGS="-device virtio-rng-pci"` が QEMU に渡されること |
| 14 | `test_debug_output` | `DEBUG=true` で起動パラメータがログ出力されること |

### 11.4 カテゴリ 3: IPMI (ipmi)

IPMI プロトコルの動作を検証する。qemu-bmc 側にも Go の統合テストがあるが、コンテナ環境での動作確認として独立して実施する。

| # | テスト名 | 検証内容 |
|---|---------|---------|
| 1 | `test_udp_623_listening` | UDP 623 が LISTEN していること |
| 2 | `test_ipmi_lan_connection` | IPMI 1.5 (lan) で mc info が成功すること |
| 3 | `test_ipmi_lanplus_connection` | IPMI 2.0 (lanplus) で mc info が成功すること |
| 4 | `test_mc_info_content` | mc info に Device ID, Firmware Revision, IPMI Version が含まれること |
| 5 | `test_ipmi_version_2` | IPMI Version が 2.0 であること |
| 6 | `test_auth_correct` | 正しい認証情報で接続が成功すること |
| 7 | `test_auth_wrong_password` | 誤ったパスワードで接続が拒否されること |
| 8 | `test_auth_wrong_username` | 誤ったユーザー名で接続が拒否されること |

### 11.5 カテゴリ 4: Redfish (redfish)

Redfish API の動作を検証する。docker-qemu-bmc にはなかった新テスト。

| # | テスト名 | 検証内容 |
|---|---------|---------|
| 1 | `test_redfish_port_listening` | HTTPS 443 (または設定ポート) が LISTEN していること |
| 2 | `test_redfish_service_root` | `GET /redfish/v1` が ServiceRoot を返すこと |
| 3 | `test_redfish_no_auth_rejected` | 認証なしのリクエストが 401 を返すこと |
| 4 | `test_redfish_systems` | `GET /redfish/v1/Systems/1` が PowerState を含むこと |
| 5 | `test_redfish_power_off` | `POST .../ComputerSystem.Reset` で ForceOff が成功すること |
| 6 | `test_redfish_power_on` | `POST .../ComputerSystem.Reset` で On が成功すること |
| 7 | `test_redfish_managers` | `GET /redfish/v1/Managers/1` が BMC 情報を返すこと |
| 8 | `test_redfish_chassis` | `GET /redfish/v1/Chassis/1` がシャーシ情報を返すこと |

### 11.6 カテゴリ 5: 電源制御 (power)

プロセスベースの電源制御モデルを検証する。旧 Phase 4 の置き換え。

| # | テスト名 | 検証内容 |
|---|---------|---------|
| 1 | `test_initial_power_on` | コンテナ起動後、電源状態が On であること |
| 2 | `test_power_status` | `ipmitool power status` が正しい状態を返すこと |
| 3 | `test_power_off` | `power off` で QEMU プロセスが終了すること |
| 4 | `test_power_off_state` | 電源 OFF 後、`power status` が Off を返すこと |
| 5 | `test_power_on` | `power on` で QEMU プロセスが新たに起動すること |
| 6 | `test_power_on_state` | 電源 ON 後、`power status` が On を返すこと |
| 7 | `test_power_cycle_pid_changes` | `power cycle` で QEMU の PID が変わること（プロセス再起動） |
| 8 | `test_power_cycle_state_on` | `power cycle` 後に電源状態が On であること |
| 9 | `test_power_reset_pid_unchanged` | `power reset` で QEMU の PID が変わらないこと（QMP reset） |
| 10 | `test_power_reset_state_on` | `power reset` 後に電源状態が On であること |
| 11 | `test_graceful_shutdown` | `power soft` (ACPI) でゲストにシャットダウンシグナルが送られること |
| 12 | `test_power_off_on_cycle` | Off → On → Off → On の状態遷移が正しく動作すること |
| 13 | `test_qemu_crash_detection` | QEMU プロセスを kill した場合、電源状態が Off に遷移すること |
| 14 | `test_power_on_after_crash` | QEMU クラッシュ後に `power on` で再起動できること |

### 11.7 カテゴリ 6: ブートデバイス (boot)

IPMI/Redfish 経由のブートデバイス変更が QEMU に反映されることを検証する。

| # | テスト名 | 検証内容 |
|---|---------|---------|
| 1 | `test_bootdev_pxe` | `chassis bootdev pxe` が受理されること |
| 2 | `test_bootdev_disk` | `chassis bootdev disk` が受理されること |
| 3 | `test_bootdev_cdrom` | `chassis bootdev cdrom` が受理されること |
| 4 | `test_bootdev_bios` | `chassis bootdev bios` が受理されること |
| 5 | `test_bootdev_applied_after_cycle` | ブートデバイス設定後の power cycle で QEMU が新しい `-boot` パラメータで起動すること |
| 6 | `test_bootdev_once_reset` | `BootSourceOverrideEnabled=Once` が起動後に Disabled にリセットされること |
| 7 | `test_bootdev_continuous` | `BootSourceOverrideEnabled=Continuous` が起動後もリセットされないこと |
| 8 | `test_bootdev_redfish_pxe` | Redfish PATCH で BootSourceOverrideTarget=Pxe が設定できること |
| 9 | `test_bootmode_bios` | `VM_BOOT_MODE=bios` でコンテナ起動時に SGA デバイスが含まれること |
| 10 | `test_bootmode_uefi` | `VM_BOOT_MODE=uefi` でコンテナ起動時に OVMF pflash が含まれること |

### 11.8 カテゴリ 7: ネットワーク (network)

TAP/ブリッジ作成と VM ネットワークパススルーを検証する。

| # | テスト名 | 検証内容 |
|---|---------|---------|
| 1 | `test_no_network_default` | VM_NETWORKS 未指定時に `-nic none` で起動すること |
| 2 | `test_tap_device_created` | VM_NETWORKS 指定時に TAP デバイスが作成されること |
| 3 | `test_bridge_created` | VM_NETWORKS 指定時にブリッジが作成されること |
| 4 | `test_tap_connected_to_bridge` | TAP デバイスがブリッジに接続されていること |
| 5 | `test_host_iface_on_bridge` | ホストインターフェースがブリッジに接続されていること |
| 6 | `test_bridge_no_ip` | ブリッジに IP アドレスが付与されていないこと（L2 のみ） |
| 7 | `test_mac_consistency` | 同じインターフェース名で同じ MAC アドレスが生成されること |
| 8 | `test_mac_uniqueness` | 異なるインターフェースで異なる MAC アドレスが生成されること |
| 9 | `test_qemu_network_args` | QEMU プロセスに `-netdev tap` 引数が含まれること |

### 11.9 カテゴリ 8: クロスプロトコル (cross)

IPMI と Redfish 間の状態一貫性を検証する。

| # | テスト名 | 検証内容 |
|---|---------|---------|
| 1 | `test_ipmi_off_redfish_verify` | IPMI で power off → Redfish API で PowerState=Off を確認 |
| 2 | `test_ipmi_on_redfish_verify` | IPMI で power on → Redfish API で PowerState=On を確認 |
| 3 | `test_redfish_off_ipmi_verify` | Redfish で ForceOff → IPMI power status で Off を確認 |
| 4 | `test_redfish_on_ipmi_verify` | Redfish で On → IPMI power status で On を確認 |
| 5 | `test_ipmi_bootdev_redfish_verify` | IPMI で bootdev pxe → Redfish GET で BootSourceOverrideTarget=Pxe を確認 |
| 6 | `test_redfish_bootdev_ipmi_verify` | Redfish で boot target Cd → IPMI chassis bootparam で cdrom を確認 |

### 11.10 カテゴリ 9: スモークテスト (quick)

CI やデプロイ後の簡易検証用。30 秒以内に完了すること。

| # | テスト名 | 検証内容 |
|---|---------|---------|
| 1 | `test_container_running` | コンテナが running 状態であること |
| 2 | `test_ipmi_responds` | `ipmitool mc info` が成功すること |
| 3 | `test_redfish_responds` | `curl /redfish/v1` が 200 を返すこと |
| 4 | `test_power_status` | `power status` が On または Off を返すこと |

### 11.11 テストヘルパー関数

docker-qemu-bmc の `test_helper.sh` から移植・改修する:

```bash
# アサーション
assert_equals()        # 値の一致
assert_contains()      # 文字列の包含
assert_not_contains()  # 文字列の非包含
assert_success()       # コマンド成功 (exit 0)
assert_failure()       # コマンド失敗 (exit != 0)

# コンテナ操作
start_test_container() # テスト用コンテナ起動（環境変数オーバーライド対応）
stop_test_container()  # テスト用コンテナ停止・削除
container_exec()       # コンテナ内コマンド実行

# IPMI ヘルパー
ipmi_cmd()             # ipmitool -I lanplus でコマンド実行
ipmi_cmd_wrong_pass()  # 誤パスワードでのコマンド実行
ipmi_cmd_wrong_user()  # 誤ユーザー名でのコマンド実行

# Redfish ヘルパー（新規）
redfish_get()          # curl で GET リクエスト（Basic Auth 付き）
redfish_post()         # curl で POST リクエスト
redfish_patch()        # curl で PATCH リクエスト
redfish_get_no_auth()  # 認証なし GET リクエスト

# 待機関数
wait_for_power_state() # 指定の電源状態になるまで待機
wait_for_qemu_running() # QEMU プロセスが起動するまで待機
wait_for_qemu_stopped() # QEMU プロセスが停止するまで待機
get_qemu_pid()         # QEMU の PID を取得
```

### 11.12 テスト環境の前提条件

- Docker が利用可能であること
- `ipmitool` がインストールされていること
- `curl` がインストールされていること（Redfish テスト用）
- `/dev/kvm` が利用可能であること（KVM テスト。TCG フォールバックテストでは不要）

### 11.13 テスト数サマリー

| カテゴリ | テスト数 | 備考 |
|---------|---------|------|
| container | 9 | 旧 Phase 1+2 の置き換え |
| entrypoint | 14 | 環境変数→CLI フラグ変換 |
| ipmi | 8 | 旧 Phase 3 の置き換え |
| redfish | 8 | 新規 |
| power | 14 | 旧 Phase 4 の置き換え + クラッシュ検知 |
| boot | 10 | 旧 bootmode + Phase 4 bootdev の統合 |
| network | 9 | 旧 Phase 5 の置き換え |
| cross | 6 | 新規（クロスプロトコル） |
| quick | 4 | スモークテスト |
| **合計** | **82** | |

## 12. 優先順位

1. **必須（P0）:** Dockerfile、entrypoint.sh、setup-network.sh の移植
2. **重要（P1）:** docker-compose.yml、GHCR 公開ワークフロー、テスト基盤 (container + ipmi + power + quick)
3. **望ましい（P2）:** containerlab example、Redfish テスト、クロスプロトコルテスト、ドキュメント
4. **後回し可（P3）:** docker-qemu-bmc の archived 化・リダイレクト
