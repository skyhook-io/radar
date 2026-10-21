package cachesource

import (
	"errors"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clientscheme "k8s.io/client-go/kubernetes/scheme"
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
	return metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(uid), ResourceVersion: "rv-" + name}
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

// fullWidget carries every field ownership reads, so a copy that drops or
// shares one shows.
func fullWidget() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1", "kind": "Widget",
		"metadata": map[string]any{
			"name": "w2", "namespace": "alpha", "uid": "uid-w2", "resourceVersion": "rv-w2",
			"deletionTimestamp": deletedAt.UTC().Format(time.RFC3339),
			"finalizers":        []any{"example.io/keep"},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment", "name": "web", "uid": "uid-deploy",
				"controller": true, "blockOwnerDeletion": false,
			}},
		},
	}}
}

func widget(namespace, name, uid string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1", "kind": "Widget",
		"metadata": map[string]any{
			"name": name, "namespace": namespace, "uid": uid, "resourceVersion": "rv-" + name,
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
func newFixture(t testing.TB, cfg k8score.CacheConfig, prepare func(*fixture)) *fixture {
	t.Helper()
	f := &fixture{
		typed: fake.NewClientset(typedObjects()...),
		dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{widgets: "WidgetList", gadgets: "GadgetList", summaries: "SummaryList"},
			widget("alpha", "w1", "uid-widget"), fullWidget()),
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

func begin(t testing.TB, s *Source) ownership.ReadSession {
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

	read := s.Object(ref("apps", "ReplicaSet", "alpha", "web-1"))
	want := ownership.Observation{
		Ref:             ref("apps", "ReplicaSet", "alpha", "web-1"),
		APIVersion:      "apps/v1",
		UID:             "uid-rs",
		ResourceVersion: "rv-web-1",
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
	*read.Object.OwnerReferences[0].BlockOwnerDeletion = false
	read.Object.OwnerReferences[1].Name = "changed"
	if !*cached.OwnerReferences[0].Controller || !*cached.OwnerReferences[0].BlockOwnerDeletion || cached.OwnerReferences[1].Name != "w1" {
		t.Fatal("changing an observation changed the cached object")
	}

	if pod := s.Object(ref("", "Pod", "alpha", "web-1-a")); pod.State != ownership.Present || pod.Object.APIVersion != "v1" {
		t.Fatalf("Pod = %v %+v", pod.State, pod.Object)
	}
	// The other direction, checked by identity: writing to the cache would
	// race the informers that own it.
	if rs := s.Object(ref("apps", "ReplicaSet", "alpha", "web-1")); &rs.Object.OwnerReferences[0] == &cached.OwnerReferences[0] ||
		rs.Object.OwnerReferences[0].Controller == cached.OwnerReferences[0].Controller ||
		rs.Object.OwnerReferences[0].BlockOwnerDeletion == cached.OwnerReferences[0].BlockOwnerDeletion {
		t.Fatal("an observation shares the cached ReplicaSet's owner references")
	}

	w := s.Object(ref("example.io", "Widget", "alpha", "w1"))
	wantOwners := []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "uid-deploy"}}
	if w.State != ownership.Present || w.Object.APIVersion != "example.io/v1" || w.Object.UID != "uid-widget" ||
		!reflect.DeepEqual(w.Object.OwnerReferences, wantOwners) {
		t.Fatalf("Widget = %v %+v", w.State, w.Object)
	}
	w.Object.OwnerReferences[0].Name = "changed"
	if again := s.Object(ref("example.io", "Widget", "alpha", "w1")); again.Object.OwnerReferences[0].Name != "web" {
		t.Fatal("changing an observation changed the cached Widget")
	}

	w2 := s.Object(ref("example.io", "Widget", "alpha", "w2"))
	wantW2 := ownership.Observation{
		Ref: ref("example.io", "Widget", "alpha", "w2"), APIVersion: "example.io/v1", UID: "uid-w2", ResourceVersion: "rv-w2",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "uid-deploy", Controller: ptr(true), BlockOwnerDeletion: ptr(false)}},
	}
	if w2.State != ownership.Present || !reflect.DeepEqual(w2.Object, wantW2) {
		t.Fatalf("w2 = %v %+v\nwant present %+v", w2.State, w2.Object, wantW2)
	}
	*w2.Object.OwnerReferences[0].Controller = false
	*w2.Object.OwnerReferences[0].BlockOwnerDeletion = true
	reread := s.Object(ref("example.io", "Widget", "alpha", "w2"))
	if !*reread.Object.OwnerReferences[0].Controller || *reread.Object.OwnerReferences[0].BlockOwnerDeletion {
		t.Fatal("changing an observation changed the cached Widget's owner flags")
	}

	if node := s.Object(ref("", "Node", "", "n1")); node.State != ownership.Present || node.Object.UID != "uid-node" {
		t.Fatalf("Node = %v %+v", node.State, node.Object)
	}
	// Radar keeps none of an Event's owner references, so ownership can't read
	// one, in either Event group.
	for _, group := range []string{"", "events.k8s.io"} {
		if event := s.Object(ref(group, "Event", "alpha", "e1")); event.State != ownership.Excluded {
			t.Fatalf("Event in %q = %v, want excluded", group, event.State)
		}
	}
	if err := s.ValidateCaches(); err != nil {
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
		{"typed kind Radar doesn't collect", ref("", "Secret", "alpha", "s"), readreason.RadarKindNotCollected},
		{"known kind with no watch", ref("example.io", "Gadget", "alpha", "g"), readreason.RadarKindNotCollected},
		{"kind whose watch was refused", ref("example.io", "Widget", "alpha", "w1"), readreason.RadarForbidden},
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

// A 401 rejects Radar's credentials rather than refusing one kind, so the
// remedy is new credentials, not more access.
func TestObjectOfAKindWhoseCredentialsWereRejected(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		f.dynamic.PrependReactor("list", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewUnauthorized("token rejected")
		})
	})
	if err := f.caches.Dynamic.EnsureWatching(widgets); !apierrors.IsUnauthorized(err) {
		t.Fatalf("scenario: EnsureWatching err=%v, want the credential rejection", err)
	}
	r := ref("example.io", "Widget", "alpha", "w1")
	got := begin(t, f.source()).Object(r)
	want := ownership.Gap{Kind: r.GroupKind(), Namespace: r.Namespace, Reason: readreason.RadarCredentialsRejected}
	if got.State != ownership.Unavailable || got.Gap != want {
		t.Fatalf("%v = %v %+v; want unavailable %+v", r, got.State, got.Gap, want)
	}
}

