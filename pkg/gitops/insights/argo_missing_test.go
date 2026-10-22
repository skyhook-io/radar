package insights

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"

	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
)

type recoveredResolver struct{ fakeResolver }

func (recoveredResolver) GetLive(string, string, string, string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "values", "namespace": "prod"}}}
}

func TestArgoPositiveLiveReadClearsEarlierMissingFlag(t *testing.T) {
	app := argoApp(map[string]any{"resources": []any{map[string]any{"kind": "ConfigMap", "namespace": "prod", "name": "values", "status": "OutOfSync"}}})
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{{Ref: gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: "values"}, Missing: true}}}
	changes := argoResourceChanges(app, tree, &recoveredResolver{})
	if len(changes) != 1 || changes[0].Missing || !changes[0].HasLive {
		t.Fatalf("positive live read did not clear absence: %+v", changes)
	}
}

func TestArgoChangesCarryAbsenceIndependentlyOfHealth(t *testing.T) {
	app := argoApp(map[string]any{"resources": []any{map[string]any{"kind": "ConfigMap", "namespace": "prod", "name": "values", "status": "OutOfSync"}}})
	for _, health := range []string{"", "Unknown", "Healthy", "Missing"} {
		for _, missing := range []bool{true, false} {
			tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{{Ref: gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: "values"}, Health: health, Missing: missing}}}
			changes := argoResourceChanges(app, tree, nil)
			if len(changes) != 1 || changes[0].Missing != missing || changes[0].HasLive == missing || changes[0].Health != health {
				t.Fatalf("health %q, missing %v: %+v", health, missing, changes)
			}
		}
	}
}

func TestConfirmedAbsenceCreatesIssueDespiteHealthyControllerVerdict(t *testing.T) {
	ref := gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: "values"}
	for _, sync := range []string{"Synced", "OutOfSync"} {
		app := argoApp(map[string]any{"health": map[string]any{"status": "Healthy"}, "resources": []any{map[string]any{"kind": ref.Kind, "namespace": ref.Namespace, "name": ref.Name, "status": sync, "health": map[string]any{"status": "Healthy"}}}})
		tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{{Ref: ref, Missing: true}}}
		issues := buildIssues(app, tree, "argocd", nil)
		found := false
		for _, issue := range issues {
			if issue.Scope != ScopeResource || issue.Reason != "Missing" {
				continue
			}
			found = true
			if issue.Source != "radar" || issue.Severity != SeverityWarning || issue.Message != "ConfigMap values is not present in the cluster" {
				t.Fatalf("wrong presence issue: %+v", issue)
			}
			if sync == "Synced" && issue.Action != "Refresh the application to check its status." {
				t.Fatalf("stale Synced guidance: %+v", issue)
			}
		}
		if !found {
			t.Fatalf("no confirmed-absence issue: %+v", issues)
		}
		changes := argoResourceChanges(app, tree, nil)
		if changes[0].Health != "Healthy" {
			t.Fatal("controller health must remain available")
		}
	}
}

// After a rollback, a resource only the current revision declares is
// expected to be missing; "sync to create it" would undo the rollback.
func TestMissingResourceAdviceAfterRollback(t *testing.T) {
	ref := gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: "values"}
	app := argoApp(map[string]any{
		"sync":      map[string]any{"status": "OutOfSync"},
		"resources": []any{map[string]any{"kind": ref.Kind, "namespace": ref.Namespace, "name": ref.Name, "status": "OutOfSync"}},
		"operationState": map[string]any{
			"phase":      "Succeeded",
			"operation":  map[string]any{"info": []any{map[string]any{"name": "radarhq.io/rollback", "value": "true"}}, "sync": map[string]any{"revision": "old"}},
			"syncResult": map[string]any{"revision": "old"},
		},
	})
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{{Ref: ref, Missing: true}}}
	for _, issue := range buildIssues(app, tree, "argocd", nil) {
		if issue.Scope == ScopeResource && issue.Reason == "Missing" {
			if issue.Action != "Auto-sync is off after a rollback to old. Syncing deploys the current revision, which can recreate it and undo the rollback." {
				t.Fatalf("missing-resource advice after a rollback: %q", issue.Action)
			}
			return
		}
	}
	t.Fatal("no missing-resource issue")
}
