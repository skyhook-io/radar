#!/usr/bin/env bash
# Kueue queue-design scenarios on a cluster that already runs Kueue.
# See scripts/kueue-scenarios/README.md. Unlike kueue-demo.sh, this never creates
# a cluster or installs Kueue, never edits Kueue's configuration, and refuses to
# touch any object that lacks this run's ownership label.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="${SCRIPT_DIR}/kueue-scenarios"
# Per-user default, so two people who don't pass --prefix get separate copies.
DEFAULT_PREFIX="kq-$(id -un 2>/dev/null | tr 'A-Z' 'a-z' | tr -c 'a-z0-9' '-' | cut -c1-20 | sed -e 's/-*$//')"
[ "${DEFAULT_PREFIX}" != "kq-" ] || DEFAULT_PREFIX=radar-kueue
PREFIX="${KUEUE_SCENARIOS_PREFIX:-${DEFAULT_PREFIX}}"
FIXTURE="${KUEUE_SCENARIOS_FIXTURE:-kueue-scenarios}"
WAIT_SECONDS="${WAIT_SECONDS:-240}"
TOKEN_DURATION="${TOKEN_DURATION:-24h}"
RADAR_URL="${RADAR_URL:-http://127.0.0.1:9280}"
OWNER_LABEL="radar.skyhook.io/kueue-scenarios"
KUBECONFIG_ARG=''
CONTEXT_ARG=''
OUT_PATH=''
IDENTITY='full'
FAILURES=0
JOB_UIDS='{}'
READ_CACHE=''
VALIDATED_KUEUE='v0.18.'

CONFIG_FILES=(00-namespaces.yaml 10-cluster.yaml 15-staged-queues.yaml 20-localqueues.yaml 50-viewer-rbac.yaml)
JOB_FILES=(30-jobs-first.yaml 40-jobs-second.yaml)
REQUIRED_CRDS=(workloads clusterqueues localqueues resourceflavors cohorts admissionchecks workloadpriorityclasses)

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BLUE='\033[34m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'; C_RED='\033[31m'; C_RESET='\033[0m'
else
  C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_RESET=''
fi

step() { printf "${C_BLUE}==> %s${C_RESET}\n" "$1"; }
ok()   { printf "${C_GREEN}    ✓ %s${C_RESET}\n" "$1"; }
warn() { printf "${C_YELLOW}    ! %s${C_RESET}\n" "$1"; }
fail() { printf "${C_RED}    ✗ %s${C_RESET}\n" "$1" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: $0 <command> (--kubeconfig PATH | --context NAME) [options]

Commands:
  up            Create the scenarios and drive each to its end state. Safe to re-run,
                including after an interruption.
  verify        Check every scenario's end state on the cluster
  verify-radar  Check what a running Radar returns for the scenarios (RADAR_URL)
  status        Show the fixture queues and Workloads
  kubeconfig    Write a kubeconfig for the restricted viewer ServiceAccount (--out PATH)
  down          Delete everything labeled ${OWNER_LABEL}=<prefix>

Options:
  --kubeconfig PATH   kubeconfig to use; its current-context is never changed
  --context NAME      context to use from the default kubeconfig
  --prefix NAME       prefix for namespaces and cluster-scoped objects (default: ${PREFIX})
  --fixture VALUE     value of the radar.skyhook.io/fixture label (default: ${FIXTURE})
  --out PATH          output path for 'kubeconfig'
  --identity MODE     'verify-radar' only: how the Radar under test reads the cluster
                        full    Radar's own identity can read every fixture namespace (default)
                        viewer  Radar runs with the viewer kubeconfig (no authentication)
                        user    Radar runs with full access and --auth-mode=proxy; requests
                                are sent as the viewer user <prefix>-viewer

One of --kubeconfig or --context is required so the target cluster is explicit.
EOF
}

kc() {
  local args=()
  [ -n "${KUBECONFIG_ARG}" ] && args+=(--kubeconfig "${KUBECONFIG_ARG}")
  [ -n "${CONTEXT_ARG}" ] && args+=(--context "${CONTEXT_ARG}")
  kubectl ${args[@]+"${args[@]}"} --request-timeout=20s "$@"
}

render() {
  sed -e "s/\${PREFIX}/${PREFIX}/g" -e "s/\${FIXTURE}/${FIXTURE}/g" "${FIXTURES_DIR}/$1"
}

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

ns() { printf '%s-%s' "${PREFIX}" "$1"; }

# --- preflight -------------------------------------------------------------------

check_args() {
  command -v kubectl >/dev/null || fail "kubectl not found"
  command -v jq >/dev/null || fail "jq not found"
  kubectl patch --help 2>/dev/null | grep -q -- '--subresource' || fail "kubectl 1.24 or later is required (patch --subresource)"
  [[ "${PREFIX}" =~ ^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$ ]] || fail "--prefix must be a DNS label of at most 32 characters"
  [[ "${FIXTURE}" =~ ^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$ ]] || fail "--fixture must be a valid label value"
  [ -z "${KUBECONFIG_ARG}" ] || [ -r "${KUBECONFIG_ARG}" ] || fail "cannot read kubeconfig ${KUBECONFIG_ARG}"
  local out
  out="$(kc version 2>&1 >/dev/null)" || fail "cannot reach the cluster with the given --kubeconfig/--context: ${out}"
}

preflight() {
  check_args
  local crd json
  for crd in "${REQUIRED_CRDS[@]}"; do
    json="$(kc get crd "${crd}.kueue.x-k8s.io" --ignore-not-found -o json)" || fail "cannot read CRD ${crd}.kueue.x-k8s.io"
    [ -n "${json}" ] || fail "Kueue CRD ${crd}.kueue.x-k8s.io is missing"
    jq -e '.spec.versions[] | select(.name == "v1beta2" and .served)' <<<"${json}" >/dev/null || \
      fail "${crd}.kueue.x-k8s.io does not serve v1beta2; these scenarios need a Kueue release that serves v1beta2"
  done
}

# Best effort: the controller may run elsewhere or be unreadable to this identity.
report_kueue_version() {
  local image
  image="$(kc get deployments -A -l control-plane=controller-manager -o json 2>/dev/null \
    | jq -r '[.items[].spec.template.spec.containers[].image | select(test("/kueue(@|:)"))][0] // empty')" || true
  # The tag lives in the last path segment, before any digest; a registry port
  # (host:5000/...) is not a tag.
  local tag="${image##*/}"
  tag="${tag%@*}"
  case "${tag}" in *:*) tag="${tag##*:}" ;; *) tag='' ;; esac
  if [ -z "${image}" ]; then
    warn "could not find the Kueue controller Deployment; expectations were validated on Kueue ${VALIDATED_KUEUE}x"
  elif [ -z "${tag}" ]; then
    warn "Kueue controller ${image##*/} is pinned by digest only, so its version is unknown; expectations were validated on ${VALIDATED_KUEUE}x"
  elif [[ "${tag}" == ${VALIDATED_KUEUE}* ]]; then
    ok "Kueue controller ${image##*/} (API kueue.x-k8s.io/v1beta2)"
  else
    warn "Kueue controller ${image##*/}: expectations were validated on ${VALIDATED_KUEUE}x, so reasons and messages may differ"
  fi
}

# --- object inventory and ownership ------------------------------------------------

