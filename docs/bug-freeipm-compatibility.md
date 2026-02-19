# Bug: FreeIPMI (ipmipower/ipmi-config) との互換性問題

**Date:** 2026-02-19
**Version:** qemu-bmc v0.5.1
**Status:** Fixed
**Severity:** High（MAAS などの FreeIPMI ベースの BMC クライアントが使用不可）

## 症状

MAAS によるノードの Commissioning/Power-on 時に以下のエラーが発生し、BMC 操作が失敗する。

```
using a non-secure cipher suite id
Failed to change the boot order to PXE 192.168.100.11: ipmi_ctx_open_outofband_2_0: session timeout
Failed to change the boot order to PXE 192.168.100.11: /usr/sbin/ipmi-config: connection timeout
PowerConnError: The IPMI session has timed out. MAAS performed several retries.
```

MAAS の journald ログ（`maas-agent`）から抜粋:

```
ERR Error executing power command error="exit status 1"
stderr="using a non-secure cipher suite id\n
Failed to change the boot order to PXE 192.168.100.11: ipmi_ctx_open_outofband_2_0: session timeout\n
...
provisioningserver.drivers.power.PowerConnError: The IPMI session has timed out."
```

## 環境

| 項目 | 値 |
|------|-----|
| qemu-bmc | v0.5.1 |
| 検証トポロジー | clab-iaas `maas-minimal` |
| BMC IP | 192.168.100.11 |
| MAAS snap | 40962 |
| FreeIPMI (ipmipower) | 1.6.9 |
| ipmitool | 動作環境内の最新版 |

## 再現手順

### 前提条件

