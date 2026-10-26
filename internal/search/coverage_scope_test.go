package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func TestTrivyClusterCheckFailureIsNotADenial(t *testing.T) {
	for _, check := range []string{"failed", "over budget", "denied"} {
		for _, query := range []string{"kind:VulnerabilityReport", "db"} {
			t.Run(check+"/"+query, func(t *testing.T) {
				p := trivyProvider(vulnGVR, "VulnerabilityReport")
				budget := new(atomic.Int32)
				if check != "over budget" {
					budget.Store(readscope.MaxAccessChecks)
				}
				gate := func(ns []string) ([]string, []string, []string) {
					return readscope.ClusterFirstNamespaceAccess(t.Context(), ns, budget, func(string) (bool, bool) { return false, false }, func(string) (bool, bool) { return false, check == "denied" })
				}
				opts := Options{DynamicKindAccess: &DynamicKindAccess{
					ClusterWide: func(string, string) (bool, bool) {
						_, denied, unchecked := gate(nil)
						return len(denied) == 0 && len(unchecked) == 0, len(unchecked) == 0
					},
					InNamespaces: func(_, _ string, ns []string) ([]string, []string) {
						allowed, _, unchecked := gate(ns)
						return allowed, unchecked
					},
				}}
				res, err := Search(t.Context(), p, Parse(query), opts)
				if err != nil {
					t.Fatal(err)
				}
				if check == "denied" {
					if query == "db" {
						if partial(res) || !slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByUserAccess) {
							t.Fatalf("broad denial: %+v", res.Coverage)
						}
					} else if !hasKindGap(res, readreason.UserForbidden, "VulnerabilityReport") {
						t.Fatalf("named denial: %+v", res.Coverage)
					}
				} else {
					if !partial(res) || !hasKindGap(res, readreason.UserPermissionCheckFailed, "VulnerabilityReport") || hasKindGap(res, readreason.UserForbidden, "VulnerabilityReport") {
						t.Fatalf("failed check: %+v", res.Coverage)
					}
					if len(res.Coverage.Gaps) != 1 || res.Coverage.Gaps[0].Namespaces.Extent != resourceapi.ExtentUnknown {
						t.Fatalf("unknown access extent: %+v", res.Coverage)
					}
				}
			})
		}
	}
}

func TestDynamicFailureInEveryNamedNamespaceReachesAll(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationSyncing, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"alpha", "beta"}}}}
	res, err := Search(t.Context(), p, Parse("kind:Widget ns:alpha ns:beta"), Options{Selection: readscope.Selection{Namespaces: []string{"alpha", "beta"}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res.Coverage)
	t.Logf("coverage=%s error=%v", raw, err)
	if len(res.Coverage.Gaps) != 1 || res.Coverage.Gaps[0].Namespaces.Extent != resourceapi.ExtentAll {
		t.Error("every requested namespace is loading; expected extent all")
	}
}

func TestNamespaceGapNamesAllKindsOnlyWhenAllWereMissed(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			reasons := map[string]map[string]string{"ConfigMap": {"alpha": string(readreason.RadarNamespaceNotCollected)}}
			if all {
				reasons["Pod"] = map[string]string{"alpha": string(readreason.RadarNamespaceNotCollected)}
			}
			p := &fakeProvider{typedNamespaceReasons: reasons, typed: map[string][]runtime.Object{"pods": {newPod("alpha", "db", "image", nil)}}}
			res, err := Search(t.Context(), p, Parse("kind:Pod kind:ConfigMap ns:alpha"), Options{Selection: readscope.Selection{Namespaces: []string{"alpha"}}})
			if err != nil {
				t.Fatal(err)
			}
			extent := resourceapi.ExtentUnknown
			hits := 1
			if all {
				extent = resourceapi.ExtentAll
				hits = 0
			}
			if len(res.Coverage.Gaps) != 1 || res.Coverage.Gaps[0].Kinds.Extent != extent || res.Total != hits {
				t.Fatalf("all=%v: %+v", all, res)
			}
		})
	}
}

