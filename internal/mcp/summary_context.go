// Per-request helpers that compute the compact SummaryContext attached
// to list_resources rows and search hits served via MCP. Mirrors the
// equivalent helpers in internal/server (REST list + search). Kept
// separate so MCP doesn't pull in the server package.

package mcp

import (
	"strings"
	"time"

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

// summaryCtxTopoMemo caches topology builds across summary-context list and
// search invocations. MCP has no shared broadcaster cache, so without
// memoization every list_resources / search call from an agent pays a
// full topology build (multi-second on multi-thousand-resource clusters).
// 5s TTL matches the REST broadcaster's cadence — short enough that
// managedBy stays current after a context switch, long enough that a
// burst of agent calls amortizes the build cost.
//
// Other MCP tools (handleGetResource, get_neighborhood) still build
// inline; threading them through here is a separate follow-up.
var summaryCtxTopoMemo = topology.NewMemoizer(5 * time.Second)

// buildSummaryContextTopology returns a topology snapshot suitable for
// resolving managedBy pointers, reusing a cached snapshot when one is
// fresh. Returns nil on failure — the caller falls back to a
// managedBy-less SummaryContext rather than failing the response.
func buildSummaryContextTopology(namespaces []string) *topology.Topology {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	opts := topology.DefaultBuildOptions()
	if len(namespaces) > 0 {
		opts.Namespaces = namespaces
	}
	topo, err := summaryCtxTopoMemo.Get(opts, func() (*topology.Topology, error) {
		builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).
			WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
		return builder.Build(opts)
	})
	if err != nil {
		return nil
	}
	return topo
}

// issueIndex keys per-resource issue counts as "group|kind|namespace|name".
// Group goes FIRST so two CRDs sharing kind+namespace+name across API
// groups (e.g. Knative serving.knative.dev/Service vs corev1 ""/Service,
// or two operators each shipping a "Cluster" CRD) get independent counts
// instead of inheriting each other's. Kind is canonicalized via
// canonicalSingular because issue sources emit the kind as-typed
// (Deployment) while callers may pass the URL plural (deployments) —
// canonicalization normalizes both. "|" can't appear in a Kubernetes API
// group (groups follow DNS subdomain rules), so it's a safe delimiter.
type issueIndex map[string]int

func (i issueIndex) count(group, kind, namespace, name string) int {
	return i[issueIndexKey(group, kind, namespace, name)]
}

func issueIndexKey(group, kind, namespace, name string) string {
	return group + "|" + strings.ToLower(canonicalSingular(kind)) + "|" + namespace + "|" + name
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
