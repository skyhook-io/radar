package resourcecontext

import (
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/health"
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

// deriveHealth classifies a resource via the shared health classifier so the AI
// context, the dashboards, the timeline, and topology all agree. Kinds it doesn't
// recognize derive to "" and the field is omitted on the wire.
func deriveHealth(obj runtime.Object) string {
	if obj == nil {
		return ""
	}
	now := time.Now()
	switch o := obj.(type) {
	case *corev1.Pod:
		// PodDisplayLevel (not raw Pod) so an unschedulable / stuck-terminating pod
		// reads degraded in AI/search context too, consistent with topology + timeline.
		return levelToString(health.PodDisplayLevel(o, now))
	case *appsv1.Deployment, *appsv1.StatefulSet, *appsv1.DaemonSet, *appsv1.ReplicaSet:
		return levelToString(health.Workload(o, now).Level)
	case *unstructured.Unstructured:
		return unstructuredHealth(o)
	}
	return ""
}

// levelToString maps a canonical health.Level onto the string vocabulary the AI
// summary / search emit. neutral (intentional/idle — suspended, scaled-to-zero,
// completed) is emitted explicitly so the consumer can distinguish "off on
// purpose" from healthy. unknown still derives to "" (the field is omitted rather
// than asserting a status for an unobservable resource).
func levelToString(l health.Level) string {
	switch l {
	case health.LevelHealthy:
		return "healthy"
	case health.LevelDegraded:
		return "degraded"
	case health.LevelUnhealthy:
		return "unhealthy"
	case health.LevelNeutral:
		return "neutral"
	}
	return ""
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
