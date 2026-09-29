#!/usr/bin/env bash
# Bootstrap a kind cluster pre-populated with CloudNativePG scenarios for
# visual-testing Radar's CNPG surfaces. Idempotent — re-run to refresh state
# or apply fixture updates without recreating the cluster.
#
# Subcommands:
#   up        Create cluster (if missing), install CNPG, apply fixtures,
#             induce a real WAL-archiving failure, freeze + patch terminal states.
#   down      Delete the kind cluster.
#   live      Same as up, but leaves the operator RUNNING. Terminal phases are
#             skipped (the operator would reconcile them away). Use this for
#             failover / switchover / backup work that needs a live controller.
#   refreeze  Re-apply the freeze + terminal phases (after a `live` run, or if
#             something reconciled them away).
#   thaw      Remove frozen-only Backup fixtures and scale the operator back up.
#   status    Inventory the cluster and show what each fixture is doing.
#   runtime   On the EXISTING cluster, with the operator live (thawed if
#             frozen): add namespace `pgrt` with MinIO, a 3-instance cluster
#             archiving to it, a real completed Backup, a restored cluster, a
#             Pooler, pgbench load + a blocked lock chain, and a Prometheus in
#             `monitoring` that Radar auto-discovers.
#   runtime-lag on|off
#             Pause (on) / resume (off) WAL replay on a pg-runtime replica so
#             replay lag grows under the pgbench load.
#   runtime-down
#             Remove the runtime namespaces (pgrt, monitoring) only.
#   help      Show this message.
#
# Prerequisites:
#   - kind         https://kind.sigs.k8s.io/
#   - kubectl
#   - python3
#
# Set CLUSTER_NAME=foo to use a different cluster (default: radar-cnpg-demo).
#
# See scripts/cnpg-demo/README.md for the coverage matrix and — more
# importantly — why some states are patched and others must be real.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-cnpg-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="${SCRIPT_DIR}/cnpg-demo"
# A subdirectory, so apply_fixtures' maxdepth-1 scan never picks these up.
RUNTIME_DIR="${FIXTURES_DIR}/runtime"
NS=pg
RT_NS=pgrt
MON_NS=monitoring

# Pinned so the demo behaves consistently. The phase STRINGS Radar matches on
# are version-sensitive — they are full English sentences from CNPG's
# api/v1/cluster_types.go, matched on equality — so bumping this can silently
# move a cluster into the "unrecognised phase" bucket. Bump deliberately and
# re-check the badges.
CNPG_VERSION="${CNPG_VERSION:-1.27.0}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.16.2}"
BARMAN_PLUGIN_VERSION="${BARMAN_PLUGIN_VERSION:-v0.14.0}"
CNPG_MANIFEST="https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.27/releases/cnpg-${CNPG_VERSION}.yaml"

PHASE_UNRECOVERABLE="Cluster is unrecoverable and needs manual intervention"

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

k() { kubectl --context "${KUBECTL_CTX}" "$@"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 not found in PATH. Install: $2"
}

cluster_exists() { kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; }

# --- Lifecycle -------------------------------------------------------------

cmd_up()   { bootstrap; freeze_and_patch; assert_healthy_baseline; print_summary "frozen"; }
cmd_live() { bootstrap; assert_no_fixture_backups; print_summary "live"; }

