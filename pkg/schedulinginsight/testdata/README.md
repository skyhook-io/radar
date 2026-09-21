# Kueue Workload fixtures

These fixtures pin the first scheduling-insight adapter to Kueue `v0.19.2`
and its served `kueue.x-k8s.io/v1beta2` Workload API.

Primary sources:

- [`v0.19.2` Workload API types](https://github.com/kubernetes-sigs/kueue/blob/v0.19.2/apis/kueue/v1beta2/workload_types.go)
- [Workload concept and condition lifecycle](https://kueue.sigs.k8s.io/docs/concepts/workload/)
- [Pending Workload observability and granular reasons](https://kueue.sigs.k8s.io/docs/tasks/manage/monitor_pending_workloads/)
- [Concurrent Admission Parent and Variant Workloads](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_concurrent_admission/)

The fixtures are deterministic status snapshots, not claims that a controller
is running in the test process. Live-controller validation remains a separate
scenario lane.
