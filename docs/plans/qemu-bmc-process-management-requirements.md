# qemu-bmc QEMU プロセス管理機能 要望書

## 1. 背景

docker-qemu-bmc プロジェクトでは、現在 `ipmi_sim` (OpenIPMI lanserv) + シェルスクリプト群 + supervisord の構成で BMC エミュレーションを実現している。これを `qemu-bmc` 単一バイナリに置き換えることで、以下を実現したい:

- supervisord、シェルスクリプト群（chassis-control.sh、power-control.sh、start-ipmi.sh、sol-bridge.sh）の廃止
- 単一バイナリによるシンプルなコンテナ構成
- 実機 BMC と同様の「BMC がホストの電源を管理する」アーキテクチャ

**現在の qemu-bmc は、既に起動済みの QEMU インスタンスに QMP 接続する前提で設計されている。** 本要望は、qemu-bmc 自身が QEMU プロセスのライフサイクル（起動・停止・監視）を管理する機能の追加を求めるものである。

## 2. 現行アーキテクチャ（置き換え対象）

```
supervisord (PID 1)
├── ipmi_sim (priority 10)     ← qemu-bmc が置き換え
│   ├── IPMI UDP:623
│   ├── SOL → tcp:localhost:9002
│   └── chassis_control → /scripts/chassis-control.sh
│       ├── supervisorctl start/stop qemu
│       └── QMP system_reset / system_powerdown
├── QEMU/KVM (priority 20)    ← qemu-bmc が管理
│   ├── QMP: /var/run/qemu/qmp.sock
│   ├── Serial: tcp:localhost:9002
│   └── VNC: :5900
└── sol-bridge (priority 25)   ← 廃止
```

### 現行の電源制御モデル

| 操作 | 現行の実装 |
|------|-----------|
| Power On | `supervisorctl start qemu` → QEMU プロセス起動 |
| Power Off | `supervisorctl stop qemu` → QEMU プロセス終了 |
| Power Cycle | supervisorctl stop → start |
| Reset | QMP `system_reset`（プロセス継続）|
| Reset (bootdev変更後) | supervisorctl stop → start（新パラメータで再起動）|
| Soft Shutdown | QMP `system_powerdown`（ACPI シャットダウン）|

## 3. 目標アーキテクチャ

```
qemu-bmc (PID 1)
├── QEMU プロセス管理
│   ├── QEMU プロセスの起動・停止・監視
│   ├── QMP ソケット接続管理
│   └── 必須パラメータの自動注入・検証
├── IPMI サーバー (UDP:623)
├── Redfish サーバー (HTTPS:443)
├── VM IPMI サーバー (TCP, optional)
└── [将来] SOL サーバー
```

## 4. 設計方針

### 4.1 基本原則

qemu-bmc 自身の設定（ポート、認証情報等）は、現行通り **環境変数** で受け取る。コンテナ環境の標準（12-factor app）に従い、変更しない。

QEMU に渡す引数は、`--` セパレータ以降の **CLI 引数** としてパススルーで受け取る。

### 4.2 QEMU 引数の受け渡し: `--` セパレータ

Unix の標準慣行（`docker run`, `kubectl exec` 等と同様）に従い、`--` 以降を QEMU 引数として扱う。Go の `flag.Args()` で個別引数の `[]string` として取得できるため、文字列パースが不要で安全に操作できる。

```bash
qemu-bmc -- -machine q35,accel=kvm -cpu host -m 2048 -smp 2 \
    -drive file=/vm/disk.qcow2,format=qcow2,if=virtio -vnc :0
```

### 4.3 動作モード

| モード | 条件 | 動作 |
|--------|------|------|
| プロセス管理モード | `--` 以降に QEMU 引数あり | qemu-bmc が QEMU プロセスを起動・管理する |
| レガシーモード | `--` 以降に QEMU 引数なし | 既存動作: 外部で起動済みの QEMU に QMP 接続のみ |

レガシーモードにより後方互換性を維持する。placemat 等、QEMU を外部で管理するユースケースに対応。

### 4.4 責務の分離