bootstrap() {
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation (or 'brew install kind')"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd python3 "https://www.python.org/downloads/"

  if cluster_exists; then
    step "Cluster '${CLUSTER_NAME}' already exists — reusing"
    delete_fixture_backups
    unpin_unresolvable_catalog
    thaw_operator_quiet
  else
    step "Creating kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --wait 60s
    ok "Cluster created"
  fi

  k cluster-info >/dev/null || fail "kind context not reachable"

  install_cnpg
  install_barman_plugin
  apply_fixtures
  wait_for_clusters
  wait_for_pooler
  break_wal_archiving
  # Velero's CRD has no controller here, so nothing will reconcile this away.
  patch_velero_backup
  # CNPG Backup phases are deliberately NOT patched here — the operator owns
  # them and drives a freshly created Backup to `running` within seconds.
  # They are written after the freeze, alongside the terminal phases.
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

# --- CNPG operator ---------------------------------------------------------

install_cnpg() {
  step "Installing CloudNativePG ${CNPG_VERSION}"
  k apply --server-side -f "${CNPG_MANIFEST}" >/dev/null
  step "Waiting for the operator to be Ready (~60s)"
  k -n cnpg-system rollout status deployment/cnpg-controller-manager --timeout=180s >/dev/null
  ok "CNPG healthy"
}

# --- Fixtures --------------------------------------------------------------

# Everything except the Backups, which are deliberately held back until after
# the freeze. A Backup created against a running operator is not just
# reconciled to `running` — the operator ATTEMPTS it, and a Backup pointed at a
# cluster with no backup configuration fails and stamps
# LastBackupSucceeded=False on that cluster. That turned the pristine baseline
# into a "Backup Failed" badge, silently, via a resource attached to it.
# The barman-cloud CNPG-I plugin — the backup path CNPG is migrating to.
#
# Installed rather than faked because the whole point of the fixture is the
# ObjectStore CR, and its CRD only exists once the plugin is deployed. Needs
# cert-manager: the plugin talks to the operator over mTLS and its manifest
# ships Certificate/Issuer resources.
#
# Set SKIP_BARMAN_PLUGIN=1 to bootstrap without it — the 09 fixture is then
# skipped and the in-tree barmanObjectStore clusters still cover everything
# they covered before.
install_barman_plugin() {
  if [ "${SKIP_BARMAN_PLUGIN:-0}" = "1" ]; then
    note "SKIP_BARMAN_PLUGIN=1 — skipping the barman-cloud plugin"
    return 0
  fi
  if k get deploy -n cnpg-system barman-cloud >/dev/null 2>&1; then
    step "barman-cloud plugin already installed — reusing"
    return 0
  fi

  step "Installing cert-manager ${CERT_MANAGER_VERSION} (barman-cloud plugin prerequisite)"
  k apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml" >/dev/null
  for d in cert-manager cert-manager-webhook cert-manager-cainjector; do
    k -n cert-manager rollout status "deploy/${d}" --timeout=180s >/dev/null
  done
  ok "cert-manager rolled out"

  # A rolled-out webhook is NOT a ready webhook. cert-manager's CA bundle is
  # injected into its ValidatingWebhookConfiguration asynchronously, and until
  # that lands every Certificate/Issuer apply fails with
  #   x509: certificate signed by unknown authority
  # The plugin manifest contains exactly those kinds, so applying it the moment
  # the Deployment reports Available loses the race — and only partially: the
  # CRD and Deployment get created while the certs do not, leaving a plugin that
  # looks installed and cannot serve. Retry until the API actually accepts it.
  step "Installing barman-cloud plugin ${BARMAN_PLUGIN_VERSION}"
  local manifest="https://github.com/cloudnative-pg/plugin-barman-cloud/releases/download/${BARMAN_PLUGIN_VERSION}/manifest.yaml"
  local applied=0
  for _ in $(seq 1 30); do
    if k apply -f "${manifest}" >/dev/null 2>&1; then
      applied=1
      break
    fi
    sleep 5
  done
  [ "${applied}" = "1" ] || fail "barman-cloud plugin manifest never applied — cert-manager webhook did not become ready"
  k -n cnpg-system rollout status deploy/barman-cloud --timeout=180s >/dev/null
  k wait --for=condition=Established crd/objectstores.barmancloud.cnpg.io --timeout=60s >/dev/null
  ok "barman-cloud plugin ready"
}

# Recovery windows and the Cluster's plugin reference.
#
# `status.serverRecoveryWindow` cannot be declared in the fixture YAML, and no
# real window can be produced on kind — there is no object storage to back up
# to, and a plugin pointed at an unreachable endpoint reports failures rather
# than a window. Same rationale as the Velero fixtures: the states worth
# rendering are written directly.
#
# pg-store       one clean server + one failing-since-last-success
# pg-store-fresh left empty on purpose — "configured but nothing restorable",
#                the state a plugin-backed Cluster can no longer report itself
#
# The plugin reference goes on pg-doomed, NOT on pg-healthy, and that is not
# arbitrary. `spec.plugins` is applied after the freeze, so the operator never
# rolls the instance pods with the plugin's sidecar, and every instance manager
# on that cluster then reports `ContinuousArchiving=False — wal archive plugin
# is not available`. On pg-healthy that turns the healthy baseline into a second
# WAL-archiving failure and the fixture's whole claim — four clusters, four
# different badges — stops being true. pg-doomed is already terminal, and a
# terminal phase outranks the condition, so its badge does not move.
wire_object_stores() {
  k get crd objectstores.barmancloud.cnpg.io >/dev/null 2>&1 || return 0

  step "Wiring ObjectStore recovery windows"
  patch_status objectstores.barmancloud.cnpg.io pg-store \
    '{"status":{"serverRecoveryWindow":{"pg-doomed":{"firstRecoverabilityPoint":"2026-07-29T02:00:00Z","lastSuccessfulBackupTime":"2026-08-12T02:00:00Z"},"pg-wal-failing":{"firstRecoverabilityPoint":"2026-07-30T02:00:00Z","lastSuccessfulBackupTime":"2026-08-05T02:00:00Z","lastFailedBackupTime":"2026-08-12T02:00:00Z"}}}}'
  ok "pg-store → one recoverable server, one failing"
  note "pg-store-fresh left with no window — configured, nothing restorable"

  step "Pointing pg-doomed at the plugin"
  k -n pg patch clusters.postgresql.cnpg.io pg-doomed --type=merge \
    -p '{"spec":{"plugins":[{"name":"barman-cloud.cloudnative-pg.io","isWALArchiver":true,"parameters":{"barmanObjectName":"pg-store"}}]}}' >/dev/null
  ok "pg-doomed → spec.plugins[barman-cloud]"
}

apply_fixtures() {
  step "Applying CNPG demo fixtures"
  [ -d "${FIXTURES_DIR}" ] || fail "Fixtures dir not found: ${FIXTURES_DIR}"

  while IFS= read -r f; do
    case "$(basename "$f")" in
      07-backups.yaml) note "deferring $(basename "$f") until after the freeze"; continue ;;
      09-objectstores.yaml)
        if ! k get crd objectstores.barmancloud.cnpg.io >/dev/null 2>&1; then
          note "skipping $(basename "$f") — barman-cloud plugin not installed"
          continue
        fi
        ;;
    esac
    note "applying $(basename "$f")"
    k apply -f "$f" >/dev/null
    # A CRD is accepted before it is served. 08-collision-instances.yaml creates
    # CRs of the two kinds 02-collision-crds.yaml defines, so without this the
    # instance apply can fail with "no matches for kind" on a slow API server —
    # and set -e turns that into a failed bootstrap. The Velero fixture waits on
    # its own collision CRD for the same reason.
    case "$(basename "$f")" in
      02-collision-crds.yaml)
        k wait --for=condition=Established crd/clusters.apps.kubeblocks.io --timeout=60s >/dev/null
        k wait --for=condition=Established crd/backups.velero.io --timeout=60s >/dev/null
        ;;
    esac
  done < <(find "${FIXTURES_DIR}" -maxdepth 1 -type f -name '*.yaml' | sort)
  ok "Fixtures applied"
}

wait_for_clusters() {
  step "Waiting for Postgres to come up (4 clusters × 2 instances, ~3 min first run)"
  local c deadline
  for c in pg-healthy pg-wal-failing pg-unrecoverable pg-doomed; do
    deadline=$((SECONDS + 420))
    while [ $SECONDS -lt $deadline ]; do
      local ready
      ready=$(k -n "${NS}" get cluster.postgresql.cnpg.io "$c" \
        -o jsonpath='{.status.readyInstances}' 2>/dev/null || echo "")
      [ "${ready:-0}" = "2" ] && break
      sleep 5
    done
    if [ "${ready:-0}" = "2" ]; then
      ok "$c 2/2"
    else
      fail "$c did not reach 2/2 within 7 minutes — check 'kubectl -n ${NS} get pods'"
    fi
  done
}

