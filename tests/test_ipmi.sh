#!/bin/bash
# test_ipmi.sh - IPMI protocol tests

test_udp_623_listening() {
    local listen
    listen=$(container_exec ss -ulnp 2>/dev/null | grep ":623" || true)
    assert_contains "$listen" "623" "UDP 623 should be listening"
}

test_ipmi_lan_connection() {
    local result
    result=$(ipmi_cmd_lan mc info 2>&1)
    assert_contains "$result" "Device ID" "IPMI lan connection should return Device ID"
}

test_ipmi_lanplus_connection() {
    local result
    result=$(ipmi_cmd mc info 2>&1)
    assert_contains "$result" "Device ID" "IPMI lanplus connection should return Device ID"
}

test_mc_info_content() {
    local result
    result=$(ipmi_cmd mc info 2>&1)
    assert_contains "$result" "Device ID" "mc info should contain Device ID"
    assert_contains "$result" "Firmware Revision" "mc info should contain Firmware Revision"
    assert_contains "$result" "IPMI Version" "mc info should contain IPMI Version"
}

test_ipmi_version_2() {
    local result
    result=$(ipmi_cmd mc info 2>&1)
    assert_contains "$result" "2.0" "IPMI Version should be 2.0"
}

test_auth_correct() {
    local result
    result=$(ipmi_cmd mc info 2>&1)
    local rc=$?
    assert_equals "0" "$rc" "Correct credentials should succeed"
}

test_auth_wrong_password() {
    local result
    result=$(ipmi_cmd_wrong_pass mc info 2>&1)
    local rc=$?
    assert_failure "$rc" "Wrong password should be rejected"
}

test_auth_wrong_username() {
    local result
    result=$(ipmi_cmd_wrong_user mc info 2>&1)
    local rc=$?
    assert_failure "$rc" "Wrong username should be rejected"
}

test_cipher_suite_3_power_status() {
    local result
    result=$(ipmi_cmd -C 3 power status 2>&1)
    local rc=$?
    assert_equals "0" "$rc" "-C 3 power status should succeed: $result"
    assert_contains "$result" "Chassis Power is" "-C 3 power status should report the power state"
}

test_cipher_suite_17_power_status() {
    local result
    result=$(ipmi_cmd -C 17 power status 2>&1)
    local rc=$?
    assert_equals "0" "$rc" "-C 17 power status should succeed: $result"
    assert_contains "$result" "Chassis Power is" "-C 17 power status should report the power state"
}

test_cipher_suite_17_bootdev_pxe() {
    local result
    result=$(ipmi_cmd -C 17 chassis bootdev pxe 2>&1)
    local rc=$?
    assert_equals "0" "$rc" "-C 17 chassis bootdev pxe should succeed: $result"
    result=$(ipmi_cmd -C 17 chassis bootparam get 5 2>&1)
    assert_contains "$result" "PXE" "-C 17 boot device should read back as PXE"
}

test_cipher_suite_17_wrong_password() {
    local result
    result=$(timeout "$CMD_TIMEOUT" ipmitool -I lanplus -C 17 -H "$IPMI_HOST" -p "$IPMI_PORT" \
        -U "$IPMI_USER" -P "wrongpassword" power status 2>&1)
    local rc=$?
    assert_failure "$rc" "-C 17 with a wrong password should be rejected"
}

# --- Run tests ---
start_test_container
wait_for_ipmi_ready 60

run_test test_udp_623_listening
run_test test_ipmi_lan_connection
run_test test_ipmi_lanplus_connection
run_test test_mc_info_content
run_test test_ipmi_version_2
run_test test_auth_correct
run_test test_auth_wrong_password
run_test test_auth_wrong_username
run_test test_cipher_suite_3_power_status
run_test test_cipher_suite_17_power_status
run_test test_cipher_suite_17_bootdev_pxe
run_test test_cipher_suite_17_wrong_password
