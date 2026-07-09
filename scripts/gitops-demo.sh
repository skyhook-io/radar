#!/usr/bin/env bash
# Bootstrap a kind cluster pre-populated with a curated set of GitOps
# scenarios for visual-testing the GitOps tab. Idempotent — can be re-run
# to refresh state or apply fixture updates without recreating the cluster.
#
# Subcommands:
#   up        Create cluster (if missing), install Argo CD + Flux, apply fixtures.
#   down      Delete the kind cluster.
#   drift     Induce drift on a healthy app (kubectl edit a managed Deployment).
#   rebreak   Re-break guestbook-broken-sync after remediation (deletes
#             demo-broken-sync namespace so the sync fails again).
#   reset     down + up.
#   status    Show what's installed and inventory the GitOps resources.
#   help      Show this message.
#
# Prerequisites:
#   - kind         https://kind.sigs.k8s.io/
#   - kubectl
#   - (optional) flux CLI for direct CR debugging — fixtures use kubectl apply
#
# Set CLUSTER_NAME=foo to use a different cluster (default: radar-gitops-demo).

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-gitops-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="${SCRIPT_DIR}/gitops-demo"

# Versions pinned so the demo behaves consistently across runs. Bump
# when you want a newer release; otherwise leave alone.
ARGOCD_VERSION="${ARGOCD_VERSION:-v2.13.2}"
FLUX_VERSION="${FLUX_VERSION:-v2.4.0}"

# Pretty colors for status output. Quietly turn off in non-interactive env.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BLUE='\033[34m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'; C_RED='\033[31m'; C_DIM='\033[2m'; C_RESET='\033[0m'
else
  C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_DIM=''; C_RESET=''
fi

step()    { printf "${C_BLUE}==> %s${C_RESET}\n" "$1"; }
ok()      { printf "${C_GREEN}    ✓ %s${C_RESET}\n" "$1"; }
warn()    { printf "${C_YELLOW}    ! %s${C_RESET}\n" "$1"; }
fail()    { printf "${C_RED}    ✗ %s${C_RESET}\n" "$1"; exit 1; }
note()    { printf "${C_DIM}    %s${C_RESET}\n" "$1"; }

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "$1 not found in PATH. Install: $2"
  fi
}

# --- Cluster lifecycle -----------------------------------------------------

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"
}

cmd_up() {
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation (or 'brew install kind')"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"

  if cluster_exists; then
    step "Cluster '${CLUSTER_NAME}' already exists — reusing"
  else
    step "Creating kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --wait 60s
    ok "Cluster created"
  fi

  kubectl --context "${KUBECTL_CTX}" cluster-info >/dev/null || fail "kind context not reachable"

  install_argocd
  install_flux
  apply_fixtures
  mint_radar_token
  print_summary
}

cmd_down() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  if cluster_exists; then
    step "Deleting cluster '${CLUSTER_NAME}'"
    kind delete cluster --name "${CLUSTER_NAME}"
    ok "Deleted"
  else
    warn "Cluster '${CLUSTER_NAME}' does not exist; nothing to do"
  fi
}

cmd_reset() {
  cmd_down
  cmd_up
}

# --- Argo CD ---------------------------------------------------------------

install_argocd() {
  step "Installing Argo CD (${ARGOCD_VERSION}) into 'argocd' namespace"

  kubectl --context "${KUBECTL_CTX}" create namespace argocd --dry-run=client -o yaml \
    | kubectl --context "${KUBECTL_CTX}" apply -f - >/dev/null

  # Apply official manifests at the pinned version. Idempotent.
  kubectl --context "${KUBECTL_CTX}" apply -n argocd \
    -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml" >/dev/null

  step "Waiting for Argo CD pods to be Ready (~60s)"
  kubectl --context "${KUBECTL_CTX}" -n argocd rollout status \
    deployment/argocd-server deployment/argocd-repo-server --timeout=180s >/dev/null
  kubectl --context "${KUBECTL_CTX}" -n argocd rollout status \
    statefulset/argocd-application-controller --timeout=180s >/dev/null
  ok "Argo CD healthy"
}

# --- Flux ------------------------------------------------------------------

