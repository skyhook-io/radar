#!/usr/bin/env bash
# Bootstrap a kind cluster pre-populated with Velero backup/restore scenarios
# for visual-testing the Velero views and the backup-failure Issues.
# Idempotent — re-run to refresh state or apply fixture updates without
# recreating the cluster.
#
# READ THIS FIRST — why `up` scales the Velero controller to zero.
#
#   A live controller with no bucket produces exactly one outcome — everything
#   Failed with a credentials error — which is the least interesting row in the
#   table. Freezing it lets the fixtures carry their own status, so all thirteen
#   phases are on screen at once, including the in-flight ones that by
#   definition do not sit still on a working cluster.
#
#   The `live` subcommand is the other half: it installs MinIO, lets Velero run,
#   and earns the states instead — Completed, a real restore, PartiallyFailed
#   from a failing hook, FailedValidation from a broken location, and a run left
#   genuinely stalled. Everything below describes `up`.
#
#   So the controller is installed and then scaled to 0, and the fixtures carry
#   their own status. Nothing reconciles it away, and every phase in the enum
#   becomes reachable. This is the whole trick; without it you will spend an
#   hour wondering why your hand-written status keeps reverting.
#
#   Worth knowing, because the usual advice sends you the wrong way: none of
#   the Velero CRDs declare a status subresource, so a plain `kubectl apply`
#   writes status. `kubectl patch --subresource=status` fails against them with
#   a confusing NotFound.
#
#   The cost, in `up` only: nothing exercises real controller behaviour. Use
#   `live` when that is the point. Out of reach in both: data-mover
#   DataUpload/DataDownload CRs, progress counters ticking while a run is in
#   flight, and full backup logs. See the README's "What `up` cannot cover".
#
# Subcommands:
#   up        Create cluster (if missing), install Velero CRDs, apply fixtures.
#   down      Delete the kind cluster.
#   reset     down + up.
#   status    Inventory what's installed and what the fixtures look like.
#   live      Give Velero real object storage (MinIO) and let the controller
#             produce the states itself: a real backup, a real restore, a
#             PartiallyFailed run, a location that genuinely breaks, a rejected
#             backup, and a run that genuinely stalls. Slower than `up`, and the
#             honest way to check this integration.
#   verify    Re-run the fixture assertions without reapplying.
#   help      Show this message.
#
# Prerequisites:
#   - kind     https://kind.sigs.k8s.io/
#   - kubectl
#   - helm     (installs the Velero chart; the controller is then scaled to 0)
#   - python3  (expands the @now tokens; in `live`, also reads the results
#              file and measures the stalled run against its budget)
#
# Set CLUSTER_NAME=foo to use a different cluster (default: radar-velero-demo).

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-velero-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="${SCRIPT_DIR}/velero-demo"
NS=velero

# Pinned so the demo behaves consistently. Chart 12.1.0 ships Velero 1.18.1,
# whose Backup phase enum is what the fixtures and the phase→level table in
# resource-utils-velero.ts are written against. Bump both together.
VELERO_CHART_VERSION="${VELERO_CHART_VERSION:-12.1.0}"
VELERO_APP_VERSION="1.18.1"

if [ -t 1 ]; then
  C_RESET=$'\e[0m'; C_BLUE=$'\e[34m'; C_GREEN=$'\e[32m'
  C_YELLOW=$'\e[33m'; C_RED=$'\e[31m'; C_DIM=$'\e[2m'
else
  C_RESET=""; C_BLUE=""; C_GREEN=""; C_YELLOW=""; C_RED=""; C_DIM=""
fi
step() { printf "${C_BLUE}==> %s${C_RESET}\n" "$1"; }
ok()   { printf "${C_GREEN}    ✓ %s${C_RESET}\n" "$1"; }
warn() { printf "${C_YELLOW}    ! %s${C_RESET}\n" "$1"; }
fail() { printf "${C_RED}    ✗ %s${C_RESET}\n" "$1"; exit 1; }
note() { printf "${C_DIM}    %s${C_RESET}\n" "$1"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 not found — install it: $2"
}

k() { kubectl --context "${KUBECTL_CTX}" "$@"; }

# Rendered fixtures (tokens expanded) live in temp dirs cleaned up on exit. A
# RETURN trap looks tidier but fires on every later function return too, and
# then trips `set -u` on a variable that has gone out of scope.
RENDER_DIRS=()
cleanup_render_dirs() {
  # Guard on the count rather than iterating "${RENDER_DIRS[@]:-}": an empty
  # array expands to one empty element, and the `[ -n "$d" ]` that skips it is
  # then the last command of an EXIT trap under `set -e` — so the script exited
  # 1 after a completely successful run.
  if [ "${#RENDER_DIRS[@]}" -gt 0 ]; then
    rm -rf "${RENDER_DIRS[@]}"
  fi
}
trap cleanup_render_dirs EXIT

cluster_exists() { kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; }

# --- Lifecycle -------------------------------------------------------------

