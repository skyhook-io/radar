// Per-request helpers that compute the compact SummaryContext attached
// to /api/ai/resources/{kind} list rows and /api/search hits.
//
// The helpers build a single per-namespace issue index and a cached
// topology snapshot up front, then expose a closure callers invoke
// per row. This keeps the per-row cost flat — without the index,
// listing 2000 pods would re-walk the entire issue compose pipeline
// per row.
//
// pkg/resourcecontext intentionally has no dependencies on internal/*
// or pkg/topology; the join happens here.

package server

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

// summaryContextBuilder is the per-request closure that produces a
// SummaryContext for a single resource. nil result is fine — the
// SummaryContext field is omitempty on every consumer.
//
// group is required so the per-resource issue lookup can distinguish
// CRDs that share kind+namespace+name across API groups (e.g. Knative
// Service vs corev1 Service, or two custom CRDs both named "Cluster"
// from different operators). Pass "" for core-group resources.
type summaryContextBuilder func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.SummaryContext

// newSummaryContextBuilder assembles the per-request closure for the
// list/search handlers. Returns nil when the cache or topology isn't
// available, in which case callers should skip context attachment
// rather than emit empty objects.
//
// Callers pass the namespace list they're scanning so the issue index
// is scoped to just those rows (the full Compose call on a 100-namespace
// cluster is fine; this is mostly belt-and-suspenders for very large
// envs). Pass nil to compose cluster-wide.
func (s *Server) newSummaryContextBuilder(namespaces []string, kindFilter string) summaryContextBuilder {
	topo := s.broadcaster.GetCachedTopology()
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}

	// One pass over the issue engine; group by group/kind/ns/name. We
	// rely on Filters.IncludeAudit and Filters.IncludeEvents staying
	// false-by-default in buildIssueIndex — that's what keeps the
	// per-row count to "problem" + "condition" only. Audit + Warning
	// events are loud and require explicit opt-in; rolling them into
	// the per-row count would distort "this Pod has 1 issue" for the
	// common case.
	idx := buildIssueIndex(provider, namespaces, kindFilter)

	resourceProvider := k8s.NewTopologyResourceProvider(k8s.GetResourceCache())
	dynamicProvider := k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())

	// One inverted-edges index per request — without it each
	// GetRelationships call would re-scan topo.Edges in O(E), turning
	// the list/search hot path into O(N × E). See pkg/topology T3.
	var relIdx *topology.RelationshipsIndex
	if topo != nil {
		relIdx = topology.IndexByResource(topo)
	}

	return func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.SummaryContext {
		var managedBy *resourcecontext.ManagedByRef
		if topo != nil {
			// Pass the fetched object when available so synthesis is
			// group-aware (avoids kind/plural collisions like Knative
			// Service vs corev1 Service). Falls back to (kind, ns, name)
			// lookup when neither obj nor u is set.
			var rawObj any
			switch {
			case obj != nil:
				rawObj = obj
			case u != nil:
				rawObj = u
			}
			rel := topology.GetRelationshipsWithObject(kind, namespace, name, rawObj, topo, resourceProvider, dynamicProvider, relIdx)
			managedBy = managedByFromRelationships(rel)
		}
		var source runtime.Object = obj
		if source == nil && u != nil {
			source = u
		}
		return resourcecontext.BuildSummary(source, resourcecontext.SummaryOptions{
			ManagedBy:  managedBy,
			IssueCount: idx.count(group, kind, namespace, name),
		})
	}
}

// issueIndex keys per-resource issue counts as "group|kind|namespace|name".
// Group goes FIRST so two CRDs sharing kind+namespace+name across API
// groups (e.g. Knative serving.knative.dev/Service vs corev1 ""/Service,
// or two operators each shipping a "Cluster" CRD) get independent counts
// instead of inheriting each other's. Kind is canonicalized via
// strings.ToLower because issue sources emit the kind as-typed
// (Deployment) while callers may pass the URL plural (deployments) —
// lowercase normalizes both. "|" can't appear in a Kubernetes API group
// (groups follow DNS subdomain rules: lowercase alphanumerics, "-",
// and "."), so it's a safe delimiter.
type issueIndex map[string]int

