#!/bin/bash
# test_fault.sh - Fault injection tests (FAULT_API_ADDR, FAULTS)

FAULT_API_PORT="${FAULT_API_PORT:-8623}"
FAULT_API_TOKEN="fault-test-token"

# fault_api METHOD PATH [JSON] - calls the fault API, prints the body
fault_api() {
    local method="$1" path="$2" data="${3:-}"
    local args=(-s --max-time "$CMD_TIMEOUT" -X "$method"
        -H "Authorization: Bearer ${FAULT_API_TOKEN}")
    if [ -n "$data" ]; then
        args+=(-H "Content-Type: application/json" -d "$data")
    fi
    curl "${args[@]}" "http://${IPMI_HOST}:${FAULT_API_PORT}${path}"
}

# json_get EXPR - prints Python expression EXPR over the JSON on stdin (d)
json_get() {
    python3 -c 'import json,sys
try:
    d = json.load(sys.stdin)
    print(eval(sys.argv[1]))
except Exception as e:
    print("ERROR: %s" % e)' "$1"
}

# add_fault JSON - adds a fault, prints its id
add_fault() {
    fault_api POST /faults "$1" | json_get "d['id']"
}

# Fast-failing ipmitool: 1 s timeout, 1 retry
ipmi_cmd_quick() {
    ipmi_cmd -N 1 -R 1 "$@"
}

test_fault_api_requires_token() {
    local status
    status=$(curl -s --max-time "$CMD_TIMEOUT" -o /dev/null -w "%{http_code}" \
        "http://${IPMI_HOST}:${FAULT_API_PORT}/faults")
    assert_equals "401" "$status" "fault API without the token should be 401"
}

test_fault_api_initial_faults() {
    # FAULTS at start: one harmless fault on a path nobody uses
    local list
    list=$(fault_api GET /faults)
    assert_equals "1" "$(echo "$list" | json_get "len(d)")" "FAULTS fault should be listed: $list" || return 1
    assert_equals "redfish-delay" "$(echo "$list" | json_get "d[0]['kind']")" || return 1
    fault_api DELETE /faults
    assert_equals "0" "$(fault_api GET /faults | json_get "len(d)")" "DELETE /faults should remove all"
}

test_fault_redfish_error() {
    local id status body
    id=$(add_fault '{"kind":"redfish-error","status":503,"path":"/redfish/v1/Systems"}')
    [ -n "$id" ] && [ "$id" != "null" ] || { echo "no fault id" >&2; return 1; }

    status=$(redfish_get_status /redfish/v1/Systems/1)
    assert_equals "503" "$status" "Systems/1 should fail with 503" || return 1
    body=$(redfish_get /redfish/v1/Systems/1)
    assert_equals "Base.1.0.GeneralError" "$(echo "$body" | json_get "d['error']['code']")" "Redfish error body: $body" || return 1
    # Other paths are not affected
    assert_equals "200" "$(redfish_get_status /redfish/v1/Managers/1)" || return 1
    assert_contains "$(docker logs "$TEST_CONTAINER" 2>&1)" "fault: $id redfish-error applied to Redfish GET /redfish/v1/Systems/1" || return 1

    fault_api DELETE "/faults/$id"
    assert_equals "200" "$(redfish_get_status /redfish/v1/Systems/1)" "Systems/1 should be back after DELETE"
}

test_fault_redfish_hang() {
    local id rc=0
    id=$(add_fault '{"kind":"redfish-hang","path":"/redfish/v1/Chassis"}')
    curl -sk --max-time 3 -u "${IPMI_USER}:${IPMI_PASS}" -o /dev/null \
        "https://${IPMI_HOST}:${REDFISH_PORT}/redfish/v1/Chassis/1" || rc=$?
    assert_equals "28" "$rc" "curl should time out (exit 28) on a hung request" || return 1

    fault_api DELETE "/faults/$id"
    assert_equals "200" "$(redfish_get_status /redfish/v1/Chassis/1)" "Chassis/1 should be back after DELETE"
}

test_fault_redfish_drop() {
    local id rc=0
    id=$(add_fault '{"kind":"redfish-drop","count":1}')
    curl -sk --max-time 5 -u "${IPMI_USER}:${IPMI_PASS}" -o /dev/null \
        "https://${IPMI_HOST}:${REDFISH_PORT}/redfish/v1" || rc=$?
    # 35 (TLS handshake reset), 52 (empty reply) or 56 (recv failure)
    [ "$rc" -ne 0 ] && [ "$rc" -ne 28 ]
    assert_success $? "curl should fail at once on a dropped connection (exit $rc)" || return 1
    # count 1: gone after one use
    assert_equals "200" "$(redfish_get_status /redfish/v1)"
}

test_fault_ipmi_drop() {
    local id rc=0
    id=$(add_fault '{"kind":"ipmi-drop","netfn":0,"cmd":1}')
    ipmi_cmd_quick power status >/dev/null 2>&1 || rc=$?
    assert_failure $rc "ipmitool power status should time out" || return 1
    # Other commands (and the session setup) still work
    ipmi_cmd_quick mc info >/dev/null 2>&1
    assert_success $? "mc info should still work" || return 1

    fault_api DELETE "/faults/$id"
    assert_contains "$(ipmi_cmd_quick power status 2>&1)" "Chassis Power is on" "power status should work after DELETE"
}