identity_jq='[(.kind | ascii_downcase) + (if (.apiVersion | contains("/")) then "." + (.apiVersion | split("/")[0]) else "" end),
  .metadata.name, (.metadata.namespace // "")]'
inventory_jq="(if .kind == \"List\" then .items[] else . end) | ${identity_jq} | join(\"|\")"

# Prints "<resource.group>|<name>|<namespace>" for every object in a manifest file.
# Fields are split on "|", not tabs: read collapses empty tab-separated fields, and
# cluster-scoped objects have an empty namespace.
objects_in() {
  local json
  json="$(render "$1" | kc create --dry-run=client -o json -f -)" || fail "cannot parse $1"
  jq -r "${inventory_jq}" <<<"${json}"
}

# Prints the ownership label of an existing object, "<none>" if unlabeled, nothing if absent.
owner_of() { # owner_of <resource> <name> <namespace>
  local json
  if [ -n "$3" ]; then
    json="$(kc get "$1" "$2" -n "$3" --ignore-not-found -o json)" || fail "cannot read $1 $3/$2"
  else
    json="$(kc get "$1" "$2" --ignore-not-found -o json)" || fail "cannot read $1 $2"
  fi
  [ -z "${json}" ] || jq -r --arg l "${OWNER_LABEL}" '.metadata.labels[$l] // "<none>"' <<<"${json}"
}

# Reads each (resource, namespace) group with one request rather than one per object:
# every kubectl call may run a credential plugin.
check_ownership() {
  local file inventory="" resource namespace names owners foreign
  for file in "${CONFIG_FILES[@]}" "${JOB_FILES[@]}"; do
    inventory+="$(objects_in "${file}")"$'\n'
  done
  while IFS='|' read -r resource namespace names; do
    [ -n "${resource}" ] || continue
    # shellcheck disable=SC2086 # names is a space-separated list of object names
    if [ -n "${namespace}" ]; then
      owners="$(kc get "${resource}" ${names} -n "${namespace}" --ignore-not-found -o json)" || fail "cannot read ${resource} in ${namespace}"
    else
      owners="$(kc get "${resource}" ${names} --ignore-not-found -o json)" || fail "cannot read ${resource}"
    fi
    [ -n "${owners}" ] || continue
    foreign="$(jq -r --arg l "${OWNER_LABEL}" --arg p "${PREFIX}" \
      '(if .kind == "List" then .items[] else . end) | select((.metadata.labels[$l] // "<none>") != $p)
       | "\(.metadata.namespace // "" | if . == "" then "" else . + "/" end)\(.metadata.name) (\($l)=\(.metadata.labels[$l] // "<none>"))"' <<<"${owners}")"
    [ -z "${foreign}" ] || fail "Refusing to adopt existing ${resource} ${foreign//$'\n'/, }; choose another --prefix"
    foreign="$(jq -r '(if .kind == "List" then .items[] else . end) | select(.metadata.deletionTimestamp != null) | .metadata.name' <<<"${owners}")"
    [ -z "${foreign}" ] || fail "${resource} ${foreign//$'\n'/, } is still being deleted by an earlier 'down'; wait for it to go and run 'up' again"
  done < <(awk -F'|' 'NF { key = $1 "|" $3; names[key] = names[key] " " $2 } END { for (k in names) print k "|" substr(names[k], 2) }' <<<"${inventory}")
}

# Creates the objects of a manifest file that do not exist yet. Existing ones were
# already proven owned by check_ownership and are left alone, because `up` changes
# some of them on purpose. Nothing is ever applied over an existing object: if
# someone else creates a same-named object after check_ownership read it, the
# create fails with AlreadyExists instead of adopting it.
create_missing() {
  local file="$1" objects groups resource namespace names existing missing foreign
  objects="$(render "${file}" | kc create --dry-run=client -o json -f - | jq -cs '[.[] | if .kind == "List" then .items[] else . end]')" || \
    fail "cannot parse ${file}"
  groups="$(jq -r "[.[] | ${identity_jq}] | group_by(.[0], .[2]) | .[] | [.[0][0], .[0][2], (map(.[1]) | join(\" \"))] | join(\"|\")" <<<"${objects}")"
  missing='[]'
  while IFS='|' read -r resource namespace names; do
    [ -n "${resource}" ] || continue
    # shellcheck disable=SC2086 # names is a space-separated list of object names
    existing="$(kc get "${resource}" ${names} ${namespace:+-n "${namespace}"} --ignore-not-found -o json)" || fail "cannot read ${resource} ${namespace}"
    existing="$(jq -c 'if . == null then [] elif .kind == "List" then .items else [.] end' <<<"${existing:-null}")"
    # Re-checked here, not only in check_ownership: anything found now is reused, so it must be ours.
    foreign="$(jq -r --arg l "${OWNER_LABEL}" --arg p "${PREFIX}" \
      '.[] | select(.metadata.labels[$l] != $p or .metadata.deletionTimestamp != null) | .metadata.name' <<<"${existing}")"
    [ -z "${foreign}" ] || fail "${resource} ${foreign//$'\n'/, } appeared or changed since the ownership check; not reusing it"
    existing="$(jq -c '[.[].metadata.name]' <<<"${existing}")"
    missing="$(jq -c --argjson objects "${objects}" --argjson existing "${existing}" --arg r "${resource}" --arg ns "${namespace}" \
      ". + [\$objects[] | select((${identity_jq} | .[0] == \$r and .[2] == \$ns) and (.metadata.name | IN(\$existing[]) | not))]" <<<"${missing}")"
  done <<<"${groups}"
  [ "$(jq 'length' <<<"${missing}")" -gt 0 ] || return 0
  jq '{apiVersion: "v1", kind: "List", items: .}' <<<"${missing}" | kc create -f - >/dev/null || fail "creating objects from ${file} failed"
}

# --- reading Workloads ---------------------------------------------------------------
# Readers return 0 (yes), 1 (no) or 2 (could not read), so a failed read is never
# mistaken for an answer. wait_for retries on 1 and 2; verify reports 2 separately.

# list_json <resource> <namespace>: a namespace's list; verify reads each once.
list_json() {
  local file
  if [ -z "${READ_CACHE}" ]; then kc get "$1" -n "$2" -o json; return; fi
  file="${READ_CACHE}/$1.$2.json"
  [ -s "${file}" ] || kc get "$1" -n "$2" -o json >"${file}" || { rm -f "${file}"; return 2; }
  cat "${file}"
}

# Prints the Workload of a fixture Job, matched by the Job's UID and controller
# reference, or "{}" while Kueue has not created it yet. Returns 3 when the Job
# itself is missing or not this run's.
workload_json() { # workload_json <namespace> <job>
  local jobs job uid list
  jobs="$(list_json jobs.batch "$1")" || return 2
  job="$(jq -c --arg n "$2" 'first(.items[] | select(.metadata.name == $n)) // empty' <<<"${jobs}")" || return 2
  [ -n "${job}" ] || return 3
  [ "$(jq -r --arg l "${OWNER_LABEL}" '.metadata.labels[$l] // ""' <<<"${job}")" = "${PREFIX}" ] || return 3
  uid="$(jq -r '.metadata.uid' <<<"${job}")"
  list="$(list_json workloads.kueue.x-k8s.io "$1")" || return 2
  jq -c --arg uid "${uid}" \
    'first(.items[] | select(any(.metadata.ownerReferences[]?; .uid == $uid and .kind == "Job" and .controller == true))) // {}' <<<"${list}" || return 2
}

wl_is() { # wl_is <namespace> <job> <jq predicate over the Workload>
  local json rc=0
  json="$(workload_json "$1" "$2")" || rc=$?
  [ "${rc}" -eq 0 ] || return "${rc}"
  [ "${json}" != "{}" ] || return 1
  pred "$3" "${json}"
}

# pred <jq predicate> <json>: 0 true, 1 false, 4 the predicate itself failed (an
# object shape this script doesn't expect, e.g. after a Kueue API change).
pred() {
  local rc=0
  jq -e "$1" <<<"$2" >/dev/null 2>&1 || rc=$?
  case "${rc}" in 0) return 0 ;; 1) return 1 ;; *) return 4 ;; esac
}

