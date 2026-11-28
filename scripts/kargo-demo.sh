#!/usr/bin/env bash
# Bootstrap a cluster with a Kargo pipeline in a mixed state, for visual-testing
# Radar's Kargo surfaces. Re-run to repair or refresh; every step checks first.
#
# Subcommands:
#   up        Create the cluster (kind, if no KUBE_CONTEXT is given), install
#             cert-manager, Argo CD, Argo Rollouts, Kargo and an in-cluster Gitea,
#             push the demo git repo, apply the pipeline, then run the promotion
#             story. Takes about 10 minutes the first time.
#   down      Remove what `up` installed. With the default kind cluster, deletes
#             the cluster. With KUBE_CONTEXT, uninstalls everything from that
#             cluster and leaves the cluster itself alone.
#   status    Show the stages, promotions, freight and Argo CD apps.
#   help      Show this message.
#
# End state (project kargo-demo, stages test -> uat -> prod):
#   - prod runs nginx 1.31.9. Its promotion history has one Failed promotion
#     (a "release-gate" step refused it) followed by a Succeeded one.
#   - test and uat hold the newer freight. Verification of that freight on uat
#     FAILED (stage uat is Healthy but not Ready).
#   - The latest promotion on test is Errored: the git push was refused because
#     Kargo will not send git credentials over plain HTTP.
#   - Auto-promotion on test is off, on purpose.
#
# Prerequisites:
#   - kubectl, helm (3.8+ for OCI charts), git, curl
#   - internet access: the Kargo warehouse watches public.ecr.aws/nginx/nginx,
#     and the charts and images come from public registries
#   - kind (only when KUBE_CONTEXT is not set)
#
# Environment:
#   KUBE_CONTEXT   Use this existing kube context instead of a kind cluster.
#   CLUSTER_NAME   kind cluster name (default: radar-kargo-demo).
#
# Everything in this demo (admin passwords, the Gitea user) is a fixed demo-only
# value. Never reuse any of it outside a throwaway cluster.
#
# Logins after `up` (run the port-forwards yourself):
#   kubectl port-forward -n kargo svc/kargo-api 8081:80      user: admin  password: admin
#   kubectl port-forward -n argocd svc/argocd-server 8080:80 user: admin  password: admin

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-kargo-demo}"
KUBE_CONTEXT="${KUBE_CONTEXT:-}"
if [ -n "${KUBE_CONTEXT}" ]; then
  KUBECTL_CTX="${KUBE_CONTEXT}"
else
  KUBECTL_CTX="kind-${CLUSTER_NAME}"
fi
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="${SCRIPT_DIR}/kargo-demo"

# Pinned so the demo behaves the same on every run. The promotion story depends
# on Kargo 1.12 behaviour, so bump deliberately and re-check the end state.
KARGO_VERSION="1.12.7"
ARGO_CD_CHART_VERSION="10.10.5"
ARGO_ROLLOUTS_CHART_VERSION="2.43.8"
CERT_MANAGER_CHART_VERSION="1.21.2"

PROJECT=kargo-demo
OLD_TAG="1.31.9"     # added by hand, so it is "older" than what the warehouse finds
GITEA_PORT="${GITEA_PORT:-13000}"

# Demo-only credentials. The two hashes are bcrypt / pbkdf2 of the word "admin".
ARGOCD_ADMIN_BCRYPT='$2a$10$5vm8wXaSdbuff0m9l21JdevzXBzJFPCi8sy6OOnpZMAG.fOXL7jvO'
KARGO_ADMIN_HASH='$pbkdf2-sha256$600000$Fc8fnFyUR9wjfx7Mu0BuEA$IIOU/KwMqEZuFpq0uAGwJTLTgrI0Ep8g7p.XRgId3Ks'
KARGO_SIGNING_KEY="demo-only-signing-key-do-not-reuse"
KARGO_DB_PASSWORD="demo-only-kargo"
GITEA_USER="kargo"
GITEA_PASSWORD="demo-only-kargo-pass-123"

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

k()  { kubectl --context "${KUBECTL_CTX}" "$@"; }
kd() { k -n "${PROJECT}" "$@"; }
h()  { helm --kube-context "${KUBECTL_CTX}" "$@"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 not found in PATH. Install: $2"
}

