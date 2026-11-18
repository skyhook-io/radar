#!/usr/bin/env bash
# Fake Agent Substrate control plane + worker Pods for exercising Radar's
# Agent Substrate workspace live. Read scripts/substrate-demo/README.md first:
# it lists every scenario, the exact object names, and what is fake.
#
#   up        Install Substrate's CRDs (if missing), apply the fixtures to
#             namespace radar-substrate-demo, build and start the fake
#             ate-api-server in the background, verify it over TLS, and print
#             the Radar connection settings.
#   down      Stop the fake API; delete the fixture namespace and the
#             fixture's SandboxConfigs. Keeps the CRDs.
#   purge     down, then also delete the Substrate CRDs if this fixture
#             installed them (labelled).
#   restart   Restart only the fake API: re-reads Pod UIDs, and writes the
#             crashed actors' last lines into the worker Pods once they are
#             old enough (it says how long is left). Leaves the cluster alone.
#   status    Is the API up, what does it serve, which fixtures exist.
#   fault     unavailable | unauthenticated | latency <dur> | clear
#             Inject a fault into every call without restarting.
#   help      This message.
#
# Options (before or after the command):
#   --context <kube context>  default $SUBSTRATE_DEMO_CONTEXT, else the one the
#                             last `up` used, else radar-test-nonprod
#   --namespace <namespace>   default $SUBSTRATE_DEMO_NAMESPACE, else the one
#                             the last `up` used, else radar-substrate-demo
#
# Everything created carries radar.skyhook.io/fixture=substrate-demo, and
# nothing without that label is modified or deleted. The fake API listens on
# 127.0.0.1 only.
#
# Env: SUBSTRATE_DEMO_STATE (default ~/.radar/substrate-demo),
#      SUBSTRATE_DEMO_PORT (default 18443), SUBSTRATE_DEMO_CONTEXT,
#      SUBSTRATE_DEMO_NAMESPACE.

set -euo pipefail

SEL="radar.skyhook.io/fixture=substrate-demo"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
CRD_DIR="${SCRIPT_DIR}/substrate-demo/crds"
STATE="${SUBSTRATE_DEMO_STATE:-${HOME}/.radar/substrate-demo}"
PORT="${SUBSTRATE_DEMO_PORT:-18443}"
BIN="${STATE}/fakeapi"
PIDFILE="${STATE}/fakeapi.pid"
LOG="${STATE}/fakeapi.log"
CRDS="workerpools.ate.dev sandboxconfigs.ate.dev csidriverconfigs.ate.dev"

if [ -t 1 ]; then
  C_RESET=$'\e[0m'; C_BLUE=$'\e[34m'; C_GREEN=$'\e[32m'; C_YELLOW=$'\e[33m'; C_RED=$'\e[31m'
else
  C_RESET=""; C_BLUE=""; C_GREEN=""; C_YELLOW=""; C_RED=""
fi
step() { printf "${C_BLUE}==> %s${C_RESET}\n" "$1"; }
ok()   { printf "${C_GREEN}    ✓ %s${C_RESET}\n" "$1"; }
warn() { printf "${C_YELLOW}    ! %s${C_RESET}\n" "$1"; }
fail() { printf "${C_RED}    ✗ %s${C_RESET}\n" "$1"; exit 1; }

CMD=""
ARGS=()
CTX="${SUBSTRATE_DEMO_CONTEXT:-}"
NS="${SUBSTRATE_DEMO_NAMESPACE:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --context) [ $# -ge 2 ] || fail "--context needs a value"; CTX="$2"; shift 2 ;;
    --context=*) CTX="${1#--context=}"; shift ;;
    --namespace) [ $# -ge 2 ] || fail "--namespace needs a value"; NS="$2"; shift 2 ;;
    --namespace=*) NS="${1#--namespace=}"; shift ;;
    *) if [ -z "$CMD" ]; then CMD="$1"; else ARGS+=("$1"); fi; shift ;;
  esac
done
CMD="${CMD:-help}"
if [ -z "$CTX" ] && [ -s "${STATE}/context" ]; then CTX="$(cat "${STATE}/context")"; fi
CTX="${CTX:-radar-test-nonprod}"
if [ -z "$NS" ] && [ -s "${STATE}/namespace" ]; then NS="$(cat "${STATE}/namespace")"; fi
NS="${NS:-radar-substrate-demo}"
fapi() { "$BIN" "$1" --namespace "$NS" "${@:2}"; }

k() { kubectl --context "$CTX" "$@"; }

