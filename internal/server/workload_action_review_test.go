package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

func requestWorkloadAction(t *testing.T, router http.Handler, kind, route string, body any) (int, map[string]any) {
	t.Helper()
	data, ok := body.(string)
	if !ok {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		data = string(b)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/workloads/"+kind+"/prod/web/actions/"+route, strings.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return w.Code, out
}

func TestWorkloadActionReviewAndExecute(t *testing.T) {
	for _, tc := range []struct {
		kind, version, act, field string
		spec                      map[string]any
		replicas                  *int32
		before, after             any
	}{
		{"Deployment", "apps/v1", "scale", "spec.replicas", map[string]any{"replicas": int64(3)}, int32Ptr(0), float64(3), float64(0)},
		{"StatefulSet", "apps/v1", "scale", "spec.replicas", map[string]any{}, int32Ptr(4), nil, float64(4)},
		{"Rollout", "argoproj.io/v1alpha1", "scale", "spec.replicas", map[string]any{"replicas": int64(4)}, int32Ptr(2), float64(4), float64(2)},
		{"Deployment", "apps/v1", "pause", "spec.paused", map[string]any{}, nil, false, true},
		{"Deployment", "apps/v1", "resume", "spec.paused", map[string]any{"paused": true}, nil, true, false},
		{"CronJob", "batch/v1", "suspend", "spec.suspend", map[string]any{}, nil, false, true},
		{"CronJob", "batch/v1", "resume", "spec.suspend", map[string]any{"suspend": true}, nil, true, false},
		{"Deployment", "apps/v1", "restart", `spec.template.metadata.annotations["kubectl.kubernetes.io/restartedAt"]`, map[string]any{}, nil, nil, nil},
		{"StatefulSet", "apps/v1", "restart", `spec.template.metadata.annotations["kubectl.kubernetes.io/restartedAt"]`, map[string]any{}, nil, nil, nil},
		{"DaemonSet", "apps/v1", "restart", `spec.template.metadata.annotations["kubectl.kubernetes.io/restartedAt"]`, map[string]any{}, nil, nil, nil},
		{"Rollout", "argoproj.io/v1alpha1", "restart", "spec.restartAt", map[string]any{}, nil, nil, nil},
	} {
		t.Run(tc.kind+"/"+tc.act, func(t *testing.T) {
			obj := reviewedDeployment()
			obj.SetKind(tc.kind)
			obj.SetAPIVersion(tc.version)
			obj.Object["spec"] = tc.spec
			router, dyn := reviewedRestartRouter(t, obj)
			input := map[string]any{"action": tc.act}
			if tc.replicas != nil {
				input["replicas"] = *tc.replicas
			}
			status, view := requestWorkloadAction(t, router, tc.kind, "preview", input)
			if status != 200 || view["field"] != tc.field || view["kind"] != tc.kind || view["name"] != "web" || view["namespace"] != "prod" || view["uid"] != "uid-1" || view["resourceVersion"] != "7" || view["before"] != tc.before || len(dyn.Actions()) != 1 || dyn.Actions()[0].GetVerb() != "get" {
				t.Fatalf("preview: %d %v, requests %v", status, view, dyn.Actions())
			}
			if tc.act == "restart" {
				at, ok := view["after"].(string)
				if !ok {
					t.Fatal(view)
				}
				if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
					t.Fatal(at)
				}
				input["restartAt"] = at
			} else if view["after"] != tc.after {
				t.Fatal(view)
			}
			input["uid"] = view["uid"]
			input["reviewToken"] = view["reviewToken"]
			status, result := requestWorkloadAction(t, router, tc.kind, "execute", input)
			if status != 200 || result["before"] != view["before"] || result["after"] != view["after"] || writes(dyn) != 1 {
				t.Fatalf("%d %v; %v", status, result, dyn.Actions())
			}
			var patch map[string]any
			last := dyn.Actions()[len(dyn.Actions())-1].(clienttesting.PatchAction)
			if err := json.Unmarshal(last.GetPatch(), &patch); err != nil {
				t.Fatal(err)
			}
			meta := patch["metadata"].(map[string]any)
			if meta["uid"] != "uid-1" || meta["resourceVersion"] != "7" {
				t.Fatal(patch)
			}
		})
	}
}

func TestWorkloadActionStrictRequest(t *testing.T) {
	router, dyn := reviewedRestartRouter(t, reviewedDeployment())
	for _, body := range []string{`{}`, `null`, `{"action":"scale"}`, `{"action":"scale","replicas":null}`, `{"action":"scale","replicas":-1}`, `{"action":"scale","replicas":10001}`, `{"action":"scale","replicas":"3"}`, `{"action":"scale","replicas":1,"replicas":2}`, `{"action":"pause","uid":"x"}`, `{"action":"pause","replicas":1}`, `{"action":"pause","args":{}}`, `{"action":"pause"}{}`, `{"action":"delete"}`} {
		if status, out := requestWorkloadAction(t, router, "Deployment", "preview", body); status != 400 {
			t.Errorf("%s: %d %v", body, status, out)
		}
	}
	if status, out := requestWorkloadAction(t, router, "Deployment", "preview", strings.Repeat(" ", 3000)); status != 413 {
		t.Errorf("oversize: %d %v", status, out)
	}
	if status, out := requestWorkloadAction(t, router, "Deployment", "execute", `{"action":"pause"}`); status != 400 {
		t.Errorf("without preview: %d %v", status, out)
	}
	if len(dyn.Actions()) != 0 {
		t.Fatal(dyn.Actions())
	}
}

func TestWorkloadActionReviewDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*unstructured.Unstructured, map[string]any)
		want   int
	}{
		{"status churn", func(o *unstructured.Unstructured, _ map[string]any) {
			o.Object["status"] = map[string]any{"readyReplicas": int64(1)}
		}, 200},
		{"replaced", func(o *unstructured.Unstructured, _ map[string]any) { o.SetUID("replacement") }, 409},
		{"replicas changed", func(o *unstructured.Unstructured, _ map[string]any) {
			o.Object["spec"].(map[string]any)["replicas"] = int64(8)
		}, 409},
		{"new owner", func(o *unstructured.Unstructured, _ map[string]any) {
			o.SetOwnerReferences([]metav1.OwnerReference{{Name: "controller", UID: "owner-2"}})
		}, 409},
		{"arguments changed", func(_ *unstructured.Unstructured, q map[string]any) { q["replicas"] = 4 }, 409},
		{"uid changed", func(_ *unstructured.Unstructured, q map[string]any) { q["uid"] = "different" }, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := reviewedDeployment()
			router, dyn := reviewedRestartRouter(t, obj)
			body := map[string]any{"action": "scale", "replicas": 2}
			status, view := requestWorkloadAction(t, router, "Deployment", "preview", body)
			if status != 200 {
				t.Fatal(status, view)
			}
			body["uid"] = view["uid"]
			body["reviewToken"] = view["reviewToken"]
			obj.SetResourceVersion("8")
			tc.mutate(obj, body)
			if err := dyn.Tracker().Update(deploymentsGVR, obj, "prod"); err != nil {
				t.Fatal(err)
			}
			status, out := requestWorkloadAction(t, router, "Deployment", "execute", body)
			if status != tc.want {
				t.Fatal(status, out)
			}
			if tc.want == 409 {
				if out["error_code"] != errCodeWorkloadChanged || writes(dyn) != 0 {
					t.Fatal(out, dyn.Actions())
				}
			} else {
				last := dyn.Actions()[len(dyn.Actions())-1].(clienttesting.PatchAction)
				if !strings.Contains(string(last.GetPatch()), `"resourceVersion":"8"`) {
					t.Fatal(string(last.GetPatch()))
				}
			}
		})
	}
}

func TestWorkloadActionStoreResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		reply  func(*unstructured.Unstructured) (runtime.Object, error)
	}{
		{"RBAC denial", 403, "", func(*unstructured.Unstructured) (runtime.Object, error) {
			return nil, apierrors.NewForbidden(deploymentsGVR.GroupResource(), "web", nil)
		}},
		{"atomic conflict", 409, errCodeWorkloadChanged, func(*unstructured.Unstructured) (runtime.Object, error) {
			return nil, apierrors.NewConflict(deploymentsGVR.GroupResource(), "web", nil)
		}},
		{"admission removed true", 409, "workload_action_not_stored", func(obj *unstructured.Unstructured) (runtime.Object, error) { return obj, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := reviewedDeployment()
			router, dyn := reviewedRestartRouter(t, obj)
			status, view := requestWorkloadAction(t, router, "Deployment", "preview", `{"action":"pause"}`)
			if status != 200 {
				t.Fatal(status, view)
			}
			dyn.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) { v, err := tc.reply(obj); return true, v, err })
			status, out := requestWorkloadAction(t, router, "Deployment", "execute", map[string]any{"action": "pause", "uid": view["uid"], "reviewToken": view["reviewToken"]})
			if status != tc.status || tc.code != "" && out["error_code"] != tc.code || writes(dyn) != 1 {
				t.Fatal(status, out, dyn.Actions())
			}
		})
	}
}

