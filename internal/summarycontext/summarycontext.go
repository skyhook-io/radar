// Package summarycontext is the shared core that powers the compact
// ResourceSummaryContext attached to /api/ai/resources/{kind} list rows, /api/search
// hits, and the MCP list_resources / search variants.
//
// The REST and MCP wrappers (internal/server, internal/mcp) differ only
// in their topology source — REST and hosted MCP reuse the server graph;
// standalone MCP memoizes its builds. Everything else (issue index,
// kind canonicalization, managedBy resolution, per-row dispatch by
// scope) is identical, so it lives here.
//
// pkg/resourcecontext has no dependencies on internal/*; the join happens here.
package summarycontext

import (
	"context"
	"log"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
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

// Budget bounds optional list/search enrichment, including authorization.
// It starts on the first row; core listing/searching uses the original context.
const Budget = 3 * time.Second

// WithBudget returns a non-nil builder that starts preparation on the first row.
// build gets a context bounded by ctx and Budget, under the shared preparation
// gate. Failed preparation retains cheap fields with omissions for unknowns.
// Call cleanup when finished; builder and cleanup must be used sequentially.
func WithBudget(ctx context.Context, build func(context.Context) Builder) (Builder, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	stopBudget := func() {}
	var builder Builder
	return func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.ResourceSummaryContext {
		if builder == nil {
			var budgetCtx context.Context
			budgetCtx, stopBudget = context.WithTimeout(ctx, Budget)
			builder = WithinContext(budgetCtx, preparationGate, func() Builder { return build(budgetCtx) })
			if builder == nil {
				reason := resourcecontext.OmittedUnavailable
				if budgetCtx.Err() != nil {
					reason = resourcecontext.OmittedBudgetExceeded
				}
				builder = func(obj runtime.Object, u *unstructured.Unstructured, group, _, _, _ string) *resourcecontext.ResourceSummaryContext {
					if obj == nil && u != nil {
						obj = u
					}
					omitted := []resourcecontext.OmittedField{{Field: "managedBy", Reason: reason}, {Field: "issueCount", Reason: reason}}
					var scanned *resourcecontext.ScannedRef
					if group == aicontext.TrivyGroup {
						if u != nil {
							_, scanned = trivyScanTarget(nil, u, nil)
						}
						omitted = append(omitted, resourcecontext.OmittedField{Field: "scanned", Reason: reason})
					}
					return resourcecontext.BuildSummary(obj, resourcecontext.SummaryOptions{Scanned: scanned, Omitted: omitted})
				}
			}
		}
		return builder(obj, u, group, kind, namespace, name)
	}, func() { stopBudget(); cancel() }
}

// Cache scans can outlive a caller's deadline. Bound that work without sharing
// caller-specific results or letting one slow preparation block every caller.
var preparationGate = NewBudgetGate(8)

// BudgetGate limits concurrent work that may outlive an enrichment deadline.
type BudgetGate struct {
	slots chan struct{}
}

// NewBudgetGate allows at most limit concurrent builds; use a positive limit.
func NewBudgetGate(limit int) *BudgetGate {
	return &BudgetGate{slots: make(chan struct{}, limit)}
}

// WithinContext bounds waiting and the number of builds left running after
// cancellation. A build keeps its gate slot until it finishes. Cancellation or
// a recovered panic returns T's zero value, as can build itself.
// Each independent cache source owns a gate so one cold source cannot suppress
// a ready answer from another.
func WithinContext[T any](ctx context.Context, gate *BudgetGate, build func() T) T {
	var zero T
	if ctx.Err() != nil {
		return zero
	}
	select {
	case gate.slots <- struct{}{}:
	case <-ctx.Done():
		return zero
	}
	if ctx.Err() != nil {
		<-gate.slots
		return zero
	}
	done := make(chan T, 1)
	go func() {
		defer func() { <-gate.slots }()
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[summarycontext] Background enrichment failed: %v", err)
				done <- zero
			}
		}()
		done <- build()
	}()
	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		return zero
	}
}

// IndexedTopology pairs a graph with its own index; a nil Index permits inline
// edge scans. A nil Topology means the graph is unavailable.
type IndexedTopology struct {
	Topology *topology.Topology
	Index    *topology.RelationshipsIndex
}

