package search

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

type cancelSearchProvider struct {
	*fakeProvider
	cancel context.CancelFunc
	stage  string
	lists  int
}

func (p *cancelSearchProvider) ListTyped(resource string, namespaces []string) ([]runtime.Object, error) {
	if p.stage == "typed" {
		p.lists++
		p.cancel()
	}
	return p.fakeProvider.ListTyped(resource, namespaces)
}
func (p *cancelSearchProvider) NamespaceObservation(gvr schema.GroupVersionResource, ns string) k8score.DynamicResourceObservation {
	if p.stage == "kind" {
		p.lists++
		p.cancel()
	}
	return p.fakeProvider.NamespaceObservation(gvr, ns)
}
func (p *cancelSearchProvider) ListDynamic(ctx context.Context, gvr schema.GroupVersionResource, ns string) ([]*unstructured.Unstructured, error) {
	if p.stage == "namespace" {
		p.lists++
		p.cancel()
	}
	return p.fakeProvider.ListDynamic(ctx, gvr, ns)
}

func TestSearchStopsOnCancellation(t *testing.T) {
	for _, stage := range []string{"before", "typed", "kind", "namespace"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &cancelSearchProvider{fakeProvider: &fakeProvider{kinds: map[schema.GroupVersionResource]string{}}, cancel: cancel, stage: stage}
			for i := range 500 {
				p.kinds[schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: fmt.Sprintf("widgets-%03d", i)}] = "Widget"
			}
			namespaces := make([]string, 5000)
			for i := range namespaces {
				namespaces[i] = fmt.Sprintf("ns-%05d", i)
			}
			if stage == "before" {
				cancel()
			}
			_, err := Search(ctx, p, Parse("test"), Options{Selection: readscope.Selection{Namespaces: namespaces}})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			if p.lists > 1 {
				t.Fatalf("continued work after cancellation: %d calls", p.lists)
			}
		})
	}
}

func TestCoverageDeduplicatesByIdentityAndReason(t *testing.T) {
	c := newCoverage(Parse("kind:Widget"))
	for range 5000 {
		c.kind("Widget", "a.io", readreason.ReadError)
	}
	c.kind("Widget", "b.io", readreason.ReadError)
	c.kind("Widget", "a.io", readreason.KindSyncTimedOut)
	c.catalog(readreason.ReadError)
	c.catalog(readreason.ReadError)
	got := c.result()
	// read_error over both Widgets, kind_sync_timed_out over one, and
	// read_error over an unknown set of kinds: one gap per reason and axis.
	if len(got) != 3 || got[0].Code != readreason.KindSyncTimedOut || got[1].Kinds == nil || got[1].Kinds.Count != 2 || got[2].Kinds == nil || got[2].Kinds.Extent != resourceapi.ExtentUnknown {
		t.Fatalf("gaps = %+v", got)
	}
}

func BenchmarkCoverageScale(b *testing.B) {
	kinds := make([]string, 500)
	for i := range kinds {
		kinds[i] = fmt.Sprintf("Widget%03d", i)
	}
	b.ResetTimer()
	for range b.N {
		c := newCoverage(Parse("test"))
		for _, kind := range kinds {
			for range 5000 {
				c.kind(kind, "example.io", readreason.ReadError)
			}
		}
		c.result()
	}
}

type clusterObservationProvider struct {
	*fakeProvider
	observations, reads, plans int
}

func (p *clusterObservationProvider) PlanDynamic(gvr schema.GroupVersionResource, namespaces []string, clusterScoped bool) readplan.Plan {
	p.plans++
	return p.fakeProvider.PlanDynamic(gvr, namespaces, clusterScoped)
}

func (p *clusterObservationProvider) NamespaceObservation(gvr schema.GroupVersionResource, ns string) k8score.DynamicResourceObservation {
	p.observations++
	return p.fakeProvider.NamespaceObservation(gvr, ns)
}
func (p *clusterObservationProvider) ListDynamic(context.Context, schema.GroupVersionResource, string) ([]*unstructured.Unstructured, error) {
	p.reads++
	return nil, nil
}

func TestSearchReusesSyncedClusterWideObservation(t *testing.T) {
	p := &clusterObservationProvider{fakeProvider: &fakeProvider{kinds: map[schema.GroupVersionResource]string{}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{}}}
	for i := range 500 {
		gvr := schema.GroupVersionResource{Group: "scale.example.io", Version: "v1", Resource: fmt.Sprintf("widgets-%03d", i)}
		p.kinds[gvr] = "Widget"
		p.fakeProvider.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeCluster}
	}
	namespaces := make([]string, 5000)
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("ns-%05d", i)
	}
	_, err := Search(context.Background(), p, Parse("test"), Options{Selection: readscope.Selection{Namespaces: namespaces}})
	if err != nil {
		t.Fatal(err)
	}
	if p.observations != 500 || p.reads != 500*5000 {
		t.Fatalf("observations=%d reads=%d", p.observations, p.reads)
	}
}

