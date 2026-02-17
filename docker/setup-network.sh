#!/bin/bash
# setup-network.sh - TAP/bridge network setup for QEMU VM passthrough
#
# Creates TAP devices and bridges for each specified network interface,
# connecting host interfaces to QEMU via virtio-net-pci.
#
# Usage: Source this file and call build_network_args
#   source /scripts/setup-network.sh
#   ARGS=$(build_network_args)

# Generate a deterministic MAC address from interface name
# Prefix: 52:54:00 (QEMU OUI), remaining 3 bytes from md5(ifname)
generate_mac() {
    local ifname="$1"
    local hash
    hash=$(echo -n "$ifname" | md5sum | cut -c1-6)
    echo "52:54:00:${hash:0:2}:${hash:2:2}:${hash:4:2}"
}

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

# Create TAP device, bridge, and connect host interface
# Args: $1=interface name, $2=tap index
setup_bridge() {
    local iface="$1"
    local idx="$2"
    local tap="tap${idx}"
    local bridge="br${idx}"

    # Create TAP device
    ip tuntap add "$tap" mode tap
    ip link set "$tap" up

    # Create bridge
    ip link add "$bridge" type bridge
    ip link set "$bridge" up

    # Connect host interface and TAP to bridge
    ip link set "$iface" master "$bridge"
    ip link set "$tap" master "$bridge"

    # Flush IP from host interface (L2 bridge only)
    ip addr flush dev "$iface"

    # Bring up host interface
    ip link set "$iface" up
}

# Build QEMU network arguments for all detected interfaces
# Outputs -netdev/-device argument pairs to stdout
build_network_args() {
    local interfaces
    interfaces=$(detect_interfaces)

    if [ -z "$interfaces" ]; then
        return
    fi

    local idx=0
    local args=""
    for iface in $interfaces; do
        local tap="tap${idx}"
        local mac
        mac=$(generate_mac "$iface")

        # Setup bridge (requires NET_ADMIN capability)
        setup_bridge "$iface" "$idx"

        args="$args -netdev tap,id=net${idx},ifname=${tap},script=no,downscript=no"
        args="$args -device virtio-net-pci,netdev=net${idx},mac=${mac}"

        idx=$((idx + 1))
    done

    echo "$args"
}