// BuilderFromIndexes composes canonical ownership next hops and optional issue
// counts. The graph is obtained only for residual spec-declared relations.
// canRead gates Trivy scan targets. A nil owners skips other ownership enrichment
// without an omission note; production callers supply a caller-scoped session.
func BuilderFromIndexes(ctx context.Context, topologySource func() IndexedTopology, namespacedIdx, clusterIdx IssueIndex, canRead func(group, kind, namespace string) bool, owners *k8s.AgentOwnership) Builder {
	var topo *topology.Topology
	var relIdx *topology.RelationshipsIndex
	var resourceProvider topology.ResourceProvider
	var dynamicProvider topology.DynamicProvider
	loaded := false
	relationships := func(kind, namespace, name string, obj any) *topology.Relationships {
		if !loaded {
			loaded = true
			if topologySource != nil {
				snapshot := topologySource()
				topo, relIdx = snapshot.Topology, snapshot.Index
			}
			if topo != nil {
				resourceProvider = k8s.NewTopologyResourceProvider(k8s.GetResourceCache())
				dynamicProvider = k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())
			}
		}
		if topo == nil {
			return nil
		}
		rel := topology.GetRelationshipsWithObject(kind, namespace, name, obj, topo,
			resourceProvider, dynamicProvider, relIdx)
		if rel == nil {
			return &topology.Relationships{}
		}
		return rel
	}
	return func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.ResourceSummaryContext {
		var managedBy *resourcecontext.ManagedByRef
		var scanned *resourcecontext.ScannedRef
		var omitted []resourcecontext.OmittedField
		residualUnavailable := false
		var source runtime.Object = obj
		if source == nil && u != nil {
			source = u
		}
		if ctx.Err() != nil {
			omitted = []resourcecontext.OmittedField{{Field: "managedBy", Reason: resourcecontext.OmittedBudgetExceeded}}
			if group == aicontext.TrivyGroup {
				if u != nil {
					_, scanned = trivyScanTarget(nil, u, nil)
				}
				omitted = append(omitted, resourcecontext.OmittedField{Field: "scanned", Reason: resourcecontext.OmittedBudgetExceeded})
			}
		} else if u != nil && group == aicontext.TrivyGroup {
			managedBy, scanned = trivyScanTarget(k8s.GetResourceCache(), u, canRead)
		} else if m, ok := source.(metav1.Object); ok && owners != nil {
			ref := resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}
			chain := owners.Of(m, ref)
			if top := chain.Top(); top != nil {
				managedBy = resourcecontext.ControllerSummary(*top)
			} else if chain == nil || chain.Stop != "" || len(chain.Refs) > 0 {
				var other []topology.ResourceRef
				if chain != nil && chain.Stop == "" && len(chain.Refs) > 0 {
					rel := relationships(kind, namespace, name, source)
					other, residualUnavailable = owners.OtherManagers(rel, ref)
				}
				if len(other) > 0 {
					managedBy = resourcecontext.ControllerSummary(other[0])
				} else {
					answer := owners.Manager(ref)
					managedBy = resourcecontext.ManagerSummary(answer)
					if managedBy == nil && answer.State != ownership.ManagerNone {
						omitted = []resourcecontext.OmittedField{{Field: "managedBy", Reason: resourcecontext.OmittedUnavailable}}
					}
				}
			}
			if reason := resourcecontext.ControllerOmission(chain); reason != "" {
				omitted = []resourcecontext.OmittedField{{Field: "managedBy", Reason: reason}}
			}
		}
		if residualUnavailable && len(omitted) == 0 {
			omitted = []resourcecontext.OmittedField{{Field: "managedBy", Reason: resourcecontext.OmittedUnavailable}}
		}
		if ctx.Err() != nil {
			hasManagedOmission := false
			for i := range omitted {
				hasManagedOmission = hasManagedOmission || omitted[i].Field == "managedBy"
				if omitted[i].Reason == resourcecontext.OmittedUnavailable {
					omitted[i].Reason = resourcecontext.OmittedBudgetExceeded
				}
			}
			if managedBy == nil && !hasManagedOmission {
				omitted = append(omitted, resourcecontext.OmittedField{Field: "managedBy", Reason: resourcecontext.OmittedBudgetExceeded})
			}
		}
		idx := namespacedIdx
		if clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group); clusterScoped {
			idx = clusterIdx
		}
		return resourcecontext.BuildSummary(source, resourcecontext.SummaryOptions{
			ManagedBy: managedBy, IssueCount: idx.Count(group, kind, namespace, name), Scanned: scanned, Omitted: omitted,
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
func BuildIssueIndex(p issues.Provider, filters issues.Filters) IssueIndex {
	filters.SkipPodTemplateContext = true
	filters.Limit = issues.NoLimit
	filters.Grouped = false

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
		// A finding about another subject (a NetworkPolicy, an expired TLS
		// Secret) also counts on each resource it affects, as the related-issues
		// lookup shows it there. Compose already removed the refs the caller
		// can't read; without an authorizer it didn't, so count none.
		if filters.CanReadRelated != nil {
			for _, ref := range f.AffectedRefs {
				mark(ref.Group, ref.Kind, ref.Namespace, ref.Name, f.ID)
			}
		}
	}
	idx := make(IssueIndex, len(seen))
	for k, ids := range seen {
		idx[k] = len(ids)
	}
	return idx
}
