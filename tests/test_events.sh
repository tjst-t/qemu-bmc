#!/bin/bash
# test_events.sh - Redfish EventService tests: an event receiver (python
# container on its own docker network) subscribes, the power is switched off
# and on via IPMI and Redfish, and the events arrive; the SSE stream is read
# with curl -N.

EVENT_NET="${EVENT_NET:-qemu-bmc-events-test}"
EVENT_RECEIVER="${EVENT_RECEIVER:-qemu-bmc-events-receiver}"
EVENT_RECEIVER_IMAGE="${EVENT_RECEIVER_IMAGE:-python:3.12-slim}"
EVENT_LOG=/tmp/events.jsonl

# The receiver appends every POSTed body (one JSON per line) to EVENT_LOG and
# answers 204; X-Token is recorded as "_token"
RECEIVER_PY='
import http.server, json
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        body["_token"] = self.headers.get("X-Token")
        body["_content_type"] = self.headers.get("Content-Type")
        with open("/tmp/events.jsonl", "a") as f:
            f.write(json.dumps(body) + "\n")
        self.send_response(204)
        self.end_headers()
    def log_message(self, *a):
        pass
http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()
'

# ev_json EXPR - prints Python expression EXPR over the JSON on stdin (d)
ev_json() {
    python3 -c 'import json,sys
try:
    d = json.load(sys.stdin)
    print(eval(sys.argv[1]))
except Exception as e:
    print("ERROR: %s" % e)' "$1"
}

start_event_receiver() {
    docker rm -f "$EVENT_RECEIVER" >/dev/null 2>&1 || true
    docker network rm "$EVENT_NET" >/dev/null 2>&1 || true
    docker network create "$EVENT_NET" >/dev/null
    docker run -d --name "$EVENT_RECEIVER" --network "$EVENT_NET" \
        "$EVENT_RECEIVER_IMAGE" python3 -u -c "$RECEIVER_PY" >/dev/null
    docker network connect "$EVENT_NET" "$TEST_CONTAINER"
}

stop_event_receiver() {
    docker rm -f "$EVENT_RECEIVER" >/dev/null 2>&1 || true
    docker network disconnect "$EVENT_NET" "$TEST_CONTAINER" >/dev/null 2>&1 || true
    docker network rm "$EVENT_NET" >/dev/null 2>&1 || true
}

# received_message_ids - the MessageIds received so far, one per line
received_message_ids() {
    timeout "$CMD_TIMEOUT" docker exec "$EVENT_RECEIVER" python3 -c '
import json
try:
    for line in open("/tmp/events.jsonl"):
        for r in json.loads(line)["Events"]:
            print(r["MessageId"])
except FileNotFoundError:
    pass'
}

clear_received() {
    timeout "$CMD_TIMEOUT" docker exec "$EVENT_RECEIVER" rm -f "$EVENT_LOG"
}

# wait_for_event MESSAGE_ID [TIMEOUT] - waits until the receiver got MESSAGE_ID
wait_for_event() {
    local id="$1" timeout="${2:-30}" i=0
    while [ $i -lt "$timeout" ]; do
        if received_message_ids | grep -qxF "$id"; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    echo "event $id not received within ${timeout}s; got: $(received_message_ids | tr '\n' ' ')" >&2
    return 1
}

redfish_req() {
    local method="$1" path="$2" data="${3:-}"
    local args=(-sk --max-time "$CMD_TIMEOUT" -u "${IPMI_USER}:${IPMI_PASS}" -X "$method")
    if [ -n "$data" ]; then
        args+=(-H "Content-Type: application/json" -d "$data")
    fi
    curl "${args[@]}" "https://${IPMI_HOST}:${REDFISH_PORT}${path}"
}

redfish_reset() {
    redfish_req POST /redfish/v1/Systems/1/Actions/ComputerSystem.Reset "{\"ResetType\":\"$1\"}"
}

test_events_event_service() {
    local es
    es=$(redfish_get /redfish/v1/EventService)
    assert_equals "True" "$(echo "$es" | ev_json "d['ServiceEnabled']")" "EventService: $es" || return 1
    assert_equals "/redfish/v1/EventService/SSE" "$(echo "$es" | ev_json "d['ServerSentEventUri']")" || return 1
    assert_equals "/redfish/v1/EventService" "$(redfish_get /redfish/v1 | ev_json "d['EventService']['@odata.id']")"
}

test_events_subscribe() {
    local headers body location
    headers=$(curl -sk --max-time "$CMD_TIMEOUT" -u "${IPMI_USER}:${IPMI_PASS}" -o /dev/null -D - \
        -X POST -H "Content-Type: application/json" \
        -d "{\"Destination\":\"http://${EVENT_RECEIVER}:8080/events\",\"Protocol\":\"Redfish\",
             \"Context\":\"container-test\",\"RegistryPrefixes\":[\"ResourceEvent\"],
             \"HttpHeaders\":[{\"X-Token\":\"tok-1\"}]}" \
        "https://${IPMI_HOST}:${REDFISH_PORT}/redfish/v1/EventService/Subscriptions")
    assert_contains "$headers" "201" "subscription should be created: $headers" || return 1
    location=$(echo "$headers" | sed -n 's/^[Ll]ocation: *//p' | tr -d '\r')
    assert_contains "$location" "/redfish/v1/EventService/Subscriptions/" || return 1
    body=$(redfish_get "$location")
    assert_equals "container-test" "$(echo "$body" | ev_json "d['Context']")" "subscription: $body" || return 1
    assert_not_contains "$body" "tok-1" "HttpHeaders must not be returned"
}

