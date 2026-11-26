package issuecorrelation

import (
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// collected reports whether kind is ready: collected in namespace ("" for a
// cluster-scoped kind), synced, and watched since before the window began.
func (c *run) collected(kind, group, namespace string) bool {
	return c.kindCollected(kind, group, namespace) && c.watchCovers(kind, group, namespace)
}

// kindCollected reports whether Radar collects kind in namespace and has
// synced it, recording why when not.
func (c *run) kindCollected(kind, group, namespace string) bool {
	if reason, ok := collectionGap(kind, group, namespace); ok {
		c.addUnknown(reason, group, kind)
		return false
	}
	return true
}

// objectWatch is watchCovers for the subject workload's kind and its
// ConfigMaps' and Secrets' kinds. A kind watched late is recorded in
// objectWatchHistory and left to historyCoversObjects: an object created
// after the watch began has had every change seen.
func (c *run) objectWatch(kind, group, namespace string) {
	if late, ok := c.lateWatch(kind, group, namespace); ok {
		c.objectWatchHistory = mergeHistory(c.objectWatchHistory, late)
	}
}

// watchCovers reports whether Radar has watched kind in namespace since
// before the window began. A kind watched later never saw the earlier
// changes, so the history is incomplete from its watch start; that is
// recorded in watchHistory.
func (c *run) watchCovers(kind, group, namespace string) bool {
	if late, ok := c.lateWatch(kind, group, namespace); ok {
		c.watchHistory = mergeHistory(c.watchHistory, late)
		c.addUnknown(issuesapi.CorrelationUnknownHistoryIncomplete, group, kind)
		return false
	}
	return true
}

// lateWatch is the history lost when Radar started watching kind in
// namespace after the window began.
func (c *run) lateWatch(kind, group, namespace string) (issuesapi.HistoryCoverage, bool) {
	since, ok := watchedSince(kind, group, namespace)
	if !ok || !since.After(c.opts.Window.From) {
		return issuesapi.HistoryCoverage{}, false
	}
	return issuesapi.HistoryCoverage{
		Incomplete: true, CompleteSince: roundUpToSecond(since).UTC(), Reasons: []issuesapi.HistoryGapReason{issuesapi.HistoryObservationStartedLate},
	}, true
}

// watchedSince is when Radar's change history for kind in namespace becomes
// complete, which is later than the store's own start: a typed informer
// counts from when its first list and the change handlers' pass over it were
// done (every one starts after the timeline, and the deferred ones only after
// the critical ones synced), and a kind collected through the dynamic cache
// (custom resources, webhook configurations) from when it was first watched.
var watchedSince = cacheWatchedSince

// SetWatchedSinceForTest stands in for when Radar's history for each kind
// becomes complete and returns the function that restores it. The dynamic
// cache dates its watches at real time, so without it a test can't have a
// webhook configuration kind watched since before its window.
func SetWatchedSinceForTest(since func(kind, group, namespace string) (time.Time, bool)) func() {
	prev := watchedSince
	watchedSince = since
	return func() { watchedSince = prev }
}

func cacheWatchedSince(kind, group, namespace string) (time.Time, bool) {
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