# The Pooler must report status.instances before the freeze, or its badge
# renders "Unknown" (absent is not zero) instead of the "Scheduled" state this
# fixture exists to show. A Pooler that never reconciles surfaces nothing on
# the CR — no status, no condition, no event — so a silent failure here looks
# exactly like slowness. Fail loudly with the operator's own reason instead.
wait_for_pooler() {
  step "Waiting for the Pooler to report"
  local deadline=$((SECONDS + 180)) scheduled=""
  while [ $SECONDS -lt $deadline ]; do
    scheduled=$(k -n "${NS}" get pooler.postgresql.cnpg.io pg-healthy-pooler \
      -o jsonpath='{.status.instances}' 2>/dev/null || echo "")
    [ -n "$scheduled" ] && break
    sleep 5
  done

  if [ -n "$scheduled" ]; then
    ok "pg-healthy-pooler status.instances=${scheduled}"
  else
    note "most likely cause: a Service name collision. Check:"
    note "  kubectl -n cnpg-system logs deploy/cnpg-controller-manager | grep notOwnedServiceName"
    fail "Pooler never reported status.instances — the fixture would render Unknown"
  fi
}

# --- The WAL-archiving failure, made real ----------------------------------
#
# This is the fixture that cannot be faked. See the README: patching
# ContinuousArchiving=False does not survive, because the instance manager
# inside each Postgres pod writes cluster status independently of the operator
# and reverts it within ~10 minutes — even with the operator scaled to zero.
#
# So instead of asserting the failure we cause it, and the ORDER matters. CNPG
# verifies the object store during bootstrap, so a cluster created with an
# unroutable endpoint never finishes coming up: it sits in "Waiting for the
# instances to become active" forever, with ContinuousArchiving=False for the
# wrong reason. Bootstrap clean, then break archiving on a healthy cluster.
break_wal_archiving() {
  step "Breaking WAL archiving on pg-wal-failing (after it came up clean)"

  local primary
  primary=$(k -n "${NS}" get cluster.postgresql.cnpg.io pg-wal-failing \
    -o jsonpath='{.status.currentPrimary}' 2>/dev/null || echo "")
  if [ -z "$primary" ]; then
    fail "pg-wal-failing has no primary after reaching 2/2"
  fi

  # Point it at an endpoint that never answers. TEST-NET-1 (RFC 5737) is
  # reserved for documentation and is unroutable by design.
  k -n "${NS}" patch cluster.postgresql.cnpg.io pg-wal-failing --type=merge -p '{
    "spec": {"backup": {"barmanObjectStore": {
      "endpointURL": "http://192.0.2.1:9000",
      "destinationPath": "s3://radar-cnpg-demo/",
      "s3Credentials": {
        "accessKeyId": {"name": "cnpg-demo-object-store", "key": "ACCESS_KEY_ID"},
        "secretAccessKey": {"name": "cnpg-demo-object-store", "key": "ACCESS_SECRET_KEY"}
      }
    }}}
  }' >/dev/null || fail "could not attach the object store"

  # Give the archiver a segment to fail on. An idle cluster may not rotate on
  # its own, and an empty segment can be skipped.
  k -n "${NS}" exec "$primary" -c postgres -- psql -q -U postgres -d postgres -c \
    "CREATE TABLE IF NOT EXISTS radar_demo_churn AS SELECT g, repeat('x', 512) AS pad FROM generate_series(1, 20000) g;" \
    >/dev/null 2>&1 || warn "seed write failed (continuing)"
  k -n "${NS}" exec "$primary" -c postgres -- psql -q -U postgres -d postgres -c \
    "SELECT pg_switch_wal();" >/dev/null 2>&1 || warn "pg_switch_wal failed (continuing)"

  # Both halves must hold, and the readiness half is the one that catches a
  # cluster broken for the wrong reason: ContinuousArchiving=False on a cluster
  # that never came up looks identical here but is a different — and useless —
  # fixture. Asserting only the condition would have reported success on it.
  local deadline=$((SECONDS + 240)) archiving="" ready=""
  while [ $SECONDS -lt $deadline ]; do
    archiving=$(k -n "${NS}" get cluster.postgresql.cnpg.io pg-wal-failing \
      -o jsonpath='{range .status.conditions[?(@.type=="ContinuousArchiving")]}{.status}{end}' 2>/dev/null || echo "")
    ready=$(k -n "${NS}" get cluster.postgresql.cnpg.io pg-wal-failing \
      -o jsonpath='{.status.readyInstances}' 2>/dev/null || echo "")
    [ "$archiving" = "False" ] && [ "${ready:-0}" = "2" ] && break
    sleep 5
  done

  if [ "$archiving" = "False" ] && [ "${ready:-0}" = "2" ]; then
    ok "ContinuousArchiving=False at ${ready}/2 Ready — healthy cluster, broken recovery point"
  else
    fail "wanted ContinuousArchiving=False at 2/2; got '${archiving:-unset}' at '${ready:-unset}'/2"
  fi
}

# --- Statuses that need the operator out of the way ------------------------

# Timestamps are computed relative to now, never hardcoded. A fixed date rots:
# a Velero backup stamped with a literal expiry renders "Expired" a few months
# later, and "Started" ages into meaninglessness — so the fixture quietly stops
# demonstrating the thing it was built to demonstrate.
# Python keeps the offset calculation identical on macOS and Linux.
ts_offset() {
  local spec="$1"
  python3 - "${spec}" <<'PY'
import datetime
import re
import sys

match = re.fullmatch(r"([+-])(\d+)([HMd])", sys.argv[1])
if not match:
    raise SystemExit(f"invalid timestamp offset: {sys.argv[1]}")

sign, amount, unit = match.groups()
field = {"H": "hours", "M": "minutes", "d": "days"}[unit]
delta = datetime.timedelta(**{field: int(amount)})
now = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
timestamp = now - delta if sign == "-" else now + delta
print(timestamp.strftime("%Y-%m-%dT%H:%M:%SZ"))
PY
}

patch_backup_phases() {
  local started stopped
  started=$(ts_offset -4H)
  stopped=$(ts_offset -3H)

  patch_status backups.postgresql.cnpg.io pg-backup-ok \
    "{\"status\":{\"phase\":\"completed\",\"startedAt\":\"${started}\",\"stoppedAt\":\"${stopped}\",\"method\":\"barmanObjectStore\"}}"
  patch_status backups.postgresql.cnpg.io pg-backup-wal-broken \
    "{\"status\":{\"phase\":\"walArchivingFailing\",\"startedAt\":\"${started}\",\"method\":\"barmanObjectStore\",\"error\":\"WAL archiving is failing, cannot start a new backup\"}}"
  patch_status backups.postgresql.cnpg.io pg-backup-unknown-phase \
    "{\"status\":{\"phase\":\"invalidBackupDefinitionRejected\",\"startedAt\":\"${started}\",\"method\":\"barmanObjectStore\"}}"
  ok "3 Backup phases set"
}

