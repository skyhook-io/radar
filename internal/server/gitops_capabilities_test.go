package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	ktesting "k8s.io/client-go/testing"

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
				if a.Group != "argoproj.io" || a.Resource != "applications" || a.Name != "demo" || a.Namespace != "argocd" || (a.Verb != "patch" && a.Verb != "get") {
					t.Fatalf("wrong SAR: %+v", a)
				}
				return tc.allowed, tc.err
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || len(actions) != 7 {
				t.Fatalf("calls=%d actions=%v", calls, actions)
			}
			for action, capability := range actions {
				if tc.err != nil {
					if capability.Allowed != nil || capability.Reason == "" {
						t.Errorf("unknown %s=%+v", action, capability)
					}
					continue
				}
				wantDenied := 0
				if !tc.allowed {
					wantDenied = 2
					if action == "refresh" {
						wantDenied = 1
					}
				}
				if capability.Allowed == nil || *capability.Allowed != tc.allowed || len(capability.Denied) != wantDenied {
					t.Errorf("%s=%+v", action, capability)
				}
			}
		})
	}
}

func TestGitOpsCapabilitiesFluxPartialAndCrossNamespaceSource(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "demo", "namespace": "apps"}, "spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo", "namespace": "sources"}}}}
	for _, tc := range []struct {
		name   string
		denied []string
	}{{"none", nil}, {"get", []string{"get"}}, {"patch", []string{"patch"}}, {"get and patch", []string{"get", "patch"}}} {
		t.Run("source denial "+tc.name, func(t *testing.T) {
			calls := []authv1.ResourceAttributes{}
			actions, err := gitOpsCapabilities(context.Background(), dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj), "kustomizations", "apps", "demo", func(_ context.Context, a authv1.ResourceAttributes) (bool, error) {
				calls = append(calls, a)
				if a.Resource == "gitrepositories" {
					if a.Group != "source.toolkit.fluxcd.io" || a.Name != "repo" || a.Namespace != "sources" {
						t.Fatalf("wrong source SAR: %+v", a)
					}
					return !slices.Contains(tc.denied, a.Verb), nil
				}
				return true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !(*actions["reconcile"].Allowed) || !(*actions["suspend"].Allowed) || !(*actions["resume"].Allowed) {
				t.Fatalf("local actions denied: %v", actions)
			}
			sync := actions["sync-with-source"]
			if *sync.Allowed != (len(tc.denied) == 0) || len(sync.Denied) != len(tc.denied) {
				t.Fatalf("source permission not enforced: %+v, calls=%v", sync, calls)
			}
			for i, verb := range tc.denied {
				want := gitOpsPermission{Verb: verb, Group: "source.toolkit.fluxcd.io", Resource: "gitrepositories", Namespace: "sources", Name: "repo", Kind: "GitRepository", Source: true}
				if sync.Denied[i] != want {
					t.Fatalf("denied[%d]=%+v, want %+v", i, sync.Denied[i], want)
				}
			}
			if len(calls) != 3 {
				t.Fatalf("each permission should be reviewed once, got %d reviews: %v", len(calls), calls)
			}
		})
	}
}

// A role that can patch nothing must see both the target and the source
// grant for Sync with source, not just the first denied operation.
func TestGitOpsCapabilitiesListsEveryDeniedPermission(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "demo", "namespace": "apps"}, "spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}}}
	actions, err := gitOpsCapabilities(context.Background(), dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj), "kustomizations", "apps", "demo", func(_ context.Context, a authv1.ResourceAttributes) (bool, error) {
		return a.Verb == "get", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []gitOpsPermission{
		{Verb: "patch", Group: "kustomize.toolkit.fluxcd.io", Resource: "kustomizations", Namespace: "apps", Name: "demo", Kind: "Kustomization"},
		{Verb: "patch", Group: "source.toolkit.fluxcd.io", Resource: "gitrepositories", Namespace: "apps", Name: "repo", Kind: "GitRepository", Source: true},
	}
	if sync := actions["sync-with-source"]; *sync.Allowed || !slices.Equal(sync.Denied, want) {
		t.Fatalf("sync-with-source=%+v", sync)
	}
	if reconcile := actions["reconcile"]; *reconcile.Allowed || !slices.Equal(reconcile.Denied, want[:1]) {
		t.Fatalf("reconcile=%+v", reconcile)
	}
}

