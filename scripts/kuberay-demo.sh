#!/usr/bin/env bash

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-kuberay-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"
KUBERAY_VERSION="v1.7.0"
KUBERAY_SOURCE_REF="59d663aeec13760646da3db9c1cf8cfe883e30b7"
KUBERAY_MANIFEST="github.com/ray-project/kuberay/ray-operator/config/default?ref=${KUBERAY_SOURCE_REF}&timeout=90s"
KUBERAY_OPERATOR_IMAGE="quay.io/kuberay/operator:v1.7.0@sha256:4a779237ef1c5262a63840ccf42d0d67f0b74e911158fbcaee4478fb1560bce6"
RAY_VERSION="2.55.0"
RAY_IMAGE="rayproject/ray:2.55.0@sha256:71bbcb7cf031c290d9f23a8fd54d8602c8f0f2004b73d616f97dddc1975e9bd4"
PENDING_IMAGE="registry.k8s.io/pause:3.10@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a"
KUBERNETES_VERSION="v1.36.1"
KIND_NODE_IMAGE="kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="${SCRIPT_DIR}/kuberay-demo"
DEMO_NS="kuberay-demo"
RAYSERVICE_NAME="revision-demo"
MARKER_NAME="radar-kuberay-demo-owner"
MARKER_VALUE="skyhook-radar-kuberay-demo-v1"
WAIT_SECONDS="${WAIT_SECONDS:-480}"
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
    --from-literal="kubeRayVersion=${KUBERAY_VERSION}" \
    --from-literal="kubeRaySourceRef=${KUBERAY_SOURCE_REF}" \
    --from-literal="kubeRayOperatorImage=${KUBERAY_OPERATOR_IMAGE}" \
    --from-literal="rayVersion=${RAY_VERSION}" \
    --from-literal="rayImage=${RAY_IMAGE}" \
    --from-literal="pendingImage=${PENDING_IMAGE}" \
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
  local recorded_image recorded_kuberay recorded_source recorded_operator recorded_ray recorded_ray_image
  local recorded_pending recorded_version server_version
  recorded_image="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.kindNodeImage}')"
  recorded_kuberay="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.kubeRayVersion}')"
  recorded_source="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.kubeRaySourceRef}')"
  recorded_operator="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.kubeRayOperatorImage}')"
  recorded_ray="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.rayVersion}')"
  recorded_ray_image="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.rayImage}')"
  recorded_pending="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.pendingImage}')"
  recorded_version="$(kc -n kube-system get configmap "${MARKER_NAME}" -o jsonpath='{.data.kubernetesVersion}')"
  server_version="$(kc version -o json | jq -r '.serverVersion.gitVersion')"
  [ "${recorded_image}" = "${KIND_NODE_IMAGE}" ] || fail "Cluster kind image differs from the pinned fixture. Run '$0 reset'."
  [ "${recorded_kuberay}" = "${KUBERAY_VERSION}" ] || fail "Cluster KubeRay version differs from ${KUBERAY_VERSION}. Run '$0 reset'."
  [ "${recorded_source}" = "${KUBERAY_SOURCE_REF}" ] || fail "Cluster KubeRay source differs from the pinned commit. Run '$0 reset'."
  [ "${recorded_operator}" = "${KUBERAY_OPERATOR_IMAGE}" ] || fail "Cluster operator image differs from the pinned digest. Run '$0 reset'."
  [ "${recorded_ray}" = "${RAY_VERSION}" ] || fail "Cluster Ray version differs from ${RAY_VERSION}. Run '$0 reset'."
  [ "${recorded_ray_image}" = "${RAY_IMAGE}" ] || fail "Cluster Ray image differs from the pinned digest. Run '$0 reset'."
  [ "${recorded_pending}" = "${PENDING_IMAGE}" ] || fail "Cluster pending-revision image differs from ${PENDING_IMAGE}. Run '$0 reset'."
  [ "${recorded_version}" = "${KUBERNETES_VERSION}" ] || fail "Cluster Kubernetes version differs from ${KUBERNETES_VERSION}. Run '$0 reset'."
  [ "${server_version}" = "${KUBERNETES_VERSION}" ] || fail "Cluster reports Kubernetes '${server_version}', expected '${KUBERNETES_VERSION}'."
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

