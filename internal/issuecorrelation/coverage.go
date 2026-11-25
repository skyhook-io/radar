package issuecorrelation

import (
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// collected records why Radar holds no changes for kind in namespace ("" for
// a cluster-scoped kind): it doesn't collect the kind, or not in that
// namespace, or it is still loading it. It reports whether the kind is ready:
// collected in namespace and synced. For a coverage-checking caller, a kind
// Radar started watching after the window began is not ready either: its
// earlier changes were never seen, so the history is incomplete from then.
func (c *run) collected(kind, group, namespace string) bool {
	ready, late := c.collectedFor(kind, group, namespace)
	if late.Incomplete {
		c.watchHistory = mergeHistory(c.watchHistory, late)
		c.addUnknown(issuesapi.CorrelationUnknownHistoryIncomplete, group, kind)
		return false
	}
	return ready
}

// collectedObject is collected for the subject workload's kind and its
// ConfigMaps' and Secrets' kinds. A kind watched late is recorded in
// objectWatchHistory and left to historyCoversObjects: an object created
// after the watch began has had every change seen.
func (c *run) collectedObject(kind, group, namespace string) bool {
	ready, late := c.collectedFor(kind, group, namespace)
	c.objectWatchHistory = mergeHistory(c.objectWatchHistory, late)
	return ready
}

// collectedFor reports whether kind is collected in namespace and synced,
// and, for a coverage-checking caller, the history lost when Radar started
// watching it after the window began.
func (c *run) collectedFor(kind, group, namespace string) (ready bool, late issuesapi.HistoryCoverage) {
	if reason, ok := collectionGap(kind, group, namespace); ok {
		c.addUnknown(reason, group, kind)
		return false, late
	}
	if !c.opts.CheckCoverage {
		return true, late
	}
	if since, ok := watchedSince(kind, group, namespace); ok && since.After(c.opts.Window.From) {
		late = issuesapi.HistoryCoverage{
			Incomplete: true, CompleteSince: roundUpToSecond(since).UTC(), Reasons: []issuesapi.HistoryGapReason{issuesapi.HistoryObservationStartedLate},
		}
	}
	return true, late
}

// watchedSince is when Radar's change history for kind in namespace becomes
// complete, which is later than the store's own start: a typed informer
// counts from when its first list and the change handlers' pass over it were
// done (every one starts after the timeline, and the deferred ones only after
// the critical ones synced), and a kind collected through the dynamic cache
// (custom resources, webhook configurations) from when it was first watched.
func watchedSince(kind, group, namespace string) (time.Time, bool) {
	if b, builtin := resourceid.BuiltinForKind(kind); builtin && (group == "" || b.Group == group) {
		if cache := k8s.GetResourceCache(); cache != nil {
			if g, resource, _, ok := k8s.ResolveChangeGVR(kind, b.Group); ok && g == b.Group {
				if _, known := cache.InformerSynced(resource); known {
					since, _, ok := cache.InformerWatchedSince(resource)
					return since, ok
				}
			}
		}
	}
	disc := k8s.GetResourceDiscovery()
	dyn := k8s.GetDynamicResourceCache()
	if disc == nil || dyn == nil {
		return time.Time{}, false
	}
	gvr, ok := disc.GetGVRWithGroup(kind, group)
	if !ok {
		return time.Time{}, false
	}
	return dyn.NamespaceWatchedSince(gvr, namespace)
}

func roundUpToSecond(t time.Time) time.Time {
	if rounded := t.Truncate(time.Second); !rounded.Equal(t) {
		return rounded.Add(time.Second)
	}
	return t
}

func collectionGap(kind, group, namespace string) (issuesapi.CorrelationUnknownReason, bool) {
	if b, builtin := resourceid.BuiltinForKind(kind); builtin && (group == "" || b.Group == group) {
		if cache := k8s.GetResourceCache(); cache != nil {
			if g, resource, _, ok := k8s.ResolveChangeGVR(kind, b.Group); ok && g == b.Group {
				if synced, known := cache.InformerSynced(resource); known {
					switch {
					case !cache.KindCoversNamespace(resource, namespace):
						return issuesapi.CorrelationUnknownNamespaceNotCollected, true
					case !synced:
						return issuesapi.CorrelationUnknownKindSyncPending, true
					}
					// Synced, but its change handlers haven't finished the
					// initial list: until then changes may arrive as initial
					// state, so the kind is still loading.
					if _, _, caughtUp := cache.InformerWatchedSince(resource); !caughtUp {
						return issuesapi.CorrelationUnknownKindSyncPending, true
					}
					return "", false
				}
			}
		}
	}
	disc := k8s.GetResourceDiscovery()
	dyn := k8s.GetDynamicResourceCache()
	if disc == nil || dyn == nil {
		return issuesapi.CorrelationUnknownKindNotCollected, true
	}
	gvr, ok := disc.GetGVRWithGroup(kind, group)
	if !ok {
		return issuesapi.CorrelationUnknownKindNotCollected, true
	}
	if dyn.IsNamespaceSynced(gvr, namespace) {
		return "", false
	}
	if dyn.HasCoveringInformer(gvr, namespace) {
		return issuesapi.CorrelationUnknownKindSyncPending, true
	}
	return issuesapi.CorrelationUnknownKindNotCollected, true
}