| 責務 | 担当 |
|------|------|
| QEMU 引数の構築（環境変数→引数変換） | docker-qemu-bmc (entrypoint.sh) |
| ネットワーク設定（TAP/ブリッジ作成） | docker-qemu-bmc (entrypoint.sh) |
| QEMU 必須パラメータの自動注入 | qemu-bmc |
| QEMU 必須パラメータの競合検証 | qemu-bmc |
| デフォルト値の補完 | qemu-bmc |
| QEMU プロセスの起動・停止・監視 | qemu-bmc |
| IPMI/Redfish プロトコル処理 | qemu-bmc |

## 5. qemu-bmc 設定仕様

### 5.1 既存環境変数（変更なし）

| 環境変数 | デフォルト | 説明 |
|----------|-----------|------|
| `QMP_SOCK` | `/var/run/qemu/qmp.sock` | QMP ソケットパス |
| `IPMI_USER` | `admin` | IPMI/Redfish ユーザー名 |
| `IPMI_PASS` | `password` | IPMI/Redfish パスワード |
| `IPMI_PORT` | `623` | IPMI UDP ポート |
| `REDFISH_PORT` | `443` | Redfish HTTPS ポート |
| `TLS_CERT` | （空） | TLS 証明書パス |
| `TLS_KEY` | （空） | TLS 鍵パス |
| `VM_BOOT_MODE` | `bios` | ブートモード: `bios` or `uefi` |
| `VM_IPMI_ADDR` | （空） | VM IPMI リッスンアドレス |
| `SERIAL_ADDR` | `localhost:9002` | SOL ブリッジ先アドレス |

### 5.2 新規追加環境変数

| 環境変数 | デフォルト | 説明 |
|----------|-----------|------|
| `QEMU_BINARY` | `qemu-system-x86_64` | QEMU 実行ファイルパス |

### 5.3 使用例

```bash
# レガシーモード: 外部 QEMU に接続（現行動作、後方互換）
qemu-bmc

# プロセス管理モード: 最小構成（デフォルト値で QEMU 起動）
qemu-bmc -- -machine q35,accel=tcg

# プロセス管理モード: フル構成
IPMI_USER=admin IPMI_PASS=secret REDFISH_PORT=8443 VM_BOOT_MODE=uefi \
qemu-bmc -- \
    -machine q35,accel=kvm -cpu host -m 2048 -smp 2 \
    -drive file=/vm/disk.qcow2,format=qcow2,if=virtio \
    -cdrom /iso/install.iso \
    -vnc :0 \
    -netdev tap,id=net0,ifname=tap0,script=no,downscript=no \
    -device virtio-net-pci,netdev=net0,mac=52:54:00:aa:bb:cc
```

## 6. QEMU パラメータの自動注入と検証

注: 本セクションはプロセス管理モード時のみ適用される。

### 6.1 qemu-bmc が自動注入するパラメータ

以下のパラメータは qemu-bmc が QEMU 起動時に自動的に追加する。ユーザーが `--` 以降で指定する必要はない。

| パラメータ | 注入される値 | 目的 |
|-----------|-------------|------|
| `-qmp` | `unix:${QMP_SOCK},server,nowait` | QMP 制御ソケット |
| `-chardev` + `-serial` | シリアルコンソール設定（§6.5参照） | SOL 用 |
| `-display none` | ヘッドレス動作 | コンテナ環境向け |

### 6.2 競合検証（バリデーション）

`--` 以降の QEMU 引数に以下のパラメータが含まれている場合、**エラーで起動を拒否**し、メッセージで理由を通知する:

| 禁止パラメータ | 理由 |
|---------------|------|
| `-qmp` | qemu-bmc が QMP ソケットを管理するため |
| `-chardev` with `id=serial0` | qemu-bmc が SOL 用シリアルを管理するため |
| `-serial` | 同上 |
| `-monitor stdio` | qemu-bmc が stdio を使用するため |
| `-daemonize` | qemu-bmc が子プロセスとして管理するため |

### 6.3 デフォルト値の補完

`--` 以降の QEMU 引数に以下のパラメータが含まれていない場合、qemu-bmc がデフォルト値を自動追加する:

| パラメータ | 不在時のデフォルト | 説明 |
|-----------|------------------|------|
| `-machine` | `-machine q35` | マシンタイプ |
| `-m` | `-m 2048` | メモリ 2GB |
| `-smp` | `-smp 2` | CPU 2コア |
| `-vga` | `-vga std` | VGA アダプタ |

注: アクセラレーション（`accel=kvm` vs `accel=tcg`）、CPU モデル（`-cpu host` vs `-cpu qemu64`）はユーザーが明示的に指定する。qemu-bmc は関知しない。

