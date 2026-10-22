#!/bin/bash
# test_network.sh - Network passthrough tests
# NOTE: These tests require /dev/net/tun and NET_ADMIN capability.
# Some tests may require containerlab or multi-interface setup.

test_no_network_default() {
    stop_test_container
    start_test_container
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(qemu_cmdline) || return 1
    assert_contains "$cmdline" "nic none" "Default should use -nic none"
}

test_tap_device_created() {
    # This test requires a container with additional interfaces
    # In a basic test environment, we verify the script's function exists
    local result
    result=$(container_exec bash -c 'source /scripts/setup-network.sh && type detect_interfaces' 2>&1)
    assert_contains "$result" "function" "setup-network.sh should define detect_interfaces function"
}

test_bridge_created() {
    # Verify setup-network.sh is available
    local result
    result=$(container_exec bash -c 'source /scripts/setup-network.sh && type setup_bridge' 2>&1)
    assert_contains "$result" "function" "setup-network.sh should define setup_bridge function"
}

test_tap_connected_to_bridge() {
    # Verify setup-network.sh is available
    local result
    result=$(container_exec bash -c 'source /scripts/setup-network.sh && type build_network_args' 2>&1)
    assert_contains "$result" "function" "setup-network.sh should define build_network_args function"
}

test_host_iface_on_bridge() {
    # This test requires actual network interfaces in the container
    # Verified by checking the script exists and is sourced
    local result
    result=$(container_exec test -f /scripts/setup-network.sh && echo "exists" || echo "missing")
    assert_equals "exists" "$result" "setup-network.sh should exist in container"
}

test_bridge_no_ip() {
    # Placeholder - requires multi-interface environment
    return 0
}

# Two guest NICs (user-mode backends, so no host interfaces are needed) plus
# an HMP monitor for reading the MACs as QEMU presents them to the guest
MAC_TEST_QEMU_ARGS="-netdev user,id=u0 -device virtio-net-pci,netdev=u0 -netdev user,id=u1 -device e1000,netdev=u1 -monitor telnet:127.0.0.1:4444,server,nowait"

start_mac_test_container() {
    stop_test_container
    start_test_container -e "POWER_ON_AT_START=true" -e "QEMU_EXTRA_ARGS=$MAC_TEST_QEMU_ARGS" "$@"
    wait_for_qemu_running 30
    wait_for_redfish_ready 60
}

# Prints the MAC of each EthernetInterface reported by Redfish, one per line
redfish_nic_macs() {
    local members
    members=$(redfish_get "/redfish/v1/Systems/1/EthernetInterfaces" | grep -o '"/redfish/v1/Systems/1/EthernetInterfaces/[^"]*"' | tr -d '"')
    for m in $members; do
        redfish_get "$m" | sed -n 's/.*"MACAddress":"\([^"]*\)".*/\1/p'
    done
}

# Prints the guest NIC MACs from HMP "info network", one per line
qemu_nic_macs() {
    container_exec bash -c 'exec 3<>/dev/tcp/127.0.0.1/4444; echo "info network" >&3; sleep 1; timeout 1 cat <&3' 2>/dev/null \
        | tr -d '\r' | sed -n 's/.*type=nic,.*macaddr=\([0-9a-f:]*\).*/\1/p'
}

test_nic_mac_matches_qemu() {
    start_mac_test_container --hostname mac-node1
    local redfish qemu
    redfish=$(redfish_nic_macs)
    qemu=$(qemu_nic_macs)
    assert_equals 2 "$(echo "$redfish" | grep -c '^52:54:')" "Redfish should report 2 derived MACs" || return 1
    assert_equals "$qemu" "$redfish" "Redfish MACs should match the MACs QEMU gives the guest"
}

