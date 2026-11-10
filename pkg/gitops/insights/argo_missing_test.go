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

// After a rollback, a resource only the current revision declares is
// expected to be missing; "sync to create it" would undo the rollback.
func TestMissingResourceAdviceAfterRollback(t *testing.T) {
	ref := gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: "values"}
	rollbackNote := "Auto-sync is off after a rollback to old. Syncing deploys the current revision, which can recreate it and undo the rollback."
	for _, syncStatus := range []string{"OutOfSync", "Unknown"} {
		for _, tc := range []struct {
			name      string
			automated map[string]any
			want      bool
		}{
			{name: "manual", want: true},
			{name: "auto-sync disabled in place", automated: map[string]any{"enabled": false}, want: true},
			// Re-enabling auto-sync before the next operation keeps the rollback
			// operationState; the advice must follow the current policy.
			{name: "auto-sync re-enabled", automated: map[string]any{"selfHeal": false}},
		} {
			t.Run(syncStatus+"/"+tc.name, func(t *testing.T) {
				app := argoApp(map[string]any{
					"sync":      map[string]any{"status": syncStatus},
					"resources": []any{map[string]any{"kind": ref.Kind, "namespace": ref.Namespace, "name": ref.Name, "status": "OutOfSync"}},
					"operationState": map[string]any{
						"phase":      "Succeeded",
						"operation":  map[string]any{"info": []any{map[string]any{"name": "radarhq.io/rollback", "value": "true"}}, "sync": map[string]any{"revision": "old"}},
						"syncResult": map[string]any{"revision": "old"},
					},
				})
				if tc.automated != nil {
					_ = unstructured.SetNestedMap(app.Object, tc.automated, "spec", "syncPolicy", "automated")
				}
				tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{{Ref: ref, Missing: true}}}
				insight := Build(app, tree, nil)
				if got := insight.Summary.RollbackRevision != ""; got != tc.want {
					t.Fatalf("rollback context = %q, want %v", insight.Summary.RollbackRevision, tc.want)
				}
				for _, issue := range insight.Issues {
					if issue.Scope == ScopeResource && issue.Reason == "Missing" {
						if got := issue.Action == rollbackNote; got != tc.want {
							t.Fatalf("missing-resource advice = %q, want rollback note %v", issue.Action, tc.want)
						}
						return
					}
				}
				t.Fatal("no missing-resource issue")
			})
		}
	}
}

// A missing resource's "sync to create it" advice gets the same hazard
// treatment as other sync advice: here a sync would also take over shared
// resources.
func TestMissingResourceAdviceCarriesSyncHazard(t *testing.T) {
	refs := []gitopstree.ResourceRef{
		{Kind: "ConfigMap", Namespace: "prod", Name: "values"},
		{Kind: "ConfigMap", Namespace: "prod", Name: "settings"},
	}
	var resources []any
	tree := &gitopstree.ResourceTree{}
	for _, ref := range refs {
		resources = append(resources, map[string]any{"kind": ref.Kind, "namespace": ref.Namespace, "name": ref.Name, "status": "OutOfSync"})
		tree.Nodes = append(tree.Nodes, gitopstree.Node{Ref: ref, Missing: true})
	}
	app := argoApp(map[string]any{
		"resources":  resources,
		"conditions": []any{map[string]any{"type": "SharedResourceWarning", "message": "ConfigMap/shared is part of applications argocd/demo and other"}},
	})
	found := 0
	for _, issue := range buildIssues(app, tree, "argocd", nil) {
		if issue.Scope == ScopeResource && issue.Reason == "Missing" {
			if !strings.HasPrefix(issue.Action, "Resolve shared ownership before syncing") || issue.Hazard != issue.Action {
				t.Fatalf("missing-resource advice ignores the sync hazard: %+v", issue)
			}
			found++
		}
	}
	if found != len(refs) {
		t.Fatalf("want %d missing-resource issues, got %d", len(refs), found)
	}
}

// After a comparison error, the reported pruning flags cannot establish the
// current render or justify offering a remediation that syncs.
func TestFailedComparisonKeepsReportedPruneHazard(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"namespace": "argocd", "name": "demo"},
		"spec": map[string]any{
			"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "demo"},
			"syncPolicy":  map[string]any{"automated": map[string]any{"prune": true}},
		},
		"status": map[string]any{
			"sync":           map[string]any{"status": "Unknown"},
			"operationState": map[string]any{"phase": "Failed", "message": `namespaces "demo" not found`},
			"resources":      []any{map[string]any{"kind": "ConfigMap", "name": "settings", "namespace": "demo", "requiresPruning": true}},
			"conditions":     []any{map[string]any{"type": "ComparisonError", "message": "Failed to load target state: revision not found"}},
		},
	}}
	for _, issue := range Build(app, nil, nil).Issues {
		if issue.Scope != ScopeOperation {
			continue
		}
		if !strings.Contains(issue.Hazard, "last reported resource list marks every listed resource for pruning, and its latest comparison reported an error") || strings.HasPrefix(issue.Hazard, "The render includes none") {
			t.Fatalf("hazard = %q, want the last-reported-list wording", issue.Hazard)
		}
		if issue.Remediation != nil {
			t.Fatalf("remediation offered while a sync could still prune everything: %+v", issue.Remediation)
		}
		return
	}
	t.Fatal("no operation issue")
}

// A rollback and a current sync hazard together: the rollback note and the
// missing-resource row keep the hazard, because a sync now would also take over
// shared resources.
func TestRollbackKeepsSyncHazard(t *testing.T) {
	ref := gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: "values"}
	app := argoApp(map[string]any{
		"sync":       map[string]any{"status": "OutOfSync"},
		"resources":  []any{map[string]any{"kind": ref.Kind, "namespace": ref.Namespace, "name": ref.Name, "status": "OutOfSync"}},
		"conditions": []any{map[string]any{"type": "SharedResourceWarning", "message": "ConfigMap/shared is part of applications argocd/demo and other"}},
		"operationState": map[string]any{
			"phase":      "Succeeded",
			"operation":  map[string]any{"info": []any{map[string]any{"name": "radarhq.io/rollback", "value": "true"}}, "sync": map[string]any{"revision": "old"}},
			"syncResult": map[string]any{"revision": "old"},
		},
	})
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{{Ref: ref, Missing: true}}}
	insight := Build(app, tree, nil)
	if insight.Summary.RollbackRevision != "old" {
		t.Fatalf("rollback context = %q, want old", insight.Summary.RollbackRevision)
	}
	var sawNote, sawMissing bool
	for _, issue := range insight.Issues {
		switch {
		case issue.Reason == "RolledBack":
			sawNote = true
			if !strings.HasPrefix(issue.Hazard, "Resolve shared ownership before syncing") || issue.Action != issue.Hazard {
				t.Errorf("rollback note dropped the hazard: %+v", issue)
			}
		case issue.Scope == ScopeResource && issue.Reason == "Missing":
			sawMissing = true
			if !strings.HasPrefix(issue.Action, "Resolve shared ownership before syncing") || issue.Hazard != issue.Action {
				t.Errorf("missing-resource row prefers rollback advice over the hazard: %+v", issue)
			}
		}
	}
	if !sawNote || !sawMissing {
		t.Fatalf("want a RolledBack note and a Missing row, got %+v", insight.Issues)
	}
}
