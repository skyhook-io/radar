package k8score

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func TestAssessKindReadScopeAndReadiness(t *testing.T) {
	ready := true
	rc := &ResourceCache{
		enabledResources:  map[string]bool{Pods: true},
		config:            CacheConfig{ResourceScopes: map[string]ResourceScope{Pods: {Enabled: true, Namespace: "alpha"}}, ResourceScopeNamespaces: map[string][]string{Pods: {"alpha", "beta"}}},
		informerStatuses:  []InformerSyncStatus{{Key: Pods}},
		informerHasSynced: []func() bool{func() bool { return ready }},
	}
	partial := rc.AssessKindRead(Pods, []string{"alpha", "gamma"})
	if partial.Complete || !slices.Equal(partial.Read, []string{"alpha"}) || !slices.Equal(partial.Uncovered, []string{"gamma"}) {
		t.Fatalf("assessment = %+v", partial)
	}
	if !rc.AssessKindRead(Pods, []string{"beta"}).Complete {
		t.Fatal("covered target should be complete")
	}
	empty := rc.AssessKindRead(Pods, []string{})
	if !empty.Complete || empty.Requested == nil || empty.Read == nil || empty.AllNamespaces {
		t.Fatalf("empty finite target became unrestricted: %+v", empty)
	}
	all := rc.AssessKindRead(Pods, nil)
	if all.Complete || all.AllNamespaces || len(all.Read) != 2 {
		t.Fatalf("unscoped assessment = %+v", all)
	}
	// Without per-namespace stores the kind's readiness is every namespace's.
	ready = false
	pending := rc.AssessKindRead(Pods, []string{"alpha", "gamma"})
	if pending.Readiness != KindPending || len(pending.Read) != 0 || pending.Read == nil || pending.Complete ||
		!slices.Equal(pending.Loading, []string{"alpha"}) || !slices.Equal(pending.Uncovered, []string{"gamma"}) {
		t.Fatalf("pending scope = %+v", pending)
	}
	rc.deferredFailed.Store(true)
	rc.deferredSynced = map[string]bool{Pods: false}
	failed := rc.AssessKindRead(Pods, []string{"alpha", "gamma"})
	if failed.Readiness != KindFailed || len(failed.Read) != 0 || failed.Complete ||
		!slices.Equal(failed.Failed, []string{"alpha"}) || !slices.Equal(failed.Uncovered, []string{"gamma"}) {
		t.Fatalf("failed scope = %+v", failed)
	}
	if unavailable := rc.AssessKindRead(Services, nil); unavailable.Complete || unavailable.AllNamespaces {
		t.Fatalf("unavailable = %+v", unavailable)
	}
	var absent *ResourceCache
	if absent.AssessKindRead(Pods, nil).Complete {
		t.Fatal("nil cache established completeness")
	}
	ready = true
	rc.config.ResourceScopes[Pods] = ResourceScope{Enabled: true}
	cluster := rc.AssessKindRead(Pods, nil)
	if !cluster.Complete || !cluster.AllNamespaces || cluster.Read != nil {
		t.Fatalf("cluster = %+v", cluster)
	}
	// An empty target is complete on either kind of cache, loaded or not.
	ready = false
	if loading := rc.AssessKindRead(Pods, []string{}); !loading.Complete || loading.TargetReadiness() != KindReady {
		t.Fatalf("empty target on a loading cluster-wide cache = %+v", loading)
	}
	// The deadline set above still applies: a named target takes the kind's state.
	if named := rc.AssessKindRead(Pods, []string{"alpha"}); named.Complete || named.TargetReadiness() != KindFailed {
		t.Fatalf("named target on a loading cluster-wide cache = %+v", named)
	}
}