func TestTypedSearchKeepsReadyNamespaceBesideUncollectedOne(t *testing.T) {
	client := fake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "db"}})
	client.PrependReactor("list", "configmaps", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == "gamma" {
			return true, nil, errors.New("gamma held pending")
		}
		return false, nil, nil
	})
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: client, ResourceTypes: map[string]bool{"configmaps": true}, ResourceScopes: map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "alpha"}}, ResourceScopeNamespaces: map[string][]string{"configmaps": {"alpha", "gamma"}}, DeferredTypes: map[string]bool{"configmaps": true}, DeferredSyncTimeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	deadline := time.Now().Add(3 * time.Second)
	for !core.AssessKindRead("configmaps", []string{"alpha"}).Complete {
		if time.Now().After(deadline) {
			t.Fatal("alpha did not sync")
		}
		time.Sleep(10 * time.Millisecond)
	}
	p := &typedPlanProvider{Provider: &CacheProvider{cache: &k8s.ResourceCache{ResourceCache: core}}}
	res, err := Search(t.Context(), p, Parse("kind:ConfigMap ns:alpha ns:beta db"), Options{Selection: readscope.Selection{Namespaces: []string{"alpha", "beta"}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res)
	t.Logf("result=%s error=%v", raw, err)
	if res.Total != 1 || res.Hits[0].Namespace != "alpha" {
		t.Error("ready alpha hit lost because beta is outside collection and unrelated gamma still loads")
	}
	if hasKindGap(res, readreason.RadarForbidden, "ConfigMap") || len(res.Coverage.Gaps) != 1 || !slices.Equal(res.Coverage.Gaps[0].Namespaces.Names, []string{"beta"}) {
		t.Errorf("coverage must only exclude beta: %+v", res.Coverage)
	}
}

type failingRefreshDiscovery struct {
	discovery.DiscoveryInterface
	fail    bool
	hold    chan struct{}
	started chan struct{}
}

func (d *failingRefreshDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	if d.hold != nil {
		d.started <- struct{}{}
		<-d.hold
	}
	if d.fail {
		return nil, nil, errors.New("discovery unavailable")
	}
	return nil, []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}}}, nil
}
func TestFailedDiscoveryRefreshQualifiesSearch(t *testing.T) {
	source := &failingRefreshDiscovery{DiscoveryInterface: fake.NewClientset().Discovery()}
	d, err := k8score.NewResourceDiscovery(source)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), Discovery: d})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dc.Stop)
	completeCRDDiscovery(t, dc)
	source.fail = true
	if err := d.Refresh(); err == nil {
		t.Fatal("expected discovery failure")
	}
	p := &CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dc}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: d}}
	resources, err := p.DynamicResources()
	t.Logf("LastRefreshFailed=%v resources=%v error=%v", d.LastRefreshFailed(), resources, err)
	if err == nil {
		t.Error("failed discovery refresh must not report a complete catalog")
	}
	for _, query := range []string{"db", "kind:NewKind"} {
		searchProvider := &catalogSearchProvider{fakeProvider: &fakeProvider{}, catalog: p}
		res, searchErr := Search(t.Context(), searchProvider, Parse(query), Options{})
		if searchErr != nil {
			t.Fatal(searchErr)
		}
		if query == "db" {
			if partial(res) || !slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByKindCollection) {
				t.Fatalf("broad retained catalog: %+v", res.Coverage)
			}
		} else {
			if !hasKindGap(res, readreason.KindUnknown, "NewKind") || len(res.Coverage.Gaps) != 2 || !slices.ContainsFunc(res.Coverage.Gaps, func(g resourceapi.Gap) bool {
				return g.Code == readreason.ReadError && g.Kinds.Extent == resourceapi.ExtentUnknown
			}) {
				t.Fatalf("unknown kind after failed refresh: %+v", res.Coverage)
			}
		}
	}

}

