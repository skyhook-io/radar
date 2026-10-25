package cachesource

import (
	"errors"
	"fmt"
	goruntime "runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func candidateRefs(r ownership.DependentCandidatesRead) []resourceid.Ref {
	var out []resourceid.Ref
	for _, c := range r.Candidates {
		out = append(out, c.Ref)
	}
	return out
}

func hasGap(r ownership.DependentCandidatesRead, want ownership.Gap) bool {
	return slices.Contains(r.Gaps, want)
}

// A resource watched at two versions is searched at the most stable version
// that has loaded, whatever the other holds.
func TestDependentCandidatesReadTheMostStableLoadedVersion(t *testing.T) {
	w1 := ref("example.io", "Widget", "alpha", "w1")
	for _, tc := range []struct {
		name        string
		v1, v1beta1 []runtime.Object
		wantUID     types.UID
	}{
		{name: "both hold it", v1: []runtime.Object{widget("alpha", "w1", "uid-widget")}, v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-widget"))}, wantUID: "uid-widget"},
		{name: "only the less stable one holds it", v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-widget"))}},
		{name: "two incarnations", v1: []runtime.Object{widget("alpha", "w1", "uid-new")}, v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-old"))}, wantUID: "uid-new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := versionsFixture(t, tc.v1, tc.v1beta1, nil)
			s := begin(t, f.source())
			for _, scope := range []ownership.SearchScope{ownership.InNamespace("alpha"), ownership.Everywhere()} {
				got := s.DependentCandidates("uid-deploy", scope)
				var uid types.UID
				for _, c := range got.Candidates {
					if c.Ref == w1 {
						uid = c.UID
					}
				}
				if uid != tc.wantUID || slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == widgetKind }) {
					t.Fatalf("%+v: w1 %q, gaps %+v; want %q and no Widget gap", scope, uid, got.Gaps, tc.wantUID)
				}
			}
		})
	}
}

// scopedVersionsFixture serves Widgets at v1 and v1beta1 from separate
// stores, each watched cluster-wide (nil) or only in the given namespaces.
func scopedVersionsFixture(t *testing.T, v1, v1beta1 []runtime.Object, v1Scope, betaScope []string) *fixture {
	t.Helper()
	beta := schema.GroupVersionResource{Group: "example.io", Version: "v1beta1", Resource: "widgets"}
	scopes := map[schema.GroupVersionResource][]string{widgets: v1Scope, beta: betaScope}
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		f.dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{widgets: "WidgetList", beta: "WidgetList", gadgets: "GadgetList", summaries: "SummaryList"},
			append(v1, v1beta1...)...)
		// A version limited to some namespaces may not be listed
		// cluster-wide, so Radar watches it namespace by namespace.
		f.dynamic.PrependReactor("list", "widgets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			if scopes[a.GetResource()] != nil && a.GetNamespace() == "" {
				return true, nil, apierrors.NewForbidden(a.GetResource().GroupResource(), "", errors.New("namespaces only"))
			}
			return false, nil, nil
		})
	})
	for gvr, scope := range scopes {
		if scope == nil {
			f.watch(t, gvr)
			continue
		}
		for _, ns := range scope {
			if _, err := f.caches.Dynamic.List(gvr, ns); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, fmt.Sprintf("%v in %s", gvr, ns), func() bool { return f.caches.Dynamic.IsNamespaceSynced(gvr, ns) })
		}
	}
	return f
}