func (i issueIndex) count(group, kind, namespace, name string) int {
	return i[issueIndexKey(group, kind, namespace, name)]
}

func issueIndexKey(group, kind, namespace, name string) string {
	return group + "|" + strings.ToLower(canonicalSingular(kind)) + "|" + namespace + "|" + name
}

// canonicalSingular collapses common plural forms back to the singular
// kind the issue engine emits. Cheap surface — only the kinds we
// actually scan in list_resources / search.
func canonicalSingular(kind string) string {
	k := strings.ToLower(kind)
	switch k {
	case "pods":
		return "pod"
	case "services":
		return "service"
	case "deployments":
		return "deployment"
	case "daemonsets":
		return "daemonset"
	case "statefulsets":
		return "statefulset"
	case "replicasets":
		return "replicaset"
	case "jobs":
		return "job"
	case "cronjobs":
		return "cronjob"
	case "ingresses":
		return "ingress"
	case "configmaps":
		return "configmap"
	case "secrets":
		return "secret"
	case "persistentvolumeclaims":
		return "persistentvolumeclaim"
	case "persistentvolumes":
		return "persistentvolume"
	case "storageclasses":
		return "storageclass"
	case "horizontalpodautoscalers", "hpas", "hpa":
		return "horizontalpodautoscaler"
	case "poddisruptionbudgets":
		return "poddisruptionbudget"
	case "nodes":
		return "node"
	case "namespaces":
		return "namespace"
	case "events":
		return "event"
	}
	return k
}

func buildIssueIndex(p issues.Provider, namespaces []string, kindFilter string) issueIndex {
	// NoLimit (not MaxLimit) is required here: a 5000-issue cluster would
	// otherwise truncate after the first 1000 sorted rows, silently
	// zeroing issueCount for resources whose issues fall in the tail.
	// We're bucketing for a per-resource lookup, not paginating — the
	// caller of summaryContext never sees the issue list itself.
	filters := issues.Filters{
		Namespaces: namespaces,
		Limit:      issues.NoLimit,
	}
	if kindFilter != "" {
		// Compose's Kinds filter expects the singular kind ("Pod"). The
		// caller may pass either the URL plural ("pods") or the singular —
		// canonicalSingular normalizes both before issuing the filter.
		filters.Kinds = []string{canonicalSingular(kindFilter)}
	}
	composed := issues.Compose(p, filters)
	idx := make(issueIndex, len(composed))
	for _, iss := range composed {
		idx[issueIndexKey(iss.Group, iss.Kind, iss.Namespace, iss.Name)]++
	}
	return idx
}

// managedByFromRelationships extracts a compact ManagedByRef from
// computed topology relationships. Preference order:
//  1. Relationships.ManagedBy[0] — the server-synthesized topmost
//     manager (ArgoCD Application > Flux Kustomization/HelmRelease >
//     Helm release > topmost K8s owner). Walks the owner chain past
//     ReplicaSets to the controlling Deployment in one shot.
//  2. Direct Owner — fallback for shapes ManagedBy synthesis declines
//     (e.g. cluster-scoped roots where the topmost manager is the
//     resource itself).
//
// Returns nil when topology has no relationship for the resource.
func managedByFromRelationships(rel *topology.Relationships) *resourcecontext.ManagedByRef {
	if rel == nil {
		return nil
	}
	if len(rel.ManagedBy) > 0 {
		ref := rel.ManagedBy[0]
		return resourcecontext.ManagedByFromOwner(ref.Kind, ref.Group, ref.Namespace, ref.Name)
	}
	if rel.Owner != nil {
		return resourcecontext.ManagedByFromOwner(rel.Owner.Kind, rel.Owner.Group, rel.Owner.Namespace, rel.Owner.Name)
	}
	return nil
}