patch_velero_backup() {
  local started completed expires
  started=$(ts_offset -6H)
  completed=$(ts_offset -5H)
  expires=$(ts_offset +25d)
  patch_status backups.velero.io velero-nightly-cluster \
    "{\"status\":{\"phase\":\"Completed\",\"startTimestamp\":\"${started}\",\"completionTimestamp\":\"${completed}\",\"expiration\":\"${expires}\",\"errors\":0}}"
  ok "Velero Backup status set (collision row)"
}

# Points pg-unrecoverable at a major version its catalog does not carry — the
# state CNPG reports as "incomplete or invalid image catalog".
#
# This cannot be a fixture. A cluster created with an unresolvable catalog never
# gets an image and never reaches 2/2 Ready, and every other thing this cluster
# demonstrates depends on it having bootstrapped first. So it runs after the
# freeze, on a cluster that is already up and already terminal, where nothing
# will reconcile it back.
pin_unresolvable_catalog() {
  step "Pinning pg-unrecoverable to a catalog version that does not exist"
  # `remove` on an absent path is a 422, so the op is only included when there is
  # something to remove — `refreeze` runs this a second time on a cluster that is
  # already pinned.
  local ops='{"op":"add","path":"/spec/imageCatalogRef","value":{"apiGroup":"postgresql.cnpg.io","kind":"ClusterImageCatalog","name":"postgres-fleet","major":15}}'
  if [ -n "$(k -n "${NS}" get clusters.postgresql.cnpg.io pg-unrecoverable -o jsonpath='{.spec.imageName}' 2>/dev/null)" ]; then
    ops='{"op":"remove","path":"/spec/imageName"},'"${ops}"
  fi
  k -n "${NS}" patch clusters.postgresql.cnpg.io pg-unrecoverable --type=json -p "[${ops}]" \
    >/dev/null || fail "could not pin pg-unrecoverable to postgres-fleet"
  ok "pg-unrecoverable → ClusterImageCatalog/postgres-fleet major 15 (absent)"
}

# The inverse, for a re-run over an existing cluster. `imageName` and
# `imageCatalogRef` are mutually exclusive and the CRD enforces it, so
# re-applying 05-clusters.yaml over a pinned pg-unrecoverable is rejected — the
# two fields have to be swapped in one patch, not left overlapping.
unpin_unresolvable_catalog() {
  [ -n "$(k -n "${NS}" get clusters.postgresql.cnpg.io pg-unrecoverable \
    -o jsonpath='{.spec.imageCatalogRef.name}' 2>/dev/null)" ] || return 0
  # Taken from a cluster that still declares one, so the tag has exactly one
  # source of truth: 05-clusters.yaml.
  local image
  image=$(k -n "${NS}" get clusters.postgresql.cnpg.io pg-healthy -o jsonpath='{.spec.imageName}' 2>/dev/null)
  [ -n "${image}" ] || fail "pg-healthy declares no spec.imageName — cannot unpin pg-unrecoverable"
  k -n "${NS}" patch clusters.postgresql.cnpg.io pg-unrecoverable --type=json -p \
    '[{"op":"remove","path":"/spec/imageCatalogRef"},
      {"op":"add","path":"/spec/imageName","value":"'"${image}"'"}]' \
    >/dev/null || fail "could not unpin pg-unrecoverable before re-applying fixtures"
  ok "pg-unrecoverable unpinned so the fixtures re-apply cleanly"
}

patch_status() {
  local resource="$1" name="$2" patch="$3"
  k -n "${NS}" patch "$resource" "$name" --subresource=status --type=merge -p "$patch" >/dev/null 2>&1 \
    || fail "could not patch $resource/$name"
}

# freeze_and_patch scales the operator to zero and then writes the terminal
# phases. Order matters: with the operator running, a patched phase is
# reconciled back to the truth within seconds.
#
# The webhooks must go too — CNPG serves its own admission webhooks from the
# controller pod, so with zero replicas every write to a CNPG resource fails
# with a connection error instead of being admitted.
freeze_and_patch() {
  step "Freezing the operator so terminal phases persist"

  k delete validatingwebhookconfiguration cnpg-validating-webhook-configuration --ignore-not-found >/dev/null
  k delete mutatingwebhookconfiguration cnpg-mutating-webhook-configuration --ignore-not-found >/dev/null
  k -n cnpg-system scale deployment/cnpg-controller-manager --replicas=0 >/dev/null
  local pods=1
  for _ in $(seq 1 60); do
    pods=$(k -n cnpg-system get pods -l app.kubernetes.io/name=cloudnative-pg --no-headers 2>/dev/null | wc -l | tr -d ' ')
    [ "${pods}" = "0" ] && break
    sleep 2
  done
  [ "${pods}" = "0" ] || fail "${pods} CNPG operator pod(s) still present after 2 minutes"
  ok "Operator scaled to 0, webhooks removed"

  step "Setting terminal phases"
  local c
  for c in pg-unrecoverable pg-doomed; do
    patch_status clusters.postgresql.cnpg.io "$c" \
      "{\"status\":{\"phase\":\"${PHASE_UNRECOVERABLE}\"}}"
    ok "$c → ${PHASE_UNRECOVERABLE}"
  done

  pin_unresolvable_catalog

  # Backups are created here, not with the other fixtures — see apply_fixtures.
  step "Creating Backups and setting their phases"
  k apply -f "${FIXTURES_DIR}/07-backups.yaml" >/dev/null
  patch_backup_phases

  wire_object_stores

  note "Instance managers keep running, so these clusters stay 2/2 Ready —"
  note "which is the point: a red badge on a row whose counts look fine."
}