### 6.4 ブートモード連携

環境変数 `VM_BOOT_MODE` に応じて、qemu-bmc が以下を自動処理する:

**UEFI モード** (`VM_BOOT_MODE=uefi`):

`--` 以降に pflash 関連の `-drive` が含まれていない場合、自動注入:
```
-drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd
-drive if=pflash,format=raw,file=/var/run/qemu/OVMF_VARS.fd
```
OVMF_VARS のインスタンスコピーも自動処理する。

**BIOS モード** (`VM_BOOT_MODE=bios`):

シリアルコンソール用に以下を自動注入:
```
-device sga
-fw_cfg name=etc/sercon-port,string=0x3f8
```

### 6.5 シリアルコンソール自動注入

環境変数 `SERIAL_ADDR` の値に基づいて以下を自動注入:
```
-chardev socket,id=serial0,host=${host},port=${port},server=on,wait=off,telnet=off
-serial chardev:serial0
```

### 6.6 ブートデバイスオーバーライド

IPMI/Redfish 経由でブートデバイスが変更された場合、QEMU 再起動時に `-boot` パラメータを動的に変更する:

- `--` 以降の引数に `-boot` が含まれていれば、その次の引数をオーバーライドで置換
- 含まれていなければ、`-boot` パラメータを追加

**BootOverride.Target から QEMU `-boot` パラメータへのマッピング:**

| BootOverride.Target | QEMU -boot パラメータ |
|---------------------|---------------------|
| None | （オリジナルの引数を維持） |
| Pxe | n（ネットワーク） |
| Hdd | c（ハードディスク） |
| Cd | d（CD-ROM） |
| BiosSetup | menu=on（ブートメニュー表示） |

`BootSourceOverrideEnabled == "Once"` の場合、起動後に自動リセット。

## 7. QEMU プロセスライフサイクル管理

注: 本セクションはプロセス管理モード時のみ適用される。レガシーモードでは現行動作を維持する。

### 7.1 起動

- qemu-bmc 起動時に QEMU プロセスを自動的に起動すること
- `--` 以降の引数 + 自動注入パラメータからコマンドラインを構築
- QMP ソケットが利用可能になるまでポーリングで待機すること（タイムアウト付き）
- QMP 接続確立後に IPMI/Redfish サーバーを開始すること

### 7.2 停止

- qemu-bmc が SIGTERM/SIGINT を受信した場合、QEMU プロセスを graceful に停止すること
- QEMU が応答しない場合のタイムアウト付き強制終了（SIGKILL）を実装すること

### 7.3 監視

- QEMU プロセスの異常終了を検知すること
- 異常終了時はログ出力し、電源状態を "Off" に更新すること
  - 自動再起動は**しない**（実機 BMC と同じ挙動）

### 7.4 電源制御モデルの変更

**現在の qemu-bmc は QMP の `stop`/`cont` で VM 一時停止/再開している。プロセス管理モードでは、これを実際のプロセス起動/停止に変更する。**

| 操作 | 要求される実装 |
|------|--------------|
| Power On | QEMU プロセスを起動（起動済みなら no-op） |
| Power Off (ForceOff) | QMP `quit` で QEMU プロセスを終了 |
| Power Cycle | QMP `quit` → QEMU プロセス再起動 |
| Hard Reset | QMP `system_reset`（プロセス継続） |
| Graceful Shutdown | QMP `system_powerdown`（ACPI）→ QEMU 自発終了を待機（タイムアウト 120 秒、超過時はログ出力のみで強制終了しない） |
| Graceful Restart | QMP `system_powerdown` → 終了待機（タイムアウト 120 秒、超過時は強制終了）→ QEMU プロセス再起動 |

注: Graceful Shutdown のタイムアウトは「ゲスト OS がシャットダウンしない」ケースへの対策。タイムアウト後も QEMU プロセスは維持し、ユーザーが ForceOff で明示的に停止する想定（実機 BMC と同じ挙動）。Graceful Restart ではタイムアウト後に強制終了して再起動に進む。

### 7.5 電源状態の判定

```
QEMU プロセスが存在しない → PowerOff
QEMU プロセスが存在し、QMP status == "running" → PowerOn
QEMU プロセスが存在し、QMP status == "paused" → PowerOn（※実機と同等）
QEMU プロセスが存在し、QMP status == "shutdown" → ゲストがシャットダウン完了
```

