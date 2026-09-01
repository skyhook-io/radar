# Kueue live admission demo

This lane creates a dedicated `kind` cluster with the real Kueue controller and
three ordinary Kubernetes Jobs. Kueue, rather than fixture patches, creates the
Workloads, conditions, queue assignments, Job suspension changes, and Pods.

It complements [`gpu-ecosystem-demo`](../gpu-ecosystem-demo/README.md). That
deterministic breadth lane validates all 37 curated CRD identities, schemas,
collisions, Radar discovery, and chart RBAC. This focused lane validates one
controller lifecycle deeply without turning the breadth suite into a fragile
mega-cluster.

## Quick start

```bash
# Prerequisites: Docker, kind, kubectl, jq, curl
make kueue-demo              # ~2-4 minutes on the first run
make kueue-demo-status

# `up` restores the previous current-context value, including an unset value.
# Use an isolated kubeconfig to run Radar without changing it.
make build
kind get kubeconfig --name radar-kueue-demo > /tmp/radar-kueue-demo.kubeconfig
./radar --kubeconfig /tmp/radar-kueue-demo.kubeconfig --port 9331 --no-browser
RADAR_URL=http://127.0.0.1:9331 ./scripts/kueue-demo.sh verify-radar

# When done
make kueue-demo-down
```

`up` is idempotent and reuses only a cluster carrying the demo's ownership
marker. `down` refuses to delete a same-named cluster when that marker is absent
or mismatched. Set `CLUSTER_NAME` to create an independently named lane.

## Controller-earned scenarios

All fixtures live in `kueue-demo` and use Kueue `v0.19.2` with its `v1beta2`
API on Kubernetes `v1.36.1`. The kind node is pinned by release digest rather
than following kind's changing default. The workload image is the pinned, multi-architecture
`registry.k8s.io/pause:3.10` image.

| Job | Queue path | Expected reconciled state | What `verify` proves |
|---|---|---|---|
| `admitted-running` | `ready` → `admission-ready` | Kueue reserves quota and admits the Workload, unsuspends the Job, and a Pod reaches `Running` | Owner UID; `QuotaReserved=True/QuotaReserved`, `Admitted=True/Admitted`, `PodsReady=True/Started` on a clean run or `Recovered` after a controller-observed interruption; `spec.suspend=false`; running Pod |
| `quota-blocked` | `ready` → `admission-ready` | Its 3-CPU request exceeds the ClusterQueue's 2-CPU nominal quota, so it remains pending before Pod creation | Owner UID; `QuotaReserved=False/Pending` with an insufficient-quota/maximum-capacity message; no admission assignment; suspended Job; zero Pods |
| `queue-held` | `held` → `admission-held` | `stopPolicy: Hold` makes the ClusterQueue and LocalQueue inactive and withholds admission | `Active=False/Stopped`, `Active=False/ClusterQueueIsInactive`, `QuotaReserved=False/Inadmissible` naming the inactive ClusterQueue; suspended Job; zero Pods |

The script never patches a status subresource. Re-running `verify` checks the
live end state and fails if a controller-owned relationship, condition,
suspension decision, or Pod presence/absence is wrong.

Re-running `up` reapplies namespace and queue configuration but preserves all
three Jobs once they exist, so the admitted Pod is not disrupted merely to
refresh the lane. Use `reset` after changing Job fixtures or to recover a
partially edited scenario.

## Radar testing

`verify-radar` expects an already-running Radar and checks:

- the health endpoint;
- exactly three group-pure `kueue.x-k8s.io/v1beta2` Workloads;
- admitted, quota-blocked, and held-queue conditions as returned by Radar;
- both group-pure ClusterQueues;
- the exact scheduling source, domain, full-GVK subject, generation-aware
  condition, decision, queue roles, and Kueue phase for every scenario;
- identical basic-tier `resourceContext.scheduling` projections from the REST
  AI resource endpoint and MCP `get_resource`.

For visual testing, use the dedicated context before starting the normal visual
workflow:

```bash
kubectl config use-context kind-radar-kueue-demo
./scripts/visual-test-start.sh
```

Restore your previous context afterward if you use this form.

## Proof boundary

Passing this lane proves that Kueue `v0.19.2` on kind Kubernetes `v1.36.1`
can reconcile a healthy admission and two real pre-Pod blockers, and
that Radar can read those resulting objects through group-aware APIs while the
REST AI resource endpoint and MCP `get_resource` expose the same basic-tier
normalized scheduling evidence.

It does **not** prove:

- GPU hardware, device-plugin registration, CUDA/driver health, or physical inventory;
- DRA allocation, MIG/fractional units, accelerator utilization, or cost;
- Kueue AdmissionCheck or ProvisioningRequest controllers;
- queue position, borrowing, cohorts, preemption, topology-aware scheduling, or MultiKueue;
- partial-RBAC behavior or controller-version compatibility beyond the pinned release.

The lane uses local Docker/kind only and has no cloud cost. The first run is
normally 2–4 minutes depending on image/network cache; repeat runs are faster.
Version bumps are deliberate because Kueue API versions, conditions, admission
semantics, and Kubernetes compatibility can change together.
