#!/bin/bash
# test_bios.sh - Redfish Bios with sushy (python container on the host
# network, tests/bios_sushy.py): attributes, the attribute registry,
# set_attributes pending until the VM is powered off and on / restarted,
# invalid values, reset_bios, BIOS_ATTRIBUTES defaults, and the values kept
# in a new container (VMEDIA_DIR on a docker volume).

BIOS_VOLUME="${BIOS_VOLUME:-qemu-bmc-bios-test}"
SUSHY_IMAGE="${SUSHY_IMAGE:-qemu-bmc-sushy-test}"
SUSHY_BASE_IMAGE="${SUSHY_BASE_IMAGE:-python:3.12-slim}"

# The sushy client image (python + sushy), built once
build_sushy_image() {
    if ! docker image inspect "$SUSHY_IMAGE" >/dev/null 2>&1; then
        echo "Building $SUSHY_IMAGE (pip install sushy)"
        printf 'FROM %s\nRUN pip install --no-cache-dir sushy\n' "$SUSHY_BASE_IMAGE" |
            docker build -q -t "$SUSHY_IMAGE" - >/dev/null
    fi
}

# sushy COMMAND [ARG] - runs tests/bios_sushy.py, prints its JSON
sushy() {
    timeout 60 docker run --rm --network host -v "$SCRIPT_DIR/bios_sushy.py:/bios_sushy.py:ro" "$SUSHY_IMAGE" \
        python3 /bios_sushy.py "https://${IPMI_HOST}:${REDFISH_PORT}" "$IPMI_USER" "$IPMI_PASS" "$@" 2>&1
}

# bios_json EXPR - prints Python expression EXPR over the JSON on stdin (d)
bios_json() {
    python3 -c 'import json,sys
try:
    d = json.load(sys.stdin)
    print(eval(sys.argv[1]))
except Exception as e:
    print("ERROR: %s" % e)' "$1"
}

bios_reset() {
    redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset "{\"ResetType\":\"$1\"}" >/dev/null
}

start_bios_container() {
    start_test_container -v "$BIOS_VOLUME:/bios-data" -e "VMEDIA_DIR=/bios-data" \
        -e 'BIOS_ATTRIBUTES={"PowerProfile":"Balanced","AssetTag":"factory"}'
    wait_for_ipmi_ready 60
    wait_for_redfish_ready 60
}

test_bios_link() {
    assert_equals "/redfish/v1/Systems/1/Bios" \
        "$(redfish_get /redfish/v1/Systems/1 | bios_json "d['Bios']['@odata.id']")" || return 1
    assert_equals "/redfish/v1/Registries" "$(redfish_get /redfish/v1 | bios_json "d['Registries']['@odata.id']")"
}

test_bios_attributes() {
    local attrs
    attrs=$(sushy attributes)
    assert_equals "Enabled" "$(echo "$attrs" | bios_json "d['ProcVirtualization']")" "attributes: $attrs" || return 1
    assert_equals "Disabled" "$(echo "$attrs" | bios_json "d['SriovGlobalEnable']")" || return 1
    assert_equals "Bios" "$(echo "$attrs" | bios_json "d['BootMode']")" "BootMode reflects VM_BOOT_MODE=bios" || return 1
    assert_equals "Disabled" "$(echo "$attrs" | bios_json "d['SecureBoot']")" || return 1
    # BIOS_ATTRIBUTES defaults
    assert_equals "Balanced" "$(echo "$attrs" | bios_json "d['PowerProfile']")" || return 1
    assert_equals "factory" "$(echo "$attrs" | bios_json "d['AssetTag']")" || return 1
    assert_equals "['OnReset']" "$(sushy apply_times | bios_json "d")"
}

test_bios_registry() {
    local reg
    reg=$(sushy registry)
    assert_equals "BiosAttributeRegistry.1.0.0" "$(echo "$reg" | bios_json "d['id']")" "registry: $reg" || return 1
    assert_equals "Enumeration ['Enabled', 'Disabled'] False" \
        "$(echo "$reg" | bios_json "' '.join(str(d['attributes']['ProcVirtualization'][k]) for k in ('type', 'values', 'read_only'))")" || return 1
    assert_equals "True" "$(echo "$reg" | bios_json "d['attributes']['BootMode']['read_only']")" || return 1
    assert_equals "Balanced" "$(echo "$reg" | bios_json "d['attributes']['PowerProfile']['default']")" || return 1
    assert_equals "String" "$(echo "$reg" | bios_json "d['attributes']['AssetTag']['type']")"
}

