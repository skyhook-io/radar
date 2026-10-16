package cachesource

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var (
	widgets   = schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	gadgets   = schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "gadgets"}
	summaries = schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "summaries"}
	deletedAt = metav1.NewTime(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC))
)

func ptr[T any](v T) *T { return &v }

func ref(group, kind, namespace, name string) resourceid.Ref {
	return resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}
}

func objectMeta(namespace, name, uid string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(uid)}
}

// replicaSetOwners mixes a controller with a non-controller whose apiVersion
// names another served version, so a copy must keep order, apiVersion and the
// difference between an unset and a false flag.
var replicaSetOwners = []metav1.OwnerReference{
	{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "uid-deploy", Controller: ptr(true), BlockOwnerDeletion: ptr(true)},
	{APIVersion: "example.io/v1beta1", Kind: "Widget", Name: "w1", UID: "uid-widget", BlockOwnerDeletion: ptr(false)},
}

func typedObjects() []runtime.Object {
	rs := &appsv1.ReplicaSet{ObjectMeta: objectMeta("alpha", "web-1", "uid-rs")}
	rs.OwnerReferences = replicaSetOwners
	pod := &corev1.Pod{ObjectMeta: objectMeta("alpha", "web-1-a", "uid-pod")}
	pod.DeletionTimestamp = &deletedAt
	pod.Finalizers = []string{"foregroundDeletion"}
	return []runtime.Object{
		&appsv1.Deployment{ObjectMeta: objectMeta("alpha", "web", "uid-deploy")},
		rs,
		pod,
		&corev1.Node{ObjectMeta: objectMeta("", "n1", "uid-node")},
		&corev1.Event{ObjectMeta: objectMeta("alpha", "e1", "uid-event")},
	}
}

func widget(namespace, name, uid string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1", "kind": "Widget",
		"metadata": map[string]any{
			"name": name, "namespace": namespace, "uid": uid,
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web", "uid": "uid-deploy"}},
		},
	}}
}

func discoveryLists() []*metav1.APIResourceList {
	gc := []string{"create", "delete", "get", "list", "patch", "update", "watch"}
	return []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: gc},
			{Name: "nodes", Kind: "Node", Verbs: gc},
			{Name: "events", Kind: "Event", Namespaced: true, Verbs: gc},
			{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: gc},
		}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
			{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: gc},
			{Name: "replicasets", Kind: "ReplicaSet", Namespaced: true, Verbs: gc},
		}},
		{GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{
			{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: gc},
			{Name: "gadgets", Kind: "Gadget", Namespaced: true, Verbs: gc},
			{Name: "summaries", Kind: "Summary", Namespaced: true, Verbs: []string{"get", "list"}},
		}},
		{GroupVersion: "example.io/v1beta1", APIResources: []metav1.APIResource{
			{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: gc},
		}},
	}
}

// fixture is a cluster's fake API clients and Radar's caches over them.
type fixture struct {
	typed   *fake.Clientset
	dynamic *dynamicfake.FakeDynamicClient
	caches  Caches
}

// newFixture starts caches over the fixture objects. cfg selects the typed
// kinds (default: Pods, ReplicaSets, Deployments, Nodes, Events); prepare runs
// before any cache starts, for reactors.
func newFixture(t *testing.T, cfg k8score.CacheConfig, prepare func(*fixture)) *fixture {
	t.Helper()
	f := &fixture{
		typed: fake.NewClientset(typedObjects()...),
		dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{widgets: "WidgetList", gadgets: "GadgetList", summaries: "SummaryList"},
			widget("alpha", "w1", "uid-widget")),
	}
	f.typed.Discovery().(*fakediscovery.FakeDiscovery).Resources = discoveryLists()
	if prepare != nil {
		prepare(f)
	}
	discovery, err := k8score.NewResourceDiscovery(f.typed.Discovery())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Client = f.typed
	if cfg.ResourceTypes == nil && cfg.ResourceScopes == nil {
		cfg.ResourceTypes = map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true, k8score.Deployments: true, k8score.Nodes: true, k8score.Events: true}
	}
	typed, err := k8score.NewResourceCache(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(typed.Stop)
	dynamic, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: f.dynamic, Discovery: discovery})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dynamic.Stop)
	f.caches = Caches{Typed: typed, Dynamic: dynamic, Discovery: discovery}
	return f
}

