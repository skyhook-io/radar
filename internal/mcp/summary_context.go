// Per-request helpers that compute the compact SummaryContext attached
// to list_resources rows and search hits served via MCP. Mirrors the
// equivalent helpers in internal/server (REST list + search). Kept
// separate so MCP doesn't pull in the server package.

package mcp

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

// newSummaryContextBuilder assembles the per-request closure for MCP
// list_resources / search. Returns nil when the cache or topology
// isn't available, in which case the caller should skip context
// attachment rather than emit empty objects.
//
// namespaces scopes the issue index to just the rows being returned;
// pass nil for cluster-wide. kindFilter ("" for search, the requested
// kind for list_resources) narrows the issue compose to a single kind
// so list_resources kind=pod doesn't pull deployment + service issues.
func newSummaryContextBuilder(namespaces []string, kindFilter string) summaryContextBuilder {
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	topo := buildSummaryContextTopology(namespaces)
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

	return func(obj runtime.Object, u *unstructured.Unstructured, kind, namespace, name string) *resourcecontext.SummaryContext {
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
			IssueCount: idx.count(kind, namespace, name),
		})
	}
}

// buildSummaryContextTopology builds a topology snapshot suitable for
// resolving managedBy pointers. MCP has no shared broadcaster cache,
// so we build directly via the builder. Returns nil on failure — the
// caller falls back to a managedBy-less SummaryContext rather than
// failing the response.
func buildSummaryContextTopology(namespaces []string) *topology.Topology {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).
		WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	opts := topology.DefaultBuildOptions()
	if len(namespaces) > 0 {
		opts.Namespaces = namespaces
	}
	topo, err := builder.Build(opts)
	if err != nil {
		return nil
	}
	return topo
}

// issueIndex keys per-resource issue counts as "kind|namespace|name".
// Kind is canonicalized via canonicalSingular because issue sources emit
// the kind as-typed (Deployment) while callers may pass the URL plural
// (deployments) — canonicalization normalizes both.
type issueIndex map[string]int

func (i issueIndex) count(kind, namespace, name string) int {
	return i[issueIndexKey(kind, namespace, name)]
}

func issueIndexKey(kind, namespace, name string) string {
	return strings.ToLower(canonicalSingular(kind)) + "|" + namespace + "|" + name
}

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
// computed topology relationships. Preference: server-synthesized
// Relationships.ManagedBy (ArgoCD > Flux > Helm > topmost K8s owner),
// then direct Owner as fallback when synthesis declines.
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

