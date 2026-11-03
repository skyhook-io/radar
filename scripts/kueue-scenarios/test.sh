#!/usr/bin/env bash
# Offline tests for scripts/kueue-scenarios.sh: no cluster, kubectl replaced by a stub.
# Covers the decisions a misread cluster could get wrong. Run after changing the script:
#   ./scripts/kueue-scenarios/test.sh

set -euo pipefail

# The script checks for a kubectl binary (and its patch --subresource flag) before
# calling its kc wrapper; give it one that can't reach anything, so the tests
# need only bash and jq.
STUB_BIN="$(mktemp -d)"
printf '#!/bin/sh\n[ "$1 $2" = "patch --help" ] && echo "--subresource=status" && exit 0\nexit 1\n' >"${STUB_BIN}/kubectl"
chmod +x "${STUB_BIN}/kubectl"
PATH="${STUB_BIN}:${PATH}"

# shellcheck source=../kueue-scenarios.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/kueue-scenarios.sh"
PREFIX=t
NO_COLOR=1
PASSED=0
FAILED=0

# The stub answers from these variables; KC_LOG collects every call.
JOBS='' WORKLOADS='' GET_JSON='' CAN_I='' KC_FAIL=''
KC_LOG="$(mktemp)"

DRY_RUN='' CREATED="$(mktemp)" CRDS='' OWNED='{}' DEPLOYMENTS='{"items":[]}' CREATE_ERROR='' CONFIG_VIEW='{}'
# Answers to successive patch calls, one per line ("ok" or an error); kc runs in
# command substitutions, so the queue lives in a file.
PATCH_ANSWERS="$(mktemp)"
trap 'rm -f -- "${KC_LOG}" "${CREATED}" "${PATCH_ANSWERS}"; rm -rf -- "${STUB_BIN}"' EXIT

kc() {
  printf '%s\n' "$*" >>"${KC_LOG}"
  if [ -n "${KC_FAIL}" ] && [[ "$*" == *"${KC_FAIL}"* ]]; then
    echo "error: the server is currently unable to handle the request" >&2
    return 1
  fi
  case "$*" in
    "get jobs.batch -n "*) printf '%s' "${JOBS}" ;;
    "get workloads.kueue.x-k8s.io -n "*) printf '%s' "${WORKLOADS}" ;;
    "auth can-i "*) printf '%s\n' "${CAN_I}"; [ "${CAN_I}" = yes ] ;;
    "create --dry-run=client "*) cat >/dev/null; printf '%s' "${DRY_RUN}" ;;
    "create -f -")
      cat >>"${CREATED}"
      [ -z "${CREATE_ERROR}" ] || { echo "${CREATE_ERROR}" >&2; return 1; } ;;
    "create token "*) echo "token-123" ;;
    "config view "*) printf '%s' "${CONFIG_VIEW}" ;;
    "get crd "*) [[ " ${CRDS} " == *" ${3%%.*} "* ]] && echo "customresourcedefinition/$3"; return 0 ;;
    *" -l radar.skyhook.io/kueue-scenarios=t -o json") jq -c --arg r "${2}" '.[$r] // {items: []}' <<<"${OWNED}" ;;
    "patch "*)
      local answer
      answer="$(head -n 1 "${PATCH_ANSWERS}")"
      sed -i.bak '1d' "${PATCH_ANSWERS}" && rm -f "${PATCH_ANSWERS}.bak"
      [ -z "${answer}" ] || [ "${answer}" = ok ] && return 0
      echo "${answer}" >&2; return 1 ;;
    "get deployments -A "*) printf '%s' "${DEPLOYMENTS}" ;;
    "delete "*|"wait "*|version) ;;
    get*) printf '%s' "${GET_JSON}" ;;
    *) echo "unexpected kc call: $*" >&2; return 1 ;;
  esac
}

check() { # check <description> <expected exit code> <command...>
  local desc="$1" want="$2" got=0
  shift 2
  ( "$@" ) >/dev/null 2>&1 || got=$?
  if [ "${got}" -eq "${want}" ]; then
    PASSED=$((PASSED + 1)); printf '    ✓ %s\n' "${desc}"
  else
    FAILED=$((FAILED + 1)); printf '    ✗ %s (exit %s, want %s)\n' "${desc}" "${got}" "${want}"
  fi
}

