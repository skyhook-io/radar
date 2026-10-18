package search

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type probeRetryProvider struct{ *CacheProvider }

func (*probeRetryProvider) TypedCoverage(string, []string) readreason.Code { return "" }

func TestSearchProbeRetryCoverage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		observation k8score.DynamicResourceObservation
		reason      readreason.Code
	}{
		{"retrying", k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced, ProbeRetrying: true, UnansweredNamespaces: []string{"b"}}, readreason.KindSyncPending},
		{"exhausted", k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced, UnansweredNamespaces: []string{"b"}}, readreason.ReadError},
		{"whole exhausted", k8score.DynamicResourceObservation{State: k8score.DynamicObservationDeferred, ReasonCode: "scope_probe_incomplete"}, readreason.ReadError},
		{"cap only", k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced, Truncated: true, ReasonCode: "namespace_fanout_truncated"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newCapProvider()
			capFixture(p, "Widget", 8, tc.observation)
			res, err := Search(context.Background(), p, Parse("robusta"), Options{})
			if err != nil {
				t.Fatal(err)
			}
			want := []UnsearchedKind{}
			if tc.reason == readreason.KindSyncPending {
				want = []UnsearchedKind{{Kind: "*", Reason: tc.reason, Count: 8}}
			}
			if tc.reason == readreason.ReadError {
				want = []UnsearchedKind{{Kind: "*", Reason: tc.reason, Count: 8}}
			}
			if !reflect.DeepEqual(unsearched(res), want) || partial(res) != (len(want) > 0) || len(p.warmed) > 0 {
				t.Fatalf("broad: %+v warm=%v want=%+v", res, p.warmed, want)
			}
			res, err = Search(context.Background(), p, Parse("kind:Widget07 robusta"), Options{})
			if err != nil {
				t.Fatal(err)
			}
			want = []UnsearchedKind{}
			if tc.reason != "" {
				want = capNamed("Widget", 8, 7, 7, tc.reason)
			}
			wantWarm := 0
			if tc.reason == readreason.ReadError {
				want, wantWarm = []UnsearchedKind{}, 1
			}
			if !reflect.DeepEqual(unsearched(res), want) || len(p.warmed) != wantWarm {
				t.Fatalf("named: %+v warm=%v want=%+v wantWarm=%d", res, p.warmed, want, wantWarm)
			}
			res, _ = Search(context.Background(), p, Parse("robusta"), Options{CanReadClusterScoped: func(string, string, string) (bool, bool) { return false, true }})
			if partial(res) {
				t.Fatalf("denied caller inherited gap: %+v", res)
			}
		})
	}
}

func TestSearchProbeRetryKeepsVisibleHitsAndRecovers(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	widget := func(ns string) runtime.Object {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": "robusta-" + ns, "namespace": ns}}}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, widget("a"), widget("b"))
	refused := 2
	dyn.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("collector scope"))
		}
		if action.GetNamespace() == "b" && refused > 0 {
			refused--
			return true, nil, fmt.Errorf("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
		}
		return false, nil, nil
	})
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	dc, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery, NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{500 * time.Millisecond}})
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
		t.Fatal("a did not sync")
	}
	skipTyped := map[string]bool{}
	for _, kind := range typedKinds {
		skipTyped[kind.Kind] = true
	}
	p := &probeRetryProvider{&CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dc}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: discovery}}}
	for _, tc := range []struct {
		query string
		ns    []string
		want  []UnsearchedKind
	}{
		{"robusta", nil, []UnsearchedKind{{Kind: "*", Reason: readreason.KindSyncPending, Count: 1}}},
		{"kind:Widget robusta", nil, []UnsearchedKind{{Kind: "Widget", Group: gvr.Group, Reason: readreason.KindSyncPending}}},
		{"robusta", []string{"a"}, []UnsearchedKind{}},
	} {
		res, err := Search(t.Context(), p, Parse(tc.query), Options{Namespaces: tc.ns, Include: IncludeNone, SkipKinds: skipTyped})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(unsearched(res), tc.want) || !reflect.DeepEqual(hitNames(res), []string{"Widget/a/robusta-a"}) {
			t.Fatalf("during retry %s ns=%v: %+v", tc.query, tc.ns, res)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for dc.Observation(gvr).ProbeRetrying && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if dc.Observation(gvr).ProbeRetrying || !dc.WaitForSync(gvr, time.Second) {
		t.Fatal("retry did not recover")
	}
	res, err := Search(t.Context(), p, Parse("robusta"), Options{Include: IncludeNone, SkipKinds: skipTyped})
	if err != nil {
		t.Fatal(err)
	}
	if partial(res) || !reflect.DeepEqual(hitNames(res), []string{"Widget/a/robusta-a", "Widget/b/robusta-b"}) {
		t.Fatalf("answered kind incomplete: %+v", res)
	}
}

func TestSearchProbeRetryForbiddenBecomesCollectorScope(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := newCapProvider()
	p.kinds[gvr] = "Widget"
	p.namespaced[gvr] = true
	p.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationDenied, Scope: k8score.DynamicObservationScopeExplicitNamespaces}
	for _, tc := range []struct {
		query string
		ns    []string
		want  []UnsearchedKind
	}{
		{"robusta", nil, []UnsearchedKind{}},
		{"kind:Widget robusta", nil, []UnsearchedKind{}},
		{"ns:b robusta", []string{"b"}, []UnsearchedKind{{Kind: "*", Reason: readreason.RadarNamespaceNotCollected, Namespaces: []string{"b"}}}},
	} {
		p.warmError = apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("collector scope"))
		res, err := Search(t.Context(), p, Parse(tc.query), Options{Namespaces: tc.ns})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(unsearched(res), tc.want) || partial(res) != (len(tc.want) > 0) {
			t.Fatalf("%s: %+v want %+v", tc.query, res, tc.want)
		}
	}
}