operator_contract_matches() {
  local image crd
  for crd in rayclusters.ray.io rayjobs.ray.io rayservices.ray.io; do
    kc get crd "${crd}" >/dev/null 2>&1 || return 1
  done
  image="$(kc -n default get deployment kuberay-operator -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)"
  [ "${image}" = "${KUBERAY_OPERATOR_IMAGE}" ]
}

assert_operator_contract() {
  local image crd
  kc -n default rollout status deployment/kuberay-operator --timeout="${WAIT_SECONDS}s" >/dev/null || \
    fail "KubeRay operator did not become Available within ${WAIT_SECONDS}s."
  image="$(kc -n default get deployment kuberay-operator -o jsonpath='{.spec.template.spec.containers[0].image}')"
  [ "${image}" = "${KUBERAY_OPERATOR_IMAGE}" ] || fail "KubeRay operator image is '${image}', expected the pinned digest."
  kc -n default get deployment kuberay-operator -o json | jq -e '
    .status.observedGeneration == .metadata.generation and
    any(.status.conditions[]?; .type == "Available" and .status == "True" and .reason == "MinimumReplicasAvailable")
  ' >/dev/null || fail "KubeRay operator is not Available."
  for crd in rayclusters.ray.io rayjobs.ray.io rayservices.ray.io; do
    kc get crd "${crd}" -o json | jq -e '
      any(.spec.versions[]?; .name == "v1" and .served == true and .storage == true)
    ' >/dev/null || fail "${crd} does not serve v1 as its storage version"
  done
}

install_kuberay() {
  if operator_contract_matches; then
    step "Reusing KubeRay ${KUBERAY_VERSION}"
    assert_operator_contract
    ok "KubeRay operator and v1 APIs match the immutable contract"
    return
  fi

  step "Installing KubeRay ${KUBERAY_VERSION} from pinned commit ${KUBERAY_SOURCE_REF:0:12}"
  kc apply --server-side -k "${KUBERAY_MANIFEST}" >/dev/null
  kc wait --for=condition=Established crd/rayclusters.ray.io --timeout=180s >/dev/null
  kc wait --for=condition=Established crd/rayjobs.ray.io --timeout=180s >/dev/null
  kc wait --for=condition=Established crd/rayservices.ray.io --timeout=180s >/dev/null
  kc -n default set image deployment/kuberay-operator "kuberay-operator=${KUBERAY_OPERATOR_IMAGE}" >/dev/null
  assert_operator_contract
  ok "KubeRay operator is Ready with the pinned image digest"
}

rayservice_json() {
  kc -n "${DEMO_NS}" get rayservices.ray.io "${RAYSERVICE_NAME}" -o json 2>/dev/null
}

active_cluster_name() {
  rayservice_json | jq -r '.status.activeServiceStatus.rayClusterName // ""'
}

pending_cluster_name() {
  rayservice_json | jq -r '.status.pendingServiceStatus.rayClusterName // ""'
}