// A stopped cache no longer follows the cluster, so a read after the stop is
// a connection change, not a kind Radar leaves out.
func TestObjectReadAfterTheCachesStopped(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	if err := f.caches.Dynamic.EnsureWatching(widgets); err != nil {
		t.Fatal(err)
	}
	if !f.caches.Dynamic.WaitForSync(widgets, 5*time.Second) {
		t.Fatal("widgets did not sync")
	}
	r := ref("example.io", "Widget", "alpha", "w1")
	s := begin(t, f.source())
	if got := s.Object(r); got.State != ownership.Present {
		t.Fatalf("%v before the stop = %v %+v", r, got.State, got.Gap)
	}
	f.caches.Dynamic.Stop()
	got := s.Object(r)
	want := ownership.Gap{Kind: r.GroupKind(), Namespace: r.Namespace, Reason: readreason.ConnectionChanged}
	if got.State != ownership.Unavailable || got.Gap != want {
		t.Fatalf("%v after the stop = %v %+v; want unavailable %+v", r, got.State, got.Gap, want)
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

// A typed kind whose load missed its deadline may still finish, but repeating
// the read doesn't restart it.
func TestObjectOfATypedKindPastItsLoadDeadline(t *testing.T) {
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
	r := ref("", "Secret", "alpha", "s")
	got := begin(t, f.source()).Object(r)
	want := ownership.Gap{Kind: r.GroupKind(), Namespace: "alpha", Reason: readreason.KindSyncTimedOut}
	if got.State != ownership.Unavailable || got.Gap != want {
		t.Fatalf("%v past its load deadline = %v %+v; want unavailable %+v", r, got.State, got.Gap, want)
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
	if want := (ownership.Gap{Kind: resourceid.GroupKind{Kind: "Pod"}, Namespace: "beta", Reason: readreason.RadarNamespaceNotCollected}); got.State != ownership.Unavailable || got.Gap != want {
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

// A watch replaced by another within the same caches, as when a cluster-wide
// watch takes over from namespace ones, fails nothing: reads go to whatever
// informers serve now, and an unloaded replacement proves nothing until it
// loads.
func TestReadsFollowAWatchReplacement(t *testing.T) {
	f, p := newPromotionFixture(t)
	source := f.source()
	before := begin(t, source)
	if got := before.Object(ref("example.io", "Widget", "alpha", "w1")); got.State != ownership.Present {
		t.Fatalf("Widget from alpha's watch = %v %+v", got.State, got.Gap)
	}
	if err := before.ValidateCaches(); err != nil {
		t.Fatal(err)
	}

	p.run(t, f, true)
	if err := before.ValidateCaches(); err != nil {
		t.Fatalf("ValidateCaches after a watch replacement = %v; only replaced caches fail it", err)
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
	if err := during.ValidateCaches(); err != nil {
		t.Fatalf("a session that read only the replacement: %v", err)
	}
}

func TestValidateFailsOnceTheCachesAreRetired(t *testing.T) {
	first := newFixture(t, k8score.CacheConfig{}, nil)
	second := newFixture(t, k8score.CacheConfig{}, nil)
	current := first.caches
	source := New(func() Caches { return current })

	s := begin(t, source)
	current = second.caches
	if err := s.ValidateCaches(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("after a switch: %v, want ErrCachesReplaced", err)
	}

	s = begin(t, source)
	second.caches.Dynamic.Stop()
	if err := s.ValidateCaches(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("after the dynamic cache stopped: %v, want ErrCachesReplaced", err)
	}
	if _, err := source.Begin(); !errors.Is(err, ownership.ErrNotConnected) {
		t.Fatalf("Begin over a stopped cache = %v, want ErrNotConnected", err)
	}

	current = first.caches
	s = begin(t, source)
	first.caches.Typed.Stop()
	if err := s.ValidateCaches(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("after the typed cache stopped: %v, want ErrCachesReplaced", err)
	}

	current = Caches{}
	if _, err := source.Begin(); !errors.Is(err, ownership.ErrNotConnected) {
		t.Fatalf("Begin with no caches = %v, want ErrNotConnected", err)
	}
}

// Radar publishes its typed cache before its dynamic cache and discovery, so a
// session can begin without them; their arrival replaces nothing it read.
func TestValidateAcceptsCachesThatArriveAfterBegin(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	f.watch(t, widgets)
	current := Caches{Typed: f.caches.Typed}
	source := New(func() Caches { return current })
	s := begin(t, source)
	if got := s.Object(ref("example.io", "Widget", "alpha", "w1")); got.State != ownership.Unavailable {
		t.Fatalf("Widget before the dynamic cache and discovery = %v", got.State)
	}
	current = f.caches
	if err := s.ValidateCaches(); err != nil {
		t.Fatalf("ValidateCaches once the dynamic cache and discovery arrive = %v, want nil", err)
	}

	other := newFixture(t, k8score.CacheConfig{}, nil)
	s = begin(t, source)
	current.Dynamic = other.caches.Dynamic
	if err := s.ValidateCaches(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("after the dynamic cache was replaced: %v, want ErrCachesReplaced", err)
	}
	current = f.caches
	s = begin(t, source)
	current.Discovery = other.caches.Discovery
	if err := s.ValidateCaches(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("after discovery was replaced: %v, want ErrCachesReplaced", err)
	}
	current = f.caches
	s = begin(t, source)
	current.Dynamic = nil
	if err := s.ValidateCaches(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("after the dynamic cache went away: %v, want ErrCachesReplaced", err)
	}
	// A typed cache replaced by one still running is a switch even before
	// the old one stops.
	current = f.caches
	s = begin(t, source)
	current.Typed = other.caches.Typed
	if err := s.ValidateCaches(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("after the typed cache was replaced: %v, want ErrCachesReplaced", err)
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
	if err := s.ValidateCaches(); err != nil {
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
	var objects []runtime.Object
	for _, kl := range k8score.AllKindListers() {
		b, ok := typedKinds[resourceid.GroupKind{Group: kl.Group(), Kind: kl.Kind()}]
		if !ok || b.Version == "" {
			t.Fatalf("typed kind %s/%s has no built-in with a version", kl.Group(), kl.Kind())
		}
		enabled[b.Resource] = true
		obj, err := clientscheme.Scheme.New(schema.GroupVersionKind{Group: b.Group, Version: b.Version, Kind: b.Kind})
		if err != nil {
			t.Fatalf("%s: %v", b.Kind, err)
		}
		m, err := apimeta.Accessor(obj)
		if err != nil {
			t.Fatal(err)
		}
		m.SetName("probe")
		if b.Namespaced {
			m.SetNamespace("alpha")
		}
		objects = append(objects, obj)
	}
	rc, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(objects...), ResourceTypes: enabled})
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
	// An object created at the built-in's version reaches the store only if
	// the informer lists that version, and the store must hold that version's
	// type: the session reports the built-in's version as the object's.
	for _, b := range typedKinds {
		namespace := ""
		if b.Namespaced {
			namespace = "alpha"
		}
		waitUntil(t, b.Kind+"'s store", func() bool {
			_, _, ok := rc.ReadableObject(b.Resource, namespace, "probe")
			return ok
		})
		obj, found, _ := rc.ReadableObject(b.Resource, namespace, "probe")
		if !found {
			t.Errorf("%s created at %s isn't in its store: the informer lists another version", b.Kind, b.APIVersion())
			continue
		}
		gvks, _, err := clientscheme.Scheme.ObjectKinds(obj)
		if err != nil || !slices.Contains(gvks, schema.GroupVersionKind{Group: b.Group, Version: b.Version, Kind: b.Kind}) {
			t.Errorf("%s's store holds %T (%v); the session reports it at %s", b.Kind, obj, gvks, b.APIVersion())
		}
		if ownership.IsEvent(b.GroupKind()) {
			continue
		}
		got := begin(t, New(func() Caches { return Caches{Typed: rc} })).Object(ref(b.Group, b.Kind, namespace, "probe"))
		if got.State != ownership.Present || got.Object.APIVersion != b.APIVersion() {
			t.Errorf("%s = %v at %q; want present at %s", b.Kind, got.State, got.Object.APIVersion, b.APIVersion())
		}
	}
}

func TestReasonsForEveryCacheState(t *testing.T) {
	for _, tc := range []struct {
		o    k8score.DynamicResourceObservation
		want readreason.Code
	}{
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnwatched, ReasonCode: "not_observed"}, readreason.RadarKindNotCollected},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnwatched, ReasonCode: "cache_stopped"}, readreason.ConnectionChanged},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationDeferred, ReasonCode: "eager_watch_pending"}, readreason.KindSyncPending},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationDeferred, ReasonCode: "scope_probe_incomplete"}, readreason.ReadError},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationDeferred}, readreason.RadarKindNotCollected},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing, ReasonCode: "initial_sync"}, readreason.KindSyncPending},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing, ReasonCode: "sync_stalled"}, readreason.KindSyncTimedOut},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}, readreason.KindSyncPending},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationDenied}, readreason.RadarForbidden},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationDenied, ReasonCode: "credentials_rejected"}, readreason.RadarCredentialsRejected},
		{k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnsupported}, readreason.KindNotWatchable},
	} {
		if got := dynamicReason(tc.o); got != tc.want {
			t.Errorf("%s/%s: %s, want %s", tc.o.State, tc.o.ReasonCode, got, tc.want)
		}
	}
}

