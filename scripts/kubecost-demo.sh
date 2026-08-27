#!/usr/bin/env bash
# Bootstrap a disposable kind cluster with Kubecost 3, deterministic prices,
# and recognizable workloads for exercising Radar's Kubecost cost source.
#
# Subcommands:
#   up             Create the cluster, install Kubecost, deploy fixtures, and
#                  wait for allocation data to reach the Aggregator.
#   down           Delete the kind cluster.
#   reset          down + up.
#   status         Show Kubecost and fixture inventory.
#   query          Query the Aggregator allocation and asset APIs directly.
#   install-radar  Build the current Go tree, install Radar in-cluster, and
#                  exercise the Service-DNS connection lane.
#   radar-smoke    Assert current summary/workload/node responses and the
#                  current-only history contract against in-cluster Radar, or
#                  local Radar when RADAR_BASE_URL is set.
#   help           Show this message.
#
# Prerequisites:
#   - kind, kubectl, helm, curl, jq
#   - docker + go (install-radar only)
#
# Set CLUSTER_NAME=foo to use another cluster (default: radar-kubecost-demo).
# Set KUBECOST_CHART_VERSION deliberately when testing a chart upgrade.
#
# See scripts/kubecost-demo/README.md before changing the topology or versions.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-kubecost-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.34.0}"
KUBECOST_CHART_VERSION="${KUBECOST_CHART_VERSION:-3.2.4}"
KUBECOST_CHART_REPO="${KUBECOST_CHART_REPO:-https://kubecost.github.io/kubecost/}"
KUBECOST_RELEASE="${KUBECOST_RELEASE:-kubecost}"
KUBECOST_NS="${KUBECOST_NS:-kubecost}"
DEMO_NS="${DEMO_NS:-cost-demo}"
RADAR_NS="${RADAR_NS:-radar}"
CLUSTER_ID="${CLUSTER_ID:-${CLUSTER_NAME}}"
DISPLAY_CURRENCY="$(printf '%s' "${DISPLAY_CURRENCY:-EUR}" | tr '[:lower:]' '[:upper:]')"
KUBECOST_LOCAL_PORT="${KUBECOST_LOCAL_PORT:-39004}"
RADAR_LOCAL_PORT="${RADAR_LOCAL_PORT:-39280}"
DATA_TIMEOUT_SECONDS="${DATA_TIMEOUT_SECONDS:-600}"
CURL_OPTS=(-fsS --connect-timeout 2 --max-time 5)
RADAR_CURL_OPTS=(-fsS --connect-timeout 2 --max-time 30)

FORWARD_PID=""
FORWARD_LOG=""
FORWARD_TARGET=""
KUBECOST_API_BASE=""
TEMP_PATHS=()

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

validate_currency() {
  case "${DISPLAY_CURRENCY}" in
    [A-Z][A-Z][A-Z]) ;;
    *) fail "DISPLAY_CURRENCY must be a 3-letter ISO 4217 code" ;;
  esac
}

kctl() {
  kubectl --context "${KUBECTL_CTX}" "$@"
}

helm_ctx() {
  helm --kube-context "${KUBECTL_CTX}" "$@"
}

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"
}

require_cluster() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist. Run '$0 up' first."
}

stop_forward() {
  if [ -n "${FORWARD_PID}" ]; then
    if kill -0 "${FORWARD_PID}" 2>/dev/null; then
      kill "${FORWARD_PID}" 2>/dev/null || true
    fi
    wait "${FORWARD_PID}" 2>/dev/null || true
  fi
  FORWARD_PID=""
  if [ -n "${FORWARD_LOG}" ]; then
    rm -f "${FORWARD_LOG}"
  fi
  FORWARD_LOG=""
  FORWARD_TARGET=""
}

assert_forward_alive() {
  if [ -n "${FORWARD_PID}" ] && ! kill -0 "${FORWARD_PID}" 2>/dev/null; then
    tail -20 "${FORWARD_LOG}" >&2 || true
    fail "port-forward to ${FORWARD_TARGET} exited while waiting for data"
  fi
}