require_tools() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd helm "https://helm.sh/docs/intro/install/ (or 'brew install helm')"
  require_cmd git "https://git-scm.com/downloads"
  require_cmd curl "https://curl.se/download.html"
  if [ -z "${KUBE_CONTEXT}" ]; then
    require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation (or 'brew install kind')"
  fi
}

cluster_exists() { kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; }

# --- Lifecycle -------------------------------------------------------------

cmd_up() {
  require_tools
  if [ -n "${KUBE_CONTEXT}" ]; then
    step "Using existing kube context '${KUBE_CONTEXT}'"
  elif cluster_exists; then
    step "Cluster '${CLUSTER_NAME}' already exists - reusing"
  else
    step "Creating kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --wait 60s
    ok "Cluster created"
  fi
  k get ns >/dev/null 2>&1 || fail "context '${KUBECTL_CTX}' is not reachable"

  install_stack
  install_gitea
  apply_pipeline
  run_story
  print_summary
}

cmd_down() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  if [ -z "${KUBE_CONTEXT}" ]; then
    require_cmd kind "https://kind.sigs.k8s.io/"
    if cluster_exists; then
      step "Deleting cluster '${CLUSTER_NAME}'"
      kind delete cluster --name "${CLUSTER_NAME}"
      ok "Deleted"
    else
      warn "Cluster '${CLUSTER_NAME}' does not exist; nothing to do"
    fi
    return
  fi
  require_cmd helm "https://helm.sh/docs/intro/install/"
  k get ns >/dev/null 2>&1 || fail "context '${KUBECTL_CTX}' is not reachable"
  uninstall_all
}

cmd_status() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  k get ns >/dev/null 2>&1 || fail "context '${KUBECTL_CTX}' is not reachable"
  if ! k get ns "${PROJECT}" >/dev/null 2>&1; then
    warn "Project '${PROJECT}' not found. Run: $0 up"
    return
  fi
  step "Stages"
  kd get stages
  step "Promotions"
  kd get promotions
  step "Freight"
  kd get freight
  step "Argo CD applications"
  k -n argocd get applications 2>/dev/null || true
}

# --- Install ---------------------------------------------------------------

install_stack() {
  step "Installing cert-manager ${CERT_MANAGER_CHART_VERSION}"
  h upgrade --install cert-manager cert-manager \
    --repo https://charts.jetstack.io \
    --version "${CERT_MANAGER_CHART_VERSION}" \
    --namespace cert-manager --create-namespace \
    --set crds.enabled=true \
    --wait >/dev/null
  ok "cert-manager ready"

  step "Installing Argo CD (chart ${ARGO_CD_CHART_VERSION})"
  h upgrade --install argocd argo-cd \
    --repo https://argoproj.github.io/argo-helm \
    --version "${ARGO_CD_CHART_VERSION}" \
    --namespace argocd --create-namespace \
    --set "configs.secret.argocdServerAdminPassword=${ARGOCD_ADMIN_BCRYPT}" \
    --set dex.enabled=false \
    --set notifications.enabled=false \
    --set 'server.extraArgs={--insecure}' \
    --wait >/dev/null
  ok "Argo CD ready"

  step "Installing Argo Rollouts (chart ${ARGO_ROLLOUTS_CHART_VERSION})"
  h upgrade --install argo-rollouts argo-rollouts \
    --repo https://argoproj.github.io/argo-helm \
    --version "${ARGO_ROLLOUTS_CHART_VERSION}" \
    --namespace argo-rollouts --create-namespace \
    --wait >/dev/null
  ok "Argo Rollouts ready"

  # Kargo refuses git credentials over plain HTTP unless allowCredentialsOverHTTP
  # is on. The in-cluster Gitea is plain HTTP, so it starts on; the story turns
  # it off for one promotion to get the Errored state.
  step "Installing Kargo ${KARGO_VERSION} (admin password: admin)"
  h upgrade --install kargo oci://ghcr.io/akuity/kargo-charts/kargo \
    --version "${KARGO_VERSION}" \
    --namespace kargo --create-namespace \
    --set api.tls.enabled=false \
    --set "api.adminAccount.passwordHash=${KARGO_ADMIN_HASH}" \
    --set "api.adminAccount.tokenSigningKey=${KARGO_SIGNING_KEY}" \
    --set "database.postgres.password=${KARGO_DB_PASSWORD}" \
    --set externalWebhooksServer.tls.enabled=false \
    --set controller.allowCredentialsOverHTTP=true \
    --wait >/dev/null
  ok "Kargo ready"
}

