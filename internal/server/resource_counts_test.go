package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestResourceCountsCountsWatchedCRDsAndMarksUnwatchedUnavailable(t *testing.T) {
	widgetGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	gadgetGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "gadgets"}
	endpointSliceGVR := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			widgetGVR:        "WidgetList",
			gadgetGVR:        "GadgetList",
			endpointSliceGVR: "EndpointSliceList",
		},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "Widget",
			"metadata":   map[string]any{"name": "w1", "namespace": "default"},
		}},
	)
	dyn.PrependReactor("list", "endpointslices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.UnstructuredList{}, nil
	})

	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "example.com", Version: "v1", Kind: "Widget", Name: "widgets", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "example.com", Version: "v1", Kind: "Gadget", Name: "gadgets", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dynCache := k8s.GetDynamicResourceCache()
	if dynCache == nil {
		t.Fatal("dynamic cache not initialized")
	}
	if err := dynCache.EnsureWatching(widgetGVR); err != nil {
		t.Fatalf("EnsureWatching(widget): %v", err)
	}
	if !dynCache.WaitForSync(widgetGVR, 2*time.Second) {
		t.Fatal("widget informer did not sync")
	}

	rec := httptest.NewRecorder()
	testServerSrv.handleResourceCounts(rec, httptest.NewRequest(http.MethodGet, "/api/resource-counts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var body ResourceCountsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body.Counts["example.com/Widget"]; got != 1 {
		t.Fatalf("Widget count = %d, want 1", got)
	}
	if _, ok := body.Counts["example.com/Gadget"]; ok {
		t.Fatalf("unwatched Gadget unexpectedly had a count: %v", body.Counts["example.com/Gadget"])
	}
	if !containsString(body.Unavailable, "example.com/Gadget") {
		t.Fatalf("unavailable = %v, want example.com/Gadget", body.Unavailable)
	}
	if got := body.Counts[endpointSliceCountKey]; got != 0 {
		t.Fatalf("EndpointSlice count = %d, want 0", got)
	}
}

func TestResourceCountsOmitsClusterScopedCRDWhenCanReadDenies(t *testing.T) {
	nodePoolGVR := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	endpointSliceGVR := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			nodePoolGVR:      "NodePoolList",
			endpointSliceGVR: "EndpointSliceList",
		},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "karpenter.sh/v1",
			"kind":       "NodePool",
			"metadata":   map[string]any{"name": "default"},
		}},
	)
	dyn.PrependReactor("list", "endpointslices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.UnstructuredList{}, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "karpenter.sh", Version: "v1", Kind: "NodePool", Name: "nodepools", Namespaced: false, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dynCache := k8s.GetDynamicResourceCache()
	if err := dynCache.EnsureWatching(nodePoolGVR); err != nil {
		t.Fatalf("EnsureWatching(nodepool): %v", err)
	}
	if !dynCache.WaitForSync(nodePoolGVR, 2*time.Second) {
		t.Fatal("nodepool informer did not sync")
	}

	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "alice"}
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "karpenter.sh", "nodepools", "", false)
	s.permCache.Set(user.Username, perms)
	req := requestWithUser(http.MethodGet, "/api/resource-counts", user)

	rec := httptest.NewRecorder()
	s.handleResourceCounts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var body ResourceCountsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := body.Counts["karpenter.sh/NodePool"]; ok {
		t.Fatalf("cluster-scoped NodePool leaked through counts: %v", body.Counts["karpenter.sh/NodePool"])
	}
	if containsString(body.Unavailable, "karpenter.sh/NodePool") {
		t.Fatalf("denied NodePool should not be advertised as unavailable: %v", body.Unavailable)
	}
}

func TestResourceCountsSurfacesDeniedCoreClusterScopedKindAsForbidden(t *testing.T) {
	// A core cluster-scoped kind (Node) always exists, so an RBAC denial must be
	// surfaced as forbidden — not silently omitted, which would render as
	// "0 / No Node found", indistinguishable from an empty cluster.
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "alice"}
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "nodes", "", false)
	s.permCache.Set(user.Username, perms)
	req := requestWithUser(http.MethodGet, "/api/resource-counts", user)

	rec := httptest.NewRecorder()
	s.handleResourceCounts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var body ResourceCountsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := body.Counts["Node"]; ok {
		t.Fatalf("denied Node leaked a count: %v", body.Counts["Node"])
	}
	if !containsString(body.Forbidden, "Node") {
		t.Fatalf("denied core cluster-scoped Node should be in forbidden, got: %v", body.Forbidden)
	}
}

func TestResourceCountsCountsClusterScopedCRDDespiteNamespaceFilter(t *testing.T) {
	nodePoolGVR := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	endpointSliceGVR := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			nodePoolGVR:      "NodePoolList",
			endpointSliceGVR: "EndpointSliceList",
		},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "karpenter.sh/v1",
			"kind":       "NodePool",
			"metadata":   map[string]any{"name": "default"},
		}},
	)
	dyn.PrependReactor("list", "endpointslices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.UnstructuredList{}, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "karpenter.sh", Version: "v1", Kind: "NodePool", Name: "nodepools", Namespaced: false, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dynCache := k8s.GetDynamicResourceCache()
	if err := dynCache.EnsureWatching(nodePoolGVR); err != nil {
		t.Fatalf("EnsureWatching(nodepool): %v", err)
	}
	if !dynCache.WaitForSync(nodePoolGVR, 2*time.Second) {
		t.Fatal("nodepool informer did not sync")
	}

	rec := httptest.NewRecorder()
	testServerSrv.handleResourceCounts(rec, httptest.NewRequest(http.MethodGet, "/api/resource-counts?namespace=default", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var body ResourceCountsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body.Counts["karpenter.sh/NodePool"]; got != 1 {
		t.Fatalf("NodePool count = %d, want 1", got)
	}
	if containsString(body.Unavailable, "karpenter.sh/NodePool") {
		t.Fatalf("counted NodePool should not be marked unavailable: %v", body.Unavailable)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
