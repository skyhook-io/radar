package k8score

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestDynamicResourceObservationUnwatchedAndUnsupportedHaveNoOrigin(t *testing.T) {
	watchable := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	unsupported := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "summaries"}
	discovery := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
	}
	discovery.AddAPIResource(APIResource{
		Group: watchable.Group, Version: watchable.Version, Kind: "Widget", Name: watchable.Resource,
		IsCRD: true, Verbs: []string{"get", "list", "watch"},
	})
	discovery.AddAPIResource(APIResource{
		Group: unsupported.Group, Version: unsupported.Version, Kind: "Summary", Name: unsupported.Resource,
		IsCRD: true, Verbs: []string{"get", "list"},
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		watchable: "WidgetList",
	})
	cache, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache: %v", err)
	}
	t.Cleanup(cache.Stop)

	for _, test := range []struct {
		name   string
		gvr    schema.GroupVersionResource
		state  DynamicObservationState
		reason string
	}{
		{name: "unwatched", gvr: watchable, state: DynamicObservationUnwatched, reason: "not_observed"},
		{name: "unsupported", gvr: unsupported, state: DynamicObservationUnsupported, reason: "list_watch_unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := cache.Observation(test.gvr)
			if got.State != test.state || got.ReasonCode != test.reason {
				t.Fatalf("observation = %+v, want state=%s reason=%s", got, test.state, test.reason)
			}
			if got.Origin != "" || got.ObservationStart != nil {
				t.Fatalf("unstarted observation assigned causal watch metadata: %+v", got)
			}
		})
	}
	if err := cache.EnsureWatching(unsupported); err == nil {
		t.Fatal("EnsureWatching unsupported GVR succeeded")
	}
	discovery.AddAPIResource(APIResource{
		Group: unsupported.Group, Version: unsupported.Version, Kind: "Summary", Name: unsupported.Resource,
		IsCRD: true, Verbs: []string{"get", "list", "watch"},
	})
	if got := cache.Observation(unsupported); got.State != DynamicObservationUnwatched || got.ReasonCode != "not_observed" {
		t.Fatalf("refreshed watchable GVR retained unsupported state: %+v", got)
	}
	if actions := dyn.Actions(); len(actions) != 0 {
		t.Fatalf("read-only observations touched the API server: %v", actions)
	}
}

func TestDynamicResourceObservationTracksOnDemandSyncAndExactGVR(t *testing.T) {
	observed := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	colliding := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "gateways"}
	discovery := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
	}
	for _, gvr := range []schema.GroupVersionResource{observed, colliding} {
		discovery.AddAPIResource(APIResource{
			Group: gvr.Group, Version: gvr.Version, Kind: "Gateway", Name: gvr.Resource,
			Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
		})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		observed:  "GatewayList",
		colliding: "GatewayList",
	})
	releaseSync := make(chan struct{})
	var observedLists atomic.Int32
	dyn.PrependReactor("list", "gateways", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetResource() != observed {
			return false, nil, nil
		}
		if observedLists.Add(1) > 1 {
			<-releaseSync
		}
		return false, nil, nil
	})
	cache, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache: %v", err)
	}
	t.Cleanup(cache.Stop)

	if err := cache.EnsureWatching(observed); err != nil {
		t.Fatalf("EnsureWatching: %v", err)
	}
	syncing := cache.Observation(observed)
	if syncing.State != DynamicObservationSyncing || syncing.Origin != DynamicObservationOriginOnDemand || syncing.ObservationStart == nil {
		t.Fatalf("syncing observation = %+v", syncing)
	}
	if syncing.Scope != DynamicObservationScopeCluster || syncing.ReasonCode != "initial_sync" {
		t.Fatalf("syncing scope/reason = %+v", syncing)
	}
	if got := cache.Observation(colliding); got.State != DynamicObservationUnwatched {
		t.Fatalf("same-named GVR inherited observation from another group: %+v", got)
	}

	close(releaseSync)
	if !cache.WaitForSync(observed, 3*time.Second) {
		t.Fatal("on-demand informer did not sync")
	}
	watched := cache.Observation(observed)
	if watched.State != DynamicObservationWatched || watched.ReasonCode != "on_demand_since" {
		t.Fatalf("watched observation = %+v", watched)
	}
	if !watched.ObservationStart.Equal(*syncing.ObservationStart) {
		t.Fatalf("observation start changed across initial sync: before=%v after=%v", syncing.ObservationStart, watched.ObservationStart)
	}
}

