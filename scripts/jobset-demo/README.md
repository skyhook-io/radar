# JobSet live controller demo

This lane creates a dedicated `kind` cluster with the real JobSet controller and
three small JobSets. The controller, rather than fixture patches, creates the
Jobs, propagates role/index identity to Pods, enforces a dependency, and writes
terminal status.

It complements [`gpu-ecosystem-demo`](../gpu-ecosystem-demo/README.md). That
deterministic breadth lane validates all 37 curated CRD identities and rendered
states. This focused lane validates JobSet reconciliation without turning the
breadth suite into a controller-heavy mega-cluster.

## Quick start

```bash
# Prerequisites: Docker, kind, kubectl, jq
make jobset-demo              # normally 2-4 minutes on the first run
make jobset-demo-status

# `up` restores the current-context value it observed, including an unset value.
# Use an isolated kubeconfig so running Radar does not change shared kube state.
make build
kind get kubeconfig --name radar-jobset-demo > /tmp/radar-jobset-demo.kubeconfig
./radar --kubeconfig /tmp/radar-jobset-demo.kubeconfig --port 9332 --no-browser
RADAR_URL=http://127.0.0.1:9332 ./scripts/jobset-demo.sh verify-radar

# When done
make jobset-demo-down
```

`up` is idempotent and reuses only a cluster carrying this demo's ownership
marker. `down` refuses to delete a same-named cluster when that marker is absent
or mismatched. Set `CLUSTER_NAME` to create an independently named lane.

## Controller-earned scenarios

All fixtures live in `jobset-demo` and use JobSet `v0.12.0` with its
`jobset.x-k8s.io/v1alpha2` API on Kubernetes `v1.36.1`. The JobSet release and
workload image tags are version-pinned; the kind node is pinned by digest. The
controller manifest is fetched from the versioned `v0.12.0` release URL.

| JobSet | Expected reconciled state | What `verify` proves |
|---|---|---|
| `roles-running` | One `leader` Job and two indexed `workers` Jobs in the `training` group each have a Running Pod | JobSet UID owner references; role, per-role index, globally unique index, group name, group replica count, and group index labels on Jobs; Job UID owners and matching propagated labels on Pods; ready/active role counts |
| `dependency-held` | Its `initializer` is Running while `workers` depend on initializer `Complete` | The initializer Job/Pod lineage is real; the root stays nonterminal; no worker Job or Pod is created before the declared dependency is satisfied |
| `terminal-failure` | A worker exits nonzero and the named `FailJobSet` rule ends the root | Failed child Job/Pod lineage; `BackoffLimitExceeded`; root `terminalState: Failed`; `Failed=True` with `FailJobSetFailurePolicyAction` |

The script never patches a status subresource. Re-running `verify` checks the
live end state and fails if ownership, identity propagation, dependency gating,
or terminal conditions are wrong.

Re-running `up` preserves all three JobSets once they exist. Use
`make jobset-demo-reset` after changing their specs or to recover a partially
edited scenario. `make jobset-demo-verify` rechecks the live states without
installing or applying anything.

## Radar testing

`verify-radar` expects an already-running Radar and checks:

- the health endpoint;
- exactly three group-pure `jobset.x-k8s.io/v1alpha2` JobSets;
- the running, dependency-held, and terminal-failure states returned by Radar;
- the controller-created Jobs and Pods returned by Radar's core resource APIs.

For visual testing, select the dedicated context before starting the normal
visual workflow and restore the previous context afterward:

```bash
kubectl config use-context kind-radar-jobset-demo
./scripts/visual-test-start.sh
```

## Proof boundary

Passing this lane proves that JobSet `v0.12.0` on kind Kubernetes `v1.36.1`
can create and identify role-, group-, and index-labelled Jobs and Pods,
withhold a dependent role, produce one explicit terminal failure, and expose
those objects through Radar's group-aware APIs.

It does **not** prove:

- GPU hardware, device plugins, DRA allocation, utilization, or cost;
- Kueue admission, quota, preemption, or topology-aware scheduling;
- framework-specific semantics for Ray, Kubeflow Training, MPI, PyTorch, or inference;
- JobSet success-policy cleanup timing, global or per-Job restart behavior,
  exclusive placement, elastic scaling, suspension, PVC retention, or very large fanout;
- partial-RBAC behavior or compatibility beyond the pinned releases.

The omitted lifecycle branches belong in focused parser fixtures or later live
lanes when Radar ships fields that depend on them. The terminal-failure fixture
deliberately omits `ttlSecondsAfterFinished` so its failed Job and Pod remain
inspectable; the lane does not generalize that retention to cleanup behavior in
other JobSet configurations.

The lane uses local Docker/kind only and has no cloud cost. The first run is
normally 2-4 minutes depending on image and network cache; every individual
state wait is capped at 240 seconds. Version bumps are deliberate because the
JobSet API, controller conditions, and Kubernetes compatibility can change
together.
