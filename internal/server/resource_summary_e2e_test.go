package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// End-to-end coverage of include=summary through the real handler path:
// HTTP request → handleListResources → dynamic cache → applySummaryStrip →
// JSON response. The unit tests in resource_summary_test.go pin the strip
// profiles; this pins the wiring — the query-param parse, the strip actually
// being applied at the writeJSON exit, and the informer-cache object
// surviving unmutated.

func argoAppE2EFixture() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      "checkout-service-production-us-east1",
			"namespace": "argocd",
			"labels":    map[string]any{"team": "payments-platform"},
			"annotations": map[string]any{
				"argocd.argoproj.io/refresh": "normal",
			},
		},
		"spec": map[string]any{
			"project": "shop-backend",
			"source": map[string]any{
				"repoURL":        "https://github.com/example-org/shop-backend-gitops",
				"targetRevision": "main",
				"path":           "environments/production/checkout-service",
			},
			"destination": map[string]any{
				"server":    "https://kubernetes.default.svc",
				"namespace": "shop-backend-production",
			},
			"syncPolicy": map[string]any{"automated": map[string]any{"prune": true}},
		},
		"status": map[string]any{
			"sync":   map[string]any{"status": "Synced", "revision": "f3c9a1d7"},
			"health": map[string]any{"status": "Healthy"},
			"operationState": map[string]any{
				"phase":      "Succeeded",
				"finishedAt": "2026-06-10T10:00:12Z",
				"syncResult": map[string]any{
					"resources": []any{map[string]any{"kind": "Deployment", "name": "checkout-service", "status": "Synced"}},
				},
			},
			"reconciledAt": "2026-06-10T10:05:00Z",
			"history": []any{
				map[string]any{"id": int64(0), "revision": "aaaa", "deployedAt": "2026-06-01T10:00:00Z"},
				map[string]any{"id": int64(1), "revision": "bbbb", "deployedAt": "2026-06-10T10:00:00Z"},
			},
			"resources": []any{
				map[string]any{"kind": "Deployment", "name": "checkout-service", "status": "Synced", "health": map[string]any{"status": "Healthy"}},
			},
		},
	}}
}

func setupArgoApplicationsDynamicCache(t *testing.T) {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "ApplicationList"},
		argoAppE2EFixture(),
	)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group:      "argoproj.io",
		Version:    "v1alpha1",
		Kind:       "Application",
		Name:       "applications",
		Namespaced: true,
		Verbs:      []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	// The dynamic cache's List is non-blocking; poll until the informer has
	// synced the fixture so the HTTP assertions below see deterministic data.
	deadline := time.Now().Add(5 * time.Second)
	for {
		cached, err := k8s.GetResourceCache().ListDynamicWithGroup(context.Background(), "applications", "", "argoproj.io")
		if err == nil && len(cached) == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("dynamic informer never synced: items=%d err=%v", len(cached), err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func listApplications(t *testing.T, query string) []map[string]any {
	t.Helper()
	var items []map[string]any
	assertOK(t, get(t, "/api/resources/applications?group=argoproj.io"+query), &items)
	if len(items) != 1 {
		t.Fatalf("got %d applications, want 1", len(items))
	}
	return items
}

func TestListResourcesIncludeSummaryE2E(t *testing.T) {
	setupArgoApplicationsDynamicCache(t)

	summarized := listApplications(t, "&include=summary")[0]

	assertPathAbsent(t, summarized, "status", "resources")
	assertPathAbsent(t, summarized, "status", "operationState", "syncResult")
	if got := mustNested(t, summarized, "status", "sync", "status"); got != "Synced" {
		t.Errorf("status.sync.status = %v, want Synced", got)
	}
	if got := mustNested(t, summarized, "status", "health", "status"); got != "Healthy" {
		t.Errorf("status.health.status = %v, want Healthy", got)
	}
	if got := mustNested(t, summarized, "status", "operationState", "phase"); got != "Succeeded" {
		t.Errorf("status.operationState.phase = %v, want Succeeded", got)
	}
	history := mustNested(t, summarized, "status", "history").([]any)
	if len(history) != 1 {
		t.Fatalf("history should be trimmed to tail, got %d entries", len(history))
	}
	tail, _ := history[0].(map[string]any)
	if len(tail) != 1 || tail["deployedAt"] != "2026-06-10T10:00:00Z" {
		t.Errorf("history tail should keep only deployedAt: %v", tail)
	}

	// The summary strip must have operated on a copy: the informer-cache
	// object behind the handler must still carry the heavy subtrees.
	cached, err := k8s.GetResourceCache().ListDynamicWithGroup(context.Background(), "applications", "", "argoproj.io")
	if err != nil {
		t.Fatalf("ListDynamicWithGroup: %v", err)
	}
	if len(cached) != 1 {
		t.Fatalf("got %d cached applications, want 1", len(cached))
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(cached[0].Object, "status", "resources"); !found {
		t.Fatal("cache object lost status.resources — summary request mutated the informer cache")
	}
	if hist, _, _ := unstructured.NestedSlice(cached[0].Object, "status", "history"); len(hist) != 2 {
		t.Fatalf("cache object history trimmed to %d entries — summary request mutated the informer cache", len(hist))
	}

	// Without include the same GET returns the full objects.
	full := listApplications(t, "")[0]
	if _, found, _ := unstructured.NestedFieldNoCopy(full, "status", "resources"); !found {
		t.Error("raw response missing status.resources")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(full, "status", "operationState", "syncResult"); !found {
		t.Error("raw response missing status.operationState.syncResult")
	}
	if fullHistory := mustNested(t, full, "status", "history").([]any); len(fullHistory) != 2 {
		t.Errorf("raw response history = %d entries, want 2", len(fullHistory))
	}
}

func TestListResourcesUnknownIncludeRejected(t *testing.T) {
	setupArgoApplicationsDynamicCache(t)

	resp := get(t, "/api/resources/applications?group=argoproj.io&include=bogus")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("non-JSON error body: %s", body)
	}
	msg, _ := payload["error"].(string)
	if msg != `unknown include="bogus" (want: summary, raw)` {
		t.Errorf("error = %q, want the accepted values named", msg)
	}
}
