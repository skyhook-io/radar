package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func TestReviewedActionAccessUsesActualPermissions(t *testing.T) {
	for _, tc := range []struct {
		kind, action, group, version, resource string
		lists                                  []authv1.ResourceAttributes
	}{
		{"Deployment", "scale", "apps", "v1", "deployments", nil},
		{"Deployment", "setContainerResources", "apps", "v1", "deployments", nil},
		{"Deployment", "setPodTemplateLabel", "apps", "v1", "deployments", []authv1.ResourceAttributes{{Version: "v1", Resource: "services", Namespace: "prod", Verb: "list"}}},
		{"Service", "setServiceSelectorLabel", "", "v1", "services", []authv1.ResourceAttributes{{Version: "v1", Resource: "pods", Namespace: "prod", Verb: "list"}, {Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices", Namespace: "prod", Verb: "list"}}},
		{"CronJob", "suspend", "batch", "v1", "cronjobs", nil},
		{"Rollout", "restart", "argoproj.io", "v1alpha1", "rollouts", nil},
	} {
		t.Run(tc.kind+"/"+tc.action, func(t *testing.T) {
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			var got []authv1.ResourceAttributes
			client.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetNamespace() != "" || a.GetResource() != authv1.SchemeGroupVersion.WithResource("selfsubjectaccessreviews") {
					t.Fatal(a)
				}
				req := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
				var review authv1.SelfSubjectAccessReview
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(req.Object, &review); err != nil {
					t.Fatal(err)
				}
				got = append(got, *review.Spec.ResourceAttributes)
				if review.Spec.ResourceAttributes.Verb == "list" {
					_, named, err := unstructured.NestedString(req.Object, "spec", "resourceAttributes", "name")
					if err != nil || named {
						t.Fatalf("list authorization must be namespace-wide: %v", req.Object)
					}
				}
				req.Object["status"] = map[string]any{"allowed": true}
				return true, req, nil
			})
			target, err := reviewedActionTarget(tc.kind, tc.action)
			if err != nil {
				t.Fatal(err)
			}
			if gotAccess := reviewedActionAccess(t.Context(), client, tc.action, target.gvr, "prod", "checkout"); gotAccess != "allowed" {
				t.Fatal(gotAccess)
			}
			want := []authv1.ResourceAttributes{{Group: tc.group, Version: tc.version, Resource: tc.resource, Namespace: "prod", Name: "checkout", Verb: "get"}, {Group: tc.group, Version: tc.version, Resource: tc.resource, Namespace: "prod", Name: "checkout", Verb: "patch"}}
			want = append(want, tc.lists...)
			if !reflect.DeepEqual(got, want) || len(client.Actions()) != len(want) {
				t.Fatalf("requests=%#v, want=%#v", got, want)
			}
		})
	}
}

func TestReviewedActionAccessIncompleteReviews(t *testing.T) {
	allowed := map[string]any{"allowed": true}
	denied := map[string]any{"allowed": false}
	for _, tc := range []struct {
		name       string
		get, patch map[string]any
		want       string
	}{
		{"patch forbidden", allowed, denied, "denied"},
		{"explicit denial", allowed, map[string]any{"allowed": false, "denied": true}, "denied"},
		{"empty status", allowed, map[string]any{}, "unknown"},
		{"contradiction", allowed, map[string]any{"allowed": true, "denied": true}, "unknown"},
		{"evaluation incomplete", allowed, map[string]any{"allowed": true, "evaluationError": "missing authorizer"}, "unknown"},
		{"review API unavailable", nil, allowed, "unknown"},
		{"known denial despite unavailable other review", nil, denied, "denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			client.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
				q := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
				verb, _, _ := unstructured.NestedString(q.Object, "spec", "resourceAttributes", "verb")
				state := tc.patch
				if verb == "get" {
					state = tc.get
				}
				if state == nil {
					return true, nil, errors.New("self-review unavailable")
				}
				q.Object["status"] = state
				return true, q, nil
			})
			if got := reviewedActionAccess(context.Background(), client, "restart", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "prod", "checkout"); got != tc.want {
				t.Fatalf("access %s, want %s", got, tc.want)
			}
		})
	}
}

func TestReviewedActionAccessOnlyAsAuthenticatedCaller(t *testing.T) {
	_, shared := reviewedRestartRouter(t, reviewedDeployment())
	var users []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			t.Errorf("unexpected Kubernetes request: %s %s", r.Method, r.URL.Path)
		}
		u := r.Header.Get("Impersonate-User")
		users = append(users, u)
		if u == "operator" && !slices.Contains(r.Header.Values("Impersonate-Group"), "operators") {
			t.Error("review lost the caller's authenticated groups")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "SelfSubjectAccessReview", "apiVersion": "authorization.k8s.io/v1", "status": map[string]any{"allowed": u == "operator"}})
	}))
	t.Cleanup(api.Close)
	if _, err := k8s.InstallTestServingConnection("access", &rest.Config{Host: api.URL}, shared, appsDiscovery()); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	(&Server{listenAddress: "0.0.0.0", authConfig: auth.Config{Mode: "proxy", Secret: "test-secret"}}).setupAppRoutes(router)
	as := func(user string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set("X-Forwarded-User", user)
			r.Header.Set("X-Forwarded-Groups", "operators")
			router.ServeHTTP(w, r)
		})
	}
	for _, user := range []string{"operator", "observer"} {
		status, got := requestWorkloadAction(t, as(user), "Deployment", "access", `{"action":"pause"}`)
		want := "allowed"
		if user == "observer" {
			want = "denied"
		}
		if status != 200 || got["access"] != want {
			t.Fatalf("%s: %d %v", user, status, got)
		}
	}
	if code, out := requestServiceAction(t, as("operator"), "access", map[string]any{"action": "setServiceSelectorLabel"}); code != 200 || out["access"] != "allowed" {
		t.Fatalf("service preflight: %d %v", code, out)
	}
	for _, tc := range []struct {
		user, kind, body string
		code             int
	}{
		{"operator", "Deployment", `{"action":"pause","user":"someone-else"}`, 400},
		{"operator", "Deployment", `{"action":"pause","action":"restart"}`, 400},
		{"operator", "Pod", `{"action":"restart"}`, 400},
		{"operator", "Node", `{"action":"restart"}`, 400},
		{"operator", "Service", `{"action":"setServiceSelectorLabel"}`, 400},
		{"operator", "Deployment", `{"action":"delete"}`, 400},
		{"radar:ai:org", "Deployment", `{"action":"pause"}`, 403},
		{"system:serviceaccount:prod:agent", "Deployment", `{"action":"pause"}`, 403},
	} {
		if code, _ := requestWorkloadAction(t, as(tc.user), tc.kind, "access", tc.body); code != tc.code {
			t.Errorf("%+v: got %d", tc, code)
		}
	}
	standalone := chi.NewRouter()
	(&Server{}).setupAppRoutes(standalone)
	if code, _ := requestWorkloadAction(t, standalone, "Deployment", "access", `{"action":"pause"}`); code != 403 {
		t.Fatalf("local service-account fallback: %d", code)
	}
	if !reflect.DeepEqual(users, []string{"operator", "operator", "observer", "operator", "operator", "operator", "operator"}) || len(shared.Actions()) != 0 {
		t.Fatalf("users %v, shared-client calls %v", users, shared.Actions())
	}
}
