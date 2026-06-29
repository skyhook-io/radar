#!/usr/bin/env bash
#
# Live end-to-end RBAC test for Radar's auth-enabled mode.
#
# Spins up Radar with --auth-mode=proxy, applies test RoleBindings for two
# imaginary users (alice, bob) restricted to different namespaces, and
# verifies via curl that:
#
#   1. REST reads filter to each user's allowed namespaces
#   2. MCP reads filter the same way
#   3. The cache stays cluster-wide — alice's pick doesn't shrink bob's view
#   4. Cluster-only kinds (Nodes) are hidden from non-admin users
#   5. Picking a denied namespace is rejected
#
# Requires: kubectl, jq, curl, a working cluster. Pre-built radar binary
# (run `make build` first) or pass --skip-build to skip rebuilding.
#
# WARNING: This script applies ClusterRoleBindings and RoleBindings to the
# CURRENT cluster. Only run against a local kind/minikube cluster, never
# against shared/prod infrastructure. The script asks for confirmation
# unless --yes is passed. RBAC objects are cleaned up on exit (trap).

set -euo pipefail

# --- Config ---
CTX=$(kubectl config current-context)
PORT="${PORT:-9389}"
AUTH_SECRET="rbac-test-secret-do-not-use-in-prod-1234567890"
NS_ALICE="${NS_ALICE:-radar-rbac-alice}"
NS_BOB="${NS_BOB:-radar-rbac-bob}"
RADAR_BIN="${RADAR_BIN:-./radar}"
SKIP_BUILD=false
ASSUME_YES=false

# --- Arg parsing ---
for arg in "$@"; do
  case "$arg" in
    --skip-build) SKIP_BUILD=true ;;
    --yes|-y) ASSUME_YES=true ;;
    --help|-h) sed -n '2,/^$/p' "$0" | sed 's/^# \?//'; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

# --- Colors ---
G='\033[0;32m'; R='\033[0;31m'; Y='\033[1;33m'; B='\033[1;34m'; N='\033[0m'
ok()   { echo -e "${G}✓${N} $1"; }
fail() { echo -e "${R}✗${N} $1"; FAILS=$((FAILS+1)); }
info() { echo -e "${B}→${N} $1"; }
warn() { echo -e "${Y}⚠${N} $1"; }

FAILS=0
RADAR_PID=""

# --- Cleanup trap ---
cleanup() {
  set +e
  if [[ -n "$RADAR_PID" ]]; then
    info "stopping radar (pid $RADAR_PID)"
    kill "$RADAR_PID" 2>/dev/null
    wait "$RADAR_PID" 2>/dev/null
  fi
  info "removing test RBAC + namespaces + CRD"
  kubectl delete clusterrolebinding "radar-rbac-test-impersonator" "radar-rbac-test-carol" "radar-rbac-test-erin" --ignore-not-found >/dev/null 2>&1
  kubectl delete clusterrole "radar-rbac-test-pods-only" "radar-rbac-test-nodes-only" --ignore-not-found >/dev/null 2>&1
  kubectl delete -n "$NS_ALICE" rolebinding "radar-test-alice" "radar-test-dave-a" "radar-test-frank" "radar-test-frank-view" --ignore-not-found >/dev/null 2>&1
  kubectl delete -n "$NS_ALICE" role "radar-rbac-test-secret-reader" --ignore-not-found >/dev/null 2>&1
  kubectl delete -n "$NS_BOB" rolebinding "radar-test-bob" "radar-test-dave-b" --ignore-not-found >/dev/null 2>&1
  kubectl delete namespace "$NS_ALICE" "$NS_BOB" --ignore-not-found >/dev/null 2>&1
  # Delete CRD instances first, then the CRD itself
  kubectl delete radartests.test.skyhook.io --all --ignore-not-found >/dev/null 2>&1
  kubectl delete crd radartests.test.skyhook.io --ignore-not-found >/dev/null 2>&1
}
trap cleanup EXIT

# --- Safety check ---
echo
echo -e "${Y}This will apply test RBAC objects to: ${B}${CTX}${N}"
if [[ "$ASSUME_YES" != "true" ]]; then
  read -rp "Continue? [y/N] " ans
  [[ "$ans" =~ ^[Yy]$ ]] || { echo "aborted"; exit 1; }
fi

# --- Pre-flight: dependencies ---
for bin in kubectl jq curl; do
  command -v "$bin" >/dev/null || { echo "missing: $bin"; exit 1; }