func (f *fixture) watch(t *testing.T, gvr schema.GroupVersionResource) {
	t.Helper()
	if err := f.caches.Dynamic.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !f.caches.Dynamic.WaitForSync(gvr, 5*time.Second) {
		t.Fatalf("%v did not sync", gvr)
	}
}

func (f *fixture) source() *Source {
	return New(func() Caches { return f.caches })
}

func begin(t *testing.T, s *Source) ownership.Session {
	t.Helper()
	session, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestObjectCopiesWhatOwnershipReads(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	f.watch(t, widgets)
	s := begin(t, f.source())

	stored, err := f.typed.AppsV1().ReplicaSets("alpha").Get(context.Background(), "web-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	read := s.Object(ref("apps", "ReplicaSet", "alpha", "web-1"))
	want := ownership.Observation{
		Ref:             ref("apps", "ReplicaSet", "alpha", "web-1"),
		APIVersion:      "apps/v1",
		UID:             "uid-rs",
		ResourceVersion: stored.ResourceVersion,
		OwnerReferences: replicaSetOwners,
	}
	if read.State != ownership.Present || !reflect.DeepEqual(read.Object, want) {
		t.Fatalf("ReplicaSet = %v %+v\nwant present %+v", read.State, read.Object, want)
	}
	cached, err := f.caches.Typed.ReplicaSets().ReplicaSets("alpha").Get("web-1")
	if err != nil {
		t.Fatal(err)
	}
	*read.Object.OwnerReferences[0].Controller = false
	read.Object.OwnerReferences[1].Name = "changed"
	if !*cached.OwnerReferences[0].Controller || cached.OwnerReferences[1].Name != "w1" {
		t.Fatal("changing an observation changed the cached object")
	}

	pod := s.Object(ref("", "Pod", "alpha", "web-1-a"))
	if pod.State != ownership.Present || pod.Object.APIVersion != "v1" ||
		pod.Object.DeletionTimestamp == nil || !pod.Object.DeletionTimestamp.Equal(&deletedAt) ||
		!reflect.DeepEqual(pod.Object.Finalizers, []string{"foregroundDeletion"}) {
		t.Fatalf("Pod = %v %+v; want its deletion timestamp and finalizers", pod.State, pod.Object)
	}

	w := s.Object(ref("example.io", "Widget", "alpha", "w1"))
	wantOwners := []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "uid-deploy"}}
	if w.State != ownership.Present || w.Object.APIVersion != "example.io/v1" || w.Object.UID != "uid-widget" ||
		!reflect.DeepEqual(w.Object.OwnerReferences, wantOwners) {
		t.Fatalf("Widget = %v %+v", w.State, w.Object)
	}

	if node := s.Object(ref("", "Node", "", "n1")); node.State != ownership.Present || node.Object.UID != "uid-node" {
		t.Fatalf("Node = %v %+v", node.State, node.Object)
	}
	// Events are left out of what can be a dependent, not out of reads.
	if event := s.Object(ref("", "Event", "alpha", "e1")); event.State != ownership.Present {
		t.Fatalf("Event = %v", event.State)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestObjectIsAbsentOnlyWhereALoadedStoreLacksIt(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	f.watch(t, widgets)
	s := begin(t, f.source())
	for _, r := range []resourceid.Ref{
		ref("", "Pod", "alpha", "missing"),
		ref("", "Pod", "beta", "web-1-a"),
		ref("", "Node", "", "missing"),
		ref("example.io", "Widget", "alpha", "missing"),
		ref("example.io", "Widget", "beta", "w1"),
	} {
		if got := s.Object(r); got.State != ownership.Absent || got.Gap != (ownership.Gap{}) {
			t.Errorf("%v = %v %+v; want absent", r, got.State, got.Gap)
		}
	}
}

func TestObjectSaysWhyItCannotAnswer(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		f.dynamic.PrependReactor("list", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(widgets.GroupResource(), "", errors.New("denied"))
		})
	})
	if err := f.caches.Dynamic.EnsureWatching(widgets); err == nil {
		t.Fatal("scenario: the widget watch should have been refused")
	}
	s := begin(t, f.source())
	for _, tc := range []struct {
		name string
		ref  resourceid.Ref
		want readreason.Code
	}{
		{"typed kind Radar doesn't collect", ref("", "Secret", "alpha", "s"), readreason.KindNotWatched},
		{"known kind with no watch", ref("example.io", "Gadget", "alpha", "g"), readreason.KindNotWatched},
		{"kind whose watch was refused", ref("example.io", "Widget", "alpha", "w1"), readreason.CollectorForbidden},
		{"kind served without watch", ref("example.io", "Summary", "alpha", "s"), readreason.KindNotWatchable},
		{"kind discovery doesn't serve", ref("example.io", "Sprocket", "alpha", "s"), readreason.KindUnknown},
		{"kind in the wrong group", ref("example.io", "Pod", "alpha", "web-1-a"), readreason.KindUnknown},
		{"namespaced typed kind without a namespace", ref("", "Pod", "", "web-1-a"), readreason.NamespaceRequired},
		{"namespaced dynamic kind without a namespace", ref("example.io", "Gadget", "", "g"), readreason.NamespaceRequired},
		// The Node store is complete, and an absence would wrongly call an
		// owner resolved with the dependent's namespace dangling.
		{"cluster-scoped kind in a namespace", ref("", "Node", "alpha", "n1"), readreason.ReadError},
		{"no name", ref("", "Pod", "alpha", ""), readreason.ReadError},
		{"no kind", ref("", "", "alpha", "web-1-a"), readreason.ReadError},
	} {
		got := s.Object(tc.ref)
		want := ownership.Gap{Kind: tc.ref.GroupKind(), Namespace: tc.ref.Namespace, Reason: tc.want}
		if got.State != ownership.Unavailable || got.Gap != want {
			t.Errorf("%s: %v %+v; want unavailable %+v", tc.name, got.State, got.Gap, want)
		}
	}
}