func TestAssessKindReadRequestedScope(t *testing.T) {
	rc := &ResourceCache{
		enabledResources:  map[string]bool{Pods: true},
		config:            CacheConfig{ResourceScopes: map[string]ResourceScope{Pods: {Enabled: true, Namespace: "alpha"}}, ResourceScopeNamespaces: map[string][]string{Pods: {"alpha", "beta"}}},
		informerStatuses:  []InformerSyncStatus{{Key: Pods}},
		informerHasSynced: []func() bool{func() bool { return true }},
	}
	for _, tt := range []struct {
		name                       string
		requested, read, uncovered []string
		complete                   bool
	}{
		{"all", nil, []string{"alpha", "beta"}, nil, false},
		{"explicit order", []string{"beta", "alpha"}, []string{"beta", "alpha"}, nil, true},
		{"partial", []string{"beta", "gamma"}, []string{"beta"}, []string{"gamma"}, false},
		{"zero overlap", []string{"gamma"}, []string{}, []string{"gamma"}, false},
		{"no target", []string{}, []string{}, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := rc.AssessKindRead(Pods, tt.requested)
			if got.Readiness != KindReady || got.Complete != tt.complete || !slices.Equal(got.Read, tt.read) || !slices.Equal(got.Uncovered, tt.uncovered) {
				t.Fatalf("assessment = %+v", got)
			}
			if got.Read == nil {
				t.Fatal("a namespace-scoped collector's read became all namespaces")
			}
		})
	}
	rc.config.ResourceScopes[Pods] = ResourceScope{Enabled: true}
	if got := rc.AssessKindRead(Pods, []string{}); !got.Complete || !got.AllNamespaces || got.Read == nil || len(got.Read) != 0 {
		t.Fatalf("cluster-wide no-target = %+v", got)
	}
	rc.informerHasSynced = []func() bool{func() bool { return false }}
	if got := rc.AssessKindRead(Pods, []string{"alpha"}); got.Readiness != KindPending || !got.AllNamespaces || len(got.Read) != 0 || !slices.Equal(got.Loading, []string{"alpha"}) || got.Complete {
		t.Fatalf("cluster-wide pending = %+v", got)
	}
}