assert_healthy_baseline() {
  local failed
  failed=$(k -n "${NS}" get cluster.postgresql.cnpg.io pg-healthy \
    -o jsonpath='{range .status.conditions[?(@.type=="LastBackupSucceeded")]}{.status}{end}' 2>/dev/null || echo "")
  [ "${failed}" != "False" ] \
    || fail "pg-healthy has LastBackupSucceeded=False — reset the demo cluster"

  # The fixture's headline claim is four clusters with four DIFFERENT badges, and
  # pg-healthy is the only one holding the healthy one. Anything that gives it an
  # archiving failure — a plugin reference it cannot run, most easily — collapses
  # two of the four badges into one without failing anything else.
  local archiving
  archiving=$(k -n "${NS}" get cluster.postgresql.cnpg.io pg-healthy \
    -o jsonpath='{range .status.conditions[?(@.type=="ContinuousArchiving")]}{.status}{end}' 2>/dev/null || echo "")
  [ "${archiving}" != "False" ] \
    || fail "pg-healthy reports ContinuousArchiving=False — it is meant to be the only Healthy badge"
  ok "pg-healthy baseline is clean on both backup conditions"
}

assert_no_fixture_backups() {
  local remaining
  remaining=$(fixture_backup_count)
  [ "${remaining}" = "0" ] || fail "${remaining} frozen-only Backup fixture(s) remain while the operator is live"
}

fixture_backup_count() {
  local count=0 name
  for name in pg-backup-ok pg-backup-wal-broken pg-backup-unknown-phase; do
    if k -n "${NS}" get backup.postgresql.cnpg.io "${name}" >/dev/null 2>&1; then
      count=$((count + 1))
    fi
  done
  printf '%s\n' "${count}"
}

delete_fixture_backups() {
  local remaining
  remaining=$(fixture_backup_count)
  [ "${remaining}" = "0" ] && return 0

  step "Removing frozen-only Backup fixtures before the operator starts"
  k -n "${NS}" delete backups.postgresql.cnpg.io \
    pg-backup-ok pg-backup-wal-broken pg-backup-unknown-phase \
    --ignore-not-found --wait=false >/dev/null

  for _ in $(seq 1 30); do
    remaining=$(fixture_backup_count)
    [ "${remaining}" = "0" ] && break
    sleep 1
  done
  [ "${remaining}" = "0" ] \
    || fail "Backup fixtures are stuck deleting; run '$0 down' and rebuild the demo"
  ok "Backup fixtures removed"
}

cmd_refreeze() {
  require_cmd python3 "https://www.python.org/downloads/"
  freeze_and_patch
  assert_healthy_baseline
}

# Thawing is NOT just scaling back up. The operator builds its PKI at startup
# by looking up its own webhook configurations, so with them deleted it fails
# `ensurePKI` and crash-loops:
#
#   unable to setup PKI infrastructure ...
#   mutatingwebhookconfigurations "cnpg-mutating-webhook-configuration" not found
#
# Re-applying the install manifest restores them, and is idempotent.
thaw_operator() {
  step "Restoring webhook configurations and scaling the operator back up"
  k apply --server-side -f "${CNPG_MANIFEST}" >/dev/null
  k -n cnpg-system scale deployment/cnpg-controller-manager --replicas=1 >/dev/null
  k -n cnpg-system rollout status deployment/cnpg-controller-manager --timeout=180s >/dev/null
  ok "Operator running"
}

thaw_operator_quiet() {
  k -n cnpg-system get deployment/cnpg-controller-manager >/dev/null 2>&1 || return 0
  local replicas
  replicas=$(k -n cnpg-system get deployment/cnpg-controller-manager -o jsonpath='{.spec.replicas}')
  [ "$replicas" = "0" ] || return 0
  note "operator was frozen — thawing so fixtures can be applied"
  thaw_operator
}

cmd_thaw() {
  delete_fixture_backups
  thaw_operator
  warn "Terminal phases will be reconciled away within seconds. Run '$0 refreeze' to restore them."
}

# --- Runtime mode ----------------------------------------------------------
#
# Everything the frozen matrix cannot show because it needs a working
# controller AND working object storage: a Backup the operator actually took,
# ContinuousArchiving=True for a real reason, a cluster restored from that
# backup, live sessions, a lock chain, replica lag and Prometheus metrics.
#
# It lives in its own namespace and never touches the `pg` fixtures' manifests —
# but it does leave the operator RUNNING, which the frozen terminal phases in
# `pg` do not survive. `refreeze` restores them, at the cost of pgrt's operator-
# driven behaviour (scheduled backups, failover); archiving itself continues,
# because the plugin sidecar talks to the instance manager, not the operator.

# CNPG serves its own admission webhooks, so every CNPG write fails while the
# operator pod restarts — and on a loaded kind node it does restart (lost
# leader-election lease), usually right after a thaw. Retry rather than fail.
rt_apply() {
  local f="$1" out=""
  for _ in $(seq 1 24); do
    out=$(k apply -f "$f" 2>&1) && return 0
    sleep 5
  done
  printf '%s\n' "$out" >&2
  fail "could not apply $(basename "$f")"
}

rt_psql() {
  # $1 pod, $2 SQL. Runs as postgres over the local socket in the app database.
  k -n "${RT_NS}" exec "$1" -c postgres -- psql -q -U postgres -d app -tAc "$2"
}

rt_primary() {
  k -n "${RT_NS}" get cluster.postgresql.cnpg.io pg-runtime -o jsonpath='{.status.currentPrimary}' 2>/dev/null || true
}

rt_condition() {
  k -n "${RT_NS}" get cluster.postgresql.cnpg.io "$1" \
    -o jsonpath="{range .status.conditions[?(@.type==\"$2\")]}{.status}{end}" 2>/dev/null || true
}

rt_wait_ready() {
  local name="$1" want="$2" budget="$3" ready=""
  local deadline=$((SECONDS + budget))
  while [ $SECONDS -lt $deadline ]; do
    ready=$(k -n "${RT_NS}" get cluster.postgresql.cnpg.io "$name" -o jsonpath='{.status.readyInstances}' 2>/dev/null || echo "")
    [ "${ready:-0}" = "$want" ] && { ok "$name ${want}/${want} Ready"; return 0; }
    sleep 5
  done
  note "phase: $(k -n "${RT_NS}" get cluster.postgresql.cnpg.io "$name" -o jsonpath='{.status.phase}' 2>/dev/null)"
  fail "$name did not reach ${want}/${want} within $((budget / 60)) min — check 'kubectl --context ${KUBECTL_CTX} -n ${RT_NS} get pods'"
}

