#!/bin/bash
# test_serial.sh - Serial console multiplexing: a Debian guest (nocloud cloud
# image, root without password, getty on ttyS0) boots from a qcow2 overlay;
# two clients inside the container stay connected to SERIAL_ADDR
# (localhost:9002) at the same time, both get the login prompt and the
# output of commands typed on either, and both stay connected across a QEMU
# power off/on. The Redfish SerialConsole log returns the same output.

SERIAL_CACHE="${SERIAL_CACHE:-${IRONIC_E2E_CACHE:-$HOME/.cache/qemu-bmc-ironic-e2e}}"
SERIAL_DEBIAN_URL="${SERIAL_DEBIAN_URL:-https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-nocloud-amd64.qcow2}"
SERIAL_DEBIAN_FILE="debian-12-nocloud-amd64.qcow2"
SERIAL_VOLUME="${SERIAL_VOLUME:-qemu-bmc-serial-test-vm}"
SERIAL_LOG_PATH=/redfish/v1/Systems/1/LogServices/SerialConsole/Entries/1/attachment

prepare_serial_disk() {
    mkdir -p "$SERIAL_CACHE"
    if [ ! -s "$SERIAL_CACHE/$SERIAL_DEBIAN_FILE" ]; then
        echo "Downloading $SERIAL_DEBIAN_URL"
        curl -fL --retry 3 -o "$SERIAL_CACHE/$SERIAL_DEBIAN_FILE.part" "$SERIAL_DEBIAN_URL" &&
            mv "$SERIAL_CACHE/$SERIAL_DEBIAN_FILE.part" "$SERIAL_CACHE/$SERIAL_DEBIAN_FILE"
    fi
    docker volume rm -f "$SERIAL_VOLUME" >/dev/null 2>&1 || true
    docker volume create "$SERIAL_VOLUME" >/dev/null
    # The cached image stays untouched: the VM writes to an overlay
    docker run --rm -v "$SERIAL_VOLUME:/vm" -v "$SERIAL_CACHE:/base:ro" --entrypoint qemu-img "$TEST_IMAGE" \
        create -q -f qcow2 -b "/base/$SERIAL_DEBIAN_FILE" -F qcow2 /vm/disk.qcow2
}

cleanup_serial_disk() {
    docker volume rm -f "$SERIAL_VOLUME" >/dev/null 2>&1 || true
}

# serial_client NAME - keeps one connection to the serial console open in the
# container: output goes to /tmp/NAME.log, what is written to the /tmp/NAME.in
# FIFO is sent. /tmp/NAME.status gets "connected", then "disconnected" if the
# connection ends (it is not reopened).
serial_client() {
    docker exec -d "$TEST_CONTAINER" bash -c '
        n=$1
        rm -f /tmp/$n.in /tmp/$n.log; mkfifo /tmp/$n.in; exec 4<>/tmp/$n.in
        : >/tmp/$n.log
        if ! exec 3<>/dev/tcp/127.0.0.1/9002; then
            echo failed >/tmp/$n.status; exit 1
        fi
        echo connected >/tmp/$n.status
        cat <&4 >&3 &
        cat <&3 >>/tmp/$n.log
        echo disconnected >>/tmp/$n.status' _ "$1"
}

serial_status() {
    container_exec cat "/tmp/$1.status" 2>/dev/null
}

serial_size() {
    container_exec bash -c 'stat -c %s "/tmp/$1.log" 2>/dev/null || echo 0' _ "$1"
}

# serial_since NAME OFFSET - output of client NAME after byte OFFSET, without CR
serial_since() {
    container_exec bash -c 'tail -c +$(( $2 + 1 )) "/tmp/$1.log" 2>/dev/null' _ "$1" "$2" | tr -d '\r'
}

serial_send() {
    container_exec bash -c 'printf "%s\r" "$2" >"/tmp/$1.in"' _ "$1" "$2"
}

# serial_wait NAME OFFSET REGEX TIMEOUT [POKE] - waits for extended REGEX in
# the output of NAME after OFFSET; with POKE, an empty line is sent through
# client POKE every 10 s (wakes up getty)
serial_wait() {
    local name="$1" off="$2" re="$3" timeout="$4" poke="$5" start
    start=$(date +%s)
    until serial_since "$name" "$off" | grep -qE -- "$re"; do
        if [ $(( $(date +%s) - start )) -ge "$timeout" ]; then
            echo "timed out waiting for /$re/ on $name; last output:"
            serial_since "$name" "$off" | tail -n 5
            return 1
        fi
        if [ -n "$poke" ] && [ $(( ($(date +%s) - start) % 10 )) -eq 0 ]; then
            serial_send "$poke" ""
        fi
        sleep 1
    done
}

