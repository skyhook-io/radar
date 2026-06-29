// Package summarycontext is the shared core that powers the compact
// ResourceSummaryContext attached to /api/ai/resources/{kind} list rows, /api/search
// hits, and the MCP list_resources / search variants.
//
// The REST and MCP wrappers (internal/server, internal/mcp) differ only
// in their topology source — REST reads from a server-wide broadcaster
// cache; MCP memoizes per-process builds. Everything else (issue index,
// kind canonicalization, managedBy resolution, per-row dispatch by
// scope) is identical, so it lives here.
//
// pkg/resourcecontext intentionally has no dependencies on internal/*
// or pkg/topology; the join happens here.
package summarycontext

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

// Builder is the per-request closure that produces a ResourceSummaryContext for
// a single resource. nil result is fine — the ResourceSummaryContext field is
// omitempty on every consumer.
//
// group is required so the per-resource issue lookup can distinguish
// CRDs that share kind+namespace+name across API groups (e.g. Knative
// Service vs corev1 Service, or two custom CRDs both named "Cluster"
// from different operators). Pass "" for core-group resources.
type Builder func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.ResourceSummaryContext

// BuilderFromIndexes assembles the per-request closure. The list path
// passes the same index for both namespacedIdx and clusterIdx (single-
// kind list, scope already chosen by the caller); search passes two
// distinct indexes — namespacedIdx scoped to user namespaces, clusterIdx
// composed cluster-wide. The closure dispatches per-hit by scope so
// cluster-scoped hits read the cluster-wide index and surface
// namespace="" issues that the namespaced filter would otherwise drop.
//
// topo is the topology snapshot the caller has already obtained from
// its preferred source (REST: broadcaster cache; MCP: short-TTL
// memoizer). nil topo is fine — managedBy is omitted but issueCount
// still resolves.
func BuilderFromIndexes(topo *topology.Topology, namespacedIdx, clusterIdx IssueIndex) Builder {
	resourceProvider := k8s.NewTopologyResourceProvider(k8s.GetResourceCache())
	dynamicProvider := k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())

	// One inverted-edges index per request — without it each
	// GetRelationships call would re-scan topo.Edges in O(E), turning
	// the list/search hot path into O(N × E). See pkg/topology T3.
	var relIdx *topology.RelationshipsIndex
	if topo != nil {
		relIdx = topology.IndexByResource(topo)
	}

	return func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.ResourceSummaryContext {
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
			managedBy = ManagedByFromRelationships(rel)
		}
		var source runtime.Object = obj
		if source == nil && u != nil {
			source = u
		}
		// Dispatch by scope: cluster-scoped hits read clusterIdx (composed
		// at namespace=nil so namespace="" issues are present), namespaced
		// hits read namespacedIdx (which honors the user's namespace
		// filter so the per-row count doesn't pull in noise from
		// namespaces the user can't see).
		idx := namespacedIdx
		if clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group); clusterScoped {
			idx = clusterIdx
		}
		return resourcecontext.BuildSummary(source, resourcecontext.SummaryOptions{
			ManagedBy:  managedBy,
			IssueCount: idx.Count(group, kind, namespace, name),
		})
	}
}

// IssueIndex keys per-resource issue counts as "group|kind|namespace|name".
// Group goes FIRST so two CRDs sharing kind+namespace+name across API
// groups (e.g. Knative serving.knative.dev/Service vs corev1 ""/Service,
// or two operators each shipping a "Cluster" CRD) get independent counts
// instead of inheriting each other's. Kind is canonicalized via
// CanonicalSingular because issue sources emit the kind as-typed
// (Deployment) while callers may pass the URL plural (deployments) —
// canonicalization normalizes both. "|" can't appear in a Kubernetes API
// group (groups follow DNS subdomain rules), so it's a safe delimiter.
type IssueIndex map[string]int

// Count returns the per-resource issue count, keyed by the group-aware
// composite key. Zero on miss.
func (i IssueIndex) Count(group, kind, namespace, name string) int {
	return i[issueIndexKey(group, kind, namespace, name)]
}

func issueIndexKey(group, kind, namespace, name string) string {
	return group + "|" + strings.ToLower(CanonicalSingular(kind)) + "|" + namespace + "|" + name
}

// CanonicalSingular collapses common plural forms back to the singular
// kind the issue engine emits. Cheap surface — only the kinds we
// actually scan in list_resources / search.
func CanonicalSingular(kind string) string {
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

// BuildIssueIndex composes the per-request issue index. NoLimit (not
// MaxLimit) is required here: a 5000-issue cluster would otherwise
// truncate after the first 1000 sorted rows, silently zeroing
// issueCount for resources whose issues fall in the tail. We're
// bucketing for a per-resource lookup, not paginating — the caller of
// the builder never sees the issue list itself.
//
// The per-row count reflects exactly the curated operational sources
// Compose runs (problem + missing_ref + scheduling + condition). Loud
// adjacent signals — raw Warning events and policy/audit posture — are
// not issue sources at all, so they can't distort "this Pod has 1 issue"
// for the common case.
//
// No Kinds filter on Compose: the index buckets every composed row by
// (group, kind, ns, name), and the per-row lookup keys off
// issueIndexKey(...) with the same canonicalization, so kind-mismatched
// rows simply never read. Filtering Compose itself by Kind would need
// CRD-plural awareness — CanonicalSingular handles built-ins but
// returns CRD plurals (e.g. "applications") unchanged, and the issue
// engine emits "Application", silently zeroing issueCount on every CRD
// row. Bucketing is O(N) over the at-most-namespace-bounded issue set,
// which the consumer materialises anyway.
func BuildIssueIndex(p issues.Provider, namespaces []string) IssueIndex {
	filters := issues.Filters{
		Namespaces: namespaces,
		Limit:      issues.NoLimit,
	}
	// Compose FLAT (uncapped): every evidence row carries the grouped issue ID
	// (enrichIdentity keys it on owner-else-self + category) and its resolved
	// Owner. Counting DISTINCT grouped issue IDs per resource — keyed on each
	// evidence resource AND its owner (the grouped subject) — makes get_resource
	// on the workload OR on ANY of its (arbitrarily many) affected pods surface
	// the same issue. Iterating the grouped issue's inline Members instead would
	// drop members past the maxInlineMembers cap; keying only the evidence (the
	// old flat index) dropped the owner.
	flat := issues.Compose(p, filters)
	seen := make(map[string]map[string]bool, len(flat)) // resourceKey -> set of grouped issue IDs
	mark := func(group, kind, namespace, name, id string) {
		k := issueIndexKey(group, kind, namespace, name)
		if seen[k] == nil {
			seen[k] = make(map[string]bool)
		}
		seen[k][id] = true
	}
	for _, f := range flat {
		mark(f.Group, f.Kind, f.Namespace, f.Name, f.ID)
		if f.Owner.Kind != "" {
			mark(f.Owner.Group, f.Owner.Kind, f.Owner.Namespace, f.Owner.Name, f.ID)
		}
	}
	idx := make(IssueIndex, len(seen))
	for k, ids := range seen {
		idx[k] = len(ids)
	}
	return idx
}

// ManagedByFromRelationships extracts a compact ManagedByRef from
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
func ManagedByFromRelationships(rel *topology.Relationships) *resourcecontext.ManagedByRef {
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
