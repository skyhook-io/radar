package insights

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
)

// argoApp builds a minimal Argo Application *unstructured.Unstructured for
// tests. Pass status as a nested map; tests that care about specific fields
// override entries directly.
func argoApp(status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"namespace": "argocd", "name": "billing"},
		"status":     status,
	}}
}

func TestBuildIssuesArgoFailedOperationProducesCritical(t *testing.T) {
	root := argoApp(map[string]any{
		"operationState": map[string]any{
			"phase":   "Failed",
			"message": "context deadline exceeded",
		},
	})
	issues := buildIssues(root, nil, "argocd")
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	got := issues[0]
	if got.Severity != "critical" || got.Scope != "operation" || got.Reason != "Failed" {
		t.Fatalf("unexpected issue: %+v", got)
	}
	if got.Message != "context deadline exceeded" {
		t.Fatalf("expected message to be carried through, got %q", got.Message)
	}
}

func TestBuildIssuesArgoRunningOperationProducesInfo(t *testing.T) {
	root := argoApp(map[string]any{
		"operationState": map[string]any{"phase": "Running"},
	})
	issues := buildIssues(root, nil, "argocd")
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Severity != "info" {
		t.Fatalf("expected info severity for Running, got %q", issues[0].Severity)
	}
}

func TestBuildIssuesArgoSortsCriticalBeforeWarning(t *testing.T) {
	// Resource list with a Degraded (critical) and an OutOfSync (warning).
	// The Degraded resource is listed second to verify sort order, not input order.
	root := argoApp(map[string]any{
		"resources": []any{
			map[string]any{
				"kind":   "Service",
				"name":   "auth",
				"sync":   map[string]any{"status": "OutOfSync"},
				"health": map[string]any{"status": "Healthy"},
				"status": "OutOfSync",
			},
			map[string]any{
				"kind":   "Deployment",
				"name":   "auth",
				"health": map[string]any{"status": "Degraded"},
				"status": "Synced",
			},
		},
	})
	issues := buildIssues(root, nil, "argocd")
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].Severity != "critical" {
		t.Fatalf("expected critical first, got %q (%+v)", issues[0].Severity, issues[0])
	}
	if issues[1].Severity != "warning" {
		t.Fatalf("expected warning second, got %q (%+v)", issues[1].Severity, issues[1])
	}
}

func TestBuildIssuesFluxStalledConditionProducesCritical(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]any{"namespace": "flux-system", "name": "apps"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Stalled", "status": "True", "reason": "DependencyNotReady", "message": "depends on missing source"},
			},
		},
	}}
	issues := buildIssues(root, nil, "fluxcd")
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Severity != "critical" {
		t.Fatalf("expected critical, got %q", issues[0].Severity)
	}
	if issues[0].Reason != "DependencyNotReady" {
		t.Fatalf("expected reason from condition, got %q", issues[0].Reason)
	}
}

func TestBuildIssuesDegradedTreeFallsThroughOnQuietRoot(t *testing.T) {
	// No conditions on root → no per-resource issues. Tree summary reports
	// degraded counts → the fallback "DegradedResources" warning fires.
	root := argoApp(map[string]any{})
	tree := &gitopstree.ResourceTree{Summary: gitopstree.Summary{Degraded: 3}}
	issues := buildIssues(root, tree, "argocd")
	if len(issues) != 1 {
		t.Fatalf("expected fallback warning, got %d issues", len(issues))
	}
	if issues[0].Reason != "DegradedResources" {
		t.Fatalf("expected DegradedResources fallback, got %q", issues[0].Reason)
	}
}

func TestBuildIssuesDegradedTreeSuppressedWhenIssuesPresent(t *testing.T) {
	// If the root already produced an issue, the tree-level fallback should not fire.
	root := argoApp(map[string]any{
		"operationState": map[string]any{"phase": "Failed"},
	})
	tree := &gitopstree.ResourceTree{Summary: gitopstree.Summary{Degraded: 3}}
	issues := buildIssues(root, tree, "argocd")
	if len(issues) != 1 {
		t.Fatalf("expected only the operation issue, got %d", len(issues))
	}
	if issues[0].Scope != "operation" {
		t.Fatalf("expected operation issue to win, got %q", issues[0].Scope)
	}
}

