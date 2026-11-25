package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/workloadrestart"
)

func reviewedRestartRouter(t *testing.T, objects ...runtime.Object) (http.Handler, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	install(t, "reviewed-restart", dyn)
	router := chi.NewRouter()
	(&Server{listenAddress: "0.0.0.0"}).setupAppRoutes(router)
	return router, dyn
}

func reviewedDeployment() *unstructured.Unstructured {
	obj := pauseTestDeployment(false)
	obj.SetUID("uid-1")
	obj.SetResourceVersion("7")
	return obj
}

func reviewedRestartRequest(kind, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/workloads/"+kind+"/prod/web/restart", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func checkRestartRefusal(t *testing.T, router http.Handler, req *http.Request, status int, code string) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body map[string]string
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &body) != nil || body["error"] == "" || body["error_code"] != code {
		t.Fatalf("got %d %s, want %d code %q", w.Code, w.Body, status, code)
	}
	return body
}

func TestRestartWithoutPreconditions(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Rollout"} {
		t.Run(kind, func(t *testing.T) {
			obj := reviewedDeployment()
			obj.SetKind(kind)
			if kind == "Rollout" {
				obj.SetAPIVersion("argoproj.io/v1alpha1")
			}
			if kind == "DaemonSet" || kind == "StatefulSet" {
				obj.Object["spec"].(map[string]any)["updateStrategy"] = map[string]any{"type": "OnDelete"}
			}
			router, dyn := reviewedRestartRouter(t, obj)
			req := reviewedRestartRequest(kind, "")
			req.ContentLength = -1 // An empty body can also arrive chunked.
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			var got struct {
				Message string `json:"message"`
				workloadrestart.Result
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Message != "Workload restart initiated" || !got.Changed {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if _, err := time.Parse(time.RFC3339, got.RestartedAt); err != nil {
				t.Fatal(err)
			}
			actions := dyn.Actions()
			if kind == "Deployment" || kind == "Rollout" {
				if len(actions) != 2 || actions[0].GetVerb() != "get" {
					t.Fatalf("expected GET then PATCH, got %v", actions)
				}
				actions = actions[1:]
			}
			if len(actions) != 1 || actions[0].GetVerb() != "patch" {
				t.Fatalf("expected one unconditional patch, got %v", actions)
			}
			var patch map[string]any
			if err := json.Unmarshal(actions[0].(clienttesting.PatchAction).GetPatch(), &patch); err != nil {
				t.Fatal(err)
			}
			path := []string{"spec", "template", "metadata", "annotations", "kubectl.kubernetes.io/restartedAt"}
			if kind == "Rollout" {
				path = []string{"spec", "restartAt"}
			}
			if marker, _, _ := unstructured.NestedString(patch, path...); marker != got.RestartedAt || patch["metadata"] != nil {
				t.Fatalf("response %s, patch %v", w.Body, patch)
			}
		})
	}
}

func TestReviewedRestartRequestValidation(t *testing.T) {
	router, dyn := reviewedRestartRouter(t, reviewedDeployment())
	for _, tc := range []struct {
		body   string
		status int
	}{
		{" ", 400}, {`{"resourceVersion":"7"}`, 400}, {"null", 400}, {"{", 400}, {"{}", 400}, {`{"uid":""}`, 400},
		{`{"uid":3}`, 400}, {`{"uid":"uid-1","resourceVersion":7}`, 400},
		{`{"uid":null}`, 400}, {`{"uid":"   "}`, 400},
		{`{"uid":"uid-1","resourceVersion":null}`, 400},
		{`{"uid":"uid-1","resourceVersion":""}`, 400},
		{`{"uid":"uid-1","resourceVersion":"7","resourceVersion":""}`, 400},
		{`{"uid":"uid-1","resourceVersion":"7","resourceVersion":"7"}`, 400},
		{`{"uid":"old","uid":"uid-1"}`, 400},
		{`{"uid":"uid-1","extra":1}`, 400}, {`{"uid":"uid-1"} {}`, 400},
		{`{"uid":"` + strings.Repeat("x", 1024) + `"}`, 413},
		{"!" + strings.Repeat(" ", 1024), 413},
	} {
		checkRestartRefusal(t, router, reviewedRestartRequest("deployments", tc.body), tc.status, "")
	}
	checkRestartRefusal(t, router, reviewedRestartRequest("jobs", `{"uid":"uid-1"}`), 400, "")
	req := reviewedRestartRequest("deployments", `{"uid":"uid-1"}`)
	req.Header.Set("Origin", "https://other.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	checkRestartRefusal(t, router, req, 403, "cross_origin_refused")
	req = reviewedRestartRequest("deployments", `{"uid":"uid-1"}`)
	req.Header.Set(connectionHeader, "other-cluster")
	checkRestartRefusal(t, router, req, 409, errCodeContextChanged)
	k8s.RetireServingConnection()
	checkRestartRefusal(t, router, reviewedRestartRequest("deployments", `{"uid":"uid-1"}`), 503, errCodeClusterUnavailable)
	if len(dyn.Actions()) != 0 {
		t.Errorf("refused requests reached Kubernetes: %v", dyn.Actions())
	}
}

func TestReviewedRestartRefusesTerminatingWorkloads(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Rollout"} {
		t.Run(kind, func(t *testing.T) {
			obj := reviewedDeployment()
			obj.SetKind(kind)
			if kind == "Rollout" {
				obj.SetAPIVersion("argoproj.io/v1alpha1")
			}
			now := metav1.Now()
			obj.SetDeletionTimestamp(&now)
			router, dyn := reviewedRestartRouter(t, obj)
			checkRestartRefusal(t, router, reviewedRestartRequest(kind, `{"uid":"uid-1"}`), 409, "workload_terminating")
			if writes(dyn) != 0 {
				t.Fatal("terminating workload was patched")
			}
		})
	}
}

func TestReviewedRestartDocumentsRefusals(t *testing.T) {
	doc, err := os.ReadFile("../../docs/workload-actions.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{errCodeWorkloadChanged, errCodeWorkloadNotFound, errCodeWorkloadPaused, errCodeWorkloadTerminating, errCodeRestartNotApplicable, errCodeRestartNotStored} {
		if !strings.Contains(string(doc), "`"+code+"`") {
			t.Errorf("workload actions documentation is missing %s", code)
		}
	}
}

func TestReviewedRestartRefusals(t *testing.T) {
	const body = `{"uid":"uid-1"}`
	for _, tc := range []struct {
		name, kind, body string
		mutate           func(*unstructured.Unstructured)
		status           int
		code             string
	}{
		{"missing", "daemonsets", body, nil, 404, errCodeWorkloadNotFound},
		{"wrong uid", "deployments", `{"uid":"old"}`, nil, 409, errCodeWorkloadChanged},
		{"stale version", "deployments", `{"uid":"uid-1","resourceVersion":"6"}`, nil, 409, errCodeWorkloadChanged},
		{"paused", "deployments", body, func(obj *unstructured.Unstructured) { obj.Object["spec"].(map[string]any)["paused"] = true }, 409, errCodeWorkloadPaused},
		{"OnDelete", "statefulsets", body, func(obj *unstructured.Unstructured) {
			obj.SetKind("StatefulSet")
			obj.Object["spec"].(map[string]any)["updateStrategy"] = map[string]any{"type": "OnDelete"}
		}, 409, errCodeRestartNotApplicable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := reviewedDeployment()
			if tc.mutate != nil {
				tc.mutate(obj)
			}
			router, dyn := reviewedRestartRouter(t, obj)
			checkRestartRefusal(t, router, reviewedRestartRequest(tc.kind, tc.body), tc.status, tc.code)
			if writes(dyn) != 0 {
				t.Fatal("refused workload was patched")
			}
		})
	}
}