// A search of every namespace reads a version watched cluster-wide for every
// namespace; with no cluster-wide watch it reads every version's namespaces.
func TestDependentCandidatesAcrossVersionsWatchedInDifferentScopes(t *testing.T) {
	w1, w9 := ref("example.io", "Widget", "alpha", "w1"), ref("example.io", "Widget", "beta", "w9")
	for _, tc := range []struct {
		name               string
		v1, v1beta1        []runtime.Object
		v1Scope, betaScope []string
		wantFound          []resourceid.Ref
		wantGaps           []ownership.Gap
	}{
		{name: "both hold it", v1: []runtime.Object{widget("alpha", "w1", "uid-widget")}, v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-widget"))},
			betaScope: []string{"alpha"}, wantFound: []resourceid.Ref{w1}},
		{name: "the cluster-wide version answers everywhere", v1: []runtime.Object{widget("beta", "w9", "uid-w9")}, v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-widget"))},
			betaScope: []string{"alpha"}, wantFound: []resourceid.Ref{w9}},
		{name: "no cluster-wide watch", v1: []runtime.Object{widget("alpha", "w1", "uid-widget")}, v1beta1: []runtime.Object{betaWidget(widget("beta", "w9", "uid-w9"))},
			v1Scope: []string{"alpha"}, betaScope: []string{"beta"}, wantFound: []resourceid.Ref{w1, w9},
			wantGaps: []ownership.Gap{{Kind: widgetKind, Reason: readreason.RadarNamespaceNotCollected}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := scopedVersionsFixture(t, tc.v1, tc.v1beta1, tc.v1Scope, tc.betaScope)
			s := begin(t, f.source())
			got := s.DependentCandidates("uid-deploy", ownership.Everywhere())
			var widgetGaps []ownership.Gap
			for _, g := range got.Gaps {
				if g.Kind == widgetKind {
					widgetGaps = append(widgetGaps, g)
				}
			}
			var found []resourceid.Ref
			for _, r := range candidateRefs(got) {
				if r.GroupKind() == widgetKind {
					found = append(found, r)
				}
			}
			if !slices.Equal(found, tc.wantFound) || !slices.Equal(widgetGaps, tc.wantGaps) {
				t.Fatalf("Widgets %v, gaps %+v; want %v, %+v", found, widgetGaps, tc.wantFound, tc.wantGaps)
			}
			if err := s.ValidateCaches(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDependentCandidatesReportAnAmbiguousKind(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		fd := f.typed.Discovery().(*fakediscovery.FakeDiscovery)
		fd.Resources = append(fd.Resources, &metav1.APIResourceList{GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{
			{Name: "morewidgets", Kind: "Widget", Namespaced: true, Verbs: []string{"delete", "get", "list", "watch"}},
		}})
	})
	f.watch(t, widgets)
	s := begin(t, f.source())
	got := s.DependentCandidates("uid-deploy", ownership.Everywhere())
	if !hasGap(got, ownership.Gap{Kind: widgetKind, Reason: readreason.ReadError}) || got.Complete() {
		t.Fatalf("dependents with Widget served by two resources = %+v; want a Widget read_error gap", got)
	}
	if slices.Contains(candidateRefs(got), ref("example.io", "Widget", "alpha", "w1")) {
		t.Fatal("an ambiguous kind's objects can't be named as one resource's")
	}
	if none := s.DependentCandidates("uid-deploy", ownership.SearchScope{}); len(none.Gaps) != 0 {
		t.Fatalf("a search of nothing reports gaps: %+v", none.Gaps)
	}
	if clusterOnly := s.DependentCandidates("uid-deploy", ownership.SearchScope{IncludeClusterScoped: true}); hasGap(clusterOnly, ownership.Gap{Kind: widgetKind, Reason: readreason.ReadError}) {
		t.Fatalf("a cluster-scoped search reports the namespaced Widget: %+v", clusterOnly.Gaps)
	}
}

// Kueue's visibility API serves PendingWorkloadsSummary through two query
// resources nothing can list or watch; it holds no dependents to miss.
func TestDependentCandidatesIgnoreAnAmbiguousKindNothingCollects(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		fd := f.typed.Discovery().(*fakediscovery.FakeDiscovery)
		fd.Resources = append(fd.Resources, &metav1.APIResourceList{GroupVersion: "visibility.kueue.x-k8s.io/v1beta2", APIResources: []metav1.APIResource{
			{Name: "clusterqueues", Kind: "PendingWorkloadsSummary"},
			{Name: "localqueues", Kind: "PendingWorkloadsSummary", Namespaced: true},
		}})
	})
	f.watch(t, widgets)
	s := begin(t, f.source())
	summary := resourceid.GroupKind{Group: "visibility.kueue.x-k8s.io", Kind: "PendingWorkloadsSummary"}
	if !s.Catalog().Ambiguous(summary) {
		t.Fatal("scenario: PendingWorkloadsSummary must be ambiguous")
	}
	for _, scope := range []ownership.SearchScope{ownership.Everywhere(), ownership.InNamespace("alpha"), {IncludeClusterScoped: true}} {
		got := s.DependentCandidates("uid-deploy", scope)
		for _, g := range got.Gaps {
			if g.Kind == summary {
				t.Fatalf("search of %+v reports %+v; nothing can collect a summary", scope, g)
			}
		}
	}
}

// A search while a cluster-wide watch takes over from namespace ones must
// not answer from the unloaded replacement. A session that read the old store
// still validates: only replaced caches fail it.
func TestDependentCandidatesThroughAPromotion(t *testing.T) {
	f, p := newPromotionFixture(t)
	source := f.source()
	before := begin(t, source)
	if got := before.DependentCandidates("uid-deploy", ownership.InNamespace("alpha")); !slices.Contains(candidateRefs(got), ref("example.io", "Widget", "alpha", "w1")) {
		t.Fatalf("from alpha's watch = %+v", got)
	}
	p.run(t, f, true)
	if err := before.ValidateCaches(); err != nil {
		t.Fatalf("ValidateCaches after a watch replacement = %v; only replaced caches fail it", err)
	}
	during := begin(t, source)
	for _, scope := range []ownership.SearchScope{ownership.InNamespace("alpha"), ownership.Everywhere()} {
		got := during.DependentCandidates("uid-deploy", scope)
		if slices.ContainsFunc(got.Candidates, func(c ownership.Observation) bool { return c.Ref.GroupKind() == widgetKind }) ||
			!slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == widgetKind && g.Reason == readreason.KindSyncPending }) {
			t.Fatalf("%+v from the unloaded replacement = %+v; want a Widget kind_sync_pending gap and no Widgets", scope, got)
		}
	}
	p.hold.Store(false)
	if !f.caches.Dynamic.WaitForSync(widgets, 5*time.Second) {
		t.Fatal("the replacement did not sync")
	}
	after := begin(t, source)
	got := after.DependentCandidates("uid-deploy", ownership.Everywhere())
	if !slices.Contains(candidateRefs(got), ref("example.io", "Widget", "alpha", "w1")) ||
		slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == widgetKind }) {
		t.Fatalf("from the loaded replacement = %+v", got)
	}
}