# known <reader...>: the reader's yes (0) or no (1); stops `up` when the cluster
# couldn't be read, so a failed read never decides a state transition.
known() {
  local rc=0
  "$@" || rc=$?
  case "${rc}" in
    0|1) return "${rc}" ;;
    3) fail "a fixture Job is missing or not this prefix's (${*:2:2}); run '$0 down' and 'up' again" ;;
    4) fail "unexpected object shape for: $* (a different Kueue API?)" ;;
    *) fail "could not read the cluster for: $*" ;;
  esac
}

cond_pred() { # cond_pred <type> <status> [reason]
  printf 'any(.status.conditions[]?; .type == "%s" and .status == "%s"%s)' "$1" "$2" "${3:+ and .reason == \"$3\"}"
}

has_cond() { wl_is "$1" "$2" "$(cond_pred "$3" "$4" "${5:-}")"; }

has_workload() { wl_is "$1" "$2" 'true'; }

# True only when the Workload was read and holds no quota reservation.
released() { wl_is "$1" "$2" "($(cond_pred QuotaReserved True)) | not"; }

cq_is() { # cq_is <clusterqueue> <jq predicate>
  local json
  json="$(kc get clusterqueues.kueue.x-k8s.io "$1" -o json)" || return 2
  pred "$2" "${json}"
}

lq_is() { # lq_is <namespace> <localqueue> <jq predicate>
  local json
  json="$(kc get localqueues.kueue.x-k8s.io "$2" -n "$1" -o json)" || return 2
  pred "$3" "${json}"
}

wait_for() { # wait_for <description> <command...>
  local desc="$1" deadline=$((SECONDS + WAIT_SECONDS))
  shift
  local rc
  while :; do
    rc=0
    "$@" || rc=$?
    [ "${rc}" -ne 0 ] || break
    [ "${rc}" -ne 3 ] || fail "a fixture Job is missing or not this prefix's (${*:2:2}); run '$0 down' and 'up' again"
    [ "${rc}" -ne 4 ] || fail "unexpected object shape while waiting for ${desc} (a different Kueue API?)"
    if [ "${SECONDS}" -ge "${deadline}" ]; then
      fail "Timed out after ${WAIT_SECONDS}s waiting for ${desc}. Inspect with: $0 status"
    fi
    sleep 3
  done
  ok "${desc}"
}

# --- writes that stand in for other controllers ------------------------------------------

# write_owned <resource> <namespace|""> <name, or the Job for a Workload> <spec|status> <jq program> [jq --arg ...]
# Every write goes through here. It reads the object, checks it is still this
# prefix's (a Workload through its owning Job), builds a merge patch from what it
# read, and sends it with that read's resourceVersion: if the object changed or
# was replaced since, the write is refused instead of landing on it. Retries on
# conflict only. A program that yields null means "already done" on that snapshot,
# and nothing is written.
write_owned() {
  local resource="$1" namespace="$2" target="$3" sub="$4" program="$5" attempt json name patch out
  local args=()
  shift 5
  [ "${sub}" = status ] && args+=(--subresource=status)
  [ -z "${namespace}" ] || args+=(-n "${namespace}")
  for attempt in 1 2 3; do
    if [ "${resource}" = workloads.kueue.x-k8s.io ]; then
      json="$(READ_CACHE='' workload_json "${namespace}" "${target}")" || fail "cannot read the Workload of Job ${namespace}/${target}"
      [ "${json}" != "{}" ] || fail "no Workload for Job ${namespace}/${target}"
    else
      json="$(kc get "${resource}" "${target}" ${namespace:+-n "${namespace}"} -o json)" || fail "cannot read ${resource} ${target}"
      jq -e --arg l "${OWNER_LABEL}" --arg p "${PREFIX}" '.metadata.labels[$l] == $p and .metadata.deletionTimestamp == null' <<<"${json}" >/dev/null || \
        fail "${resource} ${target} is not this prefix's (or is being deleted); not writing to it"
    fi
    name="$(jq -r '.metadata.name' <<<"${json}")"
    # Merge patches replace lists whole, so programs carry every list entry they keep.
    patch="$(jq -c "$@" "(${program}) as \$p | if \$p == null then null else {metadata: {resourceVersion: .metadata.resourceVersion}} + \$p end" <<<"${json}")" || \
      fail "cannot build the patch for ${resource} ${name}"
    [ "${patch}" != null ] || return 0
    out="$(kc patch "${resource}" "${name}" ${args[@]+"${args[@]}"} --type=merge -p "${patch}" 2>&1 >/dev/null)" && return 0
    [[ "${out}" == *"the object has been modified"* || "${out}" == *Conflict* ]] || fail "writing ${resource} ${name} failed: ${out}"
    sleep 1
  done
  fail "writing ${resource} ${name} kept conflicting with other updates: ${out}"
}