healthy_baseline_ready() {
  rayservice_json | jq -e --arg image "${RAY_IMAGE}" '
    .spec.rayClusterConfig.headGroupSpec.template.spec.containers[0].image == $image and
    .status.observedGeneration == .metadata.generation and
    (.status.activeServiceStatus.rayClusterName // "" | length) > 0 and
    (.status.pendingServiceStatus.rayClusterName // "") == "" and
    .status.numServeEndpoints > 0 and
    .status.activeServiceStatus.applicationStatuses["radar-demo"].status == "RUNNING" and
    .status.activeServiceStatus.applicationStatuses["radar-demo"].serveDeploymentStatuses.RadarDemo.status == "HEALTHY" and
    any(.status.conditions[]?; .type == "Ready" and .status == "True" and .reason == "NonZeroServeEndpoints") and
    any(.status.conditions[]?; .type == "UpgradeInProgress" and .status == "False" and .reason == "NoPendingCluster")
  ' >/dev/null 2>&1
}

revision_pair_ready() {
  rayservice_json | jq -e --arg image "${PENDING_IMAGE}" '
    .spec.rayClusterConfig.headGroupSpec.template.spec.containers[0].image == $image and
    .status.observedGeneration == .metadata.generation and
    (.status.activeServiceStatus.rayClusterName // "" | length) > 0 and
    (.status.pendingServiceStatus.rayClusterName // "" | length) > 0 and
    .status.activeServiceStatus.rayClusterName != .status.pendingServiceStatus.rayClusterName and
    .status.numServeEndpoints > 0 and
    any(.status.conditions[]?; .type == "Ready" and .status == "True" and .reason == "NonZeroServeEndpoints") and
    any(.status.conditions[]?; .type == "UpgradeInProgress" and .status == "True" and .reason == "BothActivePendingClustersExist")
  ' >/dev/null 2>&1
}

pending_failure_ready() {
  local pending
  pending="$(pending_cluster_name)"
  [ -n "${pending}" ] || return 1
  kc -n "${DEMO_NS}" get rayclusters.ray.io "${pending}" -o json 2>/dev/null | jq -e '
    any(.status.conditions[]?;
      .type == "HeadPodReady" and .status == "False" and
      (.reason == "CrashLoopBackOff" or .reason == "RunContainerError")) and
    any(.status.conditions[]?; .type == "RayClusterProvisioned" and .status == "False" and .reason == "RayClusterPodsProvisioning")
  ' >/dev/null 2>&1
}

assert_rayservice_spec() {
  rayservice_json | jq -e --arg rayVersion "${RAY_VERSION}" --arg rayImage "${RAY_IMAGE}" --arg pendingImage "${PENDING_IMAGE}" '
    .apiVersion == "ray.io/v1" and
    .spec.upgradeStrategy.type == "NewCluster" and
    .spec.rayClusterConfig.rayVersion == $rayVersion and
    .spec.rayClusterConfig.headGroupSpec.template.spec.containers[0].name == "ray-head" and
    (.spec.rayClusterConfig.headGroupSpec.template.spec.containers[0].image as $image |
      ([ $rayImage, $pendingImage ] | index($image)) != null) and
    (.spec.rayClusterConfig.workerGroupSpecs // [] | length) == 0 and
    (.spec.serveConfigV2 | contains("import_path: radar_serve:app"))
  ' >/dev/null || fail "${RAYSERVICE_NAME} does not match the pinned head-only NewCluster fixture"
}

serve_service_name() {
  local service_uid
  service_uid="$(rayservice_json | jq -r '.metadata.uid')"
  kc -n "${DEMO_NS}" get services -o json | jq -r --arg uid "${service_uid}" '
    [.items[] | select(
      .metadata.labels["ray.io/serve"] == "revision-demo-serve" and
      any(.metadata.ownerReferences[]?; .uid == $uid and .kind == "RayService" and .controller == true)
    ) | .metadata.name] | if length == 1 then .[0] else "" end
  '
}

verify_serve_response() {
  local active active_pod service_name response
  active="$(active_cluster_name)"
  [ -n "${active}" ] || fail "RayService has no active RayCluster"
  active_pod="$(kc -n "${DEMO_NS}" get rayclusters.ray.io "${active}" -o jsonpath='{.status.head.podName}')"
  service_name="$(serve_service_name)"
  [ -n "${active_pod}" ] || fail "Active RayCluster does not report its head Pod"
  [ -n "${service_name}" ] || fail "RayService-owned Serve Service is missing"
  response="$(kc --request-timeout=20s -n "${DEMO_NS}" exec "${active_pod}" -- python -c 'import sys, urllib.request; print(urllib.request.urlopen("http://" + sys.argv[1] + ":8000/", timeout=5).read().decode())' "${service_name}")"
  [ "${response}" = "radar-kuberay-ready" ] || fail "Serve endpoint returned '${response}', expected radar-kuberay-ready"
}

assert_revision_ownership_and_status() {
  local service clusters service_uid active pending
  service="$(rayservice_json)"
  clusters="$(kc -n "${DEMO_NS}" get rayclusters.ray.io -o json)"
  service_uid="$(jq -r '.metadata.uid' <<<"${service}")"
  active="$(jq -r '.status.activeServiceStatus.rayClusterName' <<<"${service}")"
  pending="$(jq -r '.status.pendingServiceStatus.rayClusterName' <<<"${service}")"

  jq -e --arg active "${active}" --arg pending "${pending}" '
    .status.observedGeneration == .metadata.generation and
    .status.numServeEndpoints > 0 and
    any(.status.activeServiceStatus.rayClusterStatus.conditions[]?;
      .type == "HeadPodReady" and .status == "True" and .reason == "HeadPodRunningAndReady") and
    any(.status.activeServiceStatus.rayClusterStatus.conditions[]?;
      .type == "RayClusterProvisioned" and .status == "True" and .reason == "AllPodRunningAndReadyFirstTime") and
    any(.status.conditions[]?;
      .type == "Ready" and .status == "True" and .reason == "NonZeroServeEndpoints") and
    any(.status.conditions[]?;
      .type == "UpgradeInProgress" and .status == "True" and .reason == "BothActivePendingClustersExist") and
    $active != "" and $pending != "" and $active != $pending
  ' <<<"${service}" >/dev/null || fail "RayService does not report the expected active/pending revision state"

  jq -e --arg uid "${service_uid}" --arg active "${active}" --arg pending "${pending}" \
    --arg rayImage "${RAY_IMAGE}" --arg pendingImage "${PENDING_IMAGE}" --arg version "${KUBERAY_VERSION}" '
    (.items | length) == 2 and
    ([.items[].metadata.name] | sort) == ([$active, $pending] | sort) and
    all(.items[];
      any(.metadata.ownerReferences[]?;
        .apiVersion == "ray.io/v1" and .kind == "RayService" and .uid == $uid and .controller == true) and
      .metadata.labels["ray.io/originated-from-cr-name"] == "revision-demo" and
      .metadata.labels["ray.io/originated-from-crd"] == "RayService" and
      .metadata.annotations["ray.io/kuberay-version"] == $version and
      .metadata.annotations["ray.io/num-worker-groups"] == "0") and
    any(.items[];
      .metadata.name == $active and
      .spec.headGroupSpec.template.spec.containers[0].image == $rayImage and
      any(.status.conditions[]?; .type == "HeadPodReady" and .status == "True" and .reason == "HeadPodRunningAndReady") and
      any(.status.conditions[]?; .type == "RayClusterProvisioned" and .status == "True" and .reason == "AllPodRunningAndReadyFirstTime")) and
    any(.items[];
      .metadata.name == $pending and
      .spec.headGroupSpec.template.spec.containers[0].image == $pendingImage and
      any(.status.conditions[]?;
        .type == "HeadPodReady" and .status == "False" and
        (.reason == "CrashLoopBackOff" or .reason == "RunContainerError")) and
      any(.status.conditions[]?; .type == "RayClusterProvisioned" and .status == "False" and .reason == "RayClusterPodsProvisioning"))
  ' <<<"${clusters}" >/dev/null || fail "RayClusters do not preserve the expected ownership, origin labels, images, and direct conditions"
}

assert_child_ownership_and_labels() {
  local service clusters pods services active pending service_uid active_uid pending_uid
  local active_pod pending_pod active_head_service pending_head_service
  service="$(rayservice_json)"
  clusters="$(kc -n "${DEMO_NS}" get rayclusters.ray.io -o json)"
  pods="$(kc -n "${DEMO_NS}" get pods -o json)"
  services="$(kc -n "${DEMO_NS}" get services -o json)"
  service_uid="$(jq -r '.metadata.uid' <<<"${service}")"
  active="$(jq -r '.status.activeServiceStatus.rayClusterName' <<<"${service}")"
  pending="$(jq -r '.status.pendingServiceStatus.rayClusterName' <<<"${service}")"
  active_uid="$(jq -r --arg name "${active}" '.items[] | select(.metadata.name == $name) | .metadata.uid' <<<"${clusters}")"
  pending_uid="$(jq -r --arg name "${pending}" '.items[] | select(.metadata.name == $name) | .metadata.uid' <<<"${clusters}")"
  active_pod="$(jq -r --arg name "${active}" '.items[] | select(.metadata.name == $name) | .status.head.podName' <<<"${clusters}")"
  pending_pod="$(jq -r --arg name "${pending}" '.items[] | select(.metadata.name == $name) | .status.head.podName' <<<"${clusters}")"
  active_head_service="$(jq -r --arg name "${active}" '.items[] | select(.metadata.name == $name) | .status.head.serviceName' <<<"${clusters}")"
  pending_head_service="$(jq -r --arg name "${pending}" '.items[] | select(.metadata.name == $name) | .status.head.serviceName' <<<"${clusters}")"

  jq -n -e --argjson pods "${pods}" --arg active "${active}" --arg pending "${pending}" \
    --arg activeUid "${active_uid}" --arg pendingUid "${pending_uid}" \
    --arg activePod "${active_pod}" --arg pendingPod "${pending_pod}" \
    --arg rayImage "${RAY_IMAGE}" --arg pendingImage "${PENDING_IMAGE}" '
    ($pods.items[] | select(.metadata.name == $activePod)) as $activePodObject |
    ($pods.items[] | select(.metadata.name == $pendingPod)) as $pendingPodObject |
    ($pods.items | map(select(.metadata.name == $activePod)) | length) == 1 and
    ($pods.items | map(select(.metadata.name == $pendingPod)) | length) == 1 and
    any($activePodObject.metadata.ownerReferences[]?;
      .apiVersion == "ray.io/v1" and .kind == "RayCluster" and .uid == $activeUid and .controller == true) and
    $activePodObject.metadata.labels["ray.io/cluster"] == $active and
    $activePodObject.metadata.labels["ray.io/group"] == "headgroup" and
    $activePodObject.metadata.labels["ray.io/node-type"] == "head" and
    $activePodObject.metadata.labels["ray.io/serve"] == "true" and
    $activePodObject.spec.containers[0].image == $rayImage and
    $activePodObject.status.phase == "Running" and
    any($activePodObject.status.conditions[]?; .type == "Ready" and .status == "True") and
    any($pendingPodObject.metadata.ownerReferences[]?;
      .apiVersion == "ray.io/v1" and .kind == "RayCluster" and .uid == $pendingUid and .controller == true) and
    $pendingPodObject.metadata.labels["ray.io/cluster"] == $pending and
    $pendingPodObject.metadata.labels["ray.io/group"] == "headgroup" and
    $pendingPodObject.metadata.labels["ray.io/node-type"] == "head" and
    $pendingPodObject.metadata.labels["ray.io/serve"] == "false" and
    $pendingPodObject.spec.containers[0].image == $pendingImage and
    any($pendingPodObject.status.conditions[]?; .type == "Ready" and .status == "False") and
    ($pendingPodObject.status.containerStatuses[0].state.waiting.reason == "CrashLoopBackOff" or
      ($pendingPodObject.status.containerStatuses[0].lastState.terminated.reason == "StartError" and
       ($pendingPodObject.status.containerStatuses[0].lastState.terminated.message | contains("/bin/bash"))))
  ' >/dev/null || fail "Active and pending head Pods do not preserve RayCluster ownership and role labels"

  jq -n -e --argjson services "${services}" --arg serviceUid "${service_uid}" \
    --arg active "${active}" --arg pending "${pending}" --arg activeUid "${active_uid}" --arg pendingUid "${pending_uid}" \
    --arg activeHead "${active_head_service}" --arg pendingHead "${pending_head_service}" '
    ($services.items[] | select(.metadata.name == $activeHead)) as $activeHeadObject |
    ($services.items[] | select(.metadata.name == $pendingHead)) as $pendingHeadObject |
    any($activeHeadObject.metadata.ownerReferences[]?;
      .apiVersion == "ray.io/v1" and .kind == "RayCluster" and .uid == $activeUid and .controller == true) and
    $activeHeadObject.spec.selector["ray.io/cluster"] == $active and
    $activeHeadObject.spec.selector["ray.io/node-type"] == "head" and
    any($pendingHeadObject.metadata.ownerReferences[]?;
      .apiVersion == "ray.io/v1" and .kind == "RayCluster" and .uid == $pendingUid and .controller == true) and
    $pendingHeadObject.spec.selector["ray.io/cluster"] == $pending and
    $pendingHeadObject.spec.selector["ray.io/node-type"] == "head" and
    ([ $services.items[] | select(any(.metadata.ownerReferences[]?;
      .apiVersion == "ray.io/v1" and .kind == "RayService" and .uid == $serviceUid and .controller == true)) ] | length) == 2 and
    any($services.items[];
      .metadata.labels["ray.io/originated-from-cr-name"] == "revision-demo" and
      .metadata.labels["ray.io/originated-from-crd"] == "RayService" and
      .metadata.labels["ray.io/identifier"] == "revision-demo-head" and
      .spec.selector["ray.io/cluster"] == $active) and
    any($services.items[];
      .metadata.labels["ray.io/originated-from-cr-name"] == "revision-demo" and
      .metadata.labels["ray.io/originated-from-crd"] == "RayService" and
      .metadata.labels["ray.io/serve"] == "revision-demo-serve" and
      .spec.selector["ray.io/cluster"] == $active)
  ' >/dev/null || fail "Generated Services do not preserve RayCluster/RayService ownership and active selectors"
}

apply_fixtures() {
  step "Applying the RayService fixture foundation"
  kc apply -f "${FIXTURES_DIR}/00-namespace-config.yaml" >/dev/null
  if kc -n "${DEMO_NS}" get rayservices.ray.io "${RAYSERVICE_NAME}" >/dev/null 2>&1; then
    note "preserving the reconciled RayService revisions (use reset after changing fixtures)"
  else
    kc apply -f "${FIXTURES_DIR}/10-rayservice.yaml" >/dev/null
    ok "Healthy head-only RayService submitted without patching status"
  fi
}

ensure_revision_pair() {
  local image baseline_active current_active
  assert_rayservice_spec
  image="$(rayservice_json | jq -r '.spec.rayClusterConfig.headGroupSpec.template.spec.containers[0].image')"
  if [ "${image}" = "${RAY_IMAGE}" ]; then
    step "Waiting for the real Ray Serve application"
    wait_until "RayService Ready with a healthy Serve deployment" healthy_baseline_ready
    baseline_active="$(active_cluster_name)"
    verify_serve_response
    ok "Active revision ${baseline_active} is Ready and serves radar-kuberay-ready"
    step "Creating an intentionally non-runnable pending revision"
    kc -n "${DEMO_NS}" patch rayservices.ray.io "${RAYSERVICE_NAME}" --type=json \
      -p="[{\"op\":\"replace\",\"path\":\"/spec/rayClusterConfig/headGroupSpec/template/spec/containers/0/image\",\"value\":\"${PENDING_IMAGE}\"}]" >/dev/null
    wait_until "distinct active and pending RayService revisions" revision_pair_ready
    wait_until "controller-reported pending startup failure" pending_failure_ready
    current_active="$(active_cluster_name)"
    [ "${current_active}" = "${baseline_active}" ] || fail "The active RayCluster changed while creating the pending revision"
    ok "NewCluster preserved active ${current_active} and exposed pending $(pending_cluster_name)"
  else
    step "Reusing the active/pending RayService revision pair"
    wait_until "distinct active and pending RayService revisions" revision_pair_ready
    wait_until "controller-reported pending startup failure" pending_failure_ready
  fi
}

cmd_verify() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  require_owned_cluster
  assert_cluster_contract
  assert_operator_contract
  step "Verifying controller-earned RayService revisions"
  assert_rayservice_spec
  wait_until "distinct active and pending RayService revisions" revision_pair_ready
  wait_until "controller-reported pending startup failure" pending_failure_ready
  assert_revision_ownership_and_status
  assert_child_ownership_and_labels
  verify_serve_response
  ok "Active Serve response, pending failure, conditions, ownership UIDs, labels, and selectors verified"
}

cmd_verify_radar() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd curl "https://curl.se/download.html"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  require_owned_cluster
  assert_cluster_contract
  step "Verifying Radar API at ${RADAR_URL}"

  local response rayservices rayclusters pods services active pending service_uid
  response="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/health")"
  jq -e '.status == "healthy"' <<<"${response}" >/dev/null || fail "Radar health response is not healthy"

  rayservices="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/resources/rayservices?group=ray.io")"
  jq -e '
    type == "array" and length == 1 and .[0].apiVersion == "ray.io/v1" and
    .[0].status.numServeEndpoints > 0 and
    any(.[0].status.conditions[]?; .type == "Ready" and .status == "True" and .reason == "NonZeroServeEndpoints") and
    any(.[0].status.conditions[]?; .type == "UpgradeInProgress" and .status == "True" and .reason == "BothActivePendingClustersExist") and
    .[0].status.activeServiceStatus.rayClusterName != .[0].status.pendingServiceStatus.rayClusterName
  ' <<<"${rayservices}" >/dev/null || fail "Radar did not return the group-pure active/pending RayService status"
  active="$(jq -r '.[0].status.activeServiceStatus.rayClusterName' <<<"${rayservices}")"
  pending="$(jq -r '.[0].status.pendingServiceStatus.rayClusterName' <<<"${rayservices}")"
  service_uid="$(jq -r '.[0].metadata.uid' <<<"${rayservices}")"

  rayclusters="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/resources/rayclusters?group=ray.io")"
  jq -e --arg active "${active}" --arg pending "${pending}" --arg uid "${service_uid}" '
    type == "array" and length == 2 and
    ([.[].metadata.name] | sort) == ([$active, $pending] | sort) and
    all(.[]; any(.metadata.ownerReferences[]?; .uid == $uid and .kind == "RayService" and .controller == true)) and
    any(.[]; .metadata.name == $active and
      any(.status.conditions[]?; .type == "HeadPodReady" and .status == "True" and .reason == "HeadPodRunningAndReady")) and
    any(.[]; .metadata.name == $pending and
      any(.status.conditions[]?;
        .type == "HeadPodReady" and .status == "False" and
        (.reason == "CrashLoopBackOff" or .reason == "RunContainerError")))
  ' <<<"${rayclusters}" >/dev/null || fail "Radar did not return both owned RayClusters with their native conditions"

  pods="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/resources/pods?namespace=${DEMO_NS}")"
  jq -e --arg active "${active}" --arg pending "${pending}" '
    type == "array" and
    ([.[] | select(.metadata.labels["ray.io/cluster"] == $active and
      .metadata.labels["ray.io/group"] == "headgroup" and .metadata.labels["ray.io/node-type"] == "head")] | length) == 1 and
    ([.[] | select(.metadata.labels["ray.io/cluster"] == $pending and
      .metadata.labels["ray.io/group"] == "headgroup" and .metadata.labels["ray.io/node-type"] == "head")] | length) == 1
  ' <<<"${pods}" >/dev/null || fail "Radar did not expose both controller-created head Pods and their role labels"

  services="$(curl -fsS --connect-timeout 5 --max-time 15 "${RADAR_URL}/api/resources/services?namespace=${DEMO_NS}")"
  jq -e --arg active "${active}" --arg pending "${pending}" '
    type == "array" and
    any(.[]; .spec.selector["ray.io/cluster"] == $active and .metadata.labels["ray.io/serve"] == "revision-demo-serve") and
    any(.[]; .spec.selector["ray.io/cluster"] == $active and .metadata.labels["ray.io/node-type"] == "head") and
    any(.[]; .spec.selector["ray.io/cluster"] == $pending and .metadata.labels["ray.io/node-type"] == "head")
  ' <<<"${services}" >/dev/null || fail "Radar did not expose the active Serve and both head Service selectors"
  ok "Radar returned group-aware RayService/RayCluster status and the Pod/Service lineage"
}

