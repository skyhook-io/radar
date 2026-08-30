#!/usr/bin/env bash
# Reproducible, controller-free GPU/batch/AI ecosystem coverage for Radar.
# Upstream CRDs are pinned in gpu-ecosystem-demo/resources.tsv; fixtures cover
# the 37 curated resource identities shipped by Radar without pretending kind
# can validate real GPU allocation, driver health, utilization, or scheduling.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-gpu-ecosystem-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"
SCENARIO="${SCENARIO:-current}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DEMO_DIR="${SCRIPT_DIR}/gpu-ecosystem-demo"
REGISTRY="${DEMO_DIR}/resources.tsv"
FIXTURES_DIR="${DEMO_DIR}/fixtures"
DEMO_NS="gpu-demo"
RADAR_NS="radar"
RADAR_PORT="${RADAR_PORT:-19280}"
CLEANUP_DIRS=('')
CLEANUP_FILES=('')
CLEANUP_PIDS=('')

cleanup() {
  local pid path
  for pid in "${CLEANUP_PIDS[@]}"; do
    if [ -n "${pid}" ]; then
      kill "${pid}" >/dev/null 2>&1 || true
      wait "${pid}" 2>/dev/null || true
    fi
  done
  for path in "${CLEANUP_FILES[@]}"; do [ -z "${path}" ] || rm -f "${path}"; done
  for path in "${CLEANUP_DIRS[@]}"; do [ -z "${path}" ] || rm -rf "${path}"; done
}
trap cleanup EXIT

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
  command -v "$1" >/dev/null 2>&1 || fail "$1 not found in PATH. Install: $2"
}

kctl() { kubectl --context "${KUBECTL_CTX}" "$@"; }

cluster_exists() { kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; }

require_cluster() {
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist. Run '$0 up' first."
}

validate_scenario() {
  case "${SCENARIO}" in
    current|experimental) ;;
    *) fail "SCENARIO must be 'current' or 'experimental' (got '${SCENARIO}')" ;;
  esac
}

registry_rows() {
  awk -F '\t' -v scenario="${SCENARIO}" 'NR > 1 && ($7 == "all" || $7 == scenario)' "${REGISTRY}"
}

logical_count() {
  registry_rows | awk -F '\t' '{print $1}' | sort -u | wc -l | tr -d ' '
}

timestamp_minutes_ago() {
  local minutes="$1"
  if date -u -v-"${minutes}"M '+%Y-%m-%dT%H:%M:%SZ' >/dev/null 2>&1; then
    date -u -v-"${minutes}"M '+%Y-%m-%dT%H:%M:%SZ'
  else
    date -u -d "${minutes} minutes ago" '+%Y-%m-%dT%H:%M:%SZ'
  fi
}

scenario_in_cluster() {
  kctl -n "${DEMO_NS}" get configmap gpu-ecosystem-demo-state \
    -o jsonpath='{.data.scenario}' 2>/dev/null || true
}

assert_matching_scenario() {
  local installed logical group plural kind scope version scenario url
  installed="$(scenario_in_cluster)"
  if [ -n "${installed}" ] && [ "${installed}" != "${SCENARIO}" ]; then
    fail "Cluster contains '${installed}' fixtures. Run 'SCENARIO=${SCENARIO} $0 reset' to switch inference API families."
  fi
  while IFS=$'\t' read -r logical group plural kind scope version scenario url; do
    if [ "${scenario}" != "all" ] && [ "${scenario}" != "${SCENARIO}" ] && \
      kctl get "crd/${plural}.${group}" >/dev/null 2>&1; then
      fail "Cluster already contains the '${scenario}' ${plural}.${group} CRD. Run 'SCENARIO=${SCENARIO} $0 reset' to switch inference API families."
    fi
  done < <(awk -F '\t' 'NR > 1' "${REGISTRY}")
}

record_scenario() {
  kctl create namespace "${DEMO_NS}" --dry-run=client -o yaml | kctl apply -f - >/dev/null
  kctl -n "${DEMO_NS}" create configmap gpu-ecosystem-demo-state \
    --from-literal="scenario=${SCENARIO}" --dry-run=client -o yaml | kctl apply -f - >/dev/null
}

