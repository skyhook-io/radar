package issues

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// uncheckedSet collects the issue sources Compose couldn't fully read, one
// entry per source and reason.
type uncheckedSet struct {
	entries map[uncheckedKey]*uncheckedEntry
}

type uncheckedKey struct {
	source issuesapi.UncheckedSourceName
	reason issuesapi.UncheckedReason
}

type uncheckedEntry struct {
	kinds map[issuesapi.UncheckedKind]bool
	// whole is set when the gap couldn't be pinned to kinds, so listing the
	// kinds that were pinned would understate it.
	whole bool
}

// add records the source and reason. A zero kind records a gap Radar can't
// pin to particular kinds.
func (u *uncheckedSet) add(source issuesapi.UncheckedSourceName, reason issuesapi.UncheckedReason, kind issuesapi.UncheckedKind) {
	if u == nil {
		return
	}
	if u.entries == nil {
		u.entries = map[uncheckedKey]*uncheckedEntry{}
	}
	key := uncheckedKey{source: source, reason: reason}
	entry := u.entries[key]
	if entry == nil {
		entry = &uncheckedEntry{kinds: map[issuesapi.UncheckedKind]bool{}}
		u.entries[key] = entry
	}
	if kind.Kind == "" {
		entry.whole = true
	} else {
		entry.kinds[kind] = true
	}
}

// list returns the entries in a stable order, kinds sorted.
func (u *uncheckedSet) list() []issuesapi.UncheckedSource {
	if u == nil || len(u.entries) == 0 {
		return nil
	}
	out := make([]issuesapi.UncheckedSource, 0, len(u.entries))
	for key, entry := range u.entries {
		source := issuesapi.UncheckedSource{Source: key.source, Reason: key.reason}
		for kind := range entry.kinds {
			if !entry.whole {
				source.Kinds = append(source.Kinds, kind)
			}
		}
		slices.SortFunc(source.Kinds, func(a, b issuesapi.UncheckedKind) int {
			if c := strings.Compare(a.Kind, b.Kind); c != 0 {
				return c
			}
			return strings.Compare(a.Group, b.Group)
		})
		out = append(out, source)
	}
	slices.SortFunc(out, func(a, b issuesapi.UncheckedSource) int {
		if c := strings.Compare(string(a.Source), string(b.Source)); c != 0 {
			return c
		}
		return strings.Compare(string(a.Reason), string(b.Reason))
	})
	return out
}

// typedIssueKind is a built-in kind whose own problems the curated detectors
// read from Radar's typed resource cache.
type typedIssueKind struct {
	key           string // informer key, as k8s.ResourceCache names it
	group         string
	kind          string
	clusterScoped bool
}

var typedIssueKinds = []typedIssueKind{
	{key: "pods", kind: "Pod"},
	{key: "deployments", group: "apps", kind: "Deployment"},
	{key: "statefulsets", group: "apps", kind: "StatefulSet"},
	{key: "daemonsets", group: "apps", kind: "DaemonSet"},
	{key: "jobs", group: "batch", kind: "Job"},
	{key: "cronjobs", group: "batch", kind: "CronJob"},
	{key: "services", kind: "Service"},
	{key: "configmaps", kind: "ConfigMap"},
	{key: "secrets", kind: "Secret"},
	{key: "persistentvolumeclaims", kind: "PersistentVolumeClaim"},
	{key: "horizontalpodautoscalers", group: "autoscaling", kind: "HorizontalPodAutoscaler"},
	{key: "poddisruptionbudgets", group: "policy", kind: "PodDisruptionBudget"},
	{key: "nodes", kind: "Node", clusterScoped: true},
	{key: "persistentvolumes", kind: "PersistentVolume", clusterScoped: true},
	{key: "namespaces", kind: "Namespace", clusterScoped: true},
}

// typedKindReadinessProvider reports whether a typed kind's cache can be
// served. Kinds that are still loading are listed by the readers as empty,
// which would read as "nothing broken".
type typedKindReadinessProvider interface {
	typedKindReadiness(key string) k8s.KindReadiness
}

