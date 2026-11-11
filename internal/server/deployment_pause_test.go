package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

var pauseDeploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

func pauseTestDeployment(paused bool) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "web", "namespace": "prod"},
		"spec":       map[string]any{"paused": paused},
	}}
}

func setupPauseTest(t *testing.T, objs ...runtime.Object) (*dynamicfake.FakeDynamicClient, http.Handler) {
	t.Helper()
	previousStatus := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(previousStatus) })

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{pauseDeploymentsGVR: "DeploymentList"},
		objs...,
	)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true, Verbs: []string{"get", "list", "watch", "patch"}},
	}); err != nil {
		t.Fatalf("init test cache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	s := &Server{}
	router := chi.NewRouter()
	router.Post("/api/workloads/{kind}/{namespace}/{name}/pause", s.handlePauseDeployment)
	router.Post("/api/workloads/{kind}/{namespace}/{name}/resume", s.handleResumeDeployment)
	return dyn, router
}

func pausePatchCount(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "patch" {
			n++
		}
	}
	return n
}

func storedPaused(t *testing.T, dyn *dynamicfake.FakeDynamicClient) bool {
	t.Helper()
	d, err := dyn.Tracker().Get(pauseDeploymentsGVR, "prod", "web")
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	paused, _, err := unstructured.NestedBool(d.(*unstructured.Unstructured).Object, "spec", "paused")
	if err != nil {
		t.Fatalf("read spec.paused: %v", err)
	}
	return paused
}

func TestPauseAndResumeDeployment(t *testing.T) {
	for _, tc := range []struct {
		name        string
		start       bool
		action      string
		wantPaused  bool
		wantMessage string
	}{
		{"pause", false, "pause", true, "Deployment paused"},
		{"resume", true, "resume", false, "Deployment resumed"},
		{"pausing a paused Deployment succeeds", true, "pause", true, "Deployment paused"},
		{"resuming an unpaused Deployment succeeds", false, "resume", false, "Deployment resumed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn, router := setupPauseTest(t, pauseTestDeployment(tc.start))

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/web/"+tc.action, nil))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("response not JSON: %v (%s)", err, w.Body.String())
			}
			if body["message"] != tc.wantMessage {
				t.Errorf("message = %q, want %q", body["message"], tc.wantMessage)
			}
			if got := storedPaused(t, dyn); got != tc.wantPaused {
				t.Errorf("spec.paused = %v, want %v", got, tc.wantPaused)
			}
		})
	}
}

func TestPauseDeploymentAcceptsEveryKindSpelling(t *testing.T) {
	for _, kind := range []string{"deployments", "deployment", "Deployment", "DEPLOYMENTS"} {
		t.Run(kind, func(t *testing.T) {
			dyn, router := setupPauseTest(t, pauseTestDeployment(false))

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/workloads/"+kind+"/prod/web/pause", nil))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
			}
			if !storedPaused(t, dyn) {
				t.Error("spec.paused = false, want true")
			}
		})
	}
}

// StatefulSets and DaemonSets have no spec.paused to set, and a Rollout's pause is
// its own operation, so none of them may reach the patch.
func TestPauseAndResumeRejectNonDeployments(t *testing.T) {
	for _, kind := range []string{"statefulsets", "StatefulSet", "daemonsets", "rollouts", "Rollout", "replicasets", "pods", "cronjobs"} {
		for _, action := range []string{"pause", "resume"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				dyn, router := setupPauseTest(t, pauseTestDeployment(false))

				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/workloads/"+kind+"/prod/web/"+action, nil))

				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
				}
				if n := pausePatchCount(dyn); n != 0 {
					t.Errorf("issued %d patches for %s", n, kind)
				}
			})
		}
	}
}

func TestPauseDeploymentErrorStatus(t *testing.T) {
	deployments := schema.GroupResource{Group: "apps", Resource: "deployments"}

	t.Run("missing Deployment is 404", func(t *testing.T) {
		_, router := setupPauseTest(t)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/web/pause", nil))

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
		}
	})

	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"RBAC denial is 403", apierrors.NewForbidden(deployments, "web", errors.New("nope")), http.StatusForbidden},
		{"anything else is 500", errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn, router := setupPauseTest(t, pauseTestDeployment(false))
			dyn.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, tc.err
			})

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/web/pause", nil))

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// With auth on, the patch has to run as the user. When the impersonating client
// can't be built, falling back to Radar's own client would let the user act beyond
// their RBAC, so the request must fail without patching.
func TestPauseDeploymentNeverFallsBackToRadarsIdentity(t *testing.T) {
	dyn, router := setupPauseTest(t, pauseTestDeployment(false))

	r := httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/web/pause", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "alice"}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cluster client not available") {
		t.Errorf("body = %s, want the missing-client error", w.Body.String())
	}
	if n := pausePatchCount(dyn); n != 0 {
		t.Errorf("issued %d patches through Radar's own client", n)
	}
}