install_crds() {
  step "Installing pinned upstream CRDs for the ${SCENARIO} scenario"
  local seen='' logical group plural kind scope version scenario url crd
  while IFS=$'\t' read -r logical group plural kind scope version scenario url; do
    crd="${plural}.${group}"
    case " ${seen} " in
      *" ${crd} "*) continue ;;
    esac
    seen="${seen} ${crd}"
    kctl apply --server-side -f "${url}" >/dev/null
    local established=false
    for _ in $(seq 1 90); do
      if kctl get "crd/${crd}" -o json 2>/dev/null | jq -e \
        'any(.status.conditions[]?; .type == "Established" and .status == "True")' >/dev/null; then
        established=true
        break
      fi
      sleep 1
    done
    [ "${established}" = "true" ] || fail "${crd} did not become Established within 90s"
  done < <(registry_rows)
  ok "$(logical_count) curated resource identities have established CRDs"
}

render_fixture() {
  local fixture="$1" output="$2" start_time="$3" complete_time="$4"
  sed -e "s/__START_TIME__/${start_time}/g" \
      -e "s/__COMPLETE_TIME__/${complete_time}/g" \
      "${fixture}" > "${output}"
}

patch_fixture_statuses() {
  local fixture="$1" doc api_version group version kind name namespace plural resource patch has_status actual
  while IFS= read -r doc; do
    [ -n "${doc}" ] || continue
    api_version="$(jq -r '.apiVersion' <<<"${doc}")"
    case "${api_version}" in
      */*) group="${api_version%/*}"; version="${api_version##*/}" ;;
      *) group=''; version="${api_version}" ;;
    esac
    kind="$(jq -r '.kind' <<<"${doc}")"
    name="$(jq -r '.metadata.name' <<<"${doc}")"
    namespace="$(jq -r '.metadata.namespace // empty' <<<"${doc}")"
    patch="$(jq -c '{status: .status}' <<<"${doc}")"
    if [ -z "${group}" ]; then
      resource="${kind}"
      has_status=true
    else
      plural="$(registry_rows | awk -F '\t' -v g="${group}" -v k="${kind}" '$2 == g && $4 == k {print $3; exit}')"
      [ -n "${plural}" ] || fail "No registry row for status fixture ${kind}.${group}/${name}"
      resource="${plural}.${group}"
      has_status="$(kctl get "crd/${resource}" -o json | jq -r --arg v "${version}" '.spec.versions[] | select(.name == $v) | (.subresources.status != null)')"
    fi
    if [ "${has_status}" = "true" ] && [ -n "${namespace}" ]; then
      kctl -n "${namespace}" patch "${resource}" "${name}" \
        --subresource=status --type=merge -p "${patch}" >/dev/null
    elif [ "${has_status}" = "true" ]; then
      kctl patch "${resource}" "${name}" \
        --subresource=status --type=merge -p "${patch}" >/dev/null
    elif [ -n "${namespace}" ]; then
      kctl -n "${namespace}" patch "${resource}" "${name}" \
        --type=merge -p "${patch}" >/dev/null
    else
      kctl patch "${resource}" "${name}" \
        --type=merge -p "${patch}" >/dev/null
    fi
    if [ -n "${namespace}" ]; then
      actual="$(kctl -n "${namespace}" get "${resource}" "${name}" -o json)"
    else
      actual="$(kctl get "${resource}" "${name}" -o json)"
    fi
    jq -e --argjson want "${patch}" '.status | contains($want.status)' <<<"${actual}" >/dev/null || \
      fail "API server pruned the requested status for ${kind}.${group}/${name}"
  done < <(yq ea -o=j -I=0 'select(.status != null)' "${fixture}")
}