done

# --- Build (unless skipped) ---
if [[ "$SKIP_BUILD" != "true" ]]; then
  info "building radar (run with --skip-build to skip)"
  make build >/dev/null
fi
[[ -x "$RADAR_BIN" ]] || { echo "radar binary not found at $RADAR_BIN — run make build"; exit 1; }

# --- Set up test fixtures ---
info "creating test namespaces and pods"
kubectl create namespace "$NS_ALICE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl create namespace "$NS_BOB"   --dry-run=client -o yaml | kubectl apply -f - >/dev/null

kubectl run -n "$NS_ALICE" rbac-test-pod-a --image=registry.k8s.io/pause:3.9 --restart=Never \
  --dry-run=client -o yaml 2>/dev/null | kubectl apply -f - >/dev/null
kubectl run -n "$NS_BOB"   rbac-test-pod-b --image=registry.k8s.io/pause:3.9 --restart=Never \
  --dry-run=client -o yaml 2>/dev/null | kubectl apply -f - >/dev/null

# Seed a Secret in NS_ALICE so per-namespace Secret RBAC tests have something
# to leak (or correctly hide). When the chart grants the SA cluster-wide
# secrets (rbac.secrets / rbac.helm / auth.mode != "none" / cloud.enabled),
# the cache holds it and per-user RBAC must decide visibility on read.
kubectl create secret generic -n "$NS_ALICE" rbac-test-secret-a \
  --from-literal=token=ignored \
  --dry-run=client -o yaml 2>/dev/null | kubectl apply -f - >/dev/null

info "installing test CRD (cluster-scoped, not in static cluster-only list)"
cat <<'EOF' | kubectl apply -f - >/dev/null
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: radartests.test.skyhook.io
spec:
  group: test.skyhook.io
  names:
    kind: RadarTest
    plural: radartests
    singular: radartest
  scope: Cluster
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            x-kubernetes-preserve-unknown-fields: true
EOF
# Wait for CRD to be Established (apiserver picks up the new schema)
kubectl wait --for condition=Established crd/radartests.test.skyhook.io --timeout=10s >/dev/null 2>&1 || true
# Create a couple of instances so list returns non-empty
cat <<'EOF' | kubectl apply -f - >/dev/null
apiVersion: test.skyhook.io/v1
kind: RadarTest
metadata:
  name: rbac-test-cluster-scoped-1
---
apiVersion: test.skyhook.io/v1
kind: RadarTest
metadata:
  name: rbac-test-cluster-scoped-2
EOF

info "applying RoleBindings (alice→${NS_ALICE}, bob→${NS_BOB}, carol→cluster-wide pods only, dave→${NS_ALICE}+${NS_BOB}, erin→nodes-only) + impersonate ClusterRoleBinding"
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: radar-rbac-test-impersonator
subjects:
- kind: ServiceAccount
  name: default
  namespace: default
roleRef:
  kind: ClusterRole
  name: cluster-admin
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: radar-test-alice
  namespace: $NS_ALICE
subjects:
- kind: User
  name: alice
roleRef:
  kind: ClusterRole
  name: view
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: radar-test-bob
  namespace: $NS_BOB
subjects:
- kind: User
  name: bob
roleRef:
  kind: ClusterRole
  name: view
  apiGroup: rbac.authorization.k8s.io