job() { # job <name> <uid> [owner label value]
  jq -cn --arg n "$1" --arg u "$2" --arg o "${3-t}" \
    '{metadata: {name: $n, namespace: "t-busy", uid: $u, labels: {"radar.skyhook.io/kueue-scenarios": $o}}}'
}
workload() { # workload <name> <owner uid> <owner kind> <controller> [status json]
  local status='{}'
  [ -z "${5:-}" ] || status="$5"
  jq -cn --arg n "$1" --arg u "$2" --arg k "$3" --argjson c "$4" --argjson s "${status}" \
    '{metadata: {name: $n, generation: 1, resourceVersion: "42", ownerReferences: [{kind: $k, name: "j", uid: $u, controller: $c}]}, status: $s}'
}
list() { jq -cs '{items: .}'; }

echo "==> Workload reads tell yes, no, unreadable and missing apart"
JOBS="$(job j uid-1 | list)"
WORKLOADS="$(workload wl uid-1 Job true '{"conditions":[{"type":"Admitted","status":"True","reason":"Admitted"}]}' | list)"
check "condition present" 0 has_cond t-busy j Admitted True Admitted
check "condition with another reason" 1 has_cond t-busy j Admitted True Pending
check "Job missing" 3 has_cond t-busy other Admitted True
JOBS="$(job j uid-1 someone-else | list)"
check "Job owned by another prefix counts as missing" 3 has_cond t-busy j Admitted True
JOBS="$(job j uid-1 | list)"
WORKLOADS="$(workload wl uid-1 ConfigMap true | list)"
check "a Workload owned by something else is not the Job's" 1 has_cond t-busy j Admitted True
WORKLOADS="$(workload wl uid-2 Job true | list)"
check "a Workload of an older Job with the same name is not the Job's" 1 has_cond t-busy j Admitted True
KC_FAIL="get workloads"
check "failed Workload read is unreadable, not no" 2 has_cond t-busy j Admitted True
check "released needs a successful read" 2 released t-busy j
check "known stops on an unreadable answer" 1 known has_cond t-busy j Admitted True
check "known stops on a missing Job" 1 known has_cond t-busy other Admitted True
KC_FAIL=''
WAIT_SECONDS=600
check "waiting stops at once on a missing Job" 1 wait_for "never" has_cond t-busy other Admitted True
WAIT_SECONDS=240
JOBS="$(job j uid-1 | list)"
WORKLOADS="$(workload wl uid-1 Job true '{"conditions":[]}' | list)"
check "a predicate that can't evaluate is its own answer" 4 wl_is t-busy j '.metadata.name | tonumber'
check "known stops on it" 1 known wl_is t-busy j '.metadata.name | tonumber'

echo "==> The check controller keeps what Kueue recorded"
WORKLOADS="$(workload wl uid-1 Job true '{"admissionChecks":[{"name":"t-check","state":"Pending","retryCount":1},{"name":"other","state":"Ready"}]}' | list)"
: >"${KC_LOG}"
set_check t-busy j Ready "fine" >/dev/null
patch="$(grep '^patch ' "${KC_LOG}" | sed -e 's/.*-p //')"
check "a check write keeps retryCount" 0 jq -e 'any(.status.admissionChecks[]; .name == "t-check" and .state == "Ready" and .retryCount == 1)' <<<"${patch}"
check "a check write keeps the other check" 0 jq -e 'any(.status.admissionChecks[]; .name == "other" and .state == "Ready")' <<<"${patch}"
check "already retried" 0 retried t-busy j
: >"${KC_LOG}"
set_check t-busy j Retry "again" >/dev/null
check "Retry is not requested again once Kueue counted one (same snapshot)" 1 grep -q '^patch ' "${KC_LOG}"
WORKLOADS="$(workload wl uid-1 Job true '{"admissionChecks":[{"name":"t-check","state":"Pending"}]}' | jq -c '.spec.active = false' | list)"
: >"${KC_LOG}"
set_check t-busy j Rejected "no" >/dev/null
check "Rejected is not requested again once Kueue deactivated the Workload" 1 grep -q '^patch ' "${KC_LOG}"
WORKLOADS="$(workload wl uid-1 Job true '{"admissionChecks":[{"name":"t-check","state":"Pending","retryCount":1},{"name":"other","state":"Ready"}]}' | list)"
check "the write carries the resourceVersion it read" 0 jq -e '.metadata.resourceVersion == "42"' <<<"${patch}"
printf '%s\n' 'Error from server (Conflict): the object has been modified; please apply your changes to the latest version' ok >"${PATCH_ANSWERS}"
: >"${KC_LOG}"
set_check t-busy j Ready "ok" >/dev/null 2>&1
check "a conflict is retried" 0 test "$(grep -c '^patch ' "${KC_LOG}")" = 2
printf '%s\n' 'Error from server (Forbidden): workloads.kueue.x-k8s.io "wl" is forbidden' >"${PATCH_ANSWERS}"
: >"${KC_LOG}"
check "any other refusal stops at once" 1 set_check t-busy j Ready "ok"
check "  without retrying" 0 test "$(grep -c '^patch ' "${KC_LOG}")" = 1
: >"${PATCH_ANSWERS}"

