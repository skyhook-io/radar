package capacityapi

import (
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/subject"
)

type PoolMode string

const (
	PoolModeDynamic PoolMode = "dynamic"
	PoolModeStatic  PoolMode = "static"
	PoolModeUnknown PoolMode = "unknown"
)

type NodeClassReference struct {
	Group      string `json:"group"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	APIVersion string `json:"apiVersion,omitempty"`
}

type NodeClassObservation struct {
	Reference  NodeClassReference `json:"reference"`
	Resource   *ResourceIdentity  `json:"resource,omitempty"`
	Ready      *bool              `json:"ready,omitempty"`
	Conditions []Condition        `json:"conditions"`
}

func NewNodeClassObservation(reference NodeClassReference) NodeClassObservation {
	return NodeClassObservation{Reference: reference, Conditions: []Condition{}}
}

type Requirement struct {
	Key       string   `json:"key"`
	Operator  string   `json:"operator"`
	Values    []string `json:"values"`
	MinValues *int64   `json:"minValues,omitempty"`
}

type Taint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

type Toleration struct {
	Key               string `json:"key,omitempty"`
	Operator          string `json:"operator,omitempty"`
	Value             string `json:"value,omitempty"`
	Effect            string `json:"effect,omitempty"`
	TolerationSeconds *int64 `json:"tolerationSeconds,omitempty"`
}

type PoolConfiguration struct {
	Weight                 *int64            `json:"weight,omitempty"`
	Replicas               *int64            `json:"replicas,omitempty"`
	Labels                 map[string]string `json:"labels"`
	Requirements           []Requirement     `json:"requirements"`
	Taints                 []Taint           `json:"taints"`
	StartupTaints          []Taint           `json:"startupTaints"`
	ExpireAfter            string            `json:"expireAfter,omitempty"`
	TerminationGracePeriod string            `json:"terminationGracePeriod,omitempty"`
}

func NewPoolConfiguration() PoolConfiguration {
	return PoolConfiguration{
		Labels:        map[string]string{},
		Requirements:  []Requirement{},
		Taints:        []Taint{},
		StartupTaints: []Taint{},
	}
}

type CapacityLedger struct {
	ConfiguredLimit              *QuantityObservation `json:"configuredLimit,omitempty"`
	Provisioned                  *QuantityObservation `json:"provisioned,omitempty"`
	Allocatable                  *QuantityObservation `json:"allocatable,omitempty"`
	ScheduledRequests            *QuantityObservation `json:"scheduledRequests,omitempty"`
	InFlightCapacity             *QuantityObservation `json:"inFlightCapacity,omitempty"`
	ActualUsage                  *UsageObservation    `json:"actualUsage,omitempty"`
	AggregateUnallocatedRequests *QuantityObservation `json:"aggregateUnallocatedRequests,omitempty"`
	LimitHeadroom                *QuantityObservation `json:"limitHeadroom,omitempty"`
	LimitPressure                []LimitPressure      `json:"limitPressure"`
}

type LimitPressure struct {
	Resource    string   `json:"resource"`
	Provisioned string   `json:"provisioned"`
	Limit       string   `json:"limit"`
	Percent     *float64 `json:"percent,omitempty"`
	OverLimit   bool     `json:"overLimit"`
}

func NewCapacityLedger() CapacityLedger {
	return CapacityLedger{LimitPressure: []LimitPressure{}}
}

type DisruptionBudget struct {
	Nodes    string   `json:"nodes"`
	Reasons  []string `json:"reasons"`
	Schedule string   `json:"schedule,omitempty"`
	Duration string   `json:"duration,omitempty"`
}

type DisruptionPolicy struct {
	ConsolidationPolicy string             `json:"consolidationPolicy,omitempty"`
	ConsolidateAfter    string             `json:"consolidateAfter,omitempty"`
	Budgets             []DisruptionBudget `json:"budgets"`
}

func NewDisruptionPolicy() DisruptionPolicy {
	return DisruptionPolicy{
		Budgets: []DisruptionBudget{},
	}
}

type ClaimLifecycleSummary struct {
	Total       int `json:"total"`
	Pending     int `json:"pending"`
	Launched    int `json:"launched"`
	Registered  int `json:"registered"`
	Initialized int `json:"initialized"`
	Ready       int `json:"ready"`
	Failed      int `json:"failed"`
	Terminating int `json:"terminating"`
}

type NodeLifecycleSummary struct {
	Total       int `json:"total"`
	Ready       int `json:"ready"`
	NotReady    int `json:"notReady"`
	Cordoned    int `json:"cordoned"`
	Terminating int `json:"terminating"`
}

type CompositionBucket struct {
	Value     string               `json:"value"`
	Count     int                  `json:"count"`
	Resources *QuantityObservation `json:"resources,omitempty"`
}

type PoolComposition struct {
	CapacityTypes     []CompositionBucket `json:"capacityTypes"`
	CapacityTypesMeta BoundedResultMeta   `json:"capacityTypesMeta"`
	InstanceTypes     []CompositionBucket `json:"instanceTypes"`
	InstanceTypesMeta BoundedResultMeta   `json:"instanceTypesMeta"`
	Zones             []CompositionBucket `json:"zones"`
	ZonesMeta         BoundedResultMeta   `json:"zonesMeta"`
	Architectures     []CompositionBucket `json:"architectures"`
	ArchitecturesMeta BoundedResultMeta   `json:"architecturesMeta"`
	Images            []CompositionBucket `json:"images"`
	ImagesMeta        BoundedResultMeta   `json:"imagesMeta"`
}

func NewPoolComposition() PoolComposition {
	return PoolComposition{
		CapacityTypes: []CompositionBucket{},
		InstanceTypes: []CompositionBucket{},
		Zones:         []CompositionBucket{},
		Architectures: []CompositionBucket{},
		Images:        []CompositionBucket{},
	}
}

type WorkloadAttribution struct {
	Owner    subject.Ref          `json:"owner"`
	PodCount int                  `json:"podCount"`
	Requests *QuantityObservation `json:"requests,omitempty"`
}

type WorkloadSummary struct {
	ScheduledPodCount         int                   `json:"scheduledPodCount"`
	WorkloadCount             int                   `json:"workloadCount"`
	TopScheduled              []WorkloadAttribution `json:"topScheduled"`
	TopScheduledMeta          BoundedResultMeta     `json:"topScheduledMeta"`
	PendingEligibleGroupCount int                   `json:"pendingEligibleGroupCount"`
}

func NewWorkloadSummary() WorkloadSummary {
	return WorkloadSummary{
		TopScheduled: []WorkloadAttribution{},
	}
}

type PostureFact struct {
	Code        string   `json:"code"`
	Summary     string   `json:"summary"`
	Detail      string   `json:"detail,omitempty"`
	SourcePaths []string `json:"sourcePaths"`
}

type PoolObservation struct {
	Resource           ResourceIdentity       `json:"resource"`
	Generation         int64                  `json:"generation"`
	ObservedGeneration *int64                 `json:"observedGeneration,omitempty"`
	CreatedAt          *time.Time             `json:"createdAt,omitempty"`
	UpdatedAt          *time.Time             `json:"updatedAt,omitempty"`
	Mode               PoolMode               `json:"mode"`
	Ready              *bool                  `json:"ready,omitempty"`
	Conditions         []Condition            `json:"conditions"`
	NodeClass          *NodeClassObservation  `json:"nodeClass,omitempty"`
	Configuration      PoolConfiguration      `json:"configuration"`
	Ledger             CapacityLedger         `json:"ledger"`
	Disruption         DisruptionPolicy       `json:"disruption"`
	Claims             *ClaimLifecycleSummary `json:"claims,omitempty"`
	Nodes              *NodeLifecycleSummary  `json:"nodes,omitempty"`
	Composition        *PoolComposition       `json:"composition,omitempty"`
	Workloads          *WorkloadSummary       `json:"workloads,omitempty"`
	Issues             []issuesapi.Issue      `json:"issues"`
	Facts              []PostureFact          `json:"facts"`
	Coverage           CoverageBySource       `json:"coverage"`
}

func NewPoolObservation() PoolObservation {
	return PoolObservation{
		Mode:          PoolModeUnknown,
		Conditions:    []Condition{},
		Configuration: NewPoolConfiguration(),
		Ledger:        NewCapacityLedger(),
		Disruption:    NewDisruptionPolicy(),
		Issues:        []issuesapi.Issue{},
		Facts:         []PostureFact{},
		Coverage:      CoverageBySource{},
	}
}

type PoolSummary struct {
	Resource   ResourceIdentity       `json:"resource"`
	Mode       PoolMode               `json:"mode"`
	Ready      *bool                  `json:"ready,omitempty"`
	NodeClass  *NodeClassReference    `json:"nodeClass,omitempty"`
	Ledger     CapacityLedger         `json:"ledger"`
	Claims     *ClaimLifecycleSummary `json:"claims,omitempty"`
	Nodes      *NodeLifecycleSummary  `json:"nodes,omitempty"`
	IssueCount int                    `json:"issueCount"`
	FactCount  int                    `json:"factCount"`
}

func NewPoolSummary() PoolSummary {
	return PoolSummary{Mode: PoolModeUnknown, Ledger: NewCapacityLedger()}
}

type MemberType string

const (
	MemberNode     MemberType = "node"
	MemberClaim    MemberType = "claim"
	MemberWorkload MemberType = "workload"
)

type ClaimStage string

const (
	ClaimStagePending     ClaimStage = "pending"
	ClaimStageLaunched    ClaimStage = "launched"
	ClaimStageRegistered  ClaimStage = "registered"
	ClaimStageInitialized ClaimStage = "initialized"
	ClaimStageReady       ClaimStage = "ready"
	ClaimStageFailed      ClaimStage = "failed"
	ClaimStageTerminating ClaimStage = "terminating"
	ClaimStageUnknown     ClaimStage = "unknown"
)

type NodeMember struct {
	Ready             *bool                `json:"ready,omitempty"`
	Cordoned          bool                 `json:"cordoned"`
	Conditions        []Condition          `json:"conditions"`
	InstanceType      string               `json:"instanceType,omitempty"`
	CapacityType      string               `json:"capacityType,omitempty"`
	Zone              string               `json:"zone,omitempty"`
	Architecture      string               `json:"architecture,omitempty"`
	Image             string               `json:"image,omitempty"`
	Allocatable       *QuantityObservation `json:"allocatable,omitempty"`
	ScheduledRequests *QuantityObservation `json:"scheduledRequests,omitempty"`
	ActualUsage       *UsageObservation    `json:"actualUsage,omitempty"`
	PodCount          *int                 `json:"podCount,omitempty"`
}

func NewNodeMember() NodeMember {
	return NodeMember{Conditions: []Condition{}}
}

type ClaimMember struct {
	Stage      ClaimStage           `json:"stage"`
	Conditions []Condition          `json:"conditions"`
	NodeName   string               `json:"nodeName,omitempty"`
	Node       *ResourceIdentity    `json:"node,omitempty"`
	Capacity   *QuantityObservation `json:"capacity,omitempty"`
}

func NewClaimMember() ClaimMember {
	return ClaimMember{Stage: ClaimStageUnknown, Conditions: []Condition{}}
}

type WorkloadMember struct {
	Owner     subject.Ref          `json:"owner"`
	PodCount  int                  `json:"podCount"`
	Requests  *QuantityObservation `json:"requests,omitempty"`
	Nodes     []ResourceIdentity   `json:"nodes"`
	NodesMeta BoundedResultMeta    `json:"nodesMeta"`
}

func NewWorkloadMember() WorkloadMember {
	return WorkloadMember{Nodes: []ResourceIdentity{}}
}

type PoolMember struct {
	Type     MemberType       `json:"type"`
	Resource ResourceIdentity `json:"resource"`
	Node     *NodeMember      `json:"node,omitempty"`
	Claim    *ClaimMember     `json:"claim,omitempty"`
	Workload *WorkloadMember  `json:"workload,omitempty"`
}
