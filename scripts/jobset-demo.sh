#!/usr/bin/env bash

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-jobset-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"
JOBSET_VERSION="${JOBSET_VERSION:-v0.12.0}"
JOBSET_MANIFEST="https://github.com/kubernetes-sigs/jobset/releases/download/${JOBSET_VERSION}/manifests.yaml"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-v1.36.1}"
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="${SCRIPT_DIR}/jobset-demo"
DEMO_NS="jobset-demo"
MARKER_NAME="radar-jobset-demo-owner"
MARKER_VALUE="skyhook-radar-jobset-demo-v1"
WAIT_SECONDS="${WAIT_SECONDS:-240}"
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
  local clusters
  clusters="$(kind get clusters)" || fail "Unable to list kind clusters. Check that Docker is running and accessible."
  grep -Fx "${CLUSTER_NAME}" <<<"${clusters}" >/dev/null
}

cluster_owned() {
  local marker
  marker="$(kc -n kube-system get configmap "${MARKER_NAME}" --ignore-not-found --request-timeout=10s -o json)" || \
    fail "Unable to verify ownership: context '${KUBECTL_CTX}' is missing or unreachable in the active kubeconfig. If missing, restore it with: kind export kubeconfig --name '${CLUSTER_NAME}'"
  [ -n "${marker}" ] && jq -e --arg owner "${MARKER_VALUE}" --arg cluster "${CLUSTER_NAME}" \
    '.data.owner == $owner and .data.clusterName == $cluster' <<<"${marker}" >/dev/null
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
    --from-literal="jobSetVersion=${JOBSET_VERSION}" \
    --from-literal="kubernetesVersion=${KUBERNETES_VERSION}" \
    --from-literal="kindNodeImage=${KIND_NODE_IMAGE}" \
    --dry-run=client -o yaml | kc apply -f - >/dev/null
}

restore_previous_context() {
  [ "${RESTORE_CONTEXT}" = "true" ] || return 0
  if [ -z "${PREVIOUS_CONTEXT}" ]; then
    kubectl config unset current-context >/dev/null 2>&1 || warn "Unable to restore the unset current-context" >&2
  elif [ "$(kubectl config current-context 2>/dev/null || true)" != "${PREVIOUS_CONTEXT}" ]; then
    kubectl config set current-context "${PREVIOUS_CONTEXT}" >/dev/null 2>&1 || \
      warn "Unable to restore current-context '${PREVIOUS_CONTEXT}'" >&2
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
  local recorded_image recorded_jobset recorded_version server_version
  recorded_image="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.kindNodeImage}')"
  recorded_jobset="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.jobSetVersion}')"
  recorded_version="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.kubernetesVersion}')"
  server_version="$(kc version -o json | jq -r '.serverVersion.gitVersion')"
  [ "${recorded_image}" = "${KIND_NODE_IMAGE}" ] || \
    fail "Cluster uses recorded kind image '${recorded_image:-unknown}', expected '${KIND_NODE_IMAGE}'. Run '$0 reset' to change it."
  [ "${recorded_jobset}" = "${JOBSET_VERSION}" ] || \
    fail "Cluster records JobSet '${recorded_jobset:-unknown}', expected '${JOBSET_VERSION}'. Run '$0 reset' to change it."
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
  warn "Last controller snapshot for '${description}':" >&2
  kc -n "${SNAPSHOT_NS:-${DEMO_NS}}" get "${SNAPSHOT_KINDS:-jobsets.jobset.x-k8s.io,jobs,pods}" --request-timeout=10s -o json | \
    jq -c '.items[:10][] | {kind, name: .metadata.name, terminalState: .status.terminalState, roles: .status.replicatedJobsStatus, phase: .status.phase, conditions: .status.conditions}' >&2 || true
  fail "Timed out after ${WAIT_SECONDS}s waiting for ${description}. Inspect the lane with CLUSTER_NAME='${CLUSTER_NAME}' $0 status"
}