---
# carol: cluster-wide pod read ONLY — verifies that "list pods cluster-wide"
# isn't treated as cluster-admin for cluster-scoped resources.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: radar-rbac-test-pods-only
rules:
- apiGroups: [""]
  resources: ["pods", "namespaces"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: radar-rbac-test-carol
subjects:
- kind: User
  name: carol
roleRef:
  kind: ClusterRole
  name: radar-rbac-test-pods-only
  apiGroup: rbac.authorization.k8s.io
---
# dave: multi-namespace user (verifies dashboard aggregation doesn't leak
# beyond allowed namespaces).
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: radar-test-dave-a
  namespace: $NS_ALICE
subjects:
- kind: User
  name: dave
roleRef:
  kind: ClusterRole
  name: view
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: radar-test-dave-b
  namespace: $NS_BOB
subjects:
- kind: User
  name: dave
roleRef:
  kind: ClusterRole
  name: view
  apiGroup: rbac.authorization.k8s.io
---
# erin: cluster-scoped read for nodes ONLY, no namespace access at all.
# Verifies cluster-scoped reads aren't blocked by the noNamespaceAccess
# short-circuit when the user has explicit cluster-scoped RBAC.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: radar-rbac-test-nodes-only
rules:
- apiGroups: [""]
  resources: ["nodes"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: radar-rbac-test-erin
subjects:
- kind: User
  name: erin
roleRef:
  kind: ClusterRole
  name: radar-rbac-test-nodes-only
  apiGroup: rbac.authorization.k8s.io
---
# frank: view in NS_ALICE PLUS explicit secret list/get. Same namespace
# ceiling as alice (who is bound to view, which excludes secrets) — frank
# is the positive control. He must see Secrets where alice can't, proving
# the per-namespace SAR gate honors RBAC grants rather than blanket-denying
# secrets to all namespace-bound users.
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: radar-rbac-test-secret-reader
  namespace: $NS_ALICE
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: radar-test-frank
  namespace: $NS_ALICE
subjects:
- kind: User
  name: frank
roleRef:
  kind: Role
  name: radar-rbac-test-secret-reader
  apiGroup: rbac.authorization.k8s.io
---
# frank also needs view (pods/services/etc.) so DiscoverNamespaces picks
# up NS_ALICE as accessible. Without the view-binding, list-pods SAR fails
# and AllowedNamespaces is empty, so the secret-only Role wouldn't matter.
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: radar-test-frank-view
  namespace: $NS_ALICE
subjects:
- kind: User
  name: frank
roleRef:
  kind: ClusterRole
  name: view
  apiGroup: rbac.authorization.k8s.io
EOF

# --- Boot Radar ---
info "starting radar on port $PORT (auth-mode=proxy)"
nohup "$RADAR_BIN" --port "$PORT" --auth-mode=proxy --auth-secret="$AUTH_SECRET" --no-browser \
  > "/tmp/radar-rbac-test-$PORT.log" 2>&1 &
RADAR_PID=$!

# Wait for ready (up to 30s)
for i in $(seq 1 30); do
  if curl -sf "http://localhost:$PORT/api/health" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$RADAR_PID" 2>/dev/null; then
    echo "radar exited early — see /tmp/radar-rbac-test-$PORT.log"
    exit 1
  fi
  sleep 1
done
ok "radar ready at http://localhost:$PORT"

# Need cluster-connected before running tests. Poll for state=connected.
for i in $(seq 1 30); do
  state=$(curl -sf "http://localhost:$PORT/api/connection" | jq -r '.state // empty' 2>/dev/null || true)
  [[ "$state" == "connected" ]] && break
  sleep 1
done
[[ "$state" == "connected" ]] || { echo "radar didn't connect to cluster: state=$state"; exit 1; }
ok "radar connected to cluster"

# --- Helpers ---
as_user() {
  # as_user <user> <method> <path> [data]
  local user=$1 method=$2 path=$3 data=${4:-}
  local args=(-sf -X "$method" -H "X-Forwarded-User: $user")
  if [[ -n "$data" ]]; then
    args+=(-H 'Content-Type: application/json' -d "$data")
  fi
  curl "${args[@]}" "http://localhost:$PORT$path"
}

mcp_call() {
  # mcp_call <user> <tool> <args-json>
  local user=$1 tool=$2 args=$3
  local body
  body=$(jq -n --arg t "$tool" --argjson a "$args" \
    '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:$t,arguments:$a}}')
  curl -s -X POST -H "X-Forwarded-User: $user" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d "$body" \
    "http://localhost:$PORT/mcp" \
  | sed -n 's/^data: //p' | jq -r '.result.content[0].text // ""'
}

# arr_len reads a JSON array on stdin and prints its length, or "ERR" if
# the input isn't a valid JSON array. Replaces the older `jq '. | length'
# || echo 0` idiom whose `0` fallback collapsed transport / parse errors
# into the same value as a successful "no leak" assertion, masking real
# regressions in the leak-detection tests.
arr_len() {
  jq -r 'if type == "array" then length else "ERR" end' 2>/dev/null || echo "ERR"
}

# expect_zero <description> <count> — passes when count is "0", fails on
# anything else (including "ERR" from arr_len). Use for "no leak" tests.
expect_zero() {
  local desc=$1 actual=$2
  case "$actual" in
    0)   ok "$desc" ;;
    ERR) fail "$desc — invalid response (transport/parse error)" ;;
    *)   fail "$desc — expected 0, got $actual" ;;
  esac
}