# The script is the controller for ${PREFIX}-check: it writes one check state,
# keeping every other entry and the fields Kueue keeps in its own (retryCount).
# The write is skipped when the same snapshot shows it already happened, so a
# state Kueue finished handling between earlier reads and this write is never
# requested twice: Ready once Ready, Rejected once rejected or deactivated, Retry
# once Kueue counted a retry or one is outstanding.
set_check() { # set_check <namespace> <job> <Ready|Retry|Rejected> <message>
  write_owned workloads.kueue.x-k8s.io "$1" "$2" status \
    '(first(.status.admissionChecks[]? | select(.name == $n)) // {name: $n}) as $c
     | if $c.state == $s
          or ($s == "Rejected" and .spec.active == false)
          or ($s == "Retry" and ($c.retryCount // 0) >= 1)
       then null
       else {status: {admissionChecks: ([.status.admissionChecks[]? | select(.name != $n)]
         + [$c + {state: $s, message: $m, lastTransitionTime: $t}])}}
       end' \
    --arg n "${PREFIX}-check" --arg s "$3" --arg m "$4" --arg t "$(now)"
}

check_state_is() { # check_state_is <namespace> <job> <state>
  wl_is "$1" "$2" "any(.status.admissionChecks[]?; .name == \"${PREFIX}-check\" and .state == \"$3\")"
}

retried() { wl_is "$1" "$2" '[.status.admissionChecks[]? | .retryCount // 0] | (max // 0) >= 1'; }

# Answers Ready once the Workload holds quota, unless it already did.
ready_when_reserved() { # ready_when_reserved <namespace> <job>
  if known check_state_is "$1" "$2" Ready; then return 0; fi
  wait_for "${1#"${PREFIX}"-}/$2 reserves quota" has_cond "$1" "$2" QuotaReserved True
  known check_state_is "$1" "$2" Ready || set_check "$1" "$2" Ready "Fixture check controller: ready"
}

# --- up ---------------------------------------------------------------------------------

cmd_up() {
  preflight
  report_kueue_version
  step "Checking ownership of every object this run would create (prefix ${PREFIX})"
  check_ownership
  ok "nothing in the way belongs to anyone else"
  local existing
  existing="$(owner_of namespace "$(ns quota)" "")"
  if [ -n "${existing}" ]; then
    warn "fixtures for prefix ${PREFIX} already exist, so this run resumes them; if they aren't yours, use another --prefix"
  fi

  step "Creating namespaces, ResourceFlavors, Cohort, priority classes, AdmissionCheck, queues and viewer RBAC"
  local file
  for file in "${CONFIG_FILES[@]}"; do create_missing "${file}"; done
  ok "configuration in place"

  step "Reporting ${PREFIX}-check Active, as its controller would"
  write_owned admissionchecks.kueue.x-k8s.io "" "${PREFIX}-check" status \
    'if any(.status.conditions[]?; .type == "Active" and .status == "True" and .observedGeneration == .metadata.generation) then null
     else {status: {conditions: [{type: "Active", status: "True", reason: "Active", message: "Driven by scripts/kueue-scenarios.sh", lastTransitionTime: $t, observedGeneration: .metadata.generation}]}} end' \
    --arg t "$(now)"
  wait_for "ClusterQueue ${PREFIX}-busy active" cq_is "${PREFIX}-busy" "$(cond_pred Active True)"

  step "Submitting the first Jobs"
  create_missing 30-jobs-first.yaml
  local deadline=$((SECONDS + 60))
  until known has_workload "$(ns quota)" fits; do
    [ "${SECONDS}" -lt "${deadline}" ] || \
      fail "Kueue created no Workload for Job $(ns quota)/fits within 60s: is Kueue running with its batch/job integration enabled?"
    sleep 3
  done
  wait_for "quota/fits admitted" has_cond "$(ns quota)" fits Admitted True
  wait_for "lq-denied/admitted admitted" has_cond "$(ns lq-denied)" admitted Admitted True
  # Kueue never recreates the Workload of a finished Job, so these can't be repaired in place.
  wait_for "lifecycle/finished finished (if this times out, run down and up)" has_cond "$(ns lifecycle)" finished Finished True Succeeded
  wait_for "lifecycle/failed finished" has_cond "$(ns lifecycle)" failed Finished True Failed

  step "Busy queue: admit on-spare on ${PREFIX}-spare, then remove that flavor from the queue"
  if known cq_is "${PREFIX}-busy" "any(.spec.resourceGroups[].flavors[]; .name == \"${PREFIX}-spare\")"; then
    ready_when_reserved "$(ns busy)" on-spare
    wait_for "busy/on-spare admitted" has_cond "$(ns busy)" on-spare Admitted True
    write_owned clusterqueues.kueue.x-k8s.io "" "${PREFIX}-busy" spec \
      '{spec: {resourceGroups: (.spec.resourceGroups | map(.flavors |= map(select(.name != $f))))}}' --arg f "${PREFIX}-spare"
  fi
  ok "${PREFIX}-spare is not in ${PREFIX}-busy's spec"

  step "Deactivating lifecycle/deactivated"
  if ! known wl_is "$(ns lifecycle)" deactivated '.spec.active == false'; then
    wait_for "lifecycle/deactivated admitted" has_cond "$(ns lifecycle)" deactivated Admitted True
    write_owned workloads.kueue.x-k8s.io "$(ns lifecycle)" deactivated spec '{spec: {active: false}}'
  fi
  wait_for "lifecycle/deactivated evicted" has_cond "$(ns lifecycle)" deactivated Evicted True

  step "Drain queue: a PodsReadyTimeout eviction, then HoldAndDrain"
  local policy
  policy="$(kc get clusterqueues.kueue.x-k8s.io "${PREFIX}-drain" -o jsonpath='{.spec.stopPolicy}')" || fail "cannot read ClusterQueue ${PREFIX}-drain"
  if [ "${policy}" = "None" ] || [ -z "${policy}" ]; then
    wait_for "drain/drained admitted" has_cond "$(ns drain)" drained Admitted True
    wait_for "drain/pods-ready-timeout admitted" has_cond "$(ns drain)" pods-ready-timeout Admitted True
    # Hold first so the Workload evicted next cannot be admitted again.
    write_owned clusterqueues.kueue.x-k8s.io "" "${PREFIX}-drain" spec '{spec: {stopPolicy: "Hold"}}'
    policy=Hold
  fi
  if ! known has_cond "$(ns drain)" pods-ready-timeout Evicted True PodsReadyTimeout; then
    [ "${policy}" = "Hold" ] || fail "${PREFIX}-drain is ${policy} but pods-ready-timeout was never evicted; run '$0 down' and start again"
    # waitForPodsReady is a controller-wide setting this script must not change, so it
    # writes the condition that feature writes. Kueue's eviction handling is real.
    write_owned workloads.kueue.x-k8s.io "$(ns drain)" pods-ready-timeout status \
      '{status: {conditions: ([.status.conditions[]? | select(.type != "Evicted")]
        + [{type: "Evicted", status: "True", reason: "PodsReadyTimeout", message: "Exceeded the PodsReady timeout (written by scripts/kueue-scenarios.sh)", lastTransitionTime: $t, observedGeneration: .metadata.generation}])}}' \
      --arg t "$(now)"
  fi
  wait_for "drain/pods-ready-timeout released its quota" released "$(ns drain)" pods-ready-timeout
  if [ "${policy}" != "HoldAndDrain" ]; then
    write_owned clusterqueues.kueue.x-k8s.io "" "${PREFIX}-drain" spec '{spec: {stopPolicy: "HoldAndDrain"}}'
  fi
  wait_for "drain/drained evicted" has_cond "$(ns drain)" drained Evicted True ClusterQueueStopped

  step "Submitting the second Jobs"
  local preemptor
  preemptor="$(owner_of job.batch high-priority "$(ns lifecycle)")"
  if [ -z "${preemptor}" ]; then
    wait_for "lifecycle/low-priority admitted before its preemptor arrives" has_cond "$(ns lifecycle)" low-priority Admitted True
  fi
  create_missing 40-jobs-second.yaml
  wait_for "lifecycle/low-priority preempted" has_cond "$(ns lifecycle)" low-priority Evicted True Preempted
  wait_for "lifecycle/high-priority admitted" has_cond "$(ns lifecycle)" high-priority Admitted True

  step "Answering ${PREFIX}-check for the busy queue"
  local job
  for job in borrower-1 borrower-2; do ready_when_reserved "$(ns busy)" "${job}"; done
  for job in hidden-1 hidden-2; do ready_when_reserved "$(ns busy-hidden)" "${job}"; done
  wait_for "busy/check-pending reserves quota" has_cond "$(ns busy)" check-pending QuotaReserved True

  if ! known wl_is "$(ns busy)" check-rejected '.spec.active == false' && ! known check_state_is "$(ns busy)" check-rejected Rejected; then
    wait_for "busy/check-rejected reserves quota" has_cond "$(ns busy)" check-rejected QuotaReserved True
    set_check "$(ns busy)" check-rejected Rejected "Fixture check controller: rejected on purpose"
  fi
  wait_for "busy/check-rejected deactivated by Kueue" wl_is "$(ns busy)" check-rejected '.spec.active == false'

  # retryCount is Kueue's own record of a handled Retry, so it is the resume marker:
  # Retry is written at most once per Workload.
  if ! known retried "$(ns busy)" check-retry && ! known check_state_is "$(ns busy)" check-retry Retry; then
    wait_for "busy/check-retry reserves quota" has_cond "$(ns busy)" check-retry QuotaReserved True
    set_check "$(ns busy)" check-retry Retry "Fixture check controller: retry requested once"
  fi
  wait_for "busy/check-retry requeued once by Kueue" retried "$(ns busy)" check-retry
  wait_for "busy/check-retry reserves quota again" has_cond "$(ns busy)" check-retry QuotaReserved True
  wait_for "busy/borrower-1 admitted" has_cond "$(ns busy)" borrower-1 Admitted True
  wait_for "busy/borrower-2 admitted" has_cond "$(ns busy)" borrower-2 Admitted True

  step "Done. Run '$0 verify' to check every end state."
}