cleanup() {
  stop_forward
  local path
  for path in "${TEMP_PATHS[@]-}"; do
    [ -n "${path}" ] || continue
    if [ -d "${path}" ]; then
      rm -rf "${path}"
    elif [ -e "${path}" ]; then
      rm -f "${path}"
    fi
  done
}

trap cleanup EXIT

start_forward() {
  local namespace="$1" target="$2" ports="$3" ready_url="$4"
  local local_port="${ports%%:*}"
  stop_forward
  FORWARD_LOG="$(mktemp)"
  FORWARD_TARGET="${namespace}/${target}"
  kubectl --context "${KUBECTL_CTX}" -n "${namespace}" port-forward "${target}" "${ports}" >"${FORWARD_LOG}" 2>&1 &
  FORWARD_PID=$!

  local attempt
  for attempt in $(seq 1 30); do
    assert_forward_alive
    if grep -q "^Forwarding from 127[.]0[.]0[.]1:${local_port} " "${FORWARD_LOG}" &&
      curl "${CURL_OPTS[@]}" "${ready_url}" >/dev/null 2>&1; then
      assert_forward_alive
      return
    fi
    sleep 1
  done
  tail -20 "${FORWARD_LOG}" >&2 || true
  fail "timed out waiting for port-forward to ${namespace}/${target}"
}

start_aggregator_forward() {
  start_forward "${KUBECOST_NS}" "service/${KUBECOST_RELEASE}-aggregator" \
    "${KUBECOST_LOCAL_PORT}:9004" "http://127.0.0.1:${KUBECOST_LOCAL_PORT}/healthz"
}

detect_kubecost_api_base() {
  local candidate response deadline
  deadline=$(($(date +%s) + 120))
  while [ "$(date +%s)" -lt "${deadline}" ]; do
    assert_forward_alive
    for candidate in "" "/model"; do
      response="$(curl "${CURL_OPTS[@]}" --get "http://127.0.0.1:${KUBECOST_LOCAL_PORT}${candidate}/allocation" \
        --data-urlencode 'window=1d' \
        --data-urlencode 'aggregate=cluster' \
        --data-urlencode 'accumulate=true' \
        --data-urlencode 'idle=false' \
        --data-urlencode 'shareIdle=false' 2>/dev/null || true)"
      if jq -e '(.code == 0 or .code == 200) and (.data | type == "array")' >/dev/null 2>&1 <<<"${response}"; then
        KUBECOST_API_BASE="http://127.0.0.1:${KUBECOST_LOCAL_PORT}${candidate}"
        return
      fi
    done
    sleep 5
  done
  fail "Aggregator became healthy but its allocation API did not finish warming within 2 minutes"
}

fetch_allocations() {
  local window="${1:-1d}" idle="${2:-true}" namespace="${3:-}"
  local filter="cluster:\"${CLUSTER_ID}\""
  if [ -n "${namespace}" ]; then
    filter="${filter}+namespace:\"${namespace}\""
  fi
  curl "${CURL_OPTS[@]}" --get "${KUBECOST_API_BASE}/allocation" \
    --data-urlencode "window=${window}" \
    --data-urlencode 'aggregate=cluster,namespace,pod,controllerKind,controller' \
    --data-urlencode 'accumulate=true' \
    --data-urlencode "idle=${idle}" \
    --data-urlencode 'shareIdle=false' \
    --data-urlencode "filter=${filter}"
}

fetch_assets() {
  curl "${CURL_OPTS[@]}" --get "${KUBECOST_API_BASE}/assets" \
    --data-urlencode 'window=1d' \
    --data-urlencode 'accumulate=true' \
    --data-urlencode "filter=cluster:\"${CLUSTER_ID}\"+assetType:\"node\""
}