# Every top-level *.yaml here is applied to the cluster wholesale, so anything
# in this directory that is not a cluster manifest breaks `up` — and breaks it
# during apply, after a cluster has been built, which reads as a fixture problem
# rather than a layout one. Non-manifests (the kind config) live one directory
# down; this asserts that rather than trusting whoever adds the next file.
#
# Runs before the cluster is created, so the failure costs seconds.
validate_fixture_dir() {
  local f base api
  for f in "${FIXTURES_DIR}"/*.yaml; do
    [ -e "$f" ] || continue
    base="$(basename "$f")"
    grep -q '^[[:space:]]*[^#[:space:]]' "$f" || continue   # comments-only is fine
    if ! grep -q '^apiVersion:' "$f"; then
      fail "${base} has no apiVersion — ${FIXTURES_DIR} is applied to the cluster, so it only holds manifests. Non-manifests go in a subdirectory."
    fi
    api="$(grep -m1 '^apiVersion:' "$f" | awk '{print $2}')"
    case "${api}" in
      kind.x-k8s.io/*)
        fail "${base} is a kind config, not a cluster manifest, and everything in ${FIXTURES_DIR} gets applied. Move it to ${FIXTURES_DIR}/cluster/." ;;
    esac
  done
}

cmd_up() {
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation (or 'brew install kind')"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd helm "https://helm.sh/docs/intro/install/ (or 'brew install helm')"
  require_cmd python3 "https://www.python.org/downloads/"
  validate_fixture_dir

  if cluster_exists; then
    step "Cluster '${CLUSTER_NAME}' already exists — reusing"
  else
    step "Creating kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --config "${FIXTURES_DIR}/cluster/kind-cluster.yaml" --wait 60s
    ok "Cluster created"
  fi
  k cluster-info >/dev/null || fail "kind context not reachable"

  install_velero
  apply_fixtures
  verify_fixtures
  print_summary
}

cmd_down() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  if cluster_exists; then
    step "Deleting cluster '${CLUSTER_NAME}'"
    kind delete cluster --name "${CLUSTER_NAME}"
    ok "Deleted"
  else
    warn "Cluster '${CLUSTER_NAME}' does not exist; nothing to do"
  fi
}

cmd_reset() { cmd_down; cmd_up; }

# --- Velero ----------------------------------------------------------------

# Scale the controller to zero and prove the process is gone.
#
# Wait for the POD, not for a replica count, and take the selector from the
# Deployment instead of guessing labels. Two traps here, both real:
#
#   A hardcoded selector that matches nothing makes `kubectl wait` return
#   instantly and succeed, and the chart's pod labels are not the ones a reader
#   would guess.
#
#   `.status.replicas` stops counting a pod the moment it starts terminating,
#   while the container keeps running for its grace period — and Velero's is
#   terminationGracePeriodSeconds: 3600. A replica count of zero therefore says
#   nothing about whether a controller is still reconciling.
#
# Nothing is in flight worth draining, so the leftover pod is force-deleted
# rather than waited out for up to an hour. Both callers need the same
# guarantee: `up` so hand-written status survives, `live` so the stalled run
# has nothing that could advance it.
stop_velero_controller() {
  local consequence="$1" sel pods=""
  k -n "${NS}" scale deployment/velero --replicas=0 >/dev/null

  sel="$(k -n "${NS}" get deployment velero \
    -o go-template='{{range $k, $v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}' 2>/dev/null | sed 's/,$//')"
  [ -n "${sel}" ] || fail "could not read the velero Deployment's pod selector"

  k -n "${NS}" delete pod -l "${sel}" --grace-period=0 --force --ignore-not-found >/dev/null 2>&1 || true

  for _ in $(seq 1 45); do
    pods="$(k -n "${NS}" get pods -l "${sel}" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
    [ "${pods}" = "0" ] && break
    sleep 2
  done
  [ "${pods}" = "0" ] \
    || fail "${pods} velero controller pod(s) still present after 90s — ${consequence}"
}

install_velero() {
  step "Installing Velero chart ${VELERO_CHART_VERSION} (app ${VELERO_APP_VERSION})"

  helm repo add vmware-tanzu https://vmware-tanzu.github.io/helm-charts >/dev/null 2>&1 || true
  helm repo update vmware-tanzu >/dev/null 2>&1 || true

  # credentials.useSecret=false and snapshotsEnabled=false keep the install
  # from demanding a cloud credential that would never be used — the
  # controller is about to be scaled to zero. The BSL is declared anyway so
  # the fixtures have a default location to reference.
  helm upgrade --install velero vmware-tanzu/velero \
    --kube-context "${KUBECTL_CTX}" \
    --namespace "${NS}" --create-namespace \
    --version "${VELERO_CHART_VERSION}" \
    --set credentials.useSecret=false \
    --set snapshotsEnabled=false \
    --set deployNodeAgent=false \
    --set "initContainers[0].name=velero-plugin-for-aws" \
    --set "initContainers[0].image=velero/velero-plugin-for-aws:v1.12.0" \
    --set "initContainers[0].volumeMounts[0].mountPath=/target" \
    --set "initContainers[0].volumeMounts[0].name=plugins" \
    --set "configuration.backupStorageLocation[0].name=default" \
    --set "configuration.backupStorageLocation[0].provider=aws" \
    --set "configuration.backupStorageLocation[0].bucket=radar-demo" \
    --set "configuration.backupStorageLocation[0].default=true" \
    --set "configuration.backupStorageLocation[0].config.region=us-east-1" \
    --wait --timeout 5m >/dev/null
  ok "Chart installed (13 CRDs)"

  # The load-bearing step. See the header comment.
  step "Scaling the Velero controller to 0 so hand-written status survives"
  stop_velero_controller "fixtures would be reconciled away"
  ok "Controller stopped — nothing will reconcile the fixtures away"
}

# --- Fixtures --------------------------------------------------------------

# None of the Velero CRDs declare a status subresource (checked across all six
# in v1.18.1), so status is an ordinary field and a plain apply writes it. The
# usual "kubectl apply drops status" rule only holds for kinds that DO register
# the subresource — reaching for `kubectl patch --subresource=status` here
# fails with a confusing NotFound, because there is no such subresource to
# address. If a future Velero release adds one, this is the function to change.
apply_one() {
  k apply -f "$1" >/dev/null
}

# Fixtures carry @now±Nm tokens instead of absolute timestamps, so a demo
# recorded today doesn't render as "expired 8 months ago" next year. Expand
# them into RFC3339 at apply time.
expand_tokens() {
  local src="$1" dst="$2"
  python3 - "$src" "$dst" <<'PY'
import datetime, re, sys
src, dst = sys.argv[1], sys.argv[2]
now = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
def sub(m):
    t = now + datetime.timedelta(minutes=int(m.group(1)))
    return t.strftime('%Y-%m-%dT%H:%M:%SZ')
open(dst, 'w').write(re.sub(r'@now([+-]\d+)m', sub, open(src).read()))
PY
}

apply_fixtures() {
  step "Applying Velero demo fixtures"
  [ -d "${FIXTURES_DIR}" ] || fail "Fixtures dir not found: ${FIXTURES_DIR}"

  local tmp; tmp="$(mktemp -d)"
  RENDER_DIRS+=("${tmp}")

  for f in $(ls "${FIXTURES_DIR}"/*.yaml 2>/dev/null | sort); do
    local base; base="$(basename "$f")"
    grep -q '^[[:space:]]*[^#[:space:]]' "$f" || { note "skipping ${base} (comments only)"; continue; }

    expand_tokens "$f" "${tmp}/${base}"
    note "applying ${base}"
    apply_one "${tmp}/${base}"

    # The CRD needs to be established before its CR can be created, and the
    # supersession backups need distinct creationTimestamps — see
    # verify_fixtures. Both are handled by pausing after the relevant files.
    case "${base}" in
      05-collision-crd.yaml)
        k wait --for=condition=Established crd/restores.resources.cattle.io --timeout=60s >/dev/null
        ;;
      6*-supersession-*.yaml)
        sleep 2
        ;;
    esac
  done
  ok "Fixtures applied"
}

# --- Verification ----------------------------------------------------------

# The fixture is only useful if it actually holds the states it claims to.
# These assertions are cheap and they fail loudly, which beats discovering
# mid-review that a phase silently didn't stick.
verify_fixtures() {
  step "Verifying fixture state"
  local errs=0

  # 0. No controller POD exists. Everything below is meaningless if one does —
  #    a running Velero rewrites every phase here within a reconcile loop.
  #    Checked as a pod count rather than a replica count: a terminating pod is
  #    already excluded from .status.replicas but is still running its
  #    container, and Velero's grace period is an hour.
  local vsel vpods
  vsel="$(k -n "${NS}" get deployment velero \
    -o go-template='{{range $k, $v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}' 2>/dev/null | sed 's/,$//')"
  vpods="$(k -n "${NS}" get pods -l "${vsel:-app.kubernetes.io/name=velero}" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  if [ "${vpods}" != "0" ]; then
    warn "${vpods} velero controller pod(s) present — fixtures will not survive"; errs=$((errs+1))
  else
    ok "no velero controller pod running"
  fi

  # 1. Every Backup phase in the enum is present exactly once.
  local expected="Completed Deleting Failed FailedValidation Finalizing FinalizingPartiallyFailed InProgress New PartiallyFailed Queued ReadyToStart WaitingForPluginOperations WaitingForPluginOperationsPartiallyFailed"
  local missing=""
  for p in ${expected}; do
    k get backups.velero.io -n "${NS}" \
      -o jsonpath='{.items[*].status.phase}' 2>/dev/null | tr ' ' '\n' | grep -qx "$p" || missing="${missing} $p"
  done
  if [ -n "${missing}" ]; then warn "missing Backup phases:${missing}"; errs=$((errs+1))
  else ok "all 13 Backup phases present"; fi

  # 1b. And every Restore phase. The two kinds have different enums — a Restore
  # has no Deleting, Queued or ReadyToStart — and this file drifted to seven of
  # ten while its own header claimed both partial-failure variants.
  local expected_r="Completed Failed FailedValidation Finalizing FinalizingPartiallyFailed InProgress New PartiallyFailed WaitingForPluginOperations WaitingForPluginOperationsPartiallyFailed"
  local missing_r=""
  for p in ${expected_r}; do
    k get restores.velero.io -n "${NS}" \
      -o jsonpath='{.items[*].status.phase}' 2>/dev/null | tr ' ' '\n' | grep -qx "$p" || missing_r="${missing_r} $p"
  done
  if [ -n "${missing_r}" ]; then warn "missing Restore phases:${missing_r}"; errs=$((errs+1))
  else ok "all 10 Restore phases present"; fi

  # 2. Status actually stuck. If the controller were still running, or the
  #    subresource patch silently no-opped, phases would be empty.
  local empty
  empty="$(k get backups.velero.io -n "${NS}" -o json \
    | python3 -c 'import json,sys; print(sum(1 for i in json.load(sys.stdin)["items"] if not (i.get("status") or {}).get("phase")))')"
  if [ "${empty}" != "0" ]; then warn "${empty} Backups have no phase — is the controller still running?"; errs=$((errs+1))
  else ok "hand-written status survived (controller is down)"; fi

  # 3. Supersession ordering. The rule under test is "a later successful backup
  #    clears an earlier failure in the same schedule". If the two nightly
  #    backups share a creationTimestamp, ordering falls back to the name
  #    tie-break and the test passes for a reason that has nothing to do with
  #    time — so assert the timestamps are actually distinct.
  local t_old t_new
  t_old="$(k get backup.velero.io -n "${NS}" nightly-20260806010000 -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null || true)"
  t_new="$(k get backup.velero.io -n "${NS}" nightly-20260807010000 -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null || true)"
  if [ -z "${t_old}" ] || [ -z "${t_new}" ]; then
    warn "supersession backups missing"; errs=$((errs+1))
  elif [ "${t_old}" = "${t_new}" ]; then
    warn "supersession backups share creationTimestamp ${t_old} — ordering would be decided by name, not time"; errs=$((errs+1))
  elif [[ "${t_old}" > "${t_new}" ]]; then
    warn "supersession backups created out of order (${t_old} > ${t_new})"; errs=$((errs+1))
  else
    ok "supersession ordering distinct (${t_old} -> ${t_new})"
  fi

  # 4. The collision CR exists and is genuinely foreign.
  if k get restores.resources.cattle.io rancher-restore >/dev/null 2>&1; then
    ok "rancher restores.resources.cattle.io present (plural collision covered)"
  else
    warn "rancher collision CR missing"; errs=$((errs+1))
  fi

  # 5. A restic repository exists — the Type column exists to surface it.
  if k get backuprepositories.velero.io -n "${NS}" -o jsonpath='{.items[*].spec.repositoryType}' 2>/dev/null | tr ' ' '\n' | grep -qx restic; then
    ok "restic BackupRepository present"
  else
    warn "no restic BackupRepository — the Type column has nothing to flag"; errs=$((errs+1))
  fi

  [ "${errs}" -eq 0 ] || fail "${errs} fixture check(s) failed"
}


# --- live mode -------------------------------------------------------------
#
# Everything above this line produces states by writing status into fixtures,
# because without object storage a running Velero has exactly one outcome
# available to it. `live` gives it a bucket, so the controller produces the
# states itself and Radar is read against work Velero actually did.
#
# What becomes real here: a Completed backup Velero wrote and can read back, its
# item counts, a Restore that recreated objects, a location that genuinely stops
# answering, and a run that genuinely stops moving. What stays out of reach is
# unchanged and listed in the README — anything needing CSI snapshots or the
# data mover.

MINIO_NS=minio
LIVE_DIR="${SCRIPT_DIR}/velero-demo/live"

cmd_live() {
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd helm "https://helm.sh/docs/intro/install/"
  # Checked here, not where it is used. python3 is needed twice near the end —
  # to read the results file and to measure the stalled run against its budget
  # — and discovering it is missing at that point means a cluster has already
  # been built and most of the states produced.
  require_cmd python3 "https://www.python.org/downloads/"

  if cluster_exists; then
    step "Cluster '${CLUSTER_NAME}' already exists — reusing"
  else
    step "Creating kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --config "${FIXTURES_DIR}/cluster/kind-cluster.yaml" --wait 60s
    ok "Cluster created"
  fi
  k cluster-info >/dev/null || fail "kind context not reachable"

  install_velero
  install_minio
  point_velero_at_minio
  produce_real_states
  verify_live
  print_live_summary
}

install_minio() {
  step "Installing MinIO (in-cluster S3)"
  k apply -f "${LIVE_DIR}/00-minio.yaml" >/dev/null
  k -n "${MINIO_NS}" rollout status deploy/minio --timeout=180s >/dev/null \
    || fail "MinIO did not become ready"
  ok "MinIO ready"

  # The bucket has to exist before Velero validates the location. Done with the
  # MinIO client inside the cluster rather than from the host, so no port-forward
  # and no extra tool on the developer's machine.
  # Emptied, not just created. Velero refuses a backup whose data is already in
  # the bucket ("backup already exists in object storage"), so a second `live`
  # run against surviving storage fails on the first backup it takes — and the
  # script advertises being re-runnable.
  step "Creating empty buckets radar-demo and radar-dr"
  k -n "${MINIO_NS}" delete job mc-mb --ignore-not-found >/dev/null 2>&1 || true
  cat <<EOF | k apply -f - >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: mc-mb
  namespace: ${MINIO_NS}
spec:
  backoffLimit: 3
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: mc
          image: minio/mc:RELEASE.2024-09-16T17-43-14Z
          command: ["/bin/sh","-c"]
          args:
            - |
              mc alias set local http://minio.${MINIO_NS}.svc:9000 minioadmin minioadmin &&
              mc rb --force local/radar-demo || true &&
              mc rb --force local/radar-dr || true &&
              mc mb local/radar-demo &&
              mc mb local/radar-dr
EOF
  k -n "${MINIO_NS}" wait --for=condition=complete job/mc-mb --timeout=120s >/dev/null \
    || fail "bucket creation job did not complete"
  ok "Buckets radar-demo and radar-dr created"

  # The host has to reach MinIO on the mapped port, because that is the address
  # Velero signs its download URLs for. A cluster created before this mapping
  # existed reuses fine right up until the results fetch, which then fails
  # blaming object storage — so check it here, where the cause is still legible.
  step "Checking the host can reach MinIO on the mapped port"
  if ! python3 - <<'PYEOF'
import socket, sys
s = socket.socket()
s.settimeout(5)
try:
    s.connect(("localhost", 30900))
except OSError as e:
    sys.exit(f"cannot reach localhost:30900 ({e})")
finally:
    s.close()
PYEOF
  then
    fail "localhost:30900 does not reach this cluster. That port mapping is created with the cluster (scripts/velero-demo/cluster/kind-cluster.yaml), so a cluster made before it existed cannot serve the results fetch. Recreate it: $0 down && $0 live"
  fi
  ok "localhost:30900 reaches MinIO, so Velero's download URLs will resolve"
}

point_velero_at_minio() {
  step "Pointing Velero at MinIO and starting the controller"

  # The AWS plugin reads a credentials file from this secret; the chart installed
  # with credentials.useSecret=false, so it is created here.
  k -n "${NS}" delete secret cloud-credentials --ignore-not-found >/dev/null 2>&1 || true
  k -n "${NS}" create secret generic cloud-credentials \
    --from-literal=cloud="[default]
aws_access_key_id=minioadmin
aws_secret_access_key=minioadmin" >/dev/null

  # Order matters here, and getting it wrong hangs the script. Velero's Backup and
  # Restore objects carry finalizers that only the controller clears, so
  # deleting the frozen fixtures while it is scaled to zero blocks forever —
  # `kubectl delete --all` waits for objects nothing will release.
  #
  # So: bring the controller up first, then delete. It reconciles the fixtures
  # for the few seconds before they go, which is harmless because they are on
  # their way out either way.

  # Two locations: the one Velero writes to, and a second pointed at a bucket
  # that exists, so it can be broken later in a way Velero itself notices.
  cat <<EOF | k apply -f - >/dev/null
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata:
  name: default
  namespace: ${NS}
spec:
  provider: aws
  default: true
  objectStorage:
    bucket: radar-demo
  credential:
    name: cloud-credentials
    key: cloud
  config:
    region: us-east-1
    s3Url: http://minio.${MINIO_NS}.svc:9000
    # What the controller writes through vs. what a client is told to read
    # from. Without publicUrl, Velero signs download URLs for the in-cluster
    # host and anything outside the cluster — Radar on a laptop — cannot
    # resolve them.
    publicUrl: http://localhost:30900
    s3ForcePathStyle: "true"
---
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata:
  name: dr-replica
  namespace: ${NS}
spec:
  provider: aws
  objectStorage:
    bucket: radar-dr
  credential:
    name: cloud-credentials
    key: cloud
  config:
    region: us-east-1
    s3Url: http://minio.${MINIO_NS}.svc:9000
    # What the controller writes through vs. what a client is told to read
    # from. Without publicUrl, Velero signs download URLs for the in-cluster
    # host and anything outside the cluster — Radar on a laptop — cannot
    # resolve them.
    publicUrl: http://localhost:30900
    s3ForcePathStyle: "true"
EOF

  k -n "${NS}" scale deployment/velero --replicas=1 >/dev/null
  k -n "${NS}" rollout status deploy/velero --timeout=180s >/dev/null \
    || fail "Velero controller did not become ready"
  ok "Velero controller running"

  step "Removing the frozen fixtures now that something can release them"
  k -n "${NS}" delete restores.velero.io --all --timeout=60s >/dev/null 2>&1 || clear_stuck_finalizers restores.velero.io
  k -n "${NS}" delete backups.velero.io --all --timeout=60s >/dev/null 2>&1 || clear_stuck_finalizers backups.velero.io
  k -n "${NS}" delete schedules.velero.io --all --timeout=60s >/dev/null 2>&1 || true
  ok "Fixtures removed"

  step "Waiting for Velero to validate both storage locations"
  wait_for_bsl default Available
  wait_for_bsl dr-replica Available
  ok "Both locations Available — validated by Velero against MinIO"
}

# A delete that timed out means a finalizer nothing is going to clear. Strip it
# rather than leaving the demo half-torn-down: these are fixtures on their way
# out, and the controller has no work pending on them.
clear_stuck_finalizers() {
  local kind="$1" name
  for name in $(k -n "${NS}" get "${kind}" -o name 2>/dev/null); do
    k -n "${NS}" patch "${name}" --type=merge -p '{"metadata":{"finalizers":null}}' >/dev/null 2>&1 || true
  done
  warn "cleared finalizers on remaining ${kind}"
}

wait_for_bsl() {
  local name="$1" want="$2" i phase
  for i in $(seq 1 60); do
    phase="$(k -n "${NS}" get backupstoragelocations.velero.io "${name}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    [ "${phase}" = "${want}" ] && return 0
    sleep 2
  done
  fail "storage location ${name} never reached ${want} (last: ${phase:-<none>})"
}

wait_for_backup_phase() {
  local name="$1" want="$2" i phase
  for i in $(seq 1 90); do
    phase="$(k -n "${NS}" get backups.velero.io "${name}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    [ "${phase}" = "${want}" ] && return 0
    sleep 2
  done
  fail "backup ${name} never reached ${want} (last: ${phase:-<none>})"
}

produce_real_states() {
  k apply -f "${LIVE_DIR}/10-workload.yaml" >/dev/null
  k -n demo-app rollout status deploy/web --timeout=120s >/dev/null || true

  # 1. A backup Velero really took, into a bucket it can really read.
  step "Taking a real backup of demo-app"
  new_backup live-completed default "24h0m0s" ""
  wait_for_backup_phase live-completed Completed
  ok "live-completed reached Completed — written to MinIO"

  # 2. A real restore of it. Delete the namespace first so the restore has
  #    something to actually do; a no-op restore proves nothing.
  step "Deleting demo-app and restoring it from that backup"
  k delete ns demo-app --wait=true >/dev/null 2>&1 || true
  cat <<EOF | k apply -f - >/dev/null
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: live-restore
  namespace: ${NS}
spec:
  backupName: live-completed
  includedNamespaces: ["demo-app"]
EOF
  wait_for_restore_phase live-restore
  k -n demo-app get cm app-config >/dev/null 2>&1 \
    && ok "demo-app/app-config exists again — the restore recreated real objects" \
    || warn "restore finished but app-config is absent"

  # 2a. A backup that genuinely half-worked.
  #
  #     A pre-backup hook that exits non-zero. Velero runs it, records the
  #     failure against the backup, and finishes the rest — which is exactly
  #     PartiallyFailed: some of it made it, some did not.
  #
  #     Worth its place because `up` can only hand-write this phase: without a
  #     bucket there is no way to half-succeed. With one it is a hook away, and
  #     the difference matters — the fixture asserts the phase, this earns it,
  #     along with item counts, an error count, and a real message behind it.
  step "Taking a backup whose hook fails, for a real PartiallyFailed"
  cat <<EOF | k apply -f - >/dev/null
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: live-partial
  namespace: ${NS}
spec:
  includedNamespaces: ["demo-app"]
  storageLocation: default
  ttl: 24h0m0s
  hooks:
    resources:
      - name: a-hook-that-fails
        includedNamespaces: ["demo-app"]
        labelSelector:
          matchLabels: { app: web }
        pre:
          - exec:
              container: web
              command: ["sh", "-c", "exit 7"]
              # Continue, so the run finishes partway rather than stopping dead —
              # a Failed backup is a different and much less interesting state.
              onError: Continue
EOF
  wait_for_backup_phase live-partial PartiallyFailed
  ok "live-partial reached PartiallyFailed — Velero decided it, no status was written here"

  # 2b. Prove the messages behind the counts are actually reachable.
  #
  #     Radar reads a run's warnings by creating a DownloadRequest and fetching
  #     the pre-signed URL Velero answers with. That URL is signed for the
  #     location's publicUrl, so this checks the whole chain — controller,
  #     signature, NodePort, the host's route to MinIO — from where Radar runs
  #     rather than from inside the cluster.
  #
  #     Done here because it needs the controller, and the next steps take it
  #     away. The demo cannot hold both states at once: a stalled run needs a
  #     stopped controller, and a stopped controller serves no DownloadRequest.
  step "Checking a run's messages can be fetched from outside the cluster"
  k -n "${NS}" delete downloadrequests.velero.io live-partial-results --ignore-not-found >/dev/null 2>&1 || true
  cat <<EOF | k apply -f - >/dev/null
apiVersion: velero.io/v1
kind: DownloadRequest
metadata:
  name: live-partial-results
  namespace: ${NS}
spec:
  target:
    # live-partial, not live-restore: the hook that fails is guaranteed to put a
    # message in the results file. A restore's warnings are incidental — they
    # come from whatever the cluster happened to already have — so asserting a
    # non-empty file against it would fail the whole run on a clean restore.
    kind: BackupResults
    name: live-partial
EOF
  local dl=""
  for i in $(seq 1 30); do
    dl="$(k -n "${NS}" get downloadrequests.velero.io live-partial-results -o jsonpath='{.status.downloadURL}' 2>/dev/null || true)"
    [ -n "${dl}" ] && break
    sleep 1
  done
  [ -n "${dl}" ] || fail "Velero never returned a download URL for live-partial's results"
  case "${dl}" in
    http://localhost:30900/*) ;;
    *) fail "download URL is ${dl%%/velero*}, which nothing outside the cluster can reach — publicUrl is not taking effect" ;;
  esac
  python3 - "${dl}" <<'PYEOF' || fail "the results file could not be fetched or read from the host"
import gzip, io, json, sys, urllib.request
with urllib.request.urlopen(sys.argv[1], timeout=20) as r:
    body = r.read()
data = json.load(gzip.GzipFile(fileobj=io.BytesIO(body)))
count = sum(
    len(v)
    for section in data.values()
    for v in list(section.get("namespaces", {}).values()) + [section.get("velero", []), section.get("cluster", [])]
)
if count == 0:
    raise SystemExit("results file parsed but held no messages")
print(f"      messages in the results file: {count}")
PYEOF
  k -n "${NS}" delete downloadrequests.velero.io live-partial-results --ignore-not-found >/dev/null 2>&1 || true
  ok "Velero served the results file and the host could read it"

  # 3. A backup stored in a location that then genuinely breaks.
  #
  #    The DR bucket is deleted rather than MinIO being stopped. Both locations
  #    share one MinIO, so stopping it marked BOTH Unavailable and no further
  #    backup could be validated. Removing just this bucket leaves `default`
  #    healthy and is closer to what actually happens — a DR target deleted or
  #    its permissions revoked.
  step "Taking a backup into dr-replica, then deleting that bucket underneath it"
  new_backup live-stranded dr-replica "720h0m0s" ""
  wait_for_backup_phase live-stranded Completed
  ok "live-stranded written to radar-dr"

  k -n "${MINIO_NS}" delete job mc-rb --ignore-not-found >/dev/null 2>&1 || true
  cat <<EOF | k apply -f - >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: mc-rb
  namespace: ${MINIO_NS}
spec:
  backoffLimit: 3
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: mc
          image: minio/mc:RELEASE.2024-09-16T17-43-14Z
          command: ["/bin/sh","-c"]
          args:
            - |
              mc alias set local http://minio.${MINIO_NS}.svc:9000 minioadmin minioadmin &&
              mc rb --force local/radar-dr
EOF
  k -n "${MINIO_NS}" wait --for=condition=complete job/mc-rb --timeout=120s >/dev/null || true
  ok "radar-dr bucket deleted — waiting for Velero to notice"
  wait_for_bsl dr-replica Unavailable
  ok "dr-replica marked Unavailable by Velero, still holding a Completed backup"


  # 3b. A run Velero refuses before it starts.
  #
  #     FailedValidation is not a partway failure and needs no working storage
  #     of its own: it is a pre-run rejection, which makes it the cheapest of
  #     these to earn. Point a backup at the location that was just
  #     broken and Velero declines it. This has to run after the break, and it
  #     is also why the stall step below uses the location that is still healthy
  #     — aim a backup that is meant to start at a broken location and Velero
  #     refuses it instead.
  step "Aiming a backup at the broken location, so Velero rejects it outright"
  new_backup live-rejected dr-replica "24h0m0s" ""
  wait_for_backup_phase live-rejected FailedValidation
  ok "live-rejected reached FailedValidation — Velero refused it, nothing was written here"

  # 3c. A schedule Velero runs, and one it never can.
  #
  #     live-schedule fires every two minutes into the healthy location, so the
  #     running controller submits a run and finishes it: the Velero views read
  #     a real lastBackup, a real newest successful run, and a next run computed
  #     the way the controller computes it. Once the stall step below stops the
  #     controller, the same schedule genuinely goes late, which is the state
  #     the views exist to catch.
  #
  #     The second name is 64 characters, one over the label limit. Velero
  #     validates it (it checks only the cron) and then fails every submission,
  #     because it writes the name unshortened into each backup's label.
  step "Creating a schedule Velero runs, and one whose name it cannot label"
  local long_name="sched-name-exceeds-the-sixty-three-character-label-limit-by-one1"
  cat <<EOF | k apply -f - >/dev/null
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: live-schedule
  namespace: ${NS}
spec:
  schedule: "*/2 * * * *"
  template:
    includedNamespaces: ["demo-app"]
    storageLocation: default
    ttl: 1h0m0s
---
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: ${long_name}
  namespace: ${NS}
spec:
  schedule: "* * * * *"
  template:
    includedNamespaces: ["demo-app"]
    storageLocation: default
    ttl: 1h0m0s
EOF
  local run="" i
  for i in $(seq 1 100); do
    run="$(k -n "${NS}" get backups.velero.io -l velero.io/schedule-name=live-schedule \
      -o jsonpath='{range .items[?(@.status.phase=="Completed")]}{.metadata.name}{"\n"}{end}' 2>/dev/null | head -1)"
    [ -n "${run}" ] && break
    sleep 3
  done
  [ -n "${run}" ] || fail "live-schedule produced no Completed run in five minutes"
  ok "live-schedule submitted and completed ${run}"
  local long_phase long_last long_runs
  long_phase="$(k -n "${NS}" get schedules.velero.io "${long_name}" -o jsonpath='{.status.phase}')"
  long_last="$(k -n "${NS}" get schedules.velero.io "${long_name}" -o jsonpath='{.status.lastBackup}')"
  long_runs="$(k -n "${NS}" get backups.velero.io -o name | grep -c "${long_name}" || true)"
  [ "${long_phase}" = "Enabled" ] || fail "the long-named schedule is ${long_phase:-<none>}, expected Enabled — Velero should accept it"
  if [ -n "${long_last}" ] || [ "${long_runs}" != "0" ]; then
    fail "the long-named schedule submitted a run (lastBackup=${long_last:-none}, backups=${long_runs}) — Velero's labelling has changed"
  fi
  ok "the long-named schedule is Enabled and has never submitted a run"

  # 4. A run that genuinely stops moving.
  #
  #    Two Velero mechanisms, no edited clocks. A pre-backup hook execs into the
  #    workload and sleeps, so the run stays in flight for as long as this needs
  #    — a four-object backup otherwise finishes in a second or two, which is
  #    not long enough to reliably catch. And itemOperationTimeout is set to a
  #    minute on this one backup, which is the same field the stall detector
  #    measures against, so the run passes its own declared budget for real.
  #
  #    Last, and only after the storage work: anything that restarts the
  #    controller decides this backup, and the stranded step restarts it. Run
  #    this earlier and the controller comes back, sees a run past its budget,
  #    and marks it Failed.
  step "Starting a backup held open by a backup hook, then removing the controller"
  # The hook execs into this pod, and the restore above recreated it.
  k -n demo-app rollout status deploy/web --timeout=180s >/dev/null \
    || fail "demo-app/web is not running, so the backup hook has nothing to exec into"
  cat <<EOF | k apply -f - >/dev/null
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: live-stalled
  namespace: ${NS}
spec:
  includedNamespaces: ["demo-app"]
  storageLocation: default
  ttl: 24h0m0s
  itemOperationTimeout: 1m0s
  hooks:
    resources:
      - name: hold-the-run-open
        includedNamespaces: ["demo-app"]
        labelSelector:
          matchLabels: { app: web }
        pre:
          - exec:
              container: web
              command: ["sh", "-c", "sleep 600"]
              timeout: 10m
EOF
  local i phase
  for i in $(seq 1 90); do
    phase="$(k -n "${NS}" get backups.velero.io live-stalled -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    case "${phase}" in InProgress|WaitingForPluginOperations|Finalizing) break ;; esac
    sleep 1
  done
  [ "${phase:-}" = "InProgress" ] \
    || fail "live-stalled is ${phase:-<none>}, expected InProgress — the backup hook did not hold the run open"

  # Scale, then wait for the pod to actually go. `kubectl scale` returns as soon
  # as the replica count is written; the controller keeps reconciling until its
  # process exits, and "the controller is gone" is the whole state being built.
  stop_velero_controller "the run would not be stalled — something could still advance it"

  # Only now is the claim true. The run has to actually outlive the budget it
  # declared before anything can say it has — the detector compares
  # startTimestamp against itemOperationTimeout, so until that minute is up
  # Radar correctly reports nothing, and a summary saying otherwise would be
  # describing a state the cluster has not reached yet.
  step "Waiting out the one-minute budget so the run is genuinely past it"
  local started elapsed
  started="$(k -n "${NS}" get backups.velero.io live-stalled -o jsonpath='{.status.startTimestamp}')"
  for i in $(seq 1 40); do
    elapsed="$(python3 -c "
import datetime, sys
started = datetime.datetime.fromisoformat(sys.argv[1].replace('Z', '+00:00'))
print(int((datetime.datetime.now(datetime.timezone.utc) - started).total_seconds()))
" "${started}")"
    [ "${elapsed}" -gt 75 ] && break
    sleep 5
  done
  [ "${elapsed}" -gt 75 ] || fail "live-stalled is only ${elapsed}s in; it has not yet outlived the 1m it declares"

  # Leave the controller down. That is what keeps the stalled run stalled, and
  # it is a state a real cluster reaches on its own.
  ok "controller stopped, run ${elapsed}s in against the 1m it declares"
}