func TestWorkloadActionCallerIdentity(t *testing.T) {
	obj := reviewedDeployment()
	router, dyn := reviewedRestartRouter(t, obj)
	var calls []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/apps/v1/namespaces/prod/deployments/web" {
			t.Errorf("unexpected Kubernetes path: %s", r.URL.Path)
		}
		calls = append(calls, r.Method+" "+r.Header.Get("Impersonate-User"))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			if !strings.Contains(string(body), `"uid":"uid-1"`) {
				t.Error(string(body))
			}
			if r.Header.Get("Impersonate-User") != "reviewer" {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Code: 403, Reason: "Forbidden", Message: "reviewer has no write permission"})
				return
			}
			obj.Object["spec"].(map[string]any)["paused"] = true
		}
		_ = json.NewEncoder(w).Encode(obj)
	}))
	t.Cleanup(api.Close)
	if _, err := k8s.InstallTestServingConnection("workload-review", &rest.Config{Host: api.URL}, dyn, appsDiscovery()); err != nil {
		t.Fatal(err)
	}
	as := func(user string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			router.ServeHTTP(w, r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: user, Groups: []string{"operators"}})))
		})
	}
	status, view := requestWorkloadAction(t, as("proposer"), "Deployment", "preview", `{"action":"pause"}`)
	if status != 200 {
		t.Fatalf("preview: %d %v", status, view)
	}
	body := map[string]any{"action": "pause", "uid": view["uid"], "reviewToken": view["reviewToken"]}
	if code, out := requestWorkloadAction(t, as("proposer"), "Deployment", "execute", body); code != 403 {
		t.Fatalf("%d %v", code, out)
	}
	if code, out := requestWorkloadAction(t, as("reviewer"), "Deployment", "execute", body); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if got := strings.Join(calls, ","); got != "GET proposer,GET proposer,PATCH proposer,GET reviewer,PATCH reviewer" || len(dyn.Actions()) != 0 {
		t.Fatalf("caller calls %v, shared-client calls %v", calls, dyn.Actions())
	}
}

func TestWorkloadActionGuards(t *testing.T) {
	for _, tc := range []struct {
		kind, action, reason string
		mutate               func(*unstructured.Unstructured)
	}{
		{"Deployment", "resume", "workload_action_unchanged", func(*unstructured.Unstructured) {}},
		{"Deployment", "restart", errCodeRestartNotApplicable, func(o *unstructured.Unstructured) { o.Object["spec"].(map[string]any)["paused"] = true }},
		{"DaemonSet", "restart", errCodeRestartNotApplicable, func(o *unstructured.Unstructured) {
			o.Object["spec"].(map[string]any)["updateStrategy"] = map[string]any{"type": "OnDelete"}
		}},
		{"StatefulSet", "restart", errCodeRestartNotApplicable, func(o *unstructured.Unstructured) {
			o.Object["spec"].(map[string]any)["updateStrategy"] = map[string]any{"rollingUpdate": map[string]any{"partition": int64(1)}}
		}},
		{"Rollout", "restart", "workload_action_unchanged", func(o *unstructured.Unstructured) {
			o.SetAPIVersion("argoproj.io/v1alpha1")
			o.Object["spec"].(map[string]any)["restartAt"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		}},
		{"Deployment", "pause", errCodeWorkloadTerminating, func(o *unstructured.Unstructured) { now := metav1.Now(); o.SetDeletionTimestamp(&now) }},
	} {
		t.Run(tc.kind+"/"+tc.action+"/"+tc.reason, func(t *testing.T) {
			obj := reviewedDeployment()
			obj.SetKind(tc.kind)
			tc.mutate(obj)
			router, dyn := reviewedRestartRouter(t, obj)
			code, out := requestWorkloadAction(t, router, tc.kind, "preview", map[string]any{"action": tc.action})
			if code != 409 || out["error_code"] != tc.reason || writes(dyn) != 0 {
				t.Fatal(code, out, dyn.Actions())
			}
		})
	}
}

func TestWorkloadActionStrictVersion(t *testing.T) {
	obj := reviewedDeployment()
	router, dyn := reviewedRestartRouter(t, obj)
	input := map[string]any{"action": "pause"}
	code, preview := requestWorkloadAction(t, router, "Deployment", "preview", input)
	if code != 200 {
		t.Fatal(code, preview)
	}
	input["uid"] = preview["uid"]
	input["reviewToken"] = preview["reviewToken"]
	input["expectedResourceVersion"] = preview["resourceVersion"]
	code, out := requestWorkloadAction(t, router, "Deployment", "execute", input)
	if code != 200 || writes(dyn) != 1 {
		t.Fatal(code, out)
	}
	// Make the desired field match its old value after the first write. A
	// caller pinned to version 7 cannot re-arm that original proposal.
	obj.SetResourceVersion("9")
	obj.Object["status"] = map[string]any{"readyReplicas": int64(2)}
	if err := dyn.Tracker().Update(deploymentsGVR, obj, "prod"); err != nil {
		t.Fatal(err)
	}
	code, out = requestWorkloadAction(t, router, "Deployment", "execute", input)
	if code != 409 || out["error_code"] != errCodeWorkloadChanged || writes(dyn) != 1 {
		t.Fatal(code, out, dyn.Actions())
	}
	for _, bad := range []any{"", nil, 42} {
		input["expectedResourceVersion"] = bad
		code, out = requestWorkloadAction(t, router, "Deployment", "execute", input)
		if code != 400 || writes(dyn) != 1 {
			t.Fatal(code, out, dyn.Actions())
		}
	}
}