# expect_at_least_one <description> <count> — passes when count is a
# positive integer, fails on "0" or "ERR". Use for "should see ≥1" tests.
expect_at_least_one() {
  local desc=$1 actual=$2
  case "$actual" in
    ERR) fail "$desc — invalid response (transport/parse error)" ;;
    0)   fail "$desc — got 0" ;;
    *)   ok "$desc ($actual)" ;;
  esac
}

# Wait for namespace listings to settle in the cache
sleep 2

echo
echo -e "${B}=== REST: per-user namespace listing ===${N}"

ALICE_NS=$(as_user alice GET /api/cluster/namespace-scope | jq -r '.accessibleNamespaces | sort | join(",")')
if [[ "$ALICE_NS" == "$NS_ALICE" ]]; then
  ok "alice sees only $NS_ALICE: [$ALICE_NS]"
else
  fail "alice should see [$NS_ALICE], got [$ALICE_NS]"
fi

BOB_NS=$(as_user bob GET /api/cluster/namespace-scope | jq -r '.accessibleNamespaces | sort | join(",")')
if [[ "$BOB_NS" == "$NS_BOB" ]]; then
  ok "bob sees only $NS_BOB: [$BOB_NS]"
else
  fail "bob should see [$NS_BOB], got [$BOB_NS]"
fi

echo
echo -e "${B}=== REST: pod listing is filtered ===${N}"

ALICE_PODS=$(as_user alice GET /api/resources/pods | jq -r '.[].metadata.name // empty' | sort | tr '\n' ',' | sed 's/,$//')
if [[ "$ALICE_PODS" == "rbac-test-pod-a" ]]; then
  ok "alice sees only rbac-test-pod-a"
else
  fail "alice pod list = [$ALICE_PODS], expected [rbac-test-pod-a]"
fi

BOB_PODS=$(as_user bob GET /api/resources/pods | jq -r '.[].metadata.name // empty' | sort | tr '\n' ',' | sed 's/,$//')
if [[ "$BOB_PODS" == "rbac-test-pod-b" ]]; then
  ok "bob sees only rbac-test-pod-b"
else
  fail "bob pod list = [$BOB_PODS], expected [rbac-test-pod-b]"
fi

echo
echo -e "${B}=== REST: cluster-only kinds hidden from restricted users ===${N}"

ALICE_NODES_HTTP=$(curl -s -o /dev/null -w '%{http_code}' -H "X-Forwarded-User: alice" \
  "http://localhost:$PORT/api/resources/nodes")
ALICE_NODES_BODY=$(as_user alice GET /api/resources/nodes 2>/dev/null || echo '[]')
ALICE_NODE_COUNT=$(echo "$ALICE_NODES_BODY" | arr_len)
expect_zero "alice sees 0 nodes (cluster-only kind, restricted user) — REST status $ALICE_NODES_HTTP" "$ALICE_NODE_COUNT"

echo
echo -e "${B}=== MCP: list_resources filters per-user ===${N}"

ALICE_MCP_PODS=$(mcp_call alice list_resources '{"kind":"pods"}' | jq -r '.[].name // empty' 2>/dev/null | sort | tr '\n' ',' | sed 's/,$//')
if [[ "$ALICE_MCP_PODS" == "rbac-test-pod-a" ]]; then
  ok "MCP alice list_resources kind=pods returns only rbac-test-pod-a"
else
  fail "MCP alice pods = [$ALICE_MCP_PODS], expected [rbac-test-pod-a]"
fi

BOB_MCP_PODS=$(mcp_call bob list_resources '{"kind":"pods"}' | jq -r '.[].name // empty' 2>/dev/null | sort | tr '\n' ',' | sed 's/,$//')
if [[ "$BOB_MCP_PODS" == "rbac-test-pod-b" ]]; then
  ok "MCP bob list_resources kind=pods returns only rbac-test-pod-b"
else
  fail "MCP bob pods = [$BOB_MCP_PODS], expected [rbac-test-pod-b]"
fi

echo
echo -e "${B}=== MCP: cluster-only kinds hidden ===${N}"

ALICE_MCP_NODES=$(mcp_call alice list_resources '{"kind":"nodes"}' | arr_len)
expect_zero "MCP alice list_resources kind=nodes returns 0 (cluster-only)" "$ALICE_MCP_NODES"

