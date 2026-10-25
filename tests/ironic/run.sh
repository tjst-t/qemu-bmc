#!/bin/bash
# run.sh - End-to-end test: a real Ironic (metal3 container image) deploys
# Debian onto a qemu-bmc node with the Redfish and IPMI drivers.
#
# Usage: tests/ironic/run.sh [options] [flow...]
#   flows: redfish-vmedia  Redfish + VirtualMedia (also inspection checks)
#          redfish-https   Redfish + UEFI HTTP boot
#          ipmi-pxe        IPMI + iPXE, UEFI and BIOS
#          all             all of the above (default)
#   options:
#          --image IMAGE   qemu-bmc image to test (default: build the repository)
#          --keep          leave the containers, networks and volume in place
#          -h, --help
#
# Requirements on the host: docker, curl, /dev/net/tun (and /dev/kvm, else
# QEMU falls back to TCG and the run is much slower). Downloads (IPA, Debian
# image) are kept in IRONIC_E2E_CACHE and not fetched again.
# See tests/ironic/README.md.

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_DIR=$(cd "$SCRIPT_DIR/../.." && pwd)

# --- Configuration (names and subnets chosen not to collide with other tests) ---
PREFIX="${IRONIC_E2E_PREFIX:-qbmc-ie}"
CACHE="${IRONIC_E2E_CACHE:-$HOME/.cache/qemu-bmc-ironic-e2e}"
IRONIC_IMAGE="${IRONIC_IMAGE:-quay.io/metal3-io/ironic:latest}"
CLI_IMAGE="${IRONIC_E2E_CLI_IMAGE:-qemu-bmc-ironic-e2e-cli}"
BUILD_IMAGE="qemu-bmc:ironic-e2e"
# First three octets of the two /24 networks
BMC_NET="${IRONIC_E2E_BMC_NET:-172.31.44}"
PROV_NET="${IRONIC_E2E_PROV_NET:-172.31.45}"

IPA_URL="https://tarballs.opendev.org/openstack/ironic-python-agent/dib/files/ipa-centos9-master"
DEBIAN_URL="https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-nocloud-amd64.qcow2"
DEBIAN_FILE="debian-12-nocloud-amd64.qcow2"

# The provisioning network interface is named PROV_IF in the Ironic and node
# containers (driver-opt com.docker.network.endpoint.ifname): docker's ethN
# order is not reliable with docker network connect, and the node must not
# bridge its BMC interface into the VM. The zz- prefix keeps it after the BMC
# network (eth0) where docker orders the interfaces by network name.
NET_BMC="$PREFIX-bmc"
NET_PROV="zz-$PREFIX-prov"
PROV_IF=prov0
VOLUME="$PREFIX-shared"
C_IRONIC="$PREFIX-ironic"
C_HTTPD="$PREFIX-httpd"
C_DNSMASQ="$PREFIX-dnsmasq"
C_CLI="$PREFIX-cli"
C_NODE="$PREFIX-node"

IRONIC_BMC_IP="$BMC_NET.10"
IRONIC_PROV_IP="$PROV_NET.10"
NODE_BMC_IP="$BMC_NET.21"
NODE_PROV_IP="$PROV_NET.21"
HTTP_PORT=6180
IRONIC_HTTP="http://$IRONIC_PROV_IP:$HTTP_PORT"

# The qemu-bmc node
BMC_USER=admin
BMC_PASS=password
NODE_MAC="52:54:00:44:00:01"
NODE_UUID="4e2e4400-0000-4000-8000-000000000044"
NODE_MEMORY=4096
NODE_CPUS=2
NODE_DISK_GIB=10
NODE_MANUFACTURER="qemu-bmc e2e"
NODE_MODEL="ironic-e2e"
NODE_SERIAL="QBMC-E2E-0044"
IRONIC_NODE="e2e-node"

KEEP=false
IMAGE=""
FLOWS=()

# --- Output ---
T0=$(date +%s)
log() { printf '[%5ss] %s\n' "$(( $(date +%s) - T0 ))" "$*"; }
fail() { log "FAIL: $*" >&2; exit 1; }

usage() { sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
    case "$1" in
        --image) IMAGE="$2"; shift 2 ;;
        --keep) KEEP=true; shift ;;
        -h|--help) usage; exit 0 ;;
        all) FLOWS+=(redfish-vmedia redfish-https ipmi-pxe); shift ;;
        redfish-vmedia|redfish-https|ipmi-pxe) FLOWS+=("$1"); shift ;;
        *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done
