package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/gitops"
)

func TestEnforceArgoSelectiveSyncSafety(t *testing.T) {
	trueValue := true
	resource := gitops.ArgoSyncResource{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "api"}
	opts := enforceArgoSelectiveSyncSafety(gitops.ArgoSyncOptions{
		Resources: []gitops.ArgoSyncResource{resource},
		Revision:  "unsafe-revision",
		Prune:     &trueValue,
		DryRun:    &trueValue,
		Force:     &trueValue,
		ApplyOnly: &trueValue,
	})
	if opts.Revision != "" || opts.Prune == nil || *opts.Prune || opts.ApplyOnly == nil || *opts.ApplyOnly {
		t.Fatalf("selective sync safety not enforced: %#v", opts)
	}
	if opts.DryRun == nil || !*opts.DryRun || opts.Force == nil || !*opts.Force || len(opts.Resources) != 1 {
		t.Fatalf("allowed selective sync options changed: %#v", opts)
	}
}

func TestEnforceArgoSelectiveSyncSafetyLeavesFullSyncUnchanged(t *testing.T) {
	trueValue := true
	original := gitops.ArgoSyncOptions{Revision: "release", Prune: &trueValue, ApplyOnly: &trueValue}
	opts := enforceArgoSelectiveSyncSafety(original)
	if opts.Revision != "release" || opts.Prune != original.Prune || opts.ApplyOnly != original.ApplyOnly {
		t.Fatalf("full sync options changed: %#v", opts)
	}
}

// Argo numbers history from 0, so the handler must accept id 0 while still
// rejecting a request that names no id at all.
func TestHandleArgoRollbackHistoryID(t *testing.T) {
	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	seed := func(t *testing.T) *dynamicfake.FakeDynamicClient {
		t.Helper()
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			runtime.NewScheme(),
			map[schema.GroupVersionResource]string{appGVR: "ApplicationList"},
			&unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "argoproj.io/v1alpha1",
				"kind":       "Application",
				"metadata":   map[string]any{"name": "demo", "namespace": "argocd"},
				"spec":       map[string]any{"project": "default"},
				"status": map[string]any{"history": []any{map[string]any{
					"id":       int64(0),
					"revision": "abc123",
					"source":   map[string]any{"repoURL": "https://example.com/repo", "path": "app", "targetRevision": "HEAD"},
				}}},
			}},
		)
		if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
			{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application", Name: "applications", Namespaced: true, IsCRD: true, Verbs: []string{"get", "patch"}},
		}); err != nil {
			t.Fatalf("seed argo app: %v", err)
		}
		t.Cleanup(k8s.ResetTestDynamicState)
		return dyn
	}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/argo/applications/argocd/demo/rollback", strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("namespace", "argocd")
		rctx.URLParams.Add("name", "demo")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		(&Server{}).handleArgoRollback(rr, req)
		return rr
	}

	t.Run("id 0 rolls back to the first history entry", func(t *testing.T) {
		dyn := seed(t)
		rr := post(`{"id":0}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		var patch map[string]any
		for _, action := range dyn.Actions() {
			if pa, ok := action.(clienttesting.PatchAction); ok {
				if err := json.Unmarshal(pa.GetPatch(), &patch); err != nil {
					t.Fatalf("patch body not JSON: %v", err)
				}
			}
		}
		revision, _, _ := unstructured.NestedString(patch, "operation", "sync", "revision")
		if revision != "abc123" {
			t.Fatalf("operation.sync.revision = %q, want abc123; patch=%v", revision, patch)
		}
	})

	for _, body := range []string{``, `{}`, `{"id":-1}`} {
		t.Run("rejects body "+body, func(t *testing.T) {
			dyn := seed(t)
			rr := post(body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
			}
			for _, action := range dyn.Actions() {
				if _, ok := action.(clienttesting.PatchAction); ok {
					t.Fatalf("rejected request still patched the Application: %v", dyn.Actions())
				}
			}
		})
	}
}
