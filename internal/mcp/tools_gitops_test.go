package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/gitops"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestHandleManageGitOpsRollback(t *testing.T) {
	gvr := schema.GroupVersionResource{
		Group: "argoproj.io", Version: "v1alpha1", Resource: "applications",
	}
	setup := func(t *testing.T, syncPolicy map[string]any) {
		t.Helper()
		spec := map[string]any{"project": "default"}
		if syncPolicy != nil {
			spec["syncPolicy"] = syncPolicy
		}
		app := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": "demo", "namespace": "argocd"},
			"spec":       spec,
			"status": map[string]any{"history": []any{map[string]any{
				"id":       int64(0),
				"revision": "abc123",
				"source":   map[string]any{"repoURL": "https://example.com/repo", "path": "app"},
			}}},
		}}
		setupMCPDynamicResource(t, gvr, "ApplicationList", k8s.APIResource{
			Group: "argoproj.io", Version: "v1alpha1", Kind: "Application",
			Name: "applications", Namespaced: true, Verbs: []string{"get", "patch"},
		}, app)
	}
	historyID := func(id int64) *int64 { return &id }

	t.Run("history_id 0 starts a sync to the first entry", func(t *testing.T) {
		setup(t, nil)
		if _, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{
			Action: "rollback", Tool: "argocd", Namespace: "argocd", Name: "demo", HistoryID: historyID(0),
		}); err != nil {
			t.Fatalf("handleManageGitOps: %v", err)
		}
		app, err := k8s.GetDynamicClient().Resource(gvr).Namespace("argocd").Get(context.Background(), "demo", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get app: %v", err)
		}
		if revision, _, _ := unstructured.NestedString(app.Object, "operation", "sync", "revision"); revision != "abc123" {
			t.Fatalf("operation.sync.revision = %q, want abc123; operation=%v", revision, app.Object["operation"])
		}
	})

	t.Run("missing history_id is rejected", func(t *testing.T) {
		setup(t, nil)
		if _, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{
			Action: "rollback", Tool: "argocd", Namespace: "argocd", Name: "demo",
		}); err == nil {
			t.Fatal("expected an error without history_id")
		}
	})

	t.Run("auto-sync enabled is refused", func(t *testing.T) {
		setup(t, map[string]any{"automated": map[string]any{"prune": true}})
		_, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{
			Action: "rollback", Tool: "argocd", Namespace: "argocd", Name: "demo", HistoryID: historyID(0),
		})
		if !errors.Is(err, gitops.ErrAutoSyncEnabled) {
			t.Fatalf("expected ErrAutoSyncEnabled, got %v", err)
		}
	})
}
