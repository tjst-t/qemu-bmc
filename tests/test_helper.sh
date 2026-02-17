#!/bin/bash
# test_helper.sh - Test helper functions for qemu-bmc container integration tests

# --- Configuration ---
TEST_IMAGE="${TEST_IMAGE:-qemu-bmc:test}"
TEST_CONTAINER="${TEST_CONTAINER:-qemu-bmc-test}"
IPMI_HOST="${IPMI_HOST:-127.0.0.1}"
IPMI_PORT="${IPMI_PORT:-623}"
REDFISH_PORT="${REDFISH_PORT:-443}"
IPMI_USER="${IPMI_USER:-admin}"
IPMI_PASS="${IPMI_PASS:-password}"
EVIDENCE_DIR="${EVIDENCE_DIR:-tests/evidence}"

# --- Test state ---
TESTS_TOTAL=0
TESTS_PASSED=0
TESTS_FAILED=0
CURRENT_TEST=""

# --- Colors ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

# --- Assertion functions ---

assert_equals() {
    local expected="$1"
    local actual="$2"
    local msg="${3:-Expected '$expected', got '$actual'}"
    if [ "$expected" = "$actual" ]; then
        return 0
    else
        echo -e "${RED}FAIL: $msg${NC}" >&2
        echo "  expected: '$expected'" >&2
        echo "  actual:   '$actual'" >&2
        return 1
    fi
}

assert_contains() {
    local haystack="$1"
    local needle="$2"
    local msg="${3:-Expected output to contain '$needle'}"
    if echo "$haystack" | grep -qF "$needle"; then
        return 0
    else
        echo -e "${RED}FAIL: $msg${NC}" >&2
        echo "  output: '$haystack'" >&2
        echo "  expected to contain: '$needle'" >&2
        return 1
    fi
}

assert_not_contains() {
    local haystack="$1"
    local needle="$2"
    local msg="${3:-Expected output NOT to contain '$needle'}"
    if ! echo "$haystack" | grep -qF "$needle"; then
        return 0
    else
        echo -e "${RED}FAIL: $msg${NC}" >&2
        echo "  output: '$haystack'" >&2
        echo "  expected NOT to contain: '$needle'" >&2
        return 1
    fi
}

assert_success() {
    local exit_code="$1"
    local msg="${2:-Expected command to succeed}"
    if [ "$exit_code" -eq 0 ]; then
        return 0
    else
        echo -e "${RED}FAIL: $msg (exit code: $exit_code)${NC}" >&2
        return 1
    fi
}

assert_failure() {
    local exit_code="$1"
    local msg="${2:-Expected command to fail}"
    if [ "$exit_code" -ne 0 ]; then
        return 0
    else
        echo -e "${RED}FAIL: $msg (exit code was 0)${NC}" >&2
        return 1
    fi
}

# --- Test framework ---

run_test() {
    local test_name="$1"
    CURRENT_TEST="$test_name"
    TESTS_TOTAL=$((TESTS_TOTAL + 1))

    echo -n "  $test_name ... "
    local output
    output=$("$test_name" 2>&1)
    local rc=$?

    if [ $rc -eq 0 ]; then
        echo -e "${GREEN}PASS${NC}"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${RED}FAIL${NC}"
        echo "$output" | sed 's/^/    /'
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi

    # Save evidence
    mkdir -p "$EVIDENCE_DIR"
    echo "$output" > "$EVIDENCE_DIR/${test_name}.log"
}

print_summary() {
    echo ""
    echo "================================="
    echo "Results: $TESTS_PASSED/$TESTS_TOTAL passed"
    if [ $TESTS_FAILED -gt 0 ]; then
        echo -e "${RED}$TESTS_FAILED test(s) FAILED${NC}"
        return 1
    else
        echo -e "${GREEN}All tests passed${NC}"
        return 0
    fi
}

# --- Container management ---

start_test_container() {
    local extra_env=""
    local extra_args=""

    while [ $# -gt 0 ]; do
        case "$1" in
            -e) extra_env="$extra_env -e $2"; shift 2 ;;
            --no-kvm) extra_args="$extra_args --device /dev/net/tun:/dev/net/tun"; shift ;;
            *) shift ;;
        esac
    done

    # Stop any existing container
    docker rm -f "$TEST_CONTAINER" 2>/dev/null || true

    # Start container with default settings
    # shellcheck disable=SC2086
    docker run -d \
        --name "$TEST_CONTAINER" \
        --device /dev/kvm:/dev/kvm \
        --device /dev/net/tun:/dev/net/tun \
        --cap-add NET_ADMIN \
        --cap-add SYS_ADMIN \
        --security-opt apparmor=unconfined \
        -p "${IPMI_PORT}:623/udp" \
        -p "${REDFISH_PORT}:443" \
        -p "5900:5900" \
        -e "IPMI_USER=${IPMI_USER}" \
        -e "IPMI_PASS=${IPMI_PASS}" \
        $extra_env \
        $extra_args \
        "$TEST_IMAGE"

    # Wait for container to be running
    wait_for_container_running 30
}

