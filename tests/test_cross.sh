#!/bin/bash
# test_cross.sh - Cross-protocol consistency tests (IPMI <-> Redfish)

test_ipmi_off_redfish_verify() {
    # Ensure power is on
    ipmi_cmd power on >/dev/null 2>&1
    wait_for_power_state "on" 30

    # Power off via IPMI
    ipmi_cmd power off >/dev/null 2>&1
    wait_for_power_state "off" 15

    # Verify via Redfish
    local result
    result=$(redfish_get "/redfish/v1/Systems/1")
    assert_contains "$result" '"PowerState":"Off"' "Redfish should show PowerState=Off after IPMI power off"
}

test_ipmi_on_redfish_verify() {
    # Power on via IPMI
    ipmi_cmd power on >/dev/null 2>&1
    wait_for_power_state "on" 30

    # Verify via Redfish
    local result
    result=$(redfish_get "/redfish/v1/Systems/1")
    assert_contains "$result" '"PowerState":"On"' "Redfish should show PowerState=On after IPMI power on"
}

test_redfish_off_ipmi_verify() {
    # Power off via Redfish
    redfish_post "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset" \
        '{"ResetType":"ForceOff"}' >/dev/null 2>&1
    wait_for_power_state "off" 15

    # Verify via IPMI
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "off" "IPMI should show Off after Redfish ForceOff"
}

test_redfish_on_ipmi_verify() {
    # Power on via Redfish
    redfish_post "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset" \
        '{"ResetType":"On"}' >/dev/null 2>&1
    wait_for_power_state "on" 30

    # Verify via IPMI
    local state
    state=$(ipmi_cmd power status 2>/dev/null)
    assert_contains "$state" "on" "IPMI should show On after Redfish On"
}

test_ipmi_bootdev_redfish_verify() {
    # Set boot device via IPMI
    ipmi_cmd chassis bootdev pxe >/dev/null 2>&1

    # Verify via Redfish
    local result
    result=$(redfish_get "/redfish/v1/Systems/1")
    assert_contains "$result" "Pxe" "Redfish should show BootSourceOverrideTarget=Pxe after IPMI bootdev pxe"
}

test_redfish_bootdev_ipmi_verify() {
    # Set boot device via Redfish
    redfish_patch "/redfish/v1/Systems/1" \
        '{"Boot":{"BootSourceOverrideTarget":"Cd"}}' >/dev/null 2>&1

    # Verify via IPMI
    local result
    result=$(ipmi_cmd chassis bootparam get 5 2>&1)
    assert_contains "$result" "CD" "IPMI should show CD-ROM boot after Redfish Cd target"
}

# --- Run tests ---
start_test_container
wait_for_ipmi_ready 60
wait_for_redfish_ready 60

run_test test_ipmi_off_redfish_verify
run_test test_ipmi_on_redfish_verify
run_test test_redfish_off_ipmi_verify
run_test test_redfish_on_ipmi_verify
run_test test_ipmi_bootdev_redfish_verify
run_test test_redfish_bootdev_ipmi_verify