func TestObjectIsUnavailableWhileItsStoreLoads(t *testing.T) {
	var holdWidgets atomic.Bool
	var widgetLists atomic.Int32
	holdWidgets.Store(true)
	f := newFixture(t, k8score.CacheConfig{
		ResourceTypes: map[string]bool{k8score.Pods: true, k8score.Secrets: true},
		DeferredTypes: map[string]bool{k8score.Secrets: true},
	}, func(f *fixture) {
		f.typed.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("initial list held")
		})
		// The first list is the watch's access probe; the informer's own
		// lists fail while held.
		f.dynamic.PrependReactor("list", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
			if widgetLists.Add(1) > 1 && holdWidgets.Load() {
				return true, nil, errors.New("initial list held")
			}
			return false, nil, nil
		})
	})
	if err := f.caches.Dynamic.EnsureWatching(widgets); err != nil {
		t.Fatal(err)
	}
	s := begin(t, f.source())
	for r, want := range map[resourceid.Ref]readreason.Code{
		ref("", "Secret", "alpha", "s"):            readreason.KindSyncPending,
		ref("example.io", "Widget", "alpha", "w1"): readreason.KindSyncPending,
	} {
		if got := s.Object(r); got.State != ownership.Unavailable || got.Gap.Reason != want {
			t.Errorf("%v while loading = %v %+v; want unavailable %s, never absent", r, got.State, got.Gap, want)
		}
	}
	holdWidgets.Store(false)
	if !f.caches.Dynamic.WaitForSync(widgets, 5*time.Second) {
		t.Fatal("widgets did not sync once released")
	}
	if got := s.Object(ref("example.io", "Widget", "alpha", "w1")); got.State != ownership.Present {
		t.Fatalf("Widget once loaded = %v %+v", got.State, got.Gap)
	}
}

