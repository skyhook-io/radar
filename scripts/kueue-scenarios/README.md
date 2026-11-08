# Kueue queue scenarios (any cluster that already runs Kueue)

Run queue-detail, membership, coverage and Issues fixtures beside an existing
Kueue controller. Unlike [`kueue-demo`](../kueue-demo/README.md), this lane does
not create a cluster, install/upgrade Kueue or change its configuration.

## Quick start

```bash
# Prerequisites: Kubernetes 1.26+ (Job podFailurePolicy), bash, kubectl 1.24+,
# jq, curl, and an identity that can:
#   get CRDs; create and delete namespaces, Jobs, cluster-scoped Kueue objects,
#   ClusterRoles and ClusterRoleBindings; create LocalQueues, ServiceAccounts and
#   RoleBindings in the fixture namespaces; grant what the viewer's ClusterRoles
#   grant (hold it, or have escalate/bind); patch ClusterQueue spec, Workload spec
#   and the status of Workloads and AdmissionChecks; list Jobs, LocalQueues,
#   ClusterRoles and ClusterRoleBindings across the cluster; create ServiceAccount
#   tokens; impersonate users and ServiceAccounts (verify uses --as). Listing
#   Deployments cluster-wide is optional (version report). Cluster admin covers it.
K=/path/to/cluster.kubeconfig          # or use --context NAME instead
./scripts/kueue-scenarios.sh up     --kubeconfig "$K"   # ~10 minutes on a remote cluster
./scripts/kueue-scenarios.sh verify --kubeconfig "$K"
./scripts/kueue-scenarios.sh status --kubeconfig "$K"

# When done:
./scripts/kueue-scenarios.sh down   --kubeconfig "$K"
```

The Makefile wraps the same commands: `make kueue-scenarios
SCENARIOS_KUBECONFIG=…` and/or `SCENARIOS_CONTEXT=…` (both are passed when both
are set), plus `-verify`, `-status` and `-down`, with `SCENARIOS_PREFIX=…` for
another prefix. One of
`--kubeconfig`/`--context` is always required, so the target cluster is never
implied, and the kubeconfig's current context is never changed.

Everything is named `<prefix>-…` (default prefix `kq-<your user name>`; `--prefix` or
`KUEUE_SCENARIOS_PREFIX` to change it, at most 32 characters) and labeled
`radar.skyhook.io/kueue-scenarios=<prefix>` plus `radar.skyhook.io/fixture`
(`--fixture`, default `kueue-scenarios`). Several people can run the lane on one
cluster: the default prefix is per user (from the local user name, so a generic
account such as `ubuntu` or `runner` should pass `--prefix`), and on a shared
prefix they share one copy (`up` says so when it resumes existing fixtures).

While the scenarios are up, their admitted pause Pods hold about 850m CPU and
a few hundred MiB of memory on the cluster.

## Scenarios

Validated on **Kueue v0.18.0** (API `kueue.x-k8s.io/v1beta2`, storage version
v1beta2) on EKS Kubernetes 1.34, with Kueue's default configuration plus the
`batch/job` integration. Workloads are ordinary Jobs running
`registry.k8s.io/pause:3.10` or a busybox that exits; quotas are a few hundred
millicores, so nothing needs accelerators or meaningful capacity.

The table distinguishes controller output, script-played **check controller**
responses and **injected** conditions. Only Kueue's reaction to the latter two
is controller proof.

