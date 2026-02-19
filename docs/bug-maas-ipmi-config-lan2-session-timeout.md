# Bug: MAAS Commissioning 失敗 - ipmi-config LAN_2_0 でのセッションタイムアウト

**Date:** 2026-02-19
**Version:** qemu-bmc v0.5.3
**Status:** Open
**Severity:** High（MAAS によるノード Commissioning/Power-on が動作しない）

## 症状

MAAS によるノードの Commissioning / Power-on 時に以下のエラーが発生する。

```
Failed to change the boot order to PXE 192.168.100.11: ipmi_ctx_open_outofband_2_0: session timeout
```

MAAS journald ログ（`maas-agent`）から抜粋:

```
ERR Error executing power command error="exit status 1"
stderr="using a non-secure cipher suite id
Failed to change the boot order to PXE 192.168.100.11: ipmi_ctx_open_outofband_2_0: session timeout
...（4回リトライ後）
provisioningserver.drivers.power.PowerConnError: The IPMI session has timed out."
```

## 環境

| 項目 | 値 |
|------|-----|
| qemu-bmc | v0.5.3 |
| 検証トポロジー | clab-iaas `maas-minimal` |
| BMC IP | 192.168.100.11 |
| MAAS snap | 40962 (v3.5.10) |
| FreeIPMI (ipmipower / ipmi-config) | 1.6.9 |

## 動作比較

| コマンド / 操作 | 結果 |
|----------------|------|
| `ipmipower --stat` | **成功** |
| `ipmipower --on / --off` | **成功** |
| `ipmipower --cipher-suite-id=0..17` | **全て成功** |
| `ipmi-config --checkout`（`--driver-type` 指定なし） | **成功** |
| `ipmi-config --driver-type LAN_2_0 --checkout` | **失敗** `session timeout` |
| MAAS Commissioning / Power-on | **失敗** |

## 再現手順

### 前提条件