stop_test_container() {
    docker stop "$TEST_CONTAINER" 2>/dev/null || true
    docker rm -f "$TEST_CONTAINER" 2>/dev/null || true
}

container_exec() {
    docker exec "$TEST_CONTAINER" "$@"
}

wait_for_container_running() {
    local timeout="${1:-30}"
    local i=0
    while [ $i -lt "$timeout" ]; do
        local state
        state=$(docker inspect -f '{{.State.Status}}' "$TEST_CONTAINER" 2>/dev/null || echo "missing")
        if [ "$state" = "running" ]; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    echo "Container did not reach running state within ${timeout}s" >&2
    return 1
}

# --- IPMI helpers ---

ipmi_cmd() {
    ipmitool -I lanplus -H "$IPMI_HOST" -p "$IPMI_PORT" \
        -U "$IPMI_USER" -P "$IPMI_PASS" "$@"
}

ipmi_cmd_lan() {
    ipmitool -I lan -H "$IPMI_HOST" -p "$IPMI_PORT" \
        -U "$IPMI_USER" -P "$IPMI_PASS" "$@"
}

ipmi_cmd_wrong_pass() {
    ipmitool -I lanplus -H "$IPMI_HOST" -p "$IPMI_PORT" \
        -U "$IPMI_USER" -P "wrongpassword" "$@" 2>&1
}

ipmi_cmd_wrong_user() {
    ipmitool -I lanplus -H "$IPMI_HOST" -p "$IPMI_PORT" \
        -U "wronguser" -P "$IPMI_PASS" "$@" 2>&1
}

# --- Redfish helpers ---

redfish_get() {
    local path="$1"
    curl -sk -u "${IPMI_USER}:${IPMI_PASS}" \
        "https://${IPMI_HOST}:${REDFISH_PORT}${path}"
}

redfish_post() {
    local path="$1"
    local data="$2"
    curl -sk -u "${IPMI_USER}:${IPMI_PASS}" \
        -X POST -H "Content-Type: application/json" \
        -d "$data" \
        "https://${IPMI_HOST}:${REDFISH_PORT}${path}"
}

redfish_patch() {
    local path="$1"
    local data="$2"
    curl -sk -u "${IPMI_USER}:${IPMI_PASS}" \
        -X PATCH -H "Content-Type: application/json" \
        -d "$data" \
        "https://${IPMI_HOST}:${REDFISH_PORT}${path}"
}

redfish_get_no_auth() {
    local path="$1"
    curl -sk -o /dev/null -w "%{http_code}" \
        "https://${IPMI_HOST}:${REDFISH_PORT}${path}"
}

redfish_get_status() {
    local path="$1"
    curl -sk -u "${IPMI_USER}:${IPMI_PASS}" \
        -o /dev/null -w "%{http_code}" \
        "https://${IPMI_HOST}:${REDFISH_PORT}${path}"
}

# --- Wait utilities ---

wait_for_power_state() {
    local expected="$1"
    local timeout="${2:-30}"
    local i=0
    while [ $i -lt "$timeout" ]; do
        local state
        state=$(ipmi_cmd power status 2>/dev/null | grep -o "on\|off" || true)
        if [ "$state" = "$expected" ]; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    echo "Power state did not reach '$expected' within ${timeout}s" >&2
    return 1
}

wait_for_qemu_running() {
    local timeout="${1:-30}"
    local i=0
    while [ $i -lt "$timeout" ]; do
        if container_exec pgrep -x qemu-system-x86 >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    echo "QEMU did not start within ${timeout}s" >&2
    return 1
}

wait_for_qemu_stopped() {
    local timeout="${1:-30}"
    local i=0
    while [ $i -lt "$timeout" ]; do
        if ! container_exec pgrep -x qemu-system-x86 >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    echo "QEMU did not stop within ${timeout}s" >&2
    return 1
}

get_qemu_pid() {
    container_exec pgrep -x qemu-system-x86 2>/dev/null || echo ""
}

wait_for_ipmi_ready() {
    local timeout="${1:-60}"
    local i=0
    while [ $i -lt "$timeout" ]; do
        if ipmi_cmd mc info >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    echo "IPMI did not become ready within ${timeout}s" >&2
    return 1
}

wait_for_redfish_ready() {
    local timeout="${1:-60}"
    local i=0
    while [ $i -lt "$timeout" ]; do
        local status
        status=$(redfish_get_status "/redfish/v1" 2>/dev/null || echo "000")
        if [ "$status" = "200" ]; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    echo "Redfish did not become ready within ${timeout}s" >&2
    return 1
}