| # | Scenario | Where | What it shows | Kind |
|---|---|---|---|---|
| 1 | Pending on quota | ClusterQueue `<p>-quota` (200m CPU), namespace `<p>-quota` | `fits` admitted; `waits-for-quota` QuotaReserved=False/Pending "insufficient unused quota … 50m more needed"; `too-big` "… > maximum capacity (200m)". Queue: pending 2, reserving 1, admitted 1 | controller |
| 2 | Busy ClusterQueue | `<p>-busy` in Cohort `<p>-cohort` with idle lender `<p>-lender`; namespaces `<p>-busy`, `<p>-busy-hidden` | Borrows CPU from the cohort (`flavorsReservation` borrowed > 0); memory has `borrowingLimit: "0"`. Pending 1, reserving 7, admitted 5: reserving includes admitted | controller |
| 2 | — admission check Pending | `<p>-busy` Job `check-pending` | Quota reserved, check never answered, not admitted | check controller |
| 2 | — admission check Retry | Job `check-retry` | Retry written once; Kueue evicted and requeued it. Now reserved again, check Pending, `retryCount: 1`, message "Reset to Pending after eviction. Previously: Retry" | check controller |
| 2 | — admission check Rejected | Job `check-rejected` | Kueue deactivated it: `spec.active=false`, Evicted=True/DeactivatedDueToAdmissionCheck, check reset to Pending "Previously: Rejected" | check controller |
| 2 | — removed flavor | Job `on-spare` | Admitted on flavor `<p>-spare`, which `up` then removes from the queue's spec. The Workload's `status.admission` still names `<p>-spare`; **Kueue v0.18.0 keeps no ClusterQueue status entry for it** (`verify` warns if a newer Kueue does) | controller |
| 2 | — a namespace the viewer can't read | `<p>-busy-hidden` | `hidden-1`, `hidden-2` admitted, `hidden-pending` larger than the cohort can lend. Its LocalQueue reports pending 1, admitted 2; the viewer can't read that either | controller |
| 3 | Stopped with Hold | `<p>-held` | ClusterQueue Active=False/Stopped; LocalQueue Active=False/ClusterQueueIsInactive; `held` Inadmissible | controller |
| 4 | Missing ResourceFlavor | `<p>-missing-flavor` | ClusterQueue Active=False/FlavorNotFound naming `<p>-does-not-exist`; `blocked` Inadmissible | controller |
| 5 | Stale `observedGeneration` | — | **Not reproducible against a running controller.** Kueue caught ClusterQueue, LocalQueue and Workload conditions up to a spec change within seconds. Cover it with a unit-test object in the code that reads it | — |
| 6 | LocalQueue whose Workloads the viewer can't list | `<p>-lq-denied/workloads-hidden` | `admitted` and `pending` exist; the viewer can list LocalQueues and Pods there but not Workloads | controller |
| 7 | Confirmed empty | `<p>-empty` | ClusterQueue and its only LocalQueue have never had a Workload; the viewer can read the namespace | controller |
| — | Finished | `<p>-lifecycle` `finished`, `failed` | Finished=True/Succeeded and Finished=True/Failed | controller |
| — | Preempted | `<p>-lifecycle` LocalQueue `preempt` → `<p>-preempt` | `low-priority` Evicted/Preempted and Preempted/InClusterQueue, pending again; `high-priority` admitted | controller |
| — | Evicted by a stopped queue | `<p>-drain` (HoldAndDrain) | `drained` Evicted=True/ClusterQueueStopped | controller |
| — | Evicted by PodsReadyTimeout | `<p>-drain` | `pods-ready-timeout` Evicted=True/PodsReadyTimeout, no reservation. The condition is written by the script on a held queue; real Kueue would also update `requeueState`, so don't treat this one as a complete timeout record | injected |
| — | Deactivated by a user | `<p>-lifecycle` `deactivated` | `spec.active=false` → Evicted=True/Deactivated | controller |
| — | LocalQueue missing | `<p>-lifecycle` `no-localqueue` | Inadmissible "LocalQueue does-not-exist doesn't exist" | controller |
| — | Namespace not eligible | `<p>-ineligible` → `<p>-lifecycle` | Pending "Workload namespace doesn't match ClusterQueue selector" | controller |

Exact reasons and messages follow the Kueue release. `up` and `verify` print
the Kueue controller image they find and warn when it isn't v0.18; `verify` then
lists the expectations that no longer hold. Update the table and the
expectations together.

### What the cluster's Kueue configuration can change

The lane doesn't read or change Kueue's configuration, so a few settings decide
whether some states appear or last:

- `integrations.frameworks` must include `batch/job`; otherwise no Workload is
  created and `up` times out saying so.
- `objectRetentionPolicies` can delete finished or deactivated Workloads after a
  while. Kueue doesn't recreate a Workload for a finished Job, so `up` can't
  restore them: run `down` and `up`, or use a fresh `--prefix`.
- `waitForPodsReady`, fair sharing and admission fair sharing change eviction and
  ordering behavior; the expectations assume they are off.
- RBAC bound to groups every identity has (`system:authenticated`,
  `system:serviceaccounts`) can give the viewer access the fixtures deny, and
  then scenarios 2 and 6 can't exist; `verify`'s viewer checks fail. Find the
  binding with `kubectl auth can-i --list -n <namespace> --as <subject>`.
- A ClusterQueue or Cohort outside the lane that joins `<prefix>-cohort`, or a
  LocalQueue outside the fixture namespaces that points at a fixture
  ClusterQueue, changes borrowing and counts; `verify` checks for both.
- The long-running pause Jobs ignore Pod disruptions (`podFailurePolicy`), so a
  node drain, consolidation or spot interruption reschedules the Pod instead of
  failing the Job and silently releasing its quota.
- No fixture requests `nvidia.com/gpu` or any accelerator, and none may tolerate
  `nvidia.com/gpu`: on a cluster whose autoscaler provisions GPU nodes for pods
  that request GPUs and tolerate that taint, an admitted fixture would launch real
  GPU capacity. Model quota scenarios with CPU, memory or a fake extended
  resource. If a future scenario must request `nvidia.com/gpu`, keep it
  suspended, held or quota-blocked so it never gets a Pod.
- Finished and Failed need the busybox image to pull and run, and the evictions
  wait for their Pods to stop. The pending and blocked scenarios need no Pods,
  so node capacity doesn't affect them. Pods run as a non-root user with no
  capabilities, so namespaces enforcing the `restricted` Pod Security level
  accept them.

## The restricted viewer

The ServiceAccount `<p>-viewer/viewer` (run Radar as the viewer) and user
`<p>-viewer` (proxy mode) share roles. Both can list all Namespaces, CRDs and
cluster-scoped Kueue objects, including other teams' queues. They read fixture
namespaces except `<p>-busy-hidden`, and cannot read Workloads in `<p>-lq-denied`.
`verify` checks both identities with `kubectl auth can-i --as`.