rt_ensure_operator_live() {
  k -n cnpg-system get deployment/cnpg-controller-manager >/dev/null 2>&1 \
    || fail "CNPG is not installed on '${CLUSTER_NAME}' — run '$0 up' or '$0 live' first"
  local replicas
  replicas=$(k -n cnpg-system get deployment/cnpg-controller-manager -o jsonpath='{.spec.replicas}')
  if [ "$replicas" = "0" ]; then
    # Same order as `live`: the frozen-only Backup fixtures must go BEFORE the
    # controller starts, or it attempts them and contaminates pg-healthy.
    note "operator is frozen — thawing it (runtime needs a live controller)"
    delete_fixture_backups
    thaw_operator
    warn "The frozen terminal phases in '${NS}' will be reconciled away. '$0 refreeze' restores them."
  else
    ok "Operator already running"
  fi
}

# The bucket is emptied whenever pg-runtime does not exist yet. A NEW cluster
# checks that its WAL destination is empty before it archives anything, so one
# created against a bucket still holding a previous pg-runtime's archive never
# becomes Ready ("Expected empty archive"). MinIO's data is an emptyDir and the
# image recreates the bucket at start (MINIO_DEFAULT_BUCKETS), so replacing the
# pod IS the reset. An existing pg-runtime keeps its bucket.
rt_minio() {
  step "MinIO in '${RT_NS}'"
  local existed=0
  k -n "${RT_NS}" get deploy/minio >/dev/null 2>&1 && existed=1
  k apply -f "${RUNTIME_DIR}/00-minio.yaml" >/dev/null
  if [ "$existed" = 1 ] && ! k -n "${RT_NS}" get cluster.postgresql.cnpg.io pg-runtime >/dev/null 2>&1; then
    note "pg-runtime is new — restarting MinIO so the bucket starts empty"
    k -n "${RT_NS}" rollout restart deploy/minio >/dev/null
  fi
  k -n "${RT_NS}" rollout status deploy/minio --timeout=180s >/dev/null \
    || fail "MinIO did not roll out — 'kubectl --context ${KUBECTL_CTX} -n ${RT_NS} describe pod -l app=minio'"
  ok "MinIO running, bucket cnpg"
}

rt_wait_archiving() {
  step "Waiting for ContinuousArchiving=True on pg-runtime (real WAL in MinIO)"
  local deadline=$((SECONDS + 240)) st="" nudged=0
  while [ $SECONDS -lt $deadline ]; do
    st=$(rt_condition pg-runtime ContinuousArchiving)
    [ "$st" = "True" ] && { ok "ContinuousArchiving=True"; return 0; }
    # A quiet cluster may not close a segment for archive_timeout (5 min).
    if [ "$nudged" = 0 ] && [ $SECONDS -gt $((deadline - 180)) ]; then
      local p; p=$(rt_primary)
      [ -n "$p" ] && rt_psql "$p" "SELECT pg_switch_wal();" >/dev/null 2>&1 || true
      nudged=1
    fi
    sleep 5
  done
  fail "ContinuousArchiving is '${st:-unset}' — check the plugin-barman-cloud sidecar logs on $(rt_primary)"
}

rt_wait_backup() {
  step "Waiting for the immediate ScheduledBackup run to complete"
  local deadline=$((SECONDS + 300)) phases="" name=""
  while [ $SECONDS -lt $deadline ]; do
    phases=$(k -n "${RT_NS}" get backups.postgresql.cnpg.io \
      -o jsonpath='{range .items[?(@.spec.cluster.name=="pg-runtime")]}{.metadata.name}={.status.phase}{"\n"}{end}' 2>/dev/null || true)
    name=$(printf '%s\n' "$phases" | awk -F= '$2=="completed"{print $1; exit}')
    [ -n "$name" ] && { ok "Backup ${name} completed"; return 0; }
    if printf '%s\n' "$phases" | grep -q '=failed$'; then
      note "$(printf '%s' "$phases" | tr '\n' ' ')"
      fail "a pg-runtime Backup failed — 'kubectl --context ${KUBECTL_CTX} -n ${RT_NS} describe backups'"
    fi
    sleep 5
  done
  fail "no completed pg-runtime Backup after 5 min (saw: $(printf '%s' "$phases" | tr '\n' ' '))"
}

rt_wait_pooler() {
  local deadline=$((SECONDS + 180)) scheduled=""
  while [ $SECONDS -lt $deadline ]; do
    scheduled=$(k -n "${RT_NS}" get pooler.postgresql.cnpg.io pg-runtime-pooler-rw -o jsonpath='{.status.instances}' 2>/dev/null || echo "")
    [ -n "$scheduled" ] && break
    sleep 5
  done
  [ -n "$scheduled" ] || fail "pg-runtime-pooler-rw never reported status.instances (Service name collision?)"
  k -n "${RT_NS}" rollout status deploy/pg-runtime-pooler-rw --timeout=180s >/dev/null \
    || fail "pooler Deployment did not roll out"
  ok "pg-runtime-pooler-rw ${scheduled} pods"
}

rt_wait_lock_chain() {
  step "Waiting for the lock chain (idle in transaction holder + blocked UPDATE)"
  local deadline=$((SECONDS + 180)) out="" p
  while [ $SECONDS -lt $deadline ]; do
    p=$(rt_primary)
    out=$(rt_psql "$p" "SELECT string_agg(application_name || ':' || state || ':' || coalesce(wait_event_type, '-'), ' ' ORDER BY application_name) FROM pg_stat_activity WHERE application_name IN ('radar-lock-holder', 'radar-lock-waiter');" 2>/dev/null || true)
    if printf '%s' "$out" | grep -q 'radar-lock-holder:idle in transaction' \
       && printf '%s' "$out" | grep -q 'radar-lock-waiter:active:Lock'; then
      ok "$out"
      return 0
    fi
    sleep 5
  done
  fail "lock chain not observed on the primary (saw: '${out}')"
}

