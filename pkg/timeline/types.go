// Package timeline provides a unified event storage and query system for the Explorer.
// It normalizes events from multiple sources (informer callbacks, K8s Events, historical
// reconstruction) into a single TimelineEvent type with pluggable storage backends.
package timeline

import (
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// Type aliases — canonical definitions live in pkg/k8score.
type OwnerInfo = k8score.OwnerInfo

// OwnerEvidence says how a row's Owner was determined. Only an observed owner
// describes the row's own moment; the others describe the owner as of when the
// row was recorded.
type OwnerEvidence string

const (
	// OwnerObserved: read from the object itself when the row was recorded (an
	// informer change). A nil owner means the object had none.
	OwnerObserved OwnerEvidence = "observed"
	// OwnerReconstructed: read from the object's current state and applied to a
	// historical row dated earlier; not proof of ownership at that time.
	OwnerReconstructed OwnerEvidence = "reconstructed"
	// OwnerEnriched: a K8s Event row whose subject was found (the live object or
	// its UID-keyed tombstone). A nil owner means the subject had none.
	OwnerEnriched OwnerEvidence = "enriched"
	// OwnerMissed: a K8s Event row whose subject lookup found nothing; the owner
	// is unknown.
	OwnerMissed OwnerEvidence = "missed"
	// OwnerUnidentified: a K8s Event row whose subject carries no UID, so no
	// lookup can identify it and no previously stored owner stands.
	OwnerUnidentified OwnerEvidence = "unidentified"
)

// OwnerReplacesOnUpsert reports whether a re-ingested K8s Event row's owner
// tuple (owner and evidence) replaces the stored one. A lookup that found the
// subject, or proved it can't be identified, is the fresher truth; a miss keeps
// what the row already knew. A row without evidence replaces the stored owner
// only when it carries one. The SQL stores encode the same rule.
func OwnerReplacesOnUpsert(incoming TimelineEvent) bool {
	switch incoming.OwnerEvidence {
	case OwnerEnriched, OwnerUnidentified:
		return true
	case "":
		return incoming.Owner != nil && incoming.Owner.Kind != ""
	}
	return false
}

type DiffInfo = k8score.DiffInfo
type FieldChange = k8score.FieldChange

// EventSource identifies where an event originated
type EventSource string

const (
	// SourceInformer means the event came from a SharedInformer watch callback
	SourceInformer EventSource = "informer"
	// SourceK8sEvent means the event came from a corev1.Event resource
	SourceK8sEvent EventSource = "k8s_event"
	// SourceHistorical means the event was reconstructed from resource metadata/status
	SourceHistorical EventSource = "historical"
)

// EventType categorizes what kind of event this is
type EventType string

const (
	// EventTypeAdd is a resource creation
	EventTypeAdd EventType = "add"
	// EventTypeUpdate is a resource modification
	EventTypeUpdate EventType = "update"
	// EventTypeDelete is a resource deletion
	EventTypeDelete EventType = "delete"
	// EventTypeNormal is a Normal K8s event
	EventTypeNormal EventType = "Normal"
	// EventTypeWarning is a Warning K8s event
	EventTypeWarning EventType = "Warning"
)

// ReasonRecreated marks an add event that replaced a just-deleted resource of
// the same kind/namespace/name (different UID). Such events carry a Diff
// against the predecessor object — the delete+add pair was a recreate, and
// the diff shows what changed across it.
const ReasonRecreated = "recreated"

// HealthState represents the health of a resource
type HealthState string

const (
	HealthHealthy   HealthState = "healthy"
	HealthDegraded  HealthState = "degraded"
	HealthUnhealthy HealthState = "unhealthy"
	// HealthNeutral = intentional/idle (suspended, scaled-to-0, completed) —
	// renders sky in the timeline, distinct from HealthUnknown (gray). Aggregates
	// as most-benign via health.WorseOf (ties healthy, healthy-preferred).
	HealthNeutral HealthState = "neutral"
	HealthUnknown HealthState = "unknown"
)

// GroupingMode determines how events are grouped in the timeline
type GroupingMode string

const (
	// GroupByNone returns a flat list of events
	GroupByNone GroupingMode = "none"
	// GroupByOwner groups events by K8s owner references
	GroupByOwner GroupingMode = "owner"
	// GroupByApp groups events by app.kubernetes.io/name or app label
	GroupByApp GroupingMode = "app"
	// GroupByNamespace groups events by namespace
	GroupByNamespace GroupingMode = "namespace"
)

// TimelineEvent is the unified event type stored in the timeline.
// It normalizes events from all sources (informer, K8s Event, historical)
// into a single structure that can be queried and grouped efficiently.
type TimelineEvent struct {
	// Core identity
	ID        string      `json:"id"`
	Timestamp time.Time   `json:"timestamp"`
	Source    EventSource `json:"source"`
	// Seq is the store-assigned arrival number: monotonic per store instance,
	// re-assigned when a K8s Event upserts (a count bump is a new arrival).
	// Delta reads cursor on it — arrival order, not event time — so a
	// late-arriving event with an older timestamp can't slip behind a client's
	// cursor. 0 means the store didn't assign one.
	Seq int64 `json:"seq,omitempty"`
	// ClusterContext is the kubeconfig context the event was observed on
	// ("in-cluster" when no context name exists). Persistent stores outlive
	// context switches, so reads MUST scope to the active context or events
	// from a previously-connected cluster leak into answers about this one.
	ClusterContext string `json:"clusterContext,omitempty"`

	// Resource identity
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion,omitempty"` // e.g. "apps/v1", "cluster.x-k8s.io/v1beta1" — disambiguates CRD kind collisions on navigation
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid,omitempty"`

	// Resource metadata - when the resource was actually created in K8s
	// This is different from Timestamp which is when we observed the event
	CreatedAt *time.Time `json:"createdAt,omitempty"`

	// Event details
	EventType EventType `json:"eventType"` // add, update, delete, Normal, Warning
	Reason    string    `json:"reason,omitempty"`
	Message   string    `json:"message,omitempty"`

	// Rich context (computed at write time)
	Diff        *DiffInfo   `json:"diff,omitempty"`
	HealthState HealthState `json:"healthState,omitempty"`
	Owner       *OwnerInfo  `json:"owner,omitempty"`
	// OwnerEvidence says how Owner — including its absence — was determined.
	OwnerEvidence OwnerEvidence     `json:"ownerEvidence,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"` // For app-label grouping

	// K8s Event specific
	Count int32 `json:"count,omitempty"`

	// Correlation (for linking related events, e.g., rollout)
	CorrelationID string `json:"correlationId,omitempty"`
}

// OwnerInfo, DiffInfo, FieldChange are type aliases defined above — see pkg/k8score.

// IsManaged returns true if this resource is managed by another (RS, Pod, Event)
func (e *TimelineEvent) IsManaged() bool {
	return e.Owner != nil || e.Kind == "ReplicaSet" || e.Kind == "Pod" || e.Kind == "Event"
}

// IsToplevelWorkload returns true if this is a top-level workload (representative in timeline)
func (e *TimelineEvent) IsToplevelWorkload() bool {
	switch e.Kind {
	case "Deployment", "Rollout", "DaemonSet", "StatefulSet",
		"Service", "Job", "CronJob",
		"Workflow", "CronWorkflow": // Argo Workflows
		return true
	}
	return false
}

// GetAppLabel returns the app label value for grouping (app.kubernetes.io/name or app)
func (e *TimelineEvent) GetAppLabel() string {
	if e.Labels == nil {
		return ""
	}
	if v, ok := e.Labels["app.kubernetes.io/name"]; ok && v != "" {
		return v
	}
	if v, ok := e.Labels["app"]; ok && v != "" {
		return v
	}
	return ""
}

// EventGroup represents a group of related events in the timeline
type EventGroup struct {
	ID        string          `json:"id"` // e.g., "Deployment/default/nginx"
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Events    []TimelineEvent `json:"events"`
	Children  []EventGroup    `json:"children,omitempty"`

	// Aggregated info
	HealthState HealthState `json:"healthState,omitempty"` // Worst health of all events
	EventCount  int         `json:"eventCount"`            // Total events including children
}

// TimelineResponse is the response from the timeline API with grouping
type TimelineResponse struct {
	Groups    []EventGroup    `json:"groups"`
	Ungrouped []TimelineEvent `json:"ungrouped,omitempty"` // Events that don't fit any group
	Meta      TimelineMeta    `json:"meta"`
}

// TimelineMeta contains metadata about the timeline query result
type TimelineMeta struct {
	TotalEvents int   `json:"totalEvents"`
	GroupCount  int   `json:"groupCount"`
	QueryTimeMs int64 `json:"queryTimeMs"`
	HasMore     bool  `json:"hasMore"` // For pagination
}

// FilterPreset defines a named filter configuration
type FilterPreset struct {
	Name                string      `json:"name"`
	ExcludeKinds        []string    `json:"excludeKinds,omitempty"`
	IncludeKinds        []string    `json:"includeKinds,omitempty"`
	ExcludeNamePatterns []string    `json:"excludeNamePatterns,omitempty"`
	ExcludeOperations   []EventType `json:"excludeOperations,omitempty"`
	IncludeEventTypes   []EventType `json:"includeEventTypes,omitempty"`
	IncludeManaged      bool        `json:"includeManaged"`
}

// DefaultFilterPresets returns the built-in filter presets
func DefaultFilterPresets() map[string]FilterPreset {
	return map[string]FilterPreset{
		"default": {
			Name:         "default",
			ExcludeKinds: []string{"Lease", "Endpoints", "EndpointSlice"},
			ExcludeNamePatterns: []string{
				"-lock$", "-lease$", "-leader-election$", "-heartbeat$",
				"cluster-kubestore", "cluster-autoscaler-status",
				"datadog-token", "datadog-operator-lock", "datadog-leader-election",
				"kube-root-ca.certs",
			},
			IncludeManaged: false,
		},
		"all": {
			Name:           "all",
			IncludeManaged: true,
		},
		"warnings-only": {
			Name:              "warnings-only",
			IncludeEventTypes: []EventType{EventTypeWarning},
			IncludeManaged:    true,
		},
		"workloads": {
			Name: "workloads",
			IncludeKinds: []string{
				"Deployment", "Rollout", "DaemonSet", "StatefulSet", "ReplicaSet",
				"Job", "CronJob", "Pod",
			},
			IncludeManaged: true,
		},
	}
}
