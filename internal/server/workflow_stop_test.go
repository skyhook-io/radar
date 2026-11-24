package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func testWorkflow(phase, shutdown string) *unstructured.Unstructured {
	spec := map[string]any{"workflowTemplateRef": map[string]any{"name": "migrate"}}
	if shutdown != "" {
		spec["shutdown"] = shutdown
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow",
		"metadata": map[string]any{"name": "migrate-abcde", "namespace": "jobs", "resourceVersion": "7"},
		"spec":     spec,
		"status":   map[string]any{"phase": phase},
	}}
}

func workflowStopServer(t *testing.T, objs ...runtime.Object) (*Server, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		argoWorkflowGVR: "WorkflowList",
	}, objs...)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Workflow", Name: "workflows", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "patch"}},
	}); err != nil {
		t.Fatalf("seed workflow kind: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	srv := New(Config{DevMode: true})
	t.Cleanup(srv.Stop)
	return srv, dyn
}

func workflowPatches(dyn *dynamicfake.FakeDynamicClient) []k8stesting.PatchAction {
	var out []k8stesting.PatchAction
	for _, a := range dyn.Actions() {
		if p, ok := a.(k8stesting.PatchAction); ok && a.GetResource().Resource == "workflows" {
			out = append(out, p)
		}
	}
	return out
}

func postStop(srv *Server, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/jobs/migrate-abcde/stop", strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestWorkflowStop(t *testing.T) {
	t.Run("empty unknown-length body uses the default", func(t *testing.T) {
		srv, _ := workflowStopServer(t, testWorkflow("Running", ""))
		req := httptest.NewRequest(http.MethodPost, "/api/workflows/jobs/migrate-abcde/stop", http.NoBody)
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		var resp WorkflowStopResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil || resp.Shutdown != "Terminate" || !resp.Changed {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("terminates by default, bound to the version read, and records when", func(t *testing.T) {
		srv, dyn := workflowStopServer(t, testWorkflow("Running", ""))
		rec := postStop(srv, "", nil)
		var resp WorkflowStopResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil || resp.Shutdown != "Terminate" || !resp.Changed {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		ps := workflowPatches(dyn)
		if len(ps) != 1 || string(ps[0].GetPatchType()) != "application/merge-patch+json" {
			t.Fatalf("patches: %v", ps)
		}
		body := patchBody(t, ps[0])
		md := body["metadata"].(map[string]any)
		ann := md["annotations"].(map[string]any)
		if body["spec"].(map[string]any)["shutdown"] != "Terminate" || md["resourceVersion"] != "7" || ann[workflowStoppedAtAnnotation] == nil {
			t.Fatalf("patch: %v", body)
		}
		// Without authentication there is no caller to name.
		if _, ok := ann[workflowStoppedByAnnotation]; ok || len(body["spec"].(map[string]any)) != 1 || len(ann) != 1 {
			t.Fatalf("patch writes more than shutdown and stopped-at: %v", body)
		}
	})
	t.Run("stop runs exit handlers", func(t *testing.T) {
		srv, dyn := workflowStopServer(t, testWorkflow("Running", ""))
		if rec := postStop(srv, `{"strategy":"Stop"}`, nil); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if body := patchBody(t, workflowPatches(dyn)[0]); body["spec"].(map[string]any)["shutdown"] != "Stop" {
			t.Fatalf("patch: %v", body)
		}
	})
	t.Run("refuses anything but Terminate or Stop, and other fields", func(t *testing.T) {
		srv, dyn := workflowStopServer(t, testWorkflow("Running", ""))
		for _, b := range []string{`{"strategy":"Suspend"}`, `{"strategy":"Terminate","annotations":{"x":"y"}}`, `not json`} {
			if rec := postStop(srv, b, nil); rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status %d", b, rec.Code)
			}
		}
		if len(workflowPatches(dyn)) != 0 {
			t.Fatal("a refused request patched")
		}
	})
	t.Run("missing is 404", func(t *testing.T) {
		srv, _ := workflowStopServer(t)
		if rec := postStop(srv, "", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("finished is 409 and writes nothing", func(t *testing.T) {
		for _, phase := range []string{"Succeeded", "Failed", "Error"} {
			srv, dyn := workflowStopServer(t, testWorkflow(phase, "Stop"))
			if rec := postStop(srv, "", nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "workflow_finished") || len(workflowPatches(dyn)) != 0 {
				t.Fatalf("%s: status = %d, patches = %d", phase, rec.Code, len(workflowPatches(dyn)))
			}
		}
	})
	t.Run("already stopping is a no-op", func(t *testing.T) {
		srv, dyn := workflowStopServer(t, testWorkflow("Running", "Stop"))
		rec := postStop(srv, "", nil)
		var resp WorkflowStopResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if rec.Code != http.StatusOK || resp.Changed || resp.Shutdown != "Stop" || len(workflowPatches(dyn)) != 0 {
			t.Fatalf("status = %d resp = %+v patches = %d", rec.Code, resp, len(workflowPatches(dyn)))
		}
	})
	t.Run("a change racing the patch is 409, not overwritten", func(t *testing.T) {
		srv, dyn := workflowStopServer(t, testWorkflow("Running", ""))
		dyn.PrependReactor("patch", "workflows", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewConflict(argoWorkflowGVR.GroupResource(), "migrate-abcde", nil)
		})
		if rec := postStop(srv, "", nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "workflow_changed") {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("a refused patch is 403 with Kubernetes' reason", func(t *testing.T) {
		srv, dyn := workflowStopServer(t, testWorkflow("Running", ""))
		dyn.PrependReactor("patch", "workflows", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(argoWorkflowGVR.GroupResource(), "migrate-abcde", errors.New(`User "jane" cannot patch resource "workflows"`))
		})
		rec := postStop(srv, "", nil)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `cannot patch resource \"workflows\"`) {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("rejected Kubernetes credentials return 403", func(t *testing.T) {
		srv, dyn := workflowStopServer(t, testWorkflow("Running", ""))
		dyn.PrependReactor("get", "workflows", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewUnauthorized("test expired credential")
		})
		if rec := postStop(srv, "", nil); rec.Code != http.StatusForbidden || len(workflowPatches(dyn)) != 0 {
			t.Fatalf("status = %d patches = %d", rec.Code, len(workflowPatches(dyn)))
		}
	})
	// Shared without authentication: anyone who reaches Radar may stop a
	// workflow as Radar's identity, like any workload action, but a web
	// page in their browser may not.
	t.Run("a page on another site can't stop a workflow", func(t *testing.T) {
		srv, dyn := workflowStopServer(t, testWorkflow("Running", ""))
		rec := postStop(srv, "", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"})
		if rec.Code != http.StatusForbidden || len(workflowPatches(dyn)) != 0 {
			t.Fatalf("status = %d patches = %d", rec.Code, len(workflowPatches(dyn)))
		}
	})
}

// With authentication the read and the patch run as the caller, and the
// caller is recorded as who stopped it.
func TestWorkflowStopReadsAndPatchesAsTheCaller(t *testing.T) {
	srv, dyn := workflowStopServer(t, testWorkflow("Running", ""))
	var mu sync.Mutex
	var requests []string
	var patched map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" as "+r.Header.Get("Impersonate-User"))
		if r.Method == http.MethodPatch {
			_ = json.NewDecoder(r.Body).Decode(&patched)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"argoproj.io/v1alpha1","kind":"Workflow","metadata":{"name":"migrate-abcde","namespace":"jobs","resourceVersion":"7"},"spec":{},"status":{"phase":"Running"}}`))
	}))
	t.Cleanup(api.Close)
	previous := k8s.SetTestConfig(&rest.Config{Host: api.URL})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })

	router := chi.NewRouter()
	router.Post("/api/workflows/{namespace}/{name}/stop", srv.bindWrite(srv.handleWorkflowStop))
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/jobs/migrate-abcde/stop", nil)
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "alice"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0] != "GET as alice" || requests[1] != "PATCH as alice" {
		t.Fatalf("API requests = %v, want a GET and a PATCH as alice", requests)
	}
	ann, _ := patched["metadata"].(map[string]any)["annotations"].(map[string]any)
	if ann[workflowStoppedByAnnotation] != "alice" {
		t.Fatalf("stopped-by: %v", patched)
	}
	if n := len(workflowPatches(dyn)); n != 0 {
		t.Fatalf("issued %d patches through Radar's own client", n)
	}
}
