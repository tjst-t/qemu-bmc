#!/bin/bash
# setup-network.sh - TAP/bridge network setup for QEMU VM passthrough
#
# Creates TAP devices and bridges for each specified network interface,
# connecting host interfaces to QEMU via virtio-net-pci.
#
# Usage: Source this file and call build_network_args
#   source /scripts/setup-network.sh
#   ARGS=$(build_network_args)

# Detect network interfaces for VM passthrough
# If VM_NETWORKS is set, use those (comma-separated)
# Otherwise, auto-detect eth2+ (skip eth0=management, eth1=IPMI)
detect_interfaces() {
    if [ -n "$VM_NETWORKS" ]; then
        echo "$VM_NETWORKS" | tr ',' ' '
        return
    fi

    local ifaces=""
    for iface in /sys/class/net/eth*; do
        iface=$(basename "$iface")
        case "$iface" in
            eth0|eth1) continue ;;  # Skip management and IPMI
            *) ifaces="$ifaces $iface" ;;
        esac
    done
    echo "$ifaces"
}

# Make sure /dev/net/tun is usable (TAP devices and QEMU -netdev tap need it).
# Creates the node if it is missing (Docker's default device cgroup allows
# opening it), otherwise prints an error naming the fix and returns 1.
ensure_tun_device() {
    local tun="${TUN_DEVICE:-/dev/net/tun}"
    if [ ! -c "$tun" ]; then
        mkdir -p "$(dirname "$tun")" 2>/dev/null
        mknod -m 0666 "$tun" c 10 200 2>/dev/null
    fi
    if [ -c "$tun" ] && { : <>"$tun"; } 2>/dev/null; then
        return 0
    fi
    cat >&2 <<MSG
ERROR: $tun is not available in the container, so the VM NICs for
       '$(detect_interfaces | xargs)' cannot be created (QEMU -netdev tap would fail).
       Pass the TUN device to the container:
         docker run:     --device /dev/net/tun
         docker compose: devices: ["/dev/net/tun:/dev/net/tun"]
         containerlab:   devices: ["/dev/net/tun"]
MSG
    return 1
}

# Check VM_NET_MTU (empty = leave MTUs alone). Prints the reason and
# returns 1 if it is not a number in the range Linux and QEMU accept.
validate_net_mtu() {
    [ -z "$VM_NET_MTU" ] && return 0
    case "$VM_NET_MTU" in
        *[!0-9]*)
            echo "ERROR: VM_NET_MTU='$VM_NET_MTU' is not a number (e.g. 9000)" >&2
            return 1 ;;
    esac
    if [ "${#VM_NET_MTU}" -gt 5 ] || [ "$VM_NET_MTU" -lt 68 ] || [ "$VM_NET_MTU" -gt 65535 ]; then
        echo "ERROR: VM_NET_MTU=$VM_NET_MTU is out of range (68-65535)" >&2
        return 1
    fi
}

# Raise the MTU of a container interface to VM_NET_MTU if it is smaller
# (a larger MTU, e.g. containerlab's default 9500, is kept)
raise_iface_mtu() {
    local iface="$1"
    local cur
    cur=$(cat "/sys/class/net/$iface/mtu" 2>/dev/null) || return 1
    if [ "$cur" -lt "$VM_NET_MTU" ]; then
        ip link set "$iface" mtu "$VM_NET_MTU" || return 1
    fi
}

# Create TAP device, bridge, and connect host interface
# With VM_NET_MTU the host interface, TAP and bridge get (at least) that MTU
# Args: $1=interface name, $2=tap index
setup_bridge() {
    local iface="$1"
    local idx="$2"
    local tap="tap${idx}"
    local bridge="br${idx}"

    if [ -n "$VM_NET_MTU" ]; then
        raise_iface_mtu "$iface" || return 1
    fi

    # Create TAP device
    ip tuntap add "$tap" mode tap || return 1
    if [ -n "$VM_NET_MTU" ]; then
        ip link set "$tap" mtu "$VM_NET_MTU" || return 1
    fi
    ip link set "$tap" up || return 1

    # Create bridge
    ip link add "$bridge" type bridge || return 1
    ip link set "$bridge" up || return 1

    # Connect host interface and TAP to bridge
    ip link set "$iface" master "$bridge" || return 1
    ip link set "$tap" master "$bridge" || return 1

    # Flush IP from host interface (L2 bridge only)
    ip addr flush dev "$iface" || return 1

    # Bring up host interface
    ip link set "$iface" up || return 1

    # A bridge takes the smallest MTU of its ports; set it explicitly too
    if [ -n "$VM_NET_MTU" ]; then
        ip link set "$bridge" mtu "$VM_NET_MTU" || return 1
    fi
}

# Build QEMU network arguments for all detected interfaces
# Outputs -netdev/-device argument pairs to stdout
# NIC MAC addresses are not set here: qemu-bmc assigns them (VM_MACS, or
# derived from the system UUID so they differ between nodes)
# With VM_NET_MTU, virtio-net NICs get host_mtu= so the guest uses that MTU
build_network_args() {
    validate_net_mtu || return 1

    local interfaces
    interfaces=$(detect_interfaces)

    if [ -z "$interfaces" ]; then
        return
    fi

    ensure_tun_device || return 1

    local idx=0
    local args=""
    for iface in $interfaces; do
        local tap="tap${idx}"

        # Setup bridge (requires NET_ADMIN capability)
        if ! setup_bridge "$iface" "$idx"; then
            echo "ERROR: failed to set up TAP/bridge for interface $iface" >&2
            return 1
        fi

        args="$args -netdev tap,id=net${idx},ifname=${tap},script=no,downscript=no"
        args="$args -device virtio-net-pci,netdev=net${idx}"
        if [ -n "$VM_NET_MTU" ]; then
            args="$args,host_mtu=${VM_NET_MTU}"
        fi

        idx=$((idx + 1))
    done

    echo "$args"
}