// Radar's access probe leaves a typed kind off when Kubernetes refuses it,
// when the probe can't tell, and when the kind isn't installed. The cache
// knows only that it is off.
func TestObjectOfATypedKindLeftOffIsNotWatched(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{ResourceScopes: map[string]k8score.ResourceScope{
		k8score.Pods:    {Enabled: true},
		k8score.Secrets: {},
	}}, nil)
	s := begin(t, f.source())
	for _, r := range []resourceid.Ref{ref("", "Secret", "alpha", "s"), ref("", "ConfigMap", "alpha", "c")} {
		if got := s.Object(r); got.State != ownership.Unavailable || got.Gap.Reason != readreason.RadarKindNotCollected {
			t.Fatalf("%v = %v %+v; want unavailable radar_kind_not_collected", r, got.State, got.Gap)
		}
	}
}

func TestObjectOfAnAmbiguousKindIsUnavailable(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		lists := f.typed.Discovery().(*fakediscovery.FakeDiscovery).Resources
		lists = append(lists, &metav1.APIResourceList{GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{
			{Name: "morewidgets", Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		}})
		f.typed.Discovery().(*fakediscovery.FakeDiscovery).Resources = lists
	})
	f.watch(t, widgets)
	s := begin(t, f.source())
	if got := s.Object(ref("example.io", "Widget", "alpha", "w1")); got.State != ownership.Unavailable || got.Gap.Reason != readreason.ReadError {
		t.Fatalf("Widget served by two resources = %v %+v; want unavailable read_error", got.State, got.Gap)
	}
}