wait_for_restore_phase() {
  local name="$1" i phase
  for i in $(seq 1 90); do
    phase="$(k -n "${NS}" get restores.velero.io "${name}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    case "${phase}" in Completed|PartiallyFailed|Failed|FailedValidation) ok "${name} reached ${phase}"; return 0 ;; esac
    sleep 2
  done
  warn "${name} never reached a terminal phase (last: ${phase:-<none>})"
}

new_backup() {
  local name="$1" location="$2" ttl="$3" timeout="${4:-}"
  {
    cat <<EOF
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: ${name}
  namespace: ${NS}
spec:
  includedNamespaces: ["demo-app"]
  storageLocation: ${location}
  ttl: ${ttl}
EOF
    # An `if`, not `[ ... ] && echo`: under `set -euo pipefail` a trailing test
    # that fails makes the whole group exit non-zero, the pipeline inherits it,
    # and the script dies immediately after creating the backup — with the
    # backup in place, so it looks like the step succeeded.
    if [ -n "${timeout}" ]; then
      echo "  itemOperationTimeout: ${timeout}"
    fi
  } | k apply -f - >/dev/null
}

verify_live() {
  step "Verifying the states were produced by Velero, not written here"
  local phase items errs

  phase="$(k -n "${NS}" get backups.velero.io live-completed -o jsonpath='{.status.phase}')"
  [ "${phase}" = "Completed" ] || fail "live-completed is ${phase}, want Completed"
  items="$(k -n "${NS}" get backups.velero.io live-completed -o jsonpath='{.status.progress.itemsBackedUp}')"
  [ -n "${items}" ] && [ "${items}" -gt 0 ] || fail "live-completed backed up no items — nothing was really written"
  ok "live-completed: ${items} items, counted by Velero"

  phase="$(k -n "${NS}" get restores.velero.io live-restore -o jsonpath='{.status.phase}')"
  [ "${phase}" = "Completed" ] || fail "live-restore is ${phase}, want Completed"
  k -n demo-app get cm app-config >/dev/null 2>&1 \
    || fail "live-restore reports Completed but the object it restored is absent"
  ok "live-restore: Completed, and the ConfigMap it recreated is present"

  phase="$(k -n "${NS}" get backups.velero.io live-partial -o jsonpath='{.status.phase}')"
  [ "${phase}" = "PartiallyFailed" ] || fail "live-partial is ${phase}, want PartiallyFailed"
  errs="$(k -n "${NS}" get backups.velero.io live-partial -o jsonpath='{.status.errors}')"
  [ "${errs:-0}" -gt 0 ] || fail "live-partial is PartiallyFailed but reports no errors — nothing for the messages view to fetch"
  ok "live-partial: PartiallyFailed with ${errs} error from a hook Velero really ran"

  phase="$(k -n "${NS}" get backupstoragelocations.velero.io dr-replica -o jsonpath='{.status.phase}')"
  [ "${phase}" = "Unavailable" ] || fail "dr-replica is ${phase}, want Unavailable"
  ok "dr-replica Unavailable, decided by Velero's own validation"

  phase="$(k -n "${NS}" get backups.velero.io live-stranded -o jsonpath='{.status.phase}')"
  [ "${phase}" = "Completed" ] || fail "live-stranded is ${phase}, want Completed"
  ok "live-stranded Completed but stored in an Unavailable location"

  phase="$(k -n "${NS}" get backups.velero.io live-rejected -o jsonpath='{.status.phase}')"
  [ "${phase}" = "FailedValidation" ] || fail "live-rejected is ${phase}, want FailedValidation"
  [ -n "$(k -n "${NS}" get backups.velero.io live-rejected -o jsonpath='{.status.validationErrors[0]}')" ] \
    || fail "live-rejected has no validationErrors — the reason Velero refused it is what the screen renders"
  ok "live-rejected: FailedValidation, with Velero's own reason attached"

  phase="$(k -n "${NS}" get backups.velero.io live-stalled -o jsonpath='{.status.phase}')"
  case "${phase}" in
    InProgress|WaitingForPluginOperations|Finalizing)
      ok "live-stalled stuck in ${phase}, longer than the 1m it allows one operation" ;;
    Completed)
      # Not a warning. A warning here reports success from a run that produced
      # every state but this one, and this is the only evidence the stall
      # detector has ever been exercised against a controller that really
      # stopped.
      fail "live-stalled reached Completed — the backup hook did not hold the run open" ;;
    *)
      fail "live-stalled is ${phase}; expected an in-flight phase" ;;
  esac

  # The stall detector reads exactly these two fields. Without both, the state
  # above is an in-flight backup that proves nothing about the feature: no
  # startTimestamp means Velero never picked the run up, and no
  # itemOperationTimeout means the message falls back to the 4h built-in, which
  # would not be reached for another four hours.
  [ -n "$(k -n "${NS}" get backups.velero.io live-stalled -o jsonpath='{.status.startTimestamp}')" ] \
    || fail "live-stalled has no startTimestamp — the stall detector anchors on it and will stay silent"
  [ -n "$(k -n "${NS}" get backups.velero.io live-stalled -o jsonpath='{.spec.itemOperationTimeout}')" ] \
    || fail "live-stalled has no itemOperationTimeout — the message would fall back to the 4h built-in"
  ok "live-stalled carries the startTimestamp and the timeout the message quotes"

  # The replica count is not the question — a terminating pod still reconciles,
  # and Velero's grace period is an hour. Ask whether a pod exists.
  local vsel
  vsel="$(k -n "${NS}" get deployment velero \
    -o go-template='{{range $k, $v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}' 2>/dev/null | sed 's/,$//')"
  [ -z "$(k -n "${NS}" get pods -l "${vsel:-app.kubernetes.io/name=velero}" -o name 2>/dev/null)" ] \
    || fail "a velero controller pod is still running, so the in-flight run is not stalled"
  ok "no controller pod, so the in-flight run cannot advance"
}

