#!/usr/bin/env bash
# Offline tests for scripts/kueue-scenarios.sh: no cluster, kubectl replaced by a stub.
# Covers the decisions a misread cluster could get wrong. Run after changing the script:
#   ./scripts/kueue-scenarios/test.sh

set -euo pipefail

# shellcheck source=../kueue-scenarios.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/kueue-scenarios.sh"
PREFIX=t
NO_COLOR=1
PASSED=0
FAILED=0

# The stub answers from these variables; KC_LOG collects every call.
JOBS='' WORKLOADS='' GET_JSON='' CAN_I='' KC_FAIL=''
KC_LOG="$(mktemp)"
trap 'rm -f -- "${KC_LOG}"' EXIT

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
    "patch "*) ;;
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
    '{metadata: {name: $n, generation: 1, ownerReferences: [{kind: $k, name: "j", uid: $u, controller: $c}]}, status: $s}'
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
KC_FAIL=''

echo "==> The check controller keeps what Kueue recorded"
WORKLOADS="$(workload wl uid-1 Job true '{"admissionChecks":[{"name":"t-check","state":"Pending","retryCount":1},{"name":"other","state":"Ready"}]}' | list)"
: >"${KC_LOG}"
set_check t-busy j Retry "again" >/dev/null
patch="$(grep '^patch ' "${KC_LOG}" | sed -e 's/.*-p //')"
check "Retry keeps retryCount" 0 jq -e 'any(.status.admissionChecks[]; .name == "t-check" and .state == "Retry" and .retryCount == 1)' <<<"${patch}"
check "Retry keeps the other check" 0 jq -e 'any(.status.admissionChecks[]; .name == "other" and .state == "Ready")' <<<"${patch}"
check "already retried" 0 retried t-busy j

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

echo "==> Ownership and cleanup stay inside the prefix"
objects_in() { [ "$1" = config ] && printf 'namespace|%s|\n' t-quota t-busy; return 0; }
CONFIG_FILES=(config) JOB_FILES=(jobs)
GET_JSON='{"kind":"List","items":[{"metadata":{"name":"t-quota","labels":{"radar.skyhook.io/kueue-scenarios":"t"}}}]}'
check "owned objects pass" 0 check_ownership
GET_JSON='{"kind":"List","items":[{"metadata":{"name":"t-busy","labels":{}}}]}'
check "an unlabeled same-named object is refused" 1 check_ownership
KC_FAIL="get namespace"
check "an unreadable object is refused" 1 check_ownership
KC_FAIL=''
GET_JSON='{"items":[{"metadata":{"name":"t-quota"}},{"metadata":{"name":"someone-else"}}]}'
check "down only names objects with the prefix" 0 test "$(owned_names namespaces)" = '|t-quota'

echo "==> Permission answers"
CAN_I=yes; check "yes is yes" 0 viewer_can yes list workloads t-busy
CAN_I=no; check "no is no" 0 viewer_can no list workloads t-busy
CAN_I=''; check "an error is neither" 2 viewer_can no list workloads t-busy

echo
echo "${PASSED} passed, ${FAILED} failed"
[ "${FAILED}" -eq 0 ]