// versionsFixture serves Widgets at v1 and v1beta1 from separate stores, as
// two watches of one resource are when one of them is behind.
func versionsFixture(t *testing.T, v1, v1beta1 []runtime.Object, hold func(version string) bool) *fixture {
	t.Helper()
	beta := schema.GroupVersionResource{Group: "example.io", Version: "v1beta1", Resource: "widgets"}
	lists := map[string]*atomic.Int32{"v1": {}, "v1beta1": {}}
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		f.dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{widgets: "WidgetList", beta: "WidgetList", gadgets: "GadgetList", summaries: "SummaryList"},
			append(v1, v1beta1...)...)
		// Each version's first list is its watch's access probe.
		f.dynamic.PrependReactor("list", "widgets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			v := a.GetResource().Version
			if lists[v].Add(1) > 1 && hold != nil && hold(v) {
				return true, nil, errors.New("initial list held")
			}
			return false, nil, nil
		})
	})
	for _, gvr := range []schema.GroupVersionResource{widgets, beta} {
		if err := f.caches.Dynamic.EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
		if hold == nil || !hold(gvr.Version) {
			if !f.caches.Dynamic.WaitForSync(gvr, 5*time.Second) {
				t.Fatalf("%v did not sync", gvr)
			}
		}
	}
	return f
}

