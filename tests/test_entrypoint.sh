#!/bin/bash
# test_entrypoint.sh - Environment variable / entrypoint tests

test_default_memory() {
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-m 2048" "Default memory should be 2048"
}

test_custom_memory() {
    stop_test_container
    start_test_container -e "VM_MEMORY=4096"
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-m 4096" "Custom memory should be 4096"
}

test_default_cpus() {
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-smp 2" "Default CPUs should be 2"
}

test_custom_cpus() {
    stop_test_container
    start_test_container -e "VM_CPUS=4"
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-smp 4" "Custom CPUs should be 4"
}

test_kvm_enabled() {
    # Only run if KVM is available on host
    if [ ! -e /dev/kvm ]; then
        echo "SKIP: /dev/kvm not available"
        return 0
    fi
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "accel=kvm" "KVM should be enabled when /dev/kvm is available"
}

test_kvm_fallback_tcg() {
    stop_test_container
    # Start without /dev/kvm device
    docker rm -f "$TEST_CONTAINER" 2>/dev/null || true
    docker run -d \
        --name "$TEST_CONTAINER" \
        --device /dev/net/tun:/dev/net/tun \
        --cap-add NET_ADMIN \
        --cap-add SYS_ADMIN \
        --security-opt apparmor=unconfined \
        -p "${IPMI_PORT}:623/udp" \
        -p "${REDFISH_PORT}:443" \
        -e "IPMI_USER=${IPMI_USER}" \
        -e "IPMI_PASS=${IPMI_PASS}" \
        -e "ENABLE_KVM=true" \
        "$TEST_IMAGE"
    wait_for_container_running 30
    wait_for_qemu_running 60
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "accel=tcg" "Should fallback to TCG without /dev/kvm"
}

test_custom_vnc_port() {
    stop_test_container
    start_test_container -e "VNC_PORT=5901"
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-vnc :1" "VNC_PORT=5901 should produce -vnc :1"
}

test_disk_attached() {
    # Create a dummy disk in a temp directory
    local tmpdir
    tmpdir=$(mktemp -d)
    docker run --rm -v "$tmpdir:/mnt" "$TEST_IMAGE" qemu-img create -f qcow2 /mnt/test.qcow2 1G 2>/dev/null || \
    qemu-img create -f qcow2 "$tmpdir/test.qcow2" 1G 2>/dev/null || \
    truncate -s 1G "$tmpdir/test.qcow2"

    stop_test_container
    docker rm -f "$TEST_CONTAINER" 2>/dev/null || true
    docker run -d \
        --name "$TEST_CONTAINER" \
        --device /dev/kvm:/dev/kvm \
        --cap-add SYS_ADMIN \
        --security-opt apparmor=unconfined \
        -v "$tmpdir:/vm" \
        -p "${IPMI_PORT}:623/udp" \
        -p "${REDFISH_PORT}:443" \
        -e "IPMI_USER=${IPMI_USER}" \
        -e "IPMI_PASS=${IPMI_PASS}" \
        -e "VM_DISK=/vm/test.qcow2" \
        "$TEST_IMAGE"
    wait_for_container_running 30
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-drive" "Disk should be attached with -drive"
    rm -rf "$tmpdir"
}

test_disk_missing_no_error() {
    stop_test_container
    start_test_container -e "VM_DISK=/vm/nonexistent.qcow2"
    wait_for_container_running 30
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_not_contains "$cmdline" "disk.qcow2" "Missing disk should not add a disk -drive"
}

test_cdrom_attached() {
    local tmpdir
    tmpdir=$(mktemp -d)
    truncate -s 1M "$tmpdir/test.iso"

    stop_test_container
    docker rm -f "$TEST_CONTAINER" 2>/dev/null || true
    docker run -d \
        --name "$TEST_CONTAINER" \
        --device /dev/kvm:/dev/kvm \
        --cap-add SYS_ADMIN \
        --security-opt apparmor=unconfined \
        -v "$tmpdir:/iso" \
        -p "${IPMI_PORT}:623/udp" \
        -p "${REDFISH_PORT}:443" \
        -e "IPMI_USER=${IPMI_USER}" \
        -e "IPMI_PASS=${IPMI_PASS}" \
        -e "VM_CDROM=/iso/test.iso" \
        "$TEST_IMAGE"
    wait_for_container_running 30
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    # qemu-bmc turns -cdrom into its VirtualMedia CD drive holding the ISO
    assert_contains "$cmdline" "media=cdrom,readonly=on,format=raw,file=/iso/test.iso" "CD-ROM should hold VM_CDROM"
    rm -rf "$tmpdir"
}

test_boot_device_default() {
    stop_test_container
    start_test_container
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-boot" "Default boot device should be set"
}

