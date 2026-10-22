# containerlab examples

| File | What it shows |
|------|---------------|
| `example.yml` | 2 nodes that boot their own disk (or an ISO), BMC (Redfish/noVNC, IPMI) published on the host, VM NICs on a data link |
| `mgmt-network.yml` | BMC management network separated from the VM data network, with an operator container and fixed UUIDs/MACs |

Both use the published image `ghcr.io/tjst-t/qemu-bmc:0.8` (the latest 0.8.x release). To try a local build, run `make docker-build` and change `image:` to `qemu-bmc:latest`.

## example.yml

```
  host :8441 (Redfish/noVNC) :6231/udp (IPMI) ── node1 BMC
  host :8442 (Redfish/noVNC) :6232/udp (IPMI) ── node2 BMC

  node1 VM NIC (eth1) ───────────── node2 VM NIC (eth1)      data network
```

node1 boots in BIOS mode, node2 in UEFI mode. Each node's `/vm` is `containerlab/vm/<node>/` and `/iso` is `containerlab/iso/` (both ignored by git). `VM_DISK_SIZE: "20G"` creates an empty `disk.qcow2` there at the first start, so no `qemu-img create` is needed; to boot a cloud image instead, copy it to `containerlab/vm/<node>/disk.qcow2` before deploying (an existing disk is used as is, never resized). `VM_DISK_SIZE` needs qemu-bmc 0.9 or later; with the 0.8 image, run `qemu-img create -f qcow2 containerlab/vm/<node>/disk.qcow2 20G` first.

```bash
mkdir -p containerlab/vm/node1 containerlab/vm/node2 containerlab/iso   # containerlab needs the bind directories

sudo containerlab deploy -t containerlab/example.yml

ipmitool -I lanplus -H 127.0.0.1 -p 6231 -U admin -P password chassis power on
curl -sk -u admin:password https://localhost:8441/redfish/v1/Systems/1
# console: https://localhost:8441/novnc/ (same credentials)

sudo containerlab destroy -t containerlab/example.yml --cleanup
```

To install from an ISO, put it in `containerlab/iso/`, set `VM_CDROM: "/iso/<file>.iso"` on the node, and boot it once from CD (`ipmitool ... chassis bootdev cdrom`, or Redfish `BootSourceOverrideTarget: Cd`).

## mgmt-network.yml

```
  operator (curl, ipmitool) 192.168.100.10 ─┐
  node1 BMC (eth1)          192.168.100.11 ─┼─ mgmt-switch   management network (Redfish :443, IPMI :623)
  node2 BMC (eth1)          192.168.100.12 ─┘

  node1 VM NIC (eth2) ───────────── node2 VM NIC (eth2)      data network
```

- **Management network** (`eth1` of each node): only qemu-bmc's Redfish and IPMI are used on it. The `operator` container sits on the same network and drives the nodes from there. Nothing is published on the host.
- **Data network** (`eth2`): passed through to the VM as its NIC (`VM_NETWORKS: eth2`). The container has no IP on it and the BMC is not reachable over it.
- **Fixed identity**: `SYSTEM_UUID` and `VM_MACS` per node. They are reported as Redfish `Systems/1` `UUID` / `EthernetInterfaces`, IPMI Get System GUID, and used as QEMU `-uuid` / NIC `mac=`.

qemu-bmc listens on all container addresses, so the BMC is also reachable on the containerlab management network (`eth0`, `172.20.20.0/24`) from the host. Use `192.168.100.0/24` from `operator` to exercise the separated management network.

### Deploy

Requires `/dev/net/tun` and `/dev/kvm` on the host (without KVM QEMU falls back to TCG). The `operator` node installs curl and ipmitool with `apk` at deploy time, so it needs Internet access.

```bash
sudo containerlab deploy -t containerlab/mgmt-network.yml
```

The nodes have no disk, so a powered-on VM just sits in the BIOS; that is enough for power control. To boot something, bind a directory with `disk.qcow2` to `/vm` (see `example.yml`).

### Verify

Run from the `operator` container:

