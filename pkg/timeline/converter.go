package timeline

import (
	"crypto/sha256"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// informerEventID derives a deterministic id from the resource's observable
// state (gvk + namespace + name + uid + resourceVersion), not the operation:
// a relist re-emits the identical id whether the arrival is labeled add or
// update, so replays and failovers de-dupe instead of duplicating. A delete is
// a distinct state and gets its own id via the delete marker. The uid alone
// makes the id cluster-safe (K8s uids are globally unique), so cluster context
// is deliberately left out of the hash.
func informerEventID(apiVersion, kind, namespace, name, uid, resourceVersion string, operation EventType) string {
	hashInput := apiVersion + "|" + kind + "|" + namespace + "|" + name + "|" + uid + "|" + resourceVersion
	if operation == EventTypeDelete {
		hashInput += "|delete"
	}
	hash := sha256.Sum256([]byte(hashInput))
	return fmt.Sprintf("ev-%x", hash[:16])
}

// NewInformerEvent creates a TimelineEvent from an informer callback
// createdAt is the resource's metadata.creationTimestamp (when K8s actually created it)
// apiVersion (e.g. "apps/v1", "cluster.x-k8s.io/v1beta1") disambiguates CRD kind
// collisions on navigation; pass "" if unknown (older callers).
// resourceVersion pins the event id to the resource state; pass "" only when
// truly unavailable — the stores dedup informer ids keep-first, so with a
// constant "" every later update of the resource maps to the same id and is
// DROPPED until the row ages out, not merely collapsed.
func NewInformerEvent(kind, apiVersion, namespace, name, uid, resourceVersion string, operation EventType, healthState HealthState, diff *DiffInfo, owner *OwnerInfo, labels map[string]string, createdAt *time.Time) TimelineEvent {
	return TimelineEvent{
		ID:          informerEventID(apiVersion, kind, namespace, name, uid, resourceVersion, operation),
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

// NewK8sEventTimelineEvent creates a TimelineEvent from a corev1.Event.
// The id is the Event uid — one logical row per Event, deliberately NOT one
// row per count/message revision. The count/lastTimestamp/message mutate in
// place on the same uid; both local stores upsert a same-uid bump — MemoryStore
// in appendLocked, SQLiteStore via ON CONFLICT(id) DO UPDATE in AppendBatch — so
// the row reflects the latest revision instead of dropping the bump, while an
// identical informer/historical relist dupe stays collapsed to the first row.
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
// clusterContext is folded into the id: historical ids carry no uid, so two
// clusters with a same-named resource created at the same instant would collide
// in one persistent store without it.
func NewHistoricalEvent(clusterContext, kind, apiVersion, namespace, name string, ts time.Time, reason, message string, healthState HealthState, owner *OwnerInfo, labels map[string]string) TimelineEvent {
	// Create deterministic ID from event attributes to avoid duplicates
	hashInput := fmt.Sprintf("historical:%s:%s/%s/%s:%d:%s", clusterContext, kind, namespace, name, ts.UnixNano(), reason)
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

	// Only keep labels that are useful for grouping. The GitOps identity
	// labels must ride along or the app-membership matchKeys the server ships
	// for Argo/Flux-primary apps (applications.go collectExactMatchKeys) have
	// nothing to match against on a deleted member's events. Native Helm
	// identity is an annotation (meta.helm.sh/release-name), which events
	// deliberately never carry.
	relevant := make(map[string]string)
	interestingLabels := []string{
		"app.kubernetes.io/name",
		"app.kubernetes.io/instance",
		"app.kubernetes.io/part-of",
		"app.kubernetes.io/component",
		"app",
		"name",
		"component",
		"argocd.argoproj.io/instance",
		"helm.toolkit.fluxcd.io/name",
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