test_fault_power_action_ignored() {
    local id pid_before pid_after out
    pid_before=$(get_qemu_pid)
    [ -n "$pid_before" ] || { echo "QEMU not running" >&2; return 1; }
    id=$(add_fault '{"kind":"power-action-ignored","action":"ForceOff"}')

    out=$(ipmi_cmd power off 2>&1)
    assert_success $? "ipmitool power off should succeed: $out" || return 1
    out=$(curl -sk --max-time "$CMD_TIMEOUT" -u "${IPMI_USER}:${IPMI_PASS}" -o /dev/null -w "%{http_code}" \
        -X POST -H "Content-Type: application/json" -d '{"ResetType":"ForceOff"}' \
        "https://${IPMI_HOST}:${REDFISH_PORT}/redfish/v1/Systems/1/Actions/ComputerSystem.Reset")
    assert_equals "204" "$out" "Redfish ForceOff should return 204" || return 1
    sleep 3
    pid_after=$(get_qemu_pid)
    assert_equals "$pid_before" "$pid_after" "QEMU should still run (power action ignored)" || return 1
    assert_contains "$(ipmi_cmd power status 2>&1)" "Chassis Power is on" || return 1

    fault_api DELETE "/faults/$id"
    ipmi_cmd power off >/dev/null 2>&1
    wait_for_qemu_stopped 30 || return 1
    ipmi_cmd power on >/dev/null 2>&1
    wait_for_qemu_running 30
}

test_fault_power_action_fail() {
    local id status
    id=$(add_fault '{"kind":"power-action-fail","action":"ForceRestart","completionCode":"0xc0"}')
    status=$(curl -sk --max-time "$CMD_TIMEOUT" -u "${IPMI_USER}:${IPMI_PASS}" -o /dev/null -w "%{http_code}" \
        -X POST -H "Content-Type: application/json" -d '{"ResetType":"ForceRestart"}' \
        "https://${IPMI_HOST}:${REDFISH_PORT}/redfish/v1/Systems/1/Actions/ComputerSystem.Reset")
    assert_equals "500" "$status" "Redfish ForceRestart should fail with 500" || return 1
    local out rc=0
    out=$(ipmi_cmd power reset 2>&1) || rc=$?
    assert_failure $rc "ipmitool power reset should fail: $out" || return 1
    assert_contains "$out" "Node busy" || return 1
    fault_api DELETE "/faults/$id"
}

test_fault_bmc_unavailable() {
    local id rc=0 start
    id=$(add_fault '{"kind":"bmc-unavailable","durationSeconds":8}')
    start=$(date +%s)
    curl -sk --max-time 2 -u "${IPMI_USER}:${IPMI_PASS}" -o /dev/null \
        "https://${IPMI_HOST}:${REDFISH_PORT}/redfish/v1" || rc=$?
    assert_equals "28" "$rc" "Redfish should not respond" || return 1
    rc=0
    ipmi_cmd_quick mc info >/dev/null 2>&1 || rc=$?
    assert_failure $rc "IPMI should not respond" || return 1

    # Back by itself after durationSeconds
    while [ $(($(date +%s) - start)) -lt 9 ]; do sleep 1; done
    assert_equals "200" "$(redfish_get_status /redfish/v1)" "Redfish should be back after durationSeconds" || return 1
    ipmi_cmd_quick mc info >/dev/null 2>&1
    assert_success $? "IPMI should be back after durationSeconds" || return 1
    assert_equals "0" "$(fault_api GET /faults | json_get "len(d)")" "expired fault should be gone"
}

test_fault_bmc_unavailable_deleted() {
    local id rc=0
    id=$(add_fault '{"kind":"bmc-unavailable"}')
    ipmi_cmd_quick mc info >/dev/null 2>&1 || rc=$?
    assert_failure $rc "IPMI should not respond" || return 1
    fault_api DELETE "/faults/$id"
    assert_equals "200" "$(redfish_get_status /redfish/v1)" || return 1
    ipmi_cmd_quick mc info >/dev/null 2>&1
    assert_success $? "IPMI should be back after DELETE"
}

start_test_container -p "${FAULT_API_PORT}:8623" \
    -e "FAULT_API_ADDR=:8623" -e "FAULT_API_TOKEN=${FAULT_API_TOKEN}" \
    -e 'FAULTS=[{"kind":"redfish-delay","delayMs":1,"path":"/unused"}]'
wait_for_ipmi_ready 60
wait_for_redfish_ready 60

run_test test_fault_api_requires_token
run_test test_fault_api_initial_faults
run_test test_fault_redfish_error
run_test test_fault_redfish_hang
run_test test_fault_redfish_drop
run_test test_fault_ipmi_drop
run_test test_fault_power_action_ignored
run_test test_fault_power_action_fail
run_test test_fault_bmc_unavailable
run_test test_fault_bmc_unavailable_deleted
