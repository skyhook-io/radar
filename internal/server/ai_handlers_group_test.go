package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// Group-qualified AI GET must route to the dynamic cache so CRDs whose
// plural shadows a core kind (Knative serving.knative.dev/Service vs
// core/v1 Service) resolve to the requested object — not whichever the
// typed cache happens to hold under that kind/name pair.
//
// Without the group-first branch in fetchAIResource, FetchResource(
// "services", ...) returns the core/v1 Service from the typed informer
// and ?group=serving.knative.dev is silently dropped. The bug surfaces
// as wrong-object disclosure on the AI surface: a caller asking for the
// Knative Service receives the core Service's spec + IP + selector
// instead. This pins the fix and would regress if the typed cache is
// consulted before the group qualifier.
//
// Same bug class as T12's group-blind root lookup, but on the single-
// resource GET path; ResourceContext relationship walks already disambig
// by group (see pkg/topology/managedby_test.go), so a regression here is
// the last remaining hot spot for kind/plural collisions on the GET API.
func TestAIGetResource_GroupRoutesToDynamic(t *testing.T) {
	// Seed a Knative Service named "nginx" in "default" — same name+ns as
	// the core Service registered in TestMain. Without ?group routing, the
	// typed cache wins and returns the core Service. With it, the dynamic
	// cache returns the Knative Service.
	knativeGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	knativeSvc := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "serving.knative.dev/v1",
			"kind":       "Service",
			"metadata": map[string]any{
				"name":      "nginx",
				"namespace": "default",
			},
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{"image": "gcr.io/example/hello:1"},
						},
					},
				},
			},
		},
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{knativeGVR: "ServiceList"},
		knativeSvc,
	)

	resources := []k8s.APIResource{
		{
			Group:      "serving.knative.dev",
			Version:    "v1",
			Kind:       "Service",
			Name:       "services",
			Namespaced: true,
			IsCRD:      true,
			Verbs:      []string{"get", "list", "watch"},
		},
	}
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	// Warm the informer so the Get() call below sees the seeded object
	// without racing on initial sync.
	dynCache := k8s.GetDynamicResourceCache()
	if dynCache == nil {
		t.Fatal("dynamic cache not initialized")
	}
	if err := dynCache.EnsureWatching(knativeGVR); err != nil {
		t.Fatalf("EnsureWatching: %v", err)
	}
	if !dynCache.WaitForSync(knativeGVR, 5*time.Second) {
		t.Fatal("timed out waiting for Knative Service informer sync")
	}

	resp, err := http.Get(testServer.URL + "/api/ai/resources/services/default/nginx?group=serving.knative.dev&context=none")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// context=none returns the minified resource directly (no envelope).
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	apiVersion, _ := body["apiVersion"].(string)
	if apiVersion != "serving.knative.dev/v1" {
		t.Fatalf("apiVersion = %q, want serving.knative.dev/v1 — group qualifier was ignored "+
			"and the typed cache's core Service was returned instead", apiVersion)
	}
	kind, _ := body["kind"].(string)
	if kind != "Service" {
		t.Errorf("kind = %q, want Service", kind)
	}
	// Cross-check: the core Service has a Spec.Selector / ClusterIP shape
	// that the Knative seed does NOT have. A regression that returned the
	// core Service would carry those fields here.
	spec, _ := body["spec"].(map[string]any)
	if _, hasSelector := spec["selector"]; hasSelector {
		t.Errorf("response carries Service.spec.selector — looks like the core Service leaked through "+
			"despite ?group=serving.knative.dev; body=%+v", body)
	}
}

// Happy-path sibling for the test above: when no group is passed, the
// typed-cache-first path is correct (and must continue to be — the v1
// core Service is the dominant case and must not pay a dynamic-cache
// detour just because the group-qualified branch was added).
func TestAIGetResource_NoGroupHitsTypedCache(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/ai/resources/services/default/nginx?context=none")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	apiVersion, _ := body["apiVersion"].(string)
	if apiVersion != "v1" {
		t.Fatalf("apiVersion = %q, want v1 (core Service) on no-group request", apiVersion)
	}
}
