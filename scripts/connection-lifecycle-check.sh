#!/bin/zsh
# Live checks for the connection lifecycle (docs/connection-lifecycle.md).
#
#   scripts/connection-lifecycle-check.sh setup               # fixtures + kubeconfig in $WORKDIR
#   scripts/connection-lifecycle-check.sh run <radar-binary> <label> [L1 L4 ...]
#   scripts/connection-lifecycle-check.sh cleanup
#
# Needs two clusters you can write to, named by their kubeconfig contexts:
# CONTEXT_A and CONTEXT_B (default radar-test-nonprod and radar-test-prod, the
# team's EKS test clusters). Creates a zero-replica Deployment and a ConfigMap
# "radar-live-probe" in their default namespaces. Radar runs on port 9361 with
# HOME pointed at $WORKDIR/home (your ~/.radar is untouched; ~/.aws,
# ~/.config/gcloud and ~/.azure are linked when present, so exec credential
# plugins work). Results go to $WORKDIR/results-<label>.txt. Each
# line is checked against the expectation in the checklist (the script exits 1
# on any FAIL), except with label "before", which records today's behaviour.
set -u
WORKDIR=${WORKDIR:-${TMPDIR:-/tmp}/radar-connection-lifecycle}
L=$WORKDIR
OWNER_LABEL=radar.skyhook.io/connection-lifecycle-check
CTX_A=${CONTEXT_A:-radar-test-nonprod}
CTX_B=${CONTEXT_B:-radar-test-prod}
CRED_DIRS=(.aws .config/gcloud .azure)
enc() { python3 -c 'import sys,urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"; }

setup() {
  umask 077 # the combined kubeconfig carries credentials
  mkdir -p $L/home $L/bin
  local d
  for d in $CRED_DIRS; do
    [[ -e ~/$d && ! -e $L/home/$d ]] && { mkdir -p $L/home/${d:h}; ln -s ~/$d $L/home/$d; }
  done
  print '#!/bin/sh\necho "error: SSO session expired; run aws sso login" >&2\nexit 1' > $L/bin/fail-plugin
  print '#!/bin/sh\nsleep 120' > $L/bin/stuck-plugin
  chmod +x $L/bin/*
  local tmp=$(mktemp -d)
  kubectl config view --raw --minify --flatten --context "$CTX_A" > $tmp/a.yaml
  kubectl config view --raw --minify --flatten --context "$CTX_B" > $tmp/b.yaml
  KUBECONFIG=$tmp/a.yaml:$tmp/b.yaml kubectl config view --raw --flatten > $L/kubeconfig.yaml
  rm -rf -- $tmp
  K() { kubectl --kubeconfig $L/kubeconfig.yaml "$@"; }
  local NP=$(K config view -o jsonpath="{.contexts[?(@.name==\"$CTX_A\")].context.cluster}")
  local NU=$(K config view -o jsonpath="{.contexts[?(@.name==\"$CTX_A\")].context.user}")
  K config set-cluster blackhole --server=https://10.255.255.1:6443 --insecure-skip-tls-verify=true >/dev/null
  K config set-credentials fail-user --exec-api-version=client.authentication.k8s.io/v1beta1 --exec-command=$L/bin/fail-plugin >/dev/null
  K config set-credentials stuck-user --exec-api-version=client.authentication.k8s.io/v1beta1 --exec-command=$L/bin/stuck-plugin >/dev/null
  K config set-context radar-test-unreachable --cluster=blackhole --user="$NU" >/dev/null
  K config set-context radar-test-badauth --cluster="$NP" --user=fail-user >/dev/null
  K config set-context radar-test-stuck --cluster="$NP" --user=stuck-user >/dev/null
  K config set-context radar-test-repoint --cluster="$NP" --user="$NU" >/dev/null
  chmod 600 $L/kubeconfig.yaml
  for c in "$CTX_A" "$CTX_B"; do
    if kubectl --context $c -n default get deploy radar-live-probe >/dev/null 2>&1; then
      [[ "$(kubectl --context $c -n default get deploy radar-live-probe -o jsonpath="{.metadata.labels.${OWNER_LABEL//./\\.}}")" == true ]] ||
        { print -u2 "default/radar-live-probe on $c exists and isn't ours; not touching it"; return 1; }
    else
      kubectl --context $c -n default create deployment radar-live-probe --image=registry.k8s.io/pause:3.9 --replicas=0 &&
        kubectl --context $c -n default label deployment radar-live-probe $OWNER_LABEL=true
    fi
  done
}

cleanup() {
  for c in "$CTX_A" "$CTX_B"; do
    kubectl --context $c -n default delete deployment -l $OWNER_LABEL=true --field-selector metadata.name=radar-live-probe
  done
  local d
  for d in $CRED_DIRS; do [[ -L $L/home/$d ]] && rm -- $L/home/$d; done
  rm -f -- $L/kubeconfig.yaml
}

j() { python3 -c "import sys,json; d=json.load(sys.stdin); print(eval(sys.argv[1]))" "$1"; }
status() { curl -s -m 5 "$API/connection?contexts=0"; }
connid() { status | j "d.get('connectionId','')"; }
field() { status | j "$1"; }
ts() { date +%T; }
record() { print -r -- "$*" | tee -a $OUT; }

start() { # start <podsDelay> [context]
  stop
  local ctx=${2:-$CTX_A}
  if lsof -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1; then
    record "!! port $PORT is in use by another process"; FAILED=1; return 1
  fi
  kubectl --kubeconfig $L/kubeconfig.yaml config use-context $ctx >/dev/null
  HOME=$L/home RADAR_DEBUG_SYNC_DELAY=pods=$1 $BIN --kubeconfig $L/kubeconfig.yaml --port $PORT --no-browser >> $LOG 2>&1 &
  PID=$!
  for i in {1..180}; do
    [[ "$(field "d.get('state')" 2>/dev/null)" == connected ]] && return 0
    sleep 1
  done
  record "!! start: never connected"; FAILED=1; return 1
}
stop() { [[ -n "$PID" ]] && kill $PID 2>/dev/null && wait $PID 2>/dev/null; PID=""; for i in {1..20}; do lsof -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1 || return 0; sleep 0.5; done; }

post() { # post <path> [connectionHeader] → prints "code"
  local h=()
  [[ -n "${2:-}" ]] && h=(-H "X-Radar-Connection: $2")
  curl -s -m 60 -o $L/last-body.json -w '%{http_code}' -X POST -H 'Sec-Fetch-Site: same-origin' "${h[@]}" "$API$1"
}
switch_tracked() { # starts a tracked switch; SWITCH_ID names it (empty if refused)
  SWITCH_ID=""
  local code=$(post "/contexts/$(enc "$1")?track=1")
  [[ $code == 202 ]] && SWITCH_ID=$(j "d['id']" < $L/last-body.json)
}
wait_switch() { # wait until the switch SWITCH_ID ends; prints its phase
  [[ -n "$SWITCH_ID" ]] || { print not-started; return; }
  for i in {1..240}; do
    local p=$(status | j "(lambda o: o.get('phase','') if o.get('id')=='$SWITCH_ID' else 'other')(d.get('contextSwitch') or {})")
    [[ "$p" != preflight && "$p" != switching && "$p" != stopping && "$p" != other && -n "$p" ]] && { print $p; return; }
    sleep 1
  done
  print timeout
}
wait_state() { # wait_state <state> <context>
  for i in {1..120}; do
    [[ "$(field "d.get('state')")" == $1 && "$(field "d.get('context')")" == $2 ]] && return 0
    sleep 0.5
  done
  return 1
}
restarted() { kubectl --kubeconfig $L/kubeconfig.yaml --context $1 -n default get deploy radar-live-probe -o jsonpath='{.spec.template.metadata.annotations.kubectl\.kubernetes\.io/restartedAt}' 2>/dev/null || print READ-FAILED; }
owned() {
  local c
  for c in "$CTX_A" "$CTX_B"; do
    [[ "$(kubectl --kubeconfig $L/kubeconfig.yaml --context $c -n default get deploy radar-live-probe -o jsonpath="{.metadata.labels.${OWNER_LABEL//./\\.}}" 2>/dev/null)" == true ]] || return 1
  done
}
restart_probe() { # restart_probe <connectionHeader> → "code aChanged bChanged"
  local a1=$(restarted $CTX_A) b1=$(restarted $CTX_B)
  local code=$(post /workloads/deployments/default/radar-live-probe/restart "$1")
  sleep 1
  local a2=$(restarted $CTX_A) b2=$(restarted $CTX_B)
  [[ "$a1$b1$a2$b2" == *READ-FAILED* ]] && { print "$code observation-failed"; return; }
  print "$code a=$([[ $a1 != $a2 ]] && print WROTE || print no) b=$([[ $b1 != $b2 ]] && print WROTE || print no)"
}
expect() { # expect <scenario> <observed> <wanted-for-after>
  local verdict=""
  if [[ $BUILD != before ]]; then
    if [[ "$2" == ${~3} ]]; then verdict=PASS; else verdict="FAIL (want $3)"; FAILED=1; fi
  fi
  record "$1 | $2 | $verdict"
}

run() {
  case $1 in
  L1) # a write sent mid-switch by a tab still showing the old cluster
    start 20s || return
    local idA=$(connid)
    switch_tracked $CTX_B
    wait_state connecting $CTX_B || record "L1 | never saw connecting·B |"
    expect "L1 mid-switch write, stale tab header" "$(restart_probe "$idA")" "503 a=no b=no"
    expect "L1b mid-switch write, no header (API caller)" "$(restart_probe "")" "503 a=no b=no"
    wait_switch >/dev/null
    expect "L2 stale tab after the switch completed" "$(restart_probe "$idA")" "409 a=no b=no"
    local idB=$(connid)
    expect "L2b current tab after the switch" "$(restart_probe "$idB")" "200 a=no b=WROTE"
    switch_tracked $CTX_A; wait_switch >/dev/null
    expect "L3 A→B→A: old A ID after returning" "$(restart_probe "$idA")" "409 a=no b=no"
    local idA2=$(connid)
    expect "L3b IDs never repeat" "$([[ -n $idA2 && $idA2 != $idA && $idA2 != $idB ]] && print distinct || print "same:$idA/$idB/$idA2")" "distinct"
    ;;
  L4) # status during a switch
    start 20s || return
    local idA=$(connid)
    expect "L4 status names the serving connection" "$([[ -n $idA ]] && print set || print empty)" "set"
    switch_tracked $CTX_B
    wait_state connecting $CTX_B
    local mid=$(field "repr(d.get('connectionId',''))+' '+d.get('state')")
    expect "L5 status mid-switch: no serving ID, connecting" "$mid" "'' connecting"
    wait_switch >/dev/null
    ;;
  L6) # targets that fail before leaving keep the old connection serving
    start 0s || return
    local idA=$(connid)
    for target in radar-test-unreachable radar-test-badauth radar-test-stuck; do
      local t0=$SECONDS
      switch_tracked $target
      local phase=$(wait_switch)
      local idNow=$(connid)
      expect "L6 refused before leaving: $target ($((SECONDS-t0))s)" "$phase id=$([[ $idNow == $idA ]] && print unchanged || print changed)" "failed id=unchanged"
    done
    expect "L6b old connection still writable" "$(restart_probe "$idA")" "200 a=WROTE b=no"
    ;;
  L7) # slow first sync (pods held 40s) on a tracked switch
    start 40s || return
    switch_tracked $CTX_B
    expect "L7 tracked switch, pods held 40s" "$(wait_switch)" "succeeded"
    ;;
  L8) # Radar restarts: an ID from the previous process is refused
    start 0s || return
    local idOld=$(connid)
    start 0s || return
    expect "L8 ID from a previous Radar process" "$(restart_probe "$idOld")" "409 a=no b=no"
    ;;
  L9) # same context name repointed to another cluster, then Retry
    local KC=(kubectl --kubeconfig $L/kubeconfig.yaml)
    local PC=$($KC config view -o jsonpath="{.contexts[?(@.name==\"$CTX_B\")].context.cluster}")
    local NC=$($KC config view -o jsonpath="{.contexts[?(@.name==\"$CTX_A\")].context.cluster}")
    local PU=$($KC config view -o jsonpath="{.contexts[?(@.name==\"$CTX_B\")].context.user}")
    local NU=$($KC config view -o jsonpath="{.contexts[?(@.name==\"$CTX_A\")].context.user}")
    {
      # Start from A whatever an interrupted run left behind.
      $KC config set-context radar-test-repoint --cluster="$NC" --user="$NU" >/dev/null ||
        { record "!! L9: could not point the fixture at A"; FAILED=1; return 1; }
      start 0s radar-test-repoint || return
      local idOld=$(connid)
      $KC config set-context radar-test-repoint --cluster="$PC" --user="$PU" >/dev/null ||
        { record "!! L9: could not repoint the fixture"; FAILED=1; return 1; }
      local code=$(post /connection/retry)
      expect "L9 Retry after the context was repointed" "retry=$code id=$([[ "$(connid)" != $idOld ]] && print new || print same)" "retry=200 id=new"
      expect "L9b old ID after repointing" "$(restart_probe "$idOld")" "409 a=no b=no"
      expect "L9c current ID after repointing writes to the new cluster" "$(restart_probe "$(connid)")" "200 a=no b=WROTE"
    } always {
      $KC config set-context radar-test-repoint --cluster="$NC" --user="$NU" >/dev/null
    }
    ;;
  L10) # Stop mid-warm-up: nothing serves, writes are refused as retryable
    start 40s || return
    local idA=$(connid)
    switch_tracked $CTX_B
    local opid=$SWITCH_ID
    wait_state connecting $CTX_B
    for i in {1..20}; do [[ "$(status | j "(d.get('contextSwitch') or {}).get('canStop',False)")" == True ]] && break; sleep 0.5; done
    local code=$(post "/contexts/$(enc "$CTX_B")?stopSwitch=$opid")
    local phase=$(wait_switch)
    expect "L10 Stop during warm-up" "stop=$code phase=$phase id=$(connid)" "stop=204 phase=stopped id="
    expect "L10b write after Stop" "$(restart_probe "$idA")" "503 a=no b=no"
    ;;
  *) record "!! unknown scenario $1"; FAILED=1 ;;
  esac
}


case ${1:-} in
setup) setup ;;
cleanup) cleanup ;;
run)
  BIN=$2; BUILD=$3; shift 3
  PORT=9361
  API=http://localhost:$PORT/api
  OUT=$L/results-$BUILD.txt
  LOG=$L/radar-$BUILD.log
  PID=""
  : > $OUT
  record "== $BUILD $(ts)"
  FAILED=0
  owned || { print -u2 "radar-live-probe isn't ours on both clusters (run setup); not writing to it"; exit 2; }
  scenarios=("$@")
  (( $#scenarios )) || scenarios=(L1 L4 L6 L7 L8 L9 L10)
  for one in "${scenarios[@]}"; do run $one; done
  stop
  record "== done $(ts)"
  exit $FAILED
  ;;
*) print -u2 "usage: $0 setup | run <radar-binary> <label> [scenarios] | cleanup"; exit 2 ;;
esac