jobset_condition_is() {
  local name="$1" type="$2" status="$3" reason_pattern="${4:-}"
  kc -n "${DEMO_NS}" get jobsets.jobset.x-k8s.io "${name}" -o json 2>/dev/null | jq -e \
    --arg type "${type}" --arg status "${status}" --arg reason "${reason_pattern}" \
    'any(.status.conditions[]?; .type == $type and .status == $status and ($reason == "" or ((.reason // "") | test("^(" + $reason + ")$"))))' >/dev/null 2>&1
}

job_count() {
  kc -n "${DEMO_NS}" get jobs -l "jobset.sigs.k8s.io/jobset-name=$1" -o json 2>/dev/null | jq -r '.items | length'
}

role_job_count() {
  kc -n "${DEMO_NS}" get jobs \
    -l "jobset.sigs.k8s.io/jobset-name=$1,jobset.sigs.k8s.io/replicatedjob-name=$2" \
    -o json 2>/dev/null | jq -r '.items | length'
}

running_pod_count() {
  kc -n "${DEMO_NS}" get pods -l "jobset.sigs.k8s.io/jobset-name=$1" -o json 2>/dev/null | \
    jq -r '[.items[] | select(.status.phase == "Running")] | length'
}

failed_pod_count() {
  kc -n "${DEMO_NS}" get pods -l "jobset.sigs.k8s.io/jobset-name=$1" -o json 2>/dev/null | \
    jq -r '[.items[] | select(.status.phase == "Failed")] | length'
}

count_is() {
  local function="$1" expected="$2"
  shift 2
  [ "$("${function}" "$@")" = "${expected}" ]
}

assert_job_ownership_and_labels() {
  local jobset="$1" expected="$2" jobset_uid jobs
  jobset_uid="$(kc -n "${DEMO_NS}" get jobsets.jobset.x-k8s.io "${jobset}" -o jsonpath='{.metadata.uid}')"
  jobs="$(kc -n "${DEMO_NS}" get jobs -l "jobset.sigs.k8s.io/jobset-name=${jobset}" -o json)"
  jq -e --arg name "${jobset}" --arg uid "${jobset_uid}" --argjson expected "${expected}" '
    (.items | length) == $expected and
    all(.items[];
      any(.metadata.ownerReferences[]?;
        .apiVersion == "jobset.x-k8s.io/v1alpha2" and .kind == "JobSet" and
        .name == $name and .uid == $uid and .controller == true) and
      .metadata.labels["jobset.sigs.k8s.io/jobset-name"] == $name and
      .metadata.labels["jobset.sigs.k8s.io/jobset-uid"] == $uid and
      (.metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"] | length > 0) and
      (.metadata.labels["jobset.sigs.k8s.io/job-index"] | test("^[0-9]+$")) and
      (.metadata.labels["jobset.sigs.k8s.io/job-global-index"] | test("^[0-9]+$"))) and
    ([.items[].metadata.labels["jobset.sigs.k8s.io/job-global-index"]] | unique | length) == $expected
  ' <<<"${jobs}" >/dev/null || fail "${jobset} Jobs do not have the expected owner UIDs and role/index labels"
}

assert_pod_ownership_and_labels() {
  local jobset="$1" expected="$2" pods jobs
  pods="$(kc -n "${DEMO_NS}" get pods -l "jobset.sigs.k8s.io/jobset-name=${jobset}" -o json)"
  jobs="$(kc -n "${DEMO_NS}" get jobs -l "jobset.sigs.k8s.io/jobset-name=${jobset}" -o json)"
  jq -n -e --argjson pods "${pods}" --argjson jobs "${jobs}" --arg name "${jobset}" --argjson expected "${expected}" '
    ($pods.items | length) == $expected and
    all($pods.items[]; . as $pod |
      any($jobs.items[]; . as $job |
        any($pod.metadata.ownerReferences[]?;
          .apiVersion == "batch/v1" and .kind == "Job" and
          .name == $job.metadata.name and .uid == $job.metadata.uid and .controller == true) and
        $pod.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == $name and
        $pod.metadata.labels["jobset.sigs.k8s.io/jobset-uid"] == $job.metadata.labels["jobset.sigs.k8s.io/jobset-uid"] and
        $pod.metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"] == $job.metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"] and
        $pod.metadata.labels["jobset.sigs.k8s.io/job-index"] == $job.metadata.labels["jobset.sigs.k8s.io/job-index"] and
        $pod.metadata.labels["jobset.sigs.k8s.io/job-global-index"] == $job.metadata.labels["jobset.sigs.k8s.io/job-global-index"]))
  ' >/dev/null || fail "${jobset} Pods do not have the expected Job owners and propagated role/index labels"
}

