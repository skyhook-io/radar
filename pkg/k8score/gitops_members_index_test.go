package k8score

import (
	"fmt"
	"runtime"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/gitops"
)

// argoApplication lists entries in status.resources, compared at the
// in-cluster destination with namespace shop. A nil list is one Argo CD
// hasn't recorded yet.
func argoApplication(name string, entries []any) *unstructured.Unstructured {
	status := map[string]any{"sync": map[string]any{"comparedTo": map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"}}}}
	if entries != nil {
		status["reconciledAt"] = "2026-10-19T00:00:00Z"
		status["resources"] = entries
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": name, "namespace": "argocd"},
		"status":   status,
	}}
}

// An entry of a built-in kind is filed by the object it means; one of any
// other kind under each placement its scope could call for; a manager whose
// list can't be read, under UnreadableMembersKey alone, unless it applies to
// another cluster.
func TestMemberKeys(t *testing.T) {
	keys := memberKeys(gitops.ArgoMembers, gitops.ArgoAppliesElsewhere)
	got, _ := keys(argoApplication("web", []any{
		map[string]any{"kind": "Service", "name": "ui"},
		map[string]any{"group": "rbac.authorization.k8s.io", "kind": "ClusterRole", "namespace": "shop", "name": "reader"},
		map[string]any{"group": "example.io", "kind": "Widget", "name": "w1"},
		map[string]any{"group": "example.io", "kind": "Widget", "namespace": "alpha", "name": "w2"},
	}))
	want := []string{
		MemberKey(MemberMeant, "", "Service", "shop", "ui"),
		MemberKey(MemberMeant, "rbac.authorization.k8s.io", "ClusterRole", "", "reader"),
		MemberKey(MemberAsRecorded, "example.io", "Widget", "", "w1"),
		MemberKey(MemberInDestination, "example.io", "Widget", "shop", "w1"),
		MemberKey(MemberAsRecorded, "example.io", "Widget", "alpha", "w2"),
		MemberKey(MemberClusterScoped, "example.io", "Widget", "", "w2"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	for name, app := range map[string]*unstructured.Unstructured{
		"not reconciled":        argoApplication("web", nil),
		"an entry with no name": argoApplication("web", []any{map[string]any{"kind": "Service"}}),
		"a failed comparison": func() *unstructured.Unstructured {
			app := argoApplication("web", []any{})
			app.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "ComparisonError", "message": "revision not found"}}
			return app
		}(),
		"a comparison whose outcome is unknown": func() *unstructured.Unstructured {
			app := argoApplication("web", nil)
			status := app.Object["status"].(map[string]any)
			status["reconciledAt"] = "2026-10-19T00:00:00Z"
			status["sync"].(map[string]any)["status"] = "Unknown"
			return app
		}(),
	} {
		if got, _ := keys(app); !slices.Equal(got, []string{UnreadableMembersKey}) {
			t.Fatalf("%s: keys = %q, want only the unreadable key", name, got)
		}
	}
	// An Application applying to another cluster names nothing here when its
	// comparison fails, so it isn't filed as one any object may belong to.
	remote := argoApplication("web", []any{})
	remote.Object["status"].(map[string]any)["sync"] = map[string]any{"comparedTo": map[string]any{"destination": map[string]any{"server": "https://prod.example.com:6443", "namespace": "shop"}}}
	remote.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "ComparisonError"}}
	if got, _ := keys(remote); len(got) != 0 {
		t.Fatalf("a remote Application with a failed comparison: keys = %q, want none", got)
	}
}

