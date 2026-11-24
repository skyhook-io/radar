package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

func TestWorkflowSubmitChecksLiveIdentityAfterDryRun(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "stable"
		if changed {
			name = "changed during dry-run"
		}
		t.Run(name, func(t *testing.T) {
			srv, own := workflowStopServer(t)
			var mu sync.Mutex
			var calls []string
			digest := strings.Repeat("a", 64)
			templateReads := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Header.Get("Impersonate-User") != "alice" {
					t.Error("workflow submission used a different Kubernetes identity")
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/workflowtemplates/migrate"):
					templateReads++
					calls = append(calls, "get template")
					template := &unstructured.Unstructured{Object: map[string]any{
						"apiVersion": workflowAPIVersion, "kind": "WorkflowTemplate",
						"metadata": map[string]any{"namespace": "jobs", "name": "migrate", "uid": "template-uid", "resourceVersion": "19"},
					}}
					if changed && templateReads == 2 {
						template.SetResourceVersion("20")
					}
					_ = json.NewEncoder(w).Encode(template)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/workflows"):
					step := "create"
					if r.URL.Query().Get("dryRun") == "All" {
						step = "dry-run"
					}
					calls = append(calls, step)
					var workflow unstructured.Unstructured
					if err := json.NewDecoder(r.Body).Decode(&workflow); err != nil {
						t.Error(err)
					}
					ref, _, _ := unstructured.NestedString(workflow.Object, "spec", "workflowTemplateRef", "name")
					if workflow.GetAnnotations()[workflowSubmissionAnnotation] != digest || workflow.GetName() != "reviewed-run" || ref != "migrate" {
						t.Errorf("unbound Workflow: %+v", workflow.Object)
					}
					workflow.SetUID("workflow-uid")
					_ = json.NewEncoder(w).Encode(&workflow)
				default:
					t.Errorf("unexpected Kubernetes request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(api.Close)
			previous := k8s.SetTestConfig(&rest.Config{Host: api.URL})
			t.Cleanup(func() { k8s.SetTestConfig(previous) })

			router := chi.NewRouter()
			router.Post("/api/workflowtemplates/{namespace}/{name}/submission", srv.bindWrite(srv.handleWorkflowSubmit))
			body := `{"templateUid":"template-uid","templateResourceVersion":"19","workflowName":"reviewed-run","requestDigest":"` + digest + `","parameters":{"mode":"check"}}`
			req := httptest.NewRequest(http.MethodPost, "/api/workflowtemplates/jobs/migrate/submission", strings.NewReader(body))
			req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "alice"}))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			mu.Lock()
			defer mu.Unlock()
			want := []string{"get template", "dry-run", "get template"}
			code, response := http.StatusConflict, "template_changed"
			if !changed {
				want = append(want, "create")
				code, response = http.StatusOK, "workflow-uid"
			}
			if rec.Code != code || !strings.Contains(rec.Body.String(), response) || !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v status=%d body=%s, expected %v and %d %s", calls, rec.Code, rec.Body, want, code, response)
			}
			for _, action := range own.Actions() {
				if action.GetVerb() == "create" {
					t.Fatal("created a Workflow with Radar’s shared Kubernetes identity")
				}
			}
		})
	}
}