注: ゲストが `shutdown` コマンドを実行して QMP status が "shutdown" になった場合、QEMU プロセスを終了して PowerOff に遷移すること。

## 8. 起動シーケンス

### プロセス管理モード

```
qemu-bmc 起動
  │
  ├─ 1. 環境変数の読み込み
  ├─ 2. `--` 以降の QEMU 引数を取得（flag.Args()）
  ├─ 3. QEMU 引数の検証（競合チェック）
  ├─ 4. デフォルト値の補完
  ├─ 5. ランタイムディレクトリ作成 (/var/run/qemu 等)
  ├─ 6. QEMU コマンドライン構築（ユーザー引数 + 自動注入）
  ├─ 7. QEMU プロセス起動 (exec.Command → Start)
  ├─ 8. QMP ソケット接続待機（ポーリング、タイムアウト付き）
  ├─ 9. QMP 接続確立
  ├─ 10. VM IPMI サーバー起動（設定されている場合）
  ├─ 11. IPMI サーバー起動 (UDP:623)
  ├─ 12. Redfish サーバー起動 (HTTPS:443)
  └─ 13. シグナルハンドラ登録（SIGTERM → graceful shutdown）

graceful shutdown:
  ├─ IPMI/Redfish サーバー停止
  ├─ QMP `quit` 送信
  ├─ QEMU プロセス終了待機（タイムアウト 30 秒）
  ├─ タイムアウト時は SIGKILL
  └─ 終了
```

### レガシーモード

```
qemu-bmc 起動
  │
  ├─ 1. 環境変数の読み込み
  ├─ 2. QMP ソケット接続（既存動作）
  ├─ 3. VM IPMI サーバー起動（設定されている場合）
  ├─ 4. IPMI サーバー起動 (UDP:623)
  ├─ 5. Redfish サーバー起動 (HTTPS:443)
  └─ 6. シグナルハンドラ登録
```

## 9. docker-qemu-bmc 側の統合計画

qemu-bmc にプロセス管理機能が実装された後、docker-qemu-bmc 側では以下の変更を行う。

### 削除するファイル
- `configs/supervisord.conf`
- `configs/ipmi_sim/lan.conf`
- `configs/ipmi_sim/ipmisim.emu`
- `scripts/start-ipmi.sh`
- `scripts/start-qemu.sh`
- `scripts/chassis-control.sh`
- `scripts/power-control.sh`
- `scripts/sol-bridge.sh`

### entrypoint.sh の役割

entrypoint.sh が環境変数から QEMU 引数を構築し、`--` 以降の引数として qemu-bmc に渡す:

```bash
#!/bin/bash
set -e

# ランタイムディレクトリ作成
mkdir -p /var/run/qemu /var/log/qemu

# QEMU 引数を配列で構築（クォーティング安全）
QEMU_ARGS=()

# マシンタイプとアクセラレーション
if [ "$ENABLE_KVM" = "true" ] && [ -e /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
    QEMU_ARGS+=(-machine q35,accel=kvm -cpu host)
else
    QEMU_ARGS+=(-machine q35,accel=tcg -cpu qemu64)
fi

# リソース
QEMU_ARGS+=(-m "${VM_MEMORY:-2048}" -smp "${VM_CPUS:-2}")

# ディスク
[ -n "$VM_DISK" ] && [ -f "$VM_DISK" ] && \
    QEMU_ARGS+=(-drive "file=$VM_DISK,format=qcow2,if=virtio")
[ -n "$VM_CDROM" ] && [ -f "$VM_CDROM" ] && \
    QEMU_ARGS+=(-cdrom "$VM_CDROM")

# ブートデバイス
QEMU_ARGS+=(-boot "${VM_BOOT:-c}")

# VNC
VNC_DISPLAY=$(( ${VNC_PORT:-5900} - 5900 ))
QEMU_ARGS+=(-vnc ":$VNC_DISPLAY")

# ネットワーク（TAP/ブリッジ作成 + QEMU 引数）
if [ -n "$VM_NETWORKS" ]; then
    source /scripts/setup-network.sh
    build_network_args QEMU_ARGS
else
    QEMU_ARGS+=(-nic none)
fi

# qemu-bmc を起動（-- 以降が QEMU 引数）
exec qemu-bmc -- "${QEMU_ARGS[@]}"
```

