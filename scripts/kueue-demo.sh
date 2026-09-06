#!/usr/bin/env bash
# Controller-backed Kueue admission fixtures for Radar. This complements the
# controller-free 37-kind GPU ecosystem demo; it does not emulate GPU hardware.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-kueue-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"
KUEUE_VERSION="${KUEUE_VERSION:-v0.19.2}"
KUEUE_MANIFEST="https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-v1.36.1}"
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="${SCRIPT_DIR}/kueue-demo"
DEMO_NS="kueue-demo"
MARKER_NAME="radar-kueue-demo-owner"
MARKER_VALUE="skyhook-radar-kueue-demo-v1"
WAIT_SECONDS="${WAIT_SECONDS:-300}"
RADAR_URL="${RADAR_URL:-http://127.0.0.1:9280}"
PREVIOUS_CONTEXT=''
RESTORE_CONTEXT=false

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

kc() { kubectl --context "${KUBECTL_CTX}" "$@"; }

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"
}

cluster_owned() {
  local owner cluster
  owner="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.owner}' 2>/dev/null || true)"
  cluster="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.clusterName}' 2>/dev/null || true)"
  [ "${owner}" = "${MARKER_VALUE}" ] && [ "${cluster}" = "${CLUSTER_NAME}" ]
}

require_owned_cluster() {
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist. Run '$0 up' first."
  cluster_owned || fail "Refusing to use unowned cluster '${CLUSTER_NAME}': ownership marker ${MARKER_NAME} is missing or mismatched."
  kc cluster-info >/dev/null || fail "kind context '${KUBECTL_CTX}' is not reachable"
}

mark_cluster() {
  kc -n kube-system create configmap "${MARKER_NAME}" \
    --from-literal="owner=${MARKER_VALUE}" \
    --from-literal="clusterName=${CLUSTER_NAME}" \
    --from-literal="kueueVersion=${KUEUE_VERSION}" \
    --from-literal="kubernetesVersion=${KUBERNETES_VERSION}" \
    --from-literal="kindNodeImage=${KIND_NODE_IMAGE}" \
    --dry-run=client -o yaml | kc apply -f - >/dev/null
}

restore_previous_context() {
  [ "${RESTORE_CONTEXT}" = "true" ] || return 0
  if [ -z "${PREVIOUS_CONTEXT}" ]; then
    kubectl config unset current-context >/dev/null 2>&1 || true
  elif kubectl config get-contexts "${PREVIOUS_CONTEXT}" >/dev/null 2>&1 && \
    [ "$(kubectl config current-context 2>/dev/null || true)" != "${PREVIOUS_CONTEXT}" ]; then
    kubectl config use-context "${PREVIOUS_CONTEXT}" >/dev/null 2>&1 || true
  fi
}

prepare_context_restore() {
  if [ "${RESTORE_CONTEXT}" != "true" ]; then
    PREVIOUS_CONTEXT="$(kubectl config current-context 2>/dev/null || true)"
    RESTORE_CONTEXT=true
    trap restore_previous_context EXIT
  fi
}

assert_cluster_contract() {
  local recorded_image recorded_version server_version
  recorded_image="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.kindNodeImage}')"
  recorded_version="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.kubernetesVersion}')"
  server_version="$(kc version -o json | jq -r '.serverVersion.gitVersion')"
  [ "${recorded_image}" = "${KIND_NODE_IMAGE}" ] || \
    fail "Cluster uses recorded kind image '${recorded_image:-unknown}', expected '${KIND_NODE_IMAGE}'. Run '$0 reset' to change it."
  [ "${recorded_version}" = "${KUBERNETES_VERSION}" ] || \
    fail "Cluster records Kubernetes '${recorded_version:-unknown}', expected '${KUBERNETES_VERSION}'. Run '$0 reset' to change it."
  [ "${server_version}" = "${KUBERNETES_VERSION}" ] || \
    fail "Cluster reports Kubernetes '${server_version}', expected '${KUBERNETES_VERSION}'."
}

wait_until() {
  local description="$1" deadline=$((SECONDS + WAIT_SECONDS))
  shift
  while [ "${SECONDS}" -lt "${deadline}" ]; do
    if "$@"; then
      return 0
    fi
    sleep 2
  done
  fail "Timed out after ${WAIT_SECONDS}s waiting for ${description}"
}

