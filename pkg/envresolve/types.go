package envresolve

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

type ValueState string

const (
	ValueResolved    ValueState = "resolved"
	ValueMasked      ValueState = "masked"
	ValueUnavailable ValueState = "unavailable"
	ValueMissing     ValueState = "missing"
	ValueDenied      ValueState = "denied"
)

type MissingImpact string

const (
	MissingImpactStartupBlocked MissingImpact = "startupBlocked"
	MissingImpactRestartBlocked MissingImpact = "restartBlocked"
)

type SourceState string

const (
	SourceAvailable   SourceState = "available"
	SourceMissing     SourceState = "missing"
	SourceDenied      SourceState = "denied"
	SourceUnavailable SourceState = "unavailable"
)

type SourceID struct {
	Kind string
	Name string
}

type SourceData struct {
	Kind      string
	Name      string
	State     SourceState
	Optional  bool
	Keys      []string
	Values    map[string]string
	Sensitive bool
	Message   string
}

type NodeData struct {
	State       SourceState
	Allocatable corev1.ResourceList
}

type SourceRef struct {
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
	Key      string `json:"key,omitempty"`
	Variable string `json:"variable,omitempty"`
}

type ChangeEvidence struct {
	Kind      string    `json:"kind"`
	ChangedAt time.Time `json:"changedAt"`
	Message   string    `json:"message,omitempty"`
}

type Row struct {
	Name             string          `json:"name"`
	Value            string          `json:"value,omitempty"`
	State            ValueState      `json:"state"`
	Sensitive        bool            `json:"sensitive,omitempty"`
	Source           SourceRef       `json:"source"`
	Dependencies     []SourceRef     `json:"dependencies,omitempty"`
	ShadowedSources  []SourceRef     `json:"shadowedSources,omitempty"`
	Message          string          `json:"message,omitempty"`
	Optional         bool            `json:"optional,omitempty"`
	MissingImpact    MissingImpact   `json:"missingImpact,omitempty"`
	RuntimeDependent bool            `json:"runtimeDependent,omitempty"`
	CurrentPodValue  bool            `json:"currentPodValue,omitempty"`
	Placeholder      bool            `json:"placeholder,omitempty"`
	Evidence         *ChangeEvidence `json:"evidence,omitempty"`
}

type Container struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	Rows      []Row  `json:"rows"`
	Truncated bool   `json:"truncated,omitempty"`
}

type Coverage struct {
	ObservedSince  *time.Time `json:"observedSince,omitempty"`
	Degraded       bool       `json:"degraded,omitempty"`
	DegradedReason string     `json:"degradedReason,omitempty"`
	Saturated      bool       `json:"saturated,omitempty"`
}

type PodEnvironment struct {
	Containers []Container `json:"containers"`
	Coverage   Coverage    `json:"coverage"`
	Partial    bool        `json:"partial,omitempty"`
	Truncated  bool        `json:"truncated,omitempty"`
}