# --- verify -----------------------------------------------------------------------------

expect() { # expect <description> <command...>
  local desc="$1" rc=0
  shift
  "$@" || rc=$?
  if [ "${rc}" -eq 0 ]; then
    ok "${desc}"
  else
    FAILURES=$((FAILURES + 1))
    case "${rc}" in
      2) desc+=" (could not read)" ;;
      3) desc+=" (its Job is missing)" ;;
      4) desc+=" (unexpected object shape)" ;;
    esac
    printf "${C_RED}    ✗ %s${C_RESET}\n" "${desc}"
  fi
}

msg_matches() { # msg_matches <namespace> <job> <type> <regex>
  wl_is "$1" "$2" "any(.status.conditions[]?; .type == \"$3\" and (.message // \"\" | test(\"$4\")))"
}

counts_are() { # counts_are <clusterqueue> <pending> <reserving> <admitted>
  cq_is "$1" "(.status.pendingWorkloads // 0) == $2 and (.status.reservingWorkloads // 0) == $3 and (.status.admittedWorkloads // 0) == $4"
}

lq_counts_are() { # lq_counts_are <namespace> <localqueue> <pending> <reserving> <admitted>
  lq_is "$1" "$2" "(.status.pendingWorkloads // 0) == $3 and (.status.reservingWorkloads // 0) == $4 and (.status.admittedWorkloads // 0) == $5"
}

no_workloads_in() { # no_workloads_in <namespace>
  local json
  json="$(kc get workloads.kueue.x-k8s.io -n "$1" -o json)" || return 2
  jq -e '.items | length == 0' <<<"${json}" >/dev/null
}

only_lq_for() { # only_lq_for <clusterqueue> <namespace/name>
  local json
  json="$(kc get localqueues.kueue.x-k8s.io -A -o json)" || return 2
  jq -e --arg cq "$1" --arg want "$2" '[.items[] | select(.spec.clusterQueue == $cq) | "\(.metadata.namespace)/\(.metadata.name)"] == [$want]' <<<"${json}" >/dev/null
}

# Asks the API server, as both viewer identities, whether they may do something.
viewer_can() { # viewer_can <yes|no> <verb> <resource> <namespace>
  local subject answer
  for subject in "system:serviceaccount:$(ns viewer):viewer" "${PREFIX}-viewer"; do
    # can-i exits 1 for "no" as well as for errors, so read the answer it prints.
    answer="$(kc auth can-i "$2" "$3" -n "$4" --as "${subject}" 2>/dev/null)" || true
    case "${answer}" in
      yes|no) ;;
      "no - "*) answer=no ;; # kubectl adds the authorizer's reason to some answers
      *) return 2 ;;
    esac
    [ "${answer}" = "$1" ] || return 1
  done
}