func TestReviewedRestartKubernetesErrors(t *testing.T) {
	invalid := func(path *field.Path, message string) error {
		return apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "web", field.ErrorList{field.Invalid(path, "uid-1", message)})
	}
	for _, tc := range []struct {
		name, verb string
		err        error
		status     int
		code       string
	}{
		{"expired credentials", "get", apierrors.NewUnauthorized("expired"), 401, ""},
		{"get denied", "get", apierrors.NewForbidden(deploymentsGVR.GroupResource(), "web", errors.New("get denied")), 403, ""},
		{"patch denied", "patch", apierrors.NewForbidden(deploymentsGVR.GroupResource(), "web", errors.New("patch denied")), 403, ""},
		{"immutable UID", "patch", invalid(field.NewPath("metadata", "uid"), "field is immutable"), 409, errCodeWorkloadChanged},
		{"invalid UID by policy", "patch", invalid(field.NewPath("metadata", "uid"), "not allowed by policy"), 422, ""},
		{"invalid spec", "patch", invalid(field.NewPath("spec"), "rejected by policy"), 422, ""},
		{"bad request", "patch", apierrors.NewBadRequest("invalid patch"), 400, ""},
		{"admission conflict", "patch", apierrors.NewConflict(deploymentsGVR.GroupResource(), "web", errors.New("the object has been modified; please apply your changes to the latest version and try again")), 409, ""},
		{"timeout", "patch", apierrors.NewTimeoutError("timed out", 0), 504, ""},
		{"unexpected error", "patch", errors.New("unexpected failure"), 500, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, dyn := reviewedRestartRouter(t, reviewedDeployment())
			var attempts int
			dyn.PrependReactor(tc.verb, "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
				attempts++
				return true, nil, tc.err
			})
			body := checkRestartRefusal(t, router, reviewedRestartRequest("deployments", `{"uid":"uid-1"}`), tc.status, tc.code)
			if tc.name == "admission conflict" {
				actions := dyn.Actions()
				if body["error"] != tc.err.Error() || len(actions) != 3 || actions[2].GetVerb() != "get" {
					t.Fatalf("expected the original conflict after a re-read, got %v, actions %v", body, actions)
				}
			}
			if attempts != 1 {
				t.Errorf("attempts = %d, response = %v", attempts, body)
			}
		})
	}
}