func TestSearchPlansNamespaceFanoutOnce(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	namespaces := make([]string, 5000)
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("ns-%05d", i)
	}
	p := &clusterObservationProvider{fakeProvider: &fakeProvider{
		kinds:      map[schema.GroupVersionResource]string{gvr: "Widget"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: true},
		observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {
			State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: namespaces,
		}},
	}}
	res, err := Search(t.Context(), p, Parse("test"), Options{Selection: readscope.Selection{Namespaces: namespaces}})
	if err != nil || partial(res) || p.plans != 1 || p.reads != len(namespaces) || len(p.warmed) != 0 {
		t.Fatalf("plans=%d reads=%d warm=%d result=%+v err=%v", p.plans, p.reads, len(p.warmed), res, err)
	}
}

type stopSearchProvider struct {
	*probeRetryProvider
	reads []string
}

func (p *stopSearchProvider) ListDynamic(ctx context.Context, gvr schema.GroupVersionResource, ns string) ([]*unstructured.Unstructured, error) {
	p.reads = append(p.reads, ns)
	items, err := p.CacheProvider.ListDynamic(ctx, gvr, ns)
	if ns == "a" {
		p.dynamic.Stop()
	}
	return items, err
}

func TestSearchClusterWideCacheStopsBetweenNamespaces(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	objects := []runtime.Object{}
	for _, ns := range []string{"a", "b"} {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.io/v1", "kind": "Widget",
			"metadata": map[string]any{"name": "robusta-" + ns, "namespace": ns},
		}})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, objects...)
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	dc, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dc.Stop)
	completeCRDDiscovery(t, dc)
	discovery.AddAPIResource(k8score.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}})
	if err := dc.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(gvr, time.Second) {
		t.Fatal("cluster-wide informer did not sync")
	}
	if o := dc.Observation(gvr); o.Scope != k8score.DynamicObservationScopeCluster || o.State != k8score.DynamicObservationSynced {
		t.Fatalf("observation = %+v", o)
	}
	p := &stopSearchProvider{probeRetryProvider: &probeRetryProvider{&CacheProvider{
		dynamic:   &k8s.DynamicResourceCache{DynamicResourceCache: dc},
		discovery: &k8s.ResourceDiscovery{ResourceDiscovery: discovery},
	}}}
	// A cache stopped mid-search belongs to a connection Radar left: the
	// search fails rather than answering with part of it.
	_, err = Search(t.Context(), p, Parse("kind:Widget robusta"), Options{Selection: readscope.Selection{Namespaces: []string{"a", "b"}}, Include: IncludeNone})
	if !errors.Is(err, ErrConnectionChanged) {
		t.Fatalf("err = %v, want ErrConnectionChanged", err)
	}
	if !slices.Equal(p.reads, []string{"a"}) {
		t.Fatalf("namespace reads = %v, want [a]", p.reads)
	}
}

func TestSearchClusterWideProbeGapRemainsNamespaceSpecific(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &clusterObservationProvider{fakeProvider: &fakeProvider{
		kinds:        map[schema.GroupVersionResource]string{gvr: "Widget"},
		observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeCluster, UnansweredNamespaces: []string{"b"}, ProbeRetrying: true}},
		namespaceObservations: map[schema.GroupVersionResource]map[string]k8score.DynamicResourceObservation{gvr: {
			"a": {State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeCluster},
			"b": {State: k8score.DynamicObservationDeferred, Scope: k8score.DynamicObservationScopeCluster, ReasonCode: "scope_probe_incomplete", UnansweredNamespaces: []string{"b"}, ProbeRetrying: true},
		}},
	}}
	got, err := Search(context.Background(), p, Parse("test"), Options{Selection: readscope.Selection{Namespaces: []string{"a", "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p.observations != 3 || p.reads != 1 || !partial(got) {
		t.Fatalf("observations=%d reads=%d result=%+v", p.observations, p.reads, got)
	}
}

func TestCoverageNamespaceDeduplicationPreservesSort(t *testing.T) {
	c := newCoverage(Parse("ns:z ns:a"))
	for range 5000 {
		c.namespaces(readreason.RadarNamespaceNotCollected, "z", "a", "ambient")
	}
	got := c.result()
	if len(got) != 1 || got[0].Namespaces == nil || got[0].Namespaces.Count != 2 || got[0].Namespaces.Names[0] != "a" || got[0].Namespaces.Names[1] != "z" {
		t.Fatalf("gaps=%+v", got)
	}
}

func TestSearchUnknownKindDeduplicatesCase(t *testing.T) {
	got, err := Search(context.Background(), &fakeProvider{}, Parse("kind:Widget kind:widget"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(unsearched(got)) != 1 || unsearched(got)[0].Reason != readreason.KindUnknown {
		t.Fatalf("gaps=%+v", unsearched(got))
	}
}