func betaWidget(u *unstructured.Unstructured) *unstructured.Unstructured {
	u.SetAPIVersion("example.io/v1beta1")
	return u
}

func atVersion(u *unstructured.Unstructured, rv string) *unstructured.Unstructured {
	u.SetResourceVersion(rv)
	return u
}

func adopted(u *unstructured.Unstructured) *unstructured.Unstructured {
	u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "example.io/v1", Kind: "Gadget", Name: "new-owner", UID: "uid-new-owner", Controller: ptr(true)}})
	return u
}

func TestObjectNeedsEveryWatchedVersionToAgree(t *testing.T) {
	w1 := ref("example.io", "Widget", "alpha", "w1")
	for _, tc := range []struct {
		name        string
		v1, v1beta1 []runtime.Object
		hold        func(string) bool
		want        ownership.ReadState
		wantVersion string
		wantReason  readreason.Code
	}{
		{name: "both hold it", v1: []runtime.Object{widget("alpha", "w1", "uid-widget")}, v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-widget"))},
			want: ownership.Present, wantVersion: "example.io/v1"},
		{name: "neither holds it", want: ownership.Absent},
		{name: "only one holds it", v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-widget"))},
			want: ownership.Unavailable, wantReason: readreason.KindSyncPending},
		{name: "two incarnations", v1: []runtime.Object{widget("alpha", "w1", "uid-new")}, v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-old"))},
			want: ownership.Unavailable, wantReason: readreason.KindSyncPending},
		{name: "only the loaded one answers", v1: []runtime.Object{widget("alpha", "w1", "uid-widget")}, v1beta1: []runtime.Object{betaWidget(widget("alpha", "w1", "uid-widget"))},
			hold: func(v string) bool { return v == "v1" }, want: ownership.Present, wantVersion: "example.io/v1beta1"},
		// One watch holds an update, such as an adoption, the other hasn't
		// received: the same incarnation at two resource versions.
		{name: "one holds a later update", v1: []runtime.Object{atVersion(widget("alpha", "w1", "uid-widget"), "41")},
			v1beta1: []runtime.Object{betaWidget(adopted(atVersion(widget("alpha", "w1", "uid-widget"), "42")))},
			want:    ownership.Unavailable, wantReason: readreason.KindSyncPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := versionsFixture(t, tc.v1, tc.v1beta1, tc.hold)
			s := begin(t, f.source())
			got := s.Object(w1)
			if got.State != tc.want || got.Object.APIVersion != tc.wantVersion || got.Gap.Reason != tc.wantReason {
				t.Fatalf("Widget = %v at %q %+v; want %v at %q reason %q", got.State, got.Object.APIVersion, got.Gap, tc.want, tc.wantVersion, tc.wantReason)
			}
		})
	}
}