func TestObjectOutsideTheCollectedNamespacesIsUnavailable(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{
		ResourceScopes: map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "alpha"}},
	}, nil)
	s := begin(t, f.source())
	if got := s.Object(ref("", "Pod", "alpha", "web-1-a")); got.State != ownership.Present {
		t.Fatalf("Pod in a collected namespace = %v %+v", got.State, got.Gap)
	}
	got := s.Object(ref("", "Pod", "beta", "web-1-a"))
	if want := (ownership.Gap{Kind: resourceid.GroupKind{Kind: "Pod"}, Namespace: "beta", Reason: readreason.CollectorScopeLimited}); got.State != ownership.Unavailable || got.Gap != want {
		t.Fatalf("Pod outside the collected namespaces = %v %+v; want unavailable %+v", got.State, got.Gap, want)
	}
}

// promotion stages a kind first watched in one namespace being taken over by
// a cluster-wide watch: cluster-wide list is refused until promote; after it,
// the first cluster-wide list is the watch's access probe and the informer's
// own lists fail while hold is set.
type promotion struct {
	promote, hold atomic.Bool
	lists         atomic.Int32
}

func (p *promotion) reactor(a k8stesting.Action) (bool, runtime.Object, error) {
	if a.GetNamespace() != "" {
		return false, nil, nil
	}
	if !p.promote.Load() {
		return true, nil, apierrors.NewForbidden(widgets.GroupResource(), "", errors.New("cluster-wide list denied"))
	}
	if p.lists.Add(1) > 1 && p.hold.Load() {
		return true, nil, errors.New("initial list held")
	}
	return false, nil, nil
}

func newPromotionFixture(t *testing.T) (*fixture, *promotion) {
	t.Helper()
	p := &promotion{}
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) { f.dynamic.PrependReactor("list", "widgets", p.reactor) })
	if _, err := f.caches.Dynamic.List(widgets, "alpha"); err != nil {
		t.Fatal(err)
	}
	if !f.caches.Dynamic.WaitForSync(widgets, 5*time.Second) || !f.caches.Dynamic.IsNamespaceSynced(widgets, "alpha") {
		t.Fatal("scenario: alpha's widget watch did not sync")
	}
	return f, p
}

func (p *promotion) run(t *testing.T, f *fixture, hold bool) {
	t.Helper()
	p.promote.Store(true)
	p.hold.Store(hold)
	if err := f.caches.Dynamic.EnsureWatching(widgets); err != nil {
		t.Fatal(err)
	}
	if !f.caches.Dynamic.HasCoveringInformer(widgets, "") {
		t.Fatal("scenario: no cluster-wide widget watch")
	}
}

func TestValidateFailsOnceAReadStoreIsReplaced(t *testing.T) {
	f, p := newPromotionFixture(t)
	source := f.source()
	before := begin(t, source)
	if got := before.Object(ref("example.io", "Widget", "alpha", "w1")); got.State != ownership.Present {
		t.Fatalf("Widget from alpha's watch = %v %+v", got.State, got.Gap)
	}
	if err := before.Validate(); err != nil {
		t.Fatal(err)
	}

	p.run(t, f, true)
	if err := before.Validate(); !errors.Is(err, ownership.ErrStoreReplaced) {
		t.Fatalf("Validate after the replacement = %v, want ErrStoreReplaced", err)
	}
	during := begin(t, source)
	if got := during.Object(ref("example.io", "Widget", "alpha", "w1")); got.State != ownership.Unavailable || got.Gap.Reason != readreason.KindSyncPending {
		t.Fatalf("Widget from the unloaded replacement = %v %+v; want unavailable kind_sync_pending, never absent", got.State, got.Gap)
	}

	p.hold.Store(false)
	if !f.caches.Dynamic.WaitForSync(widgets, 5*time.Second) {
		t.Fatal("the replacement did not sync once released")
	}
	if got := during.Object(ref("example.io", "Widget", "alpha", "w1")); got.State != ownership.Present {
		t.Fatalf("Widget from the loaded replacement = %v %+v", got.State, got.Gap)
	}
	if err := during.Validate(); err != nil {
		t.Fatalf("a session that read only the replacement: %v", err)
	}
}