condition_is() {
  local namespace="$1" resource="$2" name="$3" type="$4" status="$5" reason_pattern="${6:-}"
  if [ "${namespace}" = "_" ]; then
    kc get "${resource}" "${name}" -o json 2>/dev/null
  else
    kc -n "${namespace}" get "${resource}" "${name}" -o json 2>/dev/null
  fi | jq -e --arg type "${type}" --arg status "${status}" --arg reason "${reason_pattern}" \
    'any(.status.conditions[]?; .type == $type and .status == $status and ($reason == "" or ((.reason // "") | test("^(" + $reason + ")$"))))' >/dev/null 2>&1
}

workload_for_job() {
  local job="$1" uid
  uid="$(kc -n "${DEMO_NS}" get job "${job}" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
  [ -n "${uid}" ] || return 1
  kc -n "${DEMO_NS}" get workloads.kueue.x-k8s.io -o json 2>/dev/null | jq -r --arg uid "${uid}" \
    'first(.items[] | select(any(.metadata.ownerReferences[]?; .uid == $uid and .kind == "Job" and .controller == true)) | .metadata.name) // empty'
}

workload_exists_for_job() {
  [ -n "$(workload_for_job "$1")" ]
}

workload_condition_is() {
  local job="$1" type="$2" status="$3" reason="${4:-}" workload
  workload="$(workload_for_job "${job}")"
  [ -n "${workload}" ] && condition_is "${DEMO_NS}" workloads.kueue.x-k8s.io "${workload}" "${type}" "${status}" "${reason}"
}

workload_message_contains() {
  local job="$1" type="$2" pattern="$3" workload
  workload="$(workload_for_job "${job}")"
  [ -n "${workload}" ] || return 1
  kc -n "${DEMO_NS}" get workloads.kueue.x-k8s.io "${workload}" -o json 2>/dev/null | jq -e \
    --arg type "${type}" --arg pattern "${pattern}" \
    'any(.status.conditions[]?; .type == $type and (.message // "" | test($pattern; "i")))' >/dev/null 2>&1
}

job_suspended_is() {
  local job="$1" expected="$2"
  kc -n "${DEMO_NS}" get job "${job}" -o json 2>/dev/null | jq -e --argjson expected "${expected}" '.spec.suspend == $expected' >/dev/null 2>&1
}

running_pod_exists() {
  kc -n "${DEMO_NS}" get pods -l "batch.kubernetes.io/job-name=$1" -o json 2>/dev/null | jq -e 'any(.items[]; .status.phase == "Running")' >/dev/null 2>&1
}

pod_count() {
  local response
  response="$(kc -n "${DEMO_NS}" get pods -l "batch.kubernetes.io/job-name=$1" -o json)" || return 1
  jq -er '.items | length' <<<"${response}"
}

workload_reason() {
  local workload="$1"
  kc -n "${DEMO_NS}" get workloads.kueue.x-k8s.io "${workload}" -o json | jq -r \
    '[.status.conditions[]? | select(.type == "QuotaReserved")][0] | "\(.reason // "unknown"): \(.message // "no message")"'
}

kueue_webhook_ready() {
  kc apply --dry-run=server -f - >/dev/null 2>&1 <<'EOF'
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: radar-kueue-demo-webhook-probe
spec:
  nodeLabels:
    kubernetes.io/os: linux
EOF
}

install_kueue() {
  step "Installing Kueue ${KUEUE_VERSION}"
  kc apply --server-side -f "${KUEUE_MANIFEST}" >/dev/null
  kc wait --for=condition=Established crd/workloads.kueue.x-k8s.io --timeout=180s >/dev/null
  kc -n kueue-system rollout status deployment/kueue-controller-manager --timeout=300s >/dev/null

  # Deployment availability can precede the admission webhook's TLS listener.
  wait_until "Kueue admission webhook to accept connections" kueue_webhook_ready

  local image
  image="$(kc -n kueue-system get deployment kueue-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}')"
  [ "${image}" = "registry.k8s.io/kueue/kueue:${KUEUE_VERSION}" ] || \
    fail "Kueue controller image is '${image}', expected registry.k8s.io/kueue/kueue:${KUEUE_VERSION}"
  ok "Kueue controller and admission webhook are Ready with the pinned image"
}

