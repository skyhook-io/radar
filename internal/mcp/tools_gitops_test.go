package mcp

import (
	"context"
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