// A namespace watch started mid-session must not widen one answer of the
// session and not another.
func TestDependentCandidatesKeepTheSessionsNamespaceWatches(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		if err := f.dynamic.Tracker().Add(widget("beta", "wb", "uid-wb")); err != nil {
			t.Fatal(err)
		}
		f.dynamic.PrependReactor("list", "widgets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() == "" {
				return true, nil, apierrors.NewForbidden(widgets.GroupResource(), "", errors.New("cluster-wide list denied"))
			}
			return false, nil, nil
		})
	})
	if _, err := f.caches.Dynamic.List(widgets, "alpha"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "alpha's watch", func() bool { return f.caches.Dynamic.IsNamespaceSynced(widgets, "alpha") })
	s := begin(t, f.source())
	first := s.DependentCandidates("uid-deploy", ownership.Everywhere())
	if _, err := f.caches.Dynamic.List(widgets, "beta"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "beta's watch", func() bool { return f.caches.Dynamic.IsNamespaceSynced(widgets, "beta") })
	second := s.DependentCandidates("uid-deploy", ownership.Everywhere())
	if !slices.Equal(candidateRefs(first), candidateRefs(second)) || !slices.Equal(first.Gaps, second.Gaps) {
		t.Fatalf("a watch started mid-session changed the session's answer:\n first  %v %+v\n second %v %+v", candidateRefs(first), first.Gaps, candidateRefs(second), second.Gaps)
	}
	if !hasGap(first, ownership.Gap{Kind: widgetKind, Reason: readreason.RadarNamespaceNotCollected}) {
		t.Fatalf("watched namespace by namespace, the rest must be a gap: %+v", first)
	}
	if fresh := begin(t, f.source()).DependentCandidates("uid-deploy", ownership.Everywhere()); slices.Equal(candidateRefs(fresh), candidateRefs(first)) {
		t.Fatalf("a new session must see the new watch: %v", candidateRefs(fresh))
	}
}

