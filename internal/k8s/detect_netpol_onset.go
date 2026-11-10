package k8s

import (
	"context"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// onsetObservationMargin absorbs clock skew between the API server, which
// stamps creation times, and this process, which stamps observations.
const onsetObservationMargin = time.Minute

// isolationWindow is the span in which every change that can start or end an
// isolation was recorded and is still retained. A zero since means none is
// known.
type isolationWindow struct {
	since time.Time
	// policyKinds are the policy kinds whose changes count, core NetworkPolicy
	// plus the other served policy APIs.
	policyKinds []string
}

// networkPolicyIsolationOnset dates the isolation from the policy's creation,
// and only when it's established that creating it started the isolation:
//   - its spec was never changed (generation 1), so creation set the rules;
//   - every affected Service and pod already existed then, and every pod
//     those Services select is isolated, so no unrecorded readiness change
//     could have started it;
//   - the window covers it, and no other policy in the namespace or cluster
//     scope (core or another engine's), affected Service, pod those Services
//     select, or the Namespace itself was created, changed or deleted at or
//     after it (deleting an allow policy, changing a selector or the
//     namespace's labels another engine selects on, or losing a backend the
//     policy didn't isolate would start the isolation later).
//
// A pod relabeled into or out of a Service's selector counts through its old
// and new labels. Anything less leaves the onset unknown: a backdated onset
// would mislead.
func networkPolicyIsolationOnset(window isolationWindow, np *networkingv1.NetworkPolicy, impact *denyAllIngressImpact) (time.Time, bool) {
	created := np.CreationTimestamp.Time
	if created.IsZero() || np.Generation > 1 || impact.lastCreated.After(created) || impact.readinessDependent ||
		window.since.IsZero() || window.since.After(created.Add(-onsetObservationMargin)) {
		return time.Time{}, false
	}
	events, ok := isolationInputEventsSince(window.policyKinds, np.Namespace, impact.services, created)
	if !ok {
		return time.Time{}, false
	}
	for _, e := range events {
		if e.Kind == "NetworkPolicy" && e.Namespace == np.Namespace && e.Name == np.Name && resourceid.GroupFromAPIVersion(e.APIVersion) == "networking.k8s.io" {
			continue
		}
		if e.Namespace != "" && e.Namespace != np.Namespace {
			continue
		}
		if e.Kind == "Pod" && !matchesAny(impact.selectors, e.Labels) && !matchesAny(impact.selectors, previousLabels(e)) {
			continue
		}
		// An equal timestamp can't establish order, so it counts as after.
		if !e.Timestamp.Before(created) {
			return time.Time{}, false
		}
	}
	return created, true
}

// lostEventReasons are the drop reasons that lose a real change rather than
// filter noise.
var lostEventReasons = []string{timeline.DropReasonStoreFailed, timeline.DropReasonStoreDown, timeline.DropReasonChannelFull}

// networkPolicyWatchWindow returns the window in which every policy, Service
// and pod change was recorded and kept: from the latest of when this process
// began recording, when its NetworkPolicy informer synced, and when it began
// watching each other served policy API. There is none once the store has
// evicted anything or any change was lost on the way to it.
func networkPolicyWatchWindow(cache *ResourceCache) isolationWindow {
	none := isolationWindow{}
	start := timeline.ObservationStart()
	store := timeline.GetStore()
	if start.IsZero() || cache == nil || store == nil {
		return none
	}
	metrics := timeline.GetMetrics().GetSnapshot()
	for _, reason := range lostEventReasons {
		if metrics.Counters.Dropped[reason] > 0 {
			return none
		}
	}
	if store.Stats().EventsEvicted {
		return none
	}
	synced := time.Time{}
	for _, s := range cache.GetSyncStatus().Informers {
		if s.Key == "networkpolicies" && s.Synced {
			synced, _ = time.Parse(time.RFC3339, s.SyncedAt)
		}
	}
	if synced.IsZero() {
		return none
	}
	if synced.After(start) {
		start = synced
	}
	discovery := GetResourceDiscovery()
	if discovery == nil {
		return none
	}
	dynamicCache := GetDynamicResourceCache()
	kinds := []string{"NetworkPolicy"}
	for _, api := range otherPolicyAPIList() {
		gvr, served := discovery.GetGVRWithGroup(api.kind, api.group)
		if !served {
			continue
		}
		if dynamicCache == nil {
			return none
		}
		watched, ok := dynamicCache.WatchedSince(gvr)
		if !ok {
			return none
		}
		if watched.After(start) {
			start = watched
		}
		kinds = append(kinds, api.kind)
	}
	return isolationWindow{since: start, policyKinds: kinds}
}

// otherPolicyAPIList is every policy API Radar doesn't evaluate:
// otherPolicyAPIs, Cilium's, and Calico's enforced kinds.
func otherPolicyAPIList() []struct{ kind, group string } {
	out := []struct{ kind, group string }{
		{"CiliumNetworkPolicy", "cilium.io"},
		{"CiliumClusterwideNetworkPolicy", "cilium.io"},
	}
	for _, api := range otherPolicyAPIs {
		out = append(out, struct{ kind, group string }{api.kind, api.group})
	}
	for _, group := range topology.CalicoAPIGroups() {
		for _, def := range topology.CalicoPolicyKinds() {
			if !def.Staged {
				out = append(out, struct{ kind, group string }{def.Kind, group})
			}
		}
	}
	return out
}

// isolationInputEventsSince returns, since t, the events of the policy kinds
// in every namespace and at cluster scope, the Namespace's own events, and the
// namespace's events for the named Services and for pods. false when a query
// fails or hits its cap.
func isolationInputEventsSince(policyKinds []string, namespace string, services []string, t time.Time) ([]timeline.TimelineEvent, bool) {
	const limit = 500
	query := func(opts timeline.QueryOptions) ([]timeline.TimelineEvent, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		opts.Since, opts.Limit = t, limit
		opts.Sources, opts.ClusterContext = []timeline.EventSource{timeline.SourceInformer}, ActiveClusterContext()
		events, err := timeline.QueryEvents(ctx, opts)
		return events, err == nil && len(events) < limit
	}
	policies, ok := query(timeline.QueryOptions{Kinds: policyKinds})
	if !ok {
		return nil, false
	}
	svcs, ok := query(timeline.QueryOptions{Namespaces: []string{namespace}, Kinds: []string{"Service"}, APIGroups: []string{""}, Names: services})
	if !ok {
		return nil, false
	}
	pods, ok := query(timeline.QueryOptions{Namespaces: []string{namespace}, Kinds: []string{"Pod"}, APIGroups: []string{""}, IncludeManaged: true})
	if !ok {
		return nil, false
	}
	ns, ok := query(timeline.QueryOptions{Kinds: []string{"Namespace"}, APIGroups: []string{""}, Names: []string{namespace}})
	if !ok {
		return nil, false
	}
	return append(append(append(policies, svcs...), pods...), ns...), true
}

// previousLabels returns a pod's labels before a label edit, from the event's
// diff. A persisted event decodes the map as map[string]any.
func previousLabels(e timeline.TimelineEvent) map[string]string {
	if e.Diff == nil {
		return nil
	}
	for _, f := range e.Diff.Fields {
		if f.Path != "metadata.labels" {
			continue
		}
		switch old := f.OldValue.(type) {
		case map[string]string:
			return old
		case map[string]any:
			out := make(map[string]string, len(old))
			for k, v := range old {
				if s, ok := v.(string); ok {
					out[k] = s
				}
			}
			return out
		}
	}
	return nil
}

func matchesAny(selectors []labels.Selector, podLabels map[string]string) bool {
	for _, sel := range selectors {
		if sel.Matches(labels.Set(podLabels)) {
			return true
		}
	}
	return false
}
