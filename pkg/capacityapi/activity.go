package capacityapi

import "time"

type ActivityType string

const (
	ActivityProvision             ActivityType = "provision"
	ActivityLaunchFailure         ActivityType = "launch_failure"
	ActivityRegistrationFailure   ActivityType = "registration_failure"
	ActivityInitializationFailure ActivityType = "initialization_failure"
	ActivityDisruption            ActivityType = "disruption"
	ActivityInterruption          ActivityType = "interruption"
	ActivityTermination           ActivityType = "termination"
	ActivityConfigChange          ActivityType = "config_change"
)

type ActivityState string

const (
	ActivityOpen      ActivityState = "open"
	ActivityCompleted ActivityState = "completed"
	ActivityFailed    ActivityState = "failed"
	ActivityObserved  ActivityState = "observed"
	ActivityBlocked   ActivityState = "blocked"
	// ActivityEnded terminalizes an episode whose outcome is known to be over
	// but whose CAUSE was never recorded — a NodeClaim deleted before it ever
	// reached Ready, with no failing stage in the observed window. It is
	// deliberately distinct from ActivityFailed: the claim is gone either way,
	// but only one of those two states asserts something went wrong.
	ActivityEnded   ActivityState = "ended"
	ActivityUnknown ActivityState = "unknown"
)

type EvidenceSource string

const (
	EvidenceCondition       EvidenceSource = "condition"
	EvidenceKubernetesEvent EvidenceSource = "k8s_event"
	EvidenceResourceChange  EvidenceSource = "resource_change"
)

type EvidenceRelationship string

const (
	EvidenceDirect     EvidenceRelationship = "direct"
	EvidenceStructural EvidenceRelationship = "structural"
	EvidenceCorrelated EvidenceRelationship = "correlated"
)

type ActivityEvidence struct {
	At           time.Time            `json:"at"`
	Source       EvidenceSource       `json:"source"`
	ReasonCode   string               `json:"reasonCode"`
	RawReason    string               `json:"rawReason,omitempty"`
	RawMessage   string               `json:"rawMessage,omitempty"`
	Relationship EvidenceRelationship `json:"relationship"`
	Confidence   Confidence           `json:"confidence"`
	Refs         []ResourceIdentity   `json:"refs"`
}

func NewActivityEvidence() ActivityEvidence {
	return ActivityEvidence{
		Relationship: EvidenceCorrelated,
		Confidence:   ConfidenceLow,
		Refs:         []ResourceIdentity{},
	}
}

type ActivityEpisode struct {
	ID                string             `json:"id"`
	Type              ActivityType       `json:"type"`
	State             ActivityState      `json:"state"`
	Summary           string             `json:"summary"`
	PrimaryReasonCode string             `json:"primaryReasonCode,omitempty"`
	StartedAt         time.Time          `json:"startedAt"`
	EndedAt           *time.Time         `json:"endedAt,omitempty"`
	DurationSeconds   *float64           `json:"durationSeconds,omitempty"`
	Pool              *ResourceIdentity  `json:"pool,omitempty"`
	Claim             *ResourceIdentity  `json:"claim,omitempty"`
	Node              *ResourceIdentity  `json:"node,omitempty"`
	Evidence          []ActivityEvidence `json:"evidence"`
	EvidenceMeta      BoundedResultMeta  `json:"evidenceMeta"`
}

func NewActivityEpisode() ActivityEpisode {
	return ActivityEpisode{State: ActivityUnknown, Evidence: []ActivityEvidence{}}
}

type ActivityTypeCounts struct {
	Total   int                   `json:"total"`
	ByState map[ActivityState]int `json:"byState"`
}

type ActivityAggregate struct {
	Total  int                                 `json:"total"`
	ByType map[ActivityType]ActivityTypeCounts `json:"byType"`
}

func NewActivityAggregate() ActivityAggregate {
	return ActivityAggregate{ByType: map[ActivityType]ActivityTypeCounts{}}
}

type CursorStatus string

const (
	CursorValid        CursorStatus = "valid"
	CursorEpochChanged CursorStatus = "epoch_changed"
	CursorEvicted      CursorStatus = "evicted"
)

type ObservationGap struct {
	DetectedAt     time.Time `json:"detectedAt"`
	Reason         string    `json:"reason"`
	PreviousCursor string    `json:"previousCursor,omitempty"`
	NewEpoch       string    `json:"newEpoch,omitempty"`
}

type Retention struct {
	Mode          string `json:"mode"`
	MaxEvents     *int   `json:"maxEvents,omitempty"`
	MaxAgeSeconds *int64 `json:"maxAgeSeconds,omitempty"`
}

type ObservationWindow struct {
	StartedAt time.Time        `json:"startedAt"`
	EndedAt   time.Time        `json:"endedAt"`
	Sources   []EvidenceSource `json:"sources"`
	Retention Retention        `json:"retention"`
	Gaps      []ObservationGap `json:"gaps"`
}