apply_fixtures() {
  require_cmd jq "https://jqlang.github.io/jq/download/"
  require_cmd yq "https://github.com/mikefarah/yq#install"
  local tmp start_time complete_time fixture rendered
  tmp="$(mktemp -d)"
  CLEANUP_DIRS+=("${tmp}")
  start_time="$(timestamp_minutes_ago 25)"
  complete_time="$(timestamp_minutes_ago 5)"

  step "Applying deterministic ${SCENARIO} fixtures"
  for fixture in \
    "${FIXTURES_DIR}/00-core.yaml" \
    "${FIXTURES_DIR}/10-kueue.yaml" \
    "${FIXTURES_DIR}/20-ray-kserve.yaml" \
    "${FIXTURES_DIR}/30-inference-${SCENARIO}.yaml" \
    "${FIXTURES_DIR}/40-batch-training.yaml" \
    "${FIXTURES_DIR}/50-serving-operators.yaml"; do
    rendered="${tmp}/$(basename "${fixture}")"
    render_fixture "${fixture}" "${rendered}" "${start_time}" "${complete_time}"
    yq ea 'del(.status)' "${rendered}" | kctl apply -f - >/dev/null
    patch_fixture_statuses "${rendered}"
  done

  ok "Fixtures applied with relative timestamps and stable rendered states"
}

cmd_up() {
  validate_scenario
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  require_cmd yq "https://github.com/mikefarah/yq#install"

  if cluster_exists; then
    step "Reusing kind cluster '${CLUSTER_NAME}'"
  else
    step "Creating kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --wait 60s
  fi
  kctl cluster-info >/dev/null || fail "kind context '${KUBECTL_CTX}' is not reachable"
  assert_matching_scenario
  record_scenario
  install_crds
  apply_fixtures
  cmd_verify
  note "Run '$0 install-radar' to test the current build with default chart RBAC."
}

cmd_down() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  if cluster_exists; then
    step "Deleting kind cluster '${CLUSTER_NAME}'"
    kind delete cluster --name "${CLUSTER_NAME}"
    ok "Deleted"
  else
    ok "Cluster '${CLUSTER_NAME}' does not exist"
  fi
}

cmd_verify() {
  validate_scenario
  require_cluster
  require_cmd jq "https://jqlang.github.io/jq/download/"
  assert_matching_scenario
  [ "$(logical_count)" = "37" ] || fail "Registry resolves to $(logical_count) logical kinds; expected 37"

  step "Verifying upstream CRDs and all 37 group-qualified fixtures"
  local logical group plural kind scope version scenario url crd actual_group actual_plural actual_scope served count
  while IFS=$'\t' read -r logical group plural kind scope version scenario url; do
    crd="${plural}.${group}"
    actual_group="$(kctl get "crd/${crd}" -o jsonpath='{.spec.group}')"
    actual_plural="$(kctl get "crd/${crd}" -o jsonpath='{.spec.names.plural}')"
    actual_scope="$(kctl get "crd/${crd}" -o jsonpath='{.spec.scope}')"
    served="$(kctl get "crd/${crd}" -o json | jq -r --arg v "${version}" 'any(.spec.versions[]; .name == $v and .served)')"
    [ "${actual_group}" = "${group}" ] || fail "${crd}: group is ${actual_group}, expected ${group}"
    [ "${actual_plural}" = "${plural}" ] || fail "${crd}: plural is ${actual_plural}, expected ${plural}"
    [ "${actual_scope}" = "${scope}" ] || fail "${crd}: scope is ${actual_scope}, expected ${scope}"
    [ "${served}" = "true" ] || fail "${crd}: expected served version ${version}"
    count="$(kctl get "${plural}.${group}" -A -o json | jq '.items | length')"
    [ "${count}" -gt 0 ] || fail "${kind}.${group}: no fixture objects found"
  done < <(registry_rows)

  [ "$(kctl -n "${DEMO_NS}" get jobs.batch collision-demo -o name)" = "job.batch/collision-demo" ] || fail "core Job collision fixture missing"
  [ "$(kctl -n "${DEMO_NS}" get jobs.batch.volcano.sh collision-demo -o name)" = "job.batch.volcano.sh/collision-demo" ] || fail "Volcano Job collision fixture missing"
  kctl get queues.scheduling.volcano.sh gpu-volcano >/dev/null || fail "Volcano Queue collision fixture missing"
  kctl get queues.scheduling.run.ai gpu-kai >/dev/null || fail "KAI Queue collision fixture missing"
  kctl -n "${DEMO_NS}" get podgroups.scheduling.volcano.sh collision-demo >/dev/null || fail "Volcano PodGroup collision fixture missing"
  kctl -n "${DEMO_NS}" get podgroups.scheduling.run.ai collision-demo >/dev/null || fail "KAI PodGroup collision fixture missing"
  ok "37 curated identities, served versions, scopes, objects, and collision families verified"
}