wait_for_cost_data() {
  step "Waiting for Kubecost allocation data (bounded at ${DATA_TIMEOUT_SECONDS}s)"
  start_aggregator_forward
  detect_kubecost_api_base

  local started now response
  started="$(date +%s)"
  while true; do
    assert_forward_alive
    response="$(fetch_allocations 2>/dev/null || true)"
    if jq -e --arg cluster "${CLUSTER_ID}" --arg namespace "${DEMO_NS}" \
      '[.data[]?[]? | select(
        .properties.cluster == $cluster and .properties.namespace == $namespace
      ) | .properties.pod | select(type == "string")] as $pods |
      any($pods[]?; startswith("checkout-")) and
      (($pods | index("orders-0")) != null) and
      any($pods[]?; startswith("telemetry-"))' \
      >/dev/null 2>&1 <<<"${response}"; then
      ok "Aggregator has allocation data for all three ${CLUSTER_ID}/${DEMO_NS} fixtures"
      stop_forward
      return
    fi
    now="$(date +%s)"
    if [ $((now - started)) -ge "${DATA_TIMEOUT_SECONDS}" ]; then
      warn "Kubecost pods are ready but allocation data has not reached the Aggregator"
      note "Run '$0 status', then '$0 query' after another emission cycle."
      stop_forward
      return 1
    fi
    sleep 15
  done
}

kubecost_values() {
  cat <<EOF
global:
  clusterId: ${CLUSTER_ID}

aggregator:
  enabled: true
  useEmptyDir: true

localStore:
  enabled: true
  persistentVolume:
    enabled: false

finopsagent:
  enabled: true
  agent:
    exportIntervals:
      allocation: 1m
      asset: 1m
    kubecost:
      customPrices:
        enabled: true
        CPU: "0.031"
        RAM: "0.004"
        storage: "0.00014"

kubecostProductConfigs:
  currencyCode: ${DISPLAY_CURRENCY}

frontend:
  enabled: false
networkCosts:
  enabled: false
forecasting:
  enabled: false
cloudCost:
  enabled: false
clusterController:
  enabled: false
telemetry:
  enabled: false
diagnostics:
  enabled: false
heartbeat:
  enabled: false
EOF
}

apply_fixtures() {
  step "Applying ${DEMO_NS} cost fixtures"
  kctl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: ${DEMO_NS}
  labels:
    app.kubernetes.io/part-of: demo-shop
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout
  namespace: ${DEMO_NS}
  labels:
    app.kubernetes.io/name: checkout
    app.kubernetes.io/part-of: demo-shop
spec:
  replicas: 2
  selector:
    matchLabels: {app: checkout}
  template:
    metadata:
      labels:
        app: checkout
        app.kubernetes.io/name: checkout
        app.kubernetes.io/part-of: demo-shop
    spec:
      containers:
        - name: checkout
          image: registry.k8s.io/pause:3.10
          resources:
            requests: {cpu: 250m, memory: 128Mi}
---
apiVersion: v1
kind: Service
metadata:
  name: orders
  namespace: ${DEMO_NS}
spec:
  clusterIP: None
  selector: {app: orders}
  ports: [{name: placeholder, port: 80}]
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: orders
  namespace: ${DEMO_NS}
  labels:
    app.kubernetes.io/name: orders
    app.kubernetes.io/part-of: demo-shop
spec:
  serviceName: orders
  replicas: 1
  selector:
    matchLabels: {app: orders}
  template:
    metadata:
      labels:
        app: orders
        app.kubernetes.io/name: orders
        app.kubernetes.io/part-of: demo-shop
    spec:
      containers:
        - name: orders
          image: registry.k8s.io/pause:3.10
          resources:
            requests: {cpu: 150m, memory: 96Mi}
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: telemetry
  namespace: ${DEMO_NS}
  labels:
    app.kubernetes.io/name: telemetry
    app.kubernetes.io/part-of: demo-shop
spec:
  selector:
    matchLabels: {app: telemetry}
  template:
    metadata:
      labels:
        app: telemetry
        app.kubernetes.io/name: telemetry
        app.kubernetes.io/part-of: demo-shop
    spec:
      containers:
        - name: telemetry
          image: registry.k8s.io/pause:3.10
          resources:
            requests: {cpu: 50m, memory: 48Mi}
EOF

  kctl -n "${DEMO_NS}" rollout status deployment/checkout --timeout=180s >/dev/null
  kctl -n "${DEMO_NS}" rollout status statefulset/orders --timeout=180s >/dev/null
  kctl -n "${DEMO_NS}" rollout status daemonset/telemetry --timeout=180s >/dev/null
  ok "Deployment, StatefulSet, and DaemonSet fixtures are ready"
}

