#!/bin/bash
# test_container.sh - Container infrastructure tests

test_docker_build() {
    docker build -t "$TEST_IMAGE" -f docker/Dockerfile . >/dev/null 2>&1
    assert_success $? "Docker build should succeed"
}

test_container_starts() {
    start_test_container
    local state
    state=$(docker inspect -f '{{.State.Status}}' "$TEST_CONTAINER" 2>/dev/null)
    assert_equals "running" "$state" "Container should be running"
}

test_qemu_bmc_pid1() {
    local pid1_cmd
    pid1_cmd=$(container_exec cat /proc/1/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$pid1_cmd" "qemu-bmc" "PID 1 should be qemu-bmc"
}

test_qemu_process_running() {
    wait_for_qemu_running 30
    local pid
    pid=$(get_qemu_pid)
    [ -n "$pid" ]
    assert_success $? "QEMU process should be running"
}

test_no_supervisord() {
    local result
    result=$(container_exec pgrep -x supervisord 2>&1 || true)
    assert_equals "" "$result" "supervisord should not be running"
}

test_no_ipmi_sim() {
    local result
    result=$(container_exec pgrep -x ipmi_sim 2>&1 || true)
    assert_equals "" "$result" "ipmi_sim should not be running"
}

test_vnc_port_listening() {
    wait_for_qemu_running 30
    sleep 2
    local listen
    listen=$(container_exec ss -tlnp 2>/dev/null | grep ":5900" || true)
    assert_contains "$listen" "5900" "VNC port 5900 should be listening"
}

test_healthcheck_passes() {
    # Wait for healthcheck to run (start_period=15s + interval=30s)
    local timeout=60
    local i=0
    while [ $i -lt $timeout ]; do
        local health
        health=$(docker inspect -f '{{.State.Health.Status}}' "$TEST_CONTAINER" 2>/dev/null || echo "none")
        if [ "$health" = "healthy" ]; then
            return 0
        fi
        sleep 2
        i=$((i + 2))
    done
    echo "Health status: $(docker inspect -f '{{.State.Health.Status}}' "$TEST_CONTAINER" 2>/dev/null)"
    return 1
}

test_graceful_shutdown() {
    local start
    start=$(date +%s)
    docker stop -t 10 "$TEST_CONTAINER" >/dev/null 2>&1
    local end
    end=$(date +%s)
    local duration=$((end - start))
    # Should stop within 10 seconds (no timeout)
    [ "$duration" -le 10 ]
    assert_success $? "Container should stop gracefully within 10s (took ${duration}s)"

    # Restart for subsequent tests
    docker start "$TEST_CONTAINER" >/dev/null 2>&1
    wait_for_container_running 30
}

# --- Run tests ---
start_test_container
wait_for_ipmi_ready 60

run_test test_docker_build
run_test test_container_starts
run_test test_qemu_bmc_pid1
run_test test_qemu_process_running
run_test test_no_supervisord
run_test test_no_ipmi_sim
run_test test_vnc_port_listening
run_test test_healthcheck_passes
run_test test_graceful_shutdown