require_context() {
  kubectl config get-contexts -o name | grep -qx "$CTX" || fail "kube context $CTX not found"
}

# owned_or_absent fails if the object exists without the fixture label.
owned_or_absent() {
  local val
  k get "$@" >/dev/null 2>&1 || return 0
  val="$(k get "$@" -o jsonpath='{.metadata.labels.radar\.skyhook\.io/fixture}')"
  [ "$val" = "substrate-demo" ] || fail "$* exists on $CTX without the fixture label; refusing to touch it"
}

build() {
  mkdir -p "$STATE"
  chmod 700 "$STATE"
  step "Building fake ate-api-server"
  (cd "$REPO_DIR" && go build -o "$BIN" ./scripts/substrate-demo/fakeapi)
  ok "$BIN"
}

api_running() {
  [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null
}

stop_api() {
  if api_running; then
    kill "$(cat "$PIDFILE")" || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do api_running || break; sleep 0.3; done
    ok "stopped fake API (pid $(cat "$PIDFILE"))"
  fi
  rm -f "$PIDFILE"
}

# write_crash_lines appends, once per set of Pods, each crashed actor's last
# lines to the log of the worker Pod that ran it (its container's stdout), so
# reading the crashed actor's last node finds them. The fake API then serves
# the crash times those lines state.
write_crash_lines() {
  local lines="${STATE}/crash-lines.tsv"
  fapi crashlogs --pods "${STATE}/pods.json" --state "$STATE" >"$lines"
  [ -s "$lines" ] || return 0
  for pod in $(cut -f1 "$lines" | sort -u); do
    owned_or_absent pod "$pod" -n "$NS"
    # Passed as an argument: kubectl exec's stdin does not reliably reach the
    # command here, and the lines are a few kilobytes.
    b64="$(awk -F'\t' -v p="$pod" '$1 == p { sub(/^[^\t]*\t/, ""); print }' "$lines" | base64 | tr -d '\n')"
    k exec -n "$NS" "$pod" -- sh -c "echo $b64 | base64 -d > /proc/1/fd/1"
    ok "$pod: wrote the last lines of $(awk -F'\t' -v p="$pod" '$1 == p' "$lines" | grep -o '"ate.actor.name":"[^"]*"' | sort -u | wc -l | tr -d ' ') crashed actors"
  done
}

start_api() {
  stop_api
  k get pods -n "$NS" -o json >"${STATE}/pods.json"
  write_crash_lines
  rm -f "${STATE}/addr"
  nohup "$BIN" serve --namespace "$NS" --state "$STATE" --addr "127.0.0.1:${PORT}" --pods "${STATE}/pods.json" >>"$LOG" 2>&1 &
  echo $! >"$PIDFILE"
  for _ in $(seq 1 50); do [ -s "${STATE}/addr" ] && break; sleep 0.1; done
  [ -s "${STATE}/addr" ] || { tail -5 "$LOG"; fail "fake API did not start (see $LOG)"; }
  ok "fake API pid $(cat "$PIDFILE") on 127.0.0.1:${PORT} (log $LOG)"
}

probe() {
  "$BIN" probe --addr "127.0.0.1:${PORT}" --ca "${STATE}/ca.pem" --token-file "$1"
}

# delete_fixture_pod removes one of the fixture's own Pods now. A Pod already
# Terminating keeps the grace period it started with (the draining Pod's is
# 24h): a second delete with a shorter one is not honored by the kubelet, so
# that one is force-deleted. The caller has checked the fixture label.
delete_fixture_pod() {
  if [ -n "$(k get pod "$1" -n "$NS" -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null)" ]; then
    k delete pod "$1" -n "$NS" --grace-period=0 --force >/dev/null 2>&1
  else
    k delete pod "$1" -n "$NS" --grace-period=1 --wait=true --timeout=60s >/dev/null
  fi
}

install_crds() {
  step "Substrate CRDs on $CTX"
  for crd in $CRDS; do
    if k get crd "$crd" >/dev/null 2>&1; then
      if [ "$(k get crd "$crd" -o jsonpath='{.metadata.labels.radar\.skyhook\.io/fixture}')" = "substrate-demo" ]; then
        k apply --server-side --field-manager=substrate-demo --force-conflicts -f "${CRD_DIR}/ate.dev_${crd%%.*}.yaml" >/dev/null
        ok "$crd (fixture-installed, re-applied)"
      else
        ok "$crd already installed by someone else; using it as is"
      fi
    else
      k apply --server-side --field-manager=substrate-demo -f "${CRD_DIR}/ate.dev_${crd%%.*}.yaml" >/dev/null
      k label crd "$crd" "$SEL" --overwrite >/dev/null
      ok "$crd installed"
    fi
  done
  k wait --for=condition=Established crd $CRDS --timeout=60s >/dev/null
}

apply_fixtures() {
  step "Applying fixtures to $CTX/$NS (label $SEL)"
  owned_or_absent namespace "$NS"
  owned_or_absent sandboxconfig.ate.dev gvisor-default
  owned_or_absent sandboxconfig.ate.dev microvm
  fapi manifests >"${STATE}/manifests.yaml"
  # A Pod reads its script once at start, so a changed script needs a new Pod.
  while read -r pod hash; do
    k get pod "$pod" -n "$NS" >/dev/null 2>&1 || continue
    owned_or_absent pod "$pod" -n "$NS"
    cur="$(k get pod "$pod" -n "$NS" -o jsonpath='{.metadata.annotations.substrate-demo\.radar\.skyhook\.io/script-hash}')"
    if [ "$cur" != "$hash" ]; then
      delete_fixture_pod "$pod"
      ok "recreating $pod (script changed)"
    fi
  done < <(fapi manifests --hashes)
  # Server-side: the scripts ConfigMap is too big for the last-applied annotation.
  k apply --server-side --field-manager=substrate-demo --force-conflicts -f "${STATE}/manifests.yaml" >/dev/null
  ok "applied $(grep -c '^kind:' "${STATE}/manifests.yaml") objects"
  while IFS=$'\t' read -r pool patch; do
    k patch workerpool.ate.dev "$pool" -n "$NS" --subresource=status --type=merge -p "$patch" >/dev/null
  done < <(fapi pool-status)
  ok "WorkerPool status written (no controller runs)"

  step "Waiting for worker Pods"
  drain=""
  unready=""
  n=0
  while read -r pod _; do
    if [ "$(k get pod "$pod" -n "$NS" -o jsonpath='{.metadata.annotations.substrate-demo\.radar\.skyhook\.io/drain}')" = "true" ]; then
      drain="$drain $pod"
    fi
    if [ "$(k get pod "$pod" -n "$NS" -o jsonpath='{.metadata.annotations.substrate-demo\.radar\.skyhook\.io/not-ready}')" = "true" ]; then
      # Its readiness probe always fails: the unavailable worker's Pod.
      k wait --for=jsonpath='{.status.phase}'=Running "pod/$pod" -n "$NS" --timeout=120s >/dev/null 2>&1 || true
      if [ "$(k get pod "$pod" -n "$NS" -o jsonpath='{.status.phase}')" = "Running" ]; then
        ok "$pod Running and not Ready, as intended"
      else
        warn "$pod is not Running; see kubectl describe"
      fi
      continue
    fi
    k wait --for=condition=Ready "pod/$pod" -n "$NS" --timeout=120s >/dev/null 2>&1 || true
    if [ "$(k get pod "$pod" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]; then
      n=$((n + 1))
    else
      why="$(k get pod "$pod" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="PodScheduled")].message}')"
      warn "$pod not Ready: ${why:-see kubectl describe}"
      unready="$unready $pod"
    fi
  done < <(fapi manifests --hashes)
  ok "$n worker Pods Ready"
  [ -z "$unready" ] || warn "Workers on unready Pods keep their Pod UID but report a made-up node/IP and print no logs until the Pod runs; then run '$0 restart'"
  for pod in $drain; do
    if [ -z "$(k get pod "$pod" -n "$NS" -o jsonpath='{.spec.nodeName}')" ]; then
      warn "$pod is not scheduled; not deleting it yet (re-run up once it runs)"
    elif [ -z "$(k get pod "$pod" -n "$NS" -o jsonpath='{.metadata.deletionTimestamp}')" ]; then
      # Graceful delete: its 24h grace period keeps it Terminating and logging.
      k delete pod "$pod" -n "$NS" --wait=false >/dev/null
      ok "$pod deleted gracefully, now Terminating (backs the DRAINING worker)"
    else
      ok "$pod already Terminating"
    fi
  done
}

print_settings() {
  cat <<EOF

${C_GREEN}Settings → Agent Substrate (kube context ${CTX}, workers in ${NS}):${C_RESET}
  endpoint    127.0.0.1:${PORT}
  caFile      ${STATE}/ca.pem
  serverName  api.ate-system.svc
  tokenFile   ${STATE}/token           (global viewer)
              ${STATE}/token-limited   (atespace-only viewer: set atespaces code-review, support-bots)

Faults:  $0 fault unavailable|unauthenticated|latency 2s|clear
EOF
}

cmd_up() {
  require_context
  build
  echo "$CTX" >"${STATE}/context"
  echo "$NS" >"${STATE}/namespace"
  install_crds
  apply_fixtures
  step "Starting fake API"
  start_api
  step "Verifying over TLS"
  probe "${STATE}/token" | sed 's/^/    /'
  probe "${STATE}/token-limited" | sed 's/^/    /'
  print_settings
}

cmd_restart() {
  build
  require_context
  start_api
  probe "${STATE}/token" | sed 's/^/    /'
}

cmd_down() {
  step "Stopping fake API"
  stop_api
  require_context
  step "Deleting fixtures on $CTX"
  owned_or_absent namespace "$NS"
  if k get namespace "$NS" >/dev/null 2>&1; then
    # The draining Pod's 24h grace period would hold the namespace open.
    for pod in $(k get pods -n "$NS" -l "$SEL" -o jsonpath='{.items[*].metadata.name}'); do
      delete_fixture_pod "$pod"
    done
    k delete workerpools.ate.dev,configmaps -n "$NS" -l "$SEL" 2>&1 | sed 's/^/    /' || true
    # Delete the namespace only when nothing the demo didn't create is left in it.
    others=$(k get all,configmaps,secrets,persistentvolumeclaims,workerpools.ate.dev -n "$NS" -o name 2>/dev/null \
      | grep -v -e '^configmap/kube-root-ca.crt$' -e '^secret/default-token' || true)
    if [ -z "$others" ]; then
      k delete namespace "$NS" --wait=true --timeout=120s | sed 's/^/    /'
    else
      warn "kept namespace $NS: it holds objects the demo didn't create:"
      echo "$others" | sed 's/^/      /'
    fi
  fi
  k delete sandboxconfigs.ate.dev -l "$SEL" 2>&1 | sed 's/^/    /' || true
  ok "done; state (CA, tokens) kept in $STATE"
}

cmd_purge() {
  cmd_down
  step "Deleting fixture-installed Substrate CRDs"
  for crd in $(k get crd -l "$SEL" -o jsonpath='{.items[*].metadata.name}'); do
    # Deleting a CRD deletes every resource of it; keep one someone else has started using.
    others=$(k get "$crd" -A -o name 2>/dev/null | head -5)
    if [ -n "$others" ]; then
      warn "kept CRD $crd: resources of it remain (e.g. $(echo "$others" | head -1)); delete them first if they are yours"
    else
      k delete crd "$crd" 2>&1 | sed 's/^/    /' || true
    fi
  done
}

cmd_status() {
  step "Fake API"
  if api_running; then
    ok "running, pid $(cat "$PIDFILE"), 127.0.0.1:${PORT}"
    probe "${STATE}/token" | sed 's/^/    /' || warn "probe failed"
    [ -s "${STATE}/fault" ] && warn "fault active: $(tr '\n' ' ' <"${STATE}/fault")"
  else
    warn "not running"
  fi
  require_context
  step "Fixtures on $CTX/$NS"
  k get pods,configmaps,workerpools.ate.dev -n "$NS" -l "$SEL" -o wide 2>&1 | sed 's/^/    /'
  k get sandboxconfigs.ate.dev -l "$SEL" 2>&1 | sed 's/^/    /'
}

cmd_fault() {
  mkdir -p "$STATE"
  case "${1:-}" in
    unavailable|unauthenticated) echo "$1" >"${STATE}/fault" ;;
    latency) [ -n "${2:-}" ] || fail "usage: fault latency <duration, e.g. 2s>"; echo "latency=$2" >"${STATE}/fault" ;;
    clear) rm -f "${STATE}/fault" ;;
    *) fail "usage: fault unavailable|unauthenticated|latency <dur>|clear" ;;
  esac
  ok "fault: ${1}${2:+ $2}"
}

case "$CMD" in
  up) cmd_up ;;
  down) cmd_down ;;
  purge) cmd_purge ;;
  restart) cmd_restart ;;
  status) cmd_status ;;
  fault) cmd_fault ${ARGS[@]+"${ARGS[@]}"} ;;
  help|-h|--help) sed -n '2,34p' "$0" | sed 's/^# \{0,1\}//' ;;
  *) fail "unknown command $CMD (try help)" ;;
esac
