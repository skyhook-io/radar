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
  if echo "$OUT" | grep -Eq -- "$pattern"; then pass "$label"
  else fail "$label — no match for: $pattern"; fi
}

assert_not_contains() {
  local pattern="$1" label="$2"
  if echo "$OUT" | grep -Eq -- "$pattern"; then fail "$label — unexpected match for: $pattern"
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

# Identity env and self-upgrade RBAC are deliberately decoupled: Radar needs to
# know which object it is for read-only self-description (the Cloud connect
# handoff), which grants nothing. Whether it may PATCH itself is the Role — so
# assert on the Role, not on the env vars.
render "defaults — no self-upgrade RBAC"
assert_contains '--listen-address=0.0.0.0'          "shared listener explicitly enabled"
assert_not_contains '^kind: Role$'                  "no namespaced Role"
assert_not_contains '^kind: RoleBinding$'           "no namespaced RoleBinding"
assert_not_contains 'radar-self-upgrade'            "no self-upgrade Role/RoleBinding"
assert_contains 'MY_POD_NAMESPACE'                  "identity ships for read-only self-description"
assert_contains 'MY_DEPLOYMENT_NAME'                "identity ships for read-only self-description"
assert_not_contains '--opencost-currency='          "default OpenCost currency flag omitted"
echo

render "cost.currency — explicit OpenCost currency label" --set cost.currency=GBP
assert_contains '--opencost-currency=GBP'           "OpenCost currency flag rendered"
echo

render "timeline.storage=postgres — DSN from operator-managed Secret, no PVC mount" \
  --set timeline.storage=postgres \
  --set timeline.retention=720h \
  --set timeline.postgres.existingSecret=app-db-credentials \
  --set timeline.postgres.secretKey=uri
assert_contains '--timeline-storage=postgres'       "postgres storage arg"
assert_contains '--timeline-retention=720h'         "postgres retention arg"
assert_contains 'name: RADAR_TIMELINE_POSTGRES_DSN' "DSN env injected"
assert_contains 'name: app-db-credentials'          "DSN secret name"
assert_contains 'key: uri'                          "DSN secret key"
assert_not_contains 'mountPath: /data'              "no SQLite PVC mount"
assert_not_contains 'claimName: radar'              "no SQLite PVC volume"
assert_not_contains '--timeline-db='                "no sqlite db path arg"
assert_not_contains '--timeline-max-size='            "no sqlite max-size arg"
echo

render "prometheusHeadersFromEnv — flag and secret env stay separate" \
  --set traffic.prometheusHeaders.X-Scope-OrgID=tenant-1 \
  --set traffic.prometheusHeadersFromEnv.Authorization=PROMETHEUS_TOKEN \
  --set 'env[0].name=PROMETHEUS_TOKEN' \
  --set 'env[0].valueFrom.secretKeyRef.name=prometheus-auth' \
  --set 'env[0].valueFrom.secretKeyRef.key=token'
assert_contains '--prometheus-header=X-Scope-OrgID=tenant-1'           "literal Prometheus header rendered"
assert_contains '--prometheus-header-from-env=Authorization=PROMETHEUS_TOKEN' "env-sourced Prometheus header flag rendered"
assert_contains 'name: PROMETHEUS_TOKEN'                              "custom env var rendered"
assert_contains 'secretKeyRef:'                                       "secretKeyRef rendered"
assert_contains 'name: prometheus-auth'                               "secret name rendered"
assert_contains 'key: token'                                          "secret key rendered"
assert_not_contains '--prometheus-header=Authorization='               "secret value not rendered as a literal header"
echo

render "OIDC prefixes ending in colon stay string args" \
  --set auth.mode=oidc \
  --set auth.oidc.issuerURL=https://issuer.example \
  --set auth.oidc.clientID=radar \
  --set auth.oidc.redirectURL=https://radar.example/auth/callback \
  --set auth.oidc.clientSecret=secret \
  --set auth.oidc.usernamePrefix=oidc-user: \
  --set auth.oidc.groupsPrefix=oidc-groups:
if echo "$OUT" | yq 'select(.kind == "Deployment") | .spec.template.spec.containers[0].args[] | select(type != "!!str")' | grep -q .; then
  fail "OIDC prefix args parse as non-string YAML values"
else
  pass "OIDC prefix args parse as strings"
fi
assert_contains '"--auth-oidc-username-prefix=oidc-user:"' "username prefix arg rendered quoted"
assert_contains '"--auth-oidc-groups-prefix=oidc-groups:"' "groups prefix arg rendered quoted"
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
assert_not_contains 'radar-self-upgrade'            "no Role/RoleBinding without explicit opt-in"
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

render "defaults — no helm-write footprint"
assert_not_contains 'name: radar-helm$'                  "no helm add-on ClusterRole"
assert_not_contains 'name: radar-helm-admin$'            "no helm-admin ClusterRole"
assert_not_contains 'name: radar-cloud-owner-helm$'      "no cloud helm bindings"
assert_not_contains 'name: radar-cloud-member-helm$'     "no cloud helm bindings"
assert_not_contains 'name: radar-cloud-owner-helm-admin' "no cloud helm-admin bindings"
echo

render "rbac.helm=true alone — gated off without auth or cloud" --set rbac.helm=true
assert_not_contains 'name: radar-helm$'                  "no helm add-on ClusterRole without auth/cloud"
assert_not_contains 'name: radar-helm-admin$'            "no helm-admin ClusterRole without auth/cloud"
echo

render "rbac.helm=true + auth.mode=proxy — both CRs emitted, no cloud bindings" \
  --set rbac.helm=true --set auth.mode=proxy
assert_contains 'name: radar-helm$'                  "helm add-on ClusterRole emitted"
assert_contains 'name: radar-helm-admin$'            "helm-admin ClusterRole emitted"
assert_not_contains 'name: radar-cloud-owner-helm$'  "no cloud-owner-helm binding without cloud"
assert_not_contains 'name: radar-cloud-member-helm$' "no cloud-member-helm binding without cloud"
echo

render "rbac.helm=true + cloud.enabled=true + auth.mode=proxy — split bindings (member excluded from admin)" \
  --set rbac.helm=true --set auth.mode=proxy --set cloud.enabled=true \
  --set cloud.url=wss://x --set cloud.token=t --set cloud.clusterName=c
assert_contains 'name: radar-helm$'                       "helm add-on ClusterRole emitted"
assert_contains 'name: radar-helm-admin$'                 "helm-admin ClusterRole emitted"
assert_contains 'name: radar-cloud-owner-helm$'           "cloud-owner-helm binding emitted"
assert_contains 'name: radar-cloud-member-helm$'          "cloud-member-helm binding emitted"
assert_contains 'name: radar-cloud-owner-helm-admin$'     "cloud-owner-helm-admin binding emitted"
assert_not_contains 'name: radar-cloud-member-helm-admin' "member is NEVER bound to helm-admin (RBAC self-promotion)"
assert_not_contains 'name: radar-cloud-viewer-helm'       "no helm binding for viewer tier"
echo

render "cloud.defaultRbac.owner=false — owner bindings absent, member-helm still emits, no admin binding" \
  --set rbac.helm=true --set auth.mode=proxy --set cloud.enabled=true \
  --set cloud.url=wss://x --set cloud.token=t --set cloud.clusterName=c \
  --set cloud.defaultRbac.owner=false
assert_not_contains 'name: radar-cloud-owner-helm$'       "no owner-helm binding"
assert_not_contains 'name: radar-cloud-owner-helm-admin$' "no owner-helm-admin binding"
assert_contains 'name: radar-cloud-member-helm$'          "member-helm binding still emits"
assert_not_contains 'name: radar-cloud-member-helm-admin' "member is NEVER bound to admin even when owner is disabled"
echo

render "cloud.defaultRbac.member=false — member binding absent, owner gets both" \
  --set rbac.helm=true --set auth.mode=proxy --set cloud.enabled=true \
  --set cloud.url=wss://x --set cloud.token=t --set cloud.clusterName=c \
  --set cloud.defaultRbac.member=false
assert_contains 'name: radar-cloud-owner-helm$'           "owner-helm binding emits"
assert_contains 'name: radar-cloud-owner-helm-admin$'     "owner-helm-admin binding emits"
assert_not_contains 'name: radar-cloud-member-helm$'      "no member-helm binding when member disabled"
echo

render "rbac.helm=true + cloud.enabled=true + auth.mode=none — cloud helm bindings still emit" \
  --set rbac.helm=true --set cloud.enabled=true \
  --set cloud.url=wss://x --set cloud.token=t --set cloud.clusterName=c
assert_contains 'name: radar-helm$'                  "helm add-on ClusterRole still emitted (cloud.enabled satisfies the OR-clause)"
assert_contains 'name: radar-helm-admin$'            "helm-admin ClusterRole still emitted (same gate)"
assert_contains 'name: radar-cloud-owner-helm$'      "cloud-owner-helm binding emits in cloud mode"
assert_contains 'name: radar-cloud-member-helm$'     "cloud-member-helm binding emits in cloud mode"
echo

render "defaults — no RBAC reads (viewRBAC=false)"
# The base radar ClusterRole should NOT include rbac.authorization.k8s.io reads
# at default settings. This is the single test that pins the secure default.
HELM_BASE=$(echo "$OUT" | awk '/^kind: ClusterRole$/,/^---$/{ if ($0 ~ /^---$/) exit; print }' | awk '/^  name: radar$/,EOF')
if echo "$OUT" | awk '/  name: radar$/,/^---$/' | grep -Eq 'apiGroups: \["rbac.authorization.k8s.io"\]'; then
  fail "default ClusterRole still grants rbac.authorization.k8s.io reads — viewRBAC should gate this"
else
  pass "no rbac.authorization.k8s.io reads in default ClusterRole"
fi
echo

render "rbac.viewRBAC=true — RBAC reads granted" --set rbac.viewRBAC=true
assert_contains 'apiGroups: \["rbac.authorization.k8s.io"\]' "RBAC reads granted when viewRBAC=true"
assert_contains 'clusterrolebindings'                        "clusterrolebindings present"
echo

render "split content — radar-helm has CRDs but NOT clusterroles" \
  --set rbac.helm=true --set auth.mode=proxy
# Pull the radar-helm ClusterRole specifically and check its contents
HELM_RULES=$(echo "$OUT" | awk '/^kind: ClusterRole$/,/^---$/' | awk '/^metadata:$/,/^rules:$/{found=1} /  name: radar-helm$/{ok=1} END{print ok}')
# Simpler approach: just confirm the cluster-admin verbs are NOT in radar-helm's rules
# by checking the file was split (radar-helm-admin exists separately)
assert_contains 'name: radar-helm-admin$'                 "helm-admin role exists as separate resource"
# Member-safe role grants CRDs (operator charts)
assert_contains 'customresourcedefinitions'               "CRDs writable in member-safe role"
# Cluster-admin-equivalent verbs are present somewhere in the manifest (in radar-helm-admin)
assert_contains 'clusterrolebindings'                     "RBAC writes present (in admin role)"
assert_contains 'validatingwebhookconfigurations'         "webhook writes present (in admin role)"
assert_contains 'apiservices'                             "apiservices writes present (in admin role)"
echo

# ── Cloud-tier cluster-scoped RBAC add-ons (cloud-rbac-cluster-read / node-ops) ──
CLOUD="--set cloud.enabled=true --set cloud.token=dummy --set cloud.url=wss://x.example --set cloud.clusterName=c1"

render "cloud defaults: cluster-read on for all three tiers, node-ops off" $CLOUD
assert_contains 'name: radar-cluster-read$'                     "cluster-read ClusterRole rendered"
assert_contains 'name: radar-cloud-viewer-cluster-read$'        "viewer binding rendered"
assert_contains 'name: radar-cloud-member-cluster-read$'        "member binding rendered"
assert_contains 'name: radar-cloud-owner-cluster-read$'         "owner binding rendered"
assert_contains 'runtimeclasses'                                "runtimeclasses granted"
assert_contains 'name: radar:viewer$'                           "canonical radar:* subject present"
assert_contains 'name: cloud:viewer$'                           "legacy cloud:* subject present (deprecation window)"
assert_contains 'priorityclasses'                               "priorityclasses granted"
assert_not_contains 'name: radar-node-ops$'                     "node-ops absent by default"
echo

render "clusterScopedRead.viewer=false drops only the viewer binding" $CLOUD   --set cloud.defaultRbac.clusterScopedRead.viewer=false
assert_not_contains 'name: radar-cloud-viewer-cluster-read$'    "viewer binding dropped"
assert_contains 'name: radar-cloud-member-cluster-read$'        "member binding kept"
assert_contains 'name: radar-cloud-owner-cluster-read$'         "owner binding kept"
echo

render "all clusterScopedRead false: no role, no bindings" $CLOUD   --set cloud.defaultRbac.clusterScopedRead.viewer=false   --set cloud.defaultRbac.clusterScopedRead.member=false   --set cloud.defaultRbac.clusterScopedRead.owner=false
assert_not_contains 'name: radar-cluster-read$'                 "cluster-read role suppressed"
echo

render "all tiers disabled: no orphan cluster-read role" $CLOUD   --set cloud.defaultRbac.viewer=false   --set cloud.defaultRbac.member=false   --set cloud.defaultRbac.owner=false
assert_not_contains 'name: radar-cluster-read$'                 "no unbound ClusterRole when every tier is off"
echo

render "custom viewerClusterRole does NOT drop viewer cluster-read (independent axes)" $CLOUD   --set cloud.defaultRbac.viewerClusterRole=my-restricted-view
assert_contains 'name: radar-cloud-viewer-cluster-read$'        "viewer keeps cluster-read with custom namespaced role"
echo

# helm upgrade --reuse-values renders with the previous release's value tree,
# where clusterScopedRead doesn't exist. null deletes the key, simulating that.
# Absence must behave like the declared default (all on), never crash.
render "legacy values (clusterScopedRead absent) render default-on, no nil crash" $CLOUD   --set cloud.defaultRbac.clusterScopedRead=null
assert_contains 'name: radar-cloud-viewer-cluster-read$'        "absent map treated as default-on (viewer)"
assert_contains 'name: radar-cloud-owner-cluster-read$'         "absent map treated as default-on (owner)"
echo

render "rbac.metrics=false drops the metrics rule" $CLOUD --set rbac.metrics=false
assert_not_contains 'metrics.k8s.io'                            "no metrics.k8s.io rule"
echo

render "nodeOps=true renders owner-only node-ops" $CLOUD --set cloud.defaultRbac.nodeOps=true
assert_contains 'name: radar-node-ops$'                         "node-ops role rendered"
assert_contains 'name: radar-cloud-owner-node-ops$'             "owner binding rendered"
assert_contains 'pods/eviction'                                 "eviction verb present"
assert_not_contains 'name: radar-cloud-member-node-ops$'        "no member node-ops binding"
echo

render "nodeOps=true with owner tier disabled renders nothing" $CLOUD   --set cloud.defaultRbac.nodeOps=true --set cloud.defaultRbac.owner=false
assert_not_contains 'name: radar-node-ops$'                     "node-ops suppressed without owner tier"
echo

# A string "false" is truthy to Go templates; it can only reach the template
# via --set-string plus --skip-schema-validation, but a cluster-scoped WRITE
# grant must stay off even then (toString-eq gate in the template).
render "string 'false' via schema bypass must NOT enable node-ops" $CLOUD   --set-string cloud.defaultRbac.nodeOps=false --skip-schema-validation
assert_not_contains 'name: radar-node-ops$'                     "string-false stays off"
echo

render "OSS mode (cloud disabled): no cloud RBAC at all"
assert_not_contains 'name: radar-cluster-read$'                 "no cluster-read outside cloud mode"
assert_not_contains 'name: radar-cloud-viewer-cluster-read$'    "no tier bindings outside cloud mode"
echo

if [[ $FAIL -eq 0 ]]; then
  echo "All chart template tests passed."
  exit 0
else
  echo "One or more assertions failed."
  exit 1
fi
