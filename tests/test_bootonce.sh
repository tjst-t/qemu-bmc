#!/bin/bash
# test_bootonce.sh - BootSourceOverrideEnabled=Once lasts one boot. A Debian
# guest disk and a VirtualMedia ISO that prints QBMC-ONCE-CD-BOOT on the
# serial console and reboots from inside the guest: Once + Cd boots the
# ISO, and the next boot is from the disk (the default boot order) although
# the ISO stays inserted - both when the guest reboots itself (QEMU keeps
# running) and when the VM is powered off and on instead. UEFI and BIOS.

BOOTONCE_VOLUME="${BOOTONCE_VOLUME:-qemu-bmc-bootonce-test-vm}"
ONCE_MARK="QBMC-ONCE-CD-BOOT"

# bootonce_check MODE HOW - HOW: reboot (by the guest) or poweroff (ForceOff
# and On right after the ISO booted)
bootonce_check() {
    local mode="$1" how="$2" pid1 pid2 off out boot media
    prepare_debian_disk "$BOOTONCE_VOLUME"
    start_test_container -v "$BOOTONCE_VOLUME:/vm" -v "$DEBIAN_CACHE:/base:ro" \
        -e "POWER_ON_AT_START=false" -e "VM_BOOT_MODE=$mode"
    wait_for_redfish_ready 60 || return 1
    serial_client a
    sleep 1

    redfish_post /redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia \
        "{\"Image\":\"/base/$ONCE_ISO_FILE\"}" >/dev/null
    redfish_patch /redfish/v1/Systems/1 \
        '{"Boot":{"BootSourceOverrideTarget":"Cd","BootSourceOverrideEnabled":"Once"}}' >/dev/null
    redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset '{"ResetType":"On"}' >/dev/null
    wait_for_qemu_running 30 || return 1
    pid1=$(get_qemu_pid)

    # First boot: the ISO
    serial_wait a 0 "$ONCE_MARK" 180 || return 1
    boot=$(redfish_get /redfish/v1/Systems/1)
    assert_contains "$boot" '"BootSourceOverrideEnabled":"Disabled"' "Once should be consumed by the boot" || return 1

    if [ "$how" = poweroff ]; then
        redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset '{"ResetType":"ForceOff"}' >/dev/null
        wait_for_qemu_stopped 30 || return 1
        off=$(serial_size a)
        redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset '{"ResetType":"On"}' >/dev/null
        wait_for_qemu_running 30 || return 1
    else
        # The ISO reboots the VM from inside the guest
        off=$(container_exec bash -c "grep -ab -o '$ONCE_MARK' /tmp/a.log | head -1 | cut -d: -f1")
        off=$(( off + ${#ONCE_MARK} ))
    fi

    # Second boot: the disk, not the ISO again
    serial_wait a "$off" "login: *\$|$ONCE_MARK" 300 a || return 1
    out=$(serial_since a "$off")
    assert_not_contains "$out" "$ONCE_MARK" "the next boot ($how) should not boot the CD again" || return 1
    assert_contains "$out" "login:" "the next boot ($how) should boot the disk" || return 1
    if [ "$how" = reboot ]; then
        pid2=$(get_qemu_pid)
        assert_equals "$pid1" "$pid2" "the reboot should be the guest's own (same QEMU process)" || return 1
    fi

    media=$(redfish_get /redfish/v1/Managers/1/VirtualMedia/CD1)
    assert_contains "$media" '"Inserted":true' "the ISO should still be inserted"
}

# bootonce_fallback_check HOW - UEFI, empty disk, the ISO stays inserted:
# after the Once boot, the next boot (HOW: reboot by the guest, or poweroff
# and on) cannot boot the disk and must fall back to the CD, as with a
# disk, CD, network boot order on a real machine (not stop in the UEFI
# shell)
bootonce_fallback_check() {
    local how="$1" off out
    docker volume rm -f "$BOOTONCE_VOLUME-empty" >/dev/null 2>&1
    start_test_container -v "$BOOTONCE_VOLUME-empty:/vm" -v "$DEBIAN_CACHE:/base:ro" \
        -e "POWER_ON_AT_START=false" -e "VM_BOOT_MODE=uefi" -e "VM_DISK_SIZE=1G"
    wait_for_redfish_ready 60 || return 1
    serial_client a
    sleep 1

    redfish_post /redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia \
        "{\"Image\":\"/base/$ONCE_ISO_FILE\"}" >/dev/null
    redfish_patch /redfish/v1/Systems/1 \
        '{"Boot":{"BootSourceOverrideTarget":"Cd","BootSourceOverrideEnabled":"Once"}}' >/dev/null
    redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset '{"ResetType":"On"}' >/dev/null
    wait_for_qemu_running 30 || return 1
    serial_wait a 0 "$ONCE_MARK" 180 || return 1

    if [ "$how" = poweroff ]; then
        redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset '{"ResetType":"ForceOff"}' >/dev/null
        wait_for_qemu_stopped 30 || return 1
        off=$(serial_size a)
        redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset '{"ResetType":"On"}' >/dev/null
        wait_for_qemu_running 30 || return 1
    else
        off=$(container_exec bash -c "grep -ab -o '$ONCE_MARK' /tmp/a.log | head -1 | cut -d: -f1")
        off=$(( off + ${#ONCE_MARK} ))
    fi
    serial_wait a "$off" "$ONCE_MARK|Shell>" 240 || return 1
    out=$(serial_since a "$off")
    assert_contains "$out" "$ONCE_MARK" "the next boot ($how) should fall back to the CD (empty disk)"
    local rc=$?
    stop_test_container >/dev/null
    docker volume rm -f "$BOOTONCE_VOLUME-empty" >/dev/null 2>&1
    return $rc
}

test_bootonce_empty_disk_falls_back_to_cd_reboot_uefi() {
    bootonce_fallback_check reboot
}

test_bootonce_empty_disk_falls_back_to_cd_power_off_on_uefi() {
    bootonce_fallback_check poweroff
}

test_bootonce_guest_reboot_uefi() {
    bootonce_check uefi reboot
}

test_bootonce_guest_reboot_bios() {
    bootonce_check bios reboot
}

test_bootonce_power_off_on_uefi() {
    bootonce_check uefi poweroff
}

test_bootonce_power_off_on_bios() {
    bootonce_check bios poweroff
}

prepare_once_iso || echo "could not build the test ISO"
run_test test_bootonce_guest_reboot_uefi
run_test test_bootonce_guest_reboot_bios
run_test test_bootonce_power_off_on_uefi
run_test test_bootonce_power_off_on_bios
run_test test_bootonce_empty_disk_falls_back_to_cd_reboot_uefi
run_test test_bootonce_empty_disk_falls_back_to_cd_power_off_on_uefi

stop_test_container
cleanup_debian_disk "$BOOTONCE_VOLUME"
