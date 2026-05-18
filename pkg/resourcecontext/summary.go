package resourcecontext

import (
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// SummaryOptions configures the compact per-result enrichment produced by
// BuildSummary. All fields are pre-computed by the caller — this
// package never touches the issue engine, topology builder, or audit
// cache directly. Handlers in internal/* (REST list, MCP list_resources,
// search) walk the per-request topology + issue indexes once and pass
// the per-result digest in here.
type SummaryOptions struct {
	// ManagedBy is the compact owner/GitOps pointer attached to the summary.
	// Callers derive this from topology.Relationships via
	// ManagedByFromOwner; nil leaves the field absent.
	ManagedBy *ManagedByRef

	// IssueCount is the count of internal issue-engine findings scoped to
	// the subject resource. Callers pre-compute a per-namespace index
	// (e.g. via internal/issues.ComposeWithStats) once per request and
	// pass the count in for each result. Zero omits the field.
	IssueCount int

	// Health, when non-empty, overrides the derived health string. The
	// default is computed from resource status via deriveHealth — Pod
	// container readiness, replica-count workloads, and the standard
	// Ready/Available condition on CRDs. Non-trivial kinds derive to "".
	Health string
}

// BuildSummary produces the compact per-result ResourceSummaryContext
// attached to list_resources, /api/ai/resources/{kind} list, and search
// hits.
//
// Tightly bounded — only the triage fields needed to choose a next hop.
// Returns nil when all three fields would be empty so callers can
// `omitempty` the entire object on bare results and keep the wire shape minimal.
func BuildSummary(obj runtime.Object, opts SummaryOptions) *ResourceSummaryContext {
	health := opts.Health
	if health == "" {
		health = deriveHealth(obj)
	}
	if opts.ManagedBy == nil && health == "" && opts.IssueCount == 0 {
		return nil
	}
	return &ResourceSummaryContext{
		ManagedBy:  opts.ManagedBy,
		Health:     health,
		IssueCount: opts.IssueCount,
	}
}

// ManagedByFromOwner assembles a compact ManagedByRef from raw owner
// fields (typically pulled out of topology.Relationships in the handler).
// Returns nil when ownerKind or ownerName is empty so callers don't
// have to guard the assignment.
//
// Source classification:
//   - "argocd" for argoproj.io kinds (Application, ApplicationSet, Rollout)
//   - "flux" for *.fluxcd.io kinds (Kustomization, HelmRelease, GitRepository, …)
//   - "helm" for the native Helm release pseudo-owner (kind "HelmRelease"
//     with no group — emitted by topology's detectManagedByFromMeta to
//     distinguish from Flux's HelmRelease CR in helm.toolkit.fluxcd.io)
//   - "native" for everything else (Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, …)
func ManagedByFromOwner(ownerKind, ownerGroup, ownerNamespace, ownerName string) *ManagedByRef {
	if ownerKind == "" || ownerName == "" {
		return nil
	}
	return &ManagedByRef{
		Kind:      ownerKind,
		Source:    sourceForOwner(ownerKind, ownerGroup),
		Name:      ownerName,
		Namespace: ownerNamespace,
	}
}

func sourceForOwner(ownerKind, group string) string {
	// Native Helm install: topology synthesizes a {Kind:"HelmRelease", Group:""}
	// pseudo-owner from Helm's release-name/namespace annotations. This must
	// be classified BEFORE the group-based GitOps branches so we don't fall
	// through to "native" — Flux's HelmRelease lives at helm.toolkit.fluxcd.io
	// and is handled by the *.fluxcd.io branch below.
	if ownerKind == "HelmRelease" && group == "" {
		return "helm"
	}
	switch group {
	case "argoproj.io":
		return "argocd"
	}
	if strings.HasSuffix(group, ".fluxcd.io") {
		return "flux"
	}
	return "native"
}

// deriveHealth applies a tiny per-kind heuristic to classify a resource
// as "healthy" | "degraded" | "unhealthy". Kinds we don't recognize
// derive to "" and the field is omitted on the wire.
//
// Vocabulary matches the broader status-tone scheme used across the UI
// (k8s-ui StatusTone) so consumers don't need to translate.
func deriveHealth(obj runtime.Object) string {
	if obj == nil {
		return ""
	}
	switch o := obj.(type) {
	case *corev1.Pod:
		return podHealth(o)
	case *appsv1.Deployment:
		// Use Spec.Replicas (desired) not Status.Replicas (current). During
		// scale-down or rolling updates, Status.Replicas can exceed
		// Spec.Replicas while terminating pods drain; comparing ReadyReplicas
		// against Status.Replicas would falsely report "degraded" when all
		// desired replicas are actually ready. Matches StatefulSet semantics.
		desired := int32(1)
		if o.Spec.Replicas != nil {
			desired = *o.Spec.Replicas
		}
		return replicasHealth(o.Status.ReadyReplicas, desired)
	case *appsv1.StatefulSet:
		desired := int32(1)
		if o.Spec.Replicas != nil {
			desired = *o.Spec.Replicas
		}
		return replicasHealth(o.Status.ReadyReplicas, desired)
	case *appsv1.DaemonSet:
		return replicasHealth(o.Status.NumberReady, o.Status.DesiredNumberScheduled)
	case *appsv1.ReplicaSet:
		// Same Spec-vs-Status concern as Deployment above.
		desired := int32(1)
		if o.Spec.Replicas != nil {
			desired = *o.Spec.Replicas
		}
		return replicasHealth(o.Status.ReadyReplicas, desired)
	case *unstructured.Unstructured:
		return unstructuredHealth(o)
	}
	return ""
}

func podHealth(p *corev1.Pod) string {
	switch p.Status.Phase {
	case corev1.PodRunning:
		if len(p.Status.ContainerStatuses) == 0 {
			return "degraded"
		}
		for _, cs := range p.Status.ContainerStatuses {
			if !cs.Ready {
				return "degraded"
			}
		}
		return "healthy"
	case corev1.PodSucceeded:
		return "healthy"
	case corev1.PodFailed:
		return "unhealthy"
	case corev1.PodPending:
		return "degraded"
	}
	return ""
}

func replicasHealth(ready, desired int32) string {
	if desired <= 0 {
		return ""
	}
	if ready >= desired {
		return "healthy"
	}
	if ready <= 0 {
		return "unhealthy"
	}
	return "degraded"
}

// unstructuredHealth derives health for CRDs that follow the standard
// Ready/Available condition pattern. Returns "" for kinds without a
// matching condition so we don't emit a misleading status for resources
// whose status shape we don't understand.
func unstructuredHealth(u *unstructured.Unstructured) string {
	if u == nil {
		return ""
	}
	conditions, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !found || len(conditions) == 0 {
		return ""
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		condType, _ := cond["type"].(string)
		if condType != "Ready" && condType != "Available" {
			continue
		}
		status, _ := cond["status"].(string)
		switch status {
		case "True":
			return "healthy"
		case "False":
			return "unhealthy"
		default:
			return "degraded"
		}
	}
	return ""
}
