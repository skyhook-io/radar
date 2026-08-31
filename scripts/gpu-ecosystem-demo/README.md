# GPU, batch, and AI/ML ecosystem demo

This demo creates a dedicated `kind` cluster with pinned upstream CRDs and deterministic fixtures for all 37 GPU, queueing, distributed-training, and inference resource identities curated by Radar.

It answers two different questions:

- `verify`: are the intended upstream schemas, API groups, served versions, scopes, objects, and same-kind collisions present?
- `verify-radar`: can the current Radar build, installed through the shipping Helm chart with default RBAC, discover and return every group-qualified resource?

It does **not** emulate a GPU. Passing this demo makes no claim about device-plugin registration, driver or CUDA health, DRA allocation, physical-device inventory, utilization/accounting, fractional GPU semantics, or actual queue-to-Pod scheduling.

## Quick start

```bash
make gpu-ecosystem-demo
./scripts/gpu-ecosystem-demo.sh install-radar
kubectl config use-context kind-radar-gpu-ecosystem-demo
./scripts/visual-test-start.sh
```

The first run downloads 37 pinned upstream CRDs and can take several minutes. Re-running `up` refreshes the same fixtures without recreating the cluster.

```bash
./scripts/gpu-ecosystem-demo.sh status
./scripts/gpu-ecosystem-demo.sh verify
./scripts/gpu-ecosystem-demo.sh verify-radar
make gpu-ecosystem-demo-down
```

## Coverage contract

[`resources.tsv`](resources.tsv) is the machine-readable contract. Each scenario resolves to exactly 37 logical resource identities. It pins the official CRD URL and records the group, plural, kind, scope, and API version Radar is expected to handle.

| Family | Resources |
|---|---|
| Kueue + provisioning | ClusterQueue, LocalQueue, Workload, ResourceFlavor, AdmissionCheck, ProvisioningRequest |
| KubeRay | RayCluster, RayJob, RayService, RayCronJob |
| KServe | InferenceService, ServingRuntime, ClusterServingRuntime, InferenceGraph, TrainedModel, LLMInferenceService |
| Inference routing | InferencePool, InferenceObjective |
| Batch orchestration | LeaderWorkerSet, JobSet; Volcano Job, Queue, PodGroup, JobFlow, JobTemplate; KAI Queue and PodGroup |
| Kubeflow Training | PyTorchJob, TFJob, MPIJob, TrainJob |
| Model serving/operators | KAITO Workspace and RAGEngine; NVIDIA NIMService, NIMCache, NIMPipeline; AMD DeviceConfig |

The fixtures deliberately include group collisions:

- core `batch/v1` Job and Volcano `batch.volcano.sh` Job share `collision-demo`
- Volcano and KAI both define `Queue` and `PodGroup`
- the group-qualified Radar API checks ensure these remain separate

Statuses are applied through each CRD's status subresource where available, with a whole-object patch for CRDs that do not expose one. Their timestamps are relative to the run, so age-based UI remains useful instead of silently rotting.

## Inference API scenarios

The default `current` scenario installs the current Inference Gateway family:

- `InferencePool` from `inference.networking.k8s.io`
- `InferenceObjective` from `llm-d.ai`

An explicit experimental scenario replaces those two rows with the older experimental family:

```bash
SCENARIO=experimental ./scripts/gpu-ecosystem-demo.sh reset
SCENARIO=experimental ./scripts/gpu-ecosystem-demo.sh install-radar
```

Only one family is installed per cluster. If the requested scenario differs from the cluster's state marker, the script stops and requires `reset`; it never creates an ambiguous same-kind test by accident.

## Why the controllers are not installed

This suite is breadth-first and renderer-focused. Installing every controller would add image pulls, webhooks, reconciliation races, and inter-controller dependencies while still providing no physical GPU. Pinned real CRDs preserve upstream schema validation; controller-free status fixtures make the 37-way surface deterministic.

Add a live-controller mode only when a Radar feature depends on controller behavior rather than the resource contract or rendered state.

For real KubeRay ownership and active/pending RayService reconciliation, use the
focused [`kuberay-demo`](../kuberay-demo/README.md) lane. It complements this
37-kind breadth fixture rather than adding a heavy Ray runtime here.

## Real GPU acceptance lane

Before release claims involving hardware, run one ephemeral GPU node in a real managed cluster and verify:

1. the vendor device plugin registers `nvidia.com/gpu` or `amd.com/gpu` capacity;
2. a GPU-requesting Pod schedules and completes a vendor smoke test such as `nvidia-smi`;
3. Radar shows the real node capacity, requests, workload, and operator resources;
4. DRA is exercised only when the cluster, driver, and Radar feature under test all use it.

The lane is intentionally separate because `kind` cannot prove those facts. Keep the node pool at zero outside the run and delete smoke-test resources afterward.

## Updating the suite

When curated coverage changes:

1. update `resources.tsv` with a tagged upstream CRD URL;
2. update or add a fixture that passes that exact schema;
3. preserve one logical identity per scenario;
4. run both `verify` and `verify-radar`;
5. update the coverage table and the registry drift test together.

Version bumps are deliberate. A newer tag can change served versions, schemas, field paths, printer-column status assumptions, or API groups even when the kind name stays the same.
