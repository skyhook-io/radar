package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestGitOpsEmptyRenderHazardRemainsSelfContainedInMCP(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	data, err := os.ReadFile("../issues/testdata/argo-blocked-autosync.json")
	if err != nil {
		t.Fatal(err)
	}
	app := &unstructured.Unstructured{}
	if err := app.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	app.SetNamespace("alpha")
	if err := unstructured.SetNestedMap(app.Object, map[string]any{"phase": "Failed", "message": `namespaces "demo" not found`}, "status", "operationState"); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ApplicationList"}, app)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Application", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("Application cache did not sync")
	}
	ctx := withClusterAdmin(t, "admin")
	getPermCache().Get("admin", nil).SetCanI("get", gvr.Group, gvr.Resource, "alpha", true)
	result, _, err := handleIssuesTool(ctx, nil, issuesInput{Namespace: "alpha", Kind: "Application"})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Issues []issues.Issue `json:"issues"`
	}
	if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Issues) != 4 {
		t.Fatalf("want operation, empty guard, shared and orphaned; info must be omitted: %+v", response.Issues)
	}
	foundOperation, foundGuard := false, false
	for _, issue := range response.Issues {
		switch issue.Reason {
		case "RepeatedResourceWarning":
			t.Fatalf("MCP operational issues must omit repeated-resource info: %+v", issue)
		case "SharedResourceWarning", "OrphanedResourceWarning":
			if issue.Severity != issues.SeverityWarning {
				t.Fatalf("shared/orphaned must stay warning: %+v", issue)
			}
		case "AutoSyncBlockedEmpty":
			foundGuard = true
			if issue.ActionPrimary == "" || issue.ActionSecondaryTitle != "If removing all managed resources is intended" || len(issue.ActionSecondary) != 2 || issue.Action != issue.ActionPrimary+" "+strings.Join(issue.ActionSecondary, " ") {
				t.Fatalf("MCP must retain split and complete guard guidance: %+v", issue)
			}
			if !strings.Contains(issue.Action, "ApplicationSet generator") || !strings.Contains(issue.Action, "resources finalizer decides") || !strings.Contains(issue.Action, "syncPolicy.automated.allowEmpty: true") || !strings.Contains(issue.Action, "all future syncs") {
				t.Fatalf("MCP must retain both intended-render paths: %+v", issue)
			}
		}
		if issue.Reason != "OperationFailed" {
			continue
		}
		const hazard = "The render includes none of the resources this Application manages. A sync with Prune enabled could delete all of them, except resources marked Prune=false; Prune=confirm ones wait for confirmation."
		if !strings.Contains(issue.Action, hazard) || !strings.Contains(issue.Action, "Check the source path") || issue.RemediationKind != "" {
			t.Fatalf("MCP issue lost self-contained hazard guidance: %+v", issue)
		}
		foundOperation = true
	}
	if !foundOperation || !foundGuard {
		t.Fatalf("MCP response omitted operation or guard: %+v", response.Issues)
	}
}
