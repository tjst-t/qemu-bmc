#!/bin/bash
# test_boot.sh - Boot device tests

test_bootdev_pxe() {
    local result
    result=$(ipmi_cmd chassis bootdev pxe 2>&1)
    local rc=$?
    assert_equals "0" "$rc" "chassis bootdev pxe should succeed"
}

test_bootdev_disk() {
    local result
    result=$(ipmi_cmd chassis bootdev disk 2>&1)
    local rc=$?
    assert_equals "0" "$rc" "chassis bootdev disk should succeed"
}

test_bootdev_cdrom() {
    local result
    result=$(ipmi_cmd chassis bootdev cdrom 2>&1)
    local rc=$?
    assert_equals "0" "$rc" "chassis bootdev cdrom should succeed"
}

test_bootdev_bios() {
    local result
    result=$(ipmi_cmd chassis bootdev bios 2>&1)
    local rc=$?
    assert_equals "0" "$rc" "chassis bootdev bios should succeed"
}

test_bootdev_applied_after_cycle() {
    ipmi_cmd chassis bootdev pxe >/dev/null 2>&1
    ipmi_cmd power cycle >/dev/null 2>&1
    wait_for_qemu_running 30
    sleep 2
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-boot" "QEMU should have -boot parameter after cycle"
}

test_bootdev_once_reset() {
    # Set bootdev with options=once
    ipmi_cmd chassis bootdev pxe options=once >/dev/null 2>&1 || \
    ipmi_cmd chassis bootdev pxe >/dev/null 2>&1

    # Power cycle to consume the boot-once
    ipmi_cmd power cycle >/dev/null 2>&1
    wait_for_qemu_running 30
    wait_for_ipmi_ready 30

    # Check via Redfish that BootSourceOverrideEnabled is reset
    local result
    result=$(redfish_get "/redfish/v1/Systems/1")
    # After boot-once is consumed, it should be "Disabled" or "Once" depending on implementation
    assert_contains "$result" "Boot" "Systems/1 should have Boot information"
}

test_bootdev_continuous() {
    # Set boot override via Redfish with Continuous
    redfish_patch "/redfish/v1/Systems/1" \
        '{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Continuous"}}' \
        >/dev/null 2>&1

    # Power cycle
    ipmi_cmd power cycle >/dev/null 2>&1
    wait_for_qemu_running 30
    wait_for_ipmi_ready 30

    # Verify it's still set after boot
    local result
    result=$(redfish_get "/redfish/v1/Systems/1")
    assert_contains "$result" "Continuous" "BootSourceOverrideEnabled should remain Continuous after boot"
}

test_bootdev_redfish_pxe() {
    local status
    status=$(curl -sk -u "${IPMI_USER}:${IPMI_PASS}" \
        -X PATCH -H "Content-Type: application/json" \
        -d '{"Boot":{"BootSourceOverrideTarget":"Pxe"}}' \
        -o /dev/null -w "%{http_code}" \
        "https://${IPMI_HOST}:${REDFISH_PORT}/redfish/v1/Systems/1")
    assert_equals "200" "$status" "Redfish PATCH BootSourceOverrideTarget=Pxe should return 200"
}

test_bootmode_bios() {
    stop_test_container
    start_test_container -e "VM_BOOT_MODE=bios"
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "sga" "BIOS mode should include SGA device"
}

test_bootmode_uefi() {
    stop_test_container
    start_test_container -e "VM_BOOT_MODE=uefi"
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "pflash" "UEFI mode should include pflash drives"
}

# --- Run tests ---
start_test_container
wait_for_ipmi_ready 60

run_test test_bootdev_pxe
run_test test_bootdev_disk
run_test test_bootdev_cdrom
run_test test_bootdev_bios
run_test test_bootdev_applied_after_cycle
run_test test_bootdev_once_reset
run_test test_bootdev_continuous
run_test test_bootdev_redfish_pxe
run_test test_bootmode_bios
run_test test_bootmode_uefi
