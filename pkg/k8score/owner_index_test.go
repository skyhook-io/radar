package k8score

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func ownedPod(namespace, name string, owners ...metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID("uid-" + name), OwnerReferences: owners}}
}

func ownerRef(kind, name, uid string) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, UID: types.UID(uid)}
}

func names[T interface{ GetName() string }](objs []T) []string {
	var out []string
	for _, o := range objs {
		out = append(out, o.GetName())
	}
	slices.Sort(out)
	return out
}

func objectNames(objs []apiruntime.Object) []string {
	var out []string
	for _, o := range objs {
		out = append(out, o.(interface{ GetName() string }).GetName())
	}
	slices.Sort(out)
	return out
}

func TestOwnerUIDsNamesEveryReferencedUID(t *testing.T) {
	pod := ownedPod("alpha", "p", ownerRef("ReplicaSet", "rs", "uid-rs"), ownerRef("Deployment", "wrong-kind-and-name", "uid-d"), ownerRef("ReplicaSet", "no-uid", ""))
	if got, _ := ownerUIDs(pod); !slices.Equal(got, []string{"uid-rs", "uid-d"}) {
		t.Fatalf("typed = %v; want every referenced UID, coordinates ignored, empty UIDs skipped", got)
	}
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"ownerReferences": []any{
		map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "name": "c", "uid": "uid-c"},
		map[string]any{"kind": "broken"},
	}}}}
	if got, _ := ownerUIDs(u); !slices.Equal(got, []string{"uid-c"}) {
		t.Fatalf("unstructured = %v", got)
	}
	if got, _ := ownerUIDs(ownedPod("alpha", "orphan")); got != nil {
		t.Fatalf("no owners = %v, want nil", got)
	}
	if got, _ := ownerUIDs(&unstructured.Unstructured{Object: map[string]any{}}); got != nil {
		t.Fatalf("no metadata = %v, want nil", got)
	}
}

func TestOwnerUIDsAllocatesOnlyItsResult(t *testing.T) {
	pod := ownedPod("alpha", "p", ownerRef("ReplicaSet", "rs", "uid-rs"))
	orphan := ownedPod("alpha", "o")
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"ownerReferences": []any{
		map[string]any{"uid": "uid-rs"},
	}}}}
	for name, obj := range map[string]any{"typed": pod, "unstructured": u} {
		if allocs := testing.AllocsPerRun(100, func() { _, _ = ownerUIDs(obj) }); allocs > 1 {
			t.Errorf("%s: %v allocations per object, want at most 1", name, allocs)
		}
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = ownerUIDs(orphan) }); allocs != 0 {
		t.Errorf("unowned object: %v allocations, want 0", allocs)
	}
}

func TestTypedReadableDependentsUsesEveryStoreItsTargetNeeds(t *testing.T) {
	rs := ownerRef("ReplicaSet", "web-1", "uid-rs")
	client := fake.NewClientset(
		ownedPod("alpha", "a1", rs),
		ownedPod("alpha", "a2", ownerRef("Deployment", "elsewhere", "uid-rs")),
		ownedPod("beta", "b1", rs),
		ownedPod("alpha", "unrelated", ownerRef("ReplicaSet", "other", "uid-other")),
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "e", OwnerReferences: []metav1.OwnerReference{rs}}},
	)
	for name, cfg := range map[string]CacheConfig{
		"cluster-wide":         {ResourceTypes: map[string]bool{Pods: true, Events: true}},
		"partitioned by space": {ResourceScopes: map[string]ResourceScope{Pods: {Enabled: true, Namespace: "alpha"}}, ResourceScopeNamespaces: map[string][]string{Pods: {"alpha", "beta"}}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg.Client = client
			rc, err := NewResourceCache(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Stop()
			all, ok := rc.ReadableDependents(Pods, nil, "uid-rs")
			if want := []string{"a1", "a2", "b1"}; !ok || !slices.Equal(objectNames(all), want) {
				t.Fatalf("whole scope = %v, %v; want %v", objectNames(all), ok, want)
			}
			alpha, ok := rc.ReadableDependents(Pods, []string{"alpha"}, "uid-rs")
			if want := []string{"a1", "a2"}; !ok || !slices.Equal(objectNames(alpha), want) {
				t.Fatalf("alpha = %v, %v; want %v", objectNames(alpha), ok, want)
			}
			if none, ok := rc.ReadableDependents(Pods, []string{}, "uid-rs"); !ok || len(none) != 0 {
				t.Fatalf("no namespaces = %v, %v; want an empty complete answer", objectNames(none), ok)
			}
			if _, ok := rc.ReadableDependents(Pods, []string{"gamma"}, "uid-rs"); name != "cluster-wide" && ok {
				t.Fatal("a namespace the collector doesn't hold must not read as complete")
			}
		})
	}
}