test_bios_invalid() {
    local out
    for attrs in '{"NoSuchAttribute":"x"}' '{"ProcVirtualization":"Maybe"}' '{"ProcVirtualization":1}' '{"BootMode":"Uefi"}'; do
        out=$(sushy set "$attrs")
        assert_equals "400" "$(echo "$out" | bios_json "d['error']")" "set $attrs: $out" || return 1
    done
    out=$(sushy set '{"ProcVirtualization":"Maybe"}')
    assert_contains "$out" "PropertyValueNotInList" || return 1
    assert_equals "{}" "$(sushy pending)" "nothing pending after invalid requests"
}

test_bios_set_applies_at_power_on() {
    local out
    out=$(sushy set '{"ProcVirtualization":"Disabled","AssetTag":"rack-42"}')
    assert_equals "True" "$(echo "$out" | bios_json "d['ok']")" "set: $out" || return 1
    assert_equals "Disabled rack-42" "$(sushy pending | bios_json "d['ProcVirtualization'] + ' ' + d['AssetTag']")" || return 1
    assert_equals "Enabled" "$(sushy attributes | bios_json "d['ProcVirtualization']")" "pending until the next boot" || return 1

    bios_reset ForceOff
    wait_for_qemu_stopped 30 || return 1
    assert_equals "Enabled" "$(sushy attributes | bios_json "d['ProcVirtualization']")" "not applied while off" || return 1
    bios_reset On
    wait_for_qemu_running 30 || return 1
    assert_equals "Disabled rack-42" "$(sushy attributes | bios_json "d['ProcVirtualization'] + ' ' + d['AssetTag']")" || return 1
    assert_equals "{}" "$(sushy pending)"
}

test_bios_set_applies_at_restart() {
    sushy set '{"NumLock":"Off"}' >/dev/null
    bios_reset ForceRestart
    assert_equals "Off" "$(sushy attributes | bios_json "d['NumLock']")" || return 1
    assert_equals "{}" "$(sushy pending)"
}

test_bios_kept_in_new_container() {
    sushy set '{"BootRetry":"Disabled"}' >/dev/null
    # A new container on the same volume
    start_bios_container >/dev/null || return 1
    local attrs
    attrs=$(sushy attributes)
    assert_equals "Disabled Off rack-42" \
        "$(echo "$attrs" | bios_json "d['ProcVirtualization'] + ' ' + d['NumLock'] + ' ' + d['AssetTag']")" "attributes: $attrs" || return 1
    # The pending setting was kept too; POWER_ON_AT_START booted the VM, so
    # it applied
    assert_equals "Disabled" "$(echo "$attrs" | bios_json "d['BootRetry']")" || return 1
    assert_contains "$(container_exec cat /bios-data/bios-settings.json)" '"ProcVirtualization": "Disabled"'
}

test_bios_reset_bios() {
    assert_equals "True" "$(sushy reset | bios_json "d['ok']")" || return 1
    assert_equals "True" "$(redfish_get /redfish/v1/Systems/1/Bios | bios_json "d['ResetBiosToDefaultsPending']")" || return 1
    assert_equals "Disabled" "$(sushy attributes | bios_json "d['ProcVirtualization']")" "not before the next boot" || return 1
    bios_reset PowerCycle
    wait_for_qemu_running 30 || return 1
    local attrs
    attrs=$(sushy attributes)
    assert_equals "Enabled On Enabled Balanced factory" \
        "$(echo "$attrs" | bios_json "' '.join(d[k] for k in ('ProcVirtualization', 'NumLock', 'BootRetry', 'PowerProfile', 'AssetTag'))")" \
        "defaults (BIOS_ATTRIBUTES): $attrs"
}

build_sushy_image
docker volume rm -f "$BIOS_VOLUME" >/dev/null 2>&1 || true
start_bios_container

run_test test_bios_link
run_test test_bios_attributes
run_test test_bios_registry
run_test test_bios_invalid
run_test test_bios_set_applies_at_power_on
run_test test_bios_set_applies_at_restart
run_test test_bios_kept_in_new_container
run_test test_bios_reset_bios

stop_test_container >/dev/null
docker volume rm -f "$BIOS_VOLUME" >/dev/null 2>&1 || true