cmd_verify() {
  preflight
  report_kueue_version
  READ_CACHE="$(mktemp -d)"
  trap 'rm -rf -- "${READ_CACHE}"' EXIT
  local q busy hidden lc drain
  q="$(ns quota)"; busy="$(ns busy)"; hidden="$(ns busy-hidden)"; lc="$(ns lifecycle)"; drain="$(ns drain)"

  step "1. Pending on quota (${PREFIX}-quota)"
  expect "fits admitted" has_cond "${q}" fits Admitted True
  expect "waits-for-quota pending on unused quota" msg_matches "${q}" waits-for-quota QuotaReserved "insufficient unused quota"
  expect "too-big pending beyond the queue's maximum capacity" msg_matches "${q}" too-big QuotaReserved "maximum capacity"
  expect "queue counts: pending 2, reserving 1, admitted 1" counts_are "${PREFIX}-quota" 2 1 1

  step "2. Busy ClusterQueue (${PREFIX}-busy)"
  expect "in cohort ${PREFIX}-cohort and active" cq_is "${PREFIX}-busy" ".spec.cohortName == \"${PREFIX}-cohort\" and ($(cond_pred Active True))"
  expect "borrows CPU from the cohort" cq_is "${PREFIX}-busy" 'any(.status.flavorsReservation[]?.resources[]; .name == "cpu" and (.borrowed // "0") != "0")'
  expect "memory has an explicit borrowingLimit of 0" cq_is "${PREFIX}-busy" 'any(.spec.resourceGroups[].flavors[].resources[]; .name == "memory" and .borrowingLimit == "0")'
  expect "lender ${PREFIX}-lender runs nothing itself" counts_are "${PREFIX}-lender" 0 0 0
  expect "queue counts: pending 1, reserving 7, admitted 5" counts_are "${PREFIX}-busy" 1 7 5
  expect "borrower-1 admitted" has_cond "${busy}" borrower-1 Admitted True
  expect "borrower-2 admitted" has_cond "${busy}" borrower-2 Admitted True
  expect "check-pending: check still Pending" check_state_is "${busy}" check-pending Pending
  expect "check-pending: quota reserved, not admitted" wl_is "${busy}" check-pending \
    "($(cond_pred QuotaReserved True)) and ($(cond_pred Admitted True) | not)"
  expect "check-retry: requeued once (retryCount 1), quota reserved again" wl_is "${busy}" check-retry \
    "any(.status.admissionChecks[]?; .retryCount == 1) and ($(cond_pred QuotaReserved True))"
  expect "check-rejected: deactivated by Kueue" wl_is "${busy}" check-rejected \
    ".spec.active == false and ($(cond_pred Evicted True DeactivatedDueToAdmissionCheck))"
  expect "${PREFIX}-spare is no longer in the queue's spec" cq_is "${PREFIX}-busy" "all(.spec.resourceGroups[].flavors[]; .name != \"${PREFIX}-spare\")"
  expect "on-spare stays admitted on the removed flavor" wl_is "${busy}" on-spare \
    "($(cond_pred Admitted True)) and all(.status.admission.podSetAssignments[].flavors[]; . == \"${PREFIX}-spare\")"
  local entry=0
  cq_is "${PREFIX}-busy" "all(.spec.resourceGroups[].flavors[]; .name != \"${PREFIX}-spare\") and any(.status.flavorsReservation[]?; .name == \"${PREFIX}-spare\")" || entry=$?
  if [ "${entry}" -eq 2 ]; then
    FAILURES=$((FAILURES + 1)); printf "${C_RED}    ✗ %s${C_RESET}\n" "removed-flavor status entry (could not read)"
  elif [ "${entry}" -eq 0 ]; then
    warn "this Kueue keeps a ClusterQueue status entry for the removed flavor (v0.18.0 drops it)"
  else
    ok "no ClusterQueue status entry for the removed flavor (Kueue v0.18.0 behavior)"
  fi
  expect "hidden-1 admitted in ${hidden}" has_cond "${hidden}" hidden-1 Admitted True
  expect "hidden-2 admitted in ${hidden}" has_cond "${hidden}" hidden-2 Admitted True
  expect "hidden-pending pending beyond what the cohort can lend" msg_matches "${hidden}" hidden-pending QuotaReserved "maximum capacity"
  expect "${hidden}/busy counts: pending 1, reserving 2, admitted 2" lq_counts_are "${hidden}" busy 1 2 2

  step "3. Stopped with Hold (${PREFIX}-held)"
  expect "ClusterQueue Active=False/Stopped" cq_is "${PREFIX}-held" "$(cond_pred Active False Stopped)"
  expect "LocalQueue Active=False/ClusterQueueIsInactive" lq_is "$(ns held)" held "$(cond_pred Active False ClusterQueueIsInactive)"
  expect "held is inadmissible" has_cond "$(ns held)" held QuotaReserved False Inadmissible

  step "4. Missing ResourceFlavor (${PREFIX}-missing-flavor)"
  expect "ClusterQueue Active=False/FlavorNotFound" cq_is "${PREFIX}-missing-flavor" "$(cond_pred Active False FlavorNotFound)"
  expect "blocked is inadmissible" has_cond "$(ns missing-flavor)" blocked QuotaReserved False Inadmissible

  step "6. LocalQueue whose Workloads the viewer cannot list ($(ns lq-denied))"
  expect "admitted admitted" has_cond "$(ns lq-denied)" admitted Admitted True
  expect "pending pending" has_cond "$(ns lq-denied)" pending QuotaReserved False Pending

  step "7. Confirmed empty (${PREFIX}-empty)"
  expect "ClusterQueue has no Workloads" counts_are "${PREFIX}-empty" 0 0 0
  expect "$(ns empty)/empty is the only LocalQueue pointing at it" only_lq_for "${PREFIX}-empty" "$(ns empty)/empty"
  expect "no Workloads in $(ns empty)" no_workloads_in "$(ns empty)"

  step "Lifecycle and evictions"
  expect "finished: Finished/Succeeded" has_cond "${lc}" finished Finished True Succeeded
  expect "failed: Finished/Failed" has_cond "${lc}" failed Finished True Failed
  expect "low-priority: preempted within its ClusterQueue and pending" wl_is "${lc}" low-priority \
    "($(cond_pred Evicted True Preempted)) and ($(cond_pred Preempted True InClusterQueue)) and ($(cond_pred QuotaReserved True) | not)"
  expect "high-priority admitted" has_cond "${lc}" high-priority Admitted True
  expect "deactivated: spec.active=false, Evicted/Deactivated" wl_is "${lc}" deactivated ".spec.active == false and ($(cond_pred Evicted True Deactivated))"
  expect "no-localqueue: its LocalQueue doesn't exist" msg_matches "${lc}" no-localqueue QuotaReserved "LocalQueue .* doesn't exist"
  expect "not-eligible: namespace outside the queue's selector" msg_matches "$(ns ineligible)" not-eligible QuotaReserved "namespace doesn't match"
  expect "drained: Evicted/ClusterQueueStopped" has_cond "${drain}" drained Evicted True ClusterQueueStopped
  expect "pods-ready-timeout: Evicted/PodsReadyTimeout, no reservation" wl_is "${drain}" pods-ready-timeout \
    "($(cond_pred Evicted True PodsReadyTimeout)) and ($(cond_pred QuotaReserved True) | not)"

  step "Restricted viewer (ServiceAccount $(ns viewer)/viewer and user ${PREFIX}-viewer)"
  expect "can list Workloads in ${busy}" viewer_can yes list workloads.kueue.x-k8s.io "${busy}"
  expect "cannot list Workloads in ${hidden}" viewer_can no list workloads.kueue.x-k8s.io "${hidden}"
  expect "cannot list LocalQueues in ${hidden}" viewer_can no list localqueues.kueue.x-k8s.io "${hidden}"
  expect "can list LocalQueues in $(ns lq-denied)" viewer_can yes list localqueues.kueue.x-k8s.io "$(ns lq-denied)"
  expect "cannot list Workloads in $(ns lq-denied)" viewer_can no list workloads.kueue.x-k8s.io "$(ns lq-denied)"
  expect "can list Workloads in $(ns empty)" viewer_can yes list workloads.kueue.x-k8s.io "$(ns empty)"

  [ "${FAILURES}" -eq 0 ] || fail "${FAILURES} expectation(s) failed"
  step "Every scenario is in its expected state"
}

# --- verify-radar --------------------------------------------------------------------------

radar_get() { # radar_get <path>: prints the body, then the HTTP status on its own line
  local headers=()
  [ "${IDENTITY}" = "user" ] && headers+=(-H "X-Forwarded-User: ${PREFIX}-viewer")
  curl -sS --connect-timeout 5 --max-time 30 ${headers[@]+"${headers[@]}"} -w '\n%{http_code}' "${RADAR_URL}$1"
}

# radar_settled_get <path>: like radar_get, but repeats an answer whose action is
# "retry", which asks for exactly that; Radar gives them while it is still
# settling what it can read, especially right after startup.
radar_settled_get() {
  local out attempt
  for attempt in 1 2 3 4 5 6; do
    out="$(radar_get "$1")" || return 1
    jq -e 'any(.coverage.gaps[]?; .action == "retry")' <<<"${out%$'\n'*}" >/dev/null 2>&1 || break
    sleep 5
  done
  printf '%s' "${out}"
}

radar_expect() { # radar_expect <description> <path> <HTTP status> <jq predicate over the body>
  local out status body
  if ! out="$(radar_settled_get "$2")"; then
    FAILURES=$((FAILURES + 1)); printf "${C_RED}    ✗ %s (Radar unreachable)${C_RESET}\n" "$1"; return
  fi
  status="${out##*$'\n'}"; body="${out%$'\n'*}"
  if [ "${status}" = "$3" ] && jq -e --argjson uids "${JOB_UIDS}" "$4" <<<"${body}" >/dev/null 2>&1; then
    ok "$1"
  else
    FAILURES=$((FAILURES + 1))
    printf "${C_RED}    ✗ %s (HTTP %s: %s)${C_RESET}\n" "$1" "${status}" \
      "$(jq -c '{error, error_code, coverage} | with_entries(select(.value != null))' <<<"${body}" 2>/dev/null | cut -c1-400)"
  fi
}

wl_path() { printf '/api/resources/workloads?group=kueue.x-k8s.io&namespaces=%s' "$1"; }

# Prints a JSON object of this run's Job UIDs by "<namespace>/<name>", read from the cluster.
job_uids() {
  local namespace json out='{}'
  for namespace in quota busy busy-hidden held lq-denied empty; do
    json="$(kc get jobs.batch -n "$(ns "${namespace}")" -l "${OWNER_LABEL}=${PREFIX}" -o json)" || fail "cannot read Jobs in $(ns "${namespace}")"
    out="$(jq -c --argjson acc "${out}" '$acc + ([.items[] | {key: "\(.metadata.namespace)/\(.metadata.name)", value: .metadata.uid}] | from_entries)' <<<"${json}")"
  done
  printf '%s' "${out}"
}

# jq predicate: the items are exactly the Workloads that Kueue made for these Jobs
# (namespace/name), matched by the Jobs' UIDs on the cluster as a controller reference.
jobs_are() {
  local wanted
  wanted="$(printf '%s\n' "$@" | jq -R . | jq -sc .)"
  # Every item counts: one without a controlling Job maps to null and breaks equality.
  printf '([.items[] | first(.metadata.ownerReferences[]? | select(.kind == "Job" and .controller == true) | .uid) // null] | sort) == ([%s | .[] | $uids[.]] | sort) and all(%s[]; $uids[.] != null)' "${wanted}" "${wanted}"
}