assert_group_lineage() {
  local name="$1" jobs pods
  jobs="$(kc -n "${DEMO_NS}" get jobs -l "jobset.sigs.k8s.io/jobset-name=${name}" -o json)"
  pods="$(kc -n "${DEMO_NS}" get pods -l "jobset.sigs.k8s.io/jobset-name=${name}" -o json)"
  jq -e '
    (.items | length) == 3 and
    all(.items[];
      .metadata.labels["jobset.sigs.k8s.io/group-name"] == "training" and
      .metadata.labels["jobset.sigs.k8s.io/group-replicas"] == "3" and
      (.metadata.labels["jobset.sigs.k8s.io/job-group-index"] | test("^[0-9]+$"))) and
    ([.items[].metadata.labels["jobset.sigs.k8s.io/job-group-index"]] | sort) == ["0", "1", "2"]
  ' <<<"${jobs}" >/dev/null || fail "${name} Jobs do not expose the expected training group lineage"
  jq -n -e --argjson pods "${pods}" --argjson jobs "${jobs}" '
    all($pods.items[]; . as $pod |
      any($jobs.items[]; . as $job |
        any($pod.metadata.ownerReferences[]?; .uid == $job.metadata.uid and .controller == true) and
        $pod.metadata.labels["jobset.sigs.k8s.io/group-name"] == $job.metadata.labels["jobset.sigs.k8s.io/group-name"] and
        $pod.metadata.labels["jobset.sigs.k8s.io/group-replicas"] == $job.metadata.labels["jobset.sigs.k8s.io/group-replicas"] and
        $pod.metadata.labels["jobset.sigs.k8s.io/job-group-index"] == $job.metadata.labels["jobset.sigs.k8s.io/job-group-index"]))
  ' >/dev/null || fail "${name} Pods do not preserve their parent Jobs' group lineage"
}

