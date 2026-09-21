# Sourced by jobset-demo.sh so ownership, context restoration, and waits stay shared.
ADMISSION_NS="jobset-admission-demo"
KUEUE_VERSION="${KUEUE_VERSION:-v0.19.2}"

admission_webhook_ready() {
  kc apply --dry-run=server -f "${FIXTURES_DIR}/30-admission-queues.json" >/dev/null 2>&1
}

cmd_up_admission() {
  cmd_up
  local SNAPSHOT_NS="${ADMISSION_NS}" SNAPSHOT_KINDS="jobsets.jobset.x-k8s.io,jobs,pods,workloads.kueue.x-k8s.io"
  [[ "${KUEUE_VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "KUEUE_VERSION must be a pinned release"
  step "Installing Kueue ${KUEUE_VERSION} after JobSet integration is served"
  kc apply --server-side -f "https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml" >/dev/null
  kc wait --for=condition=Established crd/workloads.kueue.x-k8s.io --timeout=180s >/dev/null
  kc -n kueue-system rollout status deployment/kueue-controller-manager --timeout=300s >/dev/null
  # The webhook probe includes namespaced queues; create their namespace first.
  kc create namespace "${ADMISSION_NS}" --dry-run=client -o yaml | kc apply -f - >/dev/null
  wait_until "Kueue webhook" admission_webhook_ready
  local image
  image="$(kc -n kueue-system get deployment kueue-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}')"
  [ "${image}" = "registry.k8s.io/kueue/kueue:${KUEUE_VERSION}" ] || fail "Unexpected Kueue controller image: ${image}"
  kc apply -f "${FIXTURES_DIR}/30-admission-queues.json" >/dev/null
  local name
  for name in admitted-running quota-blocked queue-held finished; do
    if ! kc -n "${ADMISSION_NS}" get jobsets.jobset.x-k8s.io "${name}" >/dev/null 2>&1; then
      jq --arg name "${name}" '.items[] | select(.metadata.name == $name)' "${FIXTURES_DIR}/40-admission-jobsets.json" | kc create -f - >/dev/null
    fi
  done
  cmd_verify_admission
}

admission_workload() {
  local name="$1" uid
  uid="$(kc -n "${ADMISSION_NS}" get jobsets.jobset.x-k8s.io "${name}" -o jsonpath='{.metadata.uid}')" || return 1
  [ -n "${uid}" ] || return 1
  kc -n "${ADMISSION_NS}" get workloads.kueue.x-k8s.io -o json | jq -ce --arg name "${name}" --arg uid "${uid}" '
    [.items[] | select(any(.metadata.ownerReferences[]?;
      .apiVersion == "jobset.x-k8s.io/v1alpha2" and .kind == "JobSet" and
      .name == $name and .uid == $uid and .controller == true))] |
    if length == 1 then .[0] else empty end'
}

admission_condition_is() {
  local name="$1" type="$2" status="$3" reason="$4"
  admission_workload "${name}" | jq -e --arg type "${type}" --arg status "${status}" --arg reason "${reason}" '
    any(.status.conditions[]?; .type == $type and .status == $status and .reason == $reason)' >/dev/null
}

admission_children_match() {
  local name="$1" suspended="$2" expected_pods="$3" uid jobs pods
  uid="$(kc -n "${ADMISSION_NS}" get jobsets.jobset.x-k8s.io "${name}" -o jsonpath='{.metadata.uid}')" || return 1
  jobs="$(kc -n "${ADMISSION_NS}" get jobs -o json)" || return 1
  pods="$(kc -n "${ADMISSION_NS}" get pods -l "jobset.sigs.k8s.io/jobset-uid=${uid}" -o json)" || return 1
  jq -e --arg uid "${uid}" --argjson suspended "${suspended}" '[.items[] | select(any(.metadata.ownerReferences[]?; .uid == $uid and .controller == true))] | length == 1 and all(.[]; .spec.suspend == $suspended)' <<<"${jobs}" >/dev/null &&
    jq -e --argjson expected "${expected_pods}" '.items | length == $expected and all(.[]; .status.phase == "Running")' <<<"${pods}" >/dev/null
}

cmd_verify_admission() {
  local SNAPSHOT_NS="${ADMISSION_NS}" SNAPSHOT_KINDS="jobsets.jobset.x-k8s.io,jobs,pods,workloads.kueue.x-k8s.io"
  require_owned_cluster
  assert_cluster_contract
  step "Verifying real JobSet–Kueue admission and pre-Pod blockers"
  wait_until "admitted JobSet" admission_condition_is admitted-running Admitted True Admitted
  wait_until "quota-blocked JobSet" admission_condition_is quota-blocked QuotaReserved False Pending
  wait_until "queue-held JobSet" admission_condition_is queue-held QuotaReserved False Inadmissible
  wait_until "finished JobSet" admission_condition_is finished Finished True Succeeded
  wait_until "admitted Job and Running Pod" admission_children_match admitted-running false 1
  wait_until "quota-blocked suspended Job without Pods" admission_children_match quota-blocked true 0
  wait_until "queue-held suspended Job without Pods" admission_children_match queue-held true 0
  ok "All four Workloads have exact controller ownership and expected Kueue conditions"
}

radar_admission_matches() {
  local name="$1" decision="$2" phase="$3" type="$4" status="$5" reason="$6" workload response
  workload="$(admission_workload "${name}")" || return 1
  response="$(curl -fsS "${RADAR_URL}/api/kueue/admission/jobsets/${ADMISSION_NS}/${name}?group=jobset.x-k8s.io")" || return 1
  jq -e --argjson native "${workload}" --arg decision "${decision}" --arg phase "${phase}" --arg type "${type}" --arg status "${status}" --arg reason "${reason}" '
    .installed and .total == 1 and .truncated == false and (.workloads | length) == 1 and
    (.workloads[0] | .uid == $native.metadata.uid and .name == $native.metadata.name and .projection == "available" and
      (.scheduling.observations[0] | .source == "kueue" and .domain == "admission" and
       .subject.name == $native.metadata.name and .subject.namespace == $native.metadata.namespace and
       .subject.group == "kueue.x-k8s.io" and .subject.kind == "Workload" and
       .subjectGeneration == $native.metadata.generation and .decision == $decision and .kueue.phase == $phase and
       .primaryCondition.type == $type and .primaryCondition.status == $status and .primaryCondition.reason == $reason))' <<<"${response}" >/dev/null
}

cmd_verify_admission_radar() {
  cmd_verify_radar
  local SNAPSHOT_NS="${ADMISSION_NS}" SNAPSHOT_KINDS="jobsets.jobset.x-k8s.io,jobs,pods,workloads.kueue.x-k8s.io"
  cmd_verify_admission
  wait_until "Radar admitted admission" radar_admission_matches admitted-running satisfied admitted Admitted True Admitted
  wait_until "Radar quota blocker" radar_admission_matches quota-blocked unsatisfied pending QuotaReserved False Pending
  wait_until "Radar inactive queue" radar_admission_matches queue-held unsatisfied pending QuotaReserved False Inadmissible
  wait_until "Radar finished admission" radar_admission_matches finished satisfied finished Finished True Succeeded
  ok "Radar connects each JobSet to its exact controller-owned admission evidence"
}