// Every host's GitOps manager informers carry the members index from their
// start, cluster-wide or per namespace; other resources' don't, and a read of
// it from them refuses to answer rather than answering empty.
func TestGitOpsManagersAreIndexedByMembers(t *testing.T) {
	apps := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	kustomizations := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	helmReleases := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	widgets := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	kustomization := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "shop", "namespace": "flux-system"},
		"status":   map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "shop_ui__Service", "v": "v1"}}}},
	}}
	helmRelease := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "ui", "namespace": "flux-system"},
		"status":   map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "shop_ui__Service", "v": "v1"}}}},
	}}
	widget := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "w1", "namespace": "shop"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(apiruntime.NewScheme(), map[schema.GroupVersionResource]string{
		apps: "ApplicationList", kustomizations: "KustomizationList", helmReleases: "HelmReleaseList", widgets: "WidgetList",
	}, argoApplication("web", []any{map[string]any{"kind": "Service", "name": "ui"}}), argoApplication("pending", nil),
		argoApplication("widgets", []any{map[string]any{"group": "example.io", "kind": "Widget", "name": "w1"}}), kustomization, helmRelease, widget)
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	for _, gvr := range []schema.GroupVersionResource{apps, kustomizations, widgets} {
		if err := d.startWatching(gvr, ""); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, 5*time.Second, gvr.Resource, func() bool { return d.IsClusterWideSynced(gvr) })
	}
	if err := d.startWatching(helmReleases, "flux-system"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "flux-system's HelmReleases", func() bool { return d.IsNamespaceSynced(helmReleases, "flux-system") })
	ui := MemberKey(MemberMeant, "", "Service", "shop", "ui")
	for _, tc := range []struct {
		gvr                  schema.GroupVersionResource
		namespace, key, want string
	}{
		{apps, "", ui, "web"},
		{apps, "", UnreadableMembersKey, "pending"},
		{kustomizations, "", ui, "shop"},
		{helmReleases, "flux-system", ui, "ui"},
	} {
		got, ok := d.ReadableIndexed(tc.gvr, tc.namespace, GitOpsMembersIndex, tc.key)
		if !ok || !slices.Equal(names(got), []string{tc.want}) {
			t.Fatalf("%s under %q = %v, %v; want %s", tc.gvr.Resource, tc.key, names(got), ok, tc.want)
		}
	}
	// Several keys are read together: each manager filed under any of them,
	// once, however many of them it is filed under.
	w1 := []string{MemberKey(MemberAsRecorded, "example.io", "Widget", "", "w1"), MemberKey(MemberInDestination, "example.io", "Widget", "shop", "w1")}
	got, ok := d.ReadableIndexed(apps, "", GitOpsMembersIndex, append([]string{ui, UnreadableMembersKey}, w1...)...)
	if !ok || !slices.Equal(names(got), []string{"pending", "web", "widgets"}) {
		t.Fatalf("applications under any of the keys = %v, %v; want pending, web and widgets once each", names(got), ok)
	}
	if _, ok := d.ReadableIndexed(widgets, "", GitOpsMembersIndex, ui); ok {
		t.Fatal("a resource without the members index can't answer from it")
	}
}

// application is an Argo CD Application listing entries Deployments, half of
// them recorded without their namespace.
func application(i, entries int) *unstructured.Unstructured {
	list := make([]any, entries)
	for j := range list {
		e := map[string]any{"group": "apps", "kind": "Deployment", "name": fmt.Sprintf("app-%d-service-%d", i, j)}
		if j%2 == 0 {
			e["namespace"] = "shop"
		}
		list[j] = e
	}
	return argoApplication(fmt.Sprint("app-", i), list)
}

// BenchmarkMembersIndexRetainedBytes reports the heap the members index keeps
// per listed entry: 500 Applications of 40 entries each, held with and without
// it. Run with -bench MembersIndexRetained -run '^$' -benchtime 3x.
func BenchmarkMembersIndexRetainedBytes(b *testing.B) {
	const apps, entries = 500, 40
	objects := make([]*unstructured.Unstructured, apps)
	for i := range objects {
		objects[i] = application(i, entries)
	}
	retained := func(indexers cache.Indexers) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexers)
		for _, u := range objects {
			_ = store.Add(u)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(store)
		return after.HeapAlloc - before.HeapAlloc
	}
	namespaceOnly := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}
	withMembers := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc, GitOpsMembersIndex: memberKeys(gitops.ArgoMembers, gitops.ArgoAppliesElsewhere)}
	var extra float64
	for range b.N {
		extra += float64(retained(withMembers)) - float64(retained(namespaceOnly))
	}
	b.ReportMetric(extra/float64(b.N)/(apps*entries), "B/entry")
}

