#!/bin/bash
# test_update.sh - Redfish UpdateService (simulated firmware updates):
# SimpleUpdate from an image server (python container on its own docker
# network) and multipart HTTP push update the BMC (right away) and the BIOS
# (at the next boot), broken images and the firmware-image-rejected /
# firmware-update-fail faults fail without changing the versions, the
# versions are kept in a new container (VMEDIA_DIR on a docker volume), and
# sushy's simple_update works (tests/update_sushy.py).

UPDATE_NET="${UPDATE_NET:-qemu-bmc-update-test}"
UPDATE_VOLUME="${UPDATE_VOLUME:-qemu-bmc-update-test}"
IMAGE_SERVER="${IMAGE_SERVER:-qemu-bmc-update-images}"
IMAGE_SERVER_IMAGE="${IMAGE_SERVER_IMAGE:-python:3.12-slim}"
# Set by start_image_server (its address on UPDATE_NET: the name may not
# resolve yet right after docker network connect)
IMAGE_URL=""
FAULT_API_PORT="${FAULT_API_PORT:-8623}"
SUSHY_IMAGE="${SUSHY_IMAGE:-qemu-bmc-sushy-test}"
SUSHY_BASE_IMAGE="${SUSHY_BASE_IMAGE:-python:3.12-slim}"
FW_INVENTORY=/redfish/v1/UpdateService/FirmwareInventory
SIMPLE_UPDATE=/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate
UPDATE_TMP=$(mktemp -d)

# up_json EXPR - prints Python expression EXPR over the JSON on stdin (d)
up_json() {
    python3 -c 'import json,sys
try:
    d = json.load(sys.stdin)
    print(eval(sys.argv[1]))
except Exception as e:
    print("ERROR: %s" % e)' "$1"
}

up_fault() {
    curl -s --max-time "$CMD_TIMEOUT" -X "$1" -H "Content-Type: application/json" ${2:+-d "$2"} \
        "http://${IPMI_HOST}:${FAULT_API_PORT}/faults"
}

# image NAME CONTENT - puts an image on the image server and in UPDATE_TMP
image() {
    printf '%b' "$2" >"$UPDATE_TMP/$1"
    timeout "$CMD_TIMEOUT" docker exec -i "$IMAGE_SERVER" sh -c "cat >/srv/$1" <"$UPDATE_TMP/$1"
}

# post_status PATH ARGS... - POSTs with curl ARGS, prints "<status> <Location>"
# and leaves the body in $UPDATE_TMP/body
post_status() {
    local path="$1"
    shift
    curl -sk --max-time 60 -u "${IPMI_USER}:${IPMI_PASS}" -X POST -D "$UPDATE_TMP/headers" \
        -o "$UPDATE_TMP/body" -w "%{http_code}" "$@" "https://${IPMI_HOST}:${REDFISH_PORT}${path}"
    printf ' %s\n' "$(tr -d '\r' <"$UPDATE_TMP/headers" | sed -n 's/^[Ll]ocation: //p')"
}

simple_update() {
    post_status "$SIMPLE_UPDATE" -H "Content-Type: application/json" -d "$1"
}

# push FILE [UPDATE_PARAMETERS] - multipart HTTP push
push() {
    local params="${2:-}"
    [ -n "$params" ] || params='{}'
    post_status /redfish/v1/UpdateService/upload \
        -F "UpdateParameters=${params};type=application/json" -F "UpdateFile=@$UPDATE_TMP/$1"
}

# wait_task MONITOR - waits for the task monitor to answer 200, prints
# "<TaskState> <TaskStatus> <PercentComplete> <last MessageId>"
wait_task() {
    local i=0 code
    while [ $i -lt 60 ]; do
        code=$(curl -sk --max-time "$CMD_TIMEOUT" -u "${IPMI_USER}:${IPMI_PASS}" -o "$UPDATE_TMP/task" \
            -w "%{http_code}" "https://${IPMI_HOST}:${REDFISH_PORT}$1")
        if [ "$code" = "200" ]; then
            up_json "' '.join([d['TaskState'], d['TaskStatus'], str(d['PercentComplete']), d['Messages'][-1]['MessageId']])" <"$UPDATE_TMP/task"
            return 0
        fi
        if [ "$code" != "202" ]; then
            echo "task monitor $1: HTTP $code"
            return 1
        fi
        sleep 1
        i=$((i + 1))
    done
    echo "task $1 did not finish"
    return 1
}