# Gitea holds the GitOps repo the pipeline promotes into. Plain HTTP, SQLite,
# one demo user.
install_gitea() {
  step "Installing Gitea"
  k apply -f "${FIXTURES_DIR}/gitea.yaml" >/dev/null
  k -n gitea rollout status deploy/gitea --timeout=300s >/dev/null
  if ! k -n gitea exec deploy/gitea -- gitea admin user list 2>/dev/null | grep -q "[[:space:]]${GITEA_USER}[[:space:]]"; then
    k -n gitea exec deploy/gitea -- gitea admin user create \
      --username "${GITEA_USER}" --password "${GITEA_PASSWORD}" \
      --email kargo@example.invalid --admin --must-change-password=false >/dev/null
    ok "Created Gitea user '${GITEA_USER}' (demo-only password)"
  fi
  seed_gitea
  ok "Gitea ready"
}

PF_PID=""
stop_port_forward() {
  if [ -n "${PF_PID}" ]; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
    PF_PID=""
  fi
}
trap stop_port_forward EXIT

seed_gitea() {
  local base="http://localhost:${GITEA_PORT}" tmp
  kubectl --context "${KUBECTL_CTX}" -n gitea port-forward svc/gitea "${GITEA_PORT}:3000" >/dev/null 2>&1 &
  PF_PID=$!
  local i
  for i in $(seq 1 30); do
    curl -fsS "${base}/api/healthz" >/dev/null 2>&1 && break
    sleep 1
  done
  if curl -fsS -u "${GITEA_USER}:${GITEA_PASSWORD}" "${base}/api/v1/repos/${GITEA_USER}/kargo-demo" >/dev/null 2>&1; then
    note "Repo kargo/kargo-demo already seeded"
  else
    step "Pushing the demo git repo to Gitea"
    curl -fsS -u "${GITEA_USER}:${GITEA_PASSWORD}" -H 'Content-Type: application/json' \
      -d '{"name":"kargo-demo","private":false,"default_branch":"main"}' \
      "${base}/api/v1/user/repos" >/dev/null
    tmp="$(mktemp -d)"
    cp -R "${FIXTURES_DIR}/repo/." "${tmp}/"
    (
      cd "${tmp}"
      git init -q -b main
      git add -A
      git -c user.name=kargo -c user.email=kargo@example.invalid commit -qm "kargo-demo base"
      git push -q "http://${GITEA_USER}:${GITEA_PASSWORD}@localhost:${GITEA_PORT}/${GITEA_USER}/kargo-demo.git" main
    )
    rm -rf "${tmp:?}"
  fi
  stop_port_forward
}

# --- Pipeline --------------------------------------------------------------

apply_pipeline() {
  step "Applying the Argo CD ApplicationSet and the Kargo pipeline"
  # The pipeline file creates the Project, which creates the kargo-demo namespace.
  # The Gitea secret in it carries the same demo-only password as above.
  k apply -f "${FIXTURES_DIR}/appset.yaml" >/dev/null
  sed "s/__GITEA_PASSWORD__/${GITEA_PASSWORD}/" "${FIXTURES_DIR}/pipeline.yaml" | k apply -f - >/dev/null
  local i
  for i in $(seq 1 60); do
    k get ns "${PROJECT}" >/dev/null 2>&1 && break
    sleep 1
  done
  k apply -f "${FIXTURES_DIR}/verification.yaml" >/dev/null
  set_uat_verification smoke
  ok "Pipeline applied"
}

set_uat_verification() {
  kd patch stage uat --type merge \
    -p "{\"spec\":{\"verification\":{\"analysisTemplates\":[{\"name\":\"$1\"}]}}}" >/dev/null
}

# --- Story -----------------------------------------------------------------

promo_phase() { kd get promotion "$1" -o jsonpath='{.status.phase}' 2>/dev/null || true; }

wait_promotion() { # name wanted-phase
  local i p
  for i in $(seq 1 120); do
    p="$(promo_phase "$1")"
    [ "${p}" = "$2" ] && return 0
    case "${p}" in
      Succeeded|Failed|Errored|Aborted)
        fail "promotion $1 ended ${p}, wanted $2: $(kd get promotion "$1" -o jsonpath='{.status.message}')" ;;
    esac
    sleep 5
  done
  fail "timed out waiting for promotion $1 to reach $2"
}