cmd_install_radar() {
  require_cluster
  require_cmd docker "https://docs.docker.com/get-docker/"
  require_cmd helm "https://helm.sh/docs/intro/install/"
  require_cmd go "https://go.dev/dl/"
  require_cmd npm "https://nodejs.org/"
  local arch tmp image
  case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; *) arch=amd64 ;; esac
  tmp="$(mktemp -d)"
  CLEANUP_DIRS+=("${tmp}")
  image="radar-gpu-ecosystem-demo:dev"

  step "Building the current frontend, embedded assets, and linux/${arch} Radar binary"
  (cd "${REPO_ROOT}" && make frontend embed >/dev/null)
  (cd "${REPO_ROOT}" && GOOS=linux GOARCH="${arch}" CGO_ENABLED=0 go build -o "${tmp}/radar" ./cmd/explorer)
  printf '%s\n' 'FROM alpine:3.20' 'RUN apk add --no-cache ca-certificates' 'COPY radar /radar' 'ENTRYPOINT ["/radar"]' > "${tmp}/Dockerfile"
  docker build -q -t "${image}" "${tmp}" >/dev/null
  kind load docker-image "${image}" --name "${CLUSTER_NAME}" >/dev/null

  step "Installing Radar with the chart's default RBAC"
  helm --kube-context "${KUBECTL_CTX}" upgrade --install radar "${REPO_ROOT}/deploy/helm/radar" \
    --namespace "${RADAR_NS}" --create-namespace \
    --set image.repository=radar-gpu-ecosystem-demo --set image.tag=dev --set image.pullPolicy=Never \
    --wait --timeout 180s >/dev/null
  kctl -n "${RADAR_NS}" rollout restart deploy/radar >/dev/null
  kctl -n "${RADAR_NS}" rollout status deploy/radar --timeout=180s >/dev/null
  ok "Current Radar build is running in-cluster"
  cmd_verify_radar
}