echo
echo -e "${B}=== MCP: list_namespaces filters ===${N}"

ALICE_MCP_NS=$(mcp_call alice list_namespaces '{}' | jq -r '.[].name // empty' 2>/dev/null | sort | tr '\n' ',' | sed 's/,$//')
if [[ "$ALICE_MCP_NS" == "$NS_ALICE" ]]; then
  ok "MCP alice list_namespaces returns only $NS_ALICE"
else
  fail "MCP alice list_namespaces = [$ALICE_MCP_NS], expected [$NS_ALICE]"
fi

echo
echo -e "${B}=== Picker is per-user, doesn't affect other users ===${N}"

# alice picks her allowed namespace
as_user alice POST /api/cluster/namespace "{\"namespaces\":[\"$NS_ALICE\"]}" >/dev/null
ALICE_AFTER=$(as_user alice GET /api/cluster/namespace-scope | jq -r '.actives | join(",")')
BOB_AFTER=$(as_user bob   GET /api/cluster/namespace-scope | jq -r '.actives | join(",")')

if [[ "$ALICE_AFTER" == "$NS_ALICE" ]]; then
  ok "alice's pick set to $NS_ALICE"
else
  fail "alice's pick should be $NS_ALICE, got $ALICE_AFTER"
fi

if [[ "$BOB_AFTER" == "" ]]; then
  ok "bob's view unaffected by alice's pick (cache stayed shared)"
else
  fail "bob's pick = '$BOB_AFTER' — should be empty (alice's pick leaked)"
fi

# bob's pod list should still work after alice's pick — proves cache still has bob's namespace
BOB_PODS_AFTER=$(as_user bob GET /api/resources/pods | jq -r '.[].metadata.name // empty' | sort | tr '\n' ',' | sed 's/,$//')
if [[ "$BOB_PODS_AFTER" == "rbac-test-pod-b" ]]; then
  ok "bob's pod list still works after alice's pick (cache wasn't shrunk)"
else
  fail "bob's pod list broke after alice's pick: [$BOB_PODS_AFTER]"
fi

echo
echo -e "${B}=== Picking a denied namespace is rejected ===${N}"

REJECT_HTTP=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -H "X-Forwarded-User: alice" -H 'Content-Type: application/json' \
  -d "{\"namespaces\":[\"$NS_BOB\"]}" \
  "http://localhost:$PORT/api/cluster/namespace")
if [[ "$REJECT_HTTP" == "403" ]]; then
  ok "alice rejected with 403 when picking $NS_BOB"
else
  fail "expected 403 when alice picks $NS_BOB; got $REJECT_HTTP"
fi

# Mixed-allowed/denied: alice picking [her ns, bob's ns] must 403 atomically
# (no partial pick). Verify the picker is all-or-nothing.
MIXED_HTTP=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -H "X-Forwarded-User: alice" -H 'Content-Type: application/json' \
  -d "{\"namespaces\":[\"$NS_ALICE\",\"$NS_BOB\"]}" \
  "http://localhost:$PORT/api/cluster/namespace")
if [[ "$MIXED_HTTP" == "403" ]]; then
  ok "alice rejected with 403 on mixed allowed+denied pick (atomic)"
else
  fail "expected 403 on mixed pick; got $MIXED_HTTP"
fi
ALICE_MIXED_AFTER=$(as_user alice GET /api/cluster/namespace-scope | jq -r '.actives | join(",")')
if [[ "$ALICE_MIXED_AFTER" == "$NS_ALICE" ]]; then
  ok "alice's prior valid pick survived the rejected mixed update"
else
  fail "expected alice's pick to remain $NS_ALICE after rejected mixed update; got '$ALICE_MIXED_AFTER'"
fi

echo
echo -e "${B}=== Cluster-wide pods does NOT imply cluster-scoped reads (carol) ===${N}"
# carol has cluster-wide list pods + list namespaces only. She must NOT
# read Nodes / ClusterRoles / Secrets via the cache — those need their own
# RBAC. This is the per-kind SAR gate's primary regression test.

CAROL_PODS=$(as_user carol GET /api/resources/pods | jq -r '.[].metadata.name // empty' | sort | tr '\n' ',' | sed 's/,$//')
if [[ "$CAROL_PODS" == *"rbac-test-pod-a"* && "$CAROL_PODS" == *"rbac-test-pod-b"* ]]; then
  ok "carol sees pods cluster-wide (her actual permission)"
