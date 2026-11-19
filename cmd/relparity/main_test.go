package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestRestrictedComparisonSkipsDisabledNamespaceLister(t *testing.T) {
	cache := &k8s.ResourceCache{ResourceCache: &k8score.ResourceCache{}}
	if cache.Namespaces() != nil {
		t.Fatal("Namespace listing should be disabled")
	}
	provider := k8s.NewTopologyResourceProvider(cache)
	namespaces, reason := comparisonNamespaces(provider.(topology.NamespaceProvider))
	if len(namespaces) != 0 || !strings.Contains(reason, "namespaces not available") {
		t.Fatalf("namespaces %v, skipped reason %q", namespaces, reason)
	}
	data, err := json.Marshal(report{RestrictedSkippedReason: reason})
	if err != nil || !strings.Contains(string(data), `"restrictedSkippedReason":"cannot enumerate namespaces:`) {
		t.Fatalf("report %s: %v", data, err)
	}
}

func TestCollectIncludesEveryCachedClusterScopedTypedKind(t *testing.T) {
	objects := []runtime.Object{
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "reader"}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "readers"}},
	}
	enabled := map[string]bool{}
	var expected []resourceid.Ref
	for _, kl := range k8score.AllKindListers() {
		b, ok := resourceid.BuiltinForKind(kl.Kind())
		if ok && b.Group == kl.Group() && !b.Namespaced {
			enabled[b.Resource] = true
		}
	}
	typed, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(objects...), ResourceTypes: enabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(typed.Stop)
	select {
	case <-typed.DeferredDone():
	case <-time.After(5 * time.Second):
		t.Fatal("typed cache did not sync")
	}
	for _, obj := range objects {
		m := obj.(metav1.Object)
		gvks, _, err := scheme.Scheme.ObjectKinds(obj)
		if err != nil || len(gvks) != 1 {
			t.Fatalf("fixture kind: %v, %v", gvks, err)
		}
		gvk := gvks[0]
		expected = append(expected, resourceid.NewRef(gvk.Group, gvk.Kind, "", m.GetName()))
	}
	provider := k8s.NewTopologyResourceProvider(&k8s.ResourceCache{ResourceCache: typed})
	got, _ := collect(provider, typed, nil, nil, nil, 0)
	var refs []resourceid.Ref
	for _, subject := range got {
		refs = append(refs, subject.ref)
	}
	for _, ref := range expected {
		if !slices.Contains(refs, ref) {
			t.Errorf("missing subject %v in %v", ref, refs)
		}
	}
	if len(got) != len(expected) {
		t.Fatalf("got %d subjects, want %d: %v", len(got), len(expected), refs)
	}
	for _, obj := range objects {
		if !obj.GetObjectKind().GroupVersionKind().Empty() {
			t.Fatal("collect mutated a fixture's type metadata")
		}
	}
}

func TestCollectNamesTheCRDKindsItCouldNotList(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	typed := &k8score.ResourceCache{}
	provider := k8s.NewTopologyResourceProvider(&k8s.ResourceCache{ResourceCache: typed})
	_, unlisted := collect(provider, typed, &k8s.DynamicResourceCache{}, []schema.GroupVersionResource{gvr}, map[schema.GroupVersionResource]string{gvr: "Widget"}, 0)
	if _, ok := unlisted[gvr.String()]; !ok {
		t.Fatalf("a CRD kind that failed to list is not reported: %v", unlisted)
	}
}