cmd_up() {
  validate_currency
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd helm "https://helm.sh/docs/intro/install/"
  require_cmd curl "https://curl.se/download.html"
  require_cmd jq "https://jqlang.github.io/jq/download/"

  if cluster_exists; then
    ok "kind cluster ${CLUSTER_NAME} already exists"
  else
    step "Creating kind cluster ${CLUSTER_NAME} (${KIND_NODE_IMAGE})"
    kind create cluster --name "${CLUSTER_NAME}" --image "${KIND_NODE_IMAGE}" --wait 60s
  fi
  kctl cluster-info >/dev/null || fail "kind context ${KUBECTL_CTX} is not reachable"

  local values
  values="$(mktemp)"
  TEMP_PATHS+=("${values}")
  kubecost_values >"${values}"
  step "Installing Kubecost ${KUBECOST_CHART_VERSION} (ephemeral, trimmed demo profile)"
  helm_ctx upgrade --install "${KUBECOST_RELEASE}" kubecost \
    --repo "${KUBECOST_CHART_REPO}" \
    --version "${KUBECOST_CHART_VERSION}" \
    --namespace "${KUBECOST_NS}" --create-namespace \
    --values "${values}" \
    --wait --timeout 10m
  rm -f "${values}"

  kctl -n "${KUBECOST_NS}" rollout status deployment/"${KUBECOST_RELEASE}-finopsagent" --timeout=300s >/dev/null
  kctl -n "${KUBECOST_NS}" rollout status deployment/"${KUBECOST_RELEASE}-local-store" --timeout=300s >/dev/null
  kctl -n "${KUBECOST_NS}" rollout status statefulset/"${KUBECOST_RELEASE}-aggregator" --timeout=300s >/dev/null
  ok "FinOps Agent, local store, and Aggregator are ready"

  apply_fixtures
  if ! wait_for_cost_data; then
    fail "Kubecost did not produce all three fixture allocations within ${DATA_TIMEOUT_SECONDS}s"
  fi

  echo
  ok "Kubecost demo ready"
  note "Context: ${KUBECTL_CTX}"
  note "Run '$0 query' to inspect the raw allocation and asset APIs."
  note "For local Radar: kubectl config use-context ${KUBECTL_CTX}"
  note "Then run Radar with RADAR_COST_SOURCE=kubecost and RADAR_KUBECOST_CLUSTER_ID=${CLUSTER_ID}."
}

cmd_query() {
  require_cluster
  require_cmd curl "https://curl.se/download.html"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  start_aggregator_forward
  detect_kubecost_api_base

  local allocations assets
  if ! allocations="$(fetch_allocations)"; then
    fail "failed to query Kubecost allocations"
  fi
  if ! assets="$(fetch_assets)"; then
    fail "failed to query Kubecost assets"
  fi

  step "Allocation API (${KUBECOST_API_BASE}/allocation)"
  jq --arg cluster "${CLUSTER_ID}" '{
    code,
    cluster: $cluster,
    rows: ([.data[]?[]? | select(. != null)] | length),
    namespaces: ([.data[]?[]?.properties.namespace | select(. != null and . != "")] | unique),
    controllers: ([.data[]?[]? | select(.properties.controller != null) | {
      namespace: .properties.namespace,
      kind: .properties.controllerKind,
      name: .properties.controller
    }] | unique),
    podsWithoutController: ([.data[]?[]? | select(
      .properties.pod != null and
      (.properties.controller == null or .properties.controller == "__unallocated__")
    ) | {
      namespace: .properties.namespace,
      pod: .properties.pod
    }] | unique)
  }' <<<"${allocations}"

  step "Asset API (${KUBECOST_API_BASE}/assets)"
  jq --arg cluster "${CLUSTER_ID}" '{
    code,
    cluster: $cluster,
    rows: ([.data[]?[]? | select(. != null)] | length),
    nodes: ([.data[]?[]?.properties.name | select(. != null and . != "")] | unique)
  }' <<<"${assets}"
}