func TestGitOpsCapabilitiesDeniedWinsOverUnknown(t *testing.T) {
	actions, err := gitOpsCapabilities(context.Background(), nil, "applications", "argocd", "demo", func(_ context.Context, a authv1.ResourceAttributes) (bool, error) {
		if a.Verb == "get" {
			return false, errors.New("offline")
		}
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sync := actions["sync"]; sync.Allowed == nil || *sync.Allowed || len(sync.Denied) != 1 || sync.Denied[0].Verb != "patch" {
		t.Fatalf("sync=%+v", sync)
	}
}

func TestGitOpsCapabilitiesUnreadableTarget(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	_, err := gitOpsCapabilities(context.Background(), client, "kustomizations", "argocd", "missing", func(context.Context, authv1.ResourceAttributes) (bool, error) {
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
	if w.Code != 403 || w.Header().Get("Content-Type") != "application/json" || body["error_code"] != "rbac_denied" || body["verb"] != "get" || body["namespace"] != "sources" || body["resource"] != "gitrepositories" || body["group"] != "source.toolkit.fluxcd.io" {
		t.Fatalf("response=%d %v %v", w.Code, w.Header(), body)
	}
}

func TestHandleGitOpsCapabilitiesReviewsCaller(t *testing.T) {
	for _, tc := range []struct {
		allowed bool
		local   bool
	}{{false, false}, {true, false}, {false, true}, {true, true}} {
		allowed := tc.allowed
		t.Run(fmt.Sprintf("allowed=%t/local=%t", allowed, tc.local), func(t *testing.T) {
			sarCalls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					t.Error("Argo capabilities should review actions without reading the target")
					if r.Header.Get("Impersonate-User") != "alice" || r.Header.Get("Impersonate-Group") != "radar:viewer" {
						t.Errorf("GET not impersonated: %v", r.Header)
					}
					json.NewEncoder(w).Encode(map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "demo", "namespace": "argocd"}})
					return
				}
				path := "/apis/authorization.k8s.io/v1/subjectaccessreviews"
				if tc.local {
					path = "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews"
				}
				if r.Method != http.MethodPost || r.URL.Path != path {
					t.Errorf("unexpected review request: %s %s", r.Method, r.URL.Path)
				}
				var review authv1.SubjectAccessReview
				if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				sarCalls++
				if tc.local && (review.Spec.User != "" || len(review.Spec.Groups) != 0) {
					t.Errorf("SSAR supplied a subject: %+v", review.Spec)
				}
				a := review.Spec.ResourceAttributes
				if !tc.local && (review.Spec.User != "alice" || len(review.Spec.Groups) != 2 || review.Spec.Groups[0] != "radar:viewer" || review.Spec.Groups[1] != "system:authenticated") {
					t.Errorf("wrong caller identity: %+v", review.Spec)
				}
				if a.Name != "demo" || a.Namespace != "argocd" || a.Resource != "applications" || a.Group != "argoproj.io" || (a.Verb != "patch" && a.Verb != "get") {
					t.Errorf("wrong caller SAR: %+v", review.Spec)
				}
				review.TypeMeta = metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}
				if tc.local {
					review.Kind = "SelfSubjectAccessReview"
				}
				review.Status.Allowed = allowed
				if allowed {
					review.Status.EvaluationError = "another authorizer failed"
				}
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
			if tc.local {
				request = httptest.NewRequest(http.MethodGet, "/gitops/capabilities/applications/argocd/demo", nil)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, request)
			var body struct {
				Actions map[string]gitOpsActionCapability `json:"actions"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || sarCalls != 2 || *body.Actions["refresh"].Allowed != allowed {
				t.Fatalf("response=%d %s SAR calls=%d", w.Code, w.Body, sarCalls)
			}
		})
	}
}

// A Flux target the caller can't read denies every action on the read, and
// still names a missing patch so the grant guidance is complete.
func TestGitOpsCapabilitiesForbiddenTarget(t *testing.T) {
	read := gitOpsPermission{Verb: "get", Group: "kustomize.toolkit.fluxcd.io", Resource: "kustomizations", Namespace: "apps", Name: "demo", Kind: "Kustomization"}
	patch := read
	patch.Verb = "patch"
	for _, tc := range []struct {
		name         string
		patchAllowed bool
		want         []gitOpsPermission
	}{{"patch allowed", true, []gitOpsPermission{read}}, {"patch denied", false, []gitOpsPermission{read, patch}}} {
		t.Run(tc.name, func(t *testing.T) {
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			client.PrependReactor("get", "kustomizations", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "kustomize.toolkit.fluxcd.io", Resource: "kustomizations"}, "demo", errors.New(`User "alice" cannot get resource "kustomizations" in API group "kustomize.toolkit.fluxcd.io" in the namespace "apps"`))
			})
			reviews := 0
			actions, err := gitOpsCapabilities(context.Background(), client, "kustomizations", "apps", "demo", func(_ context.Context, a authv1.ResourceAttributes) (bool, error) {
				reviews++
				if a.Verb != "patch" || a.Resource != "kustomizations" {
					t.Fatalf("unexpected review %+v", a)
				}
				return tc.patchAllowed, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if reviews != 1 || len(actions) != 4 {
				t.Fatalf("reviews=%d actions=%v", reviews, actions)
			}
			for action, capability := range actions {
				if capability.Allowed == nil || *capability.Allowed || !slices.Equal(capability.Denied, tc.want) {
					t.Errorf("%s=%+v", action, capability)
				}
			}
		})
	}
}

func TestGitOpsCapabilitiesUnclassifiedForbiddenTarget(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	client.PrependReactor("get", "kustomizations", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "kustomize.toolkit.fluxcd.io", Resource: "kustomizations"}, "demo", errors.New("blocked"))
	})
	_, err := gitOpsCapabilities(context.Background(), client, "kustomizations", "apps", "demo", func(context.Context, authv1.ResourceAttributes) (bool, error) { return true, nil })
	if !apierrors.IsForbidden(err) {
		t.Fatalf("err=%v", err)
	}
}

func TestGitOpsCapabilitiesRefreshDoesNotRequireGet(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	actions, err := gitOpsCapabilities(context.Background(), client, "applications", "argocd", "demo", func(_ context.Context, attrs authv1.ResourceAttributes) (bool, error) {
		return attrs.Verb == "patch", nil
	})
	if err != nil || !*actions["refresh"].Allowed || *actions["sync"].Allowed || len(actions["sync"].Denied) != 1 || actions["sync"].Denied[0].Verb != "get" || len(client.Actions()) != 0 {
		t.Fatalf("%v %v", actions, err)
	}
}

func TestWriteGitOpsAdmissionDenied(t *testing.T) {
	gr := schema.GroupResource{Group: "argoproj.io", Resource: "applications"}
	policyDenial := func(reason metav1.StatusReason, code int32) error {
		err := apierrors.NewForbidden(gr, "demo", errors.New(`ValidatingAdmissionPolicy 'freeze' with binding 'freeze' denied request: Application demo is change-frozen`))
		err.ErrStatus.Reason, err.ErrStatus.Code = reason, code
		return err
	}
	for _, tc := range []struct {
		name    string
		err     error
		summary string
	}{
		{"webhook", apierrors.NewForbidden(gr, "demo", errors.New(`admission webhook "validation.gatekeeper.sh" denied the request: missing owner`)), "Rejected by admission webhook validation.gatekeeper.sh: missing owner"},
		{"policy default reason", policyDenial(metav1.StatusReasonInvalid, http.StatusUnprocessableEntity), "Rejected by ValidatingAdmissionPolicy freeze: Application demo is change-frozen"},
		{"policy forbidden reason", policyDenial(metav1.StatusReasonForbidden, http.StatusForbidden), "Rejected by ValidatingAdmissionPolicy freeze: Application demo is change-frozen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := gitops.ClassifyPermissionError(tc.err, "patch", schema.GroupVersionResource{Group: "argoproj.io", Resource: "applications"}, "argocd", "demo")
			w := httptest.NewRecorder()
			(&Server{}).writeGitOpsError(w, fmt.Errorf("failed to refresh Application argocd/demo: %w", err), "argo", "refresh", "argocd", "demo")
			var body map[string]any
			json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != 403 || body["error_code"] != "admission_denied" || body["summary"] != tc.summary || body["verb"] != nil {
				t.Fatalf("%d %v", w.Code, body)
			}
		})
	}
}

func TestWriteGitOpsSyncWithSourceUnsupported(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": map[string]any{"name": "demo", "namespace": "apps"}, "spec": map[string]any{"chartRef": map[string]any{"kind": "OCIRepository", "name": "chart"}}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	_, err := gitops.SyncFluxWithSource(context.Background(), client, "helmrelease", "apps", "demo")
	w := httptest.NewRecorder()
	(&Server{}).writeGitOpsError(w, err, "flux", "sync-with-source", "apps", "demo")
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusBadRequest || !strings.Contains(body["error"].(string), "spec.chartRef") {
		t.Fatalf("%d %v", w.Code, body)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "patch" {
			t.Fatalf("patched an unsupported sync: %v", action)
		}
	}
}

func TestGitOpsCapabilitiesUnsupportedSyncWithSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		obj  *unstructured.Unstructured
	}{
		{"chartRef", &unstructured.Unstructured{Object: map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": map[string]any{"name": "demo", "namespace": "apps"}, "spec": map[string]any{"chartRef": map[string]any{"kind": "OCIRepository", "name": "chart"}}}}},
		{"no source", &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "demo", "namespace": "apps"}, "spec": map[string]any{}}}},
	} {
		for _, allowed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/allowed=%t", tc.name, allowed), func(t *testing.T) {
				resource := strings.ToLower(tc.obj.GetKind()) + "s"
				actions, err := gitOpsCapabilities(context.Background(), dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), tc.obj), resource, "apps", "demo", func(context.Context, authv1.ResourceAttributes) (bool, error) { return allowed, nil })
				if err != nil {
					t.Fatal(err)
				}
				capability := actions["sync-with-source"]
				if !capability.Unsupported || capability.Allowed != nil || capability.Reason == "" || strings.Contains(capability.Reason, "retrying") {
					t.Fatalf("sync-with-source=%+v", capability)
				}
				if reconcile := actions["reconcile"]; *reconcile.Allowed != allowed {
					t.Fatalf("reconcile=%+v", reconcile)
				}
			})
		}
	}
}

func TestWriteGitOpsUnexpectedErrorLogsFailure(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	w := httptest.NewRecorder()
	(&Server{}).writeGitOpsError(w, errors.New("cluster read failed"), "gitops", "capabilities", "apps", "demo")
	if w.Code != http.StatusInternalServerError || !strings.Contains(output.String(), "[gitops] Failed to capabilities apps/demo: cluster read failed") {
		t.Fatalf("response=%d log=%s", w.Code, output.String())
	}
}

func TestWriteErrorStructuredFieldsPreserveMessage(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).writeError(w, http.StatusForbidden, "permission denied", map[string]string{"error": "overridden", "error_code": "rbac_denied", "verb": "patch"})
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusForbidden || w.Header().Get("Content-Type") != "application/json" || body["error"] != "permission denied" || body["error_code"] != "rbac_denied" || body["verb"] != "patch" {
		t.Fatalf("response=%d %v %v", w.Code, w.Header(), body)
	}
}