else
  fail "carol should see all pods, got: $CAROL_PODS"
fi

CAROL_NODES=$(as_user carol GET /api/resources/nodes | arr_len)
expect_zero "carol cannot see Nodes (no list-nodes RBAC even though she has list-pods cluster-wide)" "$CAROL_NODES"

CAROL_CRBS=$(as_user carol GET /api/resources/clusterroles | arr_len)
expect_zero "carol cannot see ClusterRoles" "$CAROL_CRBS"

CAROL_MCP_NODES=$(mcp_call carol list_resources '{"kind":"nodes"}' | arr_len)
expect_zero "MCP carol list_resources kind=nodes returns 0" "$CAROL_MCP_NODES"

echo
echo -e "${B}=== Multi-namespace user dashboard aggregates only allowed namespaces (dave) ===${N}"
# dave has view in both NS_ALICE and NS_BOB. He should see counts that sum
# only those two namespaces, not the whole cluster — the dashboard must
# iterate per allowed namespace rather than collapsing to a cluster-wide
# call.

DAVE_DASH=$(as_user dave GET /api/dashboard)
DAVE_DASH_PODS=$(echo "$DAVE_DASH" | jq -r '.resourceCounts.pods.total // 0')
# Expect exactly the two test pods (rbac-test-pod-a + rbac-test-pod-b).
if [[ "$DAVE_DASH_PODS" == "2" ]]; then
  ok "dave's dashboard pod count = 2 (NS_ALICE + NS_BOB only)"
else
  fail "dave's dashboard pod count = $DAVE_DASH_PODS; expected 2 (would be much higher if cluster-wide leaked)"
fi

# dave doesn't have list-nodes — node count should be 0.
DAVE_DASH_NODES=$(echo "$DAVE_DASH" | jq -r '.resourceCounts.nodes.total // 0')
if [[ "$DAVE_DASH_NODES" == "0" ]]; then
  ok "dave's dashboard node count = 0 (no list-nodes RBAC)"
else
  fail "dave's dashboard node count = $DAVE_DASH_NODES; expected 0"
fi

# dave doesn't have list-namespaces cluster-wide — ns count should be 0.
DAVE_DASH_NS=$(echo "$DAVE_DASH" | jq -r '.resourceCounts.namespaces // 0')
if [[ "$DAVE_DASH_NS" == "0" ]]; then
  ok "dave's dashboard namespaces count = 0 (no list-ns cluster-wide RBAC)"
else
  fail "dave's dashboard namespaces count = $DAVE_DASH_NS; expected 0"
fi

DAVE_NS=$(as_user dave GET /api/cluster/namespace-scope | jq -r '.accessibleNamespaces | sort | join(",")')
if [[ "$DAVE_NS" == "${NS_ALICE},${NS_BOB}" ]]; then
  ok "dave's accessibleNamespaces = [${NS_ALICE},${NS_BOB}]"
else
  fail "dave's accessibleNamespaces = [$DAVE_NS]; expected [${NS_ALICE},${NS_BOB}]"
fi

# Multi-namespace pick: dave selects both his allowed namespaces. Round-trip
# the slice and verify dashboards/pods stay scoped to exactly that set.
as_user dave POST /api/cluster/namespace "{\"namespaces\":[\"$NS_ALICE\",\"$NS_BOB\"]}" >/dev/null
DAVE_PICKS=$(as_user dave GET /api/cluster/namespace-scope | jq -r '.actives | sort | join(",")')
if [[ "$DAVE_PICKS" == "${NS_ALICE},${NS_BOB}" ]]; then
  ok "dave's multi-pick round-tripped as [${NS_ALICE},${NS_BOB}]"
else
  fail "dave's multi-pick = [$DAVE_PICKS]; expected [${NS_ALICE},${NS_BOB}]"
fi
DAVE_DASH_MULTI_PODS=$(as_user dave GET /api/dashboard | jq -r '.resourceCounts.pods.total // 0')
if [[ "$DAVE_DASH_MULTI_PODS" == "2" ]]; then
  ok "dave's dashboard with multi-pick still shows both ns pods (2)"
else
  fail "dave's dashboard with multi-pick = $DAVE_DASH_MULTI_PODS pods; expected 2"
fi
# Reset dave's pick so subsequent assertions see his unpinned view.
as_user dave POST /api/cluster/namespace '{"namespaces":[]}' >/dev/null

