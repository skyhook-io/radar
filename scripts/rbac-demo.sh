#!/usr/bin/env bash
# Seed a curated set of RBAC scenarios into the current kubectl context, for
# visual-testing the SA / Role / RoleBinding / Pod-permissions UI. The
# scenarios exercise: a healthy SA bound to a small Role; a SA bound to a
# wildcard ClusterRole; a system:authenticated grant; an orphan binding;
# and a SA with no bindings.
#
# Idempotent — safe to re-run. Cleans up by deleting the demo namespace
# (rbac-demo) and the cluster-wide objects with the `radar-rbac-demo=true`
# label.
#
# Subcommands:
#   up      Apply the fixtures.
#   down    Delete everything labelled radar-rbac-demo=true plus the namespace.
#   status  Print what's there.
#   help    Show this message.
#
# This script does NOT create a cluster. Point your KUBECONFIG at any cluster
# you're OK polluting with RBAC test fixtures (kind, dev, etc.) and run.
# Recommended pairing: `kind create cluster --name radar-rbac-demo` first.

set -euo pipefail

NS="${RBAC_DEMO_NS:-rbac-demo}"
LABEL="radar-rbac-demo=true"

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BLUE='\033[34m'; C_GREEN='\033[32m'; C_DIM='\033[2m'; C_RED='\033[31m'; C_RESET='\033[0m'
else
  C_BLUE=''; C_GREEN=''; C_DIM=''; C_RED=''; C_RESET=''
fi

step() { printf "${C_BLUE}==> %s${C_RESET}\n" "$1"; }
ok()   { printf "${C_GREEN}    ✓ %s${C_RESET}\n" "$1"; }
note() { printf "${C_DIM}    %s${C_RESET}\n" "$1"; }
fail() { printf "${C_RED}    ✗ %s${C_RESET}\n" "$1"; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 not found in PATH"
}

apply_fixtures() {
  step "Verifying cluster access"
  require_cmd kubectl
  if ! kubectl version --request-timeout=3s >/dev/null 2>&1; then
    fail "kubectl can't reach a cluster — check KUBECONFIG / current-context"
  fi
  local ctx
  ctx="$(kubectl config current-context)"
  ok "context: ${ctx}"

  step "Creating namespace ${NS}"
  kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl label namespace "${NS}" "${LABEL}" --overwrite >/dev/null
  ok "namespace ready"

  step "Applying RBAC fixtures"
  kubectl apply -f - <<EOF
---
# ── Scenario 1: healthy SA + Role + RoleBinding ──────────────────────────
# Exercises: Direct Bindings + Effective Permissions on SA detail page;
# Bindings reverse-lookup on Role detail page; Pod Permissions section.
apiVersion: v1
kind: ServiceAccount
metadata:
  name: app-sa
  namespace: ${NS}
  labels:
    radar-rbac-demo: "true"
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: app-reader
  namespace: ${NS}
  labels:
    radar-rbac-demo: "true"
rules:
  - apiGroups: [""]
    resources: ["pods", "configmaps"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: app-binding
  namespace: ${NS}
  labels:
    radar-rbac-demo: "true"
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: app-reader
subjects:
  - kind: ServiceAccount
    name: app-sa
    namespace: ${NS}
---
apiVersion: v1
kind: Pod
metadata:
  name: app-pod
  namespace: ${NS}
  labels:
    radar-rbac-demo: "true"
spec:
  serviceAccountName: app-sa
  containers:
    - name: app
      image: registry.k8s.io/pause:3.10
---
# ── Scenario 2: wildcard ClusterRole + SA + Pod ──────────────────────────
# Exercises: blast-radius warning banner on both SA and Pod detail pages.
apiVersion: v1
kind: ServiceAccount
metadata:
  name: risky-sa
  namespace: ${NS}
  labels:
    radar-rbac-demo: "true"
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: radar-rbac-demo-wild
  labels:
    radar-rbac-demo: "true"
rules:
  - apiGroups: ["*"]
    resources: ["*"]
    verbs: ["*"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: radar-rbac-demo-wild-bind
  labels:
    radar-rbac-demo: "true"
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: radar-rbac-demo-wild
subjects:
  - kind: ServiceAccount
    name: risky-sa
    namespace: ${NS}
---
apiVersion: v1
kind: Pod
metadata:
  name: risky-pod
  namespace: ${NS}
  labels:
    radar-rbac-demo: "true"
spec:
  serviceAccountName: risky-sa
  containers:
    - name: app
      image: registry.k8s.io/pause:3.10
---
# ── Scenario 3: system:authenticated grant ───────────────────────────────
# Exercises: wide-group warning on the RoleBinding detail page; Inherited
# section on SA detail pages.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: radar-rbac-demo-too-wide
  labels:
    radar-rbac-demo: "true"
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: view
subjects:
  - kind: Group
    name: system:authenticated
    apiGroup: rbac.authorization.k8s.io
---
# ── Scenario 4: orphan RoleBinding referencing a missing Role ────────────
# Exercises: "Could not resolve referenced role" message in the inline
# rules preview on RoleBinding detail; "orphan binding" hint on SA page.
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: orphan-binding
  namespace: ${NS}
  labels:
    radar-rbac-demo: "true"
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: this-role-does-not-exist
subjects:
  - kind: ServiceAccount
    name: app-sa
    namespace: ${NS}
---
# ── Scenario 5: SA with no bindings ──────────────────────────────────────
# Exercises: empty-state on SA detail page.
apiVersion: v1
kind: ServiceAccount
metadata:
  name: lonely-sa
  namespace: ${NS}
  labels:
    radar-rbac-demo: "true"
EOF

  ok "fixtures applied"
  note "namespace:     ${NS}"
  note "SAs:           app-sa, risky-sa, lonely-sa"
  note "Pods:          app-pod, risky-pod"
  note "Role:          app-reader (ns: ${NS})"
  note "ClusterRoles:  radar-rbac-demo-wild"
  note "Bindings:      app-binding, orphan-binding (ns: ${NS}); radar-rbac-demo-wild-bind, radar-rbac-demo-too-wide"
}

remove_fixtures() {
  step "Removing demo objects"
  kubectl delete namespace "${NS}" --ignore-not-found
  kubectl delete clusterrole -l "${LABEL}" --ignore-not-found
  kubectl delete clusterrolebinding -l "${LABEL}" --ignore-not-found
  ok "removed"
}

show_status() {
  step "Demo namespace ${NS}"
  kubectl get sa,role,rolebinding -n "${NS}" -l "${LABEL}" 2>/dev/null || echo "  (none)"
  step "Cluster-scoped demo objects"
  kubectl get clusterrole,clusterrolebinding -l "${LABEL}" 2>/dev/null || echo "  (none)"
}

usage() {
  cat <<EOF
Usage: $(basename "$0") {up|down|status|help}

  up      Apply RBAC fixtures to the current kubectl context.
  down    Remove all fixtures (delete labelled cluster objects + the namespace).
  status  Show what's currently installed from this demo.

Override RBAC_DEMO_NS to use a different namespace (default: rbac-demo).
EOF
}

case "${1:-up}" in
  up)     apply_fixtures ;;
  down)   remove_fixtures ;;
  status) show_status ;;
  help|-h|--help) usage ;;
  *)      usage; exit 1 ;;
esac
