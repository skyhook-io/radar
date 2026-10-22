package server

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Typed-path counterpart of the CRD summary profiles in resource_summary.go:
// ?include=summary for the two guarded typed kinds whose full objects dominate
// wire size on large clusters. A summarized pod serializes 5–8x smaller than
// the full object — nearly all of the difference is subtrees the resources
// table never reads — which is why the 25k-row guard existed at all.
//
// Rows are KEEP-lists rebuilt from scratch, not drop-lists: only the fields
// the k8s-ui list flow reads (columns, sort, filters, problem badges, custom
// label/annotation columns, owner deep-links) survive, so new upstream pod
// fields can never silently grow the payload back. The field set comes from a
// full audit of the table flow; resource_summary_typed_test.go pins both
// directions — every kept path and the heavyweight drops. The detail drawer
// is unaffected: row clicks always refetch the full object.
//
// The row shapes are dedicated structs rather than zeroed corev1 copies
// because the corev1 types serialize their absences: non-omitempty fields
// emit `"image":""`, `"lastProbeTime":null` and friends on every row, which
// at tens of thousands of rows is a third of the payload. JSON names mirror
// the K8s wire format exactly — the frontend accessors must not know the
// difference.
//
// Rows share slices/maps with the informer-cache objects (labels, conditions,
// owner references). That is safe under the same discipline as serving cache
// objects directly, which the raw path has always done: responses are
// marshaled, never mutated.

func summarizeTypedList(kind string, result any) any {
	switch kind {
	case "pods":
		return mapTypedRows(result, summarizePodRow)
	case "replicasets":
		return mapTypedRows(result, summarizeReplicaSetRow)
	}
	return result
}

// mapTypedRows handles the two shapes the typed switch produces: a lister's
// []*T (single-namespace or cluster-wide) and listPerNs's merged []any.
// Unexpected element types pass through untouched (fail open, same posture
// as applySummaryStrip).
func mapTypedRows[T any](result any, row func(*T) any) any {
	switch items := result.(type) {
	case []*T:
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, row(item))
		}
		return out
	case []any:
		out := make([]any, 0, len(items))
		for _, item := range items {
			if t, ok := item.(*T); ok {
				out = append(out, row(t))
			} else {
				out = append(out, item)
			}
		}
		return out
	}
	return result
}

// rowMeta keeps identity, lifecycle, the full label/annotation maps
// (user-defined custom columns read arbitrary keys), ownerReferences (the
// RS Owner column and the workload→pods deep-link filter) and generation —
// the convergence-grace check compares it to status.observedGeneration, and
// a row without it would grade controller lag as an outage.
type rowMeta struct {
	Name              string                  `json:"name,omitempty"`
	Namespace         string                  `json:"namespace,omitempty"`
	UID               types.UID               `json:"uid,omitempty"`
	Generation        int64                   `json:"generation,omitempty"`
	CreationTimestamp *metav1.Time            `json:"creationTimestamp,omitempty"`
	DeletionTimestamp *metav1.Time            `json:"deletionTimestamp,omitempty"`
	Labels            map[string]string       `json:"labels,omitempty"`
	Annotations       map[string]string       `json:"annotations,omitempty"`
	OwnerReferences   []metav1.OwnerReference `json:"ownerReferences,omitempty"`
}

func summaryRowMeta(m *metav1.ObjectMeta) rowMeta {
	out := rowMeta{
		Name:              m.Name,
		Namespace:         m.Namespace,
		UID:               m.UID,
		Generation:        m.Generation,
		DeletionTimestamp: m.DeletionTimestamp,
		Labels:            m.Labels,
		Annotations:       m.Annotations,
		OwnerReferences:   m.OwnerReferences,
	}
	if !m.CreationTimestamp.IsZero() {
		out.CreationTimestamp = &m.CreationTimestamp
	}
	return out
}

type podRow struct {
	Metadata rowMeta      `json:"metadata"`
	Spec     podRowSpec   `json:"spec"`
	Status   podRowStatus `json:"status"`
}

