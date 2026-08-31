# JobSet execution fixtures

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