func TestDependentCandidatesDoNoIO(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	f.watch(t, widgets)
	waitForWatches(t, &f.typed.Fake, "pods", "replicasets", "deployments", "nodes", "events")
	waitForWatches(t, &f.dynamic.Fake, "widgets")
	before := len(f.typed.Actions()) + len(f.dynamic.Actions())
	s := begin(t, f.source())
	for _, scope := range []ownership.SearchScope{ownership.Everywhere(), ownership.InNamespace("alpha"), {Namespaces: []string{"alpha", "gamma"}, IncludeClusterScoped: true}} {
		for _, uid := range []types.UID{"uid-deploy", "uid-rs", "uid-node", "uid-none"} {
			s.DependentCandidates(uid, scope)
		}
	}
	if err := s.ValidateCaches(); err != nil {
		t.Fatal(err)
	}
	if after := len(f.typed.Actions()) + len(f.dynamic.Actions()); after != before {
		t.Fatalf("searches made %d API calls; they must make none", after-before)
	}
	if f.caches.Dynamic.HasCoveringInformer(gadgets, "alpha") {
		t.Fatal("a search started a watch")
	}
}

// The search reads the owner index, not the kind: its cost doesn't grow with
// objects that name other owners. Counting allocations can't tell, since
// listing the whole kind is one allocation however many it holds; the bytes
// grow with them.
func TestDependentCandidatesCostDoesNotGrowWithUnrelatedObjects(t *testing.T) {
	bytesPerSearch := func(unrelated int) uint64 {
		objs := []runtime.Object{
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "rs", UID: "uid-rs"}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "owned", UID: "uid-owned", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "uid-rs"}}}},
		}
		for i := range unrelated {
			objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: fmt.Sprint("other-", i), UID: types.UID(fmt.Sprint("uid-other-", i)),
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "x", UID: types.UID(fmt.Sprint("uid-x-", i))}}}})
		}
		client := fake.NewClientset(objs...)
		client.Discovery().(*fakediscovery.FakeDiscovery).Resources = discoveryLists()
		discovery, err := k8score.NewResourceDiscovery(client.Discovery())
		if err != nil {
			t.Fatal(err)
		}
		typed, err := k8score.NewResourceCache(k8score.CacheConfig{Client: client, ResourceTypes: map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true}})
		if err != nil {
			t.Fatal(err)
		}
		defer typed.Stop()
		s, err := New(func() Caches { return Caches{Typed: typed, Discovery: discovery} }).Begin()
		if err != nil {
			t.Fatal(err)
		}
		if got := s.DependentCandidates("uid-rs", ownership.InNamespace("alpha")); len(got.Candidates) != 1 {
			t.Fatalf("with %d unrelated Pods: %+v", unrelated, got)
		}
		const searches = 20
		var before, after goruntime.MemStats
		goruntime.ReadMemStats(&before)
		for range searches {
			s.DependentCandidates("uid-rs", ownership.InNamespace("alpha"))
		}
		goruntime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / searches
	}
	small, large := bytesPerSearch(10), bytesPerSearch(5000)
	if large > small+16<<10 {
		t.Fatalf("%d bytes per search with 5,000 unrelated Pods, %d with 10; the search must not scan the kind", large, small)
	}
}