func TestSearchExhaustedNamedKindRecovers(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	var allowed atomic.Bool
	var lists atomic.Int32
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": "robusta"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, obj)
	dyn.PrependReactor("list", "widgets", func(ktesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		if !allowed.Load() {
			return true, nil, fmt.Errorf("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
		}
		return false, nil, nil
	})
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	dc, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dc.Stop)
	completeCRDDiscovery(t, dc)
	discovery.AddAPIResource(k8score.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: false, Verbs: []string{"get", "list", "watch"}})
	if err := dc.EnsureWatching(gvr); !k8score.IsClientRateLimited(err) {
		t.Fatalf("initial walk: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for dc.Observation(gvr).ProbeRetrying && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if o := dc.Observation(gvr); o.ProbeRetrying || o.ReasonCode != "scope_probe_incomplete" {
		t.Fatalf("retry did not exhaust: %+v", o)
	}
	p := &probeRetryProvider{&CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dc}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: discovery}}}
	skipTyped := map[string]bool{}
	for _, kind := range typedKinds {
		skipTyped[kind.Kind] = true
	}
	opts := Options{Include: IncludeNone, SkipKinds: skipTyped}
	before := lists.Load()
	allowed.Store(true)
	res, err := Search(t.Context(), p, Parse("robusta"), opts)
	if err != nil || !partial(res) || lists.Load() != before || !reflect.DeepEqual(unsearched(res), []UnsearchedKind{{Kind: "Widget", Group: gvr.Group, Reason: readreason.ReadError}}) {
		t.Fatalf("broad search warmed or lost the exhausted error: %+v err=%v lists=%d", res, err, lists.Load())
	}
	res, err = Search(t.Context(), p, Parse("kind:Widget robusta"), opts)
	if err != nil || partial(res) || !reflect.DeepEqual(hitNames(res), []string{"Widget//robusta"}) || dc.Observation(gvr).ProbeRetrying {
		t.Fatalf("named search did not warm and recover: %+v err=%v", res, err)
	}
}

