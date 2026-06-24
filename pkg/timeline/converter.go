package timeline

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NewInformerEvent creates a TimelineEvent from an informer callback
// createdAt is the resource's metadata.creationTimestamp (when K8s actually created it)
// apiVersion (e.g. "apps/v1", "cluster.x-k8s.io/v1beta1") disambiguates CRD kind
// collisions on navigation; pass "" if unknown (older callers).
func NewInformerEvent(kind, apiVersion, namespace, name, uid string, operation EventType, healthState HealthState, diff *DiffInfo, owner *OwnerInfo, labels map[string]string, createdAt *time.Time) TimelineEvent {
	return TimelineEvent{
		ID:          uuid.New().String(),
		Timestamp:   time.Now(),
		Source:      SourceInformer,
		Kind:        kind,
		APIVersion:  apiVersion,
		Namespace:   namespace,
		Name:        name,
		UID:         uid,
		CreatedAt:   createdAt,
		EventType:   operation,
		HealthState: healthState,
		Diff:        diff,
		Owner:       owner,
		Labels:      labels,
	}
}

// NewK8sEventTimelineEvent creates a TimelineEvent from a corev1.Event
func NewK8sEventTimelineEvent(event *corev1.Event, owner *OwnerInfo) TimelineEvent {
	// Use lastTimestamp or firstTimestamp
	ts := event.LastTimestamp.Time
	if ts.IsZero() {
		ts = event.FirstTimestamp.Time
	}
	if ts.IsZero() {
		ts = event.CreationTimestamp.Time
	}

	evtType := EventTypeNormal
	if event.Type == "Warning" {
		evtType = EventTypeWarning
	}

	return TimelineEvent{
		ID:         string(event.UID),
		Timestamp:  ts,
		Source:     SourceK8sEvent,
		Kind:       event.InvolvedObject.Kind,
		APIVersion: event.InvolvedObject.APIVersion,
		Namespace:  event.Namespace,
		Name:       event.InvolvedObject.Name,
		EventType:  evtType,
		Reason:     event.Reason,
		Message:    event.Message,
		Owner:      owner,
		Count:      event.Count,
	}
}

// NewHistoricalEvent creates a historical TimelineEvent
// The ID is deterministic based on the event content to avoid duplicates on restart
// apiVersion (e.g. "apps/v1", "cluster.x-k8s.io/v1beta1") disambiguates CRD kind
// collisions on navigation; pass "" if unknown.
func NewHistoricalEvent(kind, apiVersion, namespace, name string, ts time.Time, reason, message string, healthState HealthState, owner *OwnerInfo, labels map[string]string) TimelineEvent {
	// Create deterministic ID from event attributes to avoid duplicates
	hashInput := fmt.Sprintf("historical:%s/%s/%s:%d:%s", kind, namespace, name, ts.UnixNano(), reason)
	hash := sha256.Sum256([]byte(hashInput))
	id := fmt.Sprintf("hist-%x", hash[:8]) // Use first 8 bytes for shorter ID

	return TimelineEvent{
		ID:          id,
		Timestamp:   ts,
		Source:      SourceHistorical,
		Kind:        kind,
		APIVersion:  apiVersion,
		Namespace:   namespace,
		Name:        name,
		EventType:   EventTypeUpdate, // Historical events are shown as updates
		Reason:      reason,
		Message:     message,
		HealthState: healthState,
		Owner:       owner,
		Labels:      labels,
	}
}

// ExtractOwner gets the controller owner reference from an object
// For K8s Events, it extracts the involvedObject instead
func ExtractOwner(obj any) *OwnerInfo {
	// Special case: K8s Events use involvedObject, not ownerReferences
	if event, ok := obj.(*corev1.Event); ok {
		if event.InvolvedObject.Kind != "" && event.InvolvedObject.Name != "" {
			return &OwnerInfo{
				Kind: event.InvolvedObject.Kind,
				Name: event.InvolvedObject.Name,
			}
		}
		return nil
	}

	meta, ok := obj.(metav1.Object)
	if !ok {
		return nil
	}

	refs := meta.GetOwnerReferences()

	// First, try to find a controller owner (most accurate)
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return &OwnerInfo{
				Kind: ref.Kind,
				Name: ref.Name,
			}
		}
	}

	// Fallback: use first owner reference if no controller is marked
	if len(refs) > 0 {
		return &OwnerInfo{
			Kind: refs[0].Kind,
			Name: refs[0].Name,
		}
	}

	return nil
}

// ExtractLabels extracts labels useful for grouping from an object
func ExtractLabels(obj any) map[string]string {
	meta, ok := obj.(metav1.Object)
	if !ok {
		return nil
	}

	allLabels := meta.GetLabels()
	if len(allLabels) == 0 {
		return nil
	}

	// Only keep labels that are useful for grouping
	relevant := make(map[string]string)
	interestingLabels := []string{
		"app.kubernetes.io/name",
		"app.kubernetes.io/instance",
		"app.kubernetes.io/component",
		"app",
		"name",
		"component",
	}

	for _, key := range interestingLabels {
		if v, ok := allLabels[key]; ok && v != "" {
			relevant[key] = v
		}
	}

	if len(relevant) == 0 {
		return nil
	}
	return relevant
}

// Resource health classification for timeline events lives with the canonical
// classifiers in internal/k8s (classifyTimelineHealth → ClassifyPodHealth), not
// here: the timeline package can't reach that logic across the module boundary,
// so the caller computes health and the event just stores it. A duplicate copy
// here previously drifted and misclassified completing Job pods as degraded.

// OperationToEventType converts an operation string to EventType
func OperationToEventType(op string) EventType {
	switch op {
	case "add":
		return EventTypeAdd
	case "update":
		return EventTypeUpdate
	case "delete":
		return EventTypeDelete
	default:
		return EventType(op)
	}
}

// EventTypeToOperation converts EventType to operation string
func EventTypeToOperation(et EventType) string {
	switch et {
	case EventTypeAdd:
		return "add"
	case EventTypeUpdate:
		return "update"
	case EventTypeDelete:
		return "delete"
	default:
		return string(et)
	}
}

// HealthStateToString converts HealthState to string
func HealthStateToString(hs HealthState) string {
	return string(hs)
}

// StringToHealthState converts string to HealthState
func StringToHealthState(s string) HealthState {
	switch s {
	case "healthy":
		return HealthHealthy
	case "degraded":
		return HealthDegraded
	case "unhealthy":
		return HealthUnhealthy
	default:
		return HealthUnknown
	}
}

// ToLegacyDiffInfo converts timeline.DiffInfo to a format compatible with the legacy API
// This is for backwards compatibility during migration
func ToLegacyDiffInfo(d *DiffInfo) *DiffInfo {
	return d // Types are identical in structure
}