test_custom_ipmi_credentials() {
    stop_test_container
    docker rm -f "$TEST_CONTAINER" 2>/dev/null || true
    docker run -d \
        --name "$TEST_CONTAINER" \
        --device /dev/kvm:/dev/kvm \
        --cap-add SYS_ADMIN \
        --security-opt apparmor=unconfined \
        -p "${IPMI_PORT}:623/udp" \
        -p "${REDFISH_PORT}:443" \
        -e "IPMI_USER=testuser" \
        -e "IPMI_PASS=testpass123" \
        "$TEST_IMAGE"
    wait_for_container_running 30
    wait_for_qemu_running 30
    sleep 3
    local result
    result=$(ipmitool -I lanplus -H "$IPMI_HOST" -p "$IPMI_PORT" \
        -U testuser -P testpass123 mc info 2>&1)
    assert_contains "$result" "Device ID" "Custom IPMI credentials should work"
}

test_qemu_extra_args() {
    stop_test_container
    start_test_container -e "QEMU_EXTRA_ARGS=-device virtio-rng-pci"
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "virtio-rng-pci" "Extra QEMU args should be passed"
}

test_debug_output() {
    stop_test_container
    start_test_container -e "DEBUG=true"
    wait_for_container_running 30
    sleep 3
    local logs
    logs=$(docker logs "$TEST_CONTAINER" 2>&1)
    assert_contains "$logs" "qemu-bmc startup" "Debug output should show startup info"
}

# Prints the UUID reported by Redfish Systems/1
redfish_system_uuid() {
    redfish_get "/redfish/v1/Systems/1" | sed -n 's/.*"UUID":"\([^"]*\)".*/\1/p'
}

# Prints the GUID reported by IPMI Get System GUID (ipmitool mc guid)
ipmi_system_guid() {
    ipmi_cmd mc guid 2>/dev/null | sed -n 's/^System GUID *: *//p'
}

# Prints the -uuid value on the QEMU command line
qemu_cmdline_uuid() {
    container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' '\n' | sed -n '/^-uuid$/{n;p;}'
}

test_system_uuid_explicit() {
    local uuid="0a1b2c3d-4e5f-4061-8273-8495a6b7c8d9"
    stop_test_container
    start_test_container -e "SYSTEM_UUID=$uuid"
    wait_for_qemu_running 30
    wait_for_ipmi_ready 60
    wait_for_redfish_ready 60
    assert_equals "$uuid" "$(qemu_cmdline_uuid)" "QEMU -uuid should be SYSTEM_UUID"
    assert_equals "$uuid" "$(redfish_system_uuid)" "Redfish UUID should be SYSTEM_UUID"
    assert_equals "$uuid" "$(ipmi_system_guid)" "IPMI System GUID should be SYSTEM_UUID"
}

test_system_uuid_stable_hostname() {
    stop_test_container
    start_test_container --hostname uuid-node1
    wait_for_qemu_running 30
    wait_for_redfish_ready 60
    local first
    first=$(redfish_system_uuid)

    stop_test_container
    start_test_container --hostname uuid-node1
    wait_for_qemu_running 30
    wait_for_ipmi_ready 60
    wait_for_redfish_ready 60
    assert_equals "$first" "$(redfish_system_uuid)" "Derived UUID should survive re-creation with the same hostname"
    assert_equals "$first" "$(qemu_cmdline_uuid)" "QEMU -uuid should match Redfish UUID"
    assert_equals "$first" "$(ipmi_system_guid)" "IPMI System GUID should match Redfish UUID"
}

test_system_uuid_persisted_in_volume() {
    local tmpdir
    tmpdir=$(mktemp -d)
    chmod 777 "$tmpdir"
    stop_test_container
    start_test_container -v "$tmpdir:/vm"
    wait_for_qemu_running 30
    wait_for_redfish_ready 60
    local first
    first=$(redfish_system_uuid)

    # Re-create with a different (random) hostname: the persisted UUID wins
    stop_test_container
    start_test_container -v "$tmpdir:/vm"
    wait_for_qemu_running 30
    wait_for_redfish_ready 60
    assert_equals "$first" "$(redfish_system_uuid)" "Persisted UUID should survive re-creation"
    assert_equals "$first" "$(qemu_cmdline_uuid)" "QEMU -uuid should match persisted UUID"
    stop_test_container
    docker run --rm -v "$tmpdir:/vm" --entrypoint rm "$TEST_IMAGE" -f /vm/system-uuid 2>/dev/null || true
    rm -rf "$tmpdir"
}

# --- Run tests ---
start_test_container
wait_for_ipmi_ready 60

run_test test_default_memory
run_test test_custom_memory
run_test test_default_cpus
run_test test_custom_cpus
run_test test_kvm_enabled
run_test test_kvm_fallback_tcg
run_test test_custom_vnc_port
run_test test_disk_attached
run_test test_disk_missing_no_error
run_test test_cdrom_attached
run_test test_boot_device_default
run_test test_custom_ipmi_credentials
run_test test_qemu_extra_args
run_test test_debug_output
run_test test_system_uuid_explicit
run_test test_system_uuid_stable_hostname
run_test test_system_uuid_persisted_in_volume
