#!/usr/bin/env bash
# Bootstrap a kind cluster pre-populated with Argo Rollouts scenarios for
# visual-testing the Rollout control surface. Idempotent — re-run to refresh
# state or apply fixture updates without recreating the cluster.
#
# Subcommands:
#   up        Create cluster (if missing), install Argo Rollouts, apply fixtures,
#             then drive each Rollout into the state it is meant to demonstrate.
#   down      Delete the kind cluster.
#   roll      Push a new image on canary-manual so it parks mid-canary again
#             (use after promoting or aborting it from the UI).
#   reset     down + up.
#   visibility Re-park only the native rollout-visibility workloads.
#   status    Inventory the Rollouts and their AnalysisRuns.
#   help      Show this message.
#
# Prerequisites:
#   - kind         https://kind.sigs.k8s.io/
#   - kubectl
#
# Set CLUSTER_NAME=foo to use a different cluster (default: radar-rollouts-demo).

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-rollouts-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="${SCRIPT_DIR}/rollouts-demo"

ROLLOUTS_VERSION="${ROLLOUTS_VERSION:-v1.9.1}"

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BLUE='\033[34m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'; C_RED='\033[31m'; C_DIM='\033[2m'; C_RESET='\033[0m'
else
  C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_DIM=''; C_RESET=''
fi

step() { printf "${C_BLUE}==> %s${C_RESET}\n" "$1"; }
ok()   { printf "${C_GREEN}    ✓ %s${C_RESET}\n" "$1"; }
warn() { printf "${C_YELLOW}    ! %s${C_RESET}\n" "$1"; }
fail() { printf "${C_RED}    ✗ %s${C_RESET}\n" "$1"; exit 1; }
note() { printf "${C_DIM}    %s${C_RESET}\n" "$1"; }

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "$1 not found in PATH. Install: $2"
  fi
}

kc() { kubectl --context "${KUBECTL_CTX}" "$@"; }

ro_get() { kc -n demo-rollouts get rollout "$1" -o jsonpath="$2" 2>/dev/null || true; }

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

  kc cluster-info >/dev/null || fail "kind context not reachable"

  install_rollouts
  apply_fixtures
  drive_scenarios
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

# --- Argo Rollouts ---------------------------------------------------------

install_rollouts() {
  step "Installing Argo Rollouts (${ROLLOUTS_VERSION}) into 'argo-rollouts' namespace"

  kc create namespace argo-rollouts --dry-run=client -o yaml | kc apply -f - >/dev/null
  kc apply -n argo-rollouts \
    -f "https://github.com/argoproj/argo-rollouts/releases/download/${ROLLOUTS_VERSION}/install.yaml" >/dev/null

  step "Waiting for the Rollouts controller to be Ready (~60s)"
  kc -n argo-rollouts rollout status deployment/argo-rollouts --timeout=180s >/dev/null
  ok "Argo Rollouts healthy"
}

# --- Fixtures --------------------------------------------------------------