test_events_ipmi_power_off_on() {
    clear_received
    ipmi_cmd power off >/dev/null 2>&1
    wait_for_event ResourceEvent.1.3.ResourcePoweredOff || return 1
    ipmi_cmd power on >/dev/null 2>&1
    wait_for_event ResourceEvent.1.3.ResourcePoweredOn || return 1

    local first
    first=$(timeout "$CMD_TIMEOUT" docker exec "$EVENT_RECEIVER" head -1 "$EVENT_LOG")
    assert_equals "container-test" "$(echo "$first" | ev_json "d['Context']")" "event: $first" || return 1
    assert_equals "tok-1" "$(echo "$first" | ev_json "d['_token']")" "HttpHeaders should be sent" || return 1
    assert_equals "application/json" "$(echo "$first" | ev_json "d['_content_type']")" || return 1
    assert_equals "/redfish/v1/Systems/1" "$(echo "$first" | ev_json "d['Events'][0]['OriginOfCondition']['@odata.id']")" || return 1
    assert_equals "StatusChange" "$(echo "$first" | ev_json "d['Events'][0]['EventType']")"
}

test_events_redfish_power_off_on() {
    clear_received
    redfish_reset ForceOff >/dev/null
    wait_for_event ResourceEvent.1.3.ResourcePoweredOff || return 1
    redfish_reset On >/dev/null
    wait_for_event ResourceEvent.1.3.ResourcePoweredOn || return 1
    # Off before On, nothing else (no ResourceChanged: only power changed)
    assert_equals "ResourceEvent.1.3.ResourcePoweredOff ResourceEvent.1.3.ResourcePoweredOn" \
        "$(received_message_ids | tr '\n' ' ' | sed 's/ $//')"
}

test_events_qemu_killed() {
    local pid
    wait_for_qemu_running 30 || return 1
    clear_received
    pid=$(get_qemu_pid)
    container_exec sh -c "kill -9 $pid"
    wait_for_event ResourceEvent.1.3.ResourcePoweredOff 15 || return 1
    redfish_reset On >/dev/null
    wait_for_event ResourceEvent.1.3.ResourcePoweredOn
}

test_events_boot_override_changed() {
    clear_received
    redfish_req PATCH /redfish/v1/Systems/1 \
        '{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Continuous"}}' >/dev/null
    wait_for_event ResourceEvent.1.3.ResourceChanged 10 || return 1
    redfish_req PATCH /redfish/v1/Systems/1 \
        '{"Boot":{"BootSourceOverrideTarget":"None","BootSourceOverrideEnabled":"Disabled"}}' >/dev/null
}

test_events_submit_test_event() {
    clear_received
    redfish_req POST /redfish/v1/EventService/Actions/EventService.SubmitTestEvent \
        '{"MessageId":"ResourceEvent.1.3.ResourceChanged","OriginOfCondition":"/redfish/v1/Managers/1"}' >/dev/null
    wait_for_event ResourceEvent.1.3.ResourceChanged 10
}

test_events_sse() {
    local out=/tmp/qemu-bmc-sse.$$ pid
    # $filter: only the power state messages (StatusChange)
    curl -sk -N --max-time 60 -u "${IPMI_USER}:${IPMI_PASS}" \
        "https://${IPMI_HOST}:${REDFISH_PORT}/redfish/v1/EventService/SSE?\$filter=EventType%20eq%20'StatusChange'" \
        >"$out" 2>/dev/null &
    pid=$!
    sleep 1
    redfish_reset ForceOff >/dev/null
    wait_for_qemu_stopped 30
    redfish_reset On >/dev/null
    wait_for_qemu_running 30
    local i=0
    while [ $i -lt 15 ] && ! grep -q ResourcePoweredOn "$out"; do
        sleep 1
        i=$((i + 1))
    done
    kill "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
    local stream
    stream=$(cat "$out")
    rm -f "$out"
    assert_contains "$stream" "data: " "SSE stream: $stream" || return 1
    assert_contains "$stream" "ResourceEvent.1.3.ResourcePoweredOff" || return 1
    assert_contains "$stream" "ResourceEvent.1.3.ResourcePoweredOn" || return 1
    assert_not_contains "$stream" "ResourceChanged" "the filter should drop other events"
}

test_events_sse_requires_auth() {
    local status
    status=$(curl -sk --max-time 5 -o /dev/null -w "%{http_code}" \
        "https://${IPMI_HOST}:${REDFISH_PORT}/redfish/v1/EventService/SSE")
    assert_equals "401" "$status"
}

test_events_unsubscribe() {
    local status subscription
    # Tests run in subshells: find the subscription in the collection
    subscription=$(redfish_get /redfish/v1/EventService/Subscriptions | ev_json "d['Members'][0]['@odata.id']")
    status=$(curl -sk --max-time "$CMD_TIMEOUT" -u "${IPMI_USER}:${IPMI_PASS}" -o /dev/null -w "%{http_code}" \
        -X DELETE "https://${IPMI_HOST}:${REDFISH_PORT}${subscription}")
    assert_equals "204" "$status" "DELETE subscription" || return 1
    clear_received
    redfish_reset ForceOff >/dev/null
    wait_for_qemu_stopped 30
    sleep 3
    assert_equals "" "$(received_message_ids)" "no events after unsubscribing" || return 1
    redfish_reset On >/dev/null
    wait_for_qemu_running 30
}

start_test_container
wait_for_ipmi_ready 60
wait_for_redfish_ready 60
start_event_receiver

run_test test_events_event_service
run_test test_events_subscribe
run_test test_events_ipmi_power_off_on
run_test test_events_redfish_power_off_on
run_test test_events_qemu_killed
run_test test_events_boot_override_changed
run_test test_events_submit_test_event
run_test test_events_sse
run_test test_events_sse_requires_auth
run_test test_events_unsubscribe

stop_event_receiver
