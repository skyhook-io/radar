package capacityapi

import (
	"time"

	"github.com/skyhook-io/radar/pkg/subject"
)

type ResourceIdentity struct {
	Ref        subject.Ref `json:"ref"`
	APIVersion string      `json:"apiVersion,omitempty"`
	UID        string      `json:"uid,omitempty"`
}

type Certainty string

const (
	CertaintyExact      Certainty = "exact"
	CertaintyLowerBound Certainty = "lower_bound"
	CertaintyUpperBound Certainty = "upper_bound"
	CertaintyUnknown    Certainty = "unknown"
)

type Granularity string

const (
	GranularityAggregate             Granularity = "aggregate"
	GranularityAggregateNotBinpacked Granularity = "aggregate_not_binpacked"
	GranularityPerNode               Granularity = "per_node"
)

type ResourceVector map[string]string

type QuantityObservation struct {
	Resources   ResourceVector `json:"resources"`
	Certainty   Certainty      `json:"certainty"`
	Sources     []string       `json:"sources"`
	AsOf        time.Time      `json:"asOf"`
	Granularity Granularity    `json:"granularity"`
}

func NewQuantityObservation(asOf time.Time) QuantityObservation {
	return QuantityObservation{
		Resources:   ResourceVector{},
		Certainty:   CertaintyUnknown,
		Sources:     []string{},
		AsOf:        asOf,
		Granularity: GranularityAggregate,
	}
}

type UsageObservation struct {
	Quantity           QuantityObservation   `json:"quantity"`
	CoveredNodes       int                   `json:"coveredNodes"`
	TotalNodes         int                   `json:"totalNodes"`
	CoveredAllocatable QuantityObservation   `json:"coveredAllocatable"`
	Utilization        []ResourceUtilization `json:"utilization"`
}

type ResourceUtilization struct {
	Resource    string  `json:"resource"`
	Usage       string  `json:"usage"`
	Allocatable string  `json:"allocatable"`
	Percent     float64 `json:"percent"`
}

func NewUsageObservation(asOf time.Time) UsageObservation {
	return UsageObservation{
		Quantity:           NewQuantityObservation(asOf),
		CoveredAllocatable: NewQuantityObservation(asOf),
		Utilization:        []ResourceUtilization{},
	}
}

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

type ConditionStatus string

const (
	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"
)

type Condition struct {
	Type               string          `json:"type"`
	Status             ConditionStatus `json:"status"`
	Reason             string          `json:"reason,omitempty"`
	Message            string          `json:"message,omitempty"`
	ObservedGeneration *int64          `json:"observedGeneration,omitempty"`
	LastTransitionTime *time.Time      `json:"lastTransitionTime,omitempty"`
}

type BoundedResultMeta struct {
	Total     int  `json:"total"`
	Returned  int  `json:"returned"`
	Truncated bool `json:"truncated"`
}

type PageInfo struct {
	HasMore    bool   `json:"hasMore"`
	NextCursor string `json:"nextCursor,omitempty"`
}