version() {
    redfish_get "$FW_INVENTORY/$1" | up_json "d['Version']"
}

bios_state() {
    redfish_get "$FW_INVENTORY/BIOS" | up_json "d['Status']['State']"
}

up_reset() {
    redfish_post /redfish/v1/Systems/1/Actions/ComputerSystem.Reset "{\"ResetType\":\"$1\"}" >/dev/null
}

build_sushy_image() {
    if ! docker image inspect "$SUSHY_IMAGE" >/dev/null 2>&1; then
        echo "Building $SUSHY_IMAGE (pip install sushy)"
        printf 'FROM %s\nRUN pip install --no-cache-dir sushy\n' "$SUSHY_BASE_IMAGE" |
            docker build -q -t "$SUSHY_IMAGE" - >/dev/null
    fi
}

sushy_update() {
    timeout 120 docker run --rm --network host -v "$SCRIPT_DIR/update_sushy.py:/update_sushy.py:ro" "$SUSHY_IMAGE" \
        python3 /update_sushy.py "https://${IPMI_HOST}:${REDFISH_PORT}" "$IPMI_USER" "$IPMI_PASS" "$@" 2>&1
}

start_image_server() {
    docker rm -f "$IMAGE_SERVER" >/dev/null 2>&1 || true
    docker network rm "$UPDATE_NET" >/dev/null 2>&1 || true
    docker network create "$UPDATE_NET" >/dev/null
    docker run -d --name "$IMAGE_SERVER" --network "$UPDATE_NET" "$IMAGE_SERVER_IMAGE" \
        sh -c 'mkdir -p /srv && cd /srv && exec python3 -m http.server 8080' >/dev/null
    IMAGE_URL="http://$(docker inspect -f "{{(index .NetworkSettings.Networks \"$UPDATE_NET\").IPAddress}}" "$IMAGE_SERVER"):8080"
}

stop_image_server() {
    docker rm -f "$IMAGE_SERVER" >/dev/null 2>&1 || true
    docker network rm "$UPDATE_NET" >/dev/null 2>&1 || true
}

start_update_container() {
    start_test_container -p "${FAULT_API_PORT}:8623" -e "FAULT_API_ADDR=:8623" \
        -v "$UPDATE_VOLUME:/fw-data" -e "VMEDIA_DIR=/fw-data" \
        -e "BMC_FIRMWARE_VERSION=1.0.0" -e "FIRMWARE_UPDATE_SECONDS=3"
    docker network connect "$UPDATE_NET" "$TEST_CONTAINER"
    wait_for_ipmi_ready 60
    wait_for_redfish_ready 60
}

test_update_inventory() {
    assert_equals "/redfish/v1/UpdateService" "$(redfish_get /redfish/v1 | up_json "d['UpdateService']['@odata.id']")" || return 1
    assert_equals "/redfish/v1/UpdateService/upload" \
        "$(redfish_get /redfish/v1/UpdateService | up_json "d['MultipartHttpPushUri']")" || return 1
    assert_equals "2" "$(redfish_get "$FW_INVENTORY" | up_json "d['Members@odata.count']")" || return 1
    assert_equals "1.0.0" "$(version BMC)" "BMC_FIRMWARE_VERSION" || return 1
    assert_equals "QBMC-BIOS-1.0.0" "$(version BIOS)" "default BIOS version" || return 1
    assert_equals "1.0.0" "$(redfish_get /redfish/v1/Managers/1 | up_json "d['FirmwareVersion']")" || return 1
    assert_equals "QBMC-BIOS-1.0.0" "$(redfish_get /redfish/v1/Systems/1 | up_json "d['BiosVersion']")"
}

test_update_simple_update_bmc() {
    image bmc-2.img 'QBMC-FW BMC 2.0.0\n\x00\x01payload'
    local out monitor
    out=$(simple_update "{\"ImageURI\":\"$IMAGE_URL/bmc-2.img\",\"TransferProtocol\":\"HTTP\"}")
    assert_equals "202" "${out%% *}" "SimpleUpdate: $out $(cat "$UPDATE_TMP/body")" || return 1
    monitor=${out#* }
    assert_contains "$monitor" "/redfish/v1/TaskService/TaskMonitors/" || return 1
    # Running while the task monitor answers 202
    assert_equals "202" "$(redfish_get_status "$monitor")" || return 1
    assert_equals "Completed OK 100 Update.1.0.UpdateSuccessful" "$(wait_task "$monitor")" || return 1
    assert_equals "2.0.0" "$(version BMC)" || return 1
    assert_equals "2.0.0" "$(redfish_get /redfish/v1/Managers/1 | up_json "d['FirmwareVersion']")"
}

test_update_simple_update_bios_at_boot() {
    image bios-2.img 'QBMC-FW BIOS 2.0.0\n'
    local out
    out=$(simple_update "{\"ImageURI\":\"$IMAGE_URL/bios-2.img\",\"Targets\":[\"$FW_INVENTORY/BIOS\"]}")
    assert_equals "202" "${out%% *}" "SimpleUpdate: $out $(cat "$UPDATE_TMP/body")" || return 1
    assert_equals "Completed OK 100 Update.1.0.AwaitToActivate" "$(wait_task "${out#* }")" || return 1
    assert_contains "$(cat "$UPDATE_TMP/task")" "next system boot" || return 1
    assert_equals "QBMC-BIOS-1.0.0" "$(version BIOS)" "pending until the next boot" || return 1
    assert_equals "StandbyOffline" "$(bios_state)" || return 1

    up_reset ForceOff
    wait_for_qemu_stopped 30 || return 1
    assert_equals "QBMC-BIOS-1.0.0" "$(version BIOS)" "not applied while off" || return 1
    up_reset On
    wait_for_qemu_running 30 || return 1
    assert_equals "2.0.0" "$(version BIOS)" || return 1
    assert_equals "Enabled" "$(bios_state)" || return 1
    assert_equals "2.0.0" "$(redfish_get /redfish/v1/Systems/1 | up_json "d['BiosVersion']")"
}

test_update_multipart_bmc() {
    image bmc-3.img 'QBMC-FW BMC 3.0.0\nmultipart payload'
    local out
    out=$(push bmc-3.img "{\"Targets\":[\"$FW_INVENTORY/BMC\"],\"@Redfish.OperationApplyTime\":\"Immediate\"}")
    assert_equals "202" "${out%% *}" "push: $out $(cat "$UPDATE_TMP/body")" || return 1
    assert_equals "Completed OK 100 Update.1.0.UpdateSuccessful" "$(wait_task "${out#* }")" || return 1
    assert_equals "3.0.0" "$(version BMC)"
}

test_update_multipart_bios_at_restart() {
    image bios-3.img 'QBMC-FW BIOS 3.0.0\n'
    local out
    out=$(push bios-3.img)
    assert_equals "202" "${out%% *}" "push: $out $(cat "$UPDATE_TMP/body")" || return 1
    assert_equals "Completed OK 100 Update.1.0.AwaitToActivate" "$(wait_task "${out#* }")" || return 1
    assert_equals "2.0.0 StandbyOffline" "$(version BIOS) $(bios_state)" || return 1
    up_reset ForceRestart
    assert_equals "3.0.0 Enabled" "$(version BIOS) $(bios_state)"
}

test_update_broken_images() {
    image garbage.img 'MZ\x90\x00 not a firmware image'
    image short.img 'QBMC-FW BIOS\n'
    image bmc-9.img 'QBMC-FW BMC 9.9.9\n'
    local out
    out=$(simple_update "{\"ImageURI\":\"$IMAGE_URL/garbage.img\"}")
    assert_equals "400" "${out%% *}" "SimpleUpdate garbage" || return 1
    assert_equals "Update.1.0.VerificationFailed" \
        "$(up_json "d['error']['@Message.ExtendedInfo'][0]['MessageId']" <"$UPDATE_TMP/body")" || return 1
    out=$(simple_update "{\"ImageURI\":\"$IMAGE_URL/short.img\"}")
    assert_equals "400" "${out%% *}" "SimpleUpdate short" || return 1
    out=$(simple_update "{\"ImageURI\":\"$IMAGE_URL/missing.img\"}")
    assert_equals "400" "${out%% *}" "SimpleUpdate missing" || return 1
    out=$(push garbage.img)
    assert_equals "400" "${out%% *}" "push garbage" || return 1
    # The image is for the BMC, the Targets say BIOS
    out=$(push bmc-9.img "{\"Targets\":[\"$FW_INVENTORY/BIOS\"]}")
    assert_equals "400" "${out%% *}" "push target mismatch" || return 1
    assert_equals "3.0.0 3.0.0 Enabled" "$(version BMC) $(version BIOS) $(bios_state)"
}

test_update_fault_image_rejected() {
    up_fault POST '{"kind":"firmware-image-rejected","target":"BIOS","count":1}' >/dev/null
    image bios-4.img 'QBMC-FW BIOS 4.0.0\n'
    local out
    out=$(push bios-4.img)
    assert_equals "400" "${out%% *}" "rejected by the fault" || return 1
    assert_equals "Update.1.0.VerificationFailed" \
        "$(up_json "d['error']['@Message.ExtendedInfo'][0]['MessageId']" <"$UPDATE_TMP/body")" || return 1
    assert_equals "3.0.0 Enabled" "$(version BIOS) $(bios_state)" || return 1
    assert_equals "[]" "$(up_fault GET)" "count used up"
}

test_update_fault_update_fail() {
    up_fault POST '{"kind":"firmware-update-fail","target":"BMC","atPercent":40,"count":1}' >/dev/null
    image bmc-4.img 'QBMC-FW BMC 4.0.0\n'
    local out
    out=$(simple_update "{\"ImageURI\":\"$IMAGE_URL/bmc-4.img\"}")
    assert_equals "202" "${out%% *}" || return 1
    assert_equals "Exception Critical 40 Update.1.0.ApplyFailed" "$(wait_task "${out#* }")" || return 1
    assert_equals "3.0.0" "$(version BMC)" || return 1

    up_fault POST '{"kind":"firmware-update-fail","target":"BIOS","count":1}' >/dev/null
    out=$(push bios-4.img)
    assert_equals "202" "${out%% *}" || return 1
    assert_equals "Exception Critical 50 Update.1.0.ApplyFailed" "$(wait_task "${out#* }")" || return 1
    assert_equals "3.0.0 Enabled" "$(version BIOS) $(bios_state)"
}

test_update_sushy_simple_update() {
    image bmc-5.img 'QBMC-FW BMC 5.0.0\n'
    local out
    out=$(sushy_update simple_update "$IMAGE_URL/bmc-5.img" "$FW_INVENTORY/BMC")
    assert_equals "Completed OK" "$(echo "$out" | up_json "d['state'] + ' ' + d['status']")" "sushy: $out" || return 1
    assert_equals "{'BMC': '5.0.0', 'BIOS': '3.0.0'}" "$(sushy_update inventory | up_json "d")"
}

test_update_kept_in_new_container() {
    # A BIOS update pending when the container goes away
    image bios-6.img 'QBMC-FW BIOS 6.0.0\n'
    local out
    out=$(push bios-6.img)
    assert_equals "Completed OK 100 Update.1.0.AwaitToActivate" "$(wait_task "${out#* }")" || return 1
    start_update_container >/dev/null || return 1
    # The saved versions win over BMC_FIRMWARE_VERSION; the pending BIOS
    # applied when the new container powered the VM on
    assert_equals "5.0.0 6.0.0 Enabled" "$(version BMC) $(version BIOS) $(bios_state)" || return 1
    assert_equals "5.0.0" "$(redfish_get /redfish/v1/Managers/1 | up_json "d['FirmwareVersion']")" || return 1
    assert_contains "$(container_exec cat /fw-data/firmware.json)" '"BMC": "5.0.0"'
}

build_sushy_image
docker volume rm -f "$UPDATE_VOLUME" >/dev/null 2>&1 || true
start_image_server
start_update_container

run_test test_update_inventory
run_test test_update_simple_update_bmc
run_test test_update_simple_update_bios_at_boot
run_test test_update_multipart_bmc
run_test test_update_multipart_bios_at_restart
run_test test_update_broken_images
run_test test_update_fault_image_rejected
run_test test_update_fault_update_fail
run_test test_update_sushy_simple_update
run_test test_update_kept_in_new_container

stop_test_container >/dev/null
stop_image_server
docker volume rm -f "$UPDATE_VOLUME" >/dev/null 2>&1 || true
rm -rf "$UPDATE_TMP"
