package cachesource

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func candidateRefs(r ownership.CandidateRead) []resourceid.Ref {
	var out []resourceid.Ref
	for _, c := range r.Candidates {
		out = append(out, c.Ref)
	}
	return out
}

func hasGap(r ownership.CandidateRead, want ownership.Gap) bool {
	return slices.Contains(r.Gaps, want)
}

func TestOwnerCandidatesNeedEveryWatchedVersionToAgree(t *testing.T) {
	w1 := ref("example.io", "Widget", "alpha", "w1")
	for _, tc := range []struct {
		name        string
		v1, v1beta1 []runtime.Object
		wantFound   bool
		wantPending bool
	}{
		{name: "both hold it", v1: []runtime.Object{widget("alpha", "w1", "uid-widget")}, v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-widget"))}, wantFound: true},
		{name: "only one holds it", v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-widget"))}, wantPending: true},
		{name: "two incarnations", v1: []runtime.Object{widget("alpha", "w1", "uid-new")}, v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-old"))}, wantPending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := versionsFixture(t, tc.v1, tc.v1beta1, nil)
			s := begin(t, f.source())
			for _, domain := range []ownership.Domain{ownership.InNamespace("alpha"), ownership.Everywhere()} {
				got := s.OwnerCandidates("uid-deploy", domain, 0)
				namespace := "alpha"
				if domain.AllNamespaces {
					namespace = ""
				}
				pending := hasGap(got, ownership.Gap{Kind: widgetKind, Namespace: namespace, Reason: readreason.KindSyncPending})
				if found := slices.Contains(candidateRefs(got), w1); found != tc.wantFound || pending != tc.wantPending {
					t.Fatalf("%+v: found w1 %v, pending gap %v; want %v, %v (%+v)", domain, found, pending, tc.wantFound, tc.wantPending, got)
				}
			}
		})
	}
}

func TestOwnerCandidatesReportAnAmbiguousKind(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		fd := f.typed.Discovery().(*fakediscovery.FakeDiscovery)
		fd.Resources = append(fd.Resources, &metav1.APIResourceList{GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{
			{Name: "morewidgets", Kind: "Widget", Namespaced: true, Verbs: []string{"delete", "get", "list", "watch"}},
		}})
	})
	f.watch(t, widgets)
	s := begin(t, f.source())
	got := s.OwnerCandidates("uid-deploy", ownership.Everywhere(), 0)
	if !hasGap(got, ownership.Gap{Kind: widgetKind, Reason: readreason.ReadError}) || got.Complete() {
		t.Fatalf("dependents with Widget served by two resources = %+v; want a Widget read_error gap", got)
	}
	if slices.Contains(candidateRefs(got), ref("example.io", "Widget", "alpha", "w1")) {
		t.Fatal("an ambiguous kind's objects can't be named as one resource's")
	}
	if none := s.OwnerCandidates("uid-deploy", ownership.Domain{}, 0); len(none.Gaps) != 0 {
		t.Fatalf("a search of nothing reports gaps: %+v", none.Gaps)
	}
}

// A search while a cluster-wide watch takes over from namespace ones must
// not answer from the unloaded replacement, and a session that read the old
// store must fail Validate.
func TestOwnerCandidatesThroughAPromotion(t *testing.T) {
	f, p := newPromotionFixture(t)
	source := f.source()
	before := begin(t, source)
	if got := before.OwnerCandidates("uid-deploy", ownership.InNamespace("alpha"), 0); !slices.Contains(candidateRefs(got), ref("example.io", "Widget", "alpha", "w1")) {
		t.Fatalf("from alpha's watch = %+v", got)
	}
	p.run(t, f, true)
	if err := before.Validate(); !errors.Is(err, ownership.ErrStoreReplaced) {
		t.Fatalf("Validate after the replacement = %v, want ErrStoreReplaced", err)
	}
	during := begin(t, source)
	for _, domain := range []ownership.Domain{ownership.InNamespace("alpha"), ownership.Everywhere()} {
		got := during.OwnerCandidates("uid-deploy", domain, 0)
		if slices.ContainsFunc(got.Candidates, func(c ownership.Observation) bool { return c.Ref.GroupKind() == widgetKind }) ||
			!slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == widgetKind && g.Reason == readreason.KindSyncPending }) {
			t.Fatalf("%+v from the unloaded replacement = %+v; want a Widget kind_sync_pending gap and no Widgets", domain, got)
		}
	}
	p.hold.Store(false)
	if !f.caches.Dynamic.WaitForSync(widgets, 5*time.Second) {
		t.Fatal("the replacement did not sync")
	}
	after := begin(t, source)
	got := after.OwnerCandidates("uid-deploy", ownership.Everywhere(), 0)
	if !slices.Contains(candidateRefs(got), ref("example.io", "Widget", "alpha", "w1")) ||
		slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == widgetKind }) {
		t.Fatalf("from the loaded replacement = %+v", got)
	}
}

