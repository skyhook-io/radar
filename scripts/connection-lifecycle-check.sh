#!/bin/zsh
# Live checks for the connection lifecycle (docs/connection-lifecycle.md).
#
#   scripts/connection-lifecycle-check.sh setup               # fixtures + kubeconfig in $WORKDIR
#   scripts/connection-lifecycle-check.sh run <radar-binary> <label> [L1 L4 ...]
#   scripts/connection-lifecycle-check.sh cleanup
#
# Uses the radar-test-nonprod / radar-test-prod EKS contexts from your
# kubeconfig (aws exec auth) and creates a zero-replica Deployment
# "radar-live-probe" in their default namespaces. Radar runs on port 9361 with
# HOME pointed at $WORKDIR/home (your ~/.radar is untouched; ~/.aws is linked
# so the aws plugin works). Results go to $WORKDIR/results-<label>.txt. Each
# line is checked against the expectation in the checklist (the script exits 1
# on any FAIL), except with label "before", which records today's behaviour.
set -u
WORKDIR=${WORKDIR:-${TMPDIR:-/tmp}/radar-connection-lifecycle}
L=$WORKDIR
OWNER_LABEL=radar.skyhook.io/connection-lifecycle-check

setup() {
  umask 077 # the combined kubeconfig carries credentials
  mkdir -p $L/home $L/bin
  [[ -e $L/home/.aws ]] || ln -s ~/.aws $L/home/.aws
  print '#!/bin/sh\necho "error: SSO session expired; run aws sso login" >&2\nexit 1' > $L/bin/fail-plugin
  print '#!/bin/sh\nsleep 120' > $L/bin/stuck-plugin
  chmod +x $L/bin/*
  local tmp=$(mktemp -d)
  for c in radar-test-nonprod radar-test-prod; do
    kubectl config view --raw --minify --flatten --context $c > $tmp/$c.yaml
  done
  KUBECONFIG=$tmp/radar-test-nonprod.yaml:$tmp/radar-test-prod.yaml kubectl config view --raw --flatten > $L/kubeconfig.yaml
  rm -rf -- $tmp
  K() { kubectl --kubeconfig $L/kubeconfig.yaml "$@"; }
  local NP=$(K config view -o jsonpath='{.contexts[?(@.name=="radar-test-nonprod")].context.cluster}')
  local NU=$(K config view -o jsonpath='{.contexts[?(@.name=="radar-test-nonprod")].context.user}')
  K config set-cluster blackhole --server=https://10.255.255.1:6443 --insecure-skip-tls-verify=true >/dev/null
  K config set-credentials fail-user --exec-api-version=client.authentication.k8s.io/v1beta1 --exec-command=$L/bin/fail-plugin >/dev/null
  K config set-credentials stuck-user --exec-api-version=client.authentication.k8s.io/v1beta1 --exec-command=$L/bin/stuck-plugin >/dev/null
  K config set-context radar-test-unreachable --cluster=blackhole --user="$NU" >/dev/null
  K config set-context radar-test-badauth --cluster="$NP" --user=fail-user >/dev/null
  K config set-context radar-test-stuck --cluster="$NP" --user=stuck-user >/dev/null
  K config set-context radar-test-repoint --cluster="$NP" --user="$NU" >/dev/null
  chmod 600 $L/kubeconfig.yaml
  for c in radar-test-nonprod radar-test-prod; do
    if kubectl --context $c -n default get deploy radar-live-probe >/dev/null 2>&1; then
      [[ "$(kubectl --context $c -n default get deploy radar-live-probe -o jsonpath="{.metadata.labels.${OWNER_LABEL//./\\.}}")" == true ]] ||
        { print -u2 "default/radar-live-probe on $c exists and isn't ours; not touching it"; return 1; }
    else
      kubectl --context $c -n default create deployment radar-live-probe --image=registry.k8s.io/pause:3.9 --replicas=0 &&
        kubectl --context $c -n default label deployment radar-live-probe $OWNER_LABEL=true
    fi
    if ! kubectl --context $c -n default get configmap radar-live-probe >/dev/null 2>&1; then
      kubectl --context $c -n default create configmap radar-live-probe --from-literal=probe=true &&
        kubectl --context $c -n default label configmap radar-live-probe $OWNER_LABEL=true
    fi
  done
}

cleanup() {
  for c in radar-test-nonprod radar-test-prod; do
    kubectl --context $c -n default delete deployment -l $OWNER_LABEL=true --field-selector metadata.name=radar-live-probe
    kubectl --context $c -n default delete configmap -l $OWNER_LABEL=true --field-selector metadata.name=radar-live-probe
  done
  [[ -L $L/home/.aws ]] && rm -- $L/home/.aws
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
  local ctx=${2:-radar-test-nonprod}
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
  local code=$(post "/contexts/$1?track=1")
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
  for c in radar-test-nonprod radar-test-prod; do
    [[ "$(kubectl --kubeconfig $L/kubeconfig.yaml --context $c -n default get deploy radar-live-probe -o jsonpath="{.metadata.labels.${OWNER_LABEL//./\\.}}" 2>/dev/null)" == true ]] || return 1
  done
}
restart_probe() { # restart_probe <connectionHeader> → "code nonprodChanged prodChanged"
  local a1=$(restarted radar-test-nonprod) b1=$(restarted radar-test-prod)
  local code=$(post /workloads/deployments/default/radar-live-probe/restart "$1")
  sleep 1
  local a2=$(restarted radar-test-nonprod) b2=$(restarted radar-test-prod)
  [[ "$a1$b1$a2$b2" == *READ-FAILED* ]] && { print "$code observation-failed"; return; }
  print "$code nonprod=$([[ $a1 != $a2 ]] && print WROTE || print no) prod=$([[ $b1 != $b2 ]] && print WROTE || print no)"
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
    switch_tracked radar-test-prod
    wait_state connecting radar-test-prod || record "L1 | never saw connecting·prod |"
    expect "L1 mid-switch write, stale tab header" "$(restart_probe "$idA")" "503 nonprod=no prod=no"
    expect "L1b mid-switch write, no header (API caller)" "$(restart_probe "")" "503 nonprod=no prod=no"
    wait_switch >/dev/null
    expect "L2 stale tab after the switch completed" "$(restart_probe "$idA")" "409 nonprod=no prod=no"
    local idB=$(connid)
    expect "L2b current tab after the switch" "$(restart_probe "$idB")" "200 nonprod=no prod=WROTE"
    switch_tracked radar-test-nonprod; wait_switch >/dev/null
    expect "L3 A→B→A: old nonprod ID after returning" "$(restart_probe "$idA")" "409 nonprod=no prod=no"
    local idA2=$(connid)
    expect "L3b IDs never repeat" "$([[ -n $idA2 && $idA2 != $idA && $idA2 != $idB ]] && print distinct || print "same:$idA/$idB/$idA2")" "distinct"
    ;;
  L4) # status during a switch
    start 20s || return
    local idA=$(connid)
    expect "L4 status names the serving connection" "$([[ -n $idA ]] && print set || print empty)" "set"
    switch_tracked radar-test-prod
    wait_state connecting radar-test-prod
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
    expect "L6b old connection still writable" "$(restart_probe "$idA")" "200 nonprod=WROTE prod=no"
    ;;
  L7) # slow first sync (pods held 40s) on a tracked switch
    start 40s || return
    switch_tracked radar-test-prod
    expect "L7 tracked switch, pods held 40s" "$(wait_switch)" "succeeded"
    ;;
  L8) # Radar restarts: an ID from the previous process is refused
    start 0s || return
    local idOld=$(connid)
    start 0s || return
    expect "L8 ID from a previous Radar process" "$(restart_probe "$idOld")" "409 nonprod=no prod=no"
    ;;
  L9) # same context name repointed to another cluster, then Retry
    local KC=(kubectl --kubeconfig $L/kubeconfig.yaml)
    local PC=$($KC config view -o jsonpath='{.contexts[?(@.name=="radar-test-prod")].context.cluster}')
    local NC=$($KC config view -o jsonpath='{.contexts[?(@.name=="radar-test-nonprod")].context.cluster}')
    local PU=$($KC config view -o jsonpath='{.contexts[?(@.name=="radar-test-prod")].context.user}')
    local NU=$($KC config view -o jsonpath='{.contexts[?(@.name=="radar-test-nonprod")].context.user}')
    {
      # Start from nonprod whatever an interrupted run left behind.
      $KC config set-context radar-test-repoint --cluster="$NC" --user="$NU" >/dev/null ||
        { record "!! L9: could not point the fixture at nonprod"; FAILED=1; return 1; }
      start 0s radar-test-repoint || return
      local idOld=$(connid)
      $KC config set-context radar-test-repoint --cluster="$PC" --user="$PU" >/dev/null ||
        { record "!! L9: could not repoint the fixture"; FAILED=1; return 1; }
      local code=$(post /connection/retry)
      expect "L9 Retry after the context was repointed" "retry=$code id=$([[ "$(connid)" != $idOld ]] && print new || print same)" "retry=200 id=new"
      expect "L9b old ID after repointing" "$(restart_probe "$idOld")" "409 nonprod=no prod=no"
      expect "L9c current ID after repointing writes to the new cluster" "$(restart_probe "$(connid)")" "200 nonprod=no prod=WROTE"
    } always {
      $KC config set-context radar-test-repoint --cluster="$NC" --user="$NU" >/dev/null
    }
    ;;
  L10) # Stop mid-warm-up: nothing serves, writes are refused as retryable
    start 40s || return
    local idA=$(connid)
    switch_tracked radar-test-prod
    local opid=$SWITCH_ID
    wait_state connecting radar-test-prod
    for i in {1..20}; do [[ "$(status | j "(d.get('contextSwitch') or {}).get('canStop',False)")" == True ]] && break; sleep 0.5; done
    local code=$(post "/contexts/radar-test-prod?stopSwitch=$opid")
    local phase=$(wait_switch)
    expect "L10 Stop during warm-up" "stop=$code phase=$phase id=$(connid)" "stop=204 phase=stopped id="
    expect "L10b write after Stop" "$(restart_probe "$idA")" "503 nonprod=no prod=no"
    ;;
  L11) # other bound writes: a stale tab's delete is refused like its restart
    start 0s || return
    local idA=$(connid)
    switch_tracked radar-test-prod; wait_switch >/dev/null
    local code=$(curl -s -m 30 -o $L/last-body.json -w '%{http_code}' -X DELETE -H 'Sec-Fetch-Site: same-origin' -H "X-Radar-Connection: $idA" "$API/resources/configmaps/default/radar-live-probe")
    local np=$(kubectl --kubeconfig $L/kubeconfig.yaml --context radar-test-nonprod -n default get configmap radar-live-probe -o name 2>/dev/null)
    local pr=$(kubectl --kubeconfig $L/kubeconfig.yaml --context radar-test-prod -n default get configmap radar-live-probe -o name 2>/dev/null)
    expect "L11 stale tab deletes a same-named ConfigMap" "$code nonprod=$([[ -n $np ]] && print kept || print DELETED) prod=$([[ -n $pr ]] && print kept || print DELETED)" "409 nonprod=kept prod=kept"
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
  (( $#scenarios )) || scenarios=(L1 L4 L6 L7 L8 L9 L10 L11)
  for one in "${scenarios[@]}"; do run $one; done
  stop
  record "== done $(ts)"
  exit $FAILED
  ;;
*) print -u2 "usage: $0 setup | run <radar-binary> <label> [scenarios] | cleanup"; exit 2 ;;
esac