func TestRestartReportsMarkerReturnedByAdmission(t *testing.T) {
	for _, stored := range []string{"", "2026-01-01T00:00:00Z"} {
		obj := reviewedDeployment()
		router, dyn := reviewedRestartRouter(t, obj)
		dyn.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
			if stored != "" {
				if err := unstructured.SetNestedField(obj.Object, stored, "spec", "template", "metadata", "annotations", "kubectl.kubernetes.io/restartedAt"); err != nil {
					t.Fatal(err)
				}
			}
			return true, obj, nil
		})
		body := checkRestartRefusal(t, router, reviewedRestartRequest("deployments", `{"uid":"uid-1"}`), 409, errCodeRestartNotStored)
		if got, ok := body["restartedAt"]; !ok || got != stored {
			t.Errorf("stored marker = %v, want %q", body, stored)
		}

		w := httptest.NewRecorder()
		router.ServeHTTP(w, reviewedRestartRequest("deployments", ""))
		var unconditional map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &unconditional); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || unconditional["message"] != "Workload restart initiated" || unconditional["restartedAt"] != stored || unconditional["changed"] != false {
			t.Errorf("unconditional restart got %d %s", w.Code, w.Body)
		}
	}
}

func TestRestartAsCaller(t *testing.T) {
	for _, tc := range []struct {
		name, kind, resource, body string
		patchMetadata              any
		methods                    []string
	}{
		{"reviewed deployment", "Deployment", "deployments", `{"uid":"uid-1"}`, map[string]any{"uid": "uid-1", "resourceVersion": "7"}, []string{"GET", "PATCH"}},
		{"version-pinned deployment", "Deployment", "deployments", `{"uid":"uid-1","resourceVersion":"7"}`, map[string]any{"uid": "uid-1", "resourceVersion": "7"}, []string{"GET", "PATCH"}},
		{"unconditional deployment", "Deployment", "deployments", "", nil, []string{"GET", "PATCH"}},
		{"unconditional statefulset", "StatefulSet", "statefulsets", "", nil, []string{"PATCH"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := reviewedDeployment()
			obj.SetKind(tc.kind)
			router, dyn := reviewedRestartRouter(t, obj)
			groups := []string{"devs", "leads"}
			var methods []string
			var patchedMarker string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method)
				if r.URL.Path != "/apis/apps/v1/namespaces/prod/"+tc.resource+"/web" || r.Header.Get("Impersonate-User") != "alice" || !slices.Equal(r.Header.Values("Impersonate-Group"), groups) {
					t.Errorf("unexpected Kubernetes request: %s %s user %q groups %v", r.Method, r.URL, r.Header.Get("Impersonate-User"), r.Header.Values("Impersonate-Group"))
				}
				ret := obj.DeepCopy()
				if r.Method == http.MethodPatch {
					var patch map[string]any
					if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(patch["metadata"], tc.patchMetadata) {
						t.Errorf("patch preconditions: %v", patch["metadata"])
					}
					patchedMarker, _, _ = unstructured.NestedString(patch, "spec", "template", "metadata", "annotations", "kubectl.kubernetes.io/restartedAt")
					ret.Object["spec"] = patch["spec"]
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(ret)
			}))
			t.Cleanup(api.Close)
			if _, err := k8s.InstallTestServingConnection("impersonation", &rest.Config{Host: api.URL}, dyn, appsDiscovery()); err != nil {
				t.Fatal(err)
			}
			req := reviewedRestartRequest(tc.resource, tc.body)
			req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "alice", Groups: groups}))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			var got struct {
				Message string `json:"message"`
				workloadrestart.Result
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Message != "Workload restart initiated" || !got.Changed || got.RestartedAt != patchedMarker {
				t.Fatalf("got %d %s, patched marker %q", w.Code, w.Body, patchedMarker)
			}
			if _, err := time.Parse(time.RFC3339Nano, got.RestartedAt); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(methods, tc.methods) || len(dyn.Actions()) != 0 {
				t.Errorf("impersonated requests = %v, Radar requests = %v", methods, dyn.Actions())
			}
		})
	}
}