func TestValidateFailsWhenReadsSpanAStoreAndItsReplacement(t *testing.T) {
	f, p := newPromotionFixture(t)
	s := begin(t, f.source())
	if got := s.Object(ref("example.io", "Widget", "alpha", "w1")); got.State != ownership.Present {
		t.Fatalf("Widget from alpha's watch = %v", got.State)
	}
	p.run(t, f, false)
	if !f.caches.Dynamic.WaitForSync(widgets, 5*time.Second) {
		t.Fatal("the replacement did not sync")
	}
	if got := s.Object(ref("example.io", "Widget", "alpha", "missing")); got.State != ownership.Absent {
		t.Fatalf("a miss in the loaded replacement = %v", got.State)
	}
	if err := s.Validate(); !errors.Is(err, ownership.ErrStoreReplaced) {
		t.Fatalf("Validate = %v; a session that read both stores must fail", err)
	}
}

func TestValidateFailsOnceTheCachesAreRetired(t *testing.T) {
	first := newFixture(t, k8score.CacheConfig{}, nil)
	second := newFixture(t, k8score.CacheConfig{}, nil)
	current := first.caches
	source := New(func() Caches { return current })

	s := begin(t, source)
	current = second.caches
	if err := s.Validate(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("after a switch: %v, want ErrCachesReplaced", err)
	}

	s = begin(t, source)
	second.caches.Dynamic.Stop()
	if err := s.Validate(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("after the dynamic cache stopped: %v, want ErrCachesReplaced", err)
	}
	if _, err := source.Begin(); !errors.Is(err, ownership.ErrNotConnected) {
		t.Fatalf("Begin over a stopped cache = %v, want ErrNotConnected", err)
	}

	current = first.caches
	s = begin(t, source)
	first.caches.Typed.Stop()
	if err := s.Validate(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("after the typed cache stopped: %v, want ErrCachesReplaced", err)
	}

	current = Caches{}
	if _, err := source.Begin(); !errors.Is(err, ownership.ErrNotConnected) {
		t.Fatalf("Begin with no caches = %v, want ErrNotConnected", err)
	}
}

