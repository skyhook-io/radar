# KubeRay RayService controller demo

This demo creates a dedicated `kind` cluster and runs one real KubeRay
`RayService` through the minimum revision lifecycle Radar needs to understand:

1. KubeRay creates a head-only RayCluster, Ray starts, the Serve application
   becomes healthy, and a request returns `radar-kuberay-ready`.
2. The script changes only the head image in the RayService spec. KubeRay's
   `NewCluster` strategy keeps the healthy cluster active while creating a
   distinct pending RayCluster whose head intentionally cannot start.

The pending failure is a fixture, not an installation problem. It makes the
active-versus-pending revision state durable enough for Radar API and UI work
without patching any status.

## Quick start

```bash
make kuberay-demo
./scripts/kuberay-demo.sh status
./scripts/kuberay-demo.sh verify
make kuberay-demo-down
```

`up` and `reset` restore the kubeconfig current-context they observed, including
an unset context. Every internal command addresses
`kind-radar-kuberay-demo` explicitly. To inspect it yourself without changing
the shared context:

```bash
kubectl --context kind-radar-kuberay-demo -n kuberay-demo get rayservices,rayclusters,pods,services
```

The first run pulls the Ray image at roughly 824 MB compressed on arm64 (the
architecture-specific size varies), plus the kind node and 29 MB operator
image. Budget roughly 5-10 minutes on an uncached Docker installation. The two
head Pods request a combined 2 CPU and 4 GiB while the final fixture is present.
Everything runs in local Docker; the script creates no cloud resources or cloud
cost.

## What the lane proves

| Evidence | Assertion |
|---|---|
| RayService baseline | During the initial `up`, before the revision, KubeRay reports `Ready=True`, the `radar-demo` application is `RUNNING`, its deployment is `HEALTHY`, and the Serve endpoint answers |
| Revision identity | RayService reports distinct `activeServiceStatus.rayClusterName` and `pendingServiceStatus.rayClusterName` values with `UpgradeInProgress=True/BothActivePendingClustersExist` |
| Active continuity | RayService remains `Ready=True/NonZeroServeEndpoints`; the active RayCluster's authoritative Ready/Provisioned conditions remain true; its Serve endpoint still answers after the pending failure |
| Pending failure | The directly owned pending RayCluster reports `HeadPodReady=False` with `CrashLoopBackOff` or `RunContainerError`, plus `RayClusterProvisioned=False/RayClusterPodsProvisioning` |
| Ownership | Both RayClusters have controller owner references to the RayService UID; each head Pod and head Service has a controller owner reference to its RayCluster UID; the stable head and Serve Services are owned by the RayService UID |
| Identity labels | RayClusters carry KubeRay origin labels; Pods carry `ray.io/cluster`, `ray.io/group=headgroup`, `ray.io/node-type=head`, and active/pending Serve-role labels; Services select the correct revision |
| Immutable inputs | Kubernetes 1.36.1 kind image, KubeRay v1.7.0 source commit and operator digest, Ray 2.55.0 multi-arch image digest, and the pause image digest match the ownership marker |

KubeRay v1.7.0 exposes the active and pending names on RayService, but does not
copy the pending RayCluster's failure conditions into
`pendingServiceStatus.rayClusterStatus` in this state. The script intentionally
asserts revision identity at RayService and failure details on the directly
owned RayCluster instead of inventing an embedded condition.

The pending head's `HeadPodReady` reason alternates between
`RunContainerError` during a start attempt and `CrashLoopBackOff` between
attempts. Both are controller-earned views of the same pinned pause image
missing KubeRay's generated `/bin/bash` command, so verification accepts exactly
those two reasons while requiring the Pod to remain NotReady.

## Radar API smoke test

Build Radar, export this cluster into an isolated kubeconfig, and start Radar in
another terminal:

```bash
make build
RADAR_KUBECONFIG="$(mktemp)"
kind export kubeconfig --name radar-kuberay-demo --kubeconfig "${RADAR_KUBECONFIG}"
KUBECONFIG="${RADAR_KUBECONFIG}" ./radar --port 9333 --no-browser
```

Then run:

```bash
RADAR_URL=http://127.0.0.1:9333 ./scripts/kuberay-demo.sh verify-radar
```

That checks Radar's real group-aware `rayservices.ray.io` and
`rayclusters.ray.io` browse paths, native controller status, and the generated
Pod/Service lineage. Browser smoke is optional for this script-only fixture;
use the repository visual-test workflow when changing a visible KubeRay
surface.

## Exact pins

| Component | Pin |
|---|---|
| Kubernetes | `v1.36.1` |
| kind node | `kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5` |
| KubeRay | `v1.7.0`, source commit `59d663aeec13760646da3db9c1cf8cfe883e30b7` |
| KubeRay operator | `quay.io/kuberay/operator:v1.7.0@sha256:4a779237ef1c5262a63840ccf42d0d67f0b74e911158fbcaee4478fb1560bce6` |
| Ray | `2.55.0` |
| Ray runtime | `rayproject/ray:2.55.0@sha256:71bbcb7cf031c290d9f23a8fd54d8602c8f0f2004b73d616f97dddc1975e9bd4` |
| Intentional pending failure | `registry.k8s.io/pause:3.10@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a` |

The KubeRay source uses the immutable commit behind the official v1.7.0 tag.
Changing any contract pin requires `reset`; an existing ownership marker with a
different contract is refused.

## Proof boundary

This lane proves a head-only, CPU-only Ray Serve revision lifecycle under a real
KubeRay controller. It does **not** prove:

- GPU scheduling, drivers, device plugins, CUDA, DRA, or utilization;
- Ray worker groups, autoscaling, gang scheduling, or Kueue admission;
- RayJob or RayCronJob execution;
- incremental Gateway upgrades, traffic shifting, or zero-downtime guarantees;
- external telemetry, accounting, capacity planning, or cost attribution;
- behavior on KubeRay/Ray/Kubernetes versions other than the exact pins above.

Use [`../gpu-ecosystem-demo/README.md`](../gpu-ecosystem-demo/README.md) for the
deterministic 37-kind schema/renderer matrix, and a real GPU cluster for any
hardware claim.
