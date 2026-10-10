# Node status and removal

This cross-surface contract is implemented in `pkg/health/node_lifecycle.go` and mirrored for raw-node UI in `packages/k8s-ui/src/utils/node-lifecycle.ts`. Shared fixtures compare complete results.

- Active removal requires `ToBeDeletedByClusterAutoscaler:NoSchedule`, `karpenter.sh/disrupted:NoSchedule`, or deletion. A soft autoscaler candidate does not explain readiness loss.
- Readiness loss is expected only after a trustworthy removal start; earlier failures and unknown timing remain actionable. CA records Unix seconds; deletion also dates removal. Karpenter's marker alone supplies no start.
- Memory/disk/PID pressure remains critical independently. NetworkUnavailable warns after two minutes; missing/future transition times establish no grace. Known removals warn at 10 minutes and become critical at 30.
- Plain cordon is amber, names no actor and raises no issue. Confirm maintenance finished before uncordoning. GKE host maintenance or upgrades do not establish removal.

## Surfaces

Table, drawer and topology use lifecycle health. Raw conditions/YAML and historical timeline observations stay visible. REST AI context/MCP expose `nodeSummary.lifecycle` with `readyStatus`; minified list/search rows retain raw Ready separately.

Dashboard/vitals/MCP share exclusive Ready / NotReady / Cordoned / Removing buckets. Independent readiness failure stays NotReady; RemovingUnhealthy is a subset for pressure/prolonged removal. Capacity exposes the same member lifecycle and `nodes.operational` buckets alongside overlapping raw counts. Group readiness, allocatable, requests, scheduler predicates and NodeClaims retain their factual meanings.

NotReady retains its identity. Pressure/network issues have separate identities and condition-specific pod attribution. Removal delayed uses `termination_stuck`, timed from removal onset.

## Drain evidence

UI/REST/MCP share a read-only current pod/PDB estimate. MCP `get_resource include=drain-plan` returns at most 100 pods with total/truncation, complete summary counts and explicit options. Read errors and incomplete budget checks are visible.

It is not an eviction history or progress measure. DaemonSet/static/completed pods are *skipped by the estimate*; controller-specific do-not-disrupt/safe-to-evict policies are unevaluated. Known removal hides scheduling actions and uncordon advice.
