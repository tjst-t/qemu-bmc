# Ironic end-to-end test

`tests/ironic/run.sh` deploys Debian onto a qemu-bmc node with a real
[Ironic](https://docs.openstack.org/ironic/) (the metal3 container image),
using both the Redfish and the IPMI drivers. It is meant to be run by hand
before a release; it is not part of the regular CI.

```bash
make ironic-test                                   # all flows, image built from this repository
make ironic-test IRONIC_FLOWS=redfish-vmedia       # one flow
make ironic-test IRONIC_FLOWS=redfish-bios         # BIOS settings only (creates the node itself)
make ironic-test IRONIC_ARGS="--image ghcr.io/tjst-t/qemu-bmc:0.9"
./tests/ironic/run.sh --keep ipmi-pxe              # leave everything running afterwards
```

## Flows

| Flow | Ironic node | What is checked |
|------|-------------|-----------------|
| `redfish-vmedia` | `--driver redfish --boot-interface redfish-virtual-media` | manage → inspect → provide → deploy → active, Debian boots from the disk, undeploy → available. The inspection inventory must match the node: CPU count, memory, MAC, disk size and the SMBIOS manufacturer / product / serial (also compared with Redfish Systems/1) |
| `redfish-bios` | same node (created and provided if the flow runs alone) | manage, `baremetal node bios setting list` (BootMode `Uefi`, ProcVirtualization `Enabled`, BootMode read-only from the attribute registry), manual cleaning (`--disable-ramdisk`) with `bios.apply_configuration` setting ProcVirtualization to `Disabled`: Ironic sets it pending, reboots the node and checks it was applied; then `bios.factory_reset` brings it back to `Enabled` (checked in Ironic and in Redfish `Systems/1/Bios`). The Ironic log must not contain "BIOS settings are not supported". provide → available |
| `redfish-https` | same node, `--boot-interface redfish-https` (UEFI HTTP boot) | deploy → active, Debian boots from the disk, undeploy → available |
| `ipmi-pxe` | `--driver ipmi --boot-interface ipxe --console-interface ipmitool-socat`, IPA booted by Ironic's dnsmasq (PXE → iPXE) | manage → provide → deploy → active, Debian boots from the disk, the serial console (IPMI SOL), undeploy → available; once with `VM_BOOT_MODE=uefi` and once with `VM_BOOT_MODE=bios` (`capabilities=boot_mode:<mode>`) |
| `all` | all of the above (default) | |

"Debian boots from the disk" is checked on the serial console: the script
logs in as root (the Debian nocloud image has no root password) and expects
`findmnt -n -o SOURCE /` to be `/dev/vda1`.

"The serial console" (ipmi-pxe): the console interface validates,
`baremetal node console enable` makes `console_enabled` True, and through
the socat URL of `baremetal node console show` (Ironic runs `ipmitool ...
sol activate` for the connection) the script logs in as root and reads the
output of a command; then `console disable`. The metal3 image has no socat,
so the Ironic container runs `qemu-bmc-ironic-e2e-ironic`: `IRONIC_IMAGE`
plus socat (`microdnf install socat`, built by the script), with
`OS_DEFAULT__ENABLED_CONSOLE_INTERFACES=no-console,ipmitool-socat` and
`OS_CONSOLE__PORT_RANGE=10000:10099` (the ports socat listens on).

## What it sets up

```
             BMC network qbmc-ie-bmc 172.31.44.0/24
  ┌──────────────┬────────────────────┬──────────────────────┐
  │ .10          │                    │ .21                   │
ironic (+ httpd, dnsmasq        CLI (python-     qemu-bmc node (Redfish, IPMI)
  in its network namespace)    ironicclient)     VM_NETWORKS=prov0 → VM NIC
  │ .10 prov0                                    │ prov0 (bridged to the VM)
  └──────────────────────────────────────────────┘
             provisioning network zz-qbmc-ie-prov 172.31.45.0/24
             (dnsmasq DHCP .100-.199, PXE/iPXE, httpd :6180)
```

- Ironic: `quay.io/metal3-io/ironic:latest` (override with `IRONIC_IMAGE`) as
  three containers, `/bin/runironic`, `/bin/runhttpd` and `/bin/rundnsmasq`,
  sharing a volume at `/shared`. No authentication, SQLite.
  `IRONIC_EXTERNAL_HTTP_URL` is the BMC network address, so qemu-bmc downloads
  the virtual media / HTTP boot ISO over the BMC network.
- The provisioning network interface is named `prov0` in the Ironic and node
  containers (`docker network connect --driver-opt
  com.docker.network.endpoint.ifname=prov0`, Docker 28+): the `ethN` order is
  not reliable once a second network is connected, and the node must bridge
  this interface, not its BMC one, into the VM (`VM_NETWORKS=prov0`). The
  network name still starts with `zz-` so it sorts after the BMC network.
- qemu-bmc node: `--device /dev/kvm --device /dev/net/tun --cap-add NET_ADMIN`,
  empty 10 GiB disk, 4096 MiB, 2 CPUs, fixed `VM_MACS`, `SYSTEM_UUID` and
  `VM_MANUFACTURER` / `VM_MODEL` / `VM_SERIAL_NUMBER`.
- Ironic node: `deploy_kernel` / `deploy_ramdisk` and the image are served by
  httpd; the image is passed with `image_os_hash_algo=sha256` and
  `image_os_hash_value` (IPA does not accept `image_checksum=sha256:...`).
- One logger in the node container keeps the serial console
  (`127.0.0.1:9002`) open, writes everything to `/tmp/serial.log` and sends
  what is written to `/tmp/serial.in`; it stays connected while Ironic uses
  the console over IPMI SOL.

Everything is named `qbmc-ie-*` (override with `IRONIC_E2E_PREFIX`; the
subnets with `IRONIC_E2E_BMC_NET` / `IRONIC_E2E_PROV_NET`, e.g. `172.31.44`)
so the test can run next to the other container tests. At the end, or on a
failure, all containers, the networks and the volume are removed (`--keep`
leaves them). On a failure the Ironic node, the Ironic / httpd / dnsmasq /
qemu-bmc logs and the end of the serial console are printed first.

## Downloads

Kept in `IRONIC_E2E_CACHE` (default `~/.cache/qemu-bmc-ironic-e2e`) and not
downloaded again:

- IPA: `https://tarballs.opendev.org/openstack/ironic-python-agent/dib/files/ipa-centos9-master.{kernel,initramfs}` (~400 MB)
- Debian 12 nocloud: `https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-nocloud-amd64.qcow2` (~420 MB)

Delete the files to fetch newer ones.

## Duration

With KVM, downloads cached:

| Part | Time |
|------|------|
| Build images, start Ironic | ~0.5 min (first run: a few minutes more for the builds and pulls, plus the ~800 MB downloads) |
| `redfish-vmedia` | ~6 min |
| `redfish-bios` | ~2 min after `redfish-vmedia` (~3.5 min alone: it creates and cleans the node first) |
| `redfish-https` | ~2.5 min |
| `ipmi-pxe` (UEFI + BIOS) | ~11.5 min |
| `all` | ~23 min |

Without `/dev/kvm` QEMU falls back to TCG; the script multiplies its timeouts
by 6 and a run takes hours.

## GitHub Actions

`.github/workflows/ironic-e2e.yml` runs it on `workflow_dispatch` only
(Actions → Ironic e2e → Run workflow), with the flows and an optional image
as inputs and a 6 hour timeout.

## Requirements

Docker 28 or later, curl, sha256sum, `/dev/net/tun`, and `/dev/kvm` (optional but strongly
recommended). About 2 GB of disk for the cache and the node disk.