// A collector of one namespace holds its store under no namespace, as one
// cluster-wide informer does; a read of a namespace it doesn't collect must
// still fail rather than find nothing there.
func TestTypedReadableDependentsRefusesANamespaceOneNamespaceCollectorLacks(t *testing.T) {
	rs := ownerRef("ReplicaSet", "web-1", "uid-rs")
	client := fake.NewClientset(ownedPod("alpha", "a1", rs), ownedPod("beta", "b1", rs))
	rc, err := NewResourceCache(CacheConfig{Client: client, ResourceScopes: map[string]ResourceScope{Pods: {Enabled: true, Namespace: "alpha"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Stop()
	if alpha, ok := rc.ReadableDependents(Pods, []string{"alpha"}, "uid-rs"); !ok || !slices.Equal(objectNames(alpha), []string{"a1"}) {
		t.Fatalf("alpha = %v, %v; want [a1]", objectNames(alpha), ok)
	}
	for _, namespace := range []string{"beta", "gamma"} {
		if got, ok := rc.ReadableDependents(Pods, []string{namespace}, "uid-rs"); ok {
			t.Fatalf("%s = %v, read as complete; the collector holds only alpha", namespace, objectNames(got))
		}
	}
}

func TestTypedReadableDependentsRefusesWhatItCannotAnswer(t *testing.T) {
	rs := ownerRef("ReplicaSet", "web-1", "uid-rs")
	client := fake.NewClientset(&corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "e", OwnerReferences: []metav1.OwnerReference{rs}}})
	rc, err := NewResourceCache(CacheConfig{Client: client, ResourceTypes: map[string]bool{Pods: true, Events: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Stop()
	if _, ok := rc.ReadableDependents(Events, nil, "uid-rs"); ok {
		t.Fatal("Events aren't indexed, so they can't answer")
	}
	if _, ok := rc.ReadableDependents(Secrets, nil, "uid-rs"); ok {
		t.Fatal("a kind the cache doesn't hold can't answer")
	}
	var none *ResourceCache
	if _, ok := none.ReadableDependents(Pods, nil, "uid-rs"); ok {
		t.Fatal("a nil cache can't answer")
	}
}

func TestTypedOwnerIndexFollowsUpdatesAndDeletes(t *testing.T) {
	client := fake.NewClientset(ownedPod("alpha", "p", ownerRef("ReplicaSet", "old", "uid-old")))
	rc, err := NewResourceCache(CacheConfig{Client: client, ResourceTypes: map[string]bool{Pods: true, ReplicaSets: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Stop()
	ctx := context.Background()
	moved := ownedPod("alpha", "p", ownerRef("ReplicaSet", "new", "uid-new"))
	if _, err := client.CoreV1().Pods("alpha").Update(ctx, moved, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "the update", func() bool {
		got, _ := rc.ReadableDependents(Pods, nil, "uid-new")
		return len(got) == 1
	})
	if old, _ := rc.ReadableDependents(Pods, nil, "uid-old"); len(old) != 0 {
		t.Fatal("an owner the object no longer names still finds it")
	}
	if err := client.CoreV1().Pods("alpha").Delete(ctx, "p", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "the delete", func() bool {
		got, _ := rc.ReadableDependents(Pods, nil, "uid-new")
		return len(got) == 0
	})
	if _, err := client.AppsV1().ReplicaSets("alpha").Create(ctx, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "rs", OwnerReferences: []metav1.OwnerReference{ownerRef("Deployment", "d", "uid-d")}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "the create", func() bool {
		got, _ := rc.ReadableDependents(ReplicaSets, []string{"alpha"}, "uid-d")
		return len(got) == 1
	})
}

func TestDynamicReadableIndexedReadsOnlyItsServingInformers(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	owned := func(namespace, name, uid string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.io/v1", "kind": "Widget",
			"metadata": map[string]any{"name": name, "namespace": namespace, "ownerReferences": []any{
				map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "d", "uid": uid},
			}},
		}}
	}
	dyn := newWidgetDynamicClient(owned("alpha", "a", "uid-d"), owned("beta", "b", "uid-d"), owned("alpha", "other", "uid-other"))
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if _, ok := d.ReadableIndexed(gvr, "alpha", OwnerUIDIndex, "uid-d"); ok {
		t.Fatal("with no informer, nothing can answer")
	}
	if err := d.startWatching(gvr, "alpha"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "alpha's informer", func() bool { return d.IsNamespaceSynced(gvr, "alpha") })
	got, ok := d.ReadableIndexed(gvr, "alpha", OwnerUIDIndex, "uid-d")
	if !ok || !slices.Equal(names(got), []string{"a"}) {
		t.Fatalf("alpha = %v, %v", names(got), ok)
	}
	if _, ok := d.ReadableIndexed(gvr, "", OwnerUIDIndex, "uid-d"); ok {
		t.Fatal("namespace informers can't answer for the cluster")
	}
	if err := d.startWatching(gvr, ""); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "the cluster-wide informer", func() bool { return d.IsClusterWideSynced(gvr) })
	all, ok := d.ReadableIndexed(gvr, "", OwnerUIDIndex, "uid-d")
	if !ok || !slices.Equal(names(all), []string{"a", "b"}) {
		t.Fatalf("cluster-wide = %v, %v", names(all), ok)
	}
	beta, ok := d.ReadableIndexed(gvr, "beta", OwnerUIDIndex, "uid-d")
	if !ok || !slices.Equal(names(beta), []string{"b"}) {
		t.Fatalf("beta from the cluster-wide informer = %v, %v; want beta's only", names(beta), ok)
	}
}

// The lookup itself refuses a store that hasn't loaded, for callers that
// didn't assess the kind first.
func TestTypedReadableDependentsRefusesAnUnloadedStore(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, apiruntime.Object, error) {
		return true, nil, errors.New("initial list held")
	})
	rc, err := NewResourceCache(CacheConfig{Client: client, ResourceTypes: map[string]bool{Pods: true, Secrets: true}, DeferredTypes: map[string]bool{Secrets: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Stop()
	if _, ok := rc.ReadableDependents(Secrets, nil, "uid-x"); ok {
		t.Fatal("a store still loading answered for the whole kind")
	}
	if _, ok := rc.ReadableDependents(Secrets, []string{"alpha"}, "uid-x"); ok {
		t.Fatal("a store still loading answered for a namespace")
	}
	if _, ok := rc.ReadableDependents(Pods, nil, "uid-x"); !ok {
		t.Fatal("a loaded store must answer")
	}
}