func TestSessionDoesNoIO(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	f.watch(t, widgets)
	waitForWatches(t, &f.typed.Fake, "pods", "replicasets", "deployments", "nodes", "events")
	waitForWatches(t, &f.dynamic.Fake, "widgets")
	before := len(f.typed.Actions()) + len(f.dynamic.Actions())

	s := begin(t, f.source())
	_ = s.Catalog().DependentKinds()
	for _, r := range []resourceid.Ref{
		ref("apps", "ReplicaSet", "alpha", "web-1"),
		ref("", "Pod", "alpha", "missing"),
		ref("", "Secret", "alpha", "s"),
		ref("example.io", "Widget", "alpha", "w1"),
		ref("example.io", "Widget", "beta", "w1"),
		ref("example.io", "Gadget", "alpha", "g"),
		ref("example.io", "Summary", "alpha", "s"),
		ref("example.io", "Sprocket", "alpha", "s"),
	} {
		s.Object(r)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if after := len(f.typed.Actions()) + len(f.dynamic.Actions()); after != before {
		t.Fatalf("a session made %d API calls; it must make none", after-before)
	}
	if f.caches.Dynamic.HasCoveringInformer(gadgets, "alpha") {
		t.Fatal("a read started a watch")
	}
}

// waitForWatches waits until each resource's watch has been opened, after
// which informers over fake clients make no further calls.
func waitForWatches(t *testing.T, client *k8stesting.Fake, resources ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		watching := map[string]bool{}
		for _, a := range client.Actions() {
			if a.GetVerb() == "watch" {
				watching[a.GetResource().Resource] = true
			}
		}
		missing := ""
		for _, r := range resources {
			if !watching[r] {
				missing = r
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no watch for %s", missing)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCatalogIsBuiltOncePerDiscoveryGeneration(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	source := f.source()
	first := begin(t, source).Catalog()
	if again := begin(t, source).Catalog(); again != first {
		t.Fatal("an unchanged discovery generation rebuilt the catalog")
	}
	if !first.Complete() || !first.Serves(resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, "v1beta1") {
		t.Fatal("the catalog must keep every served version")
	}
	f.caches.Discovery.AddAPIResource(k8score.APIResource{Group: "example.io", Version: "v1", Kind: "Sprocket", Name: "sprockets", Namespaced: true})
	next := begin(t, source).Catalog()
	if _, ok := next.Kind(resourceid.GroupKind{Group: "example.io", Kind: "Sprocket"}); !ok || next == first {
		t.Fatal("a new discovery generation must give a new catalog")
	}
	if _, ok := first.Kind(resourceid.GroupKind{Group: "example.io", Kind: "Sprocket"}); ok {
		t.Fatal("a session's catalog changed after it began")
	}
}

func TestEveryTypedKindIsReadAtItsInformersVersion(t *testing.T) {
	enabled := map[string]bool{}
	for _, kl := range k8score.AllKindListers() {
		b, ok := typedKinds[resourceid.GroupKind{Group: kl.Group(), Kind: kl.Kind()}]
		if !ok || b.Version == "" {
			t.Fatalf("typed kind %s/%s has no built-in with a version", kl.Group(), kl.Kind())
		}
		enabled[b.Resource] = true
	}
	rc, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(), ResourceTypes: enabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rc.Stop)
	keys := map[string]string{}
	for _, status := range rc.GetSyncStatus().Informers {
		keys[status.Kind] = status.Key
	}
	for _, b := range typedKinds {
		if keys[b.Kind] != b.Resource {
			t.Errorf("%s: typed informer key %q, built-in resource %q", b.Kind, keys[b.Kind], b.Resource)
		}
	}
	if len(keys) != len(typedKinds) {
		t.Fatalf("%d typed informers for %d typed kinds", len(keys), len(typedKinds))
	}
}

func TestReasonsForEveryCacheState(t *testing.T) {
	for _, tc := range []struct {
		o    k8score.DynamicResourceObservation
		want readreason.Code
	}{
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnwatched, ReasonCode: "not_observed"}, readreason.KindNotWatched},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnwatched, ReasonCode: "cache_stopped"}, readreason.KindNotWatched},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationDeferred, ReasonCode: "eager_watch_pending"}, readreason.KindSyncPending},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationDeferred, ReasonCode: "watch_start_failed"}, readreason.KindWatchFailed},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationDeferred, ReasonCode: "scope_probe_incomplete"}, readreason.ReadError},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationDeferred}, readreason.KindNotWatched},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing, ReasonCode: "initial_sync"}, readreason.KindSyncPending},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing, ReasonCode: "sync_stalled"}, readreason.KindSyncFailed},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}, readreason.KindSyncPending},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationDenied}, readreason.CollectorForbidden},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnsupported}, readreason.KindNotWatchable},
	} {
		if got := dynamicReason(tc.o); got != tc.want {
			t.Errorf("%s/%s: %s, want %s", tc.o.State, tc.o.ReasonCode, got, tc.want)
		}
	}
}
