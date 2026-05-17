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
type summaryContextBuilder func(obj runtime.Object, u *unstructured.Unstructured, kind, namespace, name string) *resourcecontext.SummaryContext

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

	// One pass over the issue engine; group by kind/ns/name. Sources
	// are restricted to "problem" + "condition" — the two always-on
	// surfaces that match the default /api/issues + MCP issues_list
	// behavior. Audit + Warning events are loud and require explicit
	// opt-in; rolling them into the per-row count would distort
	// "this Pod has 1 issue" for the common case.
	idx := buildIssueIndex(provider, namespaces, kindFilter)

	resourceProvider := k8s.NewTopologyResourceProvider(k8s.GetResourceCache())
	dynamicProvider := k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())

	return func(obj runtime.Object, u *unstructured.Unstructured, kind, namespace, name string) *resourcecontext.SummaryContext {
		var managedBy *resourcecontext.ManagedByRef
		if topo != nil {
			rel := topology.GetRelationships(kind, namespace, name, topo, resourceProvider, dynamicProvider)
			managedBy = managedByFromRelationships(rel)
		}
		var source runtime.Object = obj
		if source == nil && u != nil {
			source = u
		}
		return resourcecontext.BuildSummary(source, resourcecontext.SummaryOptions{
			ManagedBy:  managedBy,
			IssueCount: idx.count(kind, namespace, name),
		})
	}
}

// issueIndex keys per-resource issue counts as "kind|namespace|name".
// Kind is canonicalized via strings.ToLower because issue sources emit
// the kind as-typed (Deployment) while callers may pass the URL plural
// (deployments) — lowercase normalizes both.
type issueIndex map[string]int

func (i issueIndex) count(kind, namespace, name string) int {
	return i[issueIndexKey(kind, namespace, name)]
}

func issueIndexKey(kind, namespace, name string) string {
	return strings.ToLower(canonicalSingular(kind)) + "|" + namespace + "|" + name
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
	filters := issues.Filters{
		Namespaces: namespaces,
		Limit:      issues.MaxLimit,
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
		idx[issueIndexKey(iss.Kind, iss.Namespace, iss.Name)]++
	}
	return idx
}

// managedByFromRelationships extracts a compact ManagedByRef from
// computed topology relationships. Preference order:
//  1. Deployment grandparent shortcut (Pods owned by ReplicaSets surface
//     the controlling Deployment, not the noisy hash-suffixed RS).
//  2. Direct Owner — covers everything else (StatefulSet pod → STS,
//     Job pod → Job, ArgoCD Application children, Flux Kustomization
//     children, etc.).
//
// Returns nil when topology has no relationship for the resource.
func managedByFromRelationships(rel *topology.Relationships) *resourcecontext.ManagedByRef {
	if rel == nil {
		return nil
	}
	var ref *topology.ResourceRef
	switch {
	case rel.Deployment != nil:
		ref = rel.Deployment
	case rel.Owner != nil:
		ref = rel.Owner
	default:
		return nil
	}
	return resourcecontext.ManagedByFromOwner(ref.Kind, ref.Group, ref.Namespace, ref.Name)
}
