#!/usr/bin/env bash
# Smoke tests for the radar Helm chart's template rendering.
#
# Exercises the self-upgrade toggle paths: the chart was silently clobbered
# by release.yml's wholesale-replace sync once (helm-charts@c68795c wiped
# helm-charts#9). Golden-string assertions here pin the rendered output so
# the next regression fails the build instead of shipping silently.
#
# Usage:
#   ./scripts/test-chart.sh

set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/deploy/helm/radar"
FAIL=0
CASE=""

fail() {
  echo "    ✗ $1"
  FAIL=1
}
pass() {
  echo "    ✓ $1"
}

assert_contains() {
  local pattern="$1" label="$2"
  if echo "$OUT" | grep -Eq "$pattern"; then pass "$label"
  else fail "$label — no match for: $pattern"; fi
}

assert_not_contains() {
  local pattern="$1" label="$2"
  if echo "$OUT" | grep -Eq "$pattern"; then fail "$label — unexpected match for: $pattern"
  else pass "$label"; fi
}

render() {
  CASE="$1"; shift
  echo "  Case: $CASE"
  OUT=$(helm template radar "$CHART_DIR" "$@" 2>&1) || {
    fail "helm template failed"
    echo "$OUT" | sed 's/^/      /'
    return
  }
}

echo "Running chart template tests against $CHART_DIR"
echo

render "defaults — no self-upgrade footprint"
assert_not_contains '^kind: Role$'                  "no namespaced Role"
assert_not_contains '^kind: RoleBinding$'           "no namespaced RoleBinding"
assert_not_contains 'MY_POD_NAMESPACE'              "no downward-API env var"
assert_not_contains 'MY_DEPLOYMENT_NAME'            "no deployment-name env var"
assert_not_contains 'self-upgrade'                  "no self-upgrade references anywhere"
echo

render "rbac.selfUpgrade=true — full feature wiring" --set rbac.selfUpgrade=true
assert_contains '^kind: Role$'                      "namespaced Role emitted"
assert_contains '^kind: RoleBinding$'               "namespaced RoleBinding emitted"
assert_contains 'name: radar-self-upgrade$'         "names match fullname-self-upgrade convention"
assert_contains 'resourceNames: \["radar"\]'        "Role restricted via resourceNames to the Deployment"
assert_contains 'verbs: \["get", "patch"\]'         "verbs scoped to get+patch"
assert_contains 'apiGroups: \["apps"\]'             "apiGroup scoped to apps"
assert_contains 'resources: \["deployments"\]'      "resource scoped to deployments"
assert_contains 'name: radar$'                      "RoleBinding subject is radar SA"
assert_contains 'MY_POD_NAMESPACE'                  "downward-API namespace env var injected"
assert_contains 'fieldPath: metadata.namespace'     "namespace sourced from downward API"
assert_contains 'MY_DEPLOYMENT_NAME'                "deployment-name env var injected"
echo

render "cloud.enabled=true alone — does NOT auto-enable self-upgrade" \
  --set cloud.enabled=true --set cloud.url=wss://x --set cloud.token=t --set cloud.clusterName=c
assert_not_contains 'MY_POD_NAMESPACE'              "env vars absent without explicit rbac.selfUpgrade"
assert_not_contains 'self-upgrade'                  "no Role/RoleBinding without explicit opt-in"
echo

render "rbac.create=false + rbac.selfUpgrade=true — feature still works" \
  --set rbac.create=false --set rbac.selfUpgrade=true
# `rbac.create` is the master switch for the big cluster-wide ClusterRole —
# a BYO-RBAC user needs that to live outside the chart. The self-upgrade
# Role is narrow (get+patch on THIS Deployment by resourceNames), so it
# doesn't belong under that switch; cloud-rbac.yaml already follows the
# same "feature gates itself, independent of rbac.create" pattern. Either
# the feature is on end-to-end or it's off end-to-end — no silent 403.
assert_contains '^kind: Role$'                      "Role still emitted — narrow scope, independent of rbac.create"
assert_contains '^kind: RoleBinding$'               "RoleBinding still emitted"
assert_not_contains '^kind: ClusterRole$'           "no ClusterRole when rbac.create=false (big ClusterRole still suppressed)"
assert_contains 'MY_POD_NAMESPACE'                  "env vars injected — feature is fully wired"
echo

if [[ $FAIL -eq 0 ]]; then
  echo "All chart template tests passed."
  exit 0
else
  echo "One or more assertions failed."
  exit 1
fi
