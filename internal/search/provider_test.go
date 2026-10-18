package search

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestCacheProviderTypedCoverageUsesReadinessAndScope(t *testing.T) {
	cache, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:         fake.NewClientset(),
		ResourceScopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "team-a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Stop)
	p := &CacheProvider{cache: &k8s.ResourceCache{ResourceCache: cache}}
	for _, tc := range []struct {
		kind       string
		namespaces []string
		reason     string
	}{
		{"Pod", []string{"team-a"}, ""},
		{"Pod", nil, "radar_namespace_not_collected"},
		{"Pod", []string{"team-b"}, "radar_namespace_not_collected"},
		{"Pod", []string{"team-a", "team-b"}, "radar_namespace_not_collected"},
		{"Secret", []string{"team-a"}, "radar_kind_not_collected"},
	} {
		if got := p.TypedCoverage(tc.kind, tc.namespaces); string(got) != tc.reason {
			t.Errorf("%s %v: got %q want %q", tc.kind, tc.namespaces, got, tc.reason)
		}
	}
}

// A target read in full is not held back by another namespace of the kind
// that is still loading, and Radar collecting the kind in only part of a
// target is reported apart from readiness.
func TestCacheProviderTypedCoverageJudgesOnlyTheTarget(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "gamma" {
			return true, nil, errors.New("gamma does not answer")
		}
		return false, nil, nil
	})
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:                  client,
		ResourceTypes:           map[string]bool{"configmaps": true},
		ResourceScopes:          map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "alpha"}},
		ResourceScopeNamespaces: map[string][]string{"configmaps": {"alpha", "gamma"}},
		DeferredTypes:           map[string]bool{"configmaps": true},
		DeferredSyncTimeout:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for !core.AssessKindRead("configmaps", []string{"alpha"}).Complete {
		if time.Now().After(deadline) {
			t.Fatal("alpha never synced")
		}
		time.Sleep(10 * time.Millisecond)
	}
	p := &CacheProvider{cache: &k8s.ResourceCache{ResourceCache: core}}
	for _, tc := range []struct {
		namespaces []string
		reason     string
		limited    bool
	}{
		{[]string{"alpha"}, "", false},
		{[]string{"alpha", "gamma"}, "kind_sync_pending", false},
		{[]string{"alpha", "beta"}, "radar_namespace_not_collected", true},
		{[]string{"gamma", "beta"}, "kind_sync_pending", true},
		{nil, "kind_sync_pending", true},
	} {
		if got := p.TypedCoverage("ConfigMap", tc.namespaces); string(got) != tc.reason {
			t.Errorf("%v: coverage %q, want %q", tc.namespaces, got, tc.reason)
		}
		if got := p.TypedCollectionLimited("ConfigMap", tc.namespaces); got != tc.limited {
			t.Errorf("%v: limited %v, want %v", tc.namespaces, got, tc.limited)
		}
	}
}

func TestCacheProviderNilDynamic(t *testing.T) {
	p := &CacheProvider{discovery: &k8s.ResourceDiscovery{}}
	p.PrepareDynamicObservations(nil)
	if _, err := p.DynamicResources(); !errors.Is(err, k8s.ErrDynamicNotReady) {
		t.Fatalf("catalog: %v", err)
	}
	if got := p.DynamicObservation(schema.GroupVersionResource{}, ""); got.State != k8score.DynamicObservationSyncing {
		t.Fatalf("observation: %+v", got)
	}
	if err := p.WarmDynamic(context.Background(), schema.GroupVersionResource{}, ""); !errors.Is(err, k8s.ErrDynamicNotReady) {
		t.Fatalf("warm: %v", err)
	}
}

func TestCacheProviderUnavailableTypedUsesCollectorPermissions(t *testing.T) {
	cache, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(), ResourceScopes: map[string]k8score.ResourceScope{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Stop)
	p := &CacheProvider{cache: &k8s.ResourceCache{ResourceCache: cache}, permissions: &k8s.ResourcePermissions{}}
	for _, tk := range typedKinds {
		if got := p.TypedCoverage(tk.Kind, nil); got != "radar_forbidden" {
			t.Errorf("%s: %q", tk.Kind, got)
		}
	}
	p.permissions.Secrets = true
	if got := p.TypedCoverage("Secret", nil); got != "radar_kind_not_collected" {
		t.Fatalf("allowed but unavailable: %q", got)
	}
}

func TestCacheProviderWarmWaitsForDiscoveryWithoutBlocking(t *testing.T) {
	p := &CacheProvider{dynamic: &k8s.DynamicResourceCache{}}
	start := time.Now()
	err := p.WarmDynamic(context.Background(), schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}, "")
	if !errors.Is(err, k8s.ErrDynamicNotReady) || time.Since(start) > time.Second {
		t.Fatalf("discovery wait: %v, %s", err, time.Since(start))
	}
}

