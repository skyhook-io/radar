# Kueue fixtures

`rad819-live.json` is every Workload, LocalQueue, Job (metadata only), ClusterQueue and AdmissionCheck of the
RAD-819 Kueue scenarios as the real Kueue v0.18.0 controller left them on an EKS 1.34 test cluster
(2026-11-03). `managedFields`, last-applied annotations, Workload `spec.podSets` and
`status.resourceRequests` are stripped; nothing else is edited. Two inputs were written by the scenario
script rather than a controller: the AdmissionCheck states in `gpu-night-819-busy` (the script plays the check
controller) and the `PodsReadyTimeout` eviction in `gpu-night-819-drain`. Everything else is controller output.

Synthetic objects in the tests cover what the live cluster can't produce: stale conditions, recovery,
recreated roots, concurrent-admission variants, ProvisioningRequests, and caller denial.
