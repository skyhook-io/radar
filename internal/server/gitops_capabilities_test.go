package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/gitops"
)

func TestGitOpsCapabilitiesArgo(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "demo", "namespace": "argocd"}}}
	for _, tc := range []struct {
		name    string
		allowed bool
		err     error
	}{{"allowed", true, nil}, {"denied", false, nil}, {"review failed", false, errors.New("offline")}} {
		t.Run(tc.name, func(t *testing.T) {
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), app)
			calls := 0
			actions, err := gitOpsCapabilities(context.Background(), client, "applications", "argocd", "demo", func(_ context.Context, a authv1.ResourceAttributes) (bool, error) {
				calls++
				if a.Group != "argoproj.io" || a.Resource != "applications" || a.Name != "demo" || a.Namespace != "argocd" || a.Verb != "patch" {
					t.Fatalf("wrong SAR: %+v", a)
				}
				return tc.allowed, tc.err
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(actions) != 7 {
				t.Fatalf("calls=%d actions=%v", calls, actions)
			}
			for action, capability := range actions {
				if capability.Allowed != tc.allowed || (!tc.allowed && capability.Reason == "") {
					t.Errorf("%s=%+v", action, capability)
				}
			}
		})
	}
}

func TestGitOpsCapabilitiesFluxPartialAndCrossNamespaceSource(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "demo", "namespace": "apps"}, "spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo", "namespace": "sources"}}}}
	for _, deniedVerb := range []string{"get", "patch", ""} {
		t.Run("source denial "+deniedVerb, func(t *testing.T) {
			calls := []authv1.ResourceAttributes{}
			actions, err := gitOpsCapabilities(context.Background(), dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj), "kustomizations", "apps", "demo", func(_ context.Context, a authv1.ResourceAttributes) (bool, error) {
				calls = append(calls, a)
				if a.Resource == "gitrepositories" {
					if a.Group != "source.toolkit.fluxcd.io" || a.Name != "repo" || a.Namespace != "sources" {
						t.Fatalf("wrong source SAR: %+v", a)
					}
					return a.Verb != deniedVerb, nil
				}
				return true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !actions["reconcile"].Allowed || !actions["suspend"].Allowed || !actions["resume"].Allowed {
				t.Fatalf("local actions denied: %v", actions)
			}
			if actions["sync-with-source"].Allowed != (deniedVerb == "") {
				t.Fatalf("source permission not enforced: %v, calls=%v", actions, calls)
			}
		})
	}
}

func TestGitOpsCapabilitiesUnreadableTarget(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	_, err := gitOpsCapabilities(context.Background(), client, "applications", "argocd", "missing", func(context.Context, authv1.ResourceAttributes) (bool, error) {
		t.Fatal("SAR after failed GET")
		return true, nil
	})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("error=%v", err)
	}
}

func TestWriteGitOpsPermissionDenied(t *testing.T) {
	original := apierrors.NewForbidden(schema.GroupResource{Group: "source.toolkit.fluxcd.io", Resource: "gitrepositories"}, "repo", fmt.Errorf("User opaque-user cannot get resource"))
	denied := &gitops.PermissionDenied{Verb: "get", Group: "source.toolkit.fluxcd.io", Resource: "gitrepositories", Namespace: "sources", Err: original}
	w := httptest.NewRecorder()
	(&Server{}).writeGitOpsError(w, fmt.Errorf("wrapped: %w", denied), "flux", "sync-with-source", "apps", "demo")
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 403 || w.Header().Get("Content-Type") != "application/json" || body["code"] != "rbac_denied" || body["verb"] != "get" || body["namespace"] != "sources" || body["resource"] != "gitrepositories" || body["group"] != "source.toolkit.fluxcd.io" {
		t.Fatalf("response=%d %v %v", w.Code, w.Header(), body)
	}
}

func TestHandleGitOpsCapabilitiesReviewsCaller(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			sarCalls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					if r.Header.Get("Impersonate-User") != "alice" || r.Header.Get("Impersonate-Group") != "radar:viewer" {
						t.Errorf("GET not impersonated: %v", r.Header)
					}
					json.NewEncoder(w).Encode(map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "demo", "namespace": "argocd"}})
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
					t.Errorf("unexpected review request: %s %s", r.Method, r.URL.Path)
				}
				var review authv1.SubjectAccessReview
				if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				sarCalls++
				a := review.Spec.ResourceAttributes
				if review.Spec.User != "alice" || len(review.Spec.Groups) != 1 || review.Spec.Groups[0] != "radar:viewer" || a.Name != "demo" || a.Namespace != "argocd" || a.Resource != "applications" || a.Group != "argoproj.io" || a.Verb != "patch" {
					t.Errorf("wrong caller SAR: %+v", review.Spec)
				}
				review.TypeMeta = metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}
				review.Status.Allowed = allowed
				json.NewEncoder(w).Encode(review)
			}))
			defer api.Close()
			config := &rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}}
			previousConfig := k8s.SetTestConfig(config)
			defer k8s.SetTestConfig(previousConfig)
			client, err := kubernetes.NewForConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			previousClient := k8s.SetTestClient(client)
			defer k8s.SetTestClient(previousClient)
			previousConnection := k8s.GetConnectionStatus()
			k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
			defer k8s.SetConnectionStatus(previousConnection)
			router := chi.NewRouter()
			router.Get("/gitops/capabilities/{kind}/{namespace}/{name}", (&Server{}).handleGitOpsCapabilities)
			request := requestWithUser(http.MethodGet, "/gitops/capabilities/applications/argocd/demo", &auth.User{Username: "alice", Groups: []string{"radar:viewer"}})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, request)
			var body struct {
				Actions map[string]gitOpsActionCapability `json:"actions"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || sarCalls != 1 || body.Actions["refresh"].Allowed != allowed {
				t.Fatalf("response=%d %s SAR calls=%d", w.Code, w.Body, sarCalls)
			}
		})
	}
}
