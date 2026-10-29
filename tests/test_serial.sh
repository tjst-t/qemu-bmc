#!/bin/bash
# test_serial.sh - Serial console multiplexing: a Debian guest (nocloud cloud
# image, root without password, getty on ttyS0) boots from a qcow2 overlay;
# two clients inside the container stay connected to SERIAL_ADDR
# (localhost:9002) at the same time, both get the login prompt and the
# output of commands typed on either, and both stay connected across a QEMU
# power off/on. The Redfish SerialConsole log returns the same output.

SERIAL_VOLUME="${SERIAL_VOLUME:-qemu-bmc-serial-test-vm}"
SERIAL_LOG_PATH=/redfish/v1/Systems/1/LogServices/SerialConsole/Entries/1/attachment

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

prepare_debian_disk "$SERIAL_VOLUME"
start_test_container -v "$SERIAL_VOLUME:/vm" -v "$DEBIAN_CACHE:/base:ro" -e "POWER_ON_AT_START=false"
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
cleanup_debian_disk "$SERIAL_VOLUME"