// A search over two watched versions needs them to hold each object at the
// same update, as a read does: one watch holding an adoption the other lacks
// leaves the namespace loading.
func TestDependentCandidatesNeedVersionsToAgreeOnTheUpdate(t *testing.T) {
	for name, domain := range map[string]ownership.SearchScope{"in alpha": ownership.InNamespace("alpha"), "everywhere": ownership.Everywhere()} {
		t.Run(name, func(t *testing.T) {
			f := versionsFixture(t, []runtime.Object{atVersion(widget("alpha", "w1", "uid-w1"), "41")}, []runtime.Object{betaWidget(atVersion(widget("alpha", "w1", "uid-w1"), "42"))}, nil)
			got := begin(t, f.source()).DependentCandidates("uid-deploy", domain, 0)
			widget := resourceid.GroupKind{Group: "example.io", Kind: "Widget"}
			listed := slices.ContainsFunc(got.Candidates, func(c ownership.Observation) bool { return c.Ref.GroupKind() == widget })
			loading := slices.ContainsFunc(got.Gaps, func(g ownership.Gap) bool { return g.Kind == widget && g.Reason == readreason.KindSyncPending })
			if listed || !loading || got.Complete() {
				t.Fatalf("got %+v; the two versions hold w1 at two updates", got)
			}
		})
	}
}

// When no watched version answers, a version watched where the read is says
// why: v1 watched only in alpha says nothing about beta, while v1beta1's
// cluster-wide watch is loading, so a retry gets its answer, or has stalled.
func TestObjectNoVersionAnsweredSaysWhyFromTheVersionWatchingIt(t *testing.T) {
	beta := schema.GroupVersionResource{Group: "example.io", Version: "v1beta1", Resource: "widgets"}
	for _, tc := range []struct {
		name       string
		stallAfter time.Duration
		want       readreason.Code
	}{
		{"loading", 0, readreason.KindSyncPending},
		{"stalled", time.Millisecond, readreason.KindSyncTimedOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var betaLists atomic.Int32
			f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
				f.dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
					map[schema.GroupVersionResource]string{widgets: "WidgetList", beta: "WidgetList", gadgets: "GadgetList", summaries: "SummaryList"})
				f.dynamic.PrependReactor("list", "widgets", func(a k8stesting.Action) (bool, runtime.Object, error) {
					switch {
					case a.GetResource().Version == "v1" && a.GetNamespace() == "":
						return true, nil, apierrors.NewForbidden(widgets.GroupResource(), "", errors.New("cluster-wide list denied"))
					case a.GetResource().Version == "v1beta1" && betaLists.Add(1) > 1:
						// The first list is the watch's access probe.
						return true, nil, errors.New("initial list held")
					}
					return false, nil, nil
				})
			})
			if tc.stallAfter > 0 {
				dyn, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: f.dynamic, Discovery: f.caches.Discovery, SyncStallAfter: tc.stallAfter})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(dyn.Stop)
				f.caches.Dynamic = dyn
			}
			if _, err := f.caches.Dynamic.List(widgets, "alpha"); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "alpha's v1 watch", func() bool { return f.caches.Dynamic.IsNamespaceSynced(widgets, "alpha") })
			if err := f.caches.Dynamic.EnsureWatching(beta); err != nil {
				t.Fatal(err)
			}
			r := ref("example.io", "Widget", "beta", "w1")
			want := ownership.Gap{Kind: r.GroupKind(), Namespace: "beta", Reason: tc.want}
			if tc.stallAfter > 0 {
				waitUntil(t, "v1beta1's stalled load", func() bool { return begin(t, f.source()).Object(r).Gap == want })
			}
			got := begin(t, f.source()).Object(r)
			if got.State != ownership.Unavailable || got.Gap != want {
				t.Fatalf("%v = %v %+v; want unavailable %+v", r, got.State, got.Gap, want)
			}
		})
	}
}

