package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestHandleManageGitOpsPreservesProducerNoChange(t *testing.T) {
	gvr := schema.GroupVersionResource{
		Group: "argoproj.io", Version: "v1alpha1", Resource: "applications",
	}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name": "demo", "namespace": "argocd",
		},
		"operation": map[string]any{"sync": map[string]any{"revision": "abc123"}},
		"status": map[string]any{
			"operationState": map[string]any{"phase": "Terminating"},
		},
	}}
	setupMCPDynamicResource(t, gvr, "ApplicationList", k8s.APIResource{
		Group: "argoproj.io", Version: "v1alpha1", Kind: "Application",
		Name: "applications", Namespaced: true, Verbs: []string{"get", "patch"},
	}, app)

	result, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{
		Action: "terminate", Tool: "argocd", Namespace: "argocd", Name: "demo",
	})
	if err != nil {
		t.Fatalf("handleManageGitOps: %v", err)
	}
	decoded := decodeToolResult(t, result)
	if decoded["status"] != "ok" || decoded["noChange"] != true {
		t.Fatalf("result = %+v, want status=ok and noChange=true", decoded)
	}
}

func TestManageGitOpsReportsExplicitPermissionDenied(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := setupMCPDynamicResource(t, gvr, "ApplicationList", k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Kind: "Application", Name: gvr.Resource, Namespaced: true})
	dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("patch", "applications", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "demo", errors.New(`User "opaque-user" cannot patch resource "applications"`))
	})
	result, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{Action: "refresh", Tool: "argocd", Namespace: "argocd", Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeToolResult(t, result)
	if !result.IsError || decoded["error_code"] != "rbac_denied" || decoded["verb"] != "patch" || decoded["resource"] != "applications" || decoded["namespace"] != "argocd" {
		t.Fatalf("result=%+v", decoded)
	}
	if strings.Contains(decoded["error"].(string), "opaque-user") {
		t.Fatalf("raw identity in summary: %v", decoded)
	}
}

func TestManageGitOpsReportsAdmissionDenied(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	policy := apierrors.NewForbidden(gvr.GroupResource(), "demo", errors.New(`ValidatingAdmissionPolicy 'freeze' with binding 'freeze' denied request: Application demo is change-frozen`))
	policy.ErrStatus.Reason, policy.ErrStatus.Code = metav1.StatusReasonInvalid, 422
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"webhook", apierrors.NewForbidden(gvr.GroupResource(), "demo", errors.New(`admission webhook "validation.gatekeeper.sh" denied the request: missing owner`)), "Rejected by admission webhook validation.gatekeeper.sh: missing owner"},
		{"validating admission policy", policy, "Rejected by ValidatingAdmissionPolicy freeze: Application demo is change-frozen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := setupMCPDynamicResource(t, gvr, "ApplicationList", k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Kind: "Application", Name: gvr.Resource, Namespaced: true})
			dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("patch", "applications", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, tc.err
			})
			result, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{Action: "refresh", Tool: "argocd", Namespace: "argocd", Name: "demo"})
			if err != nil {
				t.Fatal(err)
			}
			decoded := decodeToolResult(t, result)
			if !result.IsError || decoded["error_code"] != "admission_denied" || decoded["error"] != tc.want {
				t.Fatalf("result=%v", decoded)
			}
		})
	}
}