apply_fixtures() {
  step "Applying Kueue admission fixtures"
  note "applying 00-namespace.yaml"
  kc apply -f "${FIXTURES_DIR}/00-namespace.yaml" >/dev/null
  note "applying 10-queues.yaml"
  kc apply -f "${FIXTURES_DIR}/10-queues.yaml" >/dev/null

  local jobs_present=0 job
  for job in admitted-running quota-blocked queue-held; do
    if kc -n "${DEMO_NS}" get job "${job}" >/dev/null 2>&1; then
      jobs_present=$((jobs_present + 1))
    fi
  done
  if [ "${jobs_present}" = "3" ]; then
    note "preserving the three reconciled Jobs (use reset after changing Job fixtures)"
  else
    [ "${jobs_present}" = "0" ] || warn "Only ${jobs_present}/3 Jobs exist; applying the complete Job fixture may re-reconcile existing Jobs"
    note "applying 20-jobs.yaml"
    kc apply -f "${FIXTURES_DIR}/20-jobs.yaml" >/dev/null
  fi
  ok "Fixtures applied without patching status"
}

assert_workload_owner() {
  local job="$1" workload="$2" uid
  uid="$(kc -n "${DEMO_NS}" get job "${job}" -o jsonpath='{.metadata.uid}')"
  kc -n "${DEMO_NS}" get workloads.kueue.x-k8s.io "${workload}" -o json | jq -e --arg uid "${uid}" \
    'any(.metadata.ownerReferences[]?; .apiVersion == "batch/v1" and .kind == "Job" and .uid == $uid and .controller == true)' >/dev/null || \
    fail "Workload '${workload}' is not controller-owned by Job '${job}'"
}

verify_admitted() {
  local job="admitted-running" workload
  wait_until "Kueue Workload for ${job}" workload_exists_for_job "${job}"
  workload="$(workload_for_job "${job}")"
  assert_workload_owner "${job}" "${workload}"
  wait_until "${job} quota reservation" workload_condition_is "${job}" QuotaReserved True QuotaReserved
  wait_until "${job} admission" workload_condition_is "${job}" Admitted True Admitted
  wait_until "${job} PodsReady signal" workload_condition_is "${job}" PodsReady True 'Started|Recovered'
  wait_until "${job} unsuspension" job_suspended_is "${job}" false
  wait_until "${job} Pod to be Running" running_pod_exists "${job}"
  ok "${job}: ${workload} is admitted, Job is unsuspended, and a Pod is Running"
}

verify_quota_blocked() {
  local job="quota-blocked" workload pods
  wait_until "Kueue Workload for ${job}" workload_exists_for_job "${job}"
  workload="$(workload_for_job "${job}")"
  assert_workload_owner "${job}" "${workload}"
  wait_until "${job} to remain without quota" workload_condition_is "${job}" QuotaReserved False Pending
  wait_until "${job} quota explanation" workload_message_contains "${job}" QuotaReserved 'insufficient quota.*maximum capacity'
  wait_until "${job} to remain suspended" job_suspended_is "${job}" true
  pods="$(pod_count "${job}")" || fail "Failed to list Pods for ${job}"
  [ "${pods}" = "0" ] || fail "${job} unexpectedly created Pods"
  kc -n "${DEMO_NS}" get workloads.kueue.x-k8s.io "${workload}" -o json | jq -e '.status.admission == null' >/dev/null || \
    fail "${job} unexpectedly has an admission assignment"
  ok "${job}: controller reason is $(workload_reason "${workload}"); no Pod exists"
}

verify_held() {
  local job="queue-held" workload pods
  wait_until "admission-held ClusterQueue to become inactive" condition_is _ clusterqueues.kueue.x-k8s.io admission-held Active False Stopped
  wait_until "held LocalQueue to become inactive" condition_is "${DEMO_NS}" localqueues.kueue.x-k8s.io held Active False ClusterQueueIsInactive
  wait_until "Kueue Workload for ${job}" workload_exists_for_job "${job}"
  workload="$(workload_for_job "${job}")"
  assert_workload_owner "${job}" "${workload}"
  wait_until "${job} to remain without quota" workload_condition_is "${job}" QuotaReserved False Inadmissible
  wait_until "${job} held-queue explanation" workload_message_contains "${job}" QuotaReserved 'ClusterQueue admission-held is inactive'
  wait_until "${job} to remain suspended" job_suspended_is "${job}" true
  pods="$(pod_count "${job}")" || fail "Failed to list Pods for ${job}"
  [ "${pods}" = "0" ] || fail "${job} unexpectedly created Pods"
  [ "$(kc get clusterqueue admission-held -o jsonpath='{.spec.stopPolicy}')" = "Hold" ] || fail "admission-held no longer has stopPolicy=Hold"
  ok "${job}: held ClusterQueue is inactive, controller reason is $(workload_reason "${workload}"); no Pod exists"
}