- [clab-iaas](https://github.com/tjst-t/clab-iaas) リポジトリの `maas-minimal` トポロジーが起動済みであること

```bash
cd topologies/maas-minimal
sudo clab deploy -t maas-minimal.clab.yml
```

### 再現手順 1: FreeIPMI での直接テスト

MAAS コンテナ内から FreeIPMI (ipmipower) を使って接続を試みる。

```bash
# MAAS snap 内の FreeIPMI バイナリを使用
docker exec clab-maas-minimal-maas bash -c "
  LD_LIBRARY_PATH=/snap/maas/40962/usr/lib/x86_64-linux-gnu:/snap/maas/40962/lib/x86_64-linux-gnu \
  /snap/maas/40962/usr/sbin/ipmipower \
    -h 192.168.100.11 -u admin -p password --stat
"
```

**結果（失敗）:**

```
192.168.100.11: connection timeout
```

### 再現手順 2: 全暗号スイートでのテスト

```bash
docker exec clab-maas-minimal-maas bash -c "
  for cs in 0 1 2 3 6 7 11 12 17; do
    result=\$(LD_LIBRARY_PATH=/snap/maas/40962/usr/lib/x86_64-linux-gnu:/snap/maas/40962/lib/x86_64-linux-gnu \
      /snap/maas/40962/usr/sbin/ipmipower \
        -h 192.168.100.11 -u admin -p password --stat --cipher-suite-id=\$cs 2>&1)
    echo \"cipher-suite \$cs: \$result\"
  done
"
```

**結果（全スイート失敗）:**

```
cipher-suite 0:  192.168.100.11: session timeout
cipher-suite 1:  192.168.100.11: session timeout
cipher-suite 2:  192.168.100.11: connection timeout
cipher-suite 3:  192.168.100.11: connection timeout
cipher-suite 6:  192.168.100.11: session timeout
cipher-suite 7:  192.168.100.11: connection timeout
cipher-suite 11: 192.168.100.11: connection timeout
cipher-suite 12: 192.168.100.11: session timeout
cipher-suite 17: 192.168.100.11: connection timeout
```

### 再現手順 3: ipmitool では成功することの確認

同じコンテナ・同じ IP に対して ipmitool は正常に動作する。

```bash
docker exec clab-maas-minimal-maas \
  ipmitool -I lanplus -H 192.168.100.11 -U admin -P password chassis status
```

**結果（成功）:**

```
System Power         : on
Power Overload       : false
...
```

暗号スイート 3 を明示指定しても成功:

```bash
docker exec clab-maas-minimal-maas \
  ipmitool -I lanplus -H 192.168.100.11 -U admin -P password -C 3 chassis status
```

## 分析

| クライアント | 結果 | 備考 |
|-------------|------|------|
| ipmitool (OpenIPMI 実装) | **成功** | デフォルト・cipher 3 ともに OK |
| FreeIPMI ipmipower 1.6.9 | **失敗** | 全暗号スイートで timeout |
| FreeIPMI ipmi-config 1.6.9 | **失敗** | connection timeout |

エラーの種類:
- `connection timeout` : IPMI 2.0 RAKP ハンドシェイクが開始できない（cipher suite ネゴシエーション失敗）
- `session timeout` : RAKP は開始するがセッション確立に失敗

## 根本原因

### 原因 1: Open Session Response でのアルゴリズムエコーバック（Critical）

**該当コード:** `internal/ipmi/rmcp_plus.go:100-143` (`handleOpenSession()`)

IPMI 2.0 仕様では、BMC は Open Session Response でサポートするアルゴリズムを応答すべきだが、
現実装ではクライアントが提案した値をそのままエコーバックしている。

```go
// rmcp_plus.go:125 - クライアントの値をそのまま返している
binary.Write(resp, binary.LittleEndian, req.AuthPayloadAlgorithm)
// rmcp_plus.go:132
binary.Write(resp, binary.LittleEndian, req.IntegrityPayloadAlgorithm)
// rmcp_plus.go:139
binary.Write(resp, binary.LittleEndian, req.ConfPayloadAlgorithm)
```

**問題の詳細:**

FreeIPMI の各 cipher suite ID は、認証・完全性・機密性アルゴリズムの組み合わせを定義する（IPMI 2.0 Table 22-20）。
FreeIPMI はクライアント側で cipher suite ID からアルゴリズムコードに変換して Open Session Request を送信するが、
BMC がそのままエコーバックすると FreeIPMI は「BMC が提案したアルゴリズムが期待と異なる」と判断し、
セッションを拒否する場合がある。

| Cipher Suite ID | 認証 (Auth) | 完全性 (Integrity) | 機密性 (Conf) |
|:---:|:---:|:---:|:---:|
| 0 | RAKP-None (0x00) | None (0x00) | None (0x00) |
| 1 | RAKP-HMAC-SHA1 (0x01) | None (0x00) | None (0x00) |
| 2 | RAKP-HMAC-SHA1 (0x01) | HMAC-SHA1-96 (0x01) | None (0x00) |
| 3 | RAKP-HMAC-SHA1 (0x01) | HMAC-SHA1-96 (0x01) | AES-CBC-128 (0x01) |
| 6 | RAKP-HMAC-MD5 (0x02) | MD5-128 (0x02) | None (0x00) |
| 7 | RAKP-HMAC-MD5 (0x02) | MD5-128 (0x02) | AES-CBC-128 (0x01) |
| 11 | RAKP-HMAC-MD5 (0x02) | MD5-128 (0x02) | AES-CBC-128 (0x01) |
| 12 | RAKP-HMAC-MD5 (0x02) | MD5-128 (0x02) | xRC4-128 (0x02) |
| 17 | RAKP-HMAC-SHA256 (0x03) | HMAC-SHA256-128 (0x03) | AES-CBC-128 (0x01) |

現在の qemu-bmc は RAKP-HMAC-SHA1 + HMAC-SHA1-96 + AES-CBC-128 (= cipher suite 3) のみ実装しているが、
クライアントがどのアルゴリズムを提案しても無条件にエコーバックして成功を返すため:

- **cipher suite 0, 1, 6, 12**: サポート外のアルゴリズムを受け入れたと応答するが、RAKP ハンドシェイクで実際には HMAC-SHA1 を使うため不整合が発生 → `session timeout`
- **cipher suite 2, 3, 7, 11, 17**: FreeIPMI がレスポンスの検証でエコーバック値の形式を期待と異なると判定 → `connection timeout`

### 原因 2: アルゴリズムの検証・ネゴシエーション未実装

`handleOpenSession()` にアルゴリズム検証ロジックがない。本来は:

1. クライアントが提案したアルゴリズムが BMC でサポートされているか検証する
2. サポート外の場合はエラーステータス（0x11: invalid integrity algorithm 等）を返す
3. サポートしている場合は BMC が選択したアルゴリズムコードを応答する

### ipmitool が成功する理由

ipmitool は Open Session Response のアルゴリズム値を厳密に検証しない。BMC がエコーバックした値をそのまま受け入れ、
RAKP ハンドシェイクに進む。デフォルトで cipher suite 3 (RAKP-HMAC-SHA1 + HMAC-SHA1-96 + AES-CBC-128) を使用するため、
qemu-bmc の唯一の実装アルゴリズムと一致し、セッション確立に成功する。

### セッション確立フロー比較

```
ipmitool (成功):                          FreeIPMI (失敗):
  |-- Open Session (cipher 3) -->|          |-- Open Session (cipher N) -->|
  |<-- Response (echo back) -----|          |<-- Response (echo back) -----|
  |   [検証なし、続行]             |          |   [アルゴリズム値を厳密検証]   |
  |-- RAKP1 --->|                           |   → 検証失敗、接続中断        |
  |<-- RAKP2 ---|                           |   → connection/session timeout|
  |-- RAKP3 --->|
  |<-- RAKP4 ---|
  |   [セッション確立完了]         |
```

## 影響範囲

- MAAS（FreeIPMI ベースの BMC ドライバーを使用）によるノード管理が完全に機能しない
  - Commissioning 失敗
  - Power-on / Power-off 操作失敗
  - PXE ブート順序変更失敗
- FreeIPMI を使用する他のツール（freeipmi CLI, OpenStack Ironic の一部構成など）も影響を受ける可能性がある

## 修正方針

### 必須対応

1. **Open Session Response でのアルゴリズムネゴシエーション実装** (`rmcp_plus.go:handleOpenSession()`)
   - クライアントが提案したアルゴリズムをサポート済みアルゴリズム（RAKP-HMAC-SHA1, HMAC-SHA1-96, AES-CBC-128）と照合する
   - サポートしている場合: BMC がサポートするアルゴリズムコードを返す
   - サポート外の場合: エラーステータスコードを返す（例: `0x11` Invalid Integrity Algorithm）

2. **セッションに選択済みアルゴリズムを保存** (`session.go`)
   - `Session` 構造体にネゴシエーション結果（Auth/Integrity/Conf アルゴリズム）を保存する
   - 以降の RAKP ハンドシェイク・暗号化で使用するアルゴリズムをセッションごとに参照する

### 段階的な追加対応（任意）

3. **追加 cipher suite のサポート**
   - cipher suite 0 (認証なし): テスト用途
   - cipher suite 17 (RAKP-HMAC-SHA256): セキュリティ強化
   - MAAS がデフォルトで使用する cipher suite への対応を優先

## 修正内容（実施済み）

以下のコード修正により FreeIPMI との完全互換が達成された（workaround 不要）。

### 修正 1: Open Session Response のアルゴリズム検証 (`rmcp_plus.go`)

`handleOpenSession()` にアルゴリズム検証ロジックを追加。サポート外の cipher suite を正しい IPMI 2.0 エラーコード（0x11/0x12/0x18）で即座に拒否するよう変更。

### 修正 2: IPMI 1.5 レスポンスのシーケンス番号エコー (`rmcp.go`, `server.go`)

`SerializeIPMIResponse` に `reqSeqLun` パラメータを追加し、リクエストのシーケンス番号をレスポンスに正しくエコーバックするよう修正。FreeIPMI が `req_seq failed: 0h; expected = Bh` と報告していた問題が解決。

### 修正 3: IPMI 1.5 MD5 認証コード計算 (`rmcp.go`)

FreeIPMI の `ipmipower` は RMCP+ セッション確立前に IPMI 1.5 MD5 認証セッションを試みる。`SerializeIPMIResponse` に MD5 認証コードの正確な計算を実装（IPMI 1.5 仕様 section 5.2.2 準拠）。

```
auth_code = MD5(password[16] || sessionID[4] || ipmi_msg || seqNum[4] || password[16])
```

## テスト結果

修正後の動作確認（FreeIPMI 1.6.13、ipmitool で実施）：

```
=== ipmitool cipher suite 3 ===
System Power: on  ✓

=== ipmipower cipher suite 3 (no workaround) ===
127.0.0.1: on  ✓

=== ipmipower --on ===
127.0.0.1: ok  ✓

=== ipmipower --off ===
127.0.0.1: ok  ✓
```

## ワークアラウンド

修正前は `noauthcodecheck` が必要だったが、修正後は不要。
