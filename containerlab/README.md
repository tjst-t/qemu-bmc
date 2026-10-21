# containerlab examples

| File | What it shows |
|------|---------------|
| `example.yml` | 2 nodes, BMC ports published on the host |
| `mgmt-network.yml` | BMC management network separated from the VM data network, with an operator container and fixed UUIDs/MACs |

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
# Build the image used by the topology (qemu-bmc:latest)
make docker-build

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

## mgmt-network.yml（日本語）

BMC 管理ネットワーク（各ノードの `eth1`、`192.168.100.0/24`）と、VM のデータネットワーク（`eth2`、VM の NIC にパススルー）を分けた構成です。管理ネットワーク上の `operator` コンテナ（curl・ipmitool 入り）から各ノードを Redfish と IPMI で操作します。ホストへのポート公開はしません。各ノードの UUID・MAC は `SYSTEM_UUID`・`VM_MACS` で固定しています。

qemu-bmc はコンテナの全アドレスで待ち受けるため、containerlab の管理ネットワーク（`eth0`、`172.20.20.0/24`）からも BMC に届きます。分けた管理ネットワークを確かめるときは `operator` から `192.168.100.0/24` を使ってください。

### 起動

ホストに `/dev/net/tun` と `/dev/kvm` が必要です（KVM が無ければ TCG で動きます）。`operator` は deploy 時に `apk` で curl と ipmitool を入れるのでインターネット接続が要ります。

```bash
make docker-build   # トポロジーが使う qemu-bmc:latest を作る
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