# promote <freight-name> <stage> <wanted-phase>
# Creates a Promotion the same way the Kargo UI does: the stage's own promotion
# template steps, copied into the Promotion. No Kargo CLI needed.
promote() {
  local freight="$1" stage="$2" want="$3" steps name
  steps="$(kd get stage "${stage}" -o json | python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["spec"]["promotionTemplate"]["spec"]["steps"]))')"
  name="$(printf '{"apiVersion":"kargo.akuity.io/v1alpha1","kind":"Promotion","metadata":{"generateName":"promo-","namespace":"%s","labels":{"kargo.akuity.io/stage":"%s"}},"spec":{"stage":"%s","freight":"%s","steps":%s}}' \
    "${PROJECT}" "${stage}" "${stage}" "${freight}" "${steps}" | kd create -f - -o name | sed 's#.*/##')"
  note "promote ${freight:0:7} -> ${stage}: ${name}"
  wait_promotion "${name}" "${want}"
  PROMOTED="${name}"
}

freight_by_tag() { kd get freight -o jsonpath="{.items[?(@.images[0].tag==\"$1\")].metadata.name}"; }

# Waits until the freight shows as verified in the stage, or the stage's latest
# verification ended badly. Returns 0 either way; callers check the result.
wait_verified() { # freight stage
  local i verified phase
  for i in $(seq 1 80); do
    verified="$(kd get freight "$1" -o jsonpath='{.status.verifiedIn}')"
    case "${verified}" in *"\"$2\""*) return 0 ;; esac
    phase="$(kd get stage "$2" -o jsonpath='{.status.freightHistory[0].verificationHistory[0].phase}')"
    case "${phase}" in Failed|Error|Aborted|Inconclusive) return 0 ;; esac
    sleep 3
  done
  fail "timed out waiting for verification of ${1:0:7} on $2"
}

set_http_creds() { # true|false
  h upgrade kargo oci://ghcr.io/akuity/kargo-charts/kargo \
    --version "${KARGO_VERSION}" --namespace kargo --reuse-values \
    --set "controller.allowCredentialsOverHTTP=$1" --wait >/dev/null
}

run_story() {
  step "Running the promotion story"
  if [ -n "$(kd get promotions -o name 2>/dev/null)" ]; then
    note "Promotions already exist in ${PROJECT}; skipping the story"
    return
  fi
  require_cmd python3 "https://www.python.org/downloads/"

  # The warehouse finds the newest nginx. A second, older freight is added by hand.
  local i new_name new_tag old_name
  note "Waiting for the warehouse to discover freight (needs internet)"
  for i in $(seq 1 60); do
    [ -n "$(kd get freight -o name 2>/dev/null)" ] && break
    sleep 3
  done
  new_name="$(kd get freight -o jsonpath='{.items[0].metadata.name}')"
  new_tag="$(kd get freight -o jsonpath='{.items[0].images[0].tag}')"
  [ -n "${new_name}" ] || fail "the warehouse found no freight"
  [ "${new_tag}" != "${OLD_TAG}" ] || fail "the warehouse found ${OLD_TAG} as the newest nginx; the story needs something newer"
  if [ -z "$(freight_by_tag "${OLD_TAG}")" ]; then
    kd create -f "${FIXTURES_DIR}/freight-1.31.9.yaml" >/dev/null
    sleep 5
  fi
  old_name="$(freight_by_tag "${OLD_TAG}")"
  [ -n "${old_name}" ] || fail "could not add the ${OLD_TAG} freight"
  note "old freight ${OLD_TAG}, new freight ${new_tag}"

  # Part 1: the old freight goes test -> uat -> prod. A release gate refuses the
  # first prod attempt, which leaves a Failed promotion in prod's history.
  promote "${old_name}" test Succeeded
  wait_verified "${old_name}" test
  promote "${old_name}" uat Succeeded
  wait_verified "${old_name}" uat
  local gate='[{"op":"add","path":"/spec/promotionTemplate/spec/steps/0","value":{"uses":"fail","as":"release-gate","config":{"message":"change freeze in effect for prod"}}}]'
  kd patch stage prod --type json -p "${gate}" >/dev/null
  promote "${old_name}" prod Failed
  kd patch stage prod --type json -p '[{"op":"remove","path":"/spec/promotionTemplate/spec/steps/0"}]' >/dev/null
  promote "${old_name}" prod Succeeded
  ok "prod is on ${OLD_TAG}"

  # Part 2: the newer freight passes test, then fails verification on uat.
  promote "${new_name}" test Succeeded
  wait_verified "${new_name}" test
  kd apply -f "${FIXTURES_DIR}/verification-fail.yaml" >/dev/null
  set_uat_verification smoke-fail
  promote "${new_name}" uat Succeeded
  wait_verified "${new_name}" uat
  ok "uat verification of ${new_tag}: $(kd get stage uat -o jsonpath='{.status.freightHistory[0].verificationHistory[0].phase}')"

  # Auto-promotion on test stays off. Otherwise Kargo would pick up the
  # hand-added older freight as a new candidate.
  kd apply -f "${FIXTURES_DIR}/projectconfig.yaml" >/dev/null

  # Part 3: with HTTP credentials off, the git push step fails, so the next
  # promotion on test ends Errored. It stays the latest promotion on test.
  step "Making one promotion on test fail (git push over HTTP refused)"
  set_http_creds false
  promote "${new_name}" test Errored
  set_http_creds true
  ok "Story done"
}

