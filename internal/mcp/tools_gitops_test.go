package mcp

import (
	"context"
	"errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	dyn := setupMCPDynamicResource(t, gvr, "ApplicationList", k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Kind: "Application", Name: gvr.Resource, Namespaced: true})
	dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("patch", "applications", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "demo", errors.New(`admission webhook "validation.gatekeeper.sh" denied the request: missing owner`))
	})
	result, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{Action: "refresh", Tool: "argocd", Namespace: "argocd", Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeToolResult(t, result)
	if !result.IsError || decoded["error_code"] != "admission_denied" || !strings.HasPrefix(decoded["error"].(string), "Rejected by an admission policy:") {
		t.Fatalf("result=%v", decoded)
	}
}
