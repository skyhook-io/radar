# Kueue queue scenarios (any cluster that already runs Kueue)

This lane builds the scenarios from the Kueue queue design review on a cluster
you already have, using the real Kueue controller. It is the
fixture set for queue-level work: queue detail, the Workloads in a queue, their
coverage, and Kueue Issues.

It complements [`kueue-demo`](../kueue-demo/README.md), which creates its own
`kind` cluster, installs a pinned Kueue, and proves one admission lifecycle in
depth. This lane does neither of those things: it never creates a cluster,
installs or upgrades Kueue, or edits Kueue's configuration. Point it at a
cluster with Kueue running and it adds its own namespaces, queues and Jobs
beside whatever is already there.

## Quick start

```bash
# Prerequisites: bash, kubectl, jq, curl, and an identity that can create
# namespaces, cluster-scoped Kueue objects, ClusterRoles and ClusterRoleBindings,
# patch Workload and AdmissionCheck status, list Jobs and LocalQueues across
# namespaces, create ServiceAccount tokens, and impersonate (verify uses --as).
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

Everything is named `<prefix>-…` (default prefix `radar-kueue`; `--prefix` or
`KUEUE_SCENARIOS_PREFIX` to change it, at most 32 characters) and labeled
`radar.skyhook.io/kueue-scenarios=<prefix>` plus `radar.skyhook.io/fixture`
(`--fixture`, default `kueue-scenarios`). Several people can run the lane on one
cluster with different prefixes.

While the scenarios are up, their admitted pause Pods hold about 850m CPU and
a few hundred MiB of memory on the cluster.

## Scenarios

Validated on **Kueue v0.18.0** (API `kueue.x-k8s.io/v1beta2`, storage version
v1beta2) on EKS Kubernetes 1.34, with Kueue's default configuration plus the
`batch/job` integration. Workloads are ordinary Jobs running
`registry.k8s.io/pause:3.10` or a busybox that exits; quotas are a few hundred
millicores, so nothing needs accelerators or meaningful capacity.

**Kind** says who produced the state:
- **controller**: Kueue reached it on its own from ordinary inputs (Jobs,
  queue configuration, a user setting `spec.active`).
- **check controller**: the script plays the controller for the AdmissionCheck
  `<prefix>-check` and writes one check state, as any external check
  controller would. Everything Kueue does in response is its own.
- **injected**: the script writes a condition Kueue itself would normally write,
  because the setting that makes Kueue write it is controller-wide. Only Kueue's
  reaction is real; the cause is not.

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
- Finished and Failed need the busybox image to pull and run, and the evictions
  wait for their Pods to stop. The pending and blocked scenarios need no Pods,
  so node capacity doesn't affect them. Pods run as a non-root user with no
  capabilities, so namespaces enforcing the `restricted` Pod Security level
  accept them.

## The restricted viewer

Scenarios 2 and 6 are about what a viewer with less access sees. The lane
creates one viewer with two identities bound to the same roles:

- ServiceAccount `<p>-viewer/viewer`, to run Radar **as** the viewer;
- the plain user `<p>-viewer`, to send to a Radar running with full access and
  `--auth-mode=proxy`.

Both can list every Namespace, CRD and Kueue cluster-scoped object on the
cluster (including other teams' ClusterQueues), and read Pods, events,
Services, Jobs, LocalQueues and Workloads in the fixture namespaces, except:

- nothing at all in `<p>-busy-hidden`;
- everything but Workloads in `<p>-lq-denied`.

`verify` checks those permissions with `kubectl auth can-i --as` for both
identities.

### Run Radar as the viewer (Radar's own access is limited)

```bash
./scripts/kueue-scenarios.sh kubeconfig --kubeconfig "$K" --out /tmp/kueue-viewer.kubeconfig
P=radar-kueue
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
| Workloads in `<p>-busy` + `<p>-busy-hidden` (2) | complete, 9 | 200, partial: read `<p>-busy` (6 items), one gap for `<p>-busy-hidden` | 200, partial: read `<p>-busy` (6 items), gap `user_namespace_not_visible` for `<p>-busy-hidden` |
| Workloads in `<p>-busy-hidden` alone | — | 503 `radar_forbidden` | — |
| Workloads in `<p>-lq-denied` (6) | complete, 2 | 503 `radar_forbidden` | either complete, 2 (warns) or 403 (see below) |

Workload sets are compared by the UIDs of this run's Jobs on the cluster named
by `--kubeconfig`/`--context`, through each Workload's controller reference, so
a Radar pointed at another cluster with the same fixtures fails. These are
today's generic resource reads, not a queue-membership answer; queue features
should add their own assertions on top.

Scenario 6 reads differently depending on whose access is limited. When Radar's
own identity can't list Workloads there, Radar can't hold them and says so. When
only the signed-in user can't, a generic read still returns them: Radar
authorizes namespaced CRD reads per namespace, not per kind
([Read granularity](../../docs/authentication.md#read-granularity-namespace-level)),
and the viewer can read that namespace. `verify-radar` accepts that answer with
a warning, and also a refusal. A queue read that promises per-kind coverage has
to check `list workloads.kueue.x-k8s.io` itself; that is the case this scenario
exists to catch.

In the `viewer` column, the gap for `<p>-busy-hidden` in a two-namespace read is
currently coded `read_error` (retry), while the same namespace read alone is
`radar_forbidden`. `verify-radar` accepts either code there and warns on
`read_error`; any other code fails.

## Up, re-runs and cleanup

`up` checks every object it is about to create first, and stops if any of them
exists without this prefix's ownership label or can't be read. It then applies the configuration,
submits the Jobs in two batches, and drives each scenario to its end state:
answers checks, removes the spare flavor, deactivates one Workload, and stops the
drain queue. Each step checks the cluster before acting, so re-running `up` after
it finished, or after it was interrupted, resumes instead of repeating a step
(for example, Retry is written only while Kueue's `retryCount` is still 0). A
read that fails stops `up` rather than being taken as an answer.

Some states are transitions that only happen once per object (preemption,
removal of the spare flavor, the drain). If an object was changed by hand, `down`
and `up` again, or use another prefix.

`down` deletes objects that carry this prefix's ownership label **and** are
named with the prefix (or, for Jobs, sit in a namespace named with it): the
Jobs, the namespaces (with everything in them, including anything someone added
later), the cluster-scoped Kueue objects, and the viewer's ClusterRoles and
ClusterRoleBindings. It waits for them to go, because `up` refuses objects that
are still being deleted. If Kueue's CRDs are gone it
skips those kinds. If Kueue's controller is gone but its CRDs remain, Kueue's
finalizers can keep them terminating; `down` warns instead of waiting forever.

`scripts/kueue-scenarios/test.sh` tests the script's decisions offline (no
cluster; kubectl is stubbed): unreadable versus absent, ownership refusal,
Workload matching by Job UID, check states that keep Kueue's `retryCount`, and
cleanup scoped to the prefix. Run it (`make kueue-scenarios-test`; needs only
bash and jq) after changing the script.

## Proof boundary

Passing `verify` proves that this Kueue reached these states from these inputs
on this cluster. It does not prove:

- the states of other Kueue releases or configurations (see above);
- a real PodsReadyTimeout (injected) or a real external admission check
  controller (played by the script);
- a stale `observedGeneration` or a ClusterQueue status entry for a removed
  flavor (not reachable against a running controller, see scenarios 5 and 2);
- ProvisioningRequest, MultiKueue, topology-aware scheduling, fair sharing or
  hierarchical cohorts;
- anything about GPUs: every quota here is CPU and memory.