cmd_verify_radar() {
  check_args
  command -v curl >/dev/null || fail "curl not found"
  case "${IDENTITY}" in full|viewer|user) ;; *) fail "--identity must be full, viewer or user" ;; esac
  curl -fsS --connect-timeout 5 --max-time 10 "${RADAR_URL}/api/health" >/dev/null || fail "Radar is not reachable at ${RADAR_URL}"
  local busy hidden denied empty complete busy_jobs
  busy="$(ns busy)"; hidden="$(ns busy-hidden)"; denied="$(ns lq-denied)"; empty="$(ns empty)"
  complete='.coverage.status == "complete" and (.coverage.gaps | length == 0)'
  JOB_UIDS="$(job_uids)"
  busy_jobs="$(jobs_are "${busy}"/{on-spare,borrower-1,borrower-2,check-pending,check-retry,check-rejected})"

  step "Radar at ${RADAR_URL}, identity: ${IDENTITY}"
  radar_expect "every fixture ClusterQueue is listed" "/api/resources/clusterqueues?group=kueue.x-k8s.io" 200 \
    "${complete} and ([.items[] | select(.metadata.labels[\"${OWNER_LABEL}\"] == \"${PREFIX}\") | .metadata.name] | sort) == ([\"busy\",\"drain\",\"empty\",\"held\",\"lender\",\"lifecycle\",\"lq-denied\",\"missing-flavor\",\"preempt\",\"quota\"] | map(\"${PREFIX}-\" + .) | sort)"
  radar_expect "1: quota Workloads with their QuotaReserved reasons" "$(wl_path "$(ns quota)")" 200 \
    "${complete} and $(jobs_are "$(ns quota)"/{fits,waits-for-quota,too-big}) and any(.items[]; .metadata.ownerReferences[0].name == \"waits-for-quota\" and any(.status.conditions[]; .type == \"QuotaReserved\" and .reason == \"Pending\"))"
  radar_expect "3: held Workload inadmissible" "$(wl_path "$(ns held)")" 200 \
    "${complete} and $(jobs_are "$(ns held)/held") and any(.items[0].status.conditions[]; .reason == \"Inadmissible\")"
  radar_expect "7: confirmed empty (complete, no items)" "$(wl_path "${empty}")" 200 "${complete} and (.items | length == 0)"
  radar_expect "6: the LocalQueue itself is readable" "/api/resources/localqueues?group=kueue.x-k8s.io&namespaces=${denied}" 200 \
    "${complete} and ([.items[].metadata.name] == [\"workloads-hidden\"])"

  case "${IDENTITY}" in
    full)
      radar_expect "2: busy Workloads, both namespaces read" "$(wl_path "${busy},${hidden}")" 200 \
        "${complete} and $(jobs_are "${busy}"/{on-spare,borrower-1,borrower-2,check-pending,check-retry,check-rejected} "${hidden}"/{hidden-1,hidden-2,hidden-pending})"
      radar_expect "6: Workloads readable with full access" "$(wl_path "${denied}")" 200 \
        "${complete} and $(jobs_are "${denied}/admitted" "${denied}/pending")"
      ;;
    viewer)
      radar_expect "2: busy Workloads partial, ${hidden} Radar-forbidden" "$(wl_path "${busy},${hidden}")" 200 \
        ".coverage.status == \"partial\" and .coverage.read.namespaces.names == [\"${busy}\"] and .coverage.gaps == [{code: \"radar_forbidden\", action: \"radar_config\", namespaces: {count: 1, names: [\"${hidden}\"]}}] and ${busy_jobs}"
      radar_expect "2: ${hidden} alone: Radar's identity cannot read it" "$(wl_path "${hidden}")" 503 \
        ".error_code == \"radar_forbidden\" and .coverage.gaps[0].code == \"radar_forbidden\""
      radar_expect "6: Workloads in ${denied}: Radar's identity cannot read them" "$(wl_path "${denied}")" 503 \
        ".error_code == \"radar_forbidden\" and .coverage.gaps[0].code == \"radar_forbidden\""
      ;;
    user)
      radar_expect "2: busy Workloads partial, ${hidden} not visible to the user" "$(wl_path "${busy},${hidden}")" 200 \
        ".coverage.status == \"partial\" and .coverage.read.namespaces.names == [\"${busy}\"] and .coverage.gaps == [{code: \"user_namespace_not_visible\", action: \"change_request\", namespaces: {count: 1, names: [\"${hidden}\"]}}] and ${busy_jobs}"
      # Generic reads of namespaced CRDs are authorized per namespace, not per kind
      # (docs/authentication.md, "Read granularity"); either answer is accepted here.
      local out status
      out="$(radar_settled_get "$(wl_path "${denied}")")" || fail "Radar unreachable"
      status="${out##*$'\n'}"
      if [ "${status}" = "200" ] && jq -e --argjson uids "${JOB_UIDS}" "${complete} and $(jobs_are "${denied}/admitted" "${denied}/pending")" <<<"${out%$'\n'*}" >/dev/null; then
        warn "6: a generic read returns ${denied}'s Workloads to a user who can't list them (namespace-level read granularity); a queue read must check per kind"
      elif [ "${status}" = "403" ] && jq -e '.error_code == "user_forbidden"' <<<"${out%$'\n'*}" >/dev/null 2>&1; then
        ok "6: Workloads in ${denied} are refused to the user"
      else
        FAILURES=$((FAILURES + 1)); printf "${C_RED}    ✗ 6: unexpected read of %s (HTTP %s)${C_RESET}\n" "${denied}" "${status}"
      fi
      ;;
  esac

  [ "${FAILURES}" -eq 0 ] || fail "${FAILURES} Radar expectation(s) failed"
  step "Radar returns the expected scenario reads"
}

# --- status ------------------------------------------------------------------------------

