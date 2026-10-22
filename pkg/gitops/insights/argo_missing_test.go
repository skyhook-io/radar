package insights

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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

// A missing resource's "sync to create it" advice gets the same hazard
// treatment as other sync advice: here a sync would also take over shared
// resources.
func TestMissingResourceAdviceCarriesSyncHazard(t *testing.T) {
	ref := gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: "values"}
	app := argoApp(map[string]any{
		"resources":  []any{map[string]any{"kind": ref.Kind, "namespace": ref.Namespace, "name": ref.Name, "status": "OutOfSync"}},
		"conditions": []any{map[string]any{"type": "SharedResourceWarning", "message": "ConfigMap/shared is part of applications argocd/demo and other"}},
	})
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{{Ref: ref, Missing: true}}}
	for _, issue := range buildIssues(app, tree, "argocd", nil) {
		if issue.Scope == ScopeResource && issue.Reason == "Missing" {
			if !strings.HasPrefix(issue.Action, "Resolve shared ownership before syncing") {
				t.Fatalf("missing-resource advice ignores the sync hazard: %q", issue.Action)
			}
			return
		}
	}
	t.Fatal("no missing-resource issue")
}