echo "==> Radar's Workload sets match the Jobs on the cluster"
uids='{"t-busy/a":"uid-a","t-busy/b":"uid-b"}'
body() { jq -cn --argjson items "$1" '{items: $items}'; }
match() { jq -e --argjson uids "${uids}" "$(jobs_are "$@")" <<<"${BODY}" >/dev/null; }
BODY="$(body '[{"metadata":{"ownerReferences":[{"kind":"Job","uid":"uid-a","controller":true}]}},{"metadata":{"ownerReferences":[{"kind":"Job","uid":"uid-b","controller":true}]}}]')"
check "the exact set matches" 0 match t-busy/a t-busy/b
check "a missing item fails" 1 match t-busy/a
BODY="$(body '[{"metadata":{"ownerReferences":[{"kind":"ConfigMap","uid":"uid-a","controller":true}]}}]')"
check "a same-UID owner of another kind fails" 1 match t-busy/a
BODY="$(body '[{"metadata":{"ownerReferences":[{"kind":"Job","uid":"uid-a","controller":false}]}}]')"
check "a non-controller reference fails" 1 match t-busy/a
BODY="$(body '[]')"
check "a Job unknown on the cluster fails even with no items" 1 match t-busy/zzz
BODY="$(body '[{"metadata":{"ownerReferences":[{"kind":"Job","uid":"uid-a","controller":true}]}},{"metadata":{"name":"ownerless"}}]')"
check "an extra Workload without a controlling Job fails" 1 match t-busy/a
BODY="$(body '[{"metadata":{"ownerReferences":[{"kind":"Job","uid":"uid-a","controller":true}]}},{"metadata":{"ownerReferences":[{"kind":"RayJob","uid":"r","controller":true}]}}]')"
check "an extra Workload owned by another kind fails" 1 match t-busy/a

echo "==> Kueue version report"
version_line() { DEPLOYMENTS="$(jq -cn --arg i "$1" '{items: [{spec: {template: {spec: {containers: [{image: $i}]}}}}]}')"; report_kueue_version 2>&1; }
check "a tag in the validated series is accepted" 0 grep -q '✓' <<<"$(version_line registry.k8s.io/kueue/kueue:v0.18.0)"
check "a digest-pinned tag is read by its tag" 0 grep -q '✓' <<<"$(version_line registry.k8s.io/kueue/kueue:v0.18.1@sha256:abcd)"
check "another release warns" 0 grep -q '!' <<<"$(version_line registry.k8s.io/kueue/kueue:v0.19.2)"
check "a registry port is not a tag" 0 grep -q '✓' <<<"$(version_line registry.example:5000/kueue/kueue:v0.18.0)"
check "a digest without a tag reads as unknown" 0 grep -q 'version is unknown' <<<"$(version_line registry.example:5000/kueue/kueue@sha256:abcd)"