cmd_status() {
  preflight
  kc get clusterqueues.kueue.x-k8s.io -l "${OWNER_LABEL}=${PREFIX}"
  echo
  kc get localqueues.kueue.x-k8s.io -A -l "${OWNER_LABEL}=${PREFIX}"
  echo
  local namespaces namespace
  namespaces="$(kc get ns -l "${OWNER_LABEL}=${PREFIX}" -o jsonpath='{.items[*].metadata.name}')"
  for namespace in ${namespaces}; do
    kc get workloads.kueue.x-k8s.io -n "${namespace}" -o json | jq -r '.items[] |
      [.metadata.namespace, ((.metadata.ownerReferences // [])[0].name // .metadata.name),
       ([.status.conditions[]? | select(.status == "True") | "\(.type)/\(.reason)"] | join(",")),
       ([.status.admissionChecks[]? | "\(.name)=\(.state)"] | join(","))] | @tsv'
  done | column -t -s $'\t'
}

# --- kubeconfig -----------------------------------------------------------------------------

cmd_kubeconfig() {
  check_args
  [ -n "${OUT_PATH}" ] || fail "--out PATH is required"
  local namespace token cluster tmp
  namespace="$(ns viewer)"
  local owner
  owner="$(owner_of serviceaccount viewer "${namespace}")"
  [ "${owner}" = "${PREFIX}" ] || fail "ServiceAccount ${namespace}/viewer is missing or not owned; run '$0 up' first"
  token="$(kc create token viewer -n "${namespace}" --duration="${TOKEN_DURATION}")" || fail "TokenRequest for ${namespace}/viewer failed"
  # The whole cluster entry (server, CA, tls-server-name, proxy-url) carries over;
  # only the user changes. kubectl reads JSON kubeconfigs.
  cluster="$(kc config view --minify --raw --flatten -o json | jq -c '.clusters[0].cluster')" || fail "cannot read the source kubeconfig's cluster"
  tmp="$(mktemp "$(dirname "${OUT_PATH}")/.kueue-viewer.XXXXXX")"
  trap 'rm -f -- "${tmp}"' EXIT
  chmod 600 "${tmp}"
  jq -n --argjson cluster "${cluster}" --arg token "${token}" --arg p "${PREFIX}" --arg ns "$(ns busy)" '{
    apiVersion: "v1", kind: "Config",
    clusters: [{name: ($p + "-cluster"), cluster: $cluster}],
    users: [{name: ($p + "-viewer"), user: {token: $token}}],
    contexts: [{name: ($p + "-viewer"), context: {cluster: ($p + "-cluster"), user: ($p + "-viewer"), namespace: $ns}}],
    "current-context": ($p + "-viewer")}' >"${tmp}"
  mv -f "${tmp}" "${OUT_PATH}"
  trap - EXIT
  ok "wrote ${OUT_PATH} (ServiceAccount ${namespace}/viewer; token valid for ${TOKEN_DURATION} unless the API server caps it)"
}

# --- down ----------------------------------------------------------------------------------

# Prints the names of objects of a kind that carry this prefix's ownership label and
# are named with the prefix; a copied label alone is not enough to be deleted.
owned_names() { # owned_names <resource> [-A]
  kc get "$1" ${2:+"$2"} -l "${OWNER_LABEL}=${PREFIX}" -o json \
    | jq -r --arg p "${PREFIX}-" '.items[] | select((.metadata.namespace // .metadata.name) | startswith($p)) | [.metadata.namespace // "", .metadata.name] | join("|")'
}

# Deletes by name with this prefix's label as a selector, so an object that lost
# the label or was recreated without it by the time kubectl looks it up is left
# alone. kubectl sends no UID precondition, so a swap between its own lookup and
# its delete call is not covered.
delete_owned() { # delete_owned <resource> <name> <namespace|""> [kubectl delete flags]
  local resource="$1" name="$2" namespace="$3"
  shift 3
  kc delete "${resource}" ${namespace:+-n "${namespace}"} -l "${OWNER_LABEL}=${PREFIX}" \
    --field-selector "metadata.name=${name}" --ignore-not-found "$@" >/dev/null
}

cmd_down() {
  check_args
  local resource namespace name rows namespaces=() crd
  step "Deleting everything labeled ${OWNER_LABEL}=${PREFIX} and named ${PREFIX}-…"
  # Jobs first so their Workloads go with them; then the owned namespaces with
  # everything in them; then cluster-scoped objects.
  rows="$(owned_names jobs.batch -A)" || fail "cannot list Jobs"
  while IFS='|' read -r namespace name; do
    [ -n "${name}" ] && delete_owned job.batch "${name}" "${namespace}" --wait=false
  done <<<"${rows}"
  rows="$(owned_names namespaces)" || fail "cannot list namespaces"
  while IFS='|' read -r namespace name; do [ -n "${name}" ] && namespaces+=("${name}"); done <<<"${rows}"
  [ "${#namespaces[@]}" -eq 0 ] || warn "deleting namespaces with everything in them: ${namespaces[*]}"
  if [ "${#namespaces[@]}" -gt 0 ]; then
    for name in "${namespaces[@]}"; do delete_owned namespaces "${name}" "" --wait=false; done
  fi
  local present
  local deleting=()
  for crd in clusterqueues cohorts admissionchecks workloadpriorityclasses resourceflavors; do
    present="$(kc get crd "${crd}.kueue.x-k8s.io" --ignore-not-found -o name)" || fail "cannot read CRD ${crd}.kueue.x-k8s.io"
    [ -n "${present}" ] || continue
    resource="${crd}.kueue.x-k8s.io"
    rows="$(owned_names "${resource}")" || fail "cannot list ${resource}"
    while IFS='|' read -r namespace name; do
      [ -n "${name}" ] || continue
      delete_owned "${resource}" "${name}" "" --wait=false
      deleting+=("${resource}/${name}")
    done <<<"${rows}"
  done
  for resource in clusterrolebindings.rbac.authorization.k8s.io clusterroles.rbac.authorization.k8s.io; do
    rows="$(owned_names "${resource}")" || fail "cannot list ${resource}"
    while IFS='|' read -r namespace name; do
      [ -n "${name}" ] && delete_owned "${resource}" "${name}" ""
    done <<<"${rows}"
  done
  # Kueue finalizes queues and flavors; `up` refuses objects still being deleted,
  # so wait here rather than leave the next `up` to trip over them.
  if [ "${#namespaces[@]}" -gt 0 ] || [ "${#deleting[@]}" -gt 0 ]; then
    local out
    if ! out="$(kc wait --for=delete ${namespaces[@]+"${namespaces[@]/#/namespace/}"} ${deleting[@]+"${deleting[@]}"} --timeout="${WAIT_SECONDS}s" 2>&1 >/dev/null)"; then
      if [[ "${out}" == *"timed out"* ]]; then
        warn "still being deleted after ${WAIT_SECONDS}s: $(kc get ${namespaces[@]+"${namespaces[@]/#/namespace/}"} ${deleting[@]+"${deleting[@]}"} --ignore-not-found -o name 2>/dev/null | tr '\n' ' ')"
        warn "run 'down' again to wait longer; finalizers only clear while Kueue's controller runs"
      else
        warn "could not confirm the deletions: ${out}"
      fi
      exit 2
    fi
  fi
  ok "removed"
}

# --- main ------------------------------------------------------------------------------------

main() {
  COMMAND="${1:-}"
  [ -n "${COMMAND}" ] || { usage; exit 1; }
  shift
  case "${COMMAND}" in -h|--help|help) usage; exit 0 ;; esac
  while [ $# -gt 0 ]; do
    case "$1" in --kubeconfig|--context|--prefix|--fixture|--out|--identity) [ $# -ge 2 ] || fail "$1 needs a value" ;; esac
    case "$1" in
      --kubeconfig) KUBECONFIG_ARG="$2"; shift 2 ;;
      --context) CONTEXT_ARG="$2"; shift 2 ;;
      --prefix) PREFIX="$2"; shift 2 ;;
      --fixture) FIXTURE="$2"; shift 2 ;;
      --out) OUT_PATH="$2"; shift 2 ;;
      --identity) IDENTITY="$2"; shift 2 ;;
      -h|--help) usage; exit 0 ;;
      *) usage; fail "unknown option $1" ;;
    esac
  done
  # make and quoted arguments pass "~" through unexpanded.
  KUBECONFIG_ARG="${KUBECONFIG_ARG/#\~/${HOME}}"
  if [ -z "${KUBECONFIG_ARG}" ] && [ -z "${CONTEXT_ARG}" ]; then
    usage
    fail "pass --kubeconfig or --context so the target cluster is explicit"
  fi

  case "${COMMAND}" in
    up) cmd_up ;;
    verify) cmd_verify ;;
    verify-radar) cmd_verify_radar ;;
    status) cmd_status ;;
    kubeconfig) cmd_kubeconfig ;;
    down) cmd_down ;;
    *) usage; fail "unknown command ${COMMAND}" ;;
  esac
}

# Sourcing the script (scripts/kueue-scenarios/test.sh does) defines the functions only.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