[ ${#FLOWS[@]} -eq 0 ] && FLOWS=(redfish-vmedia redfish-https ipmi-pxe)

# Without KVM QEMU runs under TCG: allow much longer for every wait
if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
    KVM=true; SCALE=1
else
    KVM=false; SCALE=6
fi

# --- Helpers ---

# baremetal CLI (python-ironicclient) in the CLI container
bm() { docker exec "$C_CLI" baremetal "$@"; }

# Redfish GET from the CLI container (BMC network)
redfish_get() {
    docker exec "$C_CLI" curl -sfk -u "$BMC_USER:$BMC_PASS" "https://$NODE_BMC_IP$1"
}

# Wait until the node reaches provision state $1 (timeout $2 s, scaled).
# Fails on a *failed / error state and prints last_error.
wait_state() {
    local target="$1" timeout=$(( $2 * SCALE )) start state prev=""
    start=$(date +%s)
    while :; do
        state=$(bm node show "$IRONIC_NODE" -f value -c provision_state)
        if [ "$state" != "$prev" ]; then
            log "  provision_state: $state"
            prev="$state"
        fi
        [ "$state" = "$target" ] && return 0
        case "$state" in
            *failed|error)
                log "  last_error: $(bm node show "$IRONIC_NODE" -f value -c last_error)"
                fail "node went to '$state' while waiting for '$target'" ;;
        esac
        if [ $(( $(date +%s) - start )) -ge "$timeout" ]; then
            log "  last_error: $(bm node show "$IRONIC_NODE" -f value -c last_error)"
            fail "node still '$state' after ${timeout}s waiting for '$target'"
        fi
        sleep 5
    done
}

# --- Serial console ---
# qemu-bmc serves the serial console on 127.0.0.1:9002 in the node container
# (SERIAL_ADDR; the connection survives QEMU restarts). One logger in the
# container keeps it open: it appends everything to /tmp/serial.log, sends
# what is written to the /tmp/serial.in FIFO and reconnects if dropped.
start_serial_logger() {
    docker exec -d "$C_NODE" bash -c '
        rm -f /tmp/serial.in; mkfifo /tmp/serial.in; exec 4<>/tmp/serial.in
        while :; do
            (
                exec 3<>/dev/tcp/127.0.0.1/9002 || exit 1
                cat <&4 >&3 & w=$!
                cat <&3 >>/tmp/serial.log
                kill $w
            ) 2>/dev/null
            sleep 1
        done'
}

serial_size() {
    docker exec "$C_NODE" bash -c 'stat -c %s /tmp/serial.log 2>/dev/null || echo 0'
}

# Serial output after byte offset $1, without CR and terminal escapes
serial_since() {
    docker exec "$C_NODE" bash -c 'tail -c +$(( $1 + 1 )) /tmp/serial.log 2>/dev/null' _ "$1" |
        tr -d '\r' | sed 's/\x1b\[[0-9;?]*[a-zA-Z]//g'
}

serial_send() {
    docker exec "$C_NODE" bash -c 'printf "%s\r" "$1" >/tmp/serial.in' _ "$1"
}

# Wait for extended regex $2 in the serial output after offset $1 (timeout $3 s, scaled)
serial_wait() {
    local off="$1" re="$2" timeout=$(( $3 * SCALE )) start
    start=$(date +%s)
    until serial_since "$off" | grep -qE -- "$re"; do
        [ $(( $(date +%s) - start )) -ge "$timeout" ] && return 1
        sleep 2
    done
}

# Log in as root on the serial console of the deployed Debian and check that
# its root file system is /dev/vda1 (the disk Ironic wrote), not the IPA ramdisk
check_disk_boot() {
    local off start out
    log "Serial: waiting for the Debian login prompt"
    off=$(serial_size)
    start=$(date +%s)
    until serial_since "$off" | grep -qE 'login: *$'; do
        [ $(( $(date +%s) - start )) -ge $(( 600 * SCALE )) ] && fail "no login prompt on the serial console"
        serial_send ""
        sleep 5
    done
    off=$(serial_size)
    serial_send "root"
    serial_wait "$off" 'root@[^ ]*:~# *$' 60 || fail "root login on the serial console failed"
    off=$(serial_size)
    serial_send 'echo "ROOTFS=$(findmnt -n -o SOURCE /) OS=$(. /etc/os-release; echo $ID-$VERSION_ID)"'
    serial_wait "$off" 'ROOTFS=/[^ ]* OS=' 30 || fail "no answer from findmnt on the serial console"
    out=$(serial_since "$off" | grep -oE 'ROOTFS=/[^ ]* OS=[^ ]*' | head -n 1)
    serial_send "exit"
    log "Serial: $out"
    [ "$out" = "ROOTFS=/dev/vda1 OS=debian-12" ] || fail "expected ROOTFS=/dev/vda1 OS=debian-12, got '$out'"
}

# --- Setup ---

fetch() {
    local url="$1" file="$CACHE/$2"
    if [ -s "$file" ]; then
        log "Cached: $2"
        return
    fi
    log "Downloading $url"
    curl -fL --retry 3 -o "$file.part" "$url"
    mv "$file.part" "$file"
}

prepare_cache() {
    mkdir -p "$CACHE"
    fetch "$IPA_URL.kernel" ironic-python-agent.kernel
    fetch "$IPA_URL.initramfs" ironic-python-agent.initramfs
    fetch "$DEBIAN_URL" "$DEBIAN_FILE"
    # Served by httpd (user apache) from a bind mount
    chmod a+rx "$CACHE"
    chmod a+r "$CACHE"/*
    if [ ! -s "$CACHE/$DEBIAN_FILE.sha256" ] || [ "$CACHE/$DEBIAN_FILE" -nt "$CACHE/$DEBIAN_FILE.sha256" ]; then
        sha256sum "$CACHE/$DEBIAN_FILE" | cut -d' ' -f1 >"$CACHE/$DEBIAN_FILE.sha256"
    fi
    DEBIAN_SHA256=$(cat "$CACHE/$DEBIAN_FILE.sha256")
}

prepare_images() {
    if [ -z "$IMAGE" ]; then
        log "Building $BUILD_IMAGE from $REPO_DIR"
        docker build -q -t "$BUILD_IMAGE" -f "$REPO_DIR/docker/Dockerfile" "$REPO_DIR" >/dev/null
        IMAGE="$BUILD_IMAGE"
    elif ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
        log "Pulling $IMAGE"
        docker pull -q "$IMAGE" >/dev/null
    fi
    if ! docker image inspect "$IRONIC_IMAGE" >/dev/null 2>&1; then
        log "Pulling $IRONIC_IMAGE"
        docker pull -q "$IRONIC_IMAGE" >/dev/null
    fi
    log "Building $CLI_IMAGE (python-ironicclient)"
    docker build -q -t "$CLI_IMAGE" - >/dev/null <<'EOF'
FROM python:3.12-slim
RUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/* && \
    pip install --no-cache-dir python-ironicclient
EOF
}

start_ironic() {
    log "Starting networks and Ironic"
    docker network create --subnet "$BMC_NET.0/24" "$NET_BMC" >/dev/null
    docker network create --subnet "$PROV_NET.0/24" "$NET_PROV" >/dev/null
    docker volume create "$VOLUME" >/dev/null

    local env=(
        -e "IRONIC_IP=$IRONIC_PROV_IP"
        -e "PROVISIONING_INTERFACE=$PROV_IF"
        -e "HTTP_PORT=$HTTP_PORT"
        -e "IRONIC_EXTERNAL_HTTP_URL=http://$IRONIC_BMC_IP:$HTTP_PORT"
        -e "DHCP_RANGE=$PROV_NET.100,$PROV_NET.199"
        -e IRONIC_KERNEL_PARAMS=console=ttyS0
    )
    local vols=(-v "$VOLUME:/shared" -v "$CACHE:/shared/html/images:ro")

    docker create --name "$C_IRONIC" --network "$NET_BMC" --ip "$IRONIC_BMC_IP" \
        "${env[@]}" "${vols[@]}" "$IRONIC_IMAGE" /bin/runironic >/dev/null
    docker network connect --ip "$IRONIC_PROV_IP" \
        --driver-opt "com.docker.network.endpoint.ifname=$PROV_IF" "$NET_PROV" "$C_IRONIC"
    docker start "$C_IRONIC" >/dev/null
    docker run -d --name "$C_HTTPD" --network "container:$C_IRONIC" \
        "${env[@]}" "${vols[@]}" "$IRONIC_IMAGE" /bin/runhttpd >/dev/null
    docker run -d --name "$C_DNSMASQ" --network "container:$C_IRONIC" \
        --cap-add NET_ADMIN --cap-add NET_RAW \
        "${env[@]}" "${vols[@]}" "$IRONIC_IMAGE" /bin/rundnsmasq >/dev/null

    docker run -d --name "$C_CLI" --network "$NET_BMC" \
        -e "OS_ENDPOINT=http://$IRONIC_BMC_IP:6385" -e OS_AUTH_TYPE=none \
        -e OS_BAREMETAL_API_VERSION=latest \
        "$CLI_IMAGE" sleep infinity >/dev/null

    docker exec "$C_IRONIC" ip -o -4 addr show "$PROV_IF" | grep -qF "$IRONIC_PROV_IP/" ||
        fail "$PROV_IF of $C_IRONIC is not on the provisioning network"

    local start
    start=$(date +%s)
    until bm driver list >/dev/null 2>&1; do
        [ $(( $(date +%s) - start )) -ge 300 ] && fail "Ironic API did not come up"
        sleep 3
    done
    docker exec "$C_CLI" curl -sfI "http://$IRONIC_BMC_IP:$HTTP_PORT/images/ironic-python-agent.kernel" >/dev/null ||
        fail "httpd does not serve the IPA kernel"
    log "Ironic is up ($(docker exec "$C_IRONIC" ironic-conductor --version 2>/dev/null || echo '?'))"
}

# (Re)create the qemu-bmc node container with boot mode $1 and an empty disk
start_node() {
    local mode="$1"
    docker rm -f -v "$C_NODE" >/dev/null 2>&1 || true
    log "Starting qemu-bmc node ($IMAGE, VM_BOOT_MODE=$mode)"
    local kvm=()
    $KVM && kvm=(--device /dev/kvm)
    docker create --name "$C_NODE" --hostname "$C_NODE" \
        --network "$NET_BMC" --ip "$NODE_BMC_IP" \
        "${kvm[@]}" --device /dev/net/tun --cap-add NET_ADMIN \
        -e "VM_BOOT_MODE=$mode" \
        -e "VM_DISK_SIZE=${NODE_DISK_GIB}G" \
        -e "VM_MEMORY=$NODE_MEMORY" -e "VM_CPUS=$NODE_CPUS" \
        -e "VM_MACS=$NODE_MAC" -e "VM_NETWORKS=$PROV_IF" \
        -e "SYSTEM_UUID=$NODE_UUID" \
        -e "VM_MANUFACTURER=$NODE_MANUFACTURER" -e "VM_MODEL=$NODE_MODEL" \
        -e "VM_SERIAL_NUMBER=$NODE_SERIAL" \
        -e "IPMI_USER=$BMC_USER" -e "IPMI_PASS=$BMC_PASS" \
        "$IMAGE" >/dev/null
    docker network connect --ip "$NODE_PROV_IP" \
        --driver-opt "com.docker.network.endpoint.ifname=$PROV_IF" "$NET_PROV" "$C_NODE"
    docker start "$C_NODE" >/dev/null

    local start
    start=$(date +%s)
    until redfish_get /redfish/v1/Systems/1 >/dev/null 2>&1; do
        [ $(( $(date +%s) - start )) -ge 60 ] && fail "qemu-bmc Redfish did not come up"
        sleep 2
    done
    docker exec "$C_NODE" ip -o -4 addr | grep -qF "$NODE_BMC_IP/" ||
        fail "$C_NODE has no address on the BMC network"
    start_serial_logger
}

# Create the Ironic node with driver arguments "$@" and its port
create_node() {
    local uuid
    bm node delete "$IRONIC_NODE" >/dev/null 2>&1 || true
    uuid=$(bm node create --name "$IRONIC_NODE" "$@" \
        --driver-info "deploy_kernel=$IRONIC_HTTP/images/ironic-python-agent.kernel" \
        --driver-info "deploy_ramdisk=$IRONIC_HTTP/images/ironic-python-agent.initramfs" \
        -f value -c uuid)
    # port create --node needs the UUID, not the name
    bm port create --node "$uuid" "$NODE_MAC" >/dev/null
    log "Created Ironic node $IRONIC_NODE ($uuid): $*"
}

set_instance_info() {
    bm node set "$IRONIC_NODE" \
        --instance-info "image_source=$IRONIC_HTTP/images/$DEBIAN_FILE" \
        --instance-info image_os_hash_algo=sha256 \
        --instance-info "image_os_hash_value=$DEBIAN_SHA256"
}

manage_and_provide() {
    log "manage"
    bm node manage "$IRONIC_NODE"
    wait_state manageable 300
    if [ "${1:-}" = inspect ]; then
        log "inspect"
        bm node inspect "$IRONIC_NODE"
        wait_state manageable 1200
        check_inventory
    fi
    log "provide (cleaning)"
    bm node provide "$IRONIC_NODE"
    wait_state available 1800
}

deploy_and_check() {
    log "deploy ($(bm node show "$IRONIC_NODE" -f value -c driver)," \
        "boot interface $(bm node show "$IRONIC_NODE" -f value -c boot_interface))"
    set_instance_info
    bm node deploy "$IRONIC_NODE"
    wait_state active 2400
    check_disk_boot
}

undeploy() {
    log "undeploy (cleaning)"
    bm node undeploy "$IRONIC_NODE"
    wait_state available 1800
}

# Inspection data must match what the node was started with
check_inventory() {
    local got want errors=0
    got=$(docker exec "$C_CLI" sh -c 'baremetal node inventory save "$1" | python3 -c "
import json, sys
d = json.load(sys.stdin)
inv = d.get(\"inventory\", d)
print(\"cpus=%s\" % inv[\"cpu\"][\"count\"])
print(\"memory_mb=%s\" % inv[\"memory\"][\"physical_mb\"])
print(\"macs=%s\" % \",\".join(sorted(i[\"mac_address\"] for i in inv[\"interfaces\"])))
print(\"disks=%s\" % \",\".join(\"%s:%s\" % (x[\"name\"], x[\"size\"]) for x in inv[\"disks\"]))
sv = inv[\"system_vendor\"]
print(\"manufacturer=%s\" % sv.get(\"manufacturer\"))
print(\"product=%s\" % sv.get(\"product_name\"))
print(\"serial=%s\" % sv.get(\"serial_number\"))
"' _ "$IRONIC_NODE")
    want="cpus=$NODE_CPUS
memory_mb=$NODE_MEMORY
macs=$NODE_MAC
disks=/dev/vda:$(( NODE_DISK_GIB * 1024 * 1024 * 1024 ))
manufacturer=$NODE_MANUFACTURER
product=$NODE_MODEL
serial=$NODE_SERIAL"
    log "Inspection inventory:"
    printf '%s\n' "$got" | sed 's/^/    /'
    while IFS= read -r line; do
        printf '%s\n' "$got" | grep -qxF -- "$line" || { log "  mismatch: want $line"; errors=$(( errors + 1 )); }
    done <<<"$want"
    # Redfish must report the same identity as SMBIOS
    local rf
    rf=$(redfish_get /redfish/v1/Systems/1)
    for want in "\"Manufacturer\": *\"$NODE_MANUFACTURER\"" "\"Model\": *\"$NODE_MODEL\"" "\"SerialNumber\": *\"$NODE_SERIAL\""; do
        printf '%s' "$rf" | grep -qE -- "$want" || { log "  Redfish Systems/1 mismatch: want $want"; errors=$(( errors + 1 )); }
    done
    [ "$errors" -eq 0 ] || fail "inspection data does not match the node ($errors mismatches)"
    log "Inspection data matches the node"
}

# --- Flows ---

FLOW_RESULTS=()
CURRENT_NODE_MODE=""

ensure_node_container() {
    if [ "$CURRENT_NODE_MODE" != "$1" ]; then
        start_node "$1"
        CURRENT_NODE_MODE="$1"
    fi
}

redfish_driver_args() {
    echo --driver redfish \
        --driver-info "redfish_address=https://$NODE_BMC_IP" \
        --driver-info redfish_system_id=/redfish/v1/Systems/1 \
        --driver-info "redfish_username=$BMC_USER" \
        --driver-info "redfish_password=$BMC_PASS" \
        --driver-info redfish_verify_ca=False
}

# The node must be available with the Redfish driver; created (and inspected)
# if it is not
ensure_redfish_node() {
    ensure_node_container uefi
    if [ "$(bm node show "$IRONIC_NODE" -f value -c driver 2>/dev/null)" != redfish ] ||
       [ "$(bm node show "$IRONIC_NODE" -f value -c provision_state)" != available ]; then
        # shellcheck disable=SC2046
        create_node $(redfish_driver_args) --boot-interface "$1"
        manage_and_provide "${2:-}"
    fi
}

flow_redfish_vmedia() {
    ensure_node_container uefi
    # shellcheck disable=SC2046
    create_node $(redfish_driver_args) --boot-interface redfish-virtual-media
    manage_and_provide inspect
    deploy_and_check
    undeploy
}

flow_redfish_https() {
    ensure_redfish_node redfish-https
    bm node set "$IRONIC_NODE" --boot-interface redfish-https
    deploy_and_check
    undeploy
}

flow_ipmi_pxe() {
    local mode
    for mode in uefi bios; do
        log "--- ipmi-pxe, $mode"
        ensure_node_container "$mode"
        create_node --driver ipmi --boot-interface ipxe \
            --driver-info "ipmi_address=$NODE_BMC_IP" \
            --driver-info "ipmi_username=$BMC_USER" \
            --driver-info "ipmi_password=$BMC_PASS" \
            --property "capabilities=boot_mode:$mode"
        manage_and_provide
        deploy_and_check
        undeploy
    done
}

# --- Failure logs and cleanup ---

dump_logs() {
    echo "===== Ironic node =====" >&2
    bm node show "$IRONIC_NODE" -f yaml -c provision_state -c power_state -c last_error \
        -c driver -c boot_interface -c driver_internal_info >&2 2>/dev/null || true
    echo "===== ironic (last 150 lines) =====" >&2
    docker logs --tail 150 "$C_IRONIC" 2>&1 | grep -v '^+' >&2 || true
    echo "===== httpd (last 30 lines) =====" >&2
    docker logs --tail 30 "$C_HTTPD" >&2 2>&1 || true
    echo "===== dnsmasq (last 40 lines) =====" >&2
    docker logs --tail 40 "$C_DNSMASQ" >&2 2>&1 || true
    echo "===== qemu-bmc node (last 80 lines) =====" >&2
    docker logs --tail 80 "$C_NODE" >&2 2>&1 || true
    echo "===== node serial console (last 80 lines) =====" >&2
    serial_since 0 2>/dev/null | tail -n 80 >&2 || true
    echo "=====" >&2
}

cleanup() {
    docker rm -f -v "$C_NODE" "$C_CLI" "$C_DNSMASQ" "$C_HTTPD" "$C_IRONIC" >/dev/null 2>&1 || true
    docker volume rm "$VOLUME" >/dev/null 2>&1 || true
    docker network rm "$NET_BMC" "$NET_PROV" >/dev/null 2>&1 || true
}

on_exit() {
    local rc=$?
    trap - EXIT
    if [ "$rc" -ne 0 ]; then
        dump_logs
    fi
    if $KEEP; then
        log "--keep: containers ($PREFIX-*), networks and volume left in place"
    else
        log "Cleaning up"
        cleanup
    fi
    echo
    echo "===== Ironic e2e summary ====="
    local r
    for r in "${FLOW_RESULTS[@]}"; do echo "  $r"; done
    if [ "$rc" -eq 0 ]; then
        echo "  PASS (total $(( $(date +%s) - T0 ))s)"
    else
        echo "  FAIL (total $(( $(date +%s) - T0 ))s)"
    fi
    exit "$rc"
}

# --- Main ---

for cmd in docker curl sha256sum; do
    command -v "$cmd" >/dev/null || { echo "$cmd is required" >&2; exit 2; }
done
[ -e /dev/net/tun ] || { echo "/dev/net/tun is required" >&2; exit 2; }
$KVM || log "WARN: /dev/kvm is not usable, QEMU uses TCG (slow); timeouts x$SCALE"

log "Flows: ${FLOWS[*]}"
cleanup
trap on_exit EXIT
trap "exit 130" INT TERM
prepare_cache
prepare_images
start_ironic

for flow in "${FLOWS[@]}"; do
    log "===== $flow ====="
    fs=$(date +%s)
    FLOW_RESULTS+=("$flow: FAIL")
    "flow_${flow//-/_}"
    FLOW_RESULTS[-1]="$flow: PASS ($(( $(date +%s) - fs ))s)"
done
