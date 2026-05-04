#!/usr/bin/env bash
# Bootstrap a kind cluster pre-populated with a curated set of GitOps
# scenarios for visual-testing the GitOps tab. Idempotent — can be re-run
# to refresh state or apply fixture updates without recreating the cluster.
#
# Subcommands:
#   up        Create cluster (if missing), install Argo CD + Flux, apply fixtures.
#   down      Delete the kind cluster.
#   drift     Induce drift on a healthy app (kubectl edit a managed Deployment).
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
  # before Kustomization that references it).
  for f in $(ls "${FIXTURES_DIR}"/*.yaml 2>/dev/null | sort); do
    note "applying $(basename "$f")"
    kubectl --context "${KUBECTL_CTX}" apply -f "$f" >/dev/null
  done
  ok "Fixtures applied"
}

# --- Drift inducer ---------------------------------------------------------

cmd_drift() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  step "Inducing drift on demo-healthy/guestbook"

  # Wait for the Deployment to exist (Argo may still be syncing on a
  # fresh `up` run).
  for i in $(seq 1 30); do
    if kubectl --context "${KUBECTL_CTX}" -n demo-healthy get deployment guestbook-ui >/dev/null 2>&1; then
      break
    fi
    if [ "$i" -eq 30 ]; then
      fail "guestbook-ui deployment not found in demo-healthy namespace after 30s — has the cluster fully synced?"
    fi
    sleep 1
  done

  # Scale from the Git-declared 1 replica to 3. Argo will report
  # OutOfSync; this is exactly the live-drift case the GitOps view
  # needs to render correctly.
  kubectl --context "${KUBECTL_CTX}" -n demo-healthy scale deployment guestbook-ui --replicas=3 >/dev/null
  ok "Scaled guestbook-ui to 3 replicas (Git declares 1) — Argo should now report OutOfSync"
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

# --- Final summary ---------------------------------------------------------

print_summary() {
  printf "\n"
  step "Demo cluster ready"
  cat <<EOF

  Context:    ${KUBECTL_CTX}
  Argo CD:    kubectl --context ${KUBECTL_CTX} -n argocd port-forward svc/argocd-server 8080:443
  Argo admin: kubectl --context ${KUBECTL_CTX} -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d

  Run Radar against this cluster:
    kubectl config use-context ${KUBECTL_CTX}
    ./scripts/visual-test-start.sh

  Other commands:
    $0 status           # inventory the cluster
    $0 drift            # introduce live OutOfSync drift on guestbook
    $0 reset            # nuke + recreate
    $0 down             # delete cluster

EOF
}

# --- Entry point -----------------------------------------------------------

cmd_help() {
  sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

case "${1:-help}" in
  up)     cmd_up     ;;
  down)   cmd_down   ;;
  reset)  cmd_reset  ;;
  drift)  cmd_drift  ;;
  status) cmd_status ;;
  help|-h|--help) cmd_help ;;
  *)
    printf "${C_RED}Unknown subcommand: %s${C_RESET}\n\n" "$1"
    cmd_help
    exit 1
    ;;
esac
