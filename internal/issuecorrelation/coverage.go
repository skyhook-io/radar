package issuecorrelation

import (
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// checkCollected records why Radar holds no changes for a source's kind in
// namespace ("" for a cluster-scoped kind): it doesn't collect the kind, or
// not in that namespace, or it is still loading it. An empty answer about
// such a source means nothing.
func (c *run) checkCollected(kind, group, namespace string) {
	c.collected(kind, group, namespace)
}

// collected is checkCollected that also reports whether the kind is ready:
// collected in namespace and synced. For a coverage-checking caller, a kind
// Radar started watching after the window began is not ready either: its
// earlier changes were never seen, so the history is incomplete from then.
func (c *run) collected(kind, group, namespace string) bool {
	if reason, ok := collectionGap(kind, group, namespace); ok {
		c.addUnknown(reason, group, kind)
		return false
	}
	if !c.opts.CheckCoverage {
		return true
	}
	if since, ok := watchedSince(kind, group, namespace); ok && since.After(c.opts.Window.From) {
		c.watchHistory = mergeHistory(c.watchHistory, issuesapi.HistoryCoverage{
			Incomplete: true, CompleteSince: roundUpToSecond(since).UTC(), Reasons: []issuesapi.HistoryGapReason{issuesapi.HistoryObservationStartedLate},
		})
		c.addUnknown(issuesapi.CorrelationUnknownHistoryIncomplete, group, kind)
		return false
	}
	return true
}

// watchedSince is when Radar started watching kind in namespace, when that
// can be later than the store's own start: a deferred typed informer
// (ConfigMaps, Secrets, ResourceQuotas, LimitRanges among them) starts only
// after the critical ones have synced, and a kind collected through the
// dynamic cache (custom resources, webhook configurations) starts when first
// needed. Critical typed informers start at once after the timeline, so ok is
// false for them and the store's observation start stands in.
func watchedSince(kind, group, namespace string) (time.Time, bool) {
	if b, builtin := resourceid.BuiltinForKind(kind); builtin && (group == "" || b.Group == group) {
		if cache := k8s.GetResourceCache(); cache != nil {
			if g, resource, _, ok := k8s.ResolveChangeGVR(kind, b.Group); ok && g == b.Group {
				if _, known := cache.InformerSynced(resource); known {
					since, deferred, ok := cache.InformerWatchedSince(resource)
					return since, ok && deferred
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