func TestSearchExhaustedNamedKindSyncTimeoutRefreshesCoverage(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	objects := []runtime.Object{}
	for _, ns := range []string{"a", "b"} {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": "robusta-" + ns, "namespace": ns}}})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, objects...)
	var bAllowed atomic.Bool
	releaseSync := make(chan struct{})
	var releaseOnce sync.Once
	dyn.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("collector scope"))
		}
		if action.GetNamespace() == "b" {
			if !bAllowed.Load() {
				return true, nil, fmt.Errorf("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
			}
			if action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions().Limit != 1 {
				<-releaseSync
			}
		}
		return false, nil, nil
	})
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	dc, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery, NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseSync) })
		dc.Stop()
	})
	completeCRDDiscovery(t, dc)
	discovery.AddAPIResource(k8score.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}})
	if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, time.Second) {
		t.Fatalf("initial fallback sync: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for dc.Observation(gvr).ProbeRetrying && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if o := dc.Observation(gvr); o.ProbeRetrying || !reflect.DeepEqual(o.UnansweredNamespaces, []string{"b"}) {
		t.Fatalf("retry did not exhaust: %+v", o)
	}
	p := &probeRetryProvider{&CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dc}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: discovery}}}
	skipTyped := map[string]bool{}
	for _, kind := range typedKinds {
		skipTyped[kind.Kind] = true
	}
	opts := Options{Include: IncludeNone, SkipKinds: skipTyped}
	bAllowed.Store(true)
	res, err := Search(t.Context(), p, Parse("kind:Widget robusta"), opts)
	if o := dc.Observation(gvr); !dc.HasCoveringInformer(gvr, "b") || o.State != k8score.DynamicObservationSyncing || len(o.UnansweredNamespaces) != 0 {
		t.Fatalf("access did not recover before sync timeout: %+v", o)
	}
	want := []UnsearchedKind{{Kind: "Widget", Group: gvr.Group, Reason: readreason.KindSyncPending}}
	if err != nil || !partial(res) || !reflect.DeepEqual(unsearched(res), want) || !reflect.DeepEqual(hitNames(res), []string{"Widget/a/robusta-a"}) {
		t.Fatalf("sync timeout retained stale coverage or lost synced hits: %+v err=%v", res, err)
	}
	releaseOnce.Do(func() { close(releaseSync) })
	if !dc.WaitForSync(gvr, time.Second) {
		t.Fatal("recovered informer did not sync")
	}
	res, err = Search(t.Context(), p, Parse("kind:Widget robusta"), opts)
	if err != nil || partial(res) || !reflect.DeepEqual(hitNames(res), []string{"Widget/a/robusta-a", "Widget/b/robusta-b"}) {
		t.Fatalf("synced recovery retained a gap: %+v err=%v", res, err)
	}
}

func TestSearchExplicitNamespaceRetryScope(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	objects := []runtime.Object{}
	for _, ns := range []string{"a", "b"} {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": "robusta-" + ns, "namespace": ns}}})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, objects...)
	var bRefused = true
	var aAllowed atomic.Bool
	dyn.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("collector scope"))
		}
		if action.GetNamespace() == "b" && bRefused {
			bRefused = false
			return true, nil, fmt.Errorf("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
		}
		if action.GetNamespace() == "a" && !aAllowed.Load() {
			return true, nil, fmt.Errorf("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
		}
		return false, nil, nil
	})
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	dc, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery, NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{time.Millisecond, 300 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dc.Stop)
	completeCRDDiscovery(t, dc)
	discovery.AddAPIResource(k8score.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}})
	if err := dc.EnsureWatchingContext(t.Context(), gvr, "b"); !k8score.IsClientRateLimited(err) {
		t.Fatalf("initial explicit read: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for !dc.HasCoveringInformer(gvr, "b") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !dc.HasCoveringInformer(gvr, "b") || !dc.WaitForSync(gvr, time.Second) {
		t.Fatal("b did not recover")
	}
	p := &probeRetryProvider{&CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dc}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: discovery}}}
	skipTyped := map[string]bool{}
	for _, kind := range typedKinds {
		skipTyped[kind.Kind] = true
	}
	opts := Options{Include: IncludeNone, SkipKinds: skipTyped}
	res, err := Search(t.Context(), p, Parse("robusta"), opts)
	if err != nil || !partial(res) || !reflect.DeepEqual(hitNames(res), []string{"Widget/b/robusta-b"}) {
		t.Fatalf("scope settled while a remained unwatched: %+v err=%v", res, err)
	}
	aAllowed.Store(true)
	deadline = time.Now().Add(time.Second)
	for dc.Observation(gvr).ProbeRetrying && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !dc.HasCoveringInformer(gvr, "a") || !dc.WaitForSync(gvr, time.Second) {
		t.Fatal("a did not eventually recover")
	}
	res, err = Search(t.Context(), p, Parse("robusta"), opts)
	if err != nil || partial(res) || !reflect.DeepEqual(hitNames(res), []string{"Widget/a/robusta-a", "Widget/b/robusta-b"}) {
		t.Fatalf("full scope did not recover: %+v err=%v", res, err)
	}
}
