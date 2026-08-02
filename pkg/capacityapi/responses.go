package capacityapi

import "time"

type ActionSummary struct {
	Code            string             `json:"code"`
	Count           int                `json:"count"`
	HighestSeverity string             `json:"highestSeverity,omitempty"`
	Pools           []ResourceIdentity `json:"pools"`
	Truncated       bool               `json:"truncated"`
}

func NewActionSummary() ActionSummary {
	return ActionSummary{Pools: []ResourceIdentity{}}
}

type OverviewSummary struct {
	Actions         []ActionSummary        `json:"actions"`
	AggregateDemand *QuantityObservation   `json:"aggregateDemand,omitempty"`
	Scheduling      *SchedulingCapacity    `json:"scheduling,omitempty"`
	ClaimStages     *ClaimLifecycleSummary `json:"claimStages,omitempty"`
	// PoolCount is emitted only when NodePool coverage was actually observed.
	// Absent means unobserved (denied, syncing, or Karpenter not installed) —
	// a serialized 0 would read as "this fleet has no NodePools".
	PoolCount          *int `json:"poolCount,omitempty"`
	ClaimCount         *int `json:"claimCount,omitempty"`
	NodeCount          *int `json:"nodeCount,omitempty"`
	PendingPodCount    *int `json:"pendingPodCount,omitempty"`
	OrphanedClaimCount *int `json:"orphanedClaimCount,omitempty"`
	UnpooledNodeCount  *int `json:"unpooledNodeCount,omitempty"`
	// ClusterScheduling is scheduled requests vs allocatable across ALL
	// observed nodes, every manager included. Scheduling (above) stays
	// Karpenter-scoped forever — consumers depend on that meaning.
	ClusterScheduling *SchedulingCapacity `json:"clusterScheduling,omitempty"`
	// Managers lists detected capacity managers; interpretation is gated on
	// coverage (an empty list under denied node coverage is not "none").
	Managers []ManagerSummary `json:"managers"`
	// UnattributedNodeCount counts nodes with no group-identity evidence at
	// all — a presentation bucket, deliberately never called "static".
	UnattributedNodeCount *int `json:"unattributedNodeCount,omitempty"`
}

// SchedulingCapacity is the cluster-level scheduling ledger across
// Karpenter-pooled nodes only — scheduled requests vs allocatable, plus
// capacity of claims still in flight. Each value carries its own certainty;
// absent values mean the source was not observed (unavailable ≠ zero).
type SchedulingCapacity struct {
	ScheduledRequests *QuantityObservation `json:"scheduledRequests,omitempty"`
	Allocatable       *QuantityObservation `json:"allocatable,omitempty"`
	InFlightCapacity  *QuantityObservation `json:"inFlightCapacity,omitempty"`
	// NegativePriorityRequests is the SUBSET of ScheduledRequests coming from
	// pods with spec.priority < 0 — potential preemption victims. Never add it
	// to ScheduledRequests. It states a measured priority fact, not intent:
	// whether such pods are an overprovisioning buffer or productive batch
	// work, their requests are reclaimable by default-priority workloads
	// (subject to preemption policy, placement, and disruption constraints).
	NegativePriorityRequests *QuantityObservation `json:"negativePriorityRequests,omitempty"`
}

type OverviewResponse struct {
	ResponseMeta
	State          IntegrationState `json:"state"`
	Summary        OverviewSummary  `json:"summary"`
	Pools          []PoolSummary    `json:"pools"`
	PoolsTruncated bool             `json:"poolsTruncated"`
	// Groups is the logical-group inventory across every manager (Karpenter
	// pools included). Interpretation is coverage-gated: empty under observed
	// node coverage is a true zero; under denied coverage it is unavailable.
	Groups []CapacityGroupSummary `json:"groups"`
	// OrphanAutoscalerGroups are autoscaler-known groups with no joinable
	// nodes (scale-to-zero included) — never counted as logical groups.
	OrphanAutoscalerGroups     []AutoscalerChildObservation `json:"orphanAutoscalerGroups"`
	OrphanAutoscalerGroupsMeta BoundedResultMeta            `json:"orphanAutoscalerGroupsMeta"`
}

func NewOverviewResponse(generatedAt time.Time) OverviewResponse {
	return OverviewResponse{
		ResponseMeta: NewResponseMeta(generatedAt),
		State:        IntegrationSyncing,
		Summary: OverviewSummary{
			Actions:  []ActionSummary{},
			Managers: []ManagerSummary{},
		},
		Pools:                  []PoolSummary{},
		Groups:                 []CapacityGroupSummary{},
		OrphanAutoscalerGroups: []AutoscalerChildObservation{},
	}
}

type PoolDetailResponse struct {
	ResponseMeta
	State IntegrationState `json:"state"`
	Pool  *PoolObservation `json:"pool,omitempty"`
}

func NewPoolDetailResponse(generatedAt time.Time) PoolDetailResponse {
	return PoolDetailResponse{ResponseMeta: NewResponseMeta(generatedAt), State: IntegrationSyncing}
}

type PoolListResponse struct {
	ResponseMeta
	State IntegrationState `json:"state"`
	Items []PoolSummary    `json:"items"`
	Page  PageInfo         `json:"page"`
}

func NewPoolListResponse(generatedAt time.Time) PoolListResponse {
	return PoolListResponse{
		ResponseMeta: NewResponseMeta(generatedAt),
		State:        IntegrationSyncing,
		Items:        []PoolSummary{},
	}
}

type MemberListResponse struct {
	ResponseMeta
	State IntegrationState `json:"state"`
	Pool  ResourceIdentity `json:"pool"`
	Type  MemberType       `json:"type"`
	Items []PoolMember     `json:"items"`
	Page  PageInfo         `json:"page"`
}

func NewMemberListResponse(generatedAt time.Time) MemberListResponse {
	return MemberListResponse{
		ResponseMeta: NewResponseMeta(generatedAt),
		State:        IntegrationSyncing,
		Items:        []PoolMember{},
	}
}

type DemandResponse struct {
	ResponseMeta
	State   IntegrationState `json:"state"`
	Summary *DemandSummary   `json:"summary,omitempty"`
	Items   []DemandGroup    `json:"items"`
	Page    PageInfo         `json:"page"`
}

func NewDemandResponse(generatedAt time.Time) DemandResponse {
	return DemandResponse{
		ResponseMeta: NewResponseMeta(generatedAt),
		State:        IntegrationSyncing,
		Items:        []DemandGroup{},
	}
}

type ActivityResponse struct {
	ResponseMeta
	State        IntegrationState  `json:"state"`
	Items        []ActivityEpisode `json:"items"`
	Page         PageInfo          `json:"page"`
	CursorStatus CursorStatus      `json:"cursorStatus"`
	CursorGap    *ObservationGap   `json:"cursorGap,omitempty"`
	Observation  ObservationWindow `json:"observation"`
	// Aggregate summarizes the whole filtered window and is present only on
	// first-page responses; the type filter deliberately does not narrow it.
	Aggregate *ActivityAggregate `json:"aggregate,omitempty"`
}

func NewActivityResponse(generatedAt time.Time) ActivityResponse {
	return ActivityResponse{
		ResponseMeta: NewResponseMeta(generatedAt),
		State:        IntegrationSyncing,
		Items:        []ActivityEpisode{},
		CursorStatus: CursorValid,
		Observation: ObservationWindow{
			EndedAt: generatedAt,
			Sources: []EvidenceSource{},
			Gaps:    []ObservationGap{},
		},
	}
}
