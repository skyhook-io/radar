package k8s

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestVerifiedTypedMissUsesAuthoritativeAPI(t *testing.T) {
	t.Cleanup(ResetTestState)
	t.Cleanup(ResetTestDynamicState)
	client := fakeclientset.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "exists", Namespace: "outside"}})
	if err := InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "watched"}}); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	live := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"namespace": "outside", "name": "exists"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ConfigMapList"}, live)
	if err := InitTestDynamicResourceCache(dyn, []APIResource{{Version: "v1", Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	cache := GetResourceCache()
	if obj, err := cache.GetDynamicWithGroupVerified(context.Background(), "ConfigMap", "outside", "exists", ""); err != nil || obj == nil {
		t.Fatalf("outside coverage: %v, want authoritative live object", err)
	}
	if _, err := cache.GetDynamicWithGroupVerified(context.Background(), "ConfigMap", "watched", "absent", ""); !apierrors.IsNotFound(err) {
		t.Fatalf("covered and synced: %v, want NotFound", err)
	}
	if _, err := cache.GetDynamicWithGroup(context.Background(), "ConfigMap", "outside", "exists", ""); !apierrors.IsNotFound(err) {
		t.Fatalf("existing unverified reader changed: %v", err)
	}
}

func TestVerifiedDynamicMissConfirmsAgainstAPI(t *testing.T) {
	t.Cleanup(ResetTestDynamicState)
	gvr := schema.GroupVersionResource{Group: "review.example", Version: "v1", Resource: "widgets"}
	widget := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "review.example/v1", "kind": "Widget", "metadata": map[string]any{"namespace": "team", "name": "exists"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, widget)
	var ready atomic.Bool
	dyn.PrependReactor("list", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !ready.Load() {
			return true, nil, errors.New("initial list unavailable")
		}
		return false, nil, nil
	})
	if err := InitTestDynamicResourceCache(dyn, []APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	cache := &ResourceCache{}
	if obj, err := cache.GetDynamicWithGroupVerified(context.Background(), "Widget", "team", "exists", gvr.Group); err != nil || obj == nil {
		t.Fatalf("unsynced informer: %v, want authoritative live object", err)
	}
	ready.Store(true)
	if err := wait.PollUntilContextTimeout(context.Background(), 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		obj, err := cache.GetDynamicWithGroupVerified(ctx, "Widget", "team", "exists", gvr.Group)
		return err == nil && obj != nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.GetDynamicWithGroupVerified(context.Background(), "Widget", "team", "absent", gvr.Group); !apierrors.IsNotFound(err) {
		t.Fatalf("synced dynamic miss: %v, want NotFound", err)
	}
}

func TestVerifiedMissDoesNotTreatAPIFailureAsAbsence(t *testing.T) {
	t.Cleanup(ResetTestDynamicState)
	gvr := schema.GroupVersionResource{Group: "review.example", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dyn.PrependReactor("get", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "absent", errors.New("denied"))
	})
	if err := InitTestDynamicResourceCache(dyn, []APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ResourceCache{}).GetDynamicWithGroupVerified(context.Background(), "Widget", "team", "absent", gvr.Group); !apierrors.IsForbidden(err) {
		t.Fatalf("direct GET: %v, want Forbidden rather than absence", err)
	}
}
