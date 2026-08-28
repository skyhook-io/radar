# Argo Rollouts Demo Cluster

Bootstraps a `kind` cluster with Argo Rollouts installed and a set of
Rollouts parked in the states Radar's control surface has to handle. Use it
for visual-testing the Rollout actions row, revision history, and the
AnalysisRun drill-down without needing a real progressive-delivery cluster.

## Quick start

```bash
# Prerequisites: kind, kubectl
./scripts/rollouts-demo.sh up        # ~4 minutes on first run
./scripts/rollouts-demo.sh status    # inventory Rollouts + AnalysisRuns

# Run Radar against it
kubectl config use-context kind-radar-rollouts-demo
./scripts/visual-test-start.sh

# When done
./scripts/rollouts-demo.sh down
```

## What's in the cluster

All fixtures live in namespace `demo-rollouts`.

| Resource | Kind | What it exercises |
|---|---|---|
| `canary-manual` | Rollout | Parked at `CanaryPauseStep` with 3 prior fully-promoted revisions. The main actions fixture: Promote, Skip step, Promote full, Abort all live; Rollback has real targets; `Current` and `Stable` are different revisions in the history dialog. |
| `canary-analysis` | Rollout | Parked on `InconclusiveAnalysisRun`. The reason names nothing on its own — the linked AnalysisRun carries the deciding metric, its success/failure conditions, and the measured value. Also runs a background analysis. |
| `bluegreen` | Rollout | Parked on `BlueGreenPause` with `activeSelector != previewSelector` and a pre-promotion analysis. **Skip step must NOT render here** — it is canary-only. |
| `canary-degraded` | Rollout | Aborted by a failing analysis (`status.abort: true`). Retry is the live verb; Promote and Skip step are blocked. |
| `canary-workloadref` | Rollout | Uses `spec.workloadRef` → `workloadref-target` Deployment. Rollback must patch the Deployment, not the Rollout. |
| `success-rate-pass` | AnalysisTemplate | Web metric returning `1` → Successful. |
| `success-rate-inconclusive` | AnalysisTemplate | Web metric returning `2` → matches neither condition → Inconclusive. |
| `success-rate-fail` | AnalysisTemplate | Web metric returning `0` → Failed, which aborts the Rollout. |
| `radar-demo-smoke-test` | ClusterAnalysisTemplate | Job provider, cluster-scoped — covers the cluster-scoped analysis kind. |
| `metric-endpoint` | Deployment + Service + ConfigMap | nginx serving three static JSON files. Lets the web metric provider produce every verdict with no Prometheus in the cluster. |
| `rollout-visibility` | Deployment + StatefulSet + DaemonSet | One grouped Application with a paused Deployment, OnDelete StatefulSet and DaemonSet waiting for manual Pod restarts, and a Deployment whose updated revision fails image pull while its old revision stays available. Covers Applications, resource tables, drawers, and full WorkloadView. |

## Why a static JSON endpoint

Argo Rollouts returns `Inconclusive` only when a measurement satisfies
*neither* `successCondition` nor `failureCondition` (see `EvaluateResult` in
`utils/evaluate/evaluate.go`). With `successCondition: result == 1` and
`failureCondition: result == 0`, an endpoint returning `2` lands
Inconclusive deterministically — no Prometheus, no flakiness, no waiting for
real traffic.

## Re-parking after you drive a Rollout from the UI

Promoting or aborting `canary-manual` in Radar consumes the fixture. To get
it back into a paused mid-canary state:

```bash
make rollouts-demo-roll     # or: ./scripts/rollouts-demo.sh roll
```

It clears any abort, pushes the next color in the cycle, and waits for the
manual pause. Re-running `up` is also safe — every orchestration step checks
its own end state first, so nothing is rebuilt unnecessarily.

## Coverage notes

- **Capability gating** is per-verb and probed live against
  `patch rollouts` + `patch rollouts/status`. kind gives you cluster-admin,
  so all verbs appear. To exercise the denied path, run Radar with a
  restricted kubeconfig or in-cluster with a narrowed ServiceAccount.
- **Timeline** events come from `diffRollout`; every image bump the script
  performs produces step-index, weight, and pause-condition entries.
- **Topology** shows a `uses` edge from each parked Rollout to its active
  AnalysisRun, labelled by trigger (step / background / pre-promotion).
  Historical runs are deliberately not graphed.