### 新しい Dockerfile

```dockerfile
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    qemu-system-x86 \
    qemu-utils \
    iproute2 \
    ovmf \
    && rm -rf /var/lib/apt/lists/*

COPY --from=ghcr.io/tjst-t/qemu-bmc:latest /usr/local/bin/qemu-bmc /usr/local/bin/qemu-bmc
COPY scripts/entrypoint.sh /scripts/
COPY scripts/setup-network.sh /scripts/
RUN chmod +x /scripts/*.sh

EXPOSE 623/udp 443/tcp 5900/tcp

ENTRYPOINT ["/scripts/entrypoint.sh"]
```

## 10. インターフェース変更案

### 10.1 Machine インターフェース拡張

```go
type MachineInterface interface {
    // 既存（Redfish 用）
    GetPowerState() (PowerState, error)
    GetQMPStatus() (qmp.Status, error)
    Reset(resetType string) error
    GetBootOverride() BootOverride
    SetBootOverride(override BootOverride) error
    InsertMedia(image string) error
    EjectMedia() error

    // 新規: QEMU プロセス管理（プロセス管理モード時のみ使用）
    StartVM() error    // QEMU プロセスを起動し QMP 接続を確立
    StopVM() error     // QEMU プロセスを終了（graceful → force）
    IsVMRunning() bool // QEMU プロセスの生存確認
}
```

注: 現行コードでは Redfish と IPMI で別々の MachineInterface を定義している。新規メソッド（StartVM/StopVM/IsVMRunning）は IPMI 側にも追加が必要。

### 10.2 QMP Client の変更

プロセス管理モードでは QEMU の起動/停止に伴い QMP 接続が切断・再確立される。以下の対応が必要:

- QMP Client インターフェースに `Connect()` メソッドを追加し、再接続を可能にする
- QMP ソケットが存在しない状態（QEMU 未起動）でも panic しないこと
- QEMU 起動後、QMP ソケットが利用可能になるまでリトライすること（タイムアウト付き）
- QEMU 未起動時に QMP コマンドを呼び出した場合、適切なエラーを返すこと（例: `ErrNotConnected`）
- `Quit()` 呼び出し後、プロセス終了を `Wait()` で確認すること

```go
type Client interface {
    // 既存
    QueryStatus() (Status, error)
    SystemPowerdown() error
    SystemReset() error
    Stop() error
    Cont() error
    Quit() error
    BlockdevChangeMedium(device, filename string) error
    BlockdevRemoveMedium(device string) error
    Close() error

    // 新規
    Connect() error  // QMP ソケットに接続（再接続対応）
}
```

## 11. テスト要件

### ユニットテスト
- QEMU 引数の検証（競合検出）
- デフォルト値補完ロジック
- 自動注入パラメータの正確性
- ブートデバイスオーバーライドの `-boot` 置換（スライス操作）
- プロセス管理モード vs レガシーモードの判定

### 統合テスト
- QEMU プロセスの起動・停止
- Power On → QMP 接続確認 → Power Off → プロセス終了確認
- Power Cycle（プロセス再起動、新しい PID）
- Reset（QMP system_reset、同じ PID）
- ブートデバイス変更 → Power Cycle → 新ブートパラメータ適用
- QEMU 異常終了 → 電源状態が Off に遷移
- Graceful Shutdown → ACPI → QEMU 自発終了
- Graceful Shutdown タイムアウト → QEMU プロセス維持確認

## 12. スコープ外（将来課題）

- **SOL (Serial Over LAN):** qemu-bmc での SOL 実装は別タスクとする
- **QEMU プロセスの自動再起動:** 実機 BMC と同様、Power On コマンドがない限り再起動しない
- **ネットワーク設定の Go 移植:** 初期実装では docker-qemu-bmc の entrypoint.sh が担当。将来的に qemu-bmc に移植可能
- **ライブマイグレーション:** 将来の拡張として検討

## 13. 優先順位

1. **必須（P0）:** `--` パススルー受け取り・検証・自動注入、QEMU プロセス管理、電源制御モデル変更
2. **重要（P1）:** ブートデバイスの `-boot` パラメータ動的置換、デフォルト値補完
3. **望ましい（P2）:** UEFI 自動設定（pflash 注入）、BIOS シリアル設定注入
4. **後回し可（P3）:** qemu-bmc 内でのネットワーク設定サポート
