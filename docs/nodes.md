# Node status and removal

Node status separates controller intent from readiness. Radar recognizes the
cluster autoscaler's `ToBeDeletedByClusterAutoscaler:NoSchedule` marker,
Karpenter's current `karpenter.sh/disrupted:NoSchedule` marker, and a Node's
deletion timestamp. A soft
`DeletionCandidateOfClusterAutoscaler:PreferNoSchedule` marker means a candidate,
not an active removal. A plain cordon remains amber because scheduling capacity
is unavailable; it is not an operational issue and does not identify its actor.

Normal removal appears calmly in node status and has a separate fleet count.
A readiness failure remains actionable when it predates removal or the evidence
cannot establish that it followed removal. Disk, memory and PID pressure remain critical independently. NetworkUnavailable
gets a two-minute initialization grace when its transition time is known, then
warns; an unknown transition time is reported conservatively as a warning. The autoscaler records removal time as a Unix
value; a deletion timestamp also establishes a start. NoSchedule taints do not
normally record their start time. Known removals still present after 10 minutes
warn, and after 30 minutes become critical, matching Radar's termination windows.
Unknown start times are shown as unavailable, never timed from node creation.

The removal drawer refreshes a read-only drain snapshot every 30 seconds while
open. It lists pods, observed termination states, and named disruption budgets
that may refuse an eviction. These are estimates, not proof of a controller's
failed eviction or a count of previously evicted pods. Budget-read errors are
shown explicitly. Controller-specific do-not-disrupt/safe-to-evict policies are
not evaluated by the drain estimate. Older Radar versions that serve drain plans without per-pod
termination state display that field as unavailable. Uncordon advice and
scheduling actions are absent during known removal; an unexplained cordon asks
operators to confirm that maintenance is finished before resuming scheduling.

GKE host-maintenance taints can describe a VM restart, not removal; they do not
establish a removal state. Provider-specific upgrade detection and a history of removed nodes are not
inferred from a cordon or a node-pool label. Historical Karpenter taint aliases
are not inferred from the current marker contract.

Fleet counters are exclusive: an independent readiness failure stays in NotReady even when removal is also requested. Other removing nodes use the Removing bucket, whose unhealthy subset preserves pressure and prolonged-removal severity.

GKE conditions `StoragePressureRootFileSystem`, `DPv2MigrationUnsupportedCNI`, and `UnsupportedEBPFPrograms` have negative polarity: False means the problem is absent. Their raw values remain visible; they do not create false failing-condition counts.