- [clab-iaas](https://github.com/tjst-t/clab-iaas) リポジトリの `maas-minimal` トポロジーが起動済みであること

```bash
cd topologies/maas-minimal
sudo clab deploy -t maas-minimal.clab.yml
```

### 再現手順 1: ipmipower は成功することの確認

```bash
docker exec clab-maas-minimal-maas bash -c "
  LD_LIBRARY_PATH=/snap/maas/40962/usr/lib/x86_64-linux-gnu:/snap/maas/40962/lib/x86_64-linux-gnu \
  /snap/maas/40962/usr/sbin/ipmipower \
    -h 192.168.100.11 -u admin -p password --stat
"
```

**結果（成功）:**

```
192.168.100.11: on
```

### 再現手順 2: ipmi-config（driver-type なし）は成功することの確認

```bash
docker exec clab-maas-minimal-maas bash -c "
  LD_LIBRARY_PATH=/snap/maas/40962/usr/lib/x86_64-linux-gnu:/snap/maas/40962/lib/x86_64-linux-gnu \
  /snap/maas/40962/usr/sbin/ipmi-config \
    -h 192.168.100.11 -u admin -p password --checkout
" | head -5
```

**結果（成功）:**

```
# Section UserX Comments
...
```

### 再現手順 3: ipmi-config LAN_2_0 の失敗確認

```bash
docker exec clab-maas-minimal-maas bash -c "
  LD_LIBRARY_PATH=/snap/maas/40962/usr/lib/x86_64-linux-gnu:/snap/maas/40962/lib/x86_64-linux-gnu \
  /snap/maas/40962/usr/sbin/ipmi-config \
    --driver-type LAN_2_0 \
    -h 192.168.100.11 -u admin -p password --checkout
"
```

**結果（失敗）:**

```
ipmi_ctx_open_outofband_2_0: session timeout
```

### 再現手順 4: MAAS が実際に実行するコマンドの失敗確認

MAAS の Python IPMI ドライバー（`ipmi.py`）は以下のコマンドシーケンスを実行する。

```bash
# ipmi-chassis-config は ipmi-config --category=chassis のラッパー
docker exec clab-maas-minimal-maas bash -c "
  export LD_LIBRARY_PATH=/snap/maas/40962/usr/lib/x86_64-linux-gnu:/snap/maas/40962/lib/x86_64-linux-gnu
  /snap/maas/40962/usr/sbin/ipmi-config \
    --category=chassis \
    --driver-type LAN_2_0 \
    -W opensesspriv \
    -h 192.168.100.11 -u admin -p password \
    --checkout
"
```

**結果（失敗）:**

```
ipmi_ctx_open_outofband_2_0: session timeout
```

## テストケース（MAAS 発行コマンド）

`ipmi.py` のコード分析に基づき、MAAS が実際に発行するコマンドのテストケース一覧。
修正後の検証に使用する。

### 前提（共通変数）

```bash
BMC=192.168.100.11
SNAP=/snap/maas/40962
LIB="$SNAP/usr/lib/x86_64-linux-gnu:$SNAP/lib/x86_64-linux-gnu"
```

### TC-01: power_query（`ipmipower --stat`）

```bash
docker exec clab-maas-minimal-maas bash -c "
  LD_LIBRARY_PATH=$LIB \
  $SNAP/usr/sbin/ipmipower \
    -W opensesspriv \
    --driver-type LAN_2_0 \
    -h $BMC -u admin -p password \
    -I 3 -l OPERATOR \
    --stat
"
```

| 状態 | 期待結果 |
|------|---------|
| v0.5.3（修正前） | `192.168.100.11: session timeout`（失敗） |
| 修正後 | `192.168.100.11: on`（成功） |

### TC-02: power_on Step 1（`ipmi-chassis-config --commit`、boot_type=auto）

ブート順序を PXE に変更する。`ipmi-chassis-config` は `ipmi-config --category=chassis` のラッパー。
失敗しても MAAS は WARNING ログのみ出力して Step 2 に進む（`PowerAuthError` 以外は例外なし）。

```bash
docker exec clab-maas-minimal-maas bash -c "
  cat > /tmp/ipmi-pxe-boot.conf << 'EOF'
Section Chassis_Boot_Flags
        Boot_Flags_Persistent                         No
        Boot_Device                                   PXE
EndSection
EOF
  LD_LIBRARY_PATH=$LIB \
  $SNAP/usr/sbin/ipmi-config --category=chassis \
    -W opensesspriv \
    --driver-type LAN_2_0 \
    -h $BMC -u admin -p password \
    -I 3 -l OPERATOR \
    --commit --filename /tmp/ipmi-pxe-boot.conf
"
```

| 状態 | 期待結果 |
|------|---------|
| v0.5.3（修正前） | `ipmi_ctx_open_outofband_2_0: session timeout`（失敗） |
| 修正後 | exit 0（成功） |

### TC-03: power_on Step 2（`ipmipower --cycle --on-if-off`）

電源を投入する（サイクルリセット、電源 OFF 時は ON する）。
失敗すると `PowerConnError` が raise されて Commissioning が失敗する。

```bash
docker exec clab-maas-minimal-maas bash -c "
  LD_LIBRARY_PATH=$LIB \
  $SNAP/usr/sbin/ipmipower \
    -W opensesspriv \
    --driver-type LAN_2_0 \
    -h $BMC -u admin -p password \
    -I 3 -l OPERATOR \
    --cycle --on-if-off
"
```

| 状態 | 期待結果 |
|------|---------|
| v0.5.3（修正前） | `192.168.100.11: session timeout`（失敗） |
| 修正後 | `192.168.100.11: on`（成功） |

### TC-04: power_off（`ipmipower --off`、hard モード）

```bash
docker exec clab-maas-minimal-maas bash -c "
  LD_LIBRARY_PATH=$LIB \
  $SNAP/usr/sbin/ipmipower \
    -W opensesspriv \
    --driver-type LAN_2_0 \
    -h $BMC -u admin -p password \
    -I 3 -l OPERATOR \
    --off
"
```

| 状態 | 期待結果 |
|------|---------|
| v0.5.3（修正前） | `192.168.100.11: session timeout`（失敗） |
| 修正後 | `192.168.100.11: off`（成功） |

### TC-05: power_off（`ipmipower --soft`、soft モード）

`power_off_mode="soft"` 設定時。ACPI シャットダウン要求を送信する。

```bash
docker exec clab-maas-minimal-maas bash -c "
  LD_LIBRARY_PATH=$LIB \
  $SNAP/usr/sbin/ipmipower \
    -W opensesspriv \
    --driver-type LAN_2_0 \
    -h $BMC -u admin -p password \
    -I 3 -l OPERATOR \
    --soft
"
```

| 状態 | 期待結果 |
|------|---------|
| v0.5.3（修正前） | `192.168.100.11: session timeout`（失敗） |
| 修正後 | `192.168.100.11: off`（成功） |

### TC-06: power_on Step 1（boot_type=EFI）

`power_boot_type="efi"` 設定時、`BIOS_Boot_Type EFI` が追加される。

```bash
docker exec clab-maas-minimal-maas bash -c "
  cat > /tmp/ipmi-efi-boot.conf << 'EOF'
Section Chassis_Boot_Flags
        Boot_Flags_Persistent                         No
        BIOS_Boot_Type                                EFI
        Boot_Device                                   PXE
EndSection
EOF
  LD_LIBRARY_PATH=$LIB \
  $SNAP/usr/sbin/ipmi-config --category=chassis \
    -W opensesspriv \
    --driver-type LAN_2_0 \
    -h $BMC -u admin -p password \
    -I 3 -l OPERATOR \
    --commit --filename /tmp/ipmi-efi-boot.conf
"
```

| 状態 | 期待結果 |
|------|---------|
| v0.5.3（修正前） | `ipmi_ctx_open_outofband_2_0: session timeout`（失敗） |
| 修正後 | exit 0（成功） |

### TC-07: power_on Step 1（boot_type=Legacy）

`power_boot_type="legacy"` 設定時、`BIOS_Boot_Type PC-COMPATIBLE` が追加される。

```bash
docker exec clab-maas-minimal-maas bash -c "
  cat > /tmp/ipmi-legacy-boot.conf << 'EOF'
Section Chassis_Boot_Flags
        Boot_Flags_Persistent                         No
        BIOS_Boot_Type                                PC-COMPATIBLE
        Boot_Device                                   PXE
EndSection
EOF
  LD_LIBRARY_PATH=$LIB \
  $SNAP/usr/sbin/ipmi-config --category=chassis \
    -W opensesspriv \
    --driver-type LAN_2_0 \
    -h $BMC -u admin -p password \
    -I 3 -l OPERATOR \
    --commit --filename /tmp/ipmi-legacy-boot.conf
"
```

| 状態 | 期待結果 |
|------|---------|
| v0.5.3（修正前） | `ipmi_ctx_open_outofband_2_0: session timeout`（失敗） |
| 修正後 | exit 0（成功） |

## 分析

### MAAS IPMI ドライバーのコマンドシーケンス

MAAS の `/snap/maas/40962/lib/python3.10/site-packages/provisioningserver/drivers/power/ipmi.py` は以下のシーケンスで動作する。

**デフォルトパラメーター:**

| パラメーター | デフォルト値 | 説明 |
|------------|------------|------|
| `power_driver` | `LAN_2_0` | IPMI 2.0 (RMCP+) を強制使用 |
| `cipher_suite_id` | `3` | HMAC-SHA1::HMAC-SHA1-96::AES-CBC-128 |
| `privilege_level` | `OPERATOR` | 操作権限レベル |
| `workaround_flags` | `opensesspriv` | FreeIPMI ワークアラウンド |
| `power_off_mode` | `hard` | 強制電源断（`soft` 設定時は ACPI shutdown） |
| `power_boot_type` | `auto` | 起動タイプ（EFI/Legacy 自動検出） |

**power_on（Commissioning / Deploy 時）:**

```
Step 1: ipmi-chassis-config（ブート順序を PXE に変更）
  -W opensesspriv
  --driver-type LAN_2_0
  -h <BMC_IP> -u <user> -p <pass>
  -I 3 -l OPERATOR
  --commit --filename <tmpfile>

  <tmpfile> の内容:
    Section Chassis_Boot_Flags
            Boot_Flags_Persistent                         No
            Boot_Device                                   PXE
    EndSection

  ※ 失敗しても PowerAuthError 以外は WARNING ログのみ（処理継続）

Step 2: ipmipower（電源投入）
  -W opensesspriv
  --driver-type LAN_2_0
  -h <BMC_IP> -u <user> -p <pass>
  -I 3 -l OPERATOR
  --cycle --on-if-off

  ※ 失敗すると PowerConnError が raise されて Commissioning 失敗
```

**power_off:**

```
ipmipower
  -W opensesspriv
  --driver-type LAN_2_0
  -h <BMC_IP> -u <user> -p <pass>
  -I 3 -l OPERATOR
  --off   # power_off_mode="soft" の場合は --soft
```

**power_query（電源状態確認）:**

```
ipmipower
  -W opensesspriv
  --driver-type LAN_2_0
  -h <BMC_IP> -u <user> -p <pass>
  -I 3 -l OPERATOR
  --stat
```

`ipmi-chassis-config` の実体は `/snap/maas/40962/usr/sbin/ipmi-chassis-config` というシェルスクリプトで:

```sh
#!/bin/sh
exec /usr/sbin/ipmi-config --category=chassis "$@"
```

### `--driver-type LAN_2_0` が問題の原因

`--driver-type LAN_2_0` を指定すると `ipmi-config` は強制的に IPMI 2.0 (RMCP+) セッションを使用する。
指定しない場合はデフォルトで IPMI 1.5 セッションを使用し、これは qemu-bmc で正常に動作する。

### デバッグトレースによる詳細分析（v0.5.3）

`--debug` フラグを付けてパケットレベルで観察した結果（`ipmi-config --driver-type LAN_2_0 --debug`）:

```
1. IPMI 1.5 Get Channel Authentication Capabilities → 成功
2. IPMI 2.0 Open Session (cipher suite 3)          → 成功
3. IPMI 2.0 RAKP Message 1 Request                 → 成功
4. IPMI 2.0 RAKP Message 2 Response                → 成功
5. IPMI 2.0 RAKP Message 3 Request                 → 成功
6. IPMI 2.0 RAKP Message 4 Response                → 成功
7. Set Session Privilege Level（暗号化, seq=1）
   payload_type.authenticated = 1
   payload_type.encrypted = 1
   → レスポンスあり: comp_code=0h, privilege_level=4h, session_sequence_number=0h ← 問題
8. Set Session Privilege Level（暗号化, seq=2） → レスポンス: seq=0h ← 問題
9. Set Session Privilege Level（暗号化, seq=3） → レスポンス: seq=0h ← 問題
...（以降ループ、リトライ上限に達し session timeout）
```

**v0.5.2 → v0.5.3 での変化:**

| フェーズ | v0.5.2 以前 | v0.5.3 |
|---------|------------|--------|
| Open Session / RAKP | 失敗（FreeIPMI がエコーバック拒否） | 成功 |
| 暗号化コマンドへの応答 | **無応答** | **応答あり**（ただし seq=0 が不正） |
| 最終結果 | session timeout | session timeout（ループ後） |

### 根本原因

**qemu-bmc が RMCP+ セッションレスポンスの `session_sequence_number` をインクリメントしていない（常に 0 を返す）。**

IPMI 2.0 仕様（Section 13.28.6）では、BMC は各セッションに対してアウトバウンドシーケンス番号を管理し、
レスポンスごとにインクリメントして送信しなければならない。

**観測された動作:**

- クライアント（FreeIPMI）のリクエスト seq: 1, 2, 3, 4, 5... （正しくインクリメント）
- qemu-bmc のレスポンス seq: **0, 0, 0, 0, 0...** （常に 0 のまま）

FreeIPMI はレスポンスをデコードして内容を表示するが、`session_sequence_number` が
期待するウィンドウ外（0 はウィンドウ外）であるため、コマンド成功として認識しない。
同じ Set Session Privilege Level コマンドをリトライし続け、リトライ上限に達すると
`session timeout` エラーとなる。

**注意:** MAAS は `ipmipower` にも `--driver-type LAN_2_0` を付与するため（コードのコメント参照）、
`ipmipower` による power_query / power_on / power_off の全操作も同様に session timeout で失敗する。
「`ipmipower` は正常動作する」という従来の観測は `--driver-type` を指定しない場合（IPMI 1.5）に限定される。

**要修正箇所（qemu-bmc 実装側）:**

1. `rmcp_plus.go` or `session.go`: セッションごとに BMC アウトバウンドシーケンス番号（`managed_system_outbound_seq`）を管理
2. 暗号化レスポンス送信時に `session_sequence_number` フィールドに現在値をセットしてインクリメント
3. IPMI 2.0 spec section 13.28.6: "The Managed System shall increment the outbound Session Sequence Number prior to sending each new packet"

## 影響範囲

- MAAS によるノード Commissioning が完全に機能しない
  - PXE ブート順序変更失敗 → Commissioning 失敗
  - Power-on 操作失敗
- `--driver-type LAN_2_0` を使用するすべての FreeIPMI コマンドが影響を受ける
  - `ipmi-config --driver-type LAN_2_0`（ブート順序変更）
  - `ipmipower --driver-type LAN_2_0`（電源 on/off/query）
- `--driver-type` を指定しない場合（IPMI 1.5）は影響を受けない

## ワークアラウンド

現時点で有効なワークアラウンドは確認されていない。

MAAS の IPMI ドライバーは `--driver-type LAN_2_0` を常に使用するため、ドライバー側での回避は困難。

## 修正方針（案）

BMC アウトバウンドセッションシーケンス番号の管理を実装する:

1. `Session` 構造体に `managedSystemOutboundSeq uint32` フィールドを追加（初期値 1）
2. RMCP+ セッションのレスポンス構築時に `session_sequence_number` に現在値をセットする
3. レスポンス送信後にシーケンス番号をインクリメントする
4. （任意）クライアントのインバウンドシーケンス番号の検証ウィンドウ実装（IPMI 2.0 spec section 13.29）

## 関連

- [bug-freeipm-compatibility.md](./bug-freeipm-compatibility.md) — v0.5.3 で修正した FreeIPMI 互換性問題（RAKP ネゴシエーション）