roles_status_ready() {
  kc -n "${DEMO_NS}" get jobsets.jobset.x-k8s.io roles-running -o json | jq -e '
    ([.spec.replicatedJobs[].groupName] | unique) == ["training"] and
    any(.status.replicatedJobsStatus[]?; .name == "leader" and .ready == 1 and .active == 1) and
    any(.status.replicatedJobsStatus[]?; .name == "workers" and .ready == 2 and .active == 2) and
    (.status.terminalState // "") == ""
  ' >/dev/null
}

verify_roles_running() {
  local name="roles-running"
  wait_until "${name} Jobs" count_is job_count 3 "${name}"
  wait_until "${name} Pods to be Running" count_is running_pod_count 3 "${name}"
  [ "$(role_job_count "${name}" leader)" = "1" ] || fail "${name} did not create one leader Job"
  [ "$(role_job_count "${name}" workers)" = "2" ] || fail "${name} did not create two worker Jobs"
  assert_job_ownership_and_labels "${name}" 3
  kc -n "${DEMO_NS}" get jobs -l "jobset.sigs.k8s.io/jobset-name=${name}" -o json | jq -e '
    ([.items[] | select(.metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"] == "leader") |
      .metadata.labels["jobset.sigs.k8s.io/job-index"]] | sort) == ["0"] and
    ([.items[] | select(.metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"] == "workers") |
      .metadata.labels["jobset.sigs.k8s.io/job-index"]] | sort) == ["0", "1"]
  ' >/dev/null || fail "${name} Jobs do not expose the expected per-role indexes"
  assert_pod_ownership_and_labels "${name}" 3
  assert_group_lineage "${name}"
  wait_until "${name} ready/active role status" roles_status_ready
  ok "${name}: one leader and two workers are Running with controller-owned role/group/index lineage"
}

dependency_status_ready() {
  kc -n "${DEMO_NS}" get jobsets.jobset.x-k8s.io dependency-held -o json | jq -e '
    any(.status.replicatedJobsStatus[]?; .name == "initializer" and .ready == 1 and .active == 1) and
    any(.status.replicatedJobsStatus[]?; .name == "workers" and .ready == 0 and .active == 0)
  ' >/dev/null
}

verify_dependency_held() {
  local name="dependency-held"
  wait_until "${name} initializer Job" count_is role_job_count 1 "${name}" initializer
  wait_until "${name} initializer Pod to be Running" count_is running_pod_count 1 "${name}"
  wait_until "${name} ready/active role status" dependency_status_ready
  [ "$(role_job_count "${name}" workers)" = "0" ] || fail "${name} created worker Jobs before initializer completion"
  assert_job_ownership_and_labels "${name}" 1
  assert_pod_ownership_and_labels "${name}" 1
  kc -n "${DEMO_NS}" get jobsets.jobset.x-k8s.io "${name}" -o json | jq -e '
    any(.spec.replicatedJobs[]?;
      .name == "workers" and any(.dependsOn[]?; .name == "initializer" and .status == "Complete")) and
    (.status.terminalState // "") == "" and
    all(.status.conditions[]?; .type != "Completed" and .type != "Failed")
  ' >/dev/null || fail "${name} no longer exposes the expected durable dependency-held state"
  ok "${name}: initializer is Running while its Complete dependency prevents worker Job creation"
}

verify_terminal_failure() {
  local name="terminal-failure"
  wait_until "${name} terminal condition" jobset_condition_is "${name}" Failed True FailJobSetFailurePolicyAction
  wait_until "${name} failed Pod" count_is failed_pod_count 1 "${name}"
  assert_job_ownership_and_labels "${name}" 1
  assert_pod_ownership_and_labels "${name}" 1
  kc -n "${DEMO_NS}" get jobsets.jobset.x-k8s.io "${name}" -o json | jq -e '
    .status.terminalState == "Failed" and
    any(.status.replicatedJobsStatus[]?; .name == "workers" and .failed == 1) and
    any(.spec.failurePolicy.rules[]?;
      .name == "failWorker" and .action == "FailJobSet" and
      (.targetReplicatedJobs == ["workers"]) and
      (.onJobFailureReasons == ["BackoffLimitExceeded"]))
  ' >/dev/null || fail "${name} status/spec does not expose the expected explicit terminal failure"
  kc -n "${DEMO_NS}" get jobs -l "jobset.sigs.k8s.io/jobset-name=${name}" -o json | jq -e '
    any(.items[]; any(.status.conditions[]?; .type == "Failed" and .status == "True" and .reason == "BackoffLimitExceeded"))
  ' >/dev/null || fail "${name} child Job did not fail with BackoffLimitExceeded"
  ok "${name}: the real failure policy produced Failed=True/FailJobSetFailurePolicyAction"
}

jobset_webhook_ready() {
  kc apply --dry-run=server -f - >/dev/null 2>&1 <<'EOF'
apiVersion: jobset.x-k8s.io/v1alpha2
kind: JobSet
metadata:
  name: radar-jobset-webhook-probe
  namespace: default
spec:
  replicatedJobs:
  - name: worker
    replicas: 1
    template:
      spec:
        template:
          spec:
            restartPolicy: Never
            containers:
            - name: worker
              image: registry.k8s.io/pause:3.10
EOF
}

install_jobset() {
  step "Installing JobSet ${JOBSET_VERSION}"
  kc apply --server-side -f "${JOBSET_MANIFEST}" >/dev/null
  kc wait --for=condition=Established crd/jobsets.jobset.x-k8s.io --timeout=180s >/dev/null
  kc -n jobset-system rollout status deployment/jobset-controller-manager --timeout=240s >/dev/null

  local image
  image="$(kc -n jobset-system get deployment jobset-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}')"
  [ "${image}" = "registry.k8s.io/jobset/jobset:${JOBSET_VERSION}" ] || \
    fail "JobSet controller image is '${image}', expected registry.k8s.io/jobset/jobset:${JOBSET_VERSION}"
  wait_until "JobSet admission webhook" jobset_webhook_ready
  ok "JobSet controller and admission webhook are Ready with the pinned image"
}

apply_fixtures() {
  step "Applying JobSet controller fixtures"
  kc apply -f "${FIXTURES_DIR}/00-namespace.yaml" >/dev/null

  local present
  present="$(kc -n "${DEMO_NS}" get jobsets.jobset.x-k8s.io roles-running dependency-held terminal-failure \
    --ignore-not-found -o name 2>/dev/null | wc -l | tr -d ' ')"
  if [ "${present}" = "3" ]; then
    note "preserving the three reconciled JobSets (use reset after changing fixtures)"
  else
    [ "${present}" = "0" ] || warn "Only ${present}/3 JobSets exist; applying the complete fixture"
    kc apply -f "${FIXTURES_DIR}/10-jobsets.yaml" >/dev/null
  fi
  ok "Fixtures applied without patching status"
}

cmd_verify() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  require_owned_cluster
  assert_cluster_contract
  step "Verifying controller-earned JobSet states"
  verify_roles_running
  verify_dependency_held
  verify_terminal_failure
  ok "JobSet running roles, dependency hold, terminal failure, and ownership lineage verified"
}

mcp_get_jobset() {
  local jobset="$1" request response event
  request="$(jq -cn \
    --arg namespace "${DEMO_NS}" \
    --arg jobset "${jobset}" \
    '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:"get_resource",arguments:{kind:"jobsets",group:"jobset.x-k8s.io",namespace:$namespace,name:$jobset}}}')"
  response="$(curl -fsS --connect-timeout 5 --max-time 15 \
    -X POST \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d "${request}" \
    "${RADAR_URL}/mcp")" || return 1
  event="$(sed -n 's/^data: //p' <<<"${response}" | tail -n 1)"
  [ -n "${event}" ] || { warn "MCP response contained no SSE data event" >&2; return 1; }
  if jq -e '.error != null or .result.isError == true' <<<"${event}" >/dev/null; then
    jq -c '{error, result}' <<<"${event}" >&2
    return 1
  fi
  jq -er '.result.content[0].text | fromjson' <<<"${event}"
}

radar_execution_matches() {
  local name="$1" phase="$2" roles="$3" jobs="$4" ready="$5" active="$6" failed="$7"
  local expected generation rest_response mcp_response rest_execution mcp_execution
  generation="$(kc -n "${DEMO_NS}" get jobset "${name}" -o jsonpath='{.metadata.generation}')" || \
    { warn "Unable to read JobSet '${name}' generation" >&2; return 1; }
  expected="$(jq -cnS --arg phase "${phase}" --argjson generation "${generation}" \
    --argjson roles "${roles}" --argjson jobs "${jobs}" --argjson ready "${ready}" \
    --argjson active "${active}" --argjson failed "${failed}" '
    {controller:"jobset", subjectGeneration:$generation, phase:$phase, suspendRequested:false,
     jobset:{declaredRoles:$roles, declaredJobs:$jobs, observedRoles:$roles,
             jobs:{ready:$ready, active:$active, succeeded:0, failed:$failed, suspended:0},
             restarts:{global:0, globalCountTowardsMax:0, individual:0, individualCountTowardsMax:0}}}
    + if $phase == "finished" then
        {outcome:"failed", nativeState:"Failed",
         primaryCondition:{type:"Failed", status:"True", reason:"FailJobSetFailurePolicyAction"}}
      else {} end')"
  rest_response="$(curl -fsS --connect-timeout 5 --max-time 15 \
    "${RADAR_URL}/api/ai/resources/jobsets/${DEMO_NS}/${name}?group=jobset.x-k8s.io")" || \
    { warn "REST AI resource request failed for JobSet '${name}'" >&2; return 1; }
  mcp_response="$(mcp_get_jobset "${name}")" || { warn "MCP get_resource failed for JobSet '${name}'" >&2; return 1; }

  local response
  for response in "${rest_response}" "${mcp_response}"; do
    jq -e --arg name "${name}" --arg namespace "${DEMO_NS}" '
      .resource.apiVersion == "jobset.x-k8s.io/v1alpha2" and .resource.kind == "JobSet" and
      .resource.metadata.name == $name and .resource.metadata.namespace == $namespace and
      .resourceContext.tier == "basic"
    ' <<<"${response}" >/dev/null || { warn "REST/MCP returned the wrong JobSet identity or context tier for '${name}'" >&2; return 1; }
  done
  rest_execution="$(jq -ceS '.resourceContext.execution' <<<"${rest_response}")" || \
    { warn "REST execution context is missing for JobSet '${name}'" >&2; return 1; }
  mcp_execution="$(jq -ceS '.resourceContext.execution' <<<"${mcp_response}")" || \
    { warn "MCP execution context is missing for JobSet '${name}'" >&2; return 1; }
  if [ "${rest_execution}" != "${expected}" ] || [ "${mcp_execution}" != "${expected}" ]; then
    printf 'Expected: %s\nREST: %s\nMCP: %s\n' "${expected}" "${rest_execution}" "${mcp_execution}" >&2
    { warn "REST/MCP execution context differs from '${name}' controller-earned state" >&2; return 1; }
  fi
  ok "${name}: REST and MCP agree on ${phase}, ${roles} roles, ${active} active and ${failed} failed Jobs"
}

radar_health_ready() {
  curl -fsS --connect-timeout 2 --max-time 5 "${RADAR_URL}/api/health" 2>/dev/null | \
    jq -e '.status == "healthy"' >/dev/null 2>&1
}

radar_crd_discovery_ready() {
  curl -fsS --connect-timeout 2 --max-time 5 "${RADAR_URL}/api/cluster-info" 2>/dev/null | \
    jq -e '.crdDiscoveryStatus == "ready"' >/dev/null 2>&1
}

radar_inventory_ready() {
  local kind="$1" group="$2" live cached projection
  projection='map({metadata: (.metadata | {namespace, name, uid, ownerReferences, labels}), status: (.status | {phase, terminalState, replicatedJobsStatus, conditions: [.conditions[]? | {type, status, reason}]})}) | sort_by(.metadata.name)'
  live="$(kc -n "${DEMO_NS}" get "${kind}${group:+.${group}}" --request-timeout=10s -o json | jq -cS ".items | ${projection}")" || return 1
  cached="$(curl -fsS --connect-timeout 2 --max-time 5 "${RADAR_URL}/api/resources/${kind}?group=${group}&namespace=${DEMO_NS}" | jq -cS "${projection}")" || return 1
  [ "${live}" = "${cached}" ]
}

cmd_verify_radar() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd curl "https://curl.se/download.html"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  require_owned_cluster
  assert_cluster_contract
  step "Verifying Radar API at ${RADAR_URL}"

  local response jobs pods
  wait_until "Radar health endpoint" radar_health_ready
  response="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/cluster-info")" || fail "Radar cluster-info request failed"
  jq -e --arg context "${KUBECTL_CTX}" '.context == $context and .cluster == $context' <<<"${response}" >/dev/null || \
    fail "Radar is not connected to the expected context and cluster '${KUBECTL_CTX}'"
  wait_until "Radar CRD discovery" radar_crd_discovery_ready
  wait_until "Radar JobSet identities, ownership and status" radar_inventory_ready jobsets jobset.x-k8s.io
  wait_until "Radar Job identities, ownership and status" radar_inventory_ready jobs batch
  wait_until "Radar Pod identities, ownership and status" radar_inventory_ready pods ""

  response="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/resources/jobsets?group=jobset.x-k8s.io&namespace=${DEMO_NS}")" || fail "Radar jobsets list request failed"
  jq -e '
    type == "array" and length == 3 and
    all(.[]; .apiVersion == "jobset.x-k8s.io/v1alpha2") and
    any(.[]; .metadata.name == "roles-running" and (.status.terminalState // "") == "") and
    any(.[]; .metadata.name == "dependency-held" and (.status.terminalState // "") == "") and
    any(.[]; .metadata.name == "terminal-failure" and .status.terminalState == "Failed" and
      any(.status.conditions[]?; .type == "Failed" and .status == "True" and .reason == "FailJobSetFailurePolicyAction"))
  ' <<<"${response}" >/dev/null || fail "Radar did not return the three group-pure reconciled JobSets"

  jobs="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/resources/jobs?namespace=${DEMO_NS}")" || fail "Radar jobs list request failed"
  jq -e '
    type == "array" and
    ([.[] | select(.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == "roles-running")] | length) == 3 and
    all(.[] | select(.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == "roles-running");
      .metadata.labels["jobset.sigs.k8s.io/group-name"] == "training" and
      .metadata.labels["jobset.sigs.k8s.io/group-replicas"] == "3") and
    ([.[] | select(.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == "roles-running") |
      .metadata.labels["jobset.sigs.k8s.io/job-group-index"]] | sort) == ["0", "1", "2"] and
    ([.[] | select(.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == "dependency-held")] | length) == 1 and
    ([.[] | select(.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == "terminal-failure")] | length) == 1
  ' <<<"${jobs}" >/dev/null || fail "Radar did not expose the expected controller-created child Jobs"

  pods="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/resources/pods?namespace=${DEMO_NS}")" || fail "Radar pods list request failed"
  jq -e '
    type == "array" and
    ([.[] | select(.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == "roles-running" and .status.phase == "Running")] | length) == 3 and
    all(.[] | select(.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == "roles-running");
      .metadata.labels["jobset.sigs.k8s.io/group-name"] == "training" and
      .metadata.labels["jobset.sigs.k8s.io/group-replicas"] == "3") and
    ([.[] | select(.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == "roles-running") |
      .metadata.labels["jobset.sigs.k8s.io/job-group-index"]] | sort) == ["0", "1", "2"] and
    ([.[] | select(.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == "dependency-held" and .status.phase == "Running")] | length) == 1 and
    ([.[] | select(.metadata.labels["jobset.sigs.k8s.io/jobset-name"] == "terminal-failure" and .status.phase == "Failed")] | length) == 1
  ' <<<"${pods}" >/dev/null || fail "Radar did not expose the expected controller-created Pods"
  wait_until "Radar execution projection" radar_execution_matches roles-running active 2 3 3 3 0
  wait_until "Radar execution projection" radar_execution_matches dependency-held active 2 3 1 1 0
  wait_until "Radar execution projection" radar_execution_matches terminal-failure finished 1 1 0 0 1
  ok "Radar returned real JobSet lineage and exact matching REST/MCP execution context"
}

cmd_up() {
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  [[ "${JOBSET_VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "JOBSET_VERSION must be a pinned vX.Y.Z release"
  [[ "${KUBERNETES_VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "KUBERNETES_VERSION must be a pinned vX.Y.Z release"
  prepare_context_restore

  if cluster_exists; then
    cluster_owned || fail "Refusing to modify existing unowned cluster '${CLUSTER_NAME}'. Choose another CLUSTER_NAME or remove it yourself."
    step "Reusing owned kind cluster '${CLUSTER_NAME}'"
    assert_cluster_contract
  else
    step "Creating dedicated kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --image "${KIND_NODE_IMAGE}" --wait 60s
    mark_cluster || fail "Cluster created but ownership could not be recorded. Inspect it before manual cleanup: kind delete cluster --name ${CLUSTER_NAME}"
    assert_cluster_contract
    ok "Cluster created and ownership marker recorded"
  fi

  require_owned_cluster
  mark_cluster
  install_jobset
  apply_fixtures
  cmd_verify
  note "See ${FIXTURES_DIR}/README.md for the isolated-kubeconfig Radar smoke test."
}

cmd_down() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd jq "https://jqlang.github.io/jq/download/"
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
  step "JobSet controller"
  kc -n jobset-system get deployment jobset-controller-manager -o wide
  step "JobSets"
  kc -n "${DEMO_NS}" get jobsets.jobset.x-k8s.io
  step "Jobs by role and index"
  kc -n "${DEMO_NS}" get jobs \
    -L jobset.sigs.k8s.io/jobset-name,jobset.sigs.k8s.io/replicatedjob-name,jobset.sigs.k8s.io/job-index,jobset.sigs.k8s.io/job-global-index
  step "Pods by role and index"
  kc -n "${DEMO_NS}" get pods \
    -L jobset.sigs.k8s.io/jobset-name,jobset.sigs.k8s.io/replicatedjob-name,jobset.sigs.k8s.io/job-index,jobset.sigs.k8s.io/job-global-index
}

usage() {
  cat <<EOF
Usage: $0 <command>

Commands:
  up             Create/reuse the owned kind cluster, install JobSet, apply fixtures, verify
  down           Delete only the ownership-marked kind cluster
  reset          Safely recreate the owned cluster and all scenarios
  status         Show the controller, JobSets, Jobs, and Pods with role/index labels
  verify         Assert controller-earned lifecycle and ownership states
  verify-radar   Assert a running Radar exposes the real JobSet states and children
  up-admission   Add Kueue and four JobSet admission scenarios to this same cluster
  verify-admission        Verify the combined controllers and pre-Pod blockers
  verify-admission-radar  Verify both lanes plus Radar's connected admission lookup
  help           Show this message

Environment:
  CLUSTER_NAME      kind cluster name (default: radar-jobset-demo)
  JOBSET_VERSION    pinned JobSet release (default: v0.12.0)
  KUBERNETES_VERSION expected Kubernetes server version (default: v1.36.1)
  KIND_NODE_IMAGE   immutable kind node image (default: kindest/node v1.36.1 digest)
  WAIT_SECONDS      maximum wait per reconciled state (default: 240)
  RADAR_URL         running Radar base URL for verify-radar (default: http://127.0.0.1:9280)

This validates JobSet controller reconciliation and Radar's read path. It does
not validate GPUs, distributed framework semantics, or cost. The optional
up-admission mode also validates Kueue admission (KUEUE_VERSION=v0.19.2).
EOF
}

source "${FIXTURES_DIR}/admission.sh"

case "${1:-help}" in
  up) cmd_up ;;
  down) cmd_down ;;
  reset) cmd_reset ;;
  status) cmd_status ;;
  verify) cmd_verify ;;
  verify-radar) cmd_verify_radar ;;
  up-admission) cmd_up_admission ;;
  verify-admission) cmd_verify_admission ;;
  verify-admission-radar) cmd_verify_admission_radar ;;
  help|-h|--help) usage ;;
  *) fail "unknown subcommand: $1 (try '$0 help')" ;;
esac
