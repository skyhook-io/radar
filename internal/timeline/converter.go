package timeline

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NewInformerEvent creates a TimelineEvent from an informer callback
// createdAt is the resource's metadata.creationTimestamp (when K8s actually created it)
func NewInformerEvent(kind, namespace, name, uid string, operation EventType, healthState HealthState, diff *DiffInfo, owner *OwnerInfo, labels map[string]string, createdAt *time.Time) TimelineEvent {
	return TimelineEvent{
		ID:          uuid.New().String(),
		Timestamp:   time.Now(),
		Source:      SourceInformer,
		Kind:        kind,
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
		ID:        string(event.UID),
		Timestamp: ts,
		Source:    SourceK8sEvent,
		Kind:      event.InvolvedObject.Kind,
		Namespace: event.Namespace,
		Name:      event.InvolvedObject.Name,
		EventType: evtType,
		Reason:    event.Reason,
		Message:   event.Message,
		Owner:     owner,
		Count:     event.Count,
	}
}

// NewHistoricalEvent creates a historical TimelineEvent
// The ID is deterministic based on the event content to avoid duplicates on restart
func NewHistoricalEvent(kind, namespace, name string, ts time.Time, reason, message string, healthState HealthState, owner *OwnerInfo, labels map[string]string) TimelineEvent {
	// Create deterministic ID from event attributes to avoid duplicates
	hashInput := fmt.Sprintf("historical:%s/%s/%s:%d:%s", kind, namespace, name, ts.UnixNano(), reason)
	hash := sha256.Sum256([]byte(hashInput))
	id := fmt.Sprintf("hist-%x", hash[:8]) // Use first 8 bytes for shorter ID

	return TimelineEvent{
		ID:          id,
		Timestamp:   ts,
		Source:      SourceHistorical,
		Kind:        kind,
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

// DetermineHealthState determines health state from an object
func DetermineHealthState(kind string, obj any) HealthState {
	switch kind {
	case "Pod":
		if pod, ok := obj.(*corev1.Pod); ok {
			switch pod.Status.Phase {
			case corev1.PodRunning:
				for _, cs := range pod.Status.ContainerStatuses {
					if !cs.Ready {
						return HealthDegraded
					}
				}
				return HealthHealthy
			case corev1.PodSucceeded:
				return HealthHealthy
			case corev1.PodFailed:
				return HealthUnhealthy
			case corev1.PodPending:
				return HealthDegraded
			}
		}
	case "Deployment":
		if dep, ok := obj.(*appsv1.Deployment); ok {
			desired := int32(1)
			if dep.Spec.Replicas != nil {
				desired = *dep.Spec.Replicas
			}
			if dep.Status.ReadyReplicas == desired && dep.Status.AvailableReplicas == desired {
				return HealthHealthy
			}
			if dep.Status.ReadyReplicas > 0 {
				return HealthDegraded
			}
			return HealthUnhealthy
		}
	case "ReplicaSet":
		if rs, ok := obj.(*appsv1.ReplicaSet); ok {
			desired := int32(1)
			if rs.Spec.Replicas != nil {
				desired = *rs.Spec.Replicas
			}
			if rs.Status.ReadyReplicas == desired {
				return HealthHealthy
			}
			if rs.Status.ReadyReplicas > 0 {
				return HealthDegraded
			}
			return HealthUnhealthy
		}
	case "DaemonSet":
		if ds, ok := obj.(*appsv1.DaemonSet); ok {
			if ds.Status.NumberReady == ds.Status.DesiredNumberScheduled && ds.Status.DesiredNumberScheduled > 0 {
				return HealthHealthy
			}
			if ds.Status.NumberReady > 0 {
				return HealthDegraded
			}
			return HealthUnhealthy
		}
	case "StatefulSet":
		if sts, ok := obj.(*appsv1.StatefulSet); ok {
			desired := int32(1)
			if sts.Spec.Replicas != nil {
				desired = *sts.Spec.Replicas
			}
			if sts.Status.ReadyReplicas == desired {
				return HealthHealthy
			}
			if sts.Status.ReadyReplicas > 0 {
				return HealthDegraded
			}
			return HealthUnhealthy
		}
	}
	return HealthUnknown
}

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