// A namespace watch started mid-session must not widen one answer of the
// session and not another.
func TestOwnerCandidatesKeepTheSessionsNamespaceWatches(t *testing.T) {
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
	first := s.OwnerCandidates("uid-deploy", ownership.Everywhere(), 0)
	if _, err := f.caches.Dynamic.List(widgets, "beta"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "beta's watch", func() bool { return f.caches.Dynamic.IsNamespaceSynced(widgets, "beta") })
	second := s.OwnerCandidates("uid-deploy", ownership.Everywhere(), 0)
	if !slices.Equal(candidateRefs(first), candidateRefs(second)) || !slices.Equal(first.Gaps, second.Gaps) {
		t.Fatalf("a watch started mid-session changed the session's answer:\n first  %v %+v\n second %v %+v", candidateRefs(first), first.Gaps, candidateRefs(second), second.Gaps)
	}
	if !hasGap(first, ownership.Gap{Kind: widgetKind, Reason: readreason.CollectorScopeLimited}) {
		t.Fatalf("watched namespace by namespace, the rest must be a gap: %+v", first)
	}
	if fresh := begin(t, f.source()).OwnerCandidates("uid-deploy", ownership.Everywhere(), 0); slices.Equal(candidateRefs(fresh), candidateRefs(first)) {
		t.Fatalf("a new session must see the new watch: %v", candidateRefs(fresh))
	}
}

func TestOwnerCandidatesDoNoIO(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	f.watch(t, widgets)
	waitForWatches(t, &f.typed.Fake, "pods", "replicasets", "deployments", "nodes", "events")
	waitForWatches(t, &f.dynamic.Fake, "widgets")
	before := len(f.typed.Actions()) + len(f.dynamic.Actions())
	s := begin(t, f.source())
	for _, domain := range []ownership.Domain{ownership.Everywhere(), ownership.InNamespace("alpha"), {Namespaces: []string{"alpha", "gamma"}, ClusterScoped: true}} {
		for _, uid := range []types.UID{"uid-deploy", "uid-rs", "uid-node", "uid-none"} {
			s.OwnerCandidates(uid, domain, 0)
		}
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if after := len(f.typed.Actions()) + len(f.dynamic.Actions()); after != before {
		t.Fatalf("searches made %d API calls; they must make none", after-before)
	}
	if f.caches.Dynamic.HasCoveringInformer(gadgets, "alpha") {
		t.Fatal("a search started a watch")
	}
}

// The search goes through the owner index, so its cost follows the owner's
// dependents, not how many other objects Radar holds.
func TestOwnerCandidatesCostDoesNotGrowWithUnrelatedObjects(t *testing.T) {
	allocations := func(unrelated int) float64 {
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
		if got := s.OwnerCandidates("uid-rs", ownership.InNamespace("alpha"), 0); len(got.Candidates) != 1 {
			t.Fatalf("with %d unrelated Pods: %+v", unrelated, got)
		}
		return testing.AllocsPerRun(20, func() { s.OwnerCandidates("uid-rs", ownership.InNamespace("alpha"), 0) })
	}
	small, large := allocations(10), allocations(5000)
	if large > small {
		t.Fatalf("%v allocations per search with 5,000 unrelated Pods, %v with 10; the search must not scan the kind", large, small)
	}
}

// An empty answer depends on the stores that gave it as much as a full one.
func TestOwnerCandidatesNegativeFailsValidateOnceItsStoreIsReplaced(t *testing.T) {
	f, p := newPromotionFixture(t)
	s := begin(t, f.source())
	got := s.OwnerCandidates("uid-nothing-names", ownership.InNamespace("alpha"), 0)
	if slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == widgetKind }) || len(got.Candidates) != 0 {
		t.Fatalf("a search alpha's loaded watch answers = %+v", got)
	}
	p.run(t, f, true)
	if err := s.Validate(); !errors.Is(err, ownership.ErrStoreReplaced) {
		t.Fatalf("Validate after the store behind an empty answer was replaced = %v, want ErrStoreReplaced", err)
	}
}

// Paged informers are indexed like any other.
func TestOwnerCandidatesFromPagedInformers(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	f := newFixture(t, k8score.CacheConfig{ListPageSize: 2}, nil)
	s := begin(t, f.source())
	got := s.OwnerCandidates("uid-deploy", ownership.InNamespace("alpha"), 0)
	if !slices.Contains(candidateRefs(got), ref("apps", "ReplicaSet", "alpha", "web-1")) ||
		slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"} }) {
		t.Fatalf("dependents through a paged ReplicaSet informer = %+v", got)
	}
}