### Run Radar as the viewer (Radar's own access is limited)

```bash
./scripts/kueue-scenarios.sh kubeconfig --kubeconfig "$K" --out /tmp/kueue-viewer.kubeconfig
P=$(bash -c 'source scripts/kueue-scenarios.sh; echo "$PREFIX"')   # the default prefix; or the --prefix you used
./radar --kubeconfig /tmp/kueue-viewer.kubeconfig --kubeconfig-dir "" --port 9331 --no-browser \
  --namespaces $P-quota,$P-busy,$P-busy-hidden,$P-held,$P-missing-flavor,$P-lq-denied,$P-empty,$P-lifecycle,$P-drain,$P-ineligible
RADAR_URL=http://127.0.0.1:9331 ./scripts/kueue-scenarios.sh verify-radar --identity viewer --kubeconfig "$K"
```

The token comes from a TokenRequest and lasts `TOKEN_DURATION` (default `24h`,
capped by the API server); run `kubeconfig` again for a fresh one. The file is
written with mode 600.

Name the fixture namespaces with `--namespaces`: the viewer can list every
namespace, and on a cluster with more than 20 Radar only probes the first 20
(`--max-scope-candidates`), alphabetically, unless they are named.

### Send requests as the viewer user (Radar's access is full, the user's isn't)

```bash
./radar --kubeconfig "$K" --kubeconfig-dir "" --port 9331 --no-browser --auth-mode=proxy
RADAR_URL=http://127.0.0.1:9331 ./scripts/kueue-scenarios.sh verify-radar --identity user --kubeconfig "$K"
```

`verify-radar --identity user` sends `X-Forwarded-User: <p>-viewer` with every
request, so Radar authorizes it as that user. Only do this on a loopback
listener: proxy mode trusts that header.

## Checking Radar

`verify-radar` reads a running Radar at `RADAR_URL` (default
`http://127.0.0.1:9280`) through `/api/resources/...` and checks, per identity:

| Read | `full` | `viewer` (Radar runs as the viewer) | `user` (proxy mode, viewer user) |
|---|---|---|---|
| Fixture ClusterQueues | all 10, complete | all 10, complete | all 10, complete |
| Workloads in `<p>-quota`, `<p>-held` | complete, with Kueue's reasons | same | same |
| Workloads in `<p>-empty` (7) | complete, 0 items | complete, 0 items | complete, 0 items |
| LocalQueues in `<p>-lq-denied` | complete | complete | complete |
| Workloads in `<p>-busy` + `<p>-busy-hidden` (2) | complete, 9 | 200, partial: read `<p>-busy` (6 items), gap `radar_forbidden` for `<p>-busy-hidden` | 200, partial: read `<p>-busy` (6 items), gap `user_namespace_not_visible` for `<p>-busy-hidden` |
| Workloads in `<p>-busy-hidden` alone | — | 503 `radar_forbidden` | — |
| Workloads in `<p>-lq-denied` (6) | complete, 2 | 503 `radar_forbidden` | either complete, 2 (warns) or 403 (see below) |

`verify-radar` matches Workloads to this cluster's Job UIDs, so pointing Radar
at a different cluster fails. These are generic resource reads, not queue APIs.
Scenario 6 accepts either complete data (with a warning) or a denial for the
proxy user: generic CRD reads authorize namespace access, while queue reads
promise exact per-kind access. See [read granularity](../../docs/authentication.md#read-granularity-namespace-level).
Retryable startup errors are retried for up to 30 seconds before checking codes.

## Up, re-runs and cleanup

`up` refuses unreadable, terminating or unowned existing objects. It creates
missing objects without adopting others; later writes check ownership and use
resourceVersion. Re-runs resume completed steps instead of repeating transitions.
Changed manifests or manually altered one-time states need `down`/`up` or a new
prefix. Failed reads stop the run.

`down` deletes only prefixed, ownership-labeled Jobs, namespaces, Kueue objects
and viewer RBAC. **Deleting a fixture namespace also deletes anything added to
it later.** A shared prefix is one shared copy; cleanup lists its namespaces.
Deletes recheck the label, but kubectl provides no UID precondition against an
instant replacement. Cleanup waits for deletion and exits 2 if objects remain;
it skips removed CRDs and warns when controller finalizers prevent completion.

`scripts/kueue-scenarios/test.sh` tests the script's decisions offline (no
cluster; kubectl is stubbed): unreadable versus absent, ownership refusal,
Workload matching by Job UID, check states that keep Kueue's `retryCount`, and
cleanup scoped to the prefix. Run it (`make kueue-scenarios-test`; needs only
bash and jq) after changing the script.

## Proof boundary

`verify` proves these inputs reached these states on the reported Kueue release
and configuration. It does not prove real PodsReadyTimeout or an external check
controller, stale generation or removed-flavor ClusterQueue status, GPU behavior,
ProvisioningRequest, MultiKueue, topology-aware scheduling, fair sharing or
hierarchical cohorts. Keep synthetic states in unit tests, not live-proof claims.