// BenchmarkMembersIndexUpdate reports what the index adds to one store update
// of an Application, as Argo CD writes after each reconcile.
func BenchmarkMembersIndexUpdate(b *testing.B) {
	for _, entries := range []int{40, 400} {
		for name, indexers := range map[string]cache.Indexers{
			"without": {cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			"with":    {cache.NamespaceIndex: cache.MetaNamespaceIndexFunc, GitOpsMembersIndex: memberKeys(gitops.ArgoMembers, gitops.ArgoAppliesElsewhere)},
		} {
			b.Run(fmt.Sprintf("%d-entries-%s", entries, name), func(b *testing.B) {
				store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexers)
				app := application(0, entries)
				_ = store.Add(app)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					_ = store.Update(app)
				}
			})
		}
	}
}

// interleavingIndexer counts index reads and, after the first, moves its
// manager from one key to another, as an update landing between two reads
// would.
type interleavingIndexer struct {
	cache.Indexer
	next  *unstructured.Unstructured
	reads int
}

func (i *interleavingIndexer) ByIndex(index, key string) ([]any, error) {
	hits, err := i.Indexer.ByIndex(index, key)
	i.read()
	return hits, err
}

func (i *interleavingIndexer) Index(index string, obj any) ([]any, error) {
	hits, err := i.Indexer.Index(index, obj)
	i.read()
	return hits, err
}

func (i *interleavingIndexer) read() {
	if i.reads++; i.reads == 1 {
		_ = i.Indexer.Update(i.next)
	}
}

type syncedInformer struct {
	cache.SharedIndexInformer
	indexer cache.Indexer
}

func (i syncedInformer) HasSynced() bool           { return true }
func (i syncedInformer) GetIndexer() cache.Indexer { return i.indexer }

// Several keys are read together, in one pass under the store's lock: a
// manager whose list stops naming the object between reads of one key and the
// next is still found under one of them.
func TestReadableIndexedReadsItsKeysTogether(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{GitOpsMembersIndex: memberKeys(gitops.ArgoMembers, gitops.ArgoAppliesElsewhere)})
	if err := indexer.Add(argoApplication("web", []any{map[string]any{"kind": "Service", "name": "ui"}})); err != nil {
		t.Fatal(err)
	}
	interleaving := &interleavingIndexer{Indexer: indexer, next: argoApplication("web", nil)}
	d := &DynamicResourceCache{informers: map[informerKey]*informerEntry{{gvr: gvr}: {informer: syncedInformer{indexer: interleaving}}}}
	objs, ok := d.ReadableIndexed(gvr, "", GitOpsMembersIndex, UnreadableMembersKey, MemberKey(MemberMeant, "", "Service", "shop", "ui"))
	if !ok || len(objs) != 1 {
		t.Fatalf("found %v (ok %v); the manager moved between keys and was lost", objs, ok)
	}
	if interleaving.reads != 1 {
		t.Fatalf("%d index reads; the keys must be read in one", interleaving.reads)
	}
}

// A read of several owner UIDs takes them together, as a read of several
// member keys does: the owner index answers the union query too.
func TestReadableIndexedReadsSeveralOwnerUIDs(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{OwnerUIDIndex: ownerUIDs})
	for name, owner := range map[string]string{"a": "uid-1", "b": "uid-2", "c": "uid-3"} {
		u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{
			"namespace": "alpha", "name": name,
			"ownerReferences": []any{map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "name": "owner", "uid": owner}},
		}}}
		if err := indexer.Add(u); err != nil {
			t.Fatal(err)
		}
	}
	d := &DynamicResourceCache{informers: map[informerKey]*informerEntry{{gvr: gvr}: {informer: syncedInformer{indexer: indexer}}}}
	objs, ok := d.ReadableIndexed(gvr, "", OwnerUIDIndex, "uid-1", "uid-2")
	var names []string
	for _, o := range objs {
		names = append(names, o.GetName())
	}
	slices.Sort(names)
	if !ok || !slices.Equal(names, []string{"a", "b"}) {
		t.Fatalf("found %v (ok %v), want the dependents of both owners", names, ok)
	}
}
