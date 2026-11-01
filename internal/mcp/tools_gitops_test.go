package mcp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

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
		result, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{
			Action: "rollback", Tool: "argocd", Namespace: "argocd", Name: "demo", HistoryID: historyID(0),
		})
		if err != nil {
			t.Fatalf("handleManageGitOps: %v", err)
		}
		want := "Rollback to history #0 initiated for ArgoCD Application argocd/demo. Auto-sync is off. Later changes can still cause drift. Syncing or using manage_gitops with tool=argocd and action=resume deploys current Git and can undo the rollback."
		if got := decodeToolResult(t, result)["message"]; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
		app, err := k8s.GetDynamicClient().Resource(gvr).Namespace("argocd").Get(context.Background(), "demo", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get app: %v", err)
		}
		if revision, _, _ := unstructured.NestedString(app.Object, "operation", "sync", "revision"); revision != "abc123" {
			t.Fatalf("operation.sync.revision = %q, want abc123; operation=%v", revision, app.Object["operation"])
		}
	})

	t.Run("dry-run only promises a preview", func(t *testing.T) {
		setup(t, nil)
		dryRun := true
		result, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{
			Action: "rollback", Tool: "argocd", Namespace: "argocd", Name: "demo", HistoryID: historyID(0), DryRun: &dryRun,
		})
		if err != nil {
			t.Fatalf("handleManageGitOps: %v", err)
		}
		want := "Rollback dry-run to history #0 initiated for ArgoCD Application argocd/demo — preview only; no changes will be applied"
		if got := decodeToolResult(t, result)["message"]; got != want {
			t.Fatalf("message = %q, want %q", got, want)
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
		if !strings.Contains(err.Error(), "(MCP: manage_gitops with tool=argocd and action=suspend)") {
			t.Fatalf("error lacks the suspend action: %v", err)
		}
	})
}

// Through the real MCP decode path: an omitted prune doesn't prune, an
// omitted sync_options uses the Application's own, and [] clears them.
func TestManageGitOpsSyncDefaults(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	appOptions := []any{"CreateNamespace=true", "ServerSideApply=true"}
	for _, tc := range []struct {
		name        string
		args        map[string]any
		wantPrune   bool
		wantOptions []any
	}{
		{name: "defaults", args: map[string]any{}, wantOptions: appOptions},
		{name: "explicit empty options", args: map[string]any{"sync_options": []any{}}},
		{name: "explicit options and prune", args: map[string]any{"sync_options": []any{"Replace=true"}, "prune": true}, wantPrune: true, wantOptions: []any{"Replace=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupMCPDynamicResource(t, gvr, "ApplicationList", k8s.APIResource{
				Group: "argoproj.io", Version: "v1alpha1", Kind: "Application",
				Name: "applications", Namespaced: true, Verbs: []string{"get", "patch"},
			}, &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "argoproj.io/v1alpha1",
				"kind":       "Application",
				"metadata":   map[string]any{"name": "demo", "namespace": "argocd"},
				"spec":       map[string]any{"project": "default", "syncPolicy": map[string]any{"syncOptions": appOptions}},
			}})
			args := map[string]any{"action": "sync", "tool": "argocd", "namespace": "argocd", "name": "demo"}
			for k, v := range tc.args {
				args[k] = v
			}
			res, err := connectTestServer(t).CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "manage_gitops", Arguments: args})
			if err != nil || res.IsError {
				t.Fatalf("CallTool: err=%v result=%s", err, renderContent(res.Content))
			}
			app, err := k8s.GetDynamicClient().Resource(gvr).Namespace("argocd").Get(context.Background(), "demo", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get app: %v", err)
			}
			sync, _, _ := unstructured.NestedMap(app.Object, "operation", "sync")
			if sync["prune"] != tc.wantPrune {
				t.Fatalf("prune = %#v, want %v", sync["prune"], tc.wantPrune)
			}
			got, _ := sync["syncOptions"].([]any)
			if len(got) != len(tc.wantOptions) || (len(got) > 0 && !reflect.DeepEqual(got, tc.wantOptions)) {
				t.Fatalf("syncOptions = %#v, want %#v", sync["syncOptions"], tc.wantOptions)
			}
			if username, _, _ := unstructured.NestedString(app.Object, "operation", "initiatedBy", "username"); username != "radar" {
				t.Fatalf("initiatedBy.username = %q, want radar without per-user auth", username)
			}
		})
	}
}

// sync_options is accepted only by sync: an explicit empty list sent to
// another action is refused like any other option it can't use.
func TestValidateGitOpsActionInputRejectsEmptySyncOptions(t *testing.T) {
	err := validateGitOpsActionInput("rollback", manageGitOpsInput{SyncOptions: []string{}})
	if err == nil || !strings.Contains(err.Error(), "sync_options") {
		t.Fatalf("error = %v, want sync_options rejected", err)
	}
}

// resume on an Application Radar never suspended turns auto-sync on with
// prune off, as Argo CD's UI does.
func TestManageGitOpsResumeNeverAutomatedLeavesPruneOff(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	setupMCPDynamicResource(t, gvr, "ApplicationList", k8s.APIResource{
		Group: "argoproj.io", Version: "v1alpha1", Kind: "Application",
		Name: "applications", Namespaced: true, Verbs: []string{"get", "patch"},
	}, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "demo", "namespace": "argocd"},
		"spec":       map[string]any{"project": "default"},
	}})
	res, err := connectTestServer(t).CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "manage_gitops", Arguments: map[string]any{
		"action": "resume", "tool": "argocd", "namespace": "argocd", "name": "demo",
	}})
	if err != nil || res.IsError {
		t.Fatalf("CallTool: err=%v result=%s", err, renderContent(res.Content))
	}
	app, err := k8s.GetDynamicClient().Resource(gvr).Namespace("argocd").Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	automated, _, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
	if !reflect.DeepEqual(automated, map[string]any{"prune": false, "selfHeal": true}) {
		t.Fatalf("automated = %#v, want prune off and selfHeal on", automated)
	}
}

func TestManageGitOpsSyncAutoSyncRevisionGuidance(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	setupMCPDynamicResource(t, gvr, "ApplicationList", k8s.APIResource{
		Group: "argoproj.io", Version: "v1alpha1", Kind: "Application",
		Name: "applications", Namespaced: true, Verbs: []string{"get", "patch"},
	}, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "demo", "namespace": "argocd"},
		"spec": map[string]any{
			"source":     map[string]any{"targetRevision": "main"},
			"syncPolicy": map[string]any{"automated": map[string]any{}},
		},
	}})
	_, _, err := handleManageGitOps(context.Background(), nil, manageGitOpsInput{
		Action: "sync", Tool: "argocd", Namespace: "argocd", Name: "demo", Revision: "old-sha",
	})
	if !errors.Is(err, gitops.ErrAutoSyncEnabled) || !strings.Contains(err.Error(), "(MCP: manage_gitops with tool=argocd and action=suspend)") {
		t.Fatalf("error = %v, want ErrAutoSyncEnabled with MCP suspend guidance", err)
	}
	res, callErr := connectTestServer(t).CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "manage_gitops", Arguments: map[string]any{
		"action": "sync", "tool": "argocd", "namespace": "argocd", "name": "demo", "revision": "old-sha",
	}})
	if callErr != nil || !res.IsError || !strings.Contains(renderContent(res.Content), "action=suspend") {
		t.Fatalf("CallTool: err=%v result=%v, want tool error with suspend guidance", callErr, res)
	}
}