cmd_up() {
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd jq "https://jqlang.github.io/jq/download/"
  prepare_context_restore

  if cluster_exists; then
    cluster_owned || fail "Refusing to modify existing unowned cluster '${CLUSTER_NAME}'. Choose another CLUSTER_NAME or remove it yourself."
    step "Reusing owned kind cluster '${CLUSTER_NAME}'"
    assert_cluster_contract
  else
    step "Creating dedicated kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --image "${KIND_NODE_IMAGE}" --wait 120s
    mark_cluster
    assert_cluster_contract
    ok "Cluster created and ownership marker recorded"
  fi

  require_owned_cluster
  mark_cluster
  install_kuberay
  apply_fixtures
  ensure_revision_pair
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
  step "KubeRay operator"
  kc -n default get deployment kuberay-operator -o wide
  step "RayService"
  kc -n "${DEMO_NS}" get rayservices.ray.io
  note "active=$(active_cluster_name) pending=$(pending_cluster_name)"
  step "RayClusters"
  kc -n "${DEMO_NS}" get rayclusters.ray.io \
    -L ray.io/originated-from-cr-name,ray.io/originated-from-crd
  step "Head Pods"
  kc -n "${DEMO_NS}" get pods \
    -L ray.io/cluster,ray.io/group,ray.io/node-type,ray.io/serve
  step "Services"
  kc -n "${DEMO_NS}" get services \
    -L ray.io/cluster,ray.io/originated-from-cr-name,ray.io/originated-from-crd
}

usage() {
  cat <<EOF
Usage: $0 <command>

Commands:
  up             Create/reuse the owned kind cluster, install KubeRay, reconcile and verify
  down           Delete only the ownership-marked kind cluster
  reset          Safely recreate the owned cluster and both RayService revisions
  status         Show the operator, active/pending revisions, Pods, and Services
  verify         Assert real controller status, ownership, labels, and active Serve response
  verify-radar   Assert a running Radar exposes both revisions through group-aware APIs
  help           Show this message

Environment:
  CLUSTER_NAME   kind cluster name (default: radar-kuberay-demo)
  WAIT_SECONDS   maximum wait per reconciled state (default: 480)
  RADAR_URL      running Radar base URL for verify-radar (default: http://127.0.0.1:9280)

This validates one head-only CPU RayService on KubeRay ${KUBERAY_VERSION}: a
healthy active Serve revision and an intentionally non-runnable pending
NewCluster revision. It does not validate GPUs, workers, autoscaling, Kueue,
telemetry, cost, or traffic shifting.
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