cmd_runtime() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist. Run '$0 up' (or 'live') first."
  k cluster-info >/dev/null || fail "kind context not reachable"

  step "Ensuring the CNPG operator is live"
  rt_ensure_operator_live
  install_barman_plugin
  k get crd objectstores.barmancloud.cnpg.io >/dev/null 2>&1 \
    || fail "barman-cloud plugin is not installed (SKIP_BARMAN_PLUGIN=1?) — runtime mode needs it"

  rt_minio

  step "ObjectStore minio-store, Cluster pg-runtime (3 instances), Pooler"
  rt_apply "${RUNTIME_DIR}/10-objectstore.yaml"
  rt_apply "${RUNTIME_DIR}/20-cluster.yaml"
  step "Waiting for pg-runtime (3 instances, ~3 min first run)"
  rt_wait_ready pg-runtime 3 1200
  rt_wait_pooler

  step "Load generator + lock chain"
  rt_apply "${RUNTIME_DIR}/40-load.yaml"
  ok "pg-load, pg-lock-holder, pg-lock-waiter applied"

  step "Prometheus in '${MON_NS}' (Radar discovers monitoring/prometheus-server:9090)"
  rt_apply "${RUNTIME_DIR}/50-prometheus.yaml"
  # The ConfigMap may have changed on a re-run; Prometheus does not watch it.
  k -n "${MON_NS}" rollout restart deploy/prometheus-server >/dev/null 2>&1 || true

  rt_wait_archiving
  rt_apply "${RUNTIME_DIR}/25-scheduledbackup.yaml"
  rt_wait_backup

  if k -n "${RT_NS}" get cluster.postgresql.cnpg.io pg-runtime-restore >/dev/null 2>&1; then
    step "pg-runtime-restore already exists — reusing"
  else
    step "Restoring pg-runtime-restore from minio-store"
    rt_apply "${RUNTIME_DIR}/30-restore.yaml"
  fi
  rt_wait_ready pg-runtime-restore 1 900

  step "Waiting for workloads"
  k -n "${RT_NS}" rollout status deploy/pg-load --timeout=240s >/dev/null || fail "pg-load did not roll out"
  k -n "${MON_NS}" rollout status deploy/prometheus-server --timeout=180s >/dev/null || fail "Prometheus did not roll out"
  ok "pg-load and Prometheus running"
  rt_wait_lock_chain

  print_runtime_summary
}

cmd_runtime_lag() {
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist."
  local mode="${1:-}" replicas r
  replicas=$(k -n "${RT_NS}" get pods -l cnpg.io/cluster=pg-runtime,cnpg.io/instanceRole=replica \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | sort)
  [ -n "$replicas" ] || fail "no pg-runtime replicas found — run '$0 runtime' first"
  case "$mode" in
    on)
      # One replica only: the other keeps replaying, so the lag is visibly
      # per-instance rather than cluster-wide.
      r=$(printf '%s\n' "$replicas" | head -1)
      rt_psql "$r" "SELECT pg_wal_replay_pause();" >/dev/null || fail "could not pause replay on $r"
      ok "WAL replay paused on $r — replay lag grows with the pgbench load"
      note "check: $0 status   (or: kubectl --context ${KUBECTL_CTX} -n ${RT_NS} exec $r -c postgres -- psql -U postgres -tAc 'SELECT now() - pg_last_xact_replay_timestamp()')"
      ;;
    off)
      # Every replica, not just the one `on` picked: which one is first can
      # change after a failover.
      for r in $replicas; do
        rt_psql "$r" "SELECT pg_wal_replay_resume();" >/dev/null 2>&1 || true
      done
      ok "WAL replay resumed on: $(printf '%s' "$replicas" | tr '\n' ' ')"
      ;;
    *) fail "usage: $0 runtime-lag on|off" ;;
  esac
}

cmd_runtime_down() {
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist."
  step "Removing runtime fixtures (${RT_NS}, ${MON_NS}); '${NS}' is untouched"
  k delete namespace "${RT_NS}" "${MON_NS}" --ignore-not-found --timeout=300s >/dev/null \
    || warn "namespace deletion still in progress — check 'kubectl --context ${KUBECTL_CTX} get ns'"
  k delete clusterrolebinding cnpg-demo-prometheus --ignore-not-found >/dev/null
  k delete clusterrole cnpg-demo-prometheus --ignore-not-found >/dev/null
  ok "Runtime fixtures removed (operator left as it was)"
}