apply_fixtures() {
  step "Applying Argo Rollouts demo fixtures"
  [ -d "${FIXTURES_DIR}" ] || fail "Fixtures dir not found: ${FIXTURES_DIR}"

  # Numeric order matters: namespace, then the metric endpoint the
  # AnalysisTemplates point at, then the templates, then the Rollouts.
  for f in $(ls "${FIXTURES_DIR}"/*.yaml 2>/dev/null | sort); do
    note "applying $(basename "$f")"
    kc apply -f "$f" >/dev/null
  done

  kc -n demo-rollouts rollout status deployment/metric-endpoint --timeout=120s >/dev/null \
    || warn "metric-endpoint not Ready — analysis runs will Error instead of resolving"
  ok "Fixtures applied"
}

# --- Scenario orchestration ------------------------------------------------

# wait_for_rollout_phase polls status.phase until it matches, so later steps
# don't race a controller that hasn't reconciled the previous change.
wait_for_rollout_phase() {
  local name="$1" want="$2" deadline=$((SECONDS + ${3:-240}))
  while [ $SECONDS -lt $deadline ]; do
    [ "$(ro_get "$name" '{.status.phase}')" = "$want" ] && return 0
    sleep 3
  done
  return 1
}

wait_for_pause_reason() {
  local name="$1" want="$2" deadline=$((SECONDS + ${3:-300}))
  while [ $SECONDS -lt $deadline ]; do
    if ro_get "$name" '{.status.pauseConditions[*].reason}' | grep -q "$want"; then
      return 0
    fi
    sleep 3
  done
  return 1
}

set_image() {
  kc -n demo-rollouts patch rollout "$1" --type json \
    -p "[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/image\",\"value\":\"argoproj/rollouts-demo:$2\"}]" >/dev/null
}

drive_scenarios() {
  build_canary_manual_history
  park_canary_analysis
  park_bluegreen
  break_canary_degraded
  park_workloadref
  park_native_visibility
}

# Rollback needs targets, so canary-manual gets three fully-promoted
# revisions before it is left parked mid-canary on a fourth.
build_canary_manual_history() {
  step "Building canary-manual revision history"

  local revisions
  revisions=$(kc -n demo-rollouts get replicasets \
    -l app=canary-manual -o jsonpath='{.items[*].metadata.name}' | wc -w | tr -d ' ')
  if [ "${revisions:-0}" -ge 4 ]; then
    note "canary-manual already has ${revisions} revisions — skipping (idempotent)"
    return
  fi

  if ! wait_for_rollout_phase canary-manual Healthy; then
    warn "canary-manual never went Healthy on its initial revision; skipping history"
    return
  fi

  for color in green yellow; do
    note "promoting canary-manual to :${color}"
    set_image canary-manual "$color"
    if ! wait_for_pause_reason canary-manual CanaryPauseStep; then
      warn "canary-manual didn't reach a manual pause on :${color}; history may be short"
      return
    fi
    # promoteFull is how kubectl-argo-rollouts skips the remaining steps —
    # patching status directly avoids depending on the plugin being installed.
    kc -n demo-rollouts patch rollout canary-manual --subresource status --type merge \
      -p '{"status":{"promoteFull":true}}' >/dev/null 2>&1 \
      || kc -n demo-rollouts patch rollout canary-manual --type merge \
        -p '{"status":{"promoteFull":true}}' >/dev/null 2>&1 || true
    wait_for_rollout_phase canary-manual Healthy 300 \
      || warn "canary-manual didn't settle after promoting :${color}"
  done

  note "pushing :purple and leaving it parked mid-canary"
  set_image canary-manual purple
  if wait_for_pause_reason canary-manual CanaryPauseStep; then
    ok "canary-manual paused at CanaryPauseStep with Current != Stable"
  else
    warn "canary-manual didn't park — the actions row will render fewer live verbs"
  fi
}

# Two revisions are needed for the analysis to run at all: the first
# revision has nothing to compare against and goes straight to Healthy.
park_canary_analysis() {
  step "Parking canary-analysis on an inconclusive AnalysisRun"

  if ro_get canary-analysis '{.status.pauseConditions[*].reason}' | grep -q InconclusiveAnalysisRun; then
    note "canary-analysis already parked on InconclusiveAnalysisRun"
    return
  fi

  if ! wait_for_rollout_phase canary-analysis Healthy; then
    warn "canary-analysis never went Healthy on its initial revision; skipping"
    return
  fi

  set_image canary-analysis red
  if wait_for_pause_reason canary-analysis InconclusiveAnalysisRun 360; then
    ok "canary-analysis paused on InconclusiveAnalysisRun"
  else
    warn "canary-analysis didn't reach InconclusiveAnalysisRun (check the metric-endpoint pod)"
  fi
}

park_bluegreen() {
  step "Parking bluegreen on BlueGreenPause"

  if ro_get bluegreen '{.status.pauseConditions[*].reason}' | grep -q BlueGreenPause; then
    note "bluegreen already parked on BlueGreenPause"
    return
  fi

  if ! wait_for_rollout_phase bluegreen Healthy; then
    warn "bluegreen never went Healthy on its initial revision; skipping"
    return
  fi

  set_image bluegreen orange
  if wait_for_pause_reason bluegreen BlueGreenPause 360; then
    ok "bluegreen paused with activeSelector != previewSelector"
  else
    warn "bluegreen didn't park on BlueGreenPause"
  fi
}

break_canary_degraded() {
  step "Driving canary-degraded into an aborted state"

  if [ "$(ro_get canary-degraded '{.status.abort}')" = "true" ]; then
    note "canary-degraded already aborted"
    return
  fi

  if ! wait_for_rollout_phase canary-degraded Healthy; then
    warn "canary-degraded never went Healthy on its initial revision; skipping"
    return
  fi

  set_image canary-degraded red
  local deadline=$((SECONDS + 360))
  while [ $SECONDS -lt $deadline ]; do
    if [ "$(ro_get canary-degraded '{.status.abort}')" = "true" ]; then
      ok "canary-degraded aborted by a failing analysis — Retry is the live verb"
      return
    fi
    sleep 5
  done
  warn "canary-degraded didn't abort (the failing analysis may still be measuring)"
}

park_workloadref() {
  step "Parking canary-workloadref mid-canary"

  if ro_get canary-workloadref '{.status.pauseConditions[*].reason}' | grep -q CanaryPauseStep; then
    note "canary-workloadref already parked"
    return
  fi

  if ! wait_for_rollout_phase canary-workloadref Healthy; then
    warn "canary-workloadref never went Healthy; skipping"
    return
  fi

  kc -n demo-rollouts patch deployment workloadref-target --type json \
    -p '[{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"argoproj/rollouts-demo:yellow"}]' >/dev/null
  if wait_for_pause_reason canary-workloadref CanaryPauseStep; then
    ok "canary-workloadref paused — rollback here must patch the Deployment"
  else
    warn "canary-workloadref didn't park"
  fi
}

park_native_visibility() {
  step "Parking native workloads in rollout visibility states"

  kc -n demo-rollouts patch deployment native-paused --type merge \
    -p '{"spec":{"paused":false,"template":{"spec":{"containers":[{"name":"demo","image":"argoproj/rollouts-demo:blue"}]}}}}' >/dev/null
  kc -n demo-rollouts patch deployment image-failure --type json \
    -p '[{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"argoproj/rollouts-demo:blue"}]' >/dev/null
  kc -n demo-rollouts patch statefulset ondelete-stateful --type json \
    -p '[{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"argoproj/rollouts-demo:blue"}]' >/dev/null
  kc -n demo-rollouts patch daemonset ondelete-daemon --type json \
    -p '[{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"argoproj/rollouts-demo:blue"}]' >/dev/null

  for workload in deployment/native-paused deployment/image-failure; do
    kc -n demo-rollouts rollout status "$workload" --timeout=180s >/dev/null \
      || { warn "$workload did not become Ready; leaving its current state"; return; }
  done

  kc -n demo-rollouts delete pod -l app=ondelete-stateful >/dev/null
  kc -n demo-rollouts delete pod -l app=ondelete-daemon >/dev/null
  kc -n demo-rollouts wait --for=jsonpath='{.status.readyReplicas}'=2 \
    statefulset/ondelete-stateful --timeout=180s >/dev/null \
    || { warn "statefulset/ondelete-stateful did not become Ready; leaving its current state"; return; }
  kc -n demo-rollouts wait --for=jsonpath='{.status.numberReady}'=1 \
    daemonset/ondelete-daemon --timeout=180s >/dev/null \
    || { warn "daemonset/ondelete-daemon did not become Ready; leaving its current state"; return; }

  kc -n demo-rollouts patch deployment native-paused --type merge \
    -p '{"spec":{"paused":true,"template":{"spec":{"containers":[{"name":"demo","image":"argoproj/rollouts-demo:yellow"}]}}}}' >/dev/null
  kc -n demo-rollouts patch statefulset ondelete-stateful --type json \
    -p '[{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"argoproj/rollouts-demo:yellow"}]' >/dev/null
  kc -n demo-rollouts patch daemonset ondelete-daemon --type json \
    -p '[{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"argoproj/rollouts-demo:yellow"}]' >/dev/null
  kc -n demo-rollouts patch deployment image-failure --type json \
    -p '[{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"registry.invalid/radar/does-not-exist:visual-test"}]' >/dev/null

  ok "native-paused, OnDelete, and new-revision image failure states parked"
}

# --- Re-roll ---------------------------------------------------------------

cmd_roll() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  step "Rolling canary-manual to a fresh color"

  local current next
  current=$(ro_get canary-manual '{.spec.template.spec.containers[0].image}')
  case "$current" in
    *:purple) next=blue ;;
    *:blue)   next=green ;;
    *:green)  next=yellow ;;
    *)        next=purple ;;
  esac

  # Clear any lingering abort, or the controller ignores the new revision.
  kc -n demo-rollouts patch rollout canary-manual --subresource status --type merge \
    -p '{"status":{"abort":false}}' >/dev/null 2>&1 || true

  set_image canary-manual "$next"
  if wait_for_pause_reason canary-manual CanaryPauseStep; then
    ok "canary-manual rolling to :${next}, parked at CanaryPauseStep"
  else
    warn "canary-manual didn't park after rolling to :${next}"
  fi
}

cmd_visibility() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist. Run: $0 up"
  park_native_visibility
}

# --- Status ----------------------------------------------------------------

cmd_status() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"

  if ! cluster_exists; then
    warn "Cluster '${CLUSTER_NAME}' does not exist. Run: $0 up"
    return
  fi

  step "Cluster: ${CLUSTER_NAME} (context ${KUBECTL_CTX})"

  printf "\n${C_BLUE}Rollouts controller${C_RESET}\n"
  kc -n argo-rollouts get pods --no-headers 2>/dev/null \
    | awk '{ printf "    %s %s\n", $1, $3 }' || warn "controller not installed"

  printf "\n${C_BLUE}Rollouts${C_RESET}\n"
  kc -n demo-rollouts get rollouts.argoproj.io -o \
    custom-columns='NAME:.metadata.name,PHASE:.status.phase,STEP:.status.currentStepIndex,ABORT:.status.abort,PAUSE:.status.pauseConditions[*].reason' \
    --no-headers 2>/dev/null | sed 's/^/    /' || note "(none)"

  printf "\n${C_BLUE}AnalysisRuns${C_RESET}\n"
  kc -n demo-rollouts get analysisruns.argoproj.io -o \
    custom-columns='NAME:.metadata.name,PHASE:.status.phase,TRIGGER:.metadata.labels.rollout-type' \
    --no-headers 2>/dev/null | sed 's/^/    /' || note "(none)"

  printf "\n"
}

# --- Summary ---------------------------------------------------------------

print_summary() {
  printf "\n"
  step "Rollouts demo cluster ready"
  cat <<EOF

  Context: ${KUBECTL_CTX}

  Scenarios baked in:
    canary-manual        Paused at CanaryPauseStep, 3 prior revisions
                         → Promote, Skip step, Promote full, Abort, Rollback
    canary-analysis      Paused on InconclusiveAnalysisRun
                         → the AnalysisRun names the deciding metric
    bluegreen            Paused on BlueGreenPause, active != preview
                         → Skip step must be absent (canary-only verb)
    canary-degraded      Aborted by a failing analysis → Retry
    canary-workloadref   Paused; template lives on a Deployment
                         → rollback must patch the Deployment
    metric-endpoint      Static JSON backing the web metric provider

  Run Radar against this cluster:
    kubectl config use-context ${KUBECTL_CTX}
    ./scripts/visual-test-start.sh

  Other commands:
    $0 status    # inventory Rollouts + AnalysisRuns
    $0 roll      # re-park canary-manual mid-canary after promoting it
    $0 visibility # re-park only the native rollout-visibility workloads
    $0 reset     # nuke + recreate
    $0 down      # delete cluster

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
  roll)   cmd_roll   ;;
  visibility) cmd_visibility ;;
  status) cmd_status ;;
  help|-h|--help) cmd_help ;;
  *)
    printf "${C_RED}Unknown subcommand: %s${C_RESET}\n\n" "$1"
    cmd_help
    exit 1
    ;;
esac
