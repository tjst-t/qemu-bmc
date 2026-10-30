#!/bin/bash
# test_actionlog.sh - Action log: one "action:" line per write command
# (Redfish and IPMI), none for reads

ACTIONLOG_FAULT_PORT="${ACTIONLOG_FAULT_PORT:-8623}"

# action_lines - prints the action lines logged so far
action_lines() {
    docker logs "$TEST_CONTAINER" 2>&1 | grep -E '(^| )action: kind=' || true
}

# action_count - prints how many action lines were logged so far
action_count() {
    action_lines | wc -l | tr -d ' '
}

# new_actions N - prints the action lines after the first N
new_actions() {
    action_lines | tail -n +"$(($1 + 1))"
}

# expect_one_action BEFORE REGEX - exactly one action line was logged
# since BEFORE lines, and it matches REGEX
expect_one_action() {
    local before="$1" regex="$2" lines n
    sleep 0.5
    lines=$(new_actions "$before")
    n=$(printf '%s' "$lines" | grep -c . || true)
    assert_equals "1" "$n" "expected exactly one new action line, got: $lines" || return 1
    if ! printf '%s\n' "$lines" | grep -Eq "$regex"; then
        echo "action line does not match /$regex/: $lines" >&2
        return 1
    fi
}

redfish_status() {
    local method="$1" path="$2" data="$3"
    curl -sk --max-time "$CMD_TIMEOUT" -u "${IPMI_USER}:${IPMI_PASS}" -o /dev/null -w "%{http_code}" \
        -X "$method" -H "Content-Type: application/json" -d "$data" \
        "https://${IPMI_HOST}:${REDFISH_PORT}${path}"
}

actionlog_fault() {
    curl -s --max-time "$CMD_TIMEOUT" -X "$1" -H "Content-Type: application/json" ${2:+-d "$2"} \
        "http://${IPMI_HOST}:${ACTIONLOG_FAULT_PORT}/faults${3:-}"
}

RESET_PATH=/redfish/v1/Systems/1/Actions/ComputerSystem.Reset
IP='[0-9a-f.:]+'

test_actionlog_reads_log_nothing() {
    # Power on at start is no command; reads are no commands
    redfish_get /redfish/v1/Systems/1 >/dev/null
    redfish_get /redfish/v1/Managers/1/VirtualMedia/CD1 >/dev/null
    redfish_get /redfish/v1/Systems/1/Bios >/dev/null
    ipmi_cmd power status >/dev/null 2>&1
    ipmi_cmd chassis bootparam get 5 >/dev/null 2>&1
    ipmi_cmd mc info >/dev/null 2>&1
    sleep 0.5
    assert_equals "0" "$(action_count)" "no action line expected: $(action_lines)"
}

test_actionlog_redfish_reset() {
    local before
    before=$(action_count)
    assert_equals "204" "$(redfish_status POST $RESET_PATH '{"ResetType":"ForceRestart"}')" || return 1
    expect_one_action "$before" "action: kind=power via=redfish remote=$IP user=${IPMI_USER} reset_type=ForceRestart result=accepted\$"
}

test_actionlog_redfish_reset_rejected() {
    local before
    before=$(action_count)
    assert_equals "400" "$(redfish_status POST $RESET_PATH '{"ResetType":"Bogus"}')" || return 1
    expect_one_action "$before" "kind=power via=redfish .* reset_type=Bogus result=rejected status=400 reason=ActionParameterNotSupported\$"
}

test_actionlog_ipmi_power() {
    local before
    before=$(action_count)
    ipmi_cmd power reset >/dev/null 2>&1
    assert_success $? "ipmitool power reset" || return 1
    expect_one_action "$before" "action: kind=power via=ipmi remote=$IP user=${IPMI_USER} reset_type=ForceRestart result=accepted\$"
}

test_actionlog_boot_override() {
    local before
    before=$(action_count)
    assert_equals "200" "$(redfish_status PATCH /redfish/v1/Systems/1 \
        '{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":"Pxe"}}')" || return 1
    expect_one_action "$before" "kind=boot via=redfish .* enabled=Once target=Pxe mode=- result=accepted\$" || return 1

    before=$(action_count)
    ipmi_cmd chassis bootdev disk options=persistent >/dev/null 2>&1
    assert_success $? "ipmitool chassis bootdev disk" || return 1
    expect_one_action "$before" "kind=boot via=ipmi .* enabled=Continuous target=Hdd mode=Legacy result=accepted\$" || return 1

    # Back to no override
    redfish_status PATCH /redfish/v1/Systems/1 '{"Boot":{"BootSourceOverrideEnabled":"Disabled"}}' >/dev/null
}

test_actionlog_virtual_media() {
    local before vm=/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia
    before=$(action_count)
    redfish_status POST "$vm.InsertMedia" '{"Image":"http://user:secret@127.0.0.1:1/none.iso"}' >/dev/null
    expect_one_action "$before" "kind=vmedia-insert via=redfish .* device=CD1 image=http://user:xxxxx@127.0.0.1:1/none.iso result=" || return 1
    assert_not_contains "$(action_lines)" "secret" "the password must not be logged" || return 1

    before=$(action_count)
    assert_equals "204" "$(redfish_status POST "$vm.EjectMedia" '{}')" || return 1
    expect_one_action "$before" "kind=vmedia-eject via=redfish .* device=CD1 result=accepted\$"
}

test_actionlog_bios() {
    local before
    before=$(action_count)
    assert_equals "200" "$(redfish_status PATCH /redfish/v1/Systems/1/Bios/Settings '{"Attributes":{"NumLock":"Off"}}')" || return 1
    expect_one_action "$before" "kind=bios via=redfish .* op=settings attributes=NumLock result=accepted\$"
}

test_actionlog_faults() {
    local before id
    # power-action-ignored: 204 for the client, result=fault in the log
    id=$(actionlog_fault POST '{"kind":"power-action-ignored","action":"ForceOff","count":1}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
    before=$(action_count)
    assert_equals "204" "$(redfish_status POST $RESET_PATH '{"ResetType":"ForceOff"}')" || return 1
    expect_one_action "$before" "kind=power via=redfish .* reset_type=ForceOff result=fault fault=$id\$" || return 1

    # ipmi-error on Chassis Control: the command never runs
    id=$(actionlog_fault POST '{"kind":"ipmi-error","netfn":0,"cmd":2,"completionCode":"0xc3","count":1}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
    before=$(action_count)
    ipmi_cmd -R 1 power reset >/dev/null 2>&1
    expect_one_action "$before" "kind=power via=ipmi .* reset_type=ForceRestart result=fault cc=0xc3 fault=$id\$" || return 1

    # redfish-error on the Reset action: logged before the error response
    id=$(actionlog_fault POST '{"kind":"redfish-error","status":503,"path":"'$RESET_PATH'","count":1}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
    before=$(action_count)
    assert_equals "503" "$(redfish_status POST $RESET_PATH '{"ResetType":"On"}')" || return 1
    expect_one_action "$before" "kind=power via=redfish .* reset_type=On result=fault fault=$id reason=redfish-error\$"
}

start_test_container -p "${ACTIONLOG_FAULT_PORT}:8623" -e "FAULT_API_ADDR=:8623"
wait_for_ipmi_ready 60
wait_for_redfish_ready 60

run_test test_actionlog_reads_log_nothing
run_test test_actionlog_redfish_reset
run_test test_actionlog_redfish_reset_rejected
run_test test_actionlog_ipmi_power
run_test test_actionlog_boot_override
run_test test_actionlog_virtual_media
run_test test_actionlog_bios
run_test test_actionlog_faults