runtime_status() {
  k get namespace "${RT_NS}" >/dev/null 2>&1 || { note "runtime fixtures not present ('$0 runtime' adds them)"; return 0; }
  k -n "${RT_NS}" get clusters.postgresql.cnpg.io \
    -o custom-columns='NAME:.metadata.name,READY:.status.readyInstances,DESIRED:.spec.instances,PHASE:.status.phase,PRIMARY:.status.currentPrimary' 2>/dev/null || true
  local c
  for c in pg-runtime pg-runtime-restore; do
    note "$c: ContinuousArchiving=$(rt_condition "$c" ContinuousArchiving) LastBackupSucceeded=$(rt_condition "$c" LastBackupSucceeded)"
  done
  k -n "${RT_NS}" get backups.postgresql.cnpg.io \
    -o custom-columns='BACKUP:.metadata.name,PHASE:.status.phase,METHOD:.spec.method,STOPPED:.status.stoppedAt' 2>/dev/null || true
  k -n "${RT_NS}" get poolers.postgresql.cnpg.io,deploy \
    -o custom-columns='NAME:.metadata.name,READY:.status.readyReplicas,SCHEDULED:.status.instances' 2>/dev/null || true
  local r paused
  for r in $(k -n "${RT_NS}" get pods -l cnpg.io/cluster=pg-runtime,cnpg.io/instanceRole=replica -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
    paused=$(rt_psql "$r" "SELECT pg_is_wal_replay_paused() || ' lag=' || coalesce((now() - pg_last_xact_replay_timestamp())::text, '?')" 2>/dev/null || echo "?")
    note "$r replay paused=${paused}"
  done
  local p
  p=$(rt_primary)
  [ -n "$p" ] && note "sessions: $(rt_psql "$p" "SELECT string_agg(application_name || '=' || state || coalesce('/' || wait_event_type, ''), ' ') FROM pg_stat_activity WHERE application_name LIKE 'radar-%';" 2>/dev/null)"
  k -n "${MON_NS}" get svc prometheus-server -o custom-columns='PROMETHEUS:.metadata.name,NAMESPACE:.metadata.namespace,PORT:.spec.ports[0].port' 2>/dev/null || true
}

print_runtime_summary() {
  printf "\n"
  step "CNPG runtime fixtures ready"
  cat <<EOF

  Context:    ${KUBECTL_CTX}   (operator LIVE)
  Namespace:  ${RT_NS}
    pg-runtime            3 instances, WAL → minio-store (plugin), hourly + immediate backup
    pg-runtime-restore    1 instance, bootstrap.recovery from minio-store
    pg-runtime-pooler-rw  PgBouncer, transaction mode, 2 pods
    pg-load               pgbench through the Pooler (~5 tps)
    pg-lock-holder        idle in transaction, holding radar_lock_demo id=1
    pg-lock-waiter        UPDATE blocked on it (wait_event_type=Lock)
  Prometheus: ${MON_NS}/prometheus-server:9090 — a Radar well-known location

  Replica lag on demand:
    $0 runtime-lag on | off
  Instance status JSON (replicationInfo etc.):
    kubectl --context ${KUBECTL_CTX} get --raw /api/v1/namespaces/${RT_NS}/pods/https:$(rt_primary):8000/proxy/pg/status
  Remove only these:
    $0 runtime-down

EOF
}

# --- Status ----------------------------------------------------------------

cmd_status() {
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist. Run '$0 up'."

  step "Operator"
  local replicas
  replicas=$(k -n cnpg-system get deployment/cnpg-controller-manager -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "?")
  if [ "$replicas" = "0" ]; then
    note "cnpg-controller-manager scaled to 0 (frozen — terminal phases hold)"
  else
    note "cnpg-controller-manager running (live — terminal phases will not hold)"
  fi

  step "Clusters"
  k -n "${NS}" get clusters.postgresql.cnpg.io \
    -o custom-columns='NAME:.metadata.name,READY:.status.readyInstances,DESIRED:.spec.instances,PHASE:.status.phase' 2>/dev/null || true

  step "ContinuousArchiving"
  local c st
  for c in $(k -n "${NS}" get clusters.postgresql.cnpg.io -o name 2>/dev/null); do
    st=$(k -n "${NS}" get "$c" -o jsonpath='{range .status.conditions[?(@.type=="ContinuousArchiving")]}{.status}{end}' 2>/dev/null || echo "")
    [ -n "$st" ] && note "$(basename "$c"): ContinuousArchiving=${st}"
  done

  step "Backups, Poolers, ScheduledBackups"
  k -n "${NS}" get backups.postgresql.cnpg.io \
    -o custom-columns='NAME:.metadata.name,PHASE:.status.phase,CLUSTER:.spec.cluster.name' 2>/dev/null || true
  k -n "${NS}" get poolers.postgresql.cnpg.io \
    -o custom-columns='NAME:.metadata.name,DESIRED:.spec.instances,SCHEDULED:.status.instances' 2>/dev/null || true
  k -n "${NS}" get scheduledbackups.postgresql.cnpg.io \
    -o custom-columns='NAME:.metadata.name,METHOD:.spec.method,SUSPEND:.spec.suspend' 2>/dev/null || true

  step "Colliding plurals"
  k -n "${NS}" get backups.velero.io \
    -o custom-columns='NAME:.metadata.name,PHASE:.status.phase' 2>/dev/null || true
  k -n "${NS}" get clusters.apps.kubeblocks.io \
    -o custom-columns='NAME:.metadata.name' 2>/dev/null || true

  step "Runtime fixtures (${RT_NS})"
  runtime_status
}

# --- Summary ---------------------------------------------------------------

print_summary() {
  local mode="$1"
  printf "\n"
  step "CNPG demo cluster ready (${mode})"
  if [ "$mode" = "live" ]; then
    cat <<EOF

  Context:  ${KUBECTL_CTX}

  The operator is running and every cluster is 2/2 Ready. The real
  WAL-archiving failure remains active on pg-wal-failing. Hand-written
  terminal phases and the three Backup phase fixtures are intentionally
  absent — a live controller would reconcile or attempt them.

  Also: Pooler · ScheduledBackup (method: plugin) · a Velero \`backups\` CR ·
        a KubeBlocks \`clusters\` CRD for the shared-plural guards.

  Run Radar against this cluster:
    kubectl config use-context ${KUBECTL_CTX}
    ./scripts/visual-test-start.sh

  Return to the frozen rendering matrix:
    $0 refreeze

EOF
    return
  fi

  cat <<EOF

  Context:  ${KUBECTL_CTX}

  Four clusters, all 2/2 Ready, four different badges:
    pg-healthy         Healthy               no backup config → audit reports it
    pg-wal-failing     WAL Archiving Failing REAL archiver failure, self-sustaining
    pg-unrecoverable   Unrecoverable         terminal phase, muted cells + tooltips
    pg-doomed          Unrecoverable         second one, so the filter shows (2)

  Also: Pooler (Scheduled, not Ready) · ScheduledBackup (method: plugin) ·
        3 Backups (completed / walArchivingFailing / an unmapped phase) ·
        2 ObjectStores (one with a recovery window, one never backed up) ·
        6 declarative objects (Database / Publication / Subscription, in all
        three applied states) · 2 image catalogs — pg-doomed takes its image
        from one, pg-unrecoverable asks one for a version it does not carry ·
        a Velero \`backups\` CR and a KubeBlocks \`clusters\` CRD for the
        shared-plural guards.

  Run Radar against this cluster:
    kubectl config use-context ${KUBECTL_CTX}
    ./scripts/visual-test-start.sh

  Other commands:
    $0 status     # inventory + whether the operator is frozen
    $0 live       # rebuild with the operator LEFT RUNNING (no terminal phases)
    $0 refreeze   # re-apply freeze + terminal phases
    $0 down       # delete cluster

EOF
}

cmd_help() { sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

case "${1:-help}" in
  up)       cmd_up       ;;
  down)     cmd_down     ;;
  live)     cmd_live     ;;
  refreeze) cmd_refreeze ;;
  thaw)     cmd_thaw     ;;
  status)   cmd_status   ;;
  runtime)  cmd_runtime  ;;
  runtime-lag)  cmd_runtime_lag "${2:-}" ;;
  runtime-down) cmd_runtime_down ;;
  help|-h|--help) cmd_help ;;
  *)
    printf "${C_RED}Unknown subcommand: %s${C_RESET}\n\n" "$1"
    cmd_help
    exit 1
    ;;
esac
