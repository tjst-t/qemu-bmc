#!/bin/bash
# run_tests.sh - Test runner for qemu-bmc container integration tests
#
# Usage:
#   ./tests/run_tests.sh all          # Run all tests
#   ./tests/run_tests.sh container    # Run container tests only
#   ./tests/run_tests.sh quick        # Run smoke tests only
#   ./tests/run_tests.sh ipmi redfish # Run multiple categories

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=tests/test_helper.sh
source "$SCRIPT_DIR/test_helper.sh"

# Available test categories
CATEGORIES="container entrypoint ipmi redfish power boot network cross quick"

usage() {
    echo "Usage: $0 <category...>"
    echo ""
    echo "Categories:"
    echo "  all        - Run all test categories"
    echo "  container  - Container infrastructure tests"
    echo "  entrypoint - Environment variable / entrypoint tests"
    echo "  ipmi       - IPMI protocol tests"
    echo "  redfish    - Redfish API tests"
    echo "  power      - Power control tests"
    echo "  boot       - Boot device tests"
    echo "  network    - Network passthrough tests"
    echo "  cross      - Cross-protocol consistency tests"
    echo "  quick      - Smoke tests (30s max)"
    echo ""
    echo "Options:"
    echo "  --build    - Build test image before running"
    echo "  --no-cleanup - Don't remove container after tests"
    exit 1
}

# Parse arguments
BUILD=false
CLEANUP=true
REQUESTED_CATEGORIES=()

for arg in "$@"; do
    case "$arg" in
        --build) BUILD=true ;;
        --no-cleanup) CLEANUP=false ;;
        all) REQUESTED_CATEGORIES=($CATEGORIES) ;;
        -h|--help) usage ;;
        *)
            if echo "$CATEGORIES" | grep -qw "$arg"; then
                REQUESTED_CATEGORIES+=("$arg")
            else
                echo "Unknown category: $arg"
                usage
            fi
            ;;
    esac
done

if [ ${#REQUESTED_CATEGORIES[@]} -eq 0 ]; then
    usage
fi

# Build image if requested
if [ "$BUILD" = true ]; then
    echo "Building test image..."
    docker build -t "$TEST_IMAGE" -f docker/Dockerfile .
fi

# Run each category
OVERALL_RC=0
for category in "${REQUESTED_CATEGORIES[@]}"; do
    # Reset counters for each category
    TESTS_TOTAL=0
    TESTS_PASSED=0
    TESTS_FAILED=0

    test_file="$SCRIPT_DIR/test_${category}.sh"
    if [ ! -f "$test_file" ]; then
        echo -e "${YELLOW}SKIP: $test_file not found${NC}"
        continue
    fi

    echo ""
    echo "==============================="
    echo "Category: $category"
    echo "==============================="

    # shellcheck source=/dev/null
    source "$test_file"

    if ! print_summary; then
        OVERALL_RC=1
    fi
done

# Cleanup
if [ "$CLEANUP" = true ]; then
    echo ""
    echo "Cleaning up..."
    stop_test_container
fi

exit $OVERALL_RC
