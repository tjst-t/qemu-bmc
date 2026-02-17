#!/bin/bash
# test_quick.sh - Smoke tests (should complete in <30s)

test_container_running() {
    local state
    state=$(docker inspect -f '{{.State.Status}}' "$TEST_CONTAINER" 2>/dev/null || echo "missing")
    assert_equals "running" "$state" "Container should be running"
}

test_ipmi_responds() {
    local result
    result=$(ipmi_cmd mc info 2>&1)
    assert_contains "$result" "Device ID" "IPMI mc info should succeed"
}

test_redfish_responds() {
    local status
    status=$(redfish_get_status "/redfish/v1")
    assert_equals "200" "$status" "Redfish /redfish/v1 should return 200"
}

test_power_status() {
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    local rc=$?
    assert_equals "0" "$rc" "power status command should succeed"
    # Should contain either "on" or "off"
    echo "$state" | grep -qE "on|off"
    assert_success $? "power status should report on or off"
}

# --- Run tests ---
# Don't start a new container - use existing one
if ! docker inspect "$TEST_CONTAINER" >/dev/null 2>&1; then
    start_test_container
fi
wait_for_ipmi_ready 30

run_test test_container_running
run_test test_ipmi_responds
run_test test_redfish_responds
run_test test_power_status