func (p *CacheProvider) typedKindReadiness(key string) k8s.KindReadiness {
	if p == nil || p.cache == nil {
		return k8s.KindUnavailable
	}
	return p.cache.KindReadinessFor(key)
}

// recordUnloadedTypedKinds records the typed kinds in scope whose cache is
// still loading or gave up loading. A kind Radar has no informer for at all
// (its own RBAC disabled it) is not listed: the response's visibility summary
// already says which kinds Radar can't read.
func recordUnloadedTypedKinds(p Provider, f Filters, unchecked *uncheckedSet) {
	rp, ok := p.(typedKindReadinessProvider)
	if !ok {
		return
	}
	for _, tk := range typedIssueKinds {
		var reason issuesapi.UncheckedReason
		switch rp.typedKindReadiness(tk.key) {
		case k8s.KindPending:
			reason = issuesapi.UncheckedReasonSyncPending
		case k8s.KindFailed:
			reason = issuesapi.UncheckedReasonSyncFailed
		default:
			continue
		}
		if !KindFilterIncludes(f.Kinds, tk.kind, tk.key) || !callerCanListTypedKind(f, tk) {
			continue
		}
		unchecked.add(issuesapi.UncheckedSourceResources, reason, issuesapi.UncheckedKind{Group: tk.group, Kind: tk.kind})
	}
}

// callerCanListTypedKind keeps a gap off the answer when the caller couldn't
// see that kind's issues anyway.
func callerCanListTypedKind(f Filters, tk typedIssueKind) bool {
	if tk.clusterScoped {
		if len(f.Namespaces) > 0 {
			return false
		}
		return f.CanReadClusterScoped == nil || f.CanReadClusterScoped(tk.kind, tk.group)
	}
	if f.CanReadEvidence == nil {
		return true
	}
	if len(f.Namespaces) == 0 {
		return f.CanReadEvidence(EvidenceRead{Group: tk.group, Resource: tk.key, Verb: "list"})
	}
	for _, ns := range f.Namespaces {
		if f.CanReadEvidence(EvidenceRead{Group: tk.group, Resource: tk.key, Namespace: ns, Verb: "list"}) {
			return true
		}
	}
	return false
}

// dynamicSyncProvider reports whether the dynamic cache can answer for a
// custom resource kind, and whether CRD discovery is still running.
type dynamicSyncProvider interface {
	crdDiscoveryPending() bool
	// dynamicSyncGap returns the reason a read of gvr in namespaces (nil for
	// every watched scope, [""] for cluster-wide) may be incomplete, or "".
	dynamicSyncGap(gvr schema.GroupVersionResource, namespaces []string) issuesapi.UncheckedReason
}

// crdDiscoveryPending covers the wait before discovery starts too: CRD warmup
// and discovery only begin once the deferred typed informers finish, and until
// then no custom resource kind is watched.
func (p *CacheProvider) crdDiscoveryPending() bool {
	if p == nil || p.dynamic == nil {
		return false
	}
	return p.dynamic.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete
}

func (p *CacheProvider) dynamicSyncGap(gvr schema.GroupVersionResource, namespaces []string) issuesapi.UncheckedReason {
	if p == nil || p.dynamic == nil {
		return ""
	}
	synced := true
	if namespaces == nil {
		// IsSynced covers only the informers that exist; a fallback walk
		// that stopped early leaves namespaces with none.
		synced = p.dynamic.IsSynced(gvr) && !p.dynamic.AllNamespacesFanoutIncomplete(gvr)
	} else {
		for _, ns := range namespaces {
			if !p.dynamic.IsNamespaceSynced(gvr, ns) {
				synced = false
				break
			}
		}
	}
	if synced {
		return ""
	}
	if p.dynamic.Observation(gvr).ReasonCode == "sync_stalled" {
		return issuesapi.UncheckedReasonSyncFailed
	}
	return issuesapi.UncheckedReasonSyncPending
}
