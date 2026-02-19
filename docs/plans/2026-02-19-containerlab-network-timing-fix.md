# Containerlab ネットワークインターフェース待機の実装

## 背景

containerlabでqemu-bmcコンテナを使用する際、`VM_NETWORKS`で指定したインターフェースがentrypoint実行時にまだ存在せず、VMにNICが渡されない問題がある。

### containerlabのコンテナ起動シーケンス

```
1. docker create + start  →  entrypoint.sh 実行開始
2. containerlab がリンク作成  →  eth1, eth2 等がコンテナに差し込まれる
3. containerlab が exec 実行  →  ip addr add 等のポストセットアップ
```

entrypoint.shは手順1で実行されるが、eth2は手順2で初めて作成される。そのため `build_network_args` がeth2を見つけられず、QEMUが `-nic none` で起動してしまう。

### 現在のワークアラウンド（clab-iaas側）

clab側のトポロジー定義で、execコマンドにより手動でブリッジに追加している:

```yaml
server01:
  image: ghcr.io/tjst-t/qemu-bmc:latest
  env:
    VM_NETWORKS: eth2
  exec:
    - ip link set eth2 master br0       # ← ワークアラウンド
    - ip addr flush dev eth2            # ← ワークアラウンド
```

この方法の問題点:
- qemu-bmcの内部実装（ブリッジ名 `br0`）に依存しており壊れやすい
- すべてのトポロジー定義にワークアラウンドが必要
- entrypointでtap0とbr0は作成されるがeth2がブリッジに入らない中途半端な状態になる

## 修正方針

`docker/entrypoint.sh` で `build_network_args` を呼ぶ前に、`VM_NETWORKS` で指定されたインターフェースの出現を待つ。

### 対象ファイル

- `docker/entrypoint.sh`

### 変更内容

`# Network` セクション（66行目付近）を以下のように変更する:

```bash
# Network
source /scripts/setup-network.sh

# Wait for explicitly specified interfaces to appear (containerlab creates them after container start)
if [ -n "$VM_NETWORKS" ]; then
    for iface in $(echo "$VM_NETWORKS" | tr ',' ' '); do
        timeout=30
        for i in $(seq 1 $timeout); do
            [ -e "/sys/class/net/$iface" ] && break
            [ "$i" -eq "$timeout" ] && echo "WARN: Interface $iface not found after ${timeout}s, proceeding without it" >&2
            sleep 1
        done
    done
fi

NET_ARGS=$(build_network_args 2>/dev/null || true)
if [ -n "$NET_ARGS" ]; then
    QEMU_ARGS="$QEMU_ARGS $NET_ARGS"
else
    QEMU_ARGS="$QEMU_ARGS -nic none"
fi
```

### 設計判断

| 項目 | 判断 |
|------|------|
| 待機対象 | `VM_NETWORKS` が明示指定されている場合のみ。自動検出（eth2+）の場合は即座に進む |
| タイムアウト | 30秒。containerlabのリンク作成は通常1〜2秒で完了する |
| タイムアウト時の挙動 | 警告ログを出力して続行（`-nic none`にフォールバック）。コンテナを異常終了させない |
| `DEBUG=true` 時 | 待機中のログ出力は不要（DEBUGログはQEMU引数等の最終状態確認が目的のため） |

### 影響範囲

- containerlabユーザー: ワークアラウンドのexecコマンドが不要になる
- docker run / docker-compose ユーザー: `VM_NETWORKS`未指定なら挙動変化なし。指定済みの場合もインターフェースが既に存在していればsleep 0回で即座に通過
- 起動時間: containerlabの場合のみ最大数秒の遅延（実際はリンク作成が速いため1〜2秒程度）