// Paged informers are indexed like any other.
func TestDependentCandidatesFromPagedInformers(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	f := newFixture(t, k8score.CacheConfig{ListPageSize: 2}, nil)
	s := begin(t, f.source())
	got := s.DependentCandidates("uid-deploy", ownership.InNamespace("alpha"))
	if !slices.Contains(candidateRefs(got), ref("apps", "ReplicaSet", "alpha", "web-1")) ||
		slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"} }) {
		t.Fatalf("dependents through a paged ReplicaSet informer = %+v", got)
	}
}

// A typed kind whose load missed its deadline may still finish: a search says
// its load is overdue, not that it is still loading.
func TestDependentCandidatesOfATypedKindPastItsLoadDeadline(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{
		ResourceTypes:       map[string]bool{k8score.Pods: true, k8score.Secrets: true},
		DeferredTypes:       map[string]bool{k8score.Secrets: true},
		DeferredSyncTimeout: 50 * time.Millisecond,
	}, func(f *fixture) {
		f.typed.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("initial list held")
		})
	})
	waitUntil(t, "the Secrets load deadline", func() bool {
		return f.caches.Typed.KindReadinessFor(k8score.Secrets) == k8score.KindFailed
	})
	secret := resourceid.GroupKind{Kind: "Secret"}
	// One informer serves every namespace, so its state is one gap whatever
	// the search names.
	for _, scope := range []ownership.SearchScope{ownership.Everywhere(), ownership.InNamespace("alpha"), {Namespaces: []string{"alpha", "beta"}}} {
		got := begin(t, f.source()).DependentCandidates("uid-deploy", scope)
		if want := (ownership.Gap{Kind: secret, Reason: readreason.KindSyncTimedOut}); !slices.Contains(got.Gaps, want) {
			t.Errorf("%+v: gaps %+v; want %+v", scope, got.Gaps, want)
		}
	}
}

// A kind Radar doesn't collect is unread in every namespace a search names, for
// one reason, so the search says so once: a caller who may read a hundred
// namespaces asking about a cluster-scoped owner gets the gaps a caller who
// may read one gets, not a hundred times as many.
func TestDependentCandidatesReportAWholeKindsStateOnce(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	s := begin(t, f.source())
	search := func(n int) ownership.DependentCandidatesRead {
		var namespaces []string
		for i := range n {
			namespaces = append(namespaces, fmt.Sprintf("ns-%03d", i))
		}
		return s.DependentCandidates("uid-node", ownership.SearchScope{Namespaces: namespaces, IncludeClusterScoped: true})
	}
	one, hundred := search(1), search(100)
	gadget := resourceid.GroupKind{Group: "example.io", Kind: "Gadget"}
	want := ownership.Gap{Kind: gadget, Reason: readreason.RadarKindNotCollected}
	if !slices.Contains(one.Gaps, want) {
		t.Fatalf("gaps %+v; want %+v for the Gadgets nothing watches", one.Gaps, want)
	}
	if !slices.Equal(hundred.Gaps, one.Gaps) {
		t.Fatalf("a search of 100 namespaces has %d gaps, one of a single namespace %d: %+v", len(hundred.Gaps), len(one.Gaps), hundred.Gaps)
	}
	if got := s.DependentCandidates("uid-node", ownership.SearchScope{Namespaces: []string{""}}); slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == gadget }) {
		t.Fatalf("a scope naming no namespace has gaps %+v; it looked nowhere", got.Gaps)
	}
}

