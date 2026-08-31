# Execution-insight fixtures

## JobSet

These snapshots pin the first execution-insight adapter to JobSet `v0.12.0`
(`f22e565ac3cc3265c7c45ef67ecffdc689af5d77`) and its served
`jobset.x-k8s.io/v1alpha2` API.

Primary sources:

- [`v0.12.0` JobSet API types](https://github.com/kubernetes-sigs/jobset/blob/v0.12.0/api/jobset/v1alpha2/jobset_types.go)
- [`v0.12.0` controller status aggregation and child ownership](https://github.com/kubernetes-sigs/jobset/blob/v0.12.0/pkg/controllers/jobset_controller.go)
- [`v0.12.0` failure-policy and terminal-state handling](https://github.com/kubernetes-sigs/jobset/blob/v0.12.0/pkg/controllers/failure_policy.go)
- [JobSet concepts](https://jobset.sigs.k8s.io/docs/concepts/)

The fixtures are deterministic API snapshots, not claims that a controller is
running in the test process. In particular, the partial snapshot pins that
unreported roles are not interpreted as observed zero and that a failed child
Job does not make the JobSet terminal while controller policy may recover it.

## RayService

The RayService snapshot pins KubeRay `v1.7.0`
(`59d663aeec13760646da3db9c1cf8cfe883e30b7`) and its exact `ray.io/v1`
API. Conditions, active/pending runtime slots, traffic percentages, and target
capacity all come from controller-owned status.

Primary sources:

- [`v1.7.0` RayService API types](https://github.com/ray-project/kuberay/blob/v1.7.0/ray-operator/apis/ray/v1/rayservice_types.go)
- [`v1.7.0` RayCluster API types](https://github.com/ray-project/kuberay/blob/v1.7.0/ray-operator/apis/ray/v1/raycluster_types.go)
- [`v1.7.0` RayService controller](https://github.com/ray-project/kuberay/blob/v1.7.0/ray-operator/controllers/ray/rayservice_controller.go)
- [`v1.7.0` RayService controller tests](https://github.com/ray-project/kuberay/blob/v1.7.0/ray-operator/controllers/ray/rayservice_controller_unit_test.go)

The snapshot intentionally carries both upgrade and rollback conditions because
the controller can retain `UpgradeInProgress=True` while rollback is active.
It also proves that zero target or traffic percentages remain reported zero,
not unavailable.