cmd_verify() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  require_owned_cluster
  assert_cluster_contract
  step "Verifying controller-earned Kueue states"
  wait_until "admission-ready ClusterQueue to become active" condition_is _ clusterqueues.kueue.x-k8s.io admission-ready Active True Ready
  wait_until "ready LocalQueue to become active" condition_is "${DEMO_NS}" localqueues.kueue.x-k8s.io ready Active True Ready
  verify_admitted
  verify_quota_blocked
  verify_held
  ok "Kueue admitted, quota-blocked, and held-queue scenarios verified"
}

mcp_get_workload() {
  local workload="$1" request response event
  request="$(jq -cn \
    --arg namespace "${DEMO_NS}" \
    --arg workload "${workload}" \
    '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:"get_resource",arguments:{kind:"workloads",group:"kueue.x-k8s.io",namespace:$namespace,name:$workload}}}')"
  response="$(curl -fsS --connect-timeout 5 --max-time 15 \
    -X POST \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d "${request}" \
    "${RADAR_URL}/mcp")" || return 1
  event="$(sed -n 's/^data: //p' <<<"${response}" | tail -n 1)"
  [ -n "${event}" ] || return 1
  jq -er '.result.content[0].text | fromjson' <<<"${event}"
}

radar_health_ready() {
  curl -fsS --connect-timeout 2 --max-time 5 "${RADAR_URL}/api/health" 2>/dev/null | \
    jq -e '.status == "healthy"' >/dev/null 2>&1
}

radar_crd_discovery_ready() {
  curl -fsS --connect-timeout 2 --max-time 5 "${RADAR_URL}/api/cluster-info" 2>/dev/null | \
    jq -e '.crdDiscoveryStatus == "ready"' >/dev/null 2>&1
}

radar_workloads_ready() {
  curl -fsS --connect-timeout 2 --max-time 5 "${RADAR_URL}/api/resources/workloads?group=kueue.x-k8s.io" 2>/dev/null | \
    jq -e 'type == "array" and length == 3 and all(.[]; .apiVersion == "kueue.x-k8s.io/v1beta2")' >/dev/null 2>&1
}

radar_clusterqueues_ready() {
  curl -fsS --connect-timeout 2 --max-time 5 "${RADAR_URL}/api/resources/clusterqueues?group=kueue.x-k8s.io" 2>/dev/null | \
    jq -e 'type == "array" and length == 2 and all(.[]; .apiVersion == "kueue.x-k8s.io/v1beta2")' >/dev/null 2>&1
}