echo
echo -e "${B}=== Cluster-scoped CRDs not in the static map are gated via discovery (carol, dave) ===${N}"
# RadarTest is a cluster-scoped CRD that's NOT in the static IsClusterOnlyKind
# list. The discovery-based gate must catch it: only cluster-admin or
# explicitly-permitted users should see it.

# carol has cluster-wide pods+namespaces but NOT RadarTest read.
CAROL_REST_RT=$(as_user carol GET /api/resources/radartests | arr_len)
expect_zero "REST carol cannot list radartests (cluster-scoped CRD without explicit RBAC)" "$CAROL_REST_RT"

# Same via the grouped-path:
CAROL_REST_RT_GROUP=$(curl -s -H "X-Forwarded-User: carol" \
  "http://localhost:$PORT/api/resources/radartests?group=test.skyhook.io" | arr_len)
expect_zero "REST carol cannot list radartests via ?group= path" "$CAROL_REST_RT_GROUP"

# MCP path:
CAROL_MCP_RT=$(mcp_call carol list_resources '{"kind":"radartests"}' | arr_len)
expect_zero "MCP carol list_resources kind=radartests returns 0" "$CAROL_MCP_RT"

# dave (multi-namespace user) also can't see cluster-scoped CRDs without explicit RBAC.
DAVE_REST_RT=$(as_user dave GET /api/resources/radartests | arr_len)
expect_zero "REST dave cannot list radartests" "$DAVE_REST_RT"

echo
echo -e "${B}=== Cluster-scoped reads aren't blocked by the noNamespaceAccess short-circuit (erin) ===${N}"
# erin has list-nodes ONLY, no namespace access. parseNamespacesForUser
# returns [] (empty allowed), so the cluster-only gate must run BEFORE the
# noNamespaceAccess short-circuit — otherwise her cluster-scoped read is
# denied even though she has the RBAC for it.

ERIN_NS=$(as_user erin GET /api/cluster/namespace-scope | jq -r '.accessibleNamespaces | sort | join(",")')
# erin has no namespace access — accessibleNamespaces should be empty.
if [[ -z "$ERIN_NS" ]]; then
  ok "erin's accessibleNamespaces is empty (correct: no namespace RBAC)"
else
  fail "erin's accessibleNamespaces = [$ERIN_NS]; expected empty"
fi

ERIN_NODES=$(as_user erin GET /api/resources/nodes | arr_len)
expect_at_least_one "REST erin can list nodes despite no namespace access" "$ERIN_NODES"

ERIN_PODS=$(as_user erin GET /api/resources/pods | arr_len)
expect_zero "REST erin cannot list pods (no namespace RBAC)" "$ERIN_PODS"

ERIN_MCP_NODES=$(mcp_call erin list_resources '{"kind":"nodes"}' | arr_len)
if [[ "$ERIN_MCP_NODES" -ge 1 ]]; then
  ok "MCP erin list_resources kind=nodes returns ≥1"
else
  fail "MCP erin got $ERIN_MCP_NODES nodes; expected ≥1"
fi

echo
echo -e "${B}=== Dashboard CRD counts are filtered per-user ===${N}"
# carol can see her cluster-wide pods+namespaces, but not radartests. Her
# dashboard/crds endpoint should NOT include radartests.
CAROL_DASH_CRDS=$(as_user carol GET /api/dashboard/crds | jq '[.topCRDs[]?.kind] | join(",")' 2>/dev/null || echo "")
if [[ "$CAROL_DASH_CRDS" != *"RadarTest"* ]]; then
  ok "carol's /api/dashboard/crds does not include cluster-scoped RadarTest"
else
  fail "carol's /api/dashboard/crds leaked RadarTest: $CAROL_DASH_CRDS"
fi

# dave (multi-namespace) — same: shouldn't include cluster-scoped CRDs.
DAVE_DASH_CRDS=$(as_user dave GET /api/dashboard/crds | jq '[.topCRDs[]?.kind] | join(",")' 2>/dev/null || echo "")
if [[ "$DAVE_DASH_CRDS" != *"RadarTest"* ]]; then
  ok "dave's /api/dashboard/crds does not include cluster-scoped RadarTest"
else
  fail "dave's /api/dashboard/crds leaked RadarTest: $DAVE_DASH_CRDS"
fi