echo "==> Ownership and cleanup stay inside the prefix"
objects_in() {
  case "$1" in
    config) printf 'namespace|%s|\n' t-quota t-busy ;;
    jobs) printf 'job.batch|%s|t-busy\n' j1 j2 ;;
  esac
}
CONFIG_FILES=(config) JOB_FILES=(jobs)
GET_JSON='{"kind":"List","items":[{"metadata":{"name":"t-quota","labels":{"radar.skyhook.io/kueue-scenarios":"t"}}}]}'
check "owned objects pass" 0 check_ownership
: >"${KC_LOG}"
check_ownership >/dev/null
check "one read per kind and namespace" 0 test "$(grep -c '^get ' "${KC_LOG}")" = 2
check "the Jobs are read together" 0 grep -q '^get job.batch j1 j2 -n t-busy ' "${KC_LOG}"
GET_JSON='{"kind":"List","items":[{"metadata":{"name":"t-busy","labels":{}}}]}'
check "an unlabeled same-named object is refused" 1 check_ownership
GET_JSON='{"kind":"List","items":[{"metadata":{"name":"t-quota","deletionTimestamp":"2026-01-01T00:00:00Z","labels":{"radar.skyhook.io/kueue-scenarios":"t"}}}]}'
check "an owned object still being deleted is refused" 1 check_ownership
KC_FAIL="get namespace"
check "an unreadable object is refused" 1 check_ownership
KC_FAIL=''
OWNED='{"namespaces":{"items":[{"metadata":{"name":"t-quota"}},{"metadata":{"name":"someone-else"}}]}}'
check "down only names objects with the prefix" 0 test "$(owned_names namespaces)" = '|t-quota'

echo "==> down deletes only owned, prefixed objects, and skips kinds whose CRD is gone"
OWNED='{
  "jobs.batch": {"items": [{"metadata": {"namespace": "t-busy", "name": "j1"}}, {"metadata": {"namespace": "other", "name": "j2"}}]},
  "namespaces": {"items": [{"metadata": {"name": "t-busy"}}, {"metadata": {"name": "copied-label"}}]},
  "clusterqueues.kueue.x-k8s.io": {"items": [{"metadata": {"name": "t-busy"}}, {"metadata": {"name": "gpu-cluster-queue"}}]},
  "clusterroles.rbac.authorization.k8s.io": {"items": [{"metadata": {"name": "t-viewer-cluster"}}]}
}'
CRDS="clusterqueues"
: >"${KC_LOG}"
cmd_down >/dev/null
deletes="$(grep '^delete ' "${KC_LOG}")"
sel='-l radar.skyhook.io/kueue-scenarios=t --field-selector metadata.name='
check "deletes the owned Job, still requiring the label" 0 grep -qF "delete job.batch -n t-busy ${sel}j1 " <<<"${deletes}"
check "deletes the owned namespace" 0 grep -qF "delete namespaces ${sel}t-busy " <<<"${deletes}"
check "deletes the owned ClusterQueue" 0 grep -qF "delete clusterqueues.kueue.x-k8s.io ${sel}t-busy " <<<"${deletes}"
check "deletes the owned ClusterRole" 0 grep -qF "delete clusterroles.rbac.authorization.k8s.io ${sel}t-viewer-cluster " <<<"${deletes}"
check "leaves everything else alone" 0 test "$(grep -cE 'j2|copied-label|gpu-cluster-queue' <<<"${deletes}")" = 0
check "skips kinds whose CRD is gone" 1 grep -q 'resourceflavors' <<<"${deletes}"
KC_FAIL="get crd cohorts"
check "an unreadable CRD stops down instead of skipping the kind" 1 cmd_down
KC_FAIL=''
KC_FAIL="wait --for=delete"
check "down exits 2 when it can't confirm the deletions" 2 cmd_down
KC_FAIL=''

echo "==> create_missing creates only what is absent"
DRY_RUN='{"kind":"Job","apiVersion":"batch/v1","metadata":{"name":"j1","namespace":"t-busy"}}
{"kind":"Job","apiVersion":"batch/v1","metadata":{"name":"j2","namespace":"t-busy"}}'
GET_JSON='{"kind":"List","items":[{"metadata":{"name":"j1","labels":{"radar.skyhook.io/kueue-scenarios":"t"}}}]}'
: >"${CREATED}"
create_missing 30-jobs-first.yaml
check "creates the missing Job" 0 jq -e '[.items[].metadata.name] == ["j2"]' "${CREATED}"
GET_JSON='{"kind":"List","items":[{"metadata":{"name":"j1","labels":{"radar.skyhook.io/kueue-scenarios":"t"}}},{"metadata":{"name":"j2","labels":{"radar.skyhook.io/kueue-scenarios":"t"}}}]}'
: >"${CREATED}"
create_missing 30-jobs-first.yaml
check "creates nothing when everything exists" 0 test ! -s "${CREATED}"
GET_JSON='{"kind":"List","items":[]}'
CREATE_ERROR='Error from server (AlreadyExists): jobs.batch "j1" already exists'
check "an object someone else created after the check makes up stop, not adopt it" 1 create_missing 30-jobs-first.yaml
CREATE_ERROR=''
GET_JSON='{"kind":"List","items":[{"metadata":{"name":"j1","labels":{}}}]}'
check "an object someone else created before the existence read is not reused" 1 create_missing 30-jobs-first.yaml

