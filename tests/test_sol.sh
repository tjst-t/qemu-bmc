#!/bin/bash
# test_sol.sh - IPMI SOL: the Debian guest of test_serial.sh is used through
# `ipmitool -I lanplus sol activate` with cipher suites 3 and 17 (run in the
# container on a pseudo terminal that tests/sol_console.py drives like
# expect): root login, command output, `~.`; a second activate is refused,
# `sol deactivate` from another session ends an active one. A raw TCP client
# on SERIAL_ADDR stays connected throughout and sees the same output.

SOL_VOLUME="${SOL_VOLUME:-qemu-bmc-sol-test-vm}"
SOL_LOG_PATH=/redfish/v1/Systems/1/LogServices/SerialConsole/Entries/1/attachment
SOL_CONSOLE="$SCRIPT_DIR/sol_console.py"

# ipmitool in the container: SOL runs on the port the session uses, which
# ipmitool checks, so it talks to 127.0.0.1:623 rather than a published port
SOL_IPMITOOL="docker exec -it $TEST_CONTAINER ipmitool -I lanplus -H 127.0.0.1 -U $IPMI_USER -P $IPMI_PASS"
SOL_IPMITOOL_NOTTY="docker exec $TEST_CONTAINER ipmitool -I lanplus -H 127.0.0.1 -U $IPMI_USER -P $IPMI_PASS"

# sol_console STEP... -- runs `ipmitool [ARGS] sol activate` with the steps of
# sol_console.py; extra ipmitool options come before the steps as -C N
sol_activate() {
    local cipher="$1"
    shift
    # shellcheck disable=SC2086
    timeout 300 python3 "$SOL_CONSOLE" --timeout 60 "$@" -- $SOL_IPMITOOL -C "$cipher" sol activate
}

# A raw client on SERIAL_ADDR in the container, output in /tmp/raw.log
raw_client() {
    docker exec -d "$TEST_CONTAINER" bash -c '
        : >/tmp/raw.log
        exec 3<>/dev/tcp/127.0.0.1/9002 || exit 1
        echo connected >/tmp/raw.status
        cat <&3 >>/tmp/raw.log
        echo disconnected >>/tmp/raw.status'
}

raw_log() {
    container_exec cat /tmp/raw.log 2>/dev/null | tr -d '\r'
}

# SOL payload instance 1 active?  (Get Payload Activation Status)
sol_active() {
    ipmi_cmd raw 0x06 0x4a 0x01 | grep -q '01 01 00'
}

wait_for_login_prompt() {
    local start
    start=$(date +%s)
    until curl -sk --max-time 10 -u "${IPMI_USER}:${IPMI_PASS}" "https://${IPMI_HOST}:${REDFISH_PORT}${SOL_LOG_PATH}" |
        tr -d '\r' | grep -qE 'login: *$'; do
        if [ $(( $(date +%s) - start )) -ge "$1" ]; then
            echo "no login prompt on the serial console within $1 s" >&2
            return 1
        fi
        sleep 2
    done
}

test_sol_info() {
    local info
    info=$(ipmi_cmd sol info)
    assert_contains "$info" "Enabled                         : true" || return 1
    assert_contains "$info" "Payload Port                    : 623" || return 1
    assert_contains "$(ipmi_cmd sol payload status 1 2)" "is enabled" || return 1
    assert_contains "$(redfish_get /redfish/v1/Managers/1)" '"SerialConsole":{"ServiceEnabled":true,"MaxConcurrentSessions":1,"ConnectTypesSupported":["IPMI"]}' || return 1
    assert_contains "$(redfish_get /redfish/v1/Systems/1)" '"IPMI":{"ServiceEnabled":true,"Port":623'
}

# sol_login CIPHER - logs in as root over SOL, runs a command, leaves with ~.
sol_login() {
    local cipher="$1" out
    out=$(sol_activate "$cipher" \
        'expect:SOL Session operational' \
        'send:\r' 'expect:login: *$' \
        'send:root\r' 'expect:root@[^ ]*:~# *$' \
        "send:echo SOL_C${cipher}_\$((6*7))\\r" "expect:SOL_C${cipher}_42" \
        'send:exit\r' 'expect:login: *$' \
        'send:~.' 'expect:terminated ipmitool' 'eof')
    local rc=$?
    echo "$out" | tail -n 8
    [ "$rc" -eq 0 ] || return 1
    # The raw client saw it too
    assert_contains "$(raw_log)" "SOL_C${cipher}_42" "the raw serial client should see the SOL session" || return 1
    assert_equals "connected" "$(container_exec cat /tmp/raw.status)" "the raw serial client should stay connected" || return 1
    # ~. deactivated the payload
    if sol_active; then
        echo "SOL still active after ~." >&2
        return 1
    fi
}

test_sol_login_cipher_suite_3() {
    sol_login 3
}

test_sol_login_cipher_suite_17() {
    sol_login 17
}

# While one session has SOL, a second activate is refused; sol deactivate
# from another session ends the first (ipmitool notices at the next key)
test_sol_deactivate_from_another_session() {
    local out rc
    out=$(sol_activate 17 \
        'expect:SOL Session operational' \
        'send:\r' 'expect:login: *$' \
        "run:$SOL_IPMITOOL_NOTTY sol activate 2>&1 | grep -q 'already active'" \
        "run:$SOL_IPMITOOL_NOTTY raw 0x06 0x4a 0x01 | grep -q '01 01 00'" \
        "run:$SOL_IPMITOOL_NOTTY sol deactivate" \
        'sleep:1' 'send:\r' \
        'expect:SOL session closed by BMC' 'eof')
    rc=$?
    echo "$out" | tail -n 5
    [ "$rc" -eq 0 ] || return 1
    if sol_active; then
        echo "SOL still active after sol deactivate" >&2
        return 1
    fi
    # Free again
    sol_activate 3 'expect:SOL Session operational' 'send:\r' 'expect:login: *$' \
        'send:~.' 'eof' >/dev/null
}

prepare_debian_disk "$SOL_VOLUME"
start_test_container -v "$SOL_VOLUME:/vm" -v "$DEBIAN_CACHE:/base:ro" -e "POWER_ON_AT_START=false"
wait_for_redfish_ready 60
raw_client
redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset '{"ResetType":"On"}' >/dev/null
wait_for_qemu_running 30
wait_for_login_prompt 300

run_test test_sol_info
run_test test_sol_login_cipher_suite_3
run_test test_sol_login_cipher_suite_17
run_test test_sol_deactivate_from_another_session

stop_test_container
cleanup_debian_disk "$SOL_VOLUME"