// A kind whose one cluster-wide informer hasn't loaded, typed or dynamic, is
// in the same state in every namespace a search names: one gap, whether the
// search names one namespace or a hundred.
func TestDependentCandidatesReportAClusterWideStoreLoadingOnce(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{
		ResourceTypes: map[string]bool{k8score.Pods: true, k8score.Secrets: true},
		DeferredTypes: map[string]bool{k8score.Secrets: true},
	}, func(f *fixture) {
		f.typed.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("initial list held")
		})
		var widgetLists atomic.Int32
		f.dynamic.PrependReactor("list", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
			if widgetLists.Add(1) > 1 {
				// The first list is the watch's access probe.
				return true, nil, errors.New("initial list held")
			}
			return false, nil, nil
		})
	})
	if err := f.caches.Dynamic.EnsureWatching(widgets); err != nil {
		t.Fatal(err)
	}
	s := begin(t, f.source())
	search := func(n int) ownership.DependentCandidatesRead {
		var namespaces []string
		for i := range n {
			namespaces = append(namespaces, fmt.Sprintf("ns-%03d", i))
		}
		return s.DependentCandidates("uid-node", ownership.SearchScope{Namespaces: namespaces, IncludeClusterScoped: true})
	}
	one, hundred := search(1), search(100)
	for _, want := range []ownership.Gap{
		{Kind: resourceid.GroupKind{Kind: "Secret"}, Reason: readreason.KindSyncPending},
		{Kind: resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, Reason: readreason.KindSyncPending},
	} {
		if !slices.Contains(one.Gaps, want) {
			t.Fatalf("gaps %+v; want %+v", one.Gaps, want)
		}
	}
	if !slices.Equal(hundred.Gaps, one.Gaps) {
		t.Fatalf("a search of 100 namespaces has %d gaps, one of a single namespace %d: %+v", len(hundred.Gaps), len(one.Gaps), hundred.Gaps)
	}
}

// A cluster-wide store serves every namespace, so a search of many namespaces
// reads it once: the work follows the owner's dependents plus the namespaces
// asked about, not their product.
func TestDependentCandidatesReadAClusterWideStoreOncePerSearch(t *testing.T) {
	search := func(n int) uint64 {
		var namespaces []string
		f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
			for i := range n {
				ns := fmt.Sprintf("ns-%04d", i)
				u := widget(ns, "w", "uid-w-"+ns)
				u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: "n1", UID: "uid-node"}})
				if err := f.dynamic.Tracker().Add(u); err != nil {
					t.Fatal(err)
				}
				namespaces = append(namespaces, ns)
			}
		})
		f.watch(t, widgets)
		s := begin(t, f.source())
		scope := ownership.SearchScope{Namespaces: namespaces}
		if got := s.DependentCandidates("uid-node", scope); len(got.Candidates) != n {
			t.Fatalf("%d namespaces: %d candidates, want one Widget in each", n, len(got.Candidates))
		}
		var before, after goruntime.MemStats
		goruntime.ReadMemStats(&before)
		for range 3 {
			s.DependentCandidates("uid-node", scope)
		}
		goruntime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / 3
	}
	small := search(100)
	large := search(800)
	// Eight times the namespaces and dependents allocates eight times as much
	// when each store is read once; read per namespace, it grows with their
	// product.
	if large > 16*small {
		t.Fatalf("a search of 800 namespaces allocated %d bytes, %.0fx a search of 100 (%d)", large, float64(large)/float64(small), small)
	}
}