type catalogSearchProvider struct {
	*fakeProvider
	catalog     *CacheProvider
	catalogRead chan struct{}
}

func (p *catalogSearchProvider) DynamicResources() ([]schema.GroupVersionResource, error) {
	if p.catalogRead != nil {
		close(p.catalogRead)
	}
	return p.catalog.DynamicResources()
}
func (p *catalogSearchProvider) UnlistableKinds() []k8score.APIResource {
	return p.catalog.UnlistableKinds()
}

func TestSearchDoesNotWaitOnDiscoveryRefresh(t *testing.T) {
	source := &failingRefreshDiscovery{DiscoveryInterface: fake.NewClientset().Discovery()}
	d, err := k8score.NewResourceDiscovery(source, k8score.WithDiscoveryCacheTTL(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	dc, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), Discovery: d})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dc.Stop)
	completeCRDDiscovery(t, dc)
	source.hold = make(chan struct{})
	source.started = make(chan struct{}, 1)
	p := &catalogSearchProvider{fakeProvider: &fakeProvider{}, catalog: &CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dc}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: d}}}
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- d.Refresh() }()
	<-source.started
	defer func() { close(source.hold); <-refreshDone }()
	p.catalogRead = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Search(ctx, p, Parse("kind:NewKind"), Options{}); done <- err }()
	<-p.catalogRead
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("search: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Search waits for blocked discovery despite cancellation")
	}
}

func TestSearchCatalogUsesStableVersionsFromOneSnapshot(t *testing.T) {
	source := &failingRefreshDiscovery{DiscoveryInterface: fake.NewClientset().Discovery()}
	d, err := k8score.NewResourceDiscovery(source)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), Discovery: d})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dc.Stop)
	completeCRDDiscovery(t, dc)
	for _, ar := range []k8score.APIResource{
		{Group: "example.io", Version: "v1beta1", Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"create"}},
		{Group: "example.io", Version: "v1", Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"list", "watch"}},
		{Group: "example.io", Version: "v1", Name: "reviews", Kind: "Review", Verbs: []string{"create"}},
	} {
		d.AddAPIResource(ar)
	}
	p := &CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dc}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: d}}
	gvrs, err := p.DynamicResources()
	want := []schema.GroupVersionResource{{Version: "v1", Resource: "pods"}, {Group: "example.io", Version: "v1", Resource: "widgets"}}
	if err != nil || !slices.Equal(gvrs, want) {
		t.Fatalf("catalog: %v %v", gvrs, err)
	}
	d.AddAPIResource(k8score.APIResource{Group: "example.io", Version: "v1", Name: "laterreviews", Kind: "LaterReview", Verbs: []string{"create"}})
	unlistable := p.UnlistableKinds()
	if len(unlistable) != 1 || unlistable[0].Kind != "Review" {
		t.Fatalf("unlistable kinds must use the same stable-version snapshot: %+v", unlistable)
	}
}

func TestDynamicNamespaceListFailureExtents(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, whole := range []bool{false, true} {
		t.Run(fmt.Sprint(whole), func(t *testing.T) {
			failures := map[string]error{"beta": errors.New("list failed")}
			if whole {
				failures["alpha"] = errors.New("list failed")
			}
			p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}, dynamicErrors: failures, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"alpha", "beta"}}}}
			res, err := Search(t.Context(), p, Parse("kind:Widget ns:alpha ns:beta"), Options{Selection: readscope.Selection{Namespaces: []string{"alpha", "beta"}}})
			want := resourceapi.ExtentUnknown
			if whole {
				want = resourceapi.ExtentAll
			}
			if err != nil || len(res.Coverage.Gaps) != 1 || res.Coverage.Gaps[0].Code != readreason.ReadError || res.Coverage.Gaps[0].Namespaces.Extent != want {
				t.Fatalf("whole=%v: %+v %v", whole, res.Coverage, err)
			}
		})
	}
}