func TestManageGitOpsReportsUpstreamSourcePermissionDenied(t *testing.T) {
	for _, verb := range []string{"get", "patch"} {
		t.Run(verb, func(t *testing.T) {
			hr := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": map[string]any{"name": "demo", "namespace": "apps"}, "spec": map[string]any{"chartRef": map[string]any{"kind": "HelmChart", "name": "shared", "namespace": "charts"}}}}
			chart := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "HelmChart", "metadata": map[string]any{"name": "shared", "namespace": "charts"}, "spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo"}}}}
			repo := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "HelmRepository", "metadata": map[string]any{"name": "repo", "namespace": "charts"}}}
			gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
			dyn := setupMCPDynamicResource(t, gvr, "HelmReleaseList", k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Kind: "HelmRelease", Name: gvr.Resource, Namespaced: true}, hr, chart, repo)
			dyn.(*dynamicfake.FakeDynamicClient).PrependReactor(verb, "helmrepositories", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "source.toolkit.fluxcd.io", Resource: "helmrepositories"}, "repo", errors.New(`User "alice" cannot `+verb+` resource "helmrepositories"`))
			})
			result, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{Action: "sync-with-source", Tool: "fluxcd", Kind: "helmrelease", Namespace: "apps", Name: "demo"})
			if err != nil {
				t.Fatal(err)
			}
			decoded := decodeToolResult(t, result)
			if !result.IsError || decoded["error_code"] != "rbac_denied" || decoded["verb"] != verb || decoded["group"] != "source.toolkit.fluxcd.io" || decoded["resource"] != "helmrepositories" || decoded["kind"] != "HelmRepository" || decoded["namespace"] != "charts" || decoded["name"] != "repo" {
				t.Fatalf("result=%+v", decoded)
			}
		})
	}
}

// A sync-with-source can patch the source and then be refused on the target.
// The structured denial must still say the source was reconciled.
func TestManageGitOpsDenialAfterSourcesReconciledKeepsNote(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "demo", "namespace": "apps"}, "spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}}}
	repo := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository", "metadata": map[string]any{"name": "repo", "namespace": "apps"}}}
	gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	dyn := setupMCPDynamicResource(t, gvr, "KustomizationList", k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Kind: "Kustomization", Name: gvr.Resource, Namespaced: true}, ks, repo)
	sourcePatched := false
	dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("patch", "gitrepositories", func(ktesting.Action) (bool, runtime.Object, error) {
		sourcePatched = true
		return true, repo, nil
	})
	dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("patch", "kustomizations", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "demo", errors.New(`User "opaque-user" cannot patch resource "kustomizations"`))
	})
	result, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{Action: "sync-with-source", Tool: "fluxcd", Kind: "kustomization", Namespace: "apps", Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	body := decodeToolResult(t, result)
	if !sourcePatched || !result.IsError || body["error_code"] != "rbac_denied" {
		t.Fatalf("sourcePatched=%v result=%+v", sourcePatched, body)
	}
	if note, _ := body["note"].(string); note != "Reconcile already requested for GitRepository apps/repo." {
		t.Fatalf("denial lost the partial-write note: %+v", body)
	}
	if strings.Contains(body["error"].(string), "opaque-user") {
		t.Fatalf("denial leaked the apiserver user: %+v", body)
	}
}

func TestManageGitOpsMiddleSourceDenialNamesRequestedObjects(t *testing.T) {
	for _, deniedResource := range []string{"helmcharts", "helmreleases"} {
		t.Run(deniedResource, func(t *testing.T) {
			hr := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": map[string]any{"name": "demo", "namespace": "apps"}, "spec": map[string]any{"chartRef": map[string]any{"kind": "HelmChart", "name": "shared", "namespace": "charts"}}}}
			chart := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "HelmChart", "metadata": map[string]any{"name": "shared", "namespace": "charts"}, "spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo"}}}}
			repo := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "HelmRepository", "metadata": map[string]any{"name": "repo", "namespace": "charts"}}}
			gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
			dyn := setupMCPDynamicResource(t, gvr, "HelmReleaseList", k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Kind: "HelmRelease", Name: gvr.Resource, Namespaced: true}, hr, chart, repo)
			dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("patch", deniedResource, func(action ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), "demo", errors.New(`admission webhook "freeze" denied the request: frozen`))
			})
			result, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{Action: "sync-with-source", Tool: "fluxcd", Kind: "helmrelease", Namespace: "apps", Name: "demo"})
			if err != nil {
				t.Fatal(err)
			}
			body := decodeToolResult(t, result)
			want := "Reconcile already requested for HelmRepository charts/repo"
			if deniedResource == "helmreleases" {
				want += ", HelmChart charts/shared"
			}
			if !result.IsError || body["error_code"] != "admission_denied" || body["note"] != want+"." {
				t.Fatalf("partial denial=%+v", body)
			}
		})
	}
}