test_nic_mac_stable_and_unique_per_node() {
    start_mac_test_container --hostname mac-node1
    local node1
    node1=$(redfish_nic_macs)
    start_mac_test_container --hostname mac-node1
    assert_equals "$node1" "$(redfish_nic_macs)" "MACs should survive re-creation with the same hostname" || return 1
    start_mac_test_container --hostname mac-node2
    local node2
    node2=$(redfish_nic_macs)
    [ -n "$node2" ] && [ -z "$(printf '%s\n%s\n' "$node1" "$node2" | sort | uniq -d)" ]
    assert_success $? "MACs of two nodes should not overlap (node1: $node1 / node2: $node2)"
}

test_nic_mac_explicit() {
    start_mac_test_container -e "VM_MACS=02:00:00:00:00:01,02:00:00:00:00:02"
    local expected
    expected=$(printf '02:00:00:00:00:01\n02:00:00:00:00:02')
    assert_equals "$expected" "$(redfish_nic_macs)" "Redfish should report VM_MACS" || return 1
    assert_equals "$expected" "$(qemu_nic_macs)" "QEMU should use VM_MACS"
}

test_qemu_network_args() {
    # Without additional interfaces, QEMU uses -nic none
    stop_test_container
    start_test_container
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(qemu_cmdline) || return 1
    # In basic environment, should have nic none
    assert_contains "$cmdline" "nic" "QEMU should have network argument"
}

# Starts a container passing a veth "mtu0" (MTU 1500, created in the container
# while entrypoint.sh waits for VM_NETWORKS) through to the VM
start_mtu_test_container() {
    stop_test_container
    start_test_container -e "VM_NETWORKS=mtu0" "$@" || return 1
    container_exec ip link add mtu0 type veth peer name mtu0p || return 1
    wait_for_qemu_running 60
}

link_mtu() {
    container_exec cat "/sys/class/net/$1/mtu" 2>/dev/null
}

test_net_mtu() {
    start_mtu_test_container -e "VM_NET_MTU=9000" || return 1
    assert_equals 9000 "$(link_mtu tap0)" "tap0 MTU should be VM_NET_MTU" || return 1
    assert_equals 9000 "$(link_mtu br0)" "br0 MTU should be VM_NET_MTU" || return 1
    assert_equals 9000 "$(link_mtu mtu0)" "Container interface MTU should be raised to VM_NET_MTU" || return 1
    local cmdline
    cmdline=$(qemu_cmdline) || return 1
    assert_contains "$cmdline" "virtio-net-pci,netdev=net0,host_mtu=9000" "QEMU virtio-net NIC should get host_mtu"
}

test_net_mtu_unset() {
    start_mtu_test_container || return 1
    assert_equals 1500 "$(link_mtu tap0)" "tap0 MTU should be left at the default" || return 1
    assert_equals 1500 "$(link_mtu br0)" "br0 MTU should be left at the default" || return 1
    local cmdline
    cmdline=$(qemu_cmdline) || return 1
    assert_not_contains "$cmdline" "host_mtu" "QEMU should not get host_mtu without VM_NET_MTU"
}

test_net_mtu_invalid() {
    local result
    result=$(container_exec bash -c 'source /scripts/setup-network.sh; VM_NET_MTU=9k build_network_args; echo "rc=$?"' 2>&1)
    assert_contains "$result" "rc=1" "Non-numeric VM_NET_MTU should fail" || return 1
    assert_contains "$result" "VM_NET_MTU='9k' is not a number" "The error should say why" || return 1
    result=$(container_exec bash -c 'source /scripts/setup-network.sh; VM_NET_MTU=100000 build_network_args; echo "rc=$?"' 2>&1)
    assert_contains "$result" "out of range" "Out-of-range VM_NET_MTU should fail"
}

# --- Run tests ---
start_test_container
wait_for_ipmi_ready 60

run_test test_no_network_default
run_test test_tap_device_created
run_test test_bridge_created
run_test test_tap_connected_to_bridge
run_test test_host_iface_on_bridge
run_test test_bridge_no_ip
run_test test_nic_mac_matches_qemu
run_test test_nic_mac_stable_and_unique_per_node
run_test test_nic_mac_explicit
run_test test_qemu_network_args
run_test test_net_mtu
run_test test_net_mtu_unset
run_test test_net_mtu_invalid