func TestDynamicResourceObservationRetainsDeniedAndDeferredOutcomes(t *testing.T) {
	deniedGVR := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "deniedwidgets"}
	deniedDiscovery := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
		lastRefresh: time.Now(),
		cacheTTL:    time.Hour,
	}
	deniedDiscovery.AddAPIResource(APIResource{
		Group: deniedGVR.Group, Version: deniedGVR.Version, Kind: "DeniedWidget", Name: deniedGVR.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	})
	deniedClient := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{deniedGVR: "DeniedWidgetList"}, func(schema.GroupVersionResource, string) bool {
		return false
	})
	deniedCache, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: deniedClient, Discovery: deniedDiscovery})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache denied: %v", err)
	}
	t.Cleanup(deniedCache.Stop)
	if err := deniedCache.EnsureWatching(deniedGVR); err == nil {
		t.Fatal("EnsureWatching denied GVR succeeded")
	}
	denied := deniedCache.Observation(deniedGVR)
	if denied.State != DynamicObservationDenied || denied.ReasonCode != "access_denied" || denied.Origin != "" {
		t.Fatalf("denied observation = %+v", denied)
	}

	truncatedClient := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{deniedGVR: "DeniedWidgetList"}, func(schema.GroupVersionResource, string) bool {
		return false
	})
	truncatedCache, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:               truncatedClient,
		Discovery:                   deniedDiscovery,
		NamespaceFallbacks:          []string{"team-a"},
		NamespaceFallbacksTruncated: true,
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache truncated denial: %v", err)
	}
	t.Cleanup(truncatedCache.Stop)
	if err := truncatedCache.EnsureWatching(deniedGVR); err == nil {
		t.Fatal("EnsureWatching truncated denied GVR succeeded")
	}
	truncatedDenied := truncatedCache.Observation(deniedGVR)
	if truncatedDenied.State != DynamicObservationDenied || !truncatedDenied.Truncated || truncatedDenied.ReasonCode != "access_denied_partial_probe" {
		t.Fatalf("truncated denied observation = %+v", truncatedDenied)
	}

	incompleteCandidatesClient := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{deniedGVR: "DeniedWidgetList"}, func(schema.GroupVersionResource, string) bool {
		return false
	})
	incompleteCandidatesCache, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:               incompleteCandidatesClient,
		Discovery:                   deniedDiscovery,
		NamespaceFallbacksTruncated: true,
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache incomplete candidates: %v", err)
	}
	t.Cleanup(incompleteCandidatesCache.Stop)
	if err := incompleteCandidatesCache.EnsureWatching(deniedGVR); err == nil {
		t.Fatal("EnsureWatching incomplete-candidate denied GVR succeeded")
	}
	incompleteDenied := incompleteCandidatesCache.Observation(deniedGVR)
	if incompleteDenied.State != DynamicObservationDenied || !incompleteDenied.Truncated || incompleteDenied.ReasonCode != "access_denied_partial_probe" {
		t.Fatalf("incomplete-candidate denied observation = %+v", incompleteDenied)
	}

	clusterScopedGVR := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "clustersummaries"}
	clusterScopedDiscovery := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
		lastRefresh: time.Now(),
		cacheTTL:    time.Hour,
	}
	clusterScopedDiscovery.AddAPIResource(APIResource{
		Group: clusterScopedGVR.Group, Version: clusterScopedGVR.Version, Kind: "ClusterSummary", Name: clusterScopedGVR.Resource,
		IsCRD: true, Verbs: []string{"get", "list", "watch"},
	})
	clusterScopedClient := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{clusterScopedGVR: "ClusterSummaryList"}, func(schema.GroupVersionResource, string) bool {
		return false
	})
	clusterScopedCache, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:               clusterScopedClient,
		Discovery:                   clusterScopedDiscovery,
		NamespaceFallbacks:          []string{"team-a"},
		NamespaceFallbacksTruncated: true,
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache cluster-scoped denial: %v", err)
	}
	t.Cleanup(clusterScopedCache.Stop)
	if err := clusterScopedCache.EnsureWatching(clusterScopedGVR); err == nil {
		t.Fatal("EnsureWatching denied cluster-scoped GVR succeeded")
	}
	clusterScopedDenied := clusterScopedCache.Observation(clusterScopedGVR)
	if clusterScopedDenied.State != DynamicObservationDenied || clusterScopedDenied.Truncated || clusterScopedDenied.ReasonCode != "access_denied" {
		t.Fatalf("cluster-scoped denied observation = %+v", clusterScopedDenied)
	}

	for _, test := range []struct {
		name       string
		probeError error
		panicProbe bool
		remaining  int64
		reason     string
	}{
		{name: "probe failed", probeError: errors.New("temporary proxy failure"), reason: "resource_count_probe_failed"},
		{name: "probe panicked", panicProbe: true, reason: "resource_count_probe_failed"},
		{name: "large resource", remaining: 101, reason: "resource_count_exceeds_eager_limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
			discovery := &ResourceDiscovery{
				resources: []APIResource{{
					Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource,
					Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
				}},
				resourceMap: make(map[string]APIResource),
				gvrMap:      make(map[string]schema.GroupVersionResource),
				lastRefresh: time.Now(),
				cacheTTL:    time.Hour,
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
			dyn.PrependReactor("list", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
				if test.panicProbe {
					panic("probe panic")
				}
				if test.probeError != nil {
					return true, nil, test.probeError
				}
				list := &unstructured.UnstructuredList{}
				list.SetRemainingItemCount(&test.remaining)
				return true, list, nil
			})
			cache, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery})
			if err != nil {
				t.Fatalf("NewDynamicResourceCache: %v", err)
			}
			t.Cleanup(cache.Stop)
			cache.DiscoverAllCRDs()
			select {
			case <-cache.discoveryDone:
			case <-time.After(3 * time.Second):
				t.Fatal("DiscoverAllCRDs did not complete")
			}

			got := cache.Observation(gvr)
			if got.State != DynamicObservationDeferred || got.ReasonCode != test.reason {
				t.Fatalf("deferred observation = %+v, want reason=%s", got, test.reason)
			}
			if got.Origin != "" || got.ObservationStart != nil {
				t.Fatalf("deferred observation assigned causal watch metadata: %+v", got)
			}
			if test.name == "large resource" {
				if err := cache.EnsureWatching(gvr); err != nil {
					t.Fatalf("EnsureWatching deferred GVR: %v", err)
				}
				cache.mu.Lock()
				for key, entry := range cache.informers {
					if key.gvr == gvr {
						entry.cancel()
						delete(cache.informers, key)
					}
				}
				cache.mu.Unlock()
				if after := cache.Observation(gvr); after.State != DynamicObservationUnwatched {
					t.Fatalf("successful watch retained stale deferred decision after informer removal: %+v", after)
				}
			}
		})
	}
}