assert_radar_scheduling() {
  local job="$1" decision="$2" phase="$3" condition_type="$4" condition_status="$5" condition_reason="$6"
  local submission_queue="$7" entitlement_queue="${8:-}" workload rest_response mcp_response rest_scheduling mcp_scheduling
  if ! workload="$(workload_for_job "${job}")"; then
    fail "Unable to resolve the Kueue Workload for Job '${job}'"
  fi
  [ -n "${workload}" ] || fail "No Kueue Workload found for Job '${job}'"

  rest_response="$(curl -fsS --connect-timeout 5 --max-time 15 \
    "${RADAR_URL}/api/ai/resources/workloads/${DEMO_NS}/${workload}?group=kueue.x-k8s.io")" || \
    fail "Radar AI resource request failed for ${job}'s Workload '${workload}'"

  jq -e \
    --arg namespace "${DEMO_NS}" \
    --arg workload "${workload}" \
    --arg decision "${decision}" \
    --arg phase "${phase}" \
    --arg conditionType "${condition_type}" \
    --arg conditionStatus "${condition_status}" \
    --arg conditionReason "${condition_reason}" \
    --arg submissionQueue "${submission_queue}" \
    --arg entitlementQueue "${entitlement_queue}" '
      .resource.apiVersion == "kueue.x-k8s.io/v1beta2" and
      .resource.kind == "Workload" and
      .resource.metadata.namespace == $namespace and
      .resource.metadata.name == $workload and
      (.resourceContext.scheduling.observations | length) == 1 and
      (.resourceContext.scheduling.observations[0] as $observation |
        $observation.source == "kueue" and
        $observation.domain == "admission" and
        $observation.subject == {
          kind: "Workload",
          group: "kueue.x-k8s.io",
          namespace: $namespace,
          name: $workload
        } and
        $observation.subjectGeneration > 0 and
        $observation.decision == $decision and
        $observation.primaryCondition.type == $conditionType and
        $observation.primaryCondition.status == $conditionStatus and
        $observation.primaryCondition.reason == $conditionReason and
        $observation.primaryCondition.observedGeneration == $observation.subjectGeneration and
        $observation.kueue.phase == $phase and
        ($observation.queues | length) == (if $entitlementQueue == "" then 1 else 2 end) and
        $observation.queues[0].name == $submissionQueue and
        $observation.queues[0].roles == ["submission"] and
        (if $entitlementQueue == "" then
          true
        else
          $observation.queues[1].name == $entitlementQueue and
          $observation.queues[1].roles == ["entitlement"]
        end)
      )' <<<"${rest_response}" >/dev/null || \
    fail "Radar scheduling projection did not match ${job}'s controller-owned state"

  if ! mcp_response="$(mcp_get_workload "${workload}")"; then
    fail "MCP get_resource failed for ${job}'s Workload '${workload}'"
  fi
  rest_scheduling="$(jq -ceS '.resourceContext.scheduling' <<<"${rest_response}")" || \
    fail "REST scheduling projection is not valid JSON for ${job}"
  mcp_scheduling="$(jq -ceS '.resourceContext.scheduling' <<<"${mcp_response}")" || \
    fail "MCP scheduling projection is not valid JSON for ${job}"
  [ "${rest_scheduling}" = "${mcp_scheduling}" ] || \
    fail "REST and MCP scheduling projections differ for ${job}"
  ok "${job}: REST and MCP agree on ${decision}/${phase} from ${condition_type}=${condition_reason}"
}

cmd_verify_radar() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd curl "https://curl.se/download.html"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  require_owned_cluster
  assert_cluster_contract
  step "Verifying Radar API at ${RADAR_URL}"

  local response
  wait_until "Radar health endpoint" radar_health_ready

  response="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/cluster-info")" || \
    fail "Radar cluster-info request failed"
  jq -e --arg context "${KUBECTL_CTX}" '.context == $context and .cluster == $context' <<<"${response}" >/dev/null || \
    fail "Radar is not connected to the expected context and cluster '${KUBECTL_CTX}'"
  wait_until "Radar CRD discovery" radar_crd_discovery_ready

  wait_until "Radar to cache the three Kueue Workloads" radar_workloads_ready
  response="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/resources/workloads?group=kueue.x-k8s.io")" || \
    fail "Radar Workload list request failed"
  jq -e 'type == "array" and length == 3 and all(.[]; .apiVersion == "kueue.x-k8s.io/v1beta2")' <<<"${response}" >/dev/null || \
    fail "Radar did not return the three group-pure Kueue Workloads"
  jq -e 'any(.[]; any(.metadata.ownerReferences[]?; .kind == "Job" and .name == "admitted-running") and any(.status.conditions[]?; .type == "Admitted" and .status == "True"))' <<<"${response}" >/dev/null || \
    fail "Radar did not expose admitted-running's admitted Workload"
  jq -e 'any(.[]; any(.metadata.ownerReferences[]?; .kind == "Job" and .name == "quota-blocked") and any(.status.conditions[]?; .type == "QuotaReserved" and .status == "False"))' <<<"${response}" >/dev/null || \
    fail "Radar did not expose quota-blocked's pending Workload"
  jq -e 'any(.[]; any(.metadata.ownerReferences[]?; .kind == "Job" and .name == "queue-held") and any(.status.conditions[]?; .type == "QuotaReserved" and .status == "False" and .reason == "Inadmissible" and ((.message // "") | contains("ClusterQueue admission-held is inactive"))))' <<<"${response}" >/dev/null || \
    fail "Radar did not expose queue-held's inactive-queue Workload"

  wait_until "Radar to cache the two Kueue ClusterQueues" radar_clusterqueues_ready
  response="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/resources/clusterqueues?group=kueue.x-k8s.io")" || \
    fail "Radar ClusterQueue list request failed"
  jq -e 'type == "array" and length == 2 and all(.[]; .apiVersion == "kueue.x-k8s.io/v1beta2")' <<<"${response}" >/dev/null || \
    fail "Radar did not return the two group-pure ClusterQueues"

  assert_radar_scheduling admitted-running satisfied admitted Admitted True Admitted ready admission-ready
  assert_radar_scheduling quota-blocked unsatisfied pending QuotaReserved False Pending ready
  assert_radar_scheduling queue-held unsatisfied pending QuotaReserved False Inadmissible held
  ok "Radar returned real Kueue admission states with matching REST and MCP scheduling context"
}

