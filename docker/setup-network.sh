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
# NIC MAC addresses are not set here: qemu-bmc assigns them (VM_MACS, or
# derived from the system UUID so they differ between nodes)
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

        # Setup bridge (requires NET_ADMIN capability)
        setup_bridge "$iface" "$idx"

        args="$args -netdev tap,id=net${idx},ifname=${tap},script=no,downscript=no"
        args="$args -device virtio-net-pci,netdev=net${idx}"

        idx=$((idx + 1))
    done

    echo "$args"
}
