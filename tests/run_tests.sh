#!/usr/bin/env bash
set -uo pipefail

SHOW_LOGS=false
for arg in "$@"; do
    case "$arg" in
        --logs) SHOW_LOGS=true ;;
    esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

COMPOSE="docker compose -f docker-compose.yml"
PASS=0
FAIL=0

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

cleanup() {
    echo ""
    if $SHOW_LOGS; then
        echo "=== Container logs ==="
        $COMPOSE logs 2>/dev/null || true
    fi
    echo "Cleaning up..."
    $COMPOSE down --remove-orphans 2>/dev/null || true
    $COMPOSE rm -f 2>/dev/null || true
}

trap cleanup EXIT

run_test() {
    local name="$1"
    local cmd="$2"
    shift 2
    local validations=("$@")

    echo ""
    echo "=== TEST: $name ==="
    echo "  CMD: $cmd"

    output=$(eval "$cmd" 2>/dev/null) || {
        echo -e "  ${RED}FAIL${NC} (command exited with non-zero)"
        ((FAIL++))
        return
    }

    local test_failed=0
    for validation in "${validations[@]}"; do
        key="${validation%%=*}"
        expected="${validation#*=}"
        actual=$(echo "$output" | jq -r "$key" 2>/dev/null)
        if [ "$actual" != "$expected" ]; then
            echo -e "  ${RED}FAIL${NC}: $key: expected $expected, got ${actual:-<not found>}"
            test_failed=1
        else
            echo -e "  ${GREEN}PASS${NC}: $key = $expected"
        fi
    done

    if [ $test_failed -eq 0 ]; then
        ((PASS++))
    else
        ((FAIL++))
        echo "  Output:"
        echo "$output" | sed 's/^/    /'
    fi
}

run_test_variants() {
    local base_name="$1"
    local abproxy_args="$2"
    shift 2
    local validations=("$@")

    local modes=(
        "direct|"
        "HTTP proxy|-X http://moproxy:8080"
        "SOCKS5 proxy|-X socks5://moproxy:1080"
    )

    for mode_entry in "${modes[@]}"; do
        local mode_label="${mode_entry%%|*}"
        local proxy_flag="${mode_entry#*|}"

        [ -n "$proxy_flag" ] && proxy_flag=" $proxy_flag"

        run_test "$base_name ($mode_label)" \
            "$COMPOSE run --rm ab-proxy --json $abproxy_args$proxy_flag" \
            "${validations[@]}"
    done
}

echo "Starting services..."
$COMPOSE up -d --build target moproxy

echo "Waiting for services to be ready..."
timeout=30
start=$(date +%s)
while true; do
    curl -sf --max-time 1 http://localhost:9000/ >/dev/null 2>&1 && target_ok=1 || target_ok=0
    curl -s --max-time 1 http://localhost:8080/ >/dev/null 2>&1 && proxy_ok=1 || proxy_ok=0
    if [ "$target_ok" = "1" ] && [ "$proxy_ok" = "1" ]; then
        break
    fi
    if [ $(($(date +%s) - start)) -gt $timeout ]; then
        echo "Timeout waiting for services ($target_ok/$proxy_ok). Dumping logs:"
        $COMPOSE logs
        exit 1
    fi
    sleep 0.5
done

echo "Services ready."

# Test 1: Basic request
run_test_variants "Basic request - 10 reqs" \
    "-n 10 http://target/" \
    ".requests.total=10" \
    ".requests.completed=10" \
    ".requests.failed=0" \
    ".bytes_transferred=30" \
    ".requests.codes.\"200\"=10"

# Test 2: Concurrency
run_test_variants "Concurrency - 20 reqs, concurrency 4" \
    "-n 20 -c 4 http://target/" \
    ".requests.total=20" \
    ".requests.completed=20" \
    ".requests.failed=0" \
    ".requests.codes.\"200\"=20"

# Test 3: Bytes transferred accuracy
run_test_variants "Bytes transferred - 5 reqs, 1024 bytes each" \
    "-n 5 http://target/size/1024" \
    ".requests.total=5" \
    ".bytes_transferred=5120" \
    ".requests.failed=0" \
    ".requests.codes.\"200\"=5"

# Test 4: HTTP status codes
run_test_variants "HTTP status codes - 10 reqs returning 404" \
    "-n 10 http://target/status/404" \
    ".requests.total=10" \
    ".requests.codes.\"404\"=10" \
    ".requests.failed=0"

# Test 5: Multiple bursts
run_test_variants "Bursts - 3 bursts of 5 requests each" \
    "-n 5 --bursts 3 --delay 0 http://target/" \
    ".bursts=3" \
    ".requests_per_burst=5" \
    ".requests.total=15" \
    ".requests.completed=15" \
    ".requests.failed=0" \
    ".requests.codes.\"200\"=15"

# Test 6: Custom headers
run_test_variants "Custom headers - 5 reqs with X-Test header" \
    "-n 5 -H \"X-Test: hello\" http://target/headers" \
    ".requests.total=5" \
    ".requests.completed=5" \
    ".requests.failed=0" \
    ".requests.codes.\"200\"=5"

echo ""
echo "=========================================="
echo -e "Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}"
echo "=========================================="

if [ $FAIL -gt 0 ]; then
    exit 1
fi