cmd_up() {
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  [[ "${KUEUE_VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "KUEUE_VERSION must be a pinned vX.Y.Z release"
  [[ "${KUBERNETES_VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "KUBERNETES_VERSION must be a pinned vX.Y.Z release"
  prepare_context_restore

  if cluster_exists; then
    cluster_owned || fail "Refusing to modify existing unowned cluster '${CLUSTER_NAME}'. Choose another CLUSTER_NAME or remove it yourself."
    step "Reusing owned kind cluster '${CLUSTER_NAME}'"
    assert_cluster_contract
  else
    step "Creating dedicated kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --image "${KIND_NODE_IMAGE}" --wait 60s
    mark_cluster
    assert_cluster_contract
    ok "Cluster created and ownership marker recorded"
  fi

  require_owned_cluster
  mark_cluster
  install_kueue
  apply_fixtures
  cmd_verify
  note "See ${FIXTURES_DIR}/README.md for the isolated-kubeconfig Radar smoke test."
}

cmd_down() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  if ! cluster_exists; then
    ok "Cluster '${CLUSTER_NAME}' does not exist"
    return
  fi
  cluster_owned || fail "Refusing to delete unowned cluster '${CLUSTER_NAME}': ownership marker ${MARKER_NAME} is missing or mismatched."
  step "Deleting owned kind cluster '${CLUSTER_NAME}'"
  kind delete cluster --name "${CLUSTER_NAME}"
  ok "Deleted"
}

cmd_reset() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  prepare_context_restore
  cmd_down
  cmd_up
}

cmd_status() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  require_owned_cluster
  assert_cluster_contract
  step "Kueue controller"
  kc -n kueue-system get deployment kueue-controller-manager -o wide
  step "Queues"
  kc get clusterqueues.kueue.x-k8s.io
  kc -n "${DEMO_NS}" get localqueues.kueue.x-k8s.io
  step "Jobs, Workloads, and Pods"
  kc -n "${DEMO_NS}" get jobs
  kc -n "${DEMO_NS}" get workloads.kueue.x-k8s.io
  kc -n "${DEMO_NS}" get pods -o wide
}

usage() {
  cat <<EOF
Usage: $0 <command>

Commands:
  up             Create/reuse the owned kind cluster, install Kueue, apply fixtures, verify
  down           Delete only the ownership-marked kind cluster
  reset          Safely recreate the owned cluster and all scenarios
  status         Show the controller, queues, Jobs, Workloads, and Pods
  verify         Assert controller-earned admitted, quota-blocked, and held states
  verify-radar   Assert a running Radar exposes the real Kueue states
  help           Show this message

Environment:
  CLUSTER_NAME   kind cluster name (default: radar-kueue-demo)
  KUEUE_VERSION  pinned Kueue release (default: v0.19.2)
  KUBERNETES_VERSION expected Kubernetes server version (default: v1.36.1)
  KIND_NODE_IMAGE immutable kind node image (default: kindest/node v1.36.1 digest)
  WAIT_SECONDS   maximum wait per reconciled state (default: 300)
  RADAR_URL      running Radar base URL for verify-radar (default: http://127.0.0.1:9280)

This validates Kueue controller reconciliation and Radar's read path. It does not
validate GPU hardware, device plugins, DRA allocation, utilization, or cost.
EOF
}

case "${1:-help}" in
  up) cmd_up ;;
  down) cmd_down ;;
  reset) cmd_reset ;;
  status) cmd_status ;;
  verify) cmd_verify ;;
  verify-radar) cmd_verify_radar ;;
  help|-h|--help) usage ;;
  *) fail "unknown subcommand: $1 (try '$0 help')" ;;
esac
