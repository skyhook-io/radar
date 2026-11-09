# Kueue fixtures

`queue-scenarios-live.json` is every Workload, LocalQueue, Job (metadata only), ClusterQueue and AdmissionCheck of the
scripted Kueue scenarios as the real Kueue v0.18.0 controller left them on an EKS 1.34 test cluster
(2026-11-03). `managedFields`, last-applied annotations, Workload `spec.podSets` and
`status.resourceRequests` are stripped; nothing else is edited. Two inputs were written by the scenario
script rather than a controller: the AdmissionCheck states in `gpu-night-819-busy` (the script plays the check
controller) and the `PodsReadyTimeout` eviction in `gpu-night-819-drain`. Everything else is controller output.

`provisioning-live.json` is Kueue v0.18.0's real provisioning AdmissionCheck controller on the same
cluster (2026-11-03): a ClusterQueue with a `kueue.x-k8s.io/provisioning-request` check (retry limit 1) and two
Jobs. No ProvisioningRequest provider runs there, so the provider's conditions were written by hand: attempt 1
`Failed` (Kueue answered with `Retry`, evicted, requeued and created attempt 2), attempt 2 `Failed` (Kueue rejected
the check and deactivated the run), and on the second, admitted run `Provisioned` then `CapacityRevoked` (Kueue
rejected and deactivated it). Kueue's reactions are controller output.

Synthetic objects in the tests cover what the live cluster can't produce: stale conditions, recovery,
recreated roots, concurrent-admission variants, ProvisioningRequests, and caller denial.