# --- Teardown (existing cluster) -------------------------------------------

uninstall_all() {
  step "Removing the Kargo project and Argo CD apps"
  k delete project.kargo.akuity.io "${PROJECT}" --ignore-not-found --timeout=180s >/dev/null 2>&1 || true
  k -n argocd delete applicationset kargo-demo --ignore-not-found --timeout=120s >/dev/null 2>&1 || true
  k -n argocd delete application --all --timeout=120s >/dev/null 2>&1 || true
  k delete ns kargo-demo-test kargo-demo-uat kargo-demo-prod "${PROJECT}" gitea --ignore-not-found --timeout=180s >/dev/null 2>&1 || true

  step "Uninstalling Helm releases"
  local rel ns
  for rel in kargo:kargo argo-rollouts:argo-rollouts argocd:argocd cert-manager:cert-manager; do
    ns="${rel#*:}"; rel="${rel%%:*}"
    if h status "${rel}" -n "${ns}" >/dev/null 2>&1; then
      h uninstall "${rel}" -n "${ns}" --timeout 120s >/dev/null 2>&1 || warn "could not uninstall ${rel}"
    fi
  done
  # The Kargo controller puts a finalizer on its own ServiceAccount. With the
  # controller gone nothing removes it, and the kargo namespace would hang.
  k -n kargo patch serviceaccount kargo-controller --type merge \
    -p '{"metadata":{"finalizers":null}}' >/dev/null 2>&1 || true

  step "Removing namespaces and CRDs the charts left behind"
  k delete ns kargo argo-rollouts argocd cert-manager --ignore-not-found --timeout=180s >/dev/null 2>&1 || true
  local crds
  crds="$(k get crd -o name | grep -E '\.(kargo\.akuity\.io|argoproj\.io|cert-manager\.io|acme\.cert-manager\.io)$' || true)"
  if [ -n "${crds}" ]; then
    # shellcheck disable=SC2086
    k delete ${crds} --ignore-not-found --timeout=180s >/dev/null 2>&1 || warn "some CRDs did not delete"
  fi
  # Cluster-scoped leftovers carry the chart's release labels.
  k delete clusterrole,clusterrolebinding -l 'app.kubernetes.io/instance in (kargo,argocd,argo-rollouts,cert-manager)' --ignore-not-found >/dev/null 2>&1 || true
  ok "Removed"
}

# --- Summary ---------------------------------------------------------------

print_summary() {
  step "Done. Current state"
  kd get stages,promotions
  cat <<EOF

    Kargo UI:    kubectl --context ${KUBECTL_CTX} port-forward -n kargo svc/kargo-api 8081:80
                 then open http://localhost:8081 (admin / admin)
    Argo CD UI:  kubectl --context ${KUBECTL_CTX} port-forward -n argocd svc/argocd-server 8080:80
                 then open http://localhost:8080 (admin / admin)
    Remove it:   $0 down
EOF
}

cmd_help() { sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'; }

case "${1:-help}" in
  up)     cmd_up ;;
  down)   cmd_down ;;
  status) cmd_status ;;
  help|-h|--help) cmd_help ;;
  *)
    printf "${C_RED}Unknown subcommand: %s${C_RESET}\n\n" "$1"
    cmd_help
    exit 1
    ;;
esac