// describeArgoAutoSync produces user-visible chip labels — pin every state
// the function should emit so a rename of "Manual" / "Auto · prune" etc.
// requires intentional test updates rather than silently changing UX.
func TestDescribeArgoAutoSync(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		want string
	}{
		{name: "no automated → Manual", spec: map[string]any{"syncPolicy": map[string]any{}}, want: "Manual"},
		{name: "no syncPolicy at all → Manual", spec: map[string]any{}, want: "Manual"},
		{name: "automated empty → Auto", spec: map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{}}}, want: "Auto"},
		{name: "automated prune only → Auto · prune", spec: map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"prune": true}}}, want: "Auto · prune"},
		{name: "automated selfHeal only → Auto · self-heal", spec: map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}}}, want: "Auto · self-heal"},
		{name: "automated prune + selfHeal → Auto · prune · self-heal", spec: map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}}}, want: "Auto · prune · self-heal"},
		// Bool-typed-as-string defensiveness: Argo's CRD schema enforces bool,
		// but unstructured paths can deliver string "true" if a webhook or
		// admission controller mangles values. Without the type assertion
		// failing safely, we'd report "Auto · prune" for "prune": "true".
		{name: "string 'true' for prune treated as not-set → Auto", spec: map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"prune": "true"}}}, want: "Auto"},
		{name: "false flags → Auto", spec: map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"prune": false, "selfHeal": false}}}, want: "Auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := &unstructured.Unstructured{Object: map[string]any{"spec": tc.spec}}
			if got := describeArgoAutoSync(root); got != tc.want {
				t.Fatalf("describeArgoAutoSync = %q, want %q", got, tc.want)
			}
		})
	}
}

// argoResourceChanges' syncResult-status gating decides whether a per-resource
// failure message surfaces in the UI as a red SyncError. Pin the contract so
// a future refactor that simplifies the status check (e.g. `if status != ""`)
// doesn't accidentally hide pre-apply failures or leak success messages.
func TestArgoResourceChangesSyncResultGating(t *testing.T) {
	cases := []struct {
		name        string
		syncResult  map[string]any
		wantSyncErr string
		wantHook    string
	}{
		{name: "SyncFailed status → message surfaced", syncResult: map[string]any{"status": "SyncFailed", "message": "boom"}, wantSyncErr: "boom"},
		{name: "Synced status → message suppressed", syncResult: map[string]any{"status": "Synced", "message": "ok"}, wantSyncErr: ""},
		{name: "Pruned status → message suppressed", syncResult: map[string]any{"status": "Pruned", "message": "removed"}, wantSyncErr: ""},
		{name: "empty status → message surfaced (pre-apply error case)", syncResult: map[string]any{"status": "", "message": "validation failed"}, wantSyncErr: "validation failed"},
		{name: "no status field → message surfaced", syncResult: map[string]any{"message": "schema error"}, wantSyncErr: "schema error"},
		{name: "hookPhase extracted regardless of status", syncResult: map[string]any{"status": "Synced", "hookPhase": "PostSync"}, wantSyncErr: "", wantHook: "PostSync"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := argoApp(map[string]any{
				"resources": []any{map[string]any{
					"kind":       "Deployment",
					"name":       "auth",
					"syncResult": tc.syncResult,
				}},
			})
			out := argoResourceChanges(root)
			if len(out) != 1 {
				t.Fatalf("expected 1 change, got %d", len(out))
			}
			if out[0].SyncError != tc.wantSyncErr {
				t.Fatalf("SyncError = %q, want %q", out[0].SyncError, tc.wantSyncErr)
			}
			if out[0].HookPhase != tc.wantHook {
				t.Fatalf("HookPhase = %q, want %q", out[0].HookPhase, tc.wantHook)
			}
		})
	}
}