cmd_status() {
  require_cluster
  require_cmd helm "https://helm.sh/docs/intro/install/"
  step "Kubecost Helm release"
  helm_ctx -n "${KUBECOST_NS}" status "${KUBECOST_RELEASE}" 2>/dev/null | sed -n '1,12p' || warn "release missing"
  step "Kubecost pods"
  kctl -n "${KUBECOST_NS}" get pods -o wide 2>/dev/null || warn "namespace missing"
  step "Aggregator service"
  kctl -n "${KUBECOST_NS}" get service "${KUBECOST_RELEASE}-aggregator" -o wide 2>/dev/null || warn "service missing"
  step "Cost fixtures"
  kctl -n "${DEMO_NS}" get deployment,statefulset,daemonset,pod 2>/dev/null || warn "fixtures missing"
  if kctl -n "${RADAR_NS}" get deployment radar >/dev/null 2>&1; then
    step "In-cluster Radar"
    kctl -n "${RADAR_NS}" get deployment,pod
  fi
}

cmd_install_radar() {
  require_cluster
  require_cmd docker "https://docs.docker.com/get-docker/"
  require_cmd helm "https://helm.sh/docs/intro/install/"
  require_cmd go "https://go.dev/dl/"

  local repo_root arch build_dir
  repo_root="$(cd "$(dirname "$0")/.." && pwd)"
  arch="$(kctl get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')"
  case "${arch}" in
    arm64|amd64) ;;
    *) fail "unsupported kind node architecture: ${arch:-unknown}" ;;
  esac
  build_dir="$(mktemp -d)"
  TEMP_PATHS+=("${build_dir}")

  step "Building current Radar backend for linux/${arch}"
  (cd "${repo_root}" && CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -o "${build_dir}/radar" ./cmd/explorer)
  cat >"${build_dir}/Dockerfile" <<'EOF'
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY radar /radar
ENTRYPOINT ["/radar"]
EOF
  docker build --platform "linux/${arch}" -q -t radar-kubecost-demo:dev "${build_dir}" >/dev/null
  kind load docker-image radar-kubecost-demo:dev --name "${CLUSTER_NAME}" >/dev/null
  rm -rf "${build_dir}"

  step "Installing current Radar with Kubecost pinned as the source"
  helm_ctx upgrade --install radar "${repo_root}/deploy/helm/radar" \
    --namespace "${RADAR_NS}" --create-namespace \
    --set image.repository=radar-kubecost-demo \
    --set image.tag=dev \
    --set image.pullPolicy=Never \
    --set cost.source=kubecost \
    --set cost.kubecost.clusterId="${CLUSTER_ID}" \
    --set rbac.portForward=false \
    --wait --timeout 180s >/dev/null
  kctl -n "${RADAR_NS}" rollout restart deployment/radar >/dev/null
  kctl -n "${RADAR_NS}" rollout status deployment/radar --timeout=180s >/dev/null
  ok "Radar is running in-cluster against Kubecost Service DNS"
  note "Run '$0 radar-smoke' to assert Radar's API contract."
}