echo "==> Writes land only on owned, unchanged objects"
GET_JSON='{"metadata":{"name":"t-busy","resourceVersion":"7","labels":{"radar.skyhook.io/kueue-scenarios":"t"}},"spec":{"stopPolicy":"None"}}'
: >"${KC_LOG}"
write_owned clusterqueues.kueue.x-k8s.io "" t-busy spec '{spec: {stopPolicy: "Hold"}}' >/dev/null
check "an owned object is patched with the resourceVersion it was read at" 0 \
  jq -e '.metadata.resourceVersion == "7" and .spec.stopPolicy == "Hold"' <<<"$(grep '^patch ' "${KC_LOG}" | sed -e 's/.*-p //')"
check "a cluster-scoped write sends no namespace" 1 grep -q -- ' -n ' <<<"$(grep '^patch ' "${KC_LOG}")"
GET_JSON='{"metadata":{"name":"t-busy","labels":{"radar.skyhook.io/kueue-scenarios":"other"}}}'
check "an object of another prefix is not written" 1 write_owned clusterqueues.kueue.x-k8s.io "" t-busy spec '{spec: {stopPolicy: "Hold"}}'
GET_JSON='{"metadata":{"name":"t-busy","deletionTimestamp":"2026-01-01T00:00:00Z","labels":{"radar.skyhook.io/kueue-scenarios":"t"}}}'
check "an object being deleted is not written" 1 write_owned clusterqueues.kueue.x-k8s.io "" t-busy spec '{spec: {stopPolicy: "Hold"}}'

echo "==> The viewer kubeconfig"
OUT_DIR="$(mktemp -d)"
OUT_PATH="${OUT_DIR}/viewer.kubeconfig"
CONFIG_VIEW='{"clusters":[{"cluster":{"server":"https://example:443","certificate-authority-data":"Q0E=","tls-server-name":"internal.example"}}]}'
GET_JSON='{"metadata":{"name":"viewer","labels":{"radar.skyhook.io/kueue-scenarios":"t"}}}'
cmd_kubeconfig >/dev/null
check "keeps the whole cluster entry, tls-server-name included" 0 jq -e '.clusters[0].cluster["tls-server-name"] == "internal.example" and .clusters[0].cluster.server == "https://example:443"' "${OUT_PATH}"
check "uses the requested token" 0 jq -e '.users[0].user.token == "token-123"' "${OUT_PATH}"
check "is readable by its owner only" 0 test "$(stat -c '%a' "${OUT_PATH}" 2>/dev/null || stat -f '%Lp' "${OUT_PATH}")" = 600
check "leaves no temporary file" 0 test "$(find "${OUT_DIR}" -name '.kueue-viewer.*' | wc -l | tr -d ' ')" = 0
rm -rf -- "${OUT_DIR}"

echo "==> Arguments"
check "a flag without its value is refused" 1 main up --kubeconfig
tilde() { cmd_status() { printf '%s' "${KUBECONFIG_ARG}"; }; main status --kubeconfig '~/k.yaml'; }
check "a quoted ~ in --kubeconfig is expanded" 0 test "$(tilde 2>/dev/null)" = "${HOME}/k.yaml"

echo "==> Permission answers"
CAN_I=yes; check "yes is yes" 0 viewer_can yes list workloads t-busy
CAN_I=no; check "no is no" 0 viewer_can no list workloads t-busy
CAN_I='no - RBAC: clusterrole.rbac.authorization.k8s.io "gone" not found'; check "no with a reason is no" 0 viewer_can no list workloads t-busy
CAN_I=''; check "an error is neither" 2 viewer_can no list workloads t-busy

echo
echo "${PASSED} passed, ${FAILED} failed"
[ "${FAILED}" -eq 0 ]