cmd_verify_radar() {
  validate_scenario
  require_cluster
  require_cmd curl "https://curl.se/download.html"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  assert_matching_scenario
  kctl -n "${RADAR_NS}" get deploy radar >/dev/null 2>&1 || fail "Radar is not installed. Run '$0 install-radar' first."

  step "Checking the default Radar service account against all 37 resources"
  local logical group plural kind scope version scenario url answer pid log response ready
  while IFS=$'\t' read -r logical group plural kind scope version scenario url; do
    answer="$(kctl auth can-i list "${plural}.${group}" --as "system:serviceaccount:${RADAR_NS}:radar" 2>/dev/null || true)"
    [ "${answer}" = "yes" ] || fail "Default Radar SA cannot list ${plural}.${group}"
  done < <(registry_rows)

  log="$(mktemp)"
  CLEANUP_FILES+=("${log}")
  kctl -n "${RADAR_NS}" port-forward svc/radar "${RADAR_PORT}:9280" >"${log}" 2>&1 &
  pid=$!
  CLEANUP_PIDS+=("${pid}")
  ready=false
  for _ in $(seq 1 40); do
    if response="$(curl -fsS "http://127.0.0.1:${RADAR_PORT}/api/health" 2>/dev/null)" && \
      jq -e '.status == "healthy"' <<<"${response}" >/dev/null; then
      ready=true
      break
    fi
    kill -0 "${pid}" >/dev/null 2>&1 || fail "Radar port-forward exited: $(tail -n 5 "${log}" | tr '\n' ' ')"
    sleep 1
  done
  [ "${ready}" = "true" ] || fail "Radar did not report healthy within 40s; port-forward log: $(tail -n 5 "${log}" | tr '\n' ' ')"

  step "Querying Radar's group-aware API for every curated fixture"
  while IFS=$'\t' read -r logical group plural kind scope version scenario url; do
    ready=false
    for _ in $(seq 1 30); do
      if response="$(curl -fsS "http://127.0.0.1:${RADAR_PORT}/api/resources/${plural}?group=${group}" 2>/dev/null)" && \
        jq -e --arg g "${group}" 'type == "array" and length > 0 and all(.[]; (.apiVersion | split("/")[0]) == $g)' <<<"${response}" >/dev/null; then
        ready=true
        break
      fi
      kill -0 "${pid}" >/dev/null 2>&1 || fail "Radar port-forward exited while querying ${plural}.${group}: $(tail -n 5 "${log}" | tr '\n' ' ')"
      sleep 1
    done
    [ "${ready}" = "true" ] || fail "Radar did not return group-pure ${plural}.${group} objects within 30s"
  done < <(registry_rows)

  response="$(curl -fsS "http://127.0.0.1:${RADAR_PORT}/api/resources/jobs?group=batch")"
  jq -e 'length > 0 and all(.[]; (.apiVersion // "batch/v1") == "batch/v1") and any(.[]; .metadata.name == "collision-demo")' \
    <<<"${response}" >/dev/null || fail "Radar did not keep the core batch/v1 Job collision fixture separate"
  kill "${pid}" >/dev/null 2>&1 || true
  wait "${pid}" 2>/dev/null || true
  ok "Default chart RBAC and Radar discovery/API passed for all 37 identities"
}

cmd_status() {
  require_cluster
  validate_scenario
  assert_matching_scenario
  step "Scenario"
  note "configured=${SCENARIO}, installed=$(scenario_in_cluster)"
  step "Coverage"
  registry_rows | awk -F '\t' '{printf "%-28s %-42s %s\n", $4, $3 "." $2, $6}'
  step "Cluster totals"
  note "curated identities: $(logical_count)"
  note "installed matching CRDs: $(registry_rows | while IFS=$'\t' read -r _ group plural _; do kctl get "crd/${plural}.${group}" -o name 2>/dev/null || true; done | sort -u | wc -l | tr -d ' ')"
  if kctl -n "${RADAR_NS}" get deploy radar >/dev/null 2>&1; then
    kctl -n "${RADAR_NS}" get pods
  fi
}

usage() {
  cat <<EOF
Usage: $0 <command>

Commands:
  up             Create/reuse kind, install pinned upstream CRDs, seed fixtures, verify them
  down           Delete the dedicated kind cluster
  reset          Recreate the cluster and fixtures
  status         Show scenario, registry, CRD, and Radar inventory
  verify         Verify the cluster-side 37-kind contract
  install-radar  Build current code, install the chart with default RBAC, then verify Radar
  verify-radar   Verify default chart RBAC plus Radar discovery/API for all 37 kinds
  help           Show this message

Environment:
  CLUSTER_NAME   kind cluster name (default: radar-gpu-ecosystem-demo)
  SCENARIO       current (default) or experimental inference API family
  RADAR_PORT     local port used by verify-radar (default: 19280)

This validates discovery, group collisions, schemas, rendering inputs, and chart RBAC.
It does not validate GPU hardware, drivers, CUDA, DRA allocation, utilization, or accounting.
EOF
}

case "${1:-help}" in
  up) cmd_up ;;
  down) cmd_down ;;
  reset) cmd_down; cmd_up ;;
  status) cmd_status ;;
  verify) cmd_verify ;;
  install-radar) cmd_install_radar ;;
  verify-radar) cmd_verify_radar ;;
  help|-h|--help) usage ;;
  *) fail "unknown subcommand: $1 (try '$0 help')" ;;
esac