func TestDynamicResourceObservationPreferredNamespaceDenialDoesNotOverwriteGVRState(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	discovery := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
		lastRefresh: time.Now(),
		cacheTTL:    time.Hour,
	}
	discovery.AddAPIResource(APIResource{
		Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	})
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{gvr: "WidgetList"}, func(schema.GroupVersionResource, string) bool {
		return false
	})
	cache, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache: %v", err)
	}
	t.Cleanup(cache.Stop)
	cache.retainObservation(gvr, DynamicObservationDeferred, "prior_global_state", false)

	if err := cache.ensureWatching(gvr, "team-a"); err == nil {
		t.Fatal("ensureWatching denied preferred namespace succeeded")
	}
	got := cache.Observation(gvr)
	if got.State != DynamicObservationDeferred || got.ReasonCode != "prior_global_state" {
		t.Fatalf("preferred-namespace denial overwrote GVR observation: %+v", got)
	}
}

func TestDynamicResourceObservationTracksWarmupEagerAndNamespaceFanout(t *testing.T) {
	t.Run("warmup", func(t *testing.T) {
		gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
		cache, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
		if err != nil {
			t.Fatalf("NewDynamicResourceCache: %v", err)
		}
		t.Cleanup(cache.Stop)
		cache.WarmupParallel([]schema.GroupVersionResource{gvr}, 3*time.Second)
		got := cache.Observation(gvr)
		if got.State != DynamicObservationWatched || got.Origin != DynamicObservationOriginWarmup {
			t.Fatalf("warmup observation = %+v", got)
		}
	})

	t.Run("small eager", func(t *testing.T) {
		gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
		discovery := &ResourceDiscovery{
			resources: []APIResource{{
				Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource,
				Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
			}},
			resourceMap: make(map[string]APIResource),
			gvrMap:      make(map[string]schema.GroupVersionResource),
			lastRefresh: time.Now(),
			cacheTTL:    time.Hour,
		}
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
		cache, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery})
		if err != nil {
			t.Fatalf("NewDynamicResourceCache: %v", err)
		}
		t.Cleanup(cache.Stop)
		cache.DiscoverAllCRDs()
		select {
		case <-cache.discoveryDone:
		case <-time.After(3 * time.Second):
			t.Fatal("DiscoverAllCRDs did not complete")
		}
		got := cache.Observation(gvr)
		if got.State != DynamicObservationWatched || got.Origin != DynamicObservationOriginEager {
			t.Fatalf("small-eager observation = %+v", got)
		}
	})

	t.Run("namespace partial truncated", func(t *testing.T) {
		gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
		dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{gvr: "WidgetList"}, func(_ schema.GroupVersionResource, namespace string) bool {
			return namespace == "team-a" || namespace == "team-b"
		})
		cache, err := NewDynamicResourceCache(DynamicCacheConfig{
			DynamicClient:               dyn,
			NamespaceFallbacks:          []string{"team-b", "team-a"},
			NamespaceFallbacksTruncated: true,
		})
		if err != nil {
			t.Fatalf("NewDynamicResourceCache: %v", err)
		}
		t.Cleanup(cache.Stop)
		if err := cache.EnsureWatching(gvr); err != nil {
			t.Fatalf("EnsureWatching: %v", err)
		}
		if !cache.WaitForSync(gvr, 3*time.Second) {
			t.Fatal("namespace-scoped informers did not sync")
		}
		got := cache.Observation(gvr)
		if got.State != DynamicObservationWatched || got.Scope != DynamicObservationScopeExplicitNamespaces {
			t.Fatalf("namespace observation = %+v", got)
		}
		if !got.NamespacePartial || !got.Truncated || got.ReasonCode != "namespace_fanout_truncated" {
			t.Fatalf("namespace bounds = %+v", got)
		}
		if !reflect.DeepEqual(got.Namespaces, []string{"team-a", "team-b"}) {
			t.Fatalf("namespaces = %v", got.Namespaces)
		}
	})
}