cmd_radar_smoke() {
  validate_currency
  require_cluster
  require_cmd curl "https://curl.se/download.html"
  require_cmd jq "https://jqlang.github.io/jq/download/"

  start_aggregator_forward
  detect_kubecost_api_base
  local raw_allocations raw_window premise_drift=false
  raw_window="1h"
  if ! raw_allocations="$(fetch_allocations "${raw_window}" false "${DEMO_NS}")"; then
    fail "failed to query Kubecost allocations for the ${raw_window} current-data window"
  fi
  if ! jq -e 'any(.data[]?[]?; . != null)' >/dev/null <<<"${raw_allocations}"; then
    raw_window="1d"
    if ! raw_allocations="$(fetch_allocations "${raw_window}" false "${DEMO_NS}")"; then
      fail "failed to query Kubecost allocations for the ${raw_window} current-data window"
    fi
  fi
  if ! jq -e --arg namespace "${DEMO_NS}" '
    any(.data[]?[]?; .properties.namespace == $namespace and .properties.pod == "orders-0")
    ' >/dev/null <<<"${raw_allocations}"; then
    fail "Kubecost returned no orders-0 allocation in its ${raw_window} current-data window"
  fi
  if jq -e --arg namespace "${DEMO_NS}" '
    any(.data[]?[]?; .properties.namespace == $namespace and
      .properties.pod == "orders-0" and
      (.properties.controller == null or .properties.controller == "__unallocated__"))
    ' >/dev/null <<<"${raw_allocations}"; then
    ok "Kubecost left orders-0 unresolved in ${raw_window}, exercising Radar's pod-owner fallback"
  elif [ "${KUBECOST_CHART_VERSION}" = "3.2.4" ]; then
    warn "Kubecost 3.2.4 no longer exposes the unresolved orders-0 premise expected by this smoke test"
    premise_drift=true
  else
    warn "Kubecost ${KUBECOST_CHART_VERSION} resolved orders-0 directly; the pod-owner fallback premise is not exercised"
  fi
  stop_forward

  local radar_root base
  radar_root="${RADAR_BASE_URL:-}"
  if [ -n "${radar_root}" ]; then
    radar_root="${radar_root%/}"
    note "Testing local Radar at ${radar_root}"
  else
    kctl -n "${RADAR_NS}" get deployment radar >/dev/null 2>&1 || fail "Radar is not installed. Run '$0 install-radar' first, or set RADAR_BASE_URL for local Radar."
    start_forward "${RADAR_NS}" "deployment/radar" "${RADAR_LOCAL_PORT}:9280" \
      "http://127.0.0.1:${RADAR_LOCAL_PORT}/api/cluster-info"
    radar_root="http://127.0.0.1:${RADAR_LOCAL_PORT}"
  fi
  base="${radar_root}/api/opencost"

  step "Waiting for Radar's Kubecost responses (120s retry window; in-flight requests may extend it)"
  local summary="" workloads="" nodes="" trend="" deadline smoke_ready=false
  deadline=$(($(date +%s) + 120))
  while [ "$(date +%s)" -lt "${deadline}" ]; do
    assert_forward_alive
    summary="$(curl "${RADAR_CURL_OPTS[@]}" "${base}/summary" 2>/dev/null || true)"
    workloads="$(curl "${RADAR_CURL_OPTS[@]}" "${base}/workloads?namespace=${DEMO_NS}" 2>/dev/null || true)"
    nodes="$(curl "${RADAR_CURL_OPTS[@]}" "${base}/nodes" 2>/dev/null || true)"
    trend="$(curl "${RADAR_CURL_OPTS[@]}" "${base}/trend?range=24h" 2>/dev/null || true)"
    if jq -e --arg currency "${DISPLAY_CURRENCY}" '
        .available == true and .source == "kubecost" and .currency == $currency and
        (.dataThrough | type == "string" and length > 0) and (.namespaces | length > 0)
      ' >/dev/null 2>&1 <<<"${summary}" &&
      jq -e '
        .available == true and .source == "kubecost" and
        (.dataThrough | type == "string" and length > 0) and
        any(.workloads[]?; .name == "checkout" and .kind == "Deployment") and
        any(.workloads[]?; .name == "orders" and .kind == "StatefulSet") and
        any(.workloads[]?; .name == "telemetry" and .kind == "DaemonSet")
      ' >/dev/null 2>&1 <<<"${workloads}" &&
      jq -e '.available == true and .source == "kubecost" and (.nodes | length > 0)' >/dev/null 2>&1 <<<"${nodes}" &&
      jq -e '.available == false and .source == "kubecost" and .reason == "history_unsupported"' >/dev/null 2>&1 <<<"${trend}"; then
      smoke_ready=true
      break
    fi
    sleep 5
  done
  if [ "${smoke_ready}" != true ]; then
    if ! jq -e '.available == true' >/dev/null 2>&1 <<<"${summary}"; then
      printf 'Last Radar summary response:\n%s\n' "${summary}" >&2
      fail "Radar summary did not return available cost data"
    fi
    if ! jq -e '.source == "kubecost"' >/dev/null 2>&1 <<<"${summary}"; then
      printf 'Last Radar summary response:\n%s\n' "${summary}" >&2
      fail "Radar summary did not select Kubecost"
    fi
    if ! jq -e --arg currency "${DISPLAY_CURRENCY}" '.currency == $currency' >/dev/null 2>&1 <<<"${summary}"; then
      printf 'Last Radar summary response:\n%s\n' "${summary}" >&2
      fail "Radar summary currency was not ${DISPLAY_CURRENCY}"
    fi
    if ! jq -e '(.dataThrough | type == "string" and length > 0) and (.namespaces | length > 0)' >/dev/null 2>&1 <<<"${summary}"; then
      printf 'Last Radar summary response:\n%s\n' "${summary}" >&2
      fail "Radar summary did not include current namespace cost data"
    fi
    if ! jq -e '.available == true and .source == "kubecost" and (.dataThrough | type == "string" and length > 0) and any(.workloads[]?; .name == "checkout" and .kind == "Deployment") and any(.workloads[]?; .name == "orders" and .kind == "StatefulSet") and any(.workloads[]?; .name == "telemetry" and .kind == "DaemonSet")' >/dev/null 2>&1 <<<"${workloads}"; then
      printf 'Last Radar workload response:\n%s\n' "${workloads}" >&2
      fail "Radar workload response did not include all three cost-demo controllers"
    fi
    if ! jq -e '.available == true and .source == "kubecost" and (.nodes | length > 0)' >/dev/null 2>&1 <<<"${nodes}"; then
      printf 'Last Radar node response:\n%s\n' "${nodes}" >&2
      fail "Radar node response did not include Kubecost assets"
    fi
    if ! jq -e '.available == false and .source == "kubecost" and .reason == "history_unsupported"' >/dev/null 2>&1 <<<"${trend}"; then
      printf 'Last Radar trend response:\n%s\n' "${trend}" >&2
      fail "Radar trend response did not report Kubecost history_unsupported"
    fi
  fi

  ok "Radar Kubecost smoke checks passed"
  jq '{available, source, currency, dataThrough, namespaceCount: (.namespaces | length)}' <<<"${summary}"
  jq '{available, source, workloadCount: (.workloads | length)}' <<<"${workloads}"
  jq '{available, source, nodeCount: (.nodes | length)}' <<<"${nodes}"
  jq '{available, source, reason}' <<<"${trend}"
  if [ "${premise_drift}" = true ]; then
    fail "Radar's API contract passed, but Kubecost 3.2.4 did not exercise the pod-owner fallback premise"
  fi
}

cmd_down() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  if cluster_exists; then
    step "Deleting kind cluster ${CLUSTER_NAME}"
    kind delete cluster --name "${CLUSTER_NAME}"
  else
    ok "Cluster ${CLUSTER_NAME} does not exist"
  fi
}

usage() {
  sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
}

case "${1:-help}" in
  up) cmd_up ;;
  down) cmd_down ;;
  reset) cmd_down; cmd_up ;;
  status) cmd_status ;;
  query) cmd_query ;;
  install-radar) cmd_install_radar ;;
  radar-smoke) cmd_radar_smoke ;;
  help|-h|--help) usage ;;
  *) fail "unknown subcommand: $1 (try '$0 help')" ;;
esac