echo
echo -e "${B}=== Per-namespace Secret RBAC (alice's view role excludes secrets) ===${N}"
# The K8s `view` ClusterRole does NOT include secrets. The chart can grant
# the SA cluster-wide secrets (rbac.helm / auth.mode != "none" / etc.), so
# the cache may hold rbac-test-secret-a; the server's per-namespace
# `list secrets` SAR must gate the read. alice's view role fails that SAR
# and she sees nothing. frank (same namespace, plus explicit secret-reader
# Role) sees the secret.

ALICE_SECRETS=$(as_user alice GET /api/resources/secrets | arr_len)
expect_zero "REST alice cannot list secrets (view role excludes them)" "$ALICE_SECRETS"

ALICE_SECRET_GET_HTTP=$(curl -s -o /dev/null -w '%{http_code}' -H "X-Forwarded-User: alice" \
  "http://localhost:$PORT/api/resources/secrets/$NS_ALICE/rbac-test-secret-a")
if [[ "$ALICE_SECRET_GET_HTTP" == "403" ]]; then
  ok "REST alice GET secret returns 403"
else
  fail "REST alice GET secret returned $ALICE_SECRET_GET_HTTP; expected 403"
fi

ALICE_MCP_SECRETS=$(mcp_call alice list_resources '{"kind":"secrets"}' | arr_len)
expect_zero "MCP alice list_resources kind=secrets returns 0" "$ALICE_MCP_SECRETS"

# MCP get_resource returns an error string on forbidden — not a structured
# 403. Check for the substring instead of HTTP code.
ALICE_MCP_SECRET_GET=$(mcp_call alice get_resource "{\"kind\":\"secrets\",\"namespace\":\"$NS_ALICE\",\"name\":\"rbac-test-secret-a\"}")
if [[ -z "$ALICE_MCP_SECRET_GET" ]] || ! echo "$ALICE_MCP_SECRET_GET" | jq -e '.name == "rbac-test-secret-a"' >/dev/null 2>&1; then
  ok "MCP alice get_resource secrets denied (no rbac-test-secret-a in payload)"
else
  fail "MCP alice get_resource leaked secret: $ALICE_MCP_SECRET_GET"
fi

ALICE_SEARCH_SECRETS=$(as_user alice GET "/api/search?q=kind:Secret" | jq -r '[.hits[]?.name] | join(",")' 2>/dev/null || echo "ERR")
if [[ "$ALICE_SEARCH_SECRETS" != *"rbac-test-secret-a"* ]]; then
  ok "REST search kind:Secret hides rbac-test-secret-a from alice"
else
  fail "REST search leaked secret to alice: [$ALICE_SEARCH_SECRETS]"
fi

# frank: positive control. Same namespace ceiling as alice, but with explicit
# secret-reader Role bound. He must see the secret.
FRANK_SECRETS=$(as_user frank GET /api/resources/secrets | jq -r '[.[].metadata.name] | join(",")')
if [[ "$FRANK_SECRETS" == *"rbac-test-secret-a"* ]]; then
  ok "REST frank can list secrets (explicit secret-reader Role)"
else
  fail "REST frank should see rbac-test-secret-a, got: [$FRANK_SECRETS]"
fi

FRANK_SECRET_GET_HTTP=$(curl -s -o /dev/null -w '%{http_code}' -H "X-Forwarded-User: frank" \
  "http://localhost:$PORT/api/resources/secrets/$NS_ALICE/rbac-test-secret-a")
if [[ "$FRANK_SECRET_GET_HTTP" == "200" ]]; then
  ok "REST frank GET secret returns 200"
else
  fail "REST frank GET secret returned $FRANK_SECRET_GET_HTTP; expected 200"
fi

FRANK_SEARCH_SECRETS=$(as_user frank GET "/api/search?q=kind:Secret" | jq -r '[.hits[]?.name] | join(",")' 2>/dev/null || echo "ERR")
if [[ "$FRANK_SEARCH_SECRETS" == *"rbac-test-secret-a"* ]]; then
  ok "REST search kind:Secret shows rbac-test-secret-a to frank"
else
  fail "REST search missing secret for frank: [$FRANK_SEARCH_SECRETS]"
fi

# --- Summary ---
echo
if [[ "$FAILS" -eq 0 ]]; then
  echo -e "${G}All checks passed.${N}"
  exit 0
else
  echo -e "${R}$FAILS check(s) failed. See log: /tmp/radar-rbac-test-$PORT.log${N}"
  exit 1
fi
