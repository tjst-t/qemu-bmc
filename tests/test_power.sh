#!/bin/bash
# test_power.sh - Power control tests

test_initial_power_on() {
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "on" "Initial power state should be On"
}

test_power_status() {
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "Chassis Power is" "power status should report state"
}

test_power_off() {
    ipmi_cmd power off >/dev/null 2>&1
    wait_for_qemu_stopped 30
    local pid
    pid=$(get_qemu_pid)
    assert_equals "" "$pid" "QEMU process should not be running after power off"
}

test_power_off_state() {
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "off" "Power status should be Off after power off"
}

test_power_on() {
    ipmi_cmd power on >/dev/null 2>&1
    wait_for_qemu_running 30
    local pid
    pid=$(get_qemu_pid)
    [ -n "$pid" ]
    assert_success $? "QEMU process should be running after power on"
}

test_power_on_state() {
    wait_for_power_state "on" 30
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "on" "Power status should be On after power on"
}

test_power_cycle_pid_changes() {
    local pid_before
    pid_before=$(get_qemu_pid)
    ipmi_cmd power cycle >/dev/null 2>&1
    sleep 3
    wait_for_qemu_running 30
    local pid_after
    pid_after=$(get_qemu_pid)
    [ "$pid_before" != "$pid_after" ]
    assert_success $? "QEMU PID should change after power cycle (was $pid_before, now $pid_after)"
}

test_power_cycle_state_on() {
    wait_for_power_state "on" 30
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "on" "Power state should be On after power cycle"
}

test_power_reset_pid_unchanged() {
    local pid_before
    pid_before=$(get_qemu_pid)
    ipmi_cmd power reset >/dev/null 2>&1
    sleep 2
    local pid_after
    pid_after=$(get_qemu_pid)
    assert_equals "$pid_before" "$pid_after" "QEMU PID should not change after power reset"
}

test_power_reset_state_on() {
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "on" "Power state should be On after power reset"
}

test_graceful_shutdown() {
    ipmi_cmd power soft >/dev/null 2>&1
    local rc=$?
    # ACPI shutdown signal sent; may or may not actually shut down
    # depending on guest OS. Just verify command succeeds.
    assert_success $rc "power soft command should succeed"
    # Give time for potential shutdown
    sleep 3
    # Ensure we're back on for remaining tests
    ipmi_cmd power on >/dev/null 2>&1
    wait_for_qemu_running 30
}

test_power_off_on_cycle() {
    # Off
    ipmi_cmd power off >/dev/null 2>&1
    wait_for_power_state "off" 15
    # On
    ipmi_cmd power on >/dev/null 2>&1
    wait_for_power_state "on" 30
    # Off again
    ipmi_cmd power off >/dev/null 2>&1
    wait_for_power_state "off" 15
    # On again
    ipmi_cmd power on >/dev/null 2>&1
    wait_for_power_state "on" 30
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "on" "Final state should be On after Off→On→Off→On"
}

test_qemu_crash_detection() {
    local pid
    pid=$(get_qemu_pid)
    container_exec kill -9 "$pid" 2>/dev/null || true
    wait_for_power_state "off" 30
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "off" "Power state should be Off after QEMU crash"
}

test_power_on_after_crash() {
    ipmi_cmd power on >/dev/null 2>&1
    wait_for_qemu_running 30
    wait_for_power_state "on" 30
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "on" "Power on should work after crash"
}

# --- Run tests ---
start_test_container
wait_for_ipmi_ready 60

run_test test_initial_power_on
run_test test_power_status
run_test test_power_off
run_test test_power_off_state
run_test test_power_on
run_test test_power_on_state
run_test test_power_cycle_pid_changes
run_test test_power_cycle_state_on
run_test test_power_reset_pid_unchanged
run_test test_power_reset_state_on
run_test test_graceful_shutdown
run_test test_power_off_on_cycle
run_test test_qemu_crash_detection
run_test test_power_on_after_crash