func podStore(t *testing.T, synced *bool, pods ...*corev1.Pod) kindStore {
	t.Helper()
	idx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, p := range pods {
		if err := idx.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	return kindStore{indexer: idx, synced: func() bool { return *synced }}
}

func pod(namespace, name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
}

// A kind watched namespace by namespace is readable in a namespace whose own
// informer has synced while another namespace is still loading — the kind as
// a whole is not ready until every namespace is.
func TestAssessKindReadIsPerNamespace(t *testing.T) {
	alphaSynced, gammaSynced := true, false
	rc := &ResourceCache{
		enabledResources: map[string]bool{ConfigMaps: true},
		config: CacheConfig{
			ResourceScopes:          map[string]ResourceScope{ConfigMaps: {Enabled: true, Namespace: "alpha"}},
			ResourceScopeNamespaces: map[string][]string{ConfigMaps: {"alpha", "gamma"}},
		},
		informerStatuses:  []InformerSyncStatus{{Key: ConfigMaps}},
		informerHasSynced: []func() bool{func() bool { return alphaSynced && gammaSynced }},
		kindStores: map[string]map[string]kindStore{ConfigMaps: {
			"alpha": podStore(t, &alphaSynced, pod("alpha", "a1"), pod("alpha", "a2")),
			"gamma": podStore(t, &gammaSynced, pod("gamma", "g1")),
		}},
	}
	got := rc.AssessKindRead(ConfigMaps, []string{"alpha"})
	if got.Readiness != KindPending || !got.Complete || !slices.Equal(got.Read, []string{"alpha"}) || got.TargetReadiness() != KindReady {
		t.Fatalf("alpha while gamma loads = %+v", got)
	}
	all := rc.AssessKindRead(ConfigMaps, nil)
	if all.Complete || !slices.Equal(all.Read, []string{"alpha"}) || !slices.Equal(all.Loading, []string{"gamma"}) || all.TargetReadiness() != KindPending {
		t.Fatalf("unscoped while gamma loads = %+v", all)
	}

	if objs, ok := rc.ReadableObjects(ConfigMaps, []string{"alpha"}); !ok || len(objs) != 2 {
		t.Fatalf("alpha objects = %d, %v", len(objs), ok)
	}
	if n, ok := rc.ReadableCount(ConfigMaps, []string{"alpha"}); !ok || n != 2 {
		t.Fatalf("alpha count = %d, %v", n, ok)
	}
	if _, ok := rc.ReadableObjects(ConfigMaps, []string{"alpha", "gamma"}); ok {
		t.Fatal("a loading namespace was readable")
	}
	if _, ok := rc.ReadableObjects(ConfigMaps, nil); ok {
		t.Fatal("the whole kind was readable while gamma loads")
	}
	if _, found, ok := rc.ReadableObject(ConfigMaps, "alpha", "a1"); !ok || !found {
		t.Fatal("a1 should be found in synced alpha")
	}
	if _, found, ok := rc.ReadableObject(ConfigMaps, "alpha", "missing"); !ok || found {
		t.Fatal("a miss in synced alpha is a confirmed absence")
	}
	if _, _, ok := rc.ReadableObject(ConfigMaps, "gamma", "g1"); ok {
		t.Fatal("a read in loading gamma must not be authoritative")
	}
	if _, _, ok := rc.ReadableObject(ConfigMaps, "delta", "x"); ok {
		t.Fatal("an uncovered namespace must not be readable")
	}

	// The deferred deadline marks only the namespaces still loading.
	rc.deferredFailed.Store(true)
	rc.deferredSynced = map[string]bool{ConfigMaps: false}
	failed := rc.AssessKindRead(ConfigMaps, []string{"alpha", "gamma"})
	if failed.Readiness != KindFailed || !slices.Equal(failed.Read, []string{"alpha"}) || !slices.Equal(failed.Failed, []string{"gamma"}) || failed.TargetReadiness() != KindFailed {
		t.Fatalf("after the deadline = %+v", failed)
	}
	if alpha := rc.AssessKindRead(ConfigMaps, []string{"alpha"}); alpha.TargetReadiness() != KindReady {
		t.Fatalf("a loaded target is ready while another namespace failed: %+v", alpha)
	}
	// Without a partition the target is the whole kind.
	if whole := (KindReadAssessment{Readiness: KindPending, AllNamespaces: true}); whole.TargetReadiness() != KindPending {
		t.Fatalf("whole-kind target = %v", whole.TargetReadiness())
	}
	// Background kinds have no deadline.
	rc.backgroundKeys = map[string]bool{ConfigMaps: true}
	if bg := rc.AssessKindRead(ConfigMaps, []string{"gamma"}); !slices.Equal(bg.Loading, []string{"gamma"}) {
		t.Fatalf("background kind = %+v", bg)
	}

	gammaSynced = true
	if whole, ok := rc.ReadableObjects(ConfigMaps, nil); !ok || len(whole) != 3 {
		t.Fatalf("whole kind = %d, %v", len(whole), ok)
	}
}

func TestCachedObjectFoundBeforeSync(t *testing.T) {
	synced := false
	rc := &ResourceCache{enabledResources: map[string]bool{Pods: true}, kindStores: map[string]map[string]kindStore{Pods: {"": podStore(t, &synced, pod("alpha", "present"))}}}
	obj, found := rc.CachedObject(Pods, "alpha", "present")
	if !found || obj.(*corev1.Pod).Name != "present" {
		t.Fatalf("hit before sync: %v %v", obj, found)
	}
	obj.(*corev1.Pod).Name = "changed"
	if again, _ := rc.CachedObject(Pods, "alpha", "present"); again.(*corev1.Pod).Name != "present" {
		t.Fatal("lookup exposed the store object")
	}
	if _, found := rc.CachedObject(Pods, "alpha", "missing"); found {
		t.Fatal("unexpected hit")
	}
	if _, _, ok := rc.ReadableObject(Pods, "alpha", "missing"); ok {
		t.Fatal("miss before sync became authoritative")
	}
	synced = true
	if _, found, ok := rc.ReadableObject(Pods, "alpha", "missing"); found || !ok {
		t.Fatal("miss after sync must establish absence")
	}
}