func TestCacheProviderNeverWarmsDirectReadBuiltins(t *testing.T) {
	p := &CacheProvider{dynamic: &k8s.DynamicResourceCache{}}
	for _, gvr := range []schema.GroupVersionResource{
		{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"},
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"},
		{Group: "", Version: "v1", Resource: "endpoints"},
	} {
		if got := p.DynamicObservation(gvr, ""); got.State != k8score.DynamicObservationUnsupported {
			t.Errorf("%s observation: %+v", gvr.Resource, got)
		}
		if err := p.WarmDynamic(context.Background(), gvr, ""); listErrorReason(err) != "kind_not_watchable" {
			t.Errorf("%s warm: %v", gvr.Resource, err)
		}
	}
	crd := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	if got := dynamicObservationReason(p.DynamicObservation(crd, "")); got != "radar_kind_not_collected" {
		t.Fatalf("unwatched CRD: %q", got)
	}
}

// Namespace informers sync independently, and in a shared Radar the one still
// loading may be another user's. It must not hide hits from a namespace whose
// informer is ready.
func TestDynamicReadinessIsJudgedPerNamespace(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	widget := func(ns string) runtime.Object {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": "robusta-" + ns, "namespace": ns}}}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, widget("a"), widget("b"))
	release := make(chan struct{})
	defer close(release)
	dyn.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		switch {
		case action.GetNamespace() == "":
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("cluster-wide list denied"))
		case action.GetNamespace() == "b" && action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions().Limit != 1:
			// b's informer never finishes its initial list.
			<-release
		}
		return false, nil, nil
	})
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	dynamic, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dynamic.Stop)
	completeCRDDiscovery(t, dynamic)
	discovery.AddAPIResource(k8score.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}})
	ctx := context.Background()
	if err := dynamic.EnsureWatchingContext(ctx, gvr, "a"); err != nil {
		t.Fatal(err)
	}
	syncCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if !dynamic.WaitForSyncContext(syncCtx, gvr, "a") {
		t.Fatal("namespace a did not sync")
	}
	if err := dynamic.EnsureWatchingContext(ctx, gvr, "b"); err != nil {
		t.Fatal(err)
	}

	p := &CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dynamic}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: discovery}}
	for _, tc := range []struct {
		namespaces []string
		want       []UnsearchedKind
	}{
		{[]string{"a"}, []UnsearchedKind{}},
		{[]string{"a", "b"}, []UnsearchedKind{{Kind: "Widget", Group: "example.io", Reason: "kind_sync_pending"}}},
	} {
		res, err := Search(ctx, p, Parse("kind:Widget robusta"), Options{Include: IncludeNone, Namespaces: tc.namespaces})
		if err != nil {
			t.Fatal(err)
		}
		if got := hitNames(res); !reflect.DeepEqual(got, []string{"Widget/a/robusta-a"}) {
			t.Errorf("namespaces %v: hits %v, want a's widget while b is still loading", tc.namespaces, got)
		}
		if !reflect.DeepEqual(unsearched(res), tc.want) || partial(res) != (len(tc.want) > 0) {
			t.Errorf("namespaces %v: coverage %+v partial=%v, want %+v", tc.namespaces, unsearched(res), partial(res), tc.want)
		}
	}
}

// completeCRDDiscovery runs CRD discovery while the catalog holds no CRDs, so
// the cache counts as discovered without eagerly watching the test's kinds.
func completeCRDDiscovery(t *testing.T, dynamic *k8score.DynamicResourceCache) {
	t.Helper()
	done := make(chan struct{})
	dynamic.OnCRDDiscoveryComplete(func() { close(done) })
	dynamic.DiscoverAllCRDs()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CRD discovery did not complete")
	}
}

// Until API discovery has answered and CRD discovery completes, kinds Radar is
// about to watch eagerly still look unwatched or absent, so search can't rule
// them out: the catalog reports loading while what is cached keeps answering.
func TestCacheProviderCatalogLoadsUntilCRDDiscoveryCompletes(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	dynamic, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), Discovery: discovery})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dynamic.Stop)
	p := &CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dynamic}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: discovery}}
	if _, err := p.DynamicResources(); !errors.Is(err, k8s.ErrDynamicNotReady) {
		t.Fatalf("empty API catalog: %v", err)
	}
	discovery.AddAPIResource(k8score.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}})
	gvrs, err := p.DynamicResources()
	if !errors.Is(err, k8s.ErrDynamicNotReady) || !slices.Contains(gvrs, gvr) {
		t.Fatalf("before CRD discovery: %v %v", gvrs, err)
	}
	res, _ := Search(context.Background(), p, Parse("kind:Gadget robusta"), Options{})
	if want := []UnsearchedKind{{Kind: "*", Reason: "kind_sync_pending"}}; !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("search before CRD discovery = %+v, want %+v", unsearched(res), want)
	}
	completeCRDDiscovery(t, dynamic)
	res, _ = Search(context.Background(), p, Parse("kind:Gadget robusta"), Options{})
	if want := []UnsearchedKind{{Kind: "Gadget", Reason: "kind_unknown"}}; !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("search after CRD discovery = %+v, want %+v", unsearched(res), want)
	}
}

func TestTypedSearchCoveragePreservesKnownExclusionBeforeSync(t *testing.T) {
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(), ResourceScopes: map[string]k8score.ResourceScope{"services": {Enabled: true}, "pods": {Enabled: true, Namespace: "team-a"}}, DeferredTypes: map[string]bool{"pods": true}, DebugSyncDelays: map[string]time.Duration{"pods": time.Hour}, DeferredSyncTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	provider := &CacheProvider{cache: &k8s.ResourceCache{ResourceCache: core}}
	for _, tc := range []struct {
		namespaces []string
		reason     string
	}{
		{[]string{"team-b"}, "radar_namespace_not_collected"},
		{[]string{"team-a"}, "kind_sync_pending"},
		{[]string{"team-a", "team-b"}, "kind_sync_pending"},
		{nil, "kind_sync_pending"},
	} {
		if got := provider.TypedCoverage("Pod", tc.namespaces); string(got) != tc.reason {
			t.Fatalf("%v: %s, want %s", tc.namespaces, got, tc.reason)
		}
	}
}