```bash
op() { docker exec clab-qemu-bmc-mgmt-operator "$@"; }

for ip in 192.168.100.11 192.168.100.12; do
  # Redfish: power state, UUID, MAC
  op curl -sk -u admin:password https://$ip/redfish/v1/Systems/1
  op curl -sk -u admin:password https://$ip/redfish/v1/Systems/1/EthernetInterfaces/1

  # Redfish: power on / off
  op curl -sk -u admin:password -X POST -H 'Content-Type: application/json' \
    -d '{"ResetType":"On"}' https://$ip/redfish/v1/Systems/1/Actions/ComputerSystem.Reset
  op curl -sk -u admin:password -X POST -H 'Content-Type: application/json' \
    -d '{"ResetType":"ForceOff"}' https://$ip/redfish/v1/Systems/1/Actions/ComputerSystem.Reset

  # IPMI: power state / on / off, GUID
  op ipmitool -I lanplus -H $ip -U admin -P password chassis power status
  op ipmitool -I lanplus -H $ip -U admin -P password chassis power on
  op ipmitool -I lanplus -H $ip -U admin -P password chassis power off
  op ipmitool -I lanplus -H $ip -U admin -P password mc guid
done
```

Expected:

- `Systems/1` returns `"PowerState":"Off"` at first, `"On"` after a power on (Redfish or IPMI), and `chassis power status` agrees.
- The Reset action returns `204`.
- node1 reports UUID `6f1b7c52-0e1a-4d2b-9c11-000000000001` and MAC `52:54:00:00:01:01`; node2 reports `...000000000002` and `52:54:00:00:02:01`. `mc guid` shows the same UUID.

### Destroy

```bash
sudo containerlab destroy -t containerlab/mgmt-network.yml --cleanup
```

---

## 日本語

どちらの例も公開イメージ `ghcr.io/tjst-t/qemu-bmc:0.8`（0.8.x の最新）を使います。手元でビルドしたものを試すときは `make docker-build` のあと `image:` を `qemu-bmc:latest` に変えてください。

### example.yml

2 ノードがそれぞれ自分のディスク（または ISO）から起動する構成です。BMC（Redfish・noVNC・IPMI）はホストに公開します（node1：`8441`・`6231/udp`、node2：`8442`・`6232/udp`）。VM の NIC は 2 ノード間のデータリンク（`eth1`）につながります。node1 は BIOS、node2 は UEFI で起動します。各ノードの `/vm` は `containerlab/vm/<ノード名>/`、`/iso` は `containerlab/iso/` です（git の管理外）。`VM_DISK_SIZE: "20G"` により、初回の起動時に空の `disk.qcow2` が作られます（既にあるディスクはそのまま使い、大きさも変えません。qemu-bmc 0.9 以降。0.8 のイメージでは先に `qemu-img create` で作ってください）。手順は上の「example.yml」を見てください。

## mgmt-network.yml（日本語）

BMC 管理ネットワーク（各ノードの `eth1`、`192.168.100.0/24`）と、VM のデータネットワーク（`eth2`、VM の NIC にパススルー）を分けた構成です。管理ネットワーク上の `operator` コンテナ（curl・ipmitool 入り）から各ノードを Redfish と IPMI で操作します。ホストへのポート公開はしません。各ノードの UUID・MAC は `SYSTEM_UUID`・`VM_MACS` で固定しています。

qemu-bmc はコンテナの全アドレスで待ち受けるため、containerlab の管理ネットワーク（`eth0`、`172.20.20.0/24`）からも BMC に届きます。分けた管理ネットワークを確かめるときは `operator` から `192.168.100.0/24` を使ってください。

### 起動

ホストに `/dev/net/tun` と `/dev/kvm` が必要です（KVM が無ければ TCG で動きます）。`operator` は deploy 時に `apk` で curl と ipmitool を入れるのでインターネット接続が要ります。

```bash
sudo containerlab deploy -t containerlab/mgmt-network.yml
```

ディスクは付けていないので、電源を入れた VM は BIOS で止まります（電源操作の確認にはこれで足ります）。

### 確かめ方

上の「Verify」のコマンドを実行します。期待する結果:

- `Systems/1` の `PowerState` は最初 `Off`、Redfish か IPMI で電源を入れると `On`。`chassis power status` も同じ値を返す
- Reset アクションは `204` を返す
- node1 は UUID `6f1b7c52-0e1a-4d2b-9c11-000000000001`・MAC `52:54:00:00:01:01`、node2 は `...000000000002`・`52:54:00:00:02:01`。`mc guid` も同じ UUID

### 片付け

```bash
sudo containerlab destroy -t containerlab/mgmt-network.yml --cleanup
```