// With one version watched only in alpha and another watched cluster-wide but
// still loading, alpha is read through the first, and the rest of the
// namespaces are loading, not outside what Radar collects.
func TestDependentCandidatesSayTheRestLoadsWhileAClusterWideVersionLoads(t *testing.T) {
	beta := schema.GroupVersionResource{Group: "example.io", Version: "v1beta1", Resource: "widgets"}
	var held atomic.Int32
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		f.dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{widgets: "WidgetList", beta: "WidgetList", gadgets: "GadgetList", summaries: "SummaryList"},
			betaWidget(widget("alpha", "w1", "uid-widget")))
		f.dynamic.PrependReactor("list", "widgets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			switch {
			case a.GetResource().Version == "v1" && held.Add(1) > 1:
				// The first list is the watch's access probe.
				return true, nil, errors.New("initial list held")
			case a.GetResource().Version == "v1beta1" && a.GetNamespace() == "":
				return true, nil, apierrors.NewForbidden(beta.GroupResource(), "", errors.New("namespaces only"))
			}
			return false, nil, nil
		})
	})
	if _, err := f.caches.Dynamic.List(beta, "alpha"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "v1beta1 in alpha", func() bool { return f.caches.Dynamic.IsNamespaceSynced(beta, "alpha") })
	if err := f.caches.Dynamic.EnsureWatching(widgets); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "v1's held list", func() bool { return held.Load() > 1 })
	got := begin(t, f.source()).DependentCandidates("uid-deploy", ownership.Everywhere())
	var widgetGaps []ownership.Gap
	for _, g := range got.Gaps {
		if g.Kind == widgetKind {
			widgetGaps = append(widgetGaps, g)
		}
	}
	if want := []ownership.Gap{{Kind: widgetKind, Reason: readreason.KindSyncPending}}; !slices.Equal(widgetGaps, want) {
		t.Fatalf("Widget gaps = %+v; want %+v", widgetGaps, want)
	}
	if !slices.Contains(candidateRefs(got), ref("example.io", "Widget", "alpha", "w1")) {
		t.Fatalf("alpha's Widget, through its loaded namespace watch, is missing: %+v", got)
	}
}

// A typed kind watched namespace by namespace answers from the namespaces that
// have loaded while another is still loading: their dependents are found, and
// only the loading namespace is a gap.
func TestDependentCandidatesOfATypedKindLoadingInSomeNamespaces(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{
		ResourceScopes:          map[string]k8score.ResourceScope{k8score.ReplicaSets: {Enabled: true, Namespace: "alpha"}},
		ResourceScopeNamespaces: map[string][]string{k8score.ReplicaSets: {"alpha", "beta"}},
		// A critical kind would hold the cache's start until beta loads.
		DeferredTypes: map[string]bool{k8score.ReplicaSets: true},
	}, func(f *fixture) {
		f.typed.PrependReactor("list", "replicasets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() == "beta" {
				return true, nil, errors.New("initial list held")
			}
			return false, nil, nil
		})
	})
	waitUntil(t, "alpha's ReplicaSets", func() bool {
		_, ok := f.caches.Typed.ReadableObjects(k8score.ReplicaSets, []string{"alpha"})
		return ok
	})
	rs := resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}
	for _, tc := range []struct {
		scope ownership.SearchScope
		want  []ownership.Gap
	}{
		{ownership.SearchScope{Namespaces: []string{"alpha", "beta"}}, []ownership.Gap{{Kind: rs, Namespace: "beta", Reason: readreason.KindSyncPending}}},
		{ownership.Everywhere(), []ownership.Gap{{Kind: rs, Reason: readreason.RadarNamespaceNotCollected}, {Kind: rs, Namespace: "beta", Reason: readreason.KindSyncPending}}},
	} {
		got := begin(t, f.source()).DependentCandidates("uid-deploy", tc.scope)
		if !slices.ContainsFunc(got.Candidates, func(c ownership.Observation) bool { return c.Ref.GroupKind() == rs && c.Ref.Namespace == "alpha" }) {
			t.Errorf("%+v: candidates %+v; want alpha's ReplicaSet", tc.scope, got.Candidates)
		}
		var rsGaps []ownership.Gap
		for _, g := range got.Gaps {
			if g.Kind == rs {
				rsGaps = append(rsGaps, g)
			}
		}
		if !slices.Equal(rsGaps, tc.want) {
			t.Errorf("%+v: ReplicaSet gaps %+v; want %+v", tc.scope, rsGaps, tc.want)
		}
	}
}