func TestPauseAndResumeRequireAConnection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    k8s.ConnectionState
		wantBody string
	}{
		{"disconnected", k8s.StateDisconnected, "Not connected to cluster"},
		{"still connecting", k8s.StateConnecting, "cluster_connecting"},
	} {
		for _, action := range []string{"pause", "resume"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				dyn, router := setupPauseTest(t, pauseTestDeployment(false))
				k8s.SetConnectionStatus(k8s.ConnectionStatus{State: tc.state})

				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/web/"+action, nil))

				if w.Code != http.StatusServiceUnavailable {
					t.Fatalf("status = %d, want 503 (body %s)", w.Code, w.Body.String())
				}
				if !strings.Contains(w.Body.String(), tc.wantBody) {
					t.Errorf("body = %s, want it to contain %q", w.Body.String(), tc.wantBody)
				}
				if n := pausePatchCount(dyn); n != 0 {
					t.Errorf("issued %d patches while not connected", n)
				}
			})
		}
	}
}

func TestPauseDeploymentPatchesAsTheCaller(t *testing.T) {
	dyn, router := setupPauseTest(t, pauseTestDeployment(false))

	var requests []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" as "+r.Header.Get("Impersonate-User"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"prod"}}`))
	}))
	t.Cleanup(api.Close)
	previous := k8s.SetTestConfig(&rest.Config{Host: api.URL})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })

	r := httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/web/pause", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "alice"}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if len(requests) != 1 || requests[0] != "PATCH as alice" {
		t.Errorf("API requests = %v, want one PATCH as alice", requests)
	}
	if n := pausePatchCount(dyn); n != 0 {
		t.Errorf("issued %d patches through Radar's own client", n)
	}
}

func TestPauseAndResumeAreAudited(t *testing.T) {
	for _, action := range []string{"pause", "resume"} {
		t.Run(action, func(t *testing.T) {
			_, router := setupPauseTest(t, pauseTestDeployment(false))

			r := httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/web/"+action, nil)
			r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "alice"}))

			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			router.ServeHTTP(httptest.NewRecorder(), r)
			log.SetOutput(previous)

			want := `[audit] user="alice" groups=[] POST path="/api/workloads/deployments/prod/web/` + action + `" ns="prod" name="web"`
			if !strings.Contains(logs.String(), want) {
				t.Errorf("audit line %q missing from logs:\n%s", want, logs.String())
			}
		})
	}
}

// Goes through the real router so a route missing from setupAppRoutes, or one
// outside requireSameOrigin, fails here.
func TestPauseRoutesAreWiredOnTheRealRouter(t *testing.T) {
	for _, action := range []string{"pause", "resume"} {
		t.Run(action+" reaches the kind check", func(t *testing.T) {
			resp, err := http.Post(testServer.URL+"/api/workloads/statefulsets/default/nginx/"+action, "application/json", nil)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})

		t.Run(action+" refuses a cross-site browser request", func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/workloads/deployments/default/nginx/"+action, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Origin", "https://evil.example")
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
}

func TestRestartPausedDeploymentReturnsWorkloadPaused(t *testing.T) {
	dyn, _ := setupPauseTest(t, pauseTestDeployment(true))
	router := chi.NewRouter()
	router.Post("/api/workloads/{kind}/{namespace}/{name}/restart", (&Server{}).handleRestartWorkload)

	for _, kind := range []string{"deployments", "Deployments", "DEPLOYMENT"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/workloads/"+kind+"/prod/web/restart", nil))
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s: status = %d, want 409; body %s", kind, rec.Code, rec.Body.String())
		}
		var body struct {
			ErrorCode string `json:"error_code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.ErrorCode != errCodeWorkloadPaused {
			t.Fatalf("%s: error_code = %q, want %q", kind, body.ErrorCode, errCodeWorkloadPaused)
		}
	}
	if n := pausePatchCount(dyn); n != 0 {
		t.Fatalf("issued %d patches for a paused Deployment", n)
	}
}
