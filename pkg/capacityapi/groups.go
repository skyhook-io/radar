package capacityapi

import "time"

// CapacityManager identifies what manages a capacity group's size. A group
// with no detected manager carries an empty value — "none detected" is a
// statement about observation, never a manager, and is excluded from manager
// rollups.
type CapacityManager string

const (
	ManagerKarpenter         CapacityManager = "karpenter"
	ManagerClusterAutoscaler CapacityManager = "cluster_autoscaler"
	ManagerGKEAutoscaler     CapacityManager = "gke_autoscaler"
	ManagerAKSAutoscaler     CapacityManager = "aks_autoscaler"
)

// CapacityGroupSummary is a logical node group a user recognizes: a Karpenter
// NodePool, a GKE node pool, an EKS managed node group, an AKS agent pool.
// Identity requires node-label or CRD evidence — provider group names are
// never parsed into identity (they truncate). Nodes with no identity evidence
// are an unattributed presentation bucket, never a group.
type CapacityGroupSummary struct {
	// ID is "<label-domain>/<name>", e.g. "karpenter-nodepool/default",
	// "gke-nodepool/pool-1", "eks-nodegroup/system", "aks-agentpool/np1".
	// Stable across manager detection changes.
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Manager CapacityManager `json:"manager,omitempty"`
	// Platform is the identity domain the group was recognized BY — the node
	// label or CRD family that makes the row exist ("karpenter", "gke", "eks",
	// "aks", "kops", "doks", "ack", "lke", "scaleway"). Always present, and
	// deliberately distinct from Manager: a static EKS managed node group has a
	// platform and no manager, and rendering "none detected" for its platform
	// would read as failing to recognize the fleet's most basic fact.
	Platform string `json:"platform"`
	// ManagerValidated is false when the manager link rests on a join rule
	// Radar has not validated against a live cluster of this provider.
	ManagerValidated bool `json:"managerValidated"`
	NodeCount        int  `json:"nodeCount"`
	ReadyNodeCount   int  `json:"readyNodeCount"`
	// Allocatable and ScheduledRequests follow the ledger rules: absent means
	// unobserved, and pod-derived values appear only when pods were observed.
	Allocatable       *QuantityObservation `json:"allocatable,omitempty"`
	ScheduledRequests *QuantityObservation `json:"scheduledRequests,omitempty"`
	// Scaling is the typed scaling story — always at least one fact, never a
	// dash: bounds/target when published, otherwise an honest statement of
	// what is not published or not detected.
	Scaling []ScalingFact `json:"scaling"`
	// Children are the autoscaler's own per-zone groups (MIGs/VMSS) joined to
	// this logical group by node-name-prefix evidence.
	Children     []AutoscalerChildObservation `json:"children"`
	ChildrenMeta BoundedResultMeta            `json:"childrenMeta"`
}

type ScalingFact struct {
	// Code is machine-readable, and is exactly one of: "limits" (Karpenter
	// NodePool spec.limits, configured or not), "pool_not_observed" (a
	// Karpenter-labeled group whose NodePool could not be read), "bounds" and
	// "target" (published by the autoscaler), "bounds_not_published" (an
	// autoscaler is running but says nothing about this group),
	// "no_manager_detected" (nothing manages this group's size), and
	// "manager_detection_unavailable" (the detection source itself was denied
	// or unreadable — never conflate with "no manager").
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

// AutoscalerChildObservation is one node group as the autoscaler itself
// reports it (a zonal MIG, a VMSS). Its ID is the published name's basename
// and never changes when the child gains or loses a parent group — orphans
// (no joinable nodes, including scale-to-zero groups) stay orphans until
// nodes appear.
type AutoscalerChildObservation struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	MinSize    *int               `json:"minSize,omitempty"`
	MaxSize    *int               `json:"maxSize,omitempty"`
	Target     *int               `json:"target,omitempty"`
	Health     string             `json:"health,omitempty"`
	ReadyNodes *int               `json:"readyNodes,omitempty"`
	TotalNodes *int               `json:"totalNodes,omitempty"`
	Backoff    *AutoscalerBackoff `json:"backoff,omitempty"`
	// AsOf is the autoscaler's own probe time for this child; nil when the
	// payload published null (inactive groups do). Nil is "not probed", not
	// zero and not now.
	AsOf *time.Time `json:"asOf,omitempty"`
}

type AutoscalerBackoff struct {
	ErrorClass   string `json:"errorClass,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type ManagerRollupStatus string

const (
	ManagerHealthy  ManagerRollupStatus = "healthy"
	ManagerDegraded ManagerRollupStatus = "degraded"
	ManagerUnknown  ManagerRollupStatus = "unknown"
)

// ManagerSummary is one detected capacity manager with a worst-of health
// rollup. A denied or stale source never rolls up as healthy.
type ManagerSummary struct {
	Manager    CapacityManager     `json:"manager"`
	GroupCount int                 `json:"groupCount"`
	Status     ManagerRollupStatus `json:"status"`
	Detail     string              `json:"detail,omitempty"`
}
