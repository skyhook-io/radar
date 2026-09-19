# Execution context

`resourceContext.execution` describes one supported root's last reported finite
execution. It is the same projection in REST AI detail and MCP `get_resource`.
Only `jobset.x-k8s.io/v1alpha2` is implemented. Unsupported GVKs omit the block;
a supported root with insufficient status has `phase: unknown`.

## Shared contract

| Field | Meaning |
|---|---|
| `controller` | Adapter family; currently `jobset`, not the identity of the active reconciler |
| `subjectGeneration` | Root metadata generation, preserved because AI minification removes it |
| `phase` | `pending`, `active`, `suspended`, `finished`, or `unknown` |
| `outcome` | Present exactly when finished; currently `succeeded` or `failed` |
| `primaryCondition` | Selected native condition: type, status, reason, and observed generation when reported |
| `nativeState` | Principal native lifecycle field verbatim; JobSet uses `status.terminalState` |
| `suspendRequested` | Optional requested suspension; false is distinct from unsupported/unavailable |
| `jobset` | Typed JobSet quantities and recreation counters |

`outcome` is an extensible string vocabulary: consumers must preserve unfamiliar
values as unclassified outcomes rather than map them to success or failure. New
values require an adapter backed by authoritative root evidence; the existing
values retain their meanings. No speculative cancellation taxonomy is encoded.

`pending` means status supports waiting with no activity visible in this snapshot,
not that the root never ran or that quota is blocking it. `active` includes
startup, retries and cleanup, not a guarantee of Running Pods or healthy user
code. `suspended` requires observed controller evidence, not just a spec request.
`finished` requires a root outcome; child failures alone cannot establish it.
Object deletion is separate: `metadata.deletionTimestamp` remains in the resource,
and does not change the last reported execution into a fabricated outcome.

The condition's observed generation applies to that condition, not all counters.
JobSet v0.12.0 does not populate it in the relevant controller paths. No fresh/stale
verdict or synthetic observation timestamp is invented. Older reported evidence
is retained: a suspended condition with `suspendRequested: false` explains resume
lag. Messages (bounded to 256 bytes) and transition times appear only at diagnostic
tier in this block; raw resource/status summaries keep their own detail. This
parser tier support does not add JobSet to the `diagnose` tool.

## JobSet detail

`jobset` contains `declaredRoles`, `declaredJobs`, optional `observedRoles`, optional
`jobs`, and optional `restarts`. The declared Job total uses the controller's
replica default of one when omitted.

`jobs` carries `ready`, `active`, `succeeded`, `failed`, and `suspended` child-Job
counts as one observed group. An absent group is unavailable; a present zero is
observed zero. Ready can include completed Pods satisfying a still-active Job's
threshold, and active can include Pending Pods. These counters overlap; do not
sum them into a total. `observedRoles` describes the reported input population,
not an identity-completeness guarantee. The pinned controller normally reports
all roles; deliberately partial unit fixtures test sums over reported entries.

`restarts` separates `global`, `globalCountTowardsMax`, `individual`, and
`individualCountTowardsMax`. These count JobSet/Job recreation, not container
restarts, execution attempts, or in-place restarts. Global counters require their
native status evidence. Once global status is observed, an omitted counted value
means zero. Once role status is reported, omitted per-role arrays mean zero for
those roles; without role status, individual totals stay absent. Raw status
retains in-place restart fields and individual array details.

The phase precedence is:

1. Known terminalState: authoritative over contradictory conditions; finished
   with its outcome, plus a matching true condition if present. Raw conditions
   remain on the resource; this precedence does not claim which evidence is newer. An unrecognized nonempty terminalState stays unknown and visible.
2. Otherwise, true Failed or Completed condition: finished with outcome.
3. True StartupPolicyInProgress with no suspension requested: active. In-order
   resume retains Suspended=True until all roles start; startup evidence takes
   precedence in that case.
4. True Suspended: suspended, including ordinary resume lag.
5. True RestartingJobSet or StartupPolicyInProgress: active, retaining the condition.
6. Reported roles with any positive child-Job counter: active, not terminal.
7. Reported roles with all-zero counters: pending.
8. Otherwise: unknown, including an empty status or global retries alone.

The controller treats omitted `spec.suspend` as false. Its value is exposed as
intent only. Neither this value nor the phase identifies the actor or supplies
an instruction to resume work.

## Why a hybrid schema

Shared fields answer stable operator questions. Typed extensions retain units,
secondary lifecycle dimensions, and retry scopes. A reusable Go value type does
not require a universal top-level field: `ChildJobCounts` uses the matching
[JobSet v0.12.0 definitions](https://github.com/kubernetes-sigs/jobset/blob/v0.12.0/api/jobset/v1alpha2/jobset_types.go)
and [TrainJob v2.0.0 definitions](https://github.com/kubeflow/trainer/blob/v2.0.0/pkg/apis/trainer/v1alpha1/trainjob_types.go),
so a future TrainJob adapter can reuse it inside its own extension.

The following are design constraints, not implemented support:

| Family | Precision that must remain native |
|---|---|
| TrainJob | Runtime-derived declarations and TrainJob→JobSet→Job→Pod lineage |
| RayJob | Root deployment status versus application status and attempt counts; `FAILED` during `Retrying` is not terminal root failure |
| Volcano Job | Task/Pod counters, gang thresholds, and resumable Aborted versus terminal outcomes |
| Argo Workflow | Workflow versus logical graph nodes/attempts, Error versus Failed, and offloaded-node coverage |
| Native Job | Pod counters and indexed completion; existing `jobSummary` stays unchanged |

Ray's root `Complete` can accompany application `STOPPED`, so its future adapter
must add a distinct stopped outcome rather than claim success. See the
[KubeRay v1.7.0 controller](https://github.com/ray-project/kuberay/blob/v1.7.0/ray-operator/controllers/ray/rayjob_controller.go).
Serving roots such as RayService and LeaderWorkerSet need runtime/readiness
semantics; this finite-execution block does not force them into terminal outcomes.

Extensions are added with real adapters, not empty placeholders. Shared counter
types require matching units, populations, state definitions, overlap and missing
value semantics, not just similar field names. Full policies, graphs and child
lists belong in resource/detail surfaces. This summary cannot be summed across
nested execution roots without a separate ownership-aware aggregation design.