test_serial_qemu_uses_internal_socket() {
    local cmdline
    cmdline=$(qemu_cmdline)
    assert_contains "$cmdline" "socket,id=serial0,path=/var/run/qemu/serial.sock,server=off" "QEMU should connect to the internal serial socket" || return 1
    assert_not_contains "$cmdline" "port=9002" "QEMU should not listen on SERIAL_ADDR itself" || return 1
    assert_equals "connected" "$(serial_status a)" "client a" || return 1
    assert_equals "connected" "$(serial_status b)" "client b"
}

test_serial_two_clients_get_login_prompt() {
    serial_wait a 0 'login: *$' 300 a || return 1
    serial_wait b 0 'login: *$' 30
}

test_serial_command_output_on_both_clients() {
    local off_a off_b
    off_a=$(serial_size a); off_b=$(serial_size b)
    serial_send a "root"
    serial_wait a "$off_a" 'root@[^ ]*:~# *$' 60 || return 1
    serial_wait b "$off_b" 'root@[^ ]*:~# *$' 10 || return 1

    # Typed on a; the shell's answer (not the echo of the command) is on both
    off_a=$(serial_size a); off_b=$(serial_size b)
    serial_send a 'echo FROM_A_$((6*7))'
    serial_wait a "$off_a" 'FROM_A_42' 30 || return 1
    serial_wait b "$off_b" 'FROM_A_42' 10 || return 1

    # Typed on b
    off_a=$(serial_size a); off_b=$(serial_size b)
    serial_send b 'echo FROM_B_$((40+3))'
    serial_wait b "$off_b" 'FROM_B_43' 30 || return 1
    serial_wait a "$off_a" 'FROM_B_43' 10
}

test_serial_redfish_log() {
    local log entry
    log=$(curl -sk --max-time "$CMD_TIMEOUT" -u "${IPMI_USER}:${IPMI_PASS}" \
        "https://${IPMI_HOST}:${REDFISH_PORT}${SERIAL_LOG_PATH}" | tr -d '\r')
    assert_contains "$log" "FROM_A_42" "Redfish serial log should have the console output" || return 1
    assert_contains "$log" "FROM_B_43" "Redfish serial log should have the console output" || return 1
    entry=$(redfish_get /redfish/v1/Systems/1/LogServices/SerialConsole/Entries/1)
    assert_contains "$entry" "\"AdditionalDataURI\":\"$SERIAL_LOG_PATH\"" || return 1
    assert_contains "$(redfish_get /redfish/v1/Systems/1/LogServices)" "SerialConsole"
}

test_serial_survives_power_off_on() {
    local off_a off_b
    serial_send a "exit"
    redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset '{"ResetType":"ForceOff"}' >/dev/null
    wait_for_qemu_stopped 30 || return 1
    off_a=$(serial_size a); off_b=$(serial_size b)
    redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset '{"ResetType":"On"}' >/dev/null
    wait_for_qemu_running 30 || return 1

    # The same connections get the new boot, and input still reaches the VM
    serial_wait a "$off_a" 'login: *$' 300 a || return 1
    serial_wait b "$off_b" 'login: *$' 30 || return 1
    assert_equals "connected" "$(serial_status a)" "client a should not have been disconnected" || return 1
    assert_equals "connected" "$(serial_status b)" "client b should not have been disconnected" || return 1
    off_a=$(serial_size a)
    serial_send b "root"
    serial_wait a "$off_a" 'root@[^ ]*:~# *$' 60
}

prepare_serial_disk
start_test_container -v "$SERIAL_VOLUME:/vm" -v "$SERIAL_CACHE:/base:ro" -e "POWER_ON_AT_START=false"
wait_for_redfish_ready 60
# Both clients connect while the VM is off and see it boot
serial_client a
serial_client b
sleep 1
redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset '{"ResetType":"On"}' >/dev/null
wait_for_qemu_running 30

run_test test_serial_qemu_uses_internal_socket
run_test test_serial_two_clients_get_login_prompt
run_test test_serial_command_output_on_both_clients
run_test test_serial_redfish_log
run_test test_serial_survives_power_off_on

stop_test_container
cleanup_serial_disk