install_flux() {
  step "Installing Flux (${FLUX_VERSION}) into 'flux-system' namespace"

  # Use the official install manifest. Bypasses the flux CLI so the
  # script works in CI without needing to install yet another tool.
  kubectl --context "${KUBECTL_CTX}" apply \
    -f "https://github.com/fluxcd/flux2/releases/download/${FLUX_VERSION}/install.yaml" >/dev/null

  step "Waiting for Flux controllers to be Ready (~60s)"
  kubectl --context "${KUBECTL_CTX}" -n flux-system rollout status \
    deployment/source-controller \
    deployment/kustomize-controller \
    deployment/helm-controller \
    deployment/notification-controller \
    --timeout=180s >/dev/null
  ok "Flux healthy"
}

# --- Demo fixtures ---------------------------------------------------------

apply_fixtures() {
  step "Applying GitOps demo fixtures"
  if [ ! -d "${FIXTURES_DIR}" ]; then
    fail "Fixtures dir not found: ${FIXTURES_DIR}"
  fi

  # Apply in number order so later resources can reference earlier ones
  # (e.g. AppProject before Application that uses it; GitRepository
  # before Kustomization that references it). Skip empty / comment-only
  # files so a placeholder YAML doesn't trip set -e on
  # "no objects passed to apply".
  for f in $(ls "${FIXTURES_DIR}"/*.yaml 2>/dev/null | sort); do
    if ! grep -q '^[[:space:]]*[^#[:space:]]' "$f"; then
      note "skipping $(basename "$f") (placeholder / comments only)"
      continue
    fi
    note "applying $(basename "$f")"
    kubectl --context "${KUBECTL_CTX}" apply -f "$f" >/dev/null
  done
  ok "Fixtures applied"

  setup_rollback_history
  setup_zombie
}

# wait_for_app_synced polls until an Argo Application reports Synced+Healthy.
# Used by setup_rollback_history to sequence the two syncs that produce
# the multi-entry history. Returns 1 on timeout so callers fail loudly
# rather than continue against a not-yet-converged state.
wait_for_app_synced() {
  local app="$1" deadline=$((SECONDS + 180))
  while [ $SECONDS -lt $deadline ]; do
    local sync health
    sync=$(kubectl --context "${KUBECTL_CTX}" -n argocd get application "$app" \
      -o jsonpath='{.status.sync.status}' 2>/dev/null || echo "")
    health=$(kubectl --context "${KUBECTL_CTX}" -n argocd get application "$app" \
      -o jsonpath='{.status.health.status}' 2>/dev/null || echo "")
    if [ "$sync" = "Synced" ] && [ "$health" = "Healthy" ]; then
      return 0
    fi
    sleep 2
  done
  return 1
}

# setup_rollback_history orchestrates the rollback fixture so it has at
# least two distinct successful syncs in status.history before the script
# returns control. The Application starts in auto-sync mode (so the first
# sync happens without manual intervention); we then change the path to
# trigger a second sync, wait for it to converge, and finally flip the
# app to Manual mode (rollback is gated on auto-sync being off — the
# controller would otherwise re-sync forward to HEAD immediately after
# any rollback attempt).
setup_rollback_history() {
  step "Orchestrating guestbook-rollback history"

  if ! wait_for_app_synced guestbook-rollback; then
    warn "guestbook-rollback didn't converge after first sync; skipping rollback orchestration"
    return
  fi

  # If we've already done this orchestration on a prior `up` run the path
  # is already kustomize-guestbook and the app is already Manual — skip
  # to keep the script idempotent.
  local current_path
  current_path=$(kubectl --context "${KUBECTL_CTX}" -n argocd get application guestbook-rollback \
    -o jsonpath='{.spec.source.path}' 2>/dev/null || echo "")
  if [ "$current_path" = "kustomize-guestbook" ]; then
    note "guestbook-rollback already at kustomize-guestbook (history orchestration done previously)"
  else
    note "patching path → kustomize-guestbook to trigger second sync"
    kubectl --context "${KUBECTL_CTX}" -n argocd patch application guestbook-rollback \
      --type merge -p '{"spec":{"source":{"path":"kustomize-guestbook"}}}' >/dev/null
    if ! wait_for_app_synced guestbook-rollback; then
      warn "guestbook-rollback didn't converge after path change; rollback button may stay disabled"
      return
    fi
  fi

  # Flip to Manual mode so the rollback button isn't auto-suppressed.
  # JSON patch to remove the `automated` block in place.
  kubectl --context "${KUBECTL_CTX}" -n argocd patch application guestbook-rollback \
    --type json -p '[{"op":"remove","path":"/spec/syncPolicy/automated"}]' >/dev/null 2>&1 || true

  local history_count
  history_count=$(kubectl --context "${KUBECTL_CTX}" -n argocd get application guestbook-rollback \
    -o jsonpath='{.status.history}' 2>/dev/null | python3 -c 'import sys,json; print(len(json.loads(sys.stdin.read() or "[]")))' 2>/dev/null || echo "?")
  ok "guestbook-rollback now Manual mode with ${history_count} history entries"
}

# setup_zombie applies the fake-finalizer trick: wait for the
# Kustomization to be Ready, patch in a fake finalizer that no controller
# owns, then delete the resource. Flux removes its own finalizer
# normally; the fake one blocks deletion forever, leaving the resource
# in a stable Terminating state for visual-testing the lifecycle UI.
# Other Flux apps are unaffected — none of the standard Flux controllers
# are stopped or interfered with.
setup_zombie() {
  step "Setting up zombie Kustomization"

  # If a zombie already exists from a prior run, skip — re-creating it
  # would require deleting the existing one first (which we can't do
  # because the fake finalizer holds it). The existing one is fine.
  if kubectl --context "${KUBECTL_CTX}" -n flux-system get kustomization zombie-kustomization \
       -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null | grep -q .; then
    ok "zombie-kustomization already in Terminating state (from previous run)"
    return
  fi

  # Wait for the Kustomization to reconcile at least once so it's a
  # realistic resource (not just a CR shell) before we kill it.
  local deadline=$((SECONDS + 90))
  while [ $SECONDS -lt $deadline ]; do
    if kubectl --context "${KUBECTL_CTX}" -n flux-system get kustomization zombie-kustomization \
         -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null | grep -qE 'True|False'; then
      break
    fi
    sleep 2
  done

  note "patching in fake finalizer (radar-demo.io/intentional-zombie)"
  kubectl --context "${KUBECTL_CTX}" -n flux-system patch kustomization zombie-kustomization \
    --type json -p '[{"op":"add","path":"/metadata/finalizers/-","value":"radar-demo.io/intentional-zombie"}]' \
    >/dev/null 2>&1 || true

  note "deleting zombie-kustomization (will block on fake finalizer)"
  # --wait=false because we *want* the deletion to hang.
  kubectl --context "${KUBECTL_CTX}" -n flux-system delete kustomization zombie-kustomization --wait=false >/dev/null 2>&1 || true

  ok "zombie-kustomization now stuck Terminating (severity will ramp: info → warning @5min → alert @30min)"
}

# --- Drift inducer ---------------------------------------------------------

cmd_drift() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  step "Inducing multi-field drift on demo-drift/guestbook"

  # Wait for the Deployment to exist (Argo may still be syncing on a
  # fresh `up` run).
  for i in $(seq 1 30); do
    if kubectl --context "${KUBECTL_CTX}" -n demo-drift get deployment guestbook-ui >/dev/null 2>&1; then
      break
    fi
    if [ "$i" -eq 30 ]; then
      fail "guestbook-ui deployment not found in demo-drift namespace after 30s — has the cluster fully synced?"
    fi
    sleep 1
  done

  # Multi-field drift. Each mutation exercises a different branch of
  # computeDriftFromLastApplied so the Changes tab renders a realistic
  # mix of scalar/nested/added/changed entries instead of a single line.
  #
  # 1. spec.replicas: scalar change (1 → 3)
  # 2. annotations: added entry on a nested map (operator-style label)
  # 3. resources.limits: added entry on a deeply nested map
  # The annotations + limits also produce drift entries that exercise
  # the nested-map recursion path, where the spec.replicas only hits
  # the top-level scalar branch.
  kubectl --context "${KUBECTL_CTX}" -n demo-drift scale deployment guestbook-ui --replicas=3 >/dev/null
  kubectl --context "${KUBECTL_CTX}" -n demo-drift annotate deployment guestbook-ui \
    radar-demo.io/induced-drift="multi-field" --overwrite >/dev/null
  # Add a CPU limit (Argo's last-applied won't have one) → "added" drift
  # entry on a nested path. Use a JSON patch so we replace just the
  # resources block without disturbing the rest of the container spec.
  kubectl --context "${KUBECTL_CTX}" -n demo-drift patch deployment guestbook-ui --type json -p '[
    {"op":"replace","path":"/spec/template/spec/containers/0/resources","value":{"limits":{"cpu":"500m","memory":"128Mi"}}}
  ]' >/dev/null 2>&1 || warn "couldn't add resources block (may already drift differently)"

  ok "Multi-field drift induced — Changes tab should show replicas, annotations, and resources entries"
}

# cmd_rebreak resets guestbook-broken-sync to its broken state after a user
# has remediated it (clicked "Create namespace" in Radar's failure card).
# Deletes the demo-broken-sync namespace so Argo's next sync attempt fails
# again with "namespaces \"demo-broken-sync\" not found", which is what the
# fixture is meant to exercise. Argo's retry counter climbs back to 5
# within ~30s of the retry backoff.
cmd_rebreak() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  step "Re-breaking guestbook-broken-sync"
  if kubectl --context "${KUBECTL_CTX}" get namespace demo-broken-sync >/dev/null 2>&1; then
    kubectl --context "${KUBECTL_CTX}" delete namespace demo-broken-sync >/dev/null
    ok "Deleted namespace demo-broken-sync — Argo will retry-fail over the next ~30s"
  else
    note "namespace demo-broken-sync already absent — fixture is already broken"
  fi
}

# --- Status ----------------------------------------------------------------

cmd_status() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"

  if ! cluster_exists; then
    warn "Cluster '${CLUSTER_NAME}' does not exist. Run: $0 up"
    return
  fi

  step "Cluster: ${CLUSTER_NAME} (context ${KUBECTL_CTX})"
  printf "\n${C_BLUE}Argo CD pods${C_RESET}\n"
  kubectl --context "${KUBECTL_CTX}" -n argocd get pods --no-headers 2>/dev/null \
    | awk '{ printf "    %s %s\n", $1, $3 }' || warn "no pods (controllers not installed?)"

  printf "\n${C_BLUE}Flux pods${C_RESET}\n"
  kubectl --context "${KUBECTL_CTX}" -n flux-system get pods --no-headers 2>/dev/null \
    | awk '{ printf "    %s %s\n", $1, $3 }' || warn "no pods (controllers not installed?)"

  printf "\n${C_BLUE}Argo Applications${C_RESET}\n"
  kubectl --context "${KUBECTL_CTX}" -n argocd get applications.argoproj.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s sync=%s health=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n${C_BLUE}Argo ApplicationSets${C_RESET}\n"
  kubectl --context "${KUBECTL_CTX}" -n argocd get applicationsets.argoproj.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s\n", $1 }' || note "(none)"

  printf "\n${C_BLUE}Flux Kustomizations${C_RESET}\n"
  kubectl --context "${KUBECTL_CTX}" get kustomizations.kustomize.toolkit.fluxcd.io -A --no-headers 2>/dev/null \
    | awk '{ printf "    %s/%s ready=%s\n", $1, $2, $4 }' || note "(none)"

  printf "\n${C_BLUE}Flux HelmReleases${C_RESET}\n"
  kubectl --context "${KUBECTL_CTX}" get helmreleases.helm.toolkit.fluxcd.io -A --no-headers 2>/dev/null \
    | awk '{ printf "    %s/%s ready=%s\n", $1, $2, $4 }' || note "(none)"

  printf "\n"
}

# --- Radar Argo CD API token -------------------------------------------------

# mint_radar_token provisions a get-only Argo CD local account ("radar") and
# mints an API token for it, so the Argo CD integration (Settings -> Argo CD,
# or PUT /api/integrations/argocd) can be exercised against the demo cluster.
# The token lands in scripts/gitops-demo/.radar-argocd-token (gitignored).
# Idempotent: re-running replaces the token.
mint_radar_token() {
  step "Minting a get-only Argo CD API token for Radar"

  kubectl --context "${KUBECTL_CTX}" -n argocd patch configmap argocd-cm --type merge \
    -p '{"data":{"accounts.radar":"apiKey"}}' >/dev/null
  kubectl --context "${KUBECTL_CTX}" -n argocd patch configmap argocd-rbac-cm --type merge \
    -p '{"data":{"policy.csv":"p, role:radar, applications, get, */*, allow\ng, radar, role:radar"}}' >/dev/null

  local admin_pw pf_pid token session_token
  admin_pw=$(kubectl --context "${KUBECTL_CTX}" -n argocd get secret argocd-initial-admin-secret \
    -o jsonpath='{.data.password}' | base64 -d)

  kubectl --context "${KUBECTL_CTX}" -n argocd port-forward svc/argocd-server 18085:443 >/dev/null 2>&1 &
  pf_pid=$!
  # Wait for the forward to accept connections; the account cm change is
  # hot-reloaded by argocd-server, but give it a couple of retries too.
  local attempt session_resp
  for attempt in $(seq 1 15); do
    sleep 1
    session_resp=$(curl -ks -X POST https://localhost:18085/api/v1/session \
      -H "Content-Type: application/json" \
      -d "{\"username\":\"admin\",\"password\":\"${admin_pw}\"}" 2>/dev/null) || continue
    session_token=$(printf '%s' "$session_resp" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
    [ -n "$session_token" ] && break
  done
  if [ -z "${session_token:-}" ]; then
    kill "$pf_pid" 2>/dev/null || true
    warn "Could not log in to argocd-server to mint the Radar token (skipping — mint manually with 'argocd account generate-token --account radar')"
    return 0
  fi

  for attempt in $(seq 1 10); do
    token=$(curl -ks -X POST https://localhost:18085/api/v1/account/radar/token \
      -H "Authorization: Bearer ${session_token}" -H "Content-Type: application/json" -d '{}' \
      | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
    [ -n "$token" ] && break
    sleep 2
  done
  kill "$pf_pid" 2>/dev/null || true

  if [ -z "${token:-}" ]; then
    warn "Token mint failed (account may still be propagating) — mint manually with 'argocd account generate-token --account radar'"
    return 0
  fi

  printf '%s' "$token" > "${SCRIPT_DIR}/gitops-demo/.radar-argocd-token"
  chmod 600 "${SCRIPT_DIR}/gitops-demo/.radar-argocd-token"
  ok "Token written to scripts/gitops-demo/.radar-argocd-token"
}

# --- Final summary ---------------------------------------------------------

print_summary() {
  printf "\n"
  step "Demo cluster ready"
  cat <<EOF

  Context:    ${KUBECTL_CTX}
  Argo CD:    kubectl --context ${KUBECTL_CTX} -n argocd port-forward svc/argocd-server 8080:443
  Argo admin: kubectl --context ${KUBECTL_CTX} -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d

  Scenarios baked in:
    Argo  — guestbook-{healthy,drift,manual,suspended,broken-path,broken-sync,rollback}
            app-of-apps + ApplicationSet → 3 children, AppProject filter
    Flux  — podinfo-{base,overlay (dependsOn),suspended}, podinfo HelmRelease,
            zombie-kustomization (stuck Terminating, severity ramps with age)

  Run Radar against this cluster:
    kubectl config use-context ${KUBECTL_CTX}
    ./scripts/visual-test-start.sh

  Connect Radar's Argo CD integration (deep diff):
    port-forward argocd-server (see above), then in Radar Settings -> Argo CD:
    URL https://localhost:8080, insecure TLS on, token from
    scripts/gitops-demo/.radar-argocd-token

  Other commands:
    $0 status           # inventory the cluster
    $0 drift            # induce multi-field drift on guestbook-drift
    $0 reset            # nuke + recreate
    $0 down             # delete cluster

EOF
}

# --- Entry point -----------------------------------------------------------

cmd_help() {
  sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

case "${1:-help}" in
  up)      cmd_up      ;;
  down)    cmd_down    ;;
  reset)   cmd_reset   ;;
  drift)   cmd_drift   ;;
  rebreak) cmd_rebreak ;;
  status)  cmd_status  ;;
  help|-h|--help) cmd_help ;;
  *)
    printf "${C_RED}Unknown subcommand: %s${C_RESET}\n\n" "$1"
    cmd_help
    exit 1
    ;;
esac
