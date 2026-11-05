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

## Certificate reports

RKE2 and K3s node certificate warnings appear on the Node with the original
monitor report, reported validity dates and last-report time. A fresh warning
appears in Issues as soon as the native monitor reports it, including inside
its renewal window. Radar does not impose a separate 30-day warning cutoff.
Fresh reports become critical under 7 days to expiry or when they report an
invalid certificate. Unknown validity dates remain a warning with the original
report. A validity report that disagrees with Radar's clock shows the reported
clock/validity problem; expiry inside 7 days remains critical even when the
monitor's expired state disagrees with Radar's clock.

Reports older than two hours or spanning a history gap remain visible as
warning Issues with current status unverified. Their last-report time and
original text stay available; a stale report does not claim a current failure
or become critical as its old expiry date passes. Expiry of the Kubernetes
Event does not remove the retained timeline report. A later
`CertificateExpirationOK` supersedes an earlier warning as a reported outcome.
Silence or a CA-only warning does not establish leaf renewal. The warning can
be shown only while its report remains in the existing timeline retention.

Fresh live reports are usable when Event collection covers `default`, including
immediately after Radar starts. For retained reports, the existing timeline's
retention, restart gaps and collection limits apply.
Reports dated in the future or before the current Node's creation are ignored.
Missing reports do not establish certificate health. Radar interprets
`CertificateExpirationWarning`; the native monitor checks certificate bundles,
which can include CA certificates. A restart can renew eligible node certificates;
CAs need separate rotation. The separate `CACertificateExpirationWarning` is not
evaluated here. Node report visibility follows the Node's
cluster-scoped read permission. Node Ready and removal health remain separate.

The resource Issues endpoint's `coverage=1` response can include
`node_certificate_report`. REST AI detail and MCP `get_resource` include the
same assessment as `nodeCertificateReport` with context enabled; MCP also
includes it with `include=issues`. Its `uid` binds the response to the current
Node view; a native name-valued Event UID does not prove a Node incarnation.

## Drain evidence

UI/REST/MCP share a read-only current pod/PDB estimate. MCP `get_resource include=drain-plan` returns at most 100 pods with total/truncation, complete summary counts and explicit options. Read errors and incomplete budget checks are visible.

It is not an eviction history or progress measure. DaemonSet/static/completed pods are *skipped by the estimate*; controller-specific do-not-disrupt/safe-to-evict policies are unevaluated. Known removal hides scheduling actions and uncordon advice.

## Drain completion

Drain waits by default until the Pods whose evictions were accepted are deleted.
An accepted eviction alone is not completion. DaemonSet, static and completed
Pods excluded by the operation do not prevent completion.

A deadline can leave Pods pending deletion. If Radar cannot verify deletion,
it reports unconfirmed Pods and a verification error instead of claiming the
node is drained. REST outcomes distinguish `complete`, `incomplete`,
`unconfirmed` and `started`; `waitForDeletion: false` returns after evictions
are accepted. Review the per-Pod results before proceeding with node removal.