print_live_summary() {
  echo
  step "Ready (live)"
  note "context:  ${KUBECTL_CTX}"
  note "point Radar at it:  kubectl config use-context ${KUBECTL_CTX} && make restart"
  echo
  note "Produced by Velero itself:"
  note "  live-completed   Completed, item counts from a real backup into MinIO"
  note "  live-restore     a restore that recreated demo-app"
  note "  live-stranded    Completed, in a location Velero then marked Unavailable"
  note "  live-partial     PartiallyFailed, from a pre-backup hook that really failed"
  note "  live-rejected    FailedValidation, refused against the broken location"
  note "  live-stalled     in flight with the controller removed underneath it"
  note "  live-schedule    real runs every two minutes, late once the controller stopped"
  note "  sched-name-…one1 Enabled, and never able to submit (64-character name)"
  echo
  note "Absent in both modes (see README): the data-mover objects, which need a"
  note "  snapshot-capable CSI driver as well as a bucket."
  note "Run './scripts/velero-demo.sh reset' to go back to the frozen fixture set."
}

cmd_verify() { verify_fixtures; }

# --- Status ----------------------------------------------------------------

cmd_status() {
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist — run: $0 up"
  step "Cluster '${CLUSTER_NAME}'"
  note "$(k -n "${NS}" get deployment velero -o jsonpath='velero controller replicas={.spec.replicas} (0 is correct)' 2>/dev/null || echo 'velero deployment not found')"
  for kind in backups restores schedules backupstoragelocations volumesnapshotlocations backuprepositories; do
    printf "${C_DIM}    %-26s %s${C_RESET}\n" "${kind}" \
      "$(k get "${kind}.velero.io" -n "${NS}" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  done
  echo
  step "Backup phases"
  k get backups.velero.io -n "${NS}" \
    -o custom-columns='NAME:.metadata.name,PHASE:.status.phase,SCHEDULE:.metadata.labels.velero\.io/schedule-name' 2>/dev/null
  echo
  step "Schedules"
  k get schedules.velero.io -n "${NS}" \
    -o custom-columns='NAME:.metadata.name,PHASE:.status.phase,PAUSED:.spec.paused,CRON:.spec.schedule' 2>/dev/null
}

print_summary() {
  echo
  step "Ready"
  note "context:  ${KUBECTL_CTX}"
  note "point Radar at it:  kubectl config use-context ${KUBECTL_CTX} && make restart"
  note "coverage matrix:    scripts/velero-demo/README.md"
  note "inventory:          $0 status"
}

case "${1:-help}" in
  up)     cmd_up ;;
  live)   cmd_live ;;
  down)   cmd_down ;;
  reset)  cmd_reset ;;
  status) cmd_status ;;
  verify) cmd_verify ;;
  help|-h|--help) sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d' ;;
  *) fail "Unknown subcommand: $1 (try: $0 help)" ;;
esac