// A watch of a version discovery no longer serves can outlive it; its store
// isn't evidence about the kind as the catalog has it.
func TestObjectIgnoresAWatchOfAnUnservedVersion(t *testing.T) {
	alpha := schema.GroupVersionResource{Group: "example.io", Version: "v1alpha1", Resource: "widgets"}
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		f.dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{widgets: "WidgetList", alpha: "WidgetList", gadgets: "GadgetList", summaries: "SummaryList"})
	})
	f.caches.Discovery.AddAPIResource(k8score.APIResource{Group: "example.io", Version: "v1alpha1", Kind: "Widget", Name: "widgets", Namespaced: true, Verbs: []string{"list", "watch"}})
	f.watch(t, alpha)
	f.typed.Discovery().(*fakediscovery.FakeDiscovery).Resources = discoveryLists()
	if err := f.caches.Discovery.Refresh(); err != nil {
		t.Fatal(err)
	}
	s := begin(t, f.source())
	if got := s.Object(ref("example.io", "Widget", "alpha", "w1")); got.State != ownership.Unavailable || got.Gap.Reason != readreason.RadarKindNotCollected {
		t.Fatalf("Widget with only an unserved version watched = %v %+v; want unavailable radar_kind_not_collected", got.State, got.Gap)
	}
}

func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A connection without discovery still reads its typed kinds; kinds that need
// discovery's catalog can't be read, and none is called unserved.
func TestBeginWithoutDiscoveryReadsTypedKinds(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	s, err := New(func() Caches { return Caches{Typed: f.caches.Typed} }).Begin()
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Object(ref("apps", "ReplicaSet", "alpha", "web-1")); got.State != ownership.Present {
		t.Fatalf("ReplicaSet without discovery = %v %+v", got.State, got.Gap)
	}
	if got := s.Object(ref("example.io", "Widget", "alpha", "w1")); got.State != ownership.Unavailable || got.Gap.Reason != readreason.ReadError {
		t.Fatalf("Widget without discovery = %v %+v; want read_error", got.State, got.Gap)
	}
	if s.Catalog().Complete() {
		t.Fatal("no discovery can't be a complete catalog")
	}
	if err := s.ValidateCaches(); err != nil {
		t.Fatal(err)
	}
}

// A catalog belongs to one discovery, not to a generation number: after a
// context switch the new cluster's discovery can be at the same generation as
// the old one's, and its sessions must still read its own catalog.
func TestSessionsReadTheCatalogOfTheirOwnDiscovery(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	client := fake.NewClientset()
	client.Discovery().(*fakediscovery.FakeDiscovery).Resources = append(discoveryLists(), &metav1.APIResourceList{
		GroupVersion: "other.example.io/v1",
		APIResources: []metav1.APIResource{{Name: "gizmos", Kind: "Gizmo", Namespaced: true, Verbs: []string{"delete", "get", "list", "watch"}}},
	})
	other, err := k8score.NewResourceDiscovery(client.Discovery())
	if err != nil {
		t.Fatal(err)
	}
	if other.Generation() != f.caches.Discovery.Generation() {
		t.Fatalf("generations %d and %d; the test needs them equal", other.Generation(), f.caches.Discovery.Generation())
	}
	current := f.caches
	source := New(func() Caches { return current })
	gizmo := resourceid.GroupKind{Group: "other.example.io", Kind: "Gizmo"}
	if _, known := begin(t, source).Catalog().Kind(gizmo); known {
		t.Fatal("the first cluster's catalog lists Gizmo")
	}
	current.Discovery = other
	if _, known := begin(t, source).Catalog().Kind(gizmo); !known {
		t.Fatal("a session over the second cluster's discovery read the first one's catalog")
	}
}