type podRowSpec struct {
	NodeName       string            `json:"nodeName,omitempty"`
	Containers     []podRowContainer `json:"containers,omitempty"`
	InitContainers []podRowContainer `json:"initContainers,omitempty"`
	// Pod-level budget (PodLevelResources): the CPU/Mem column checks which
	// requests/limits it sets.
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

type podRowContainer struct {
	Name string `json:"name"`
	// RestartPolicy is the native-sidecar marker — dropping it would
	// silently mis-badge sidecar crash loops in podCrashHistoryLevel.
	RestartPolicy *corev1.ContainerRestartPolicy `json:"restartPolicy,omitempty"`
	// Resources kept whole (not just the GPU keys the column reads today):
	// cpu/memory cost little and keep any future non-metrics fallback honest.
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

type podRowStatus struct {
	Phase                 corev1.PodPhase         `json:"phase,omitempty"`
	Reason                string                  `json:"reason,omitempty"`
	Message               string                  `json:"message,omitempty"`
	PodIP                 string                  `json:"podIP,omitempty"`
	PodIPs                []corev1.PodIP          `json:"podIPs,omitempty"`
	Conditions            []podRowCondition       `json:"conditions,omitempty"`
	ContainerStatuses     []podRowContainerStatus `json:"containerStatuses,omitempty"`
	InitContainerStatuses []podRowContainerStatus `json:"initContainerStatuses,omitempty"`
}

type podRowCondition struct {
	Type    corev1.PodConditionType `json:"type"`
	Status  corev1.ConditionStatus  `json:"status"`
	Reason  string                  `json:"reason,omitempty"`
	Message string                  `json:"message,omitempty"`
}

// podRowContainerStatus keeps the crash-forensics fields (state, lastState,
// restartCount, ready) and sheds the identifier strings and per-container
// resource echoes the table never reads.
type podRowContainerStatus struct {
	Name         string                 `json:"name"`
	State        *corev1.ContainerState `json:"state,omitempty"`
	LastState    *corev1.ContainerState `json:"lastState,omitempty"`
	Ready        bool                   `json:"ready"`
	RestartCount int32                  `json:"restartCount"`
}

func summarizePodRow(p *corev1.Pod) any {
	return &podRow{
		Metadata: summaryRowMeta(&p.ObjectMeta),
		Spec: podRowSpec{
			NodeName:       p.Spec.NodeName,
			Containers:     summaryContainers(p.Spec.Containers, false),
			InitContainers: summaryContainers(p.Spec.InitContainers, true),
			Resources:      p.Spec.Resources,
		},
		Status: podRowStatus{
			Phase:                 p.Status.Phase,
			Reason:                p.Status.Reason,
			Message:               p.Status.Message,
			PodIP:                 p.Status.PodIP,
			PodIPs:                p.Status.PodIPs,
			Conditions:            summaryPodConditions(p.Status.Conditions),
			ContainerStatuses:     summaryContainerStatuses(p.Status.ContainerStatuses),
			InitContainerStatuses: summaryContainerStatuses(p.Status.InitContainerStatuses),
		},
	}
}

func summaryContainers(containers []corev1.Container, withRestartPolicy bool) []podRowContainer {
	if containers == nil {
		return nil
	}
	out := make([]podRowContainer, 0, len(containers))
	for i := range containers {
		c := &containers[i]
		row := podRowContainer{Name: c.Name}
		if withRestartPolicy {
			row.RestartPolicy = c.RestartPolicy
		}
		if len(c.Resources.Requests) > 0 || len(c.Resources.Limits) > 0 {
			row.Resources = &c.Resources
		}
		out = append(out, row)
	}
	return out
}

func summaryPodConditions(conditions []corev1.PodCondition) []podRowCondition {
	if conditions == nil {
		return nil
	}
	out := make([]podRowCondition, 0, len(conditions))
	for _, c := range conditions {
		out = append(out, podRowCondition{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message})
	}
	return out
}

func summaryContainerStatuses(statuses []corev1.ContainerStatus) []podRowContainerStatus {
	if statuses == nil {
		return nil
	}
	out := make([]podRowContainerStatus, 0, len(statuses))
	for i := range statuses {
		cs := &statuses[i]
		row := podRowContainerStatus{
			Name:         cs.Name,
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
		}
		if cs.State != (corev1.ContainerState{}) {
			row.State = &cs.State
		}
		if cs.LastTerminationState != (corev1.ContainerState{}) {
			row.LastState = &cs.LastTerminationState
		}
		out = append(out, row)
	}
	return out
}

type replicaSetRow struct {
	Metadata rowMeta             `json:"metadata"`
	Spec     replicaSetRowSpec   `json:"spec"`
	Status   replicaSetRowStatus `json:"status"`
}

type replicaSetRowSpec struct {
	Replicas *int32 `json:"replicas,omitempty"`
}

type replicaSetRowStatus struct {
	Replicas           int32 `json:"replicas"`
	ReadyReplicas      int32 `json:"readyReplicas,omitempty"`
	AvailableReplicas  int32 `json:"availableReplicas,omitempty"`
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// summarizeReplicaSetRow drops spec.template — the embedded PodSpec is nearly
// the whole object — plus selector and conditions. The table renders only
// name/namespace/ready/owner/status/age from the counters and metadata,
// plus observedGeneration for the convergence-grace check.
func summarizeReplicaSetRow(rs *appsv1.ReplicaSet) any {
	return &replicaSetRow{
		Metadata: summaryRowMeta(&rs.ObjectMeta),
		Spec:     replicaSetRowSpec{Replicas: rs.Spec.Replicas},
		Status: replicaSetRowStatus{
			Replicas:           rs.Status.Replicas,
			ReadyReplicas:      rs.Status.ReadyReplicas,
			AvailableReplicas:  rs.Status.AvailableReplicas,
			ObservedGeneration: rs.Status.ObservedGeneration,
		},
	}
}
