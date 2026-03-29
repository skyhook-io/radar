#!/usr/bin/env bash
#
# Semi-automated proxy auth e2e test.
# Runs radar with --auth-mode=proxy against the current kubeconfig context
# and validates auth behavior via curl.
#
# Prerequisites:
#   - radar binary built (make build)
#   - kubectl access to a cluster with multiple namespaces
#   - K8s RBAC configured (or at least system:masters group works)
#
# Usage:
#   ./scripts/test-proxy-auth.sh [radar-binary-path]
#
# The script picks a random port to avoid conflicts.

set -euo pipefail

RADAR="${1:-./radar}"
PORT=$((9300 + RANDOM % 100))
BASE="http://localhost:${PORT}"
PASS=0
FAIL=0
PID=""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

cleanup() {
    if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
        kill "$PID" 2>/dev/null
        wait "$PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

check() {
    local desc="$1"
    local expected="$2"
    local actual="$3"
    if [[ "$actual" == "$expected" ]]; then
        echo -e "  ${GREEN}PASS${NC} $desc (got $actual)"
        PASS=$((PASS + 1))
    else
        echo -e "  ${RED}FAIL${NC} $desc (expected $expected, got $actual)"
        FAIL=$((FAIL + 1))
    fi
}

check_not_empty() {
    local desc="$1"
    local value="$2"
    if [[ -n "$value" && "$value" != "null" && "$value" != "0" ]]; then
        echo -e "  ${GREEN}PASS${NC} $desc (got $value)"
        PASS=$((PASS + 1))
    else
        echo -e "  ${RED}FAIL${NC} $desc (expected non-empty, got '$value')"
        FAIL=$((FAIL + 1))
    fi
}

# --- Start radar ---
echo -e "${YELLOW}Starting radar on port $PORT with --auth-mode=proxy${NC}"
"$RADAR" --port "$PORT" --auth-mode=proxy --no-browser --auth-secret=test-e2e-secret 2>/dev/null &
PID=$!

# Wait for health
echo "Waiting for radar to be ready..."
for i in $(seq 1 30); do
    if curl -sf "$BASE/api/health" >/dev/null 2>&1; then
        echo "Radar ready."
        break
    fi
    if ! kill -0 "$PID" 2>/dev/null; then
        echo -e "${RED}Radar process died${NC}"
        exit 1
    fi
    sleep 1
done

if ! curl -sf "$BASE/api/health" >/dev/null 2>&1; then
    echo -e "${RED}Radar failed to start within 30s${NC}"
    exit 1
fi

# --- Test 1: Unauthenticated requests ---
echo ""
echo -e "${YELLOW}Test 1: Unauthenticated requests → 401${NC}"

for path in /api/topology /api/resources/pods /api/namespaces /api/dashboard /api/events; do
    code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE$path")
    check "$path" "401" "$code"
done

# --- Test 2: Exempt paths ---
echo ""
echo -e "${YELLOW}Test 2: Exempt paths → 200 without auth${NC}"

code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/health")
check "/api/health" "200" "$code"

# --- Test 3: /api/auth/me (soft-auth) ---
echo ""
echo -e "${YELLOW}Test 3: /api/auth/me (soft-auth)${NC}"

code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/auth/me")
check "unauthenticated /api/auth/me → 200" "200" "$code"

auth_enabled=$(curl -s "$BASE/api/auth/me" | jq -r '.authEnabled')
check "authEnabled is true" "true" "$auth_enabled"

# --- Test 4: Admin user (system:masters) ---
echo ""
echo -e "${YELLOW}Test 4: Admin user (system:masters) → full access${NC}"

code=$(curl -s -o /dev/null -w '%{http_code}' \
    -H 'X-Forwarded-User: admin@test' \
    -H 'X-Forwarded-Groups: system:masters' \
    "$BASE/api/topology")
check "admin /api/topology" "200" "$code"

ns_count=$(curl -s \
    -H 'X-Forwarded-User: admin@test' \
    -H 'X-Forwarded-Groups: system:masters' \
    "$BASE/api/namespaces" | jq 'length')
check_not_empty "admin sees namespaces" "$ns_count"

me=$(curl -s \
    -H 'X-Forwarded-User: admin@test' \
    -H 'X-Forwarded-Groups: system:masters' \
    "$BASE/api/auth/me")
username=$(echo "$me" | jq -r '.username')
check "admin username" "admin@test" "$username"

# --- Test 5: Session cookie round-trip ---
echo ""
echo -e "${YELLOW}Test 5: Session cookie round-trip${NC}"

# Get cookie from proxy-auth request
cookie_header=$(curl -s -D - -o /dev/null \
    -H 'X-Forwarded-User: cookie-test' \
    -H 'X-Forwarded-Groups: testers' \
    "$BASE/api/auth/me" | grep -i 'set-cookie.*radar_session')

if [[ -n "$cookie_header" ]]; then
    echo -e "  ${GREEN}PASS${NC} session cookie received"
    PASS=$((PASS + 1))

    # Extract cookie value
    cookie_val=$(echo "$cookie_header" | sed 's/.*radar_session=\([^;]*\).*/\1/')

    # Use cookie only (no proxy headers)
    code=$(curl -s -o /dev/null -w '%{http_code}' \
        -b "radar_session=$cookie_val" \
        "$BASE/api/auth/me")
    check "cookie-only request → 200" "200" "$code"

    cookie_user=$(curl -s -b "radar_session=$cookie_val" "$BASE/api/auth/me" | jq -r '.username')
    check "cookie preserves username" "cookie-test" "$cookie_user"
else
    echo -e "  ${RED}FAIL${NC} no session cookie received"
    FAIL=$((FAIL + 1))
fi

# --- Test 6: Restricted user (no K8s RBAC) ---
echo ""
echo -e "${YELLOW}Test 6: User with no K8s RBAC → limited/no access${NC}"

# A user with no RBAC bindings should see 0 or very few namespaces.
# (Exact behavior depends on cluster config — some clusters give default access.)
restricted_ns=$(curl -s \
    -H 'X-Forwarded-User: no-rbac-user@test' \
    -H 'X-Forwarded-Groups: no-access-group' \
    "$BASE/api/namespaces" | jq 'length')
echo -e "  ${YELLOW}INFO${NC} restricted user sees $restricted_ns namespaces (expect 0 or very few)"

restricted_pods=$(curl -s \
    -H 'X-Forwarded-User: no-rbac-user@test' \
    -H 'X-Forwarded-Groups: no-access-group' \
    "$BASE/api/resources/pods" | jq 'length')
echo -e "  ${YELLOW}INFO${NC} restricted user sees $restricted_pods pods"

# --- Summary ---
echo ""
echo -e "${YELLOW}═══════════════════════════════${NC}"
TOTAL=$((PASS + FAIL))
echo -e "  Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC} (of $TOTAL)"
echo -e "${YELLOW}═══════════════════════════════${NC}"

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
