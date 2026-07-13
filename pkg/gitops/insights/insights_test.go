package insights

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/gitops/diagnose"
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
	issues := buildIssues(root, nil, "argocd", nil)
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

func TestBuildArgoCleansControllerMessages(t *testing.T) {
	rawOperation := "rpc error: code = Unknown desc = app path does not exist"
	rawResource := "rpc error: code = Unknown desc = resource hook failed"
	root := argoApp(map[string]any{
		"operationState": map[string]any{
			"phase":     "Failed",
			"message":   rawOperation,
			"startedAt": "2026-05-03T12:00:00Z",
		},
		"resources": []any{
			map[string]any{
				"group":     "apps",
				"kind":      "Deployment",
				"namespace": "apps",
				"name":      "api",
				"status":    "OutOfSync",
				"syncResult": map[string]any{
					"status":  "SyncFailed",
					"message": rawResource,
				},
			},
		},
	})

	got := Build(root, nil, nil)
	if got.Summary.OperationMessage != "app path does not exist" {
		t.Fatalf("summary operation message = %q, want cleaned app path error", got.Summary.OperationMessage)
	}
	if got.Summary.RawOperationMessage != rawOperation {
		t.Fatalf("summary raw operation message = %q, want %q", got.Summary.RawOperationMessage, rawOperation)
	}
	var opIssue *Issue
	for i := range got.Issues {
		if got.Issues[i].Scope == ScopeOperation {
			opIssue = &got.Issues[i]
			break
		}
	}
	if opIssue == nil || opIssue.Message != "app path does not exist" {
		t.Fatalf("operation issue did not carry cleaned message: %+v", got.Issues)
	}
	if opIssue.RawMessage != rawOperation {
		t.Fatalf("operation issue raw message = %q, want %q", opIssue.RawMessage, rawOperation)
	}
	if len(got.History) != 1 || got.History[0].Message != "app path does not exist" {
		t.Fatalf("history did not carry cleaned message: %+v", got.History)
	}
	if got.History[0].RawMessage != rawOperation {
		t.Fatalf("history raw message = %q, want %q", got.History[0].RawMessage, rawOperation)
	}
	if len(got.Changes) != 1 || got.Changes[0].SyncError != "resource hook failed" {
		t.Fatalf("resource change did not carry cleaned sync error: %+v", got.Changes)
	}
	if got.Changes[0].RawSyncError != rawResource {
		t.Fatalf("resource change raw sync error = %q, want %q", got.Changes[0].RawSyncError, rawResource)
	}
	for _, msg := range []string{
		got.Summary.OperationMessage,
		opIssue.Message,
		got.History[0].Message,
		got.Changes[0].SyncError,
	} {
		if strings.Contains(msg, "rpc error") || strings.Contains(msg, "Unknown desc") {
			t.Fatalf("Argo controller message leaked gRPC envelope: %q", msg)
		}
	}
}

func TestBuildIssuesArgoRunningOperationProducesInfo(t *testing.T) {
	root := argoApp(map[string]any{
		"operationState": map[string]any{"phase": "Running"},
	})
	issues := buildIssues(root, nil, "argocd", nil)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Severity != "info" {
		t.Fatalf("expected info severity for Running, got %q", issues[0].Severity)
	}
}

func TestBuildIssuesArgoSortsCriticalBeforeWarning(t *testing.T) {
	// A Degraded resource (critical, resource scope) plus an app-level
	// ManualDrift warning (OutOfSync with auto-sync off). The detector chain
	// appends the warning first; the severity-stable sort must hoist the
	// critical resource issue above it.
	root := argoApp(map[string]any{
		"sync": map[string]any{"status": "OutOfSync"},
		"resources": []any{
			map[string]any{
				"kind":   "Deployment",
				"name":   "auth",
				"health": map[string]any{"status": "Degraded"},
				"status": "Synced",
			},
		},
	})
	issues := buildIssues(root, nil, "argocd", nil)
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d (%+v)", len(issues), issues)
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
	issues := buildIssues(root, nil, "fluxcd", nil)
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
	issues := buildIssues(root, tree, "argocd", nil)
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
	issues := buildIssues(root, tree, "argocd", nil)
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
			out := argoResourceChanges(root, nil)
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

// TestRemediationFromParsed_CreateNamespace pins the adapter that maps the
// vocabulary-neutral remediation primitives from pkg/gitops/diagnose onto the
// insights Remediation wire type. The pure parse-table coverage lives in
// pkg/gitops/diagnose; this asserts the one-click fix still reaches the
// failure card after the boundary crossing.
func TestRemediationFromParsed_CreateNamespace(t *testing.T) {
	parsed := diagnose.ParseArgoOperationError(`failed to create resource: namespaces "demo-broken-sync" not found`)
	got := remediationFromParsed(parsed)
	if got == nil {
		t.Fatalf("expected Remediation, got nil")
	}
	if got.Kind != RemediationCreateNamespace {
		t.Errorf("Kind = %q, want %q", got.Kind, RemediationCreateNamespace)
	}
	if got.Target != "demo-broken-sync" {
		t.Errorf("Target = %q, want %q", got.Target, "demo-broken-sync")
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestRemediationFromParsed_NoRemediation confirms a parsed failure without a
// structured fix yields a nil Remediation (the common case) rather than a
// broken button.
func TestRemediationFromParsed_NoRemediation(t *testing.T) {
	parsed := diagnose.ParseArgoOperationError(`Deployment.apps "billing" is forbidden: User "x" cannot patch resource`)
	if got := remediationFromParsed(parsed); got != nil {
		t.Errorf("remediationFromParsed = %+v, want nil for non-remediable failure", got)
	}
}

// TestFluxPhaseLabel pins the (status, reason) → outcome mapping used by
// Flux history rows. The substring matches are order-sensitive; a reorder
// (e.g. "error" before "succeed") could shadow "PartialSucceededError"-type
// reasons. The fallback paths ensure unknown reasons surface to the UI as
// the raw reason rather than an empty chip.
func TestFluxPhaseLabel(t *testing.T) {
	cases := []struct {
		status string
		reason string
		want   string
	}{
		{"True", "InstallSucceeded", "Succeeded"},
		{"True", "UpgradeSucceeded", "Succeeded"},
		{"False", "UpgradeFailed", "Failed"},
		{"False", "ArtifactFetchError", "Failed"},
		{"True", "Progressing", "Reconciling"},
		{"True", "Reconciling", "Reconciling"},
		{"True", "Suspended", "Suspended"},
		{"True", "WeirdNovelReason", "WeirdNovelReason"}, // unknown → raw reason
		{"True", "", "True"},                             // empty reason → status
		{"", "", ""},                                     // both empty → empty
	}
	for _, tc := range cases {
		if got := fluxPhaseLabel(tc.status, tc.reason); got != tc.want {
			t.Errorf("fluxPhaseLabel(%q, %q) = %q, want %q", tc.status, tc.reason, got, tc.want)
		}
	}
}

func TestRemediationValidate_RejectsEmptyTarget(t *testing.T) {
	r := &Remediation{Kind: RemediationCreateNamespace}
	if err := r.Validate(); err == nil {
		t.Errorf("Validate() on create-namespace with empty Target = nil, want error")
	}
}

// TestRemediationConstantMatchesDiagnose pins the cross-package contract: the
// issues-engine path ships diagnose.RemediationCreateNamespace while the
// insights path ships insights.RemediationCreateNamespace, and both must equal
// the literal the frontend dispatches on. This is the only package that can
// import both, so the equality assertion lives here.
func TestRemediationConstantMatchesDiagnose(t *testing.T) {
	if diagnose.RemediationCreateNamespace != string(RemediationCreateNamespace) {
		t.Errorf("diagnose.RemediationCreateNamespace=%q != insights.RemediationCreateNamespace=%q — one-click fix wiring would silently break",
			diagnose.RemediationCreateNamespace, string(RemediationCreateNamespace))
	}
}

func TestNewCreateNamespaceRemediation_NilOnEmpty(t *testing.T) {
	if r := NewCreateNamespaceRemediation("", "hint"); r != nil {
		t.Errorf("NewCreateNamespaceRemediation(\"\", …) = %v, want nil", r)
	}
	if r := NewCreateNamespaceRemediation("demo", "hint"); r == nil || r.Target != "demo" {
		t.Errorf("NewCreateNamespaceRemediation(\"demo\", …) = %v, want valid", r)
	}
}

func TestBuildIssuesSuppressesResourceIssueDuplicatedByOperationFailure(t *testing.T) {
	// When the operation message names CRD scaledjobs.keda.sh AND the
	// resources[] list also flags the same CRD as Missing, we want only
	// the operation issue. The resource issue is the same root cause from
	// a different angle and adds noise.
	root := argoApp(map[string]any{
		"operationState": map[string]any{
			"phase":   "Failed",
			"message": `error when patching "/dev/shm/foo": CustomResourceDefinition.apiextensions.k8s.io "scaledjobs.keda.sh" is invalid: metadata.annotations: Too long`,
		},
		"resources": []any{map[string]any{
			"kind":   "CustomResourceDefinition",
			"name":   "scaledjobs.keda.sh",
			"status": "OutOfSync",
			"health": map[string]any{"status": "Missing"},
		}},
	})
	issues := buildIssues(root, nil, "argocd", nil)
	for _, iss := range issues {
		if iss.Scope == ScopeResource {
			for _, ref := range iss.Refs {
				if ref.Kind == "CustomResourceDefinition" && ref.Name == "scaledjobs.keda.sh" {
					t.Fatalf("expected the resource issue for the same CRD to be suppressed when the operation failure already names it; issues=%v", issues)
				}
			}
		}
	}
}

// TestBuildIssuesSuppressesResourceIssueForNamespacedKind pins the namespace
// projection used by the operation-message suppression. argoAffectedRefRE
// captures Kind+Name only — never a namespace — so the suppression key must
// be projected to the same shape on both sides. Earlier versions used refKey
// (which includes namespace) and silently failed to match any namespaced
// resource; only the CRD fixture in the test above passed.
func TestBuildIssuesSuppressesResourceIssueForNamespacedKind(t *testing.T) {
	root := argoApp(map[string]any{
		"operationState": map[string]any{
			"phase":   "Failed",
			"message": `error when patching "/dev/shm/foo": Deployment.apps "guestbook-ui" is invalid: spec.replicas: Invalid value`,
		},
		"resources": []any{map[string]any{
			"kind":      "Deployment",
			"name":      "guestbook-ui",
			"namespace": "demo-healthy",
			"status":    "OutOfSync",
			"health":    map[string]any{"status": "Missing"},
		}},
	})
	issues := buildIssues(root, nil, "argocd", nil)
	for _, iss := range issues {
		if iss.Scope == ScopeResource {
			for _, ref := range iss.Refs {
				if ref.Kind == "Deployment" && ref.Name == "guestbook-ui" && ref.Namespace == "demo-healthy" {
					t.Fatalf("expected namespaced Deployment issue to be suppressed by the operation failure that already names it; issues=%v", issues)
				}
			}
		}
	}
}

// TestBuildIssuesSuppressesEveryResourceInRemediatedNamespace pins the
// "missing-namespace remediation collapses downstream symptoms" path.
// When the parent op fails because namespace X doesn't exist, every Missing
// resource targeting X is just a derivative symptom of the same root cause —
// surfacing them turns one actionable issue into N+1 noisy rows.
func TestBuildIssuesSuppressesEveryResourceInRemediatedNamespace(t *testing.T) {
	root := argoApp(map[string]any{
		"operationState": map[string]any{
			"phase":   "Failed",
			"message": `failed to create resource: namespaces "demo-broken-sync" not found`,
		},
		"resources": []any{
			map[string]any{"kind": "Deployment", "name": "guestbook-ui", "namespace": "demo-broken-sync", "status": "OutOfSync", "health": map[string]any{"status": "Missing"}},
			map[string]any{"kind": "Service", "name": "guestbook-ui", "namespace": "demo-broken-sync", "status": "OutOfSync", "health": map[string]any{"status": "Missing"}},
			// Control: a resource in a *different* namespace must still surface
			// — the suppression is namespace-scoped, not blanket.
			map[string]any{"kind": "Deployment", "name": "bystander", "namespace": "other-ns", "status": "OutOfSync", "health": map[string]any{"status": "Missing"}},
		},
	})
	issues := buildIssues(root, nil, "argocd", nil)
	var brokenNsRefs []Ref
	var otherNsSeen bool
	for _, iss := range issues {
		if iss.Scope != ScopeResource {
			continue
		}
		for _, ref := range iss.Refs {
			switch ref.Namespace {
			case "demo-broken-sync":
				brokenNsRefs = append(brokenNsRefs, ref)
			case "other-ns":
				otherNsSeen = true
			}
		}
	}
	if len(brokenNsRefs) > 0 {
		t.Errorf("expected all resource issues in the remediated namespace to be suppressed; got: %v", brokenNsRefs)
	}
	if !otherNsSeen {
		t.Errorf("expected the bystander Deployment in other-ns to still surface (suppression is per-namespace, not blanket)")
	}
}

// stuckLoopApp builds an Argo Application in the stuck-drift state used
// across detector tests. Defaults match the user's actual cluster state
// (sync=OutOfSync, last operation Succeeded, recent reconcile, auto-sync
// with prune+selfHeal).
func stuckLoopApp(t *testing.T, opts ...func(*unstructured.Unstructured)) *unstructured.Unstructured {
	t.Helper()
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"namespace": "argocd", "name": "x"},
		"spec": map[string]any{
			"syncPolicy": map[string]any{
				"automated": map[string]any{"prune": true, "selfHeal": true},
			},
		},
		"status": map[string]any{
			"sync":   map[string]any{"status": "OutOfSync"},
			"health": map[string]any{"status": "Progressing"},
			"operationState": map[string]any{
				"phase":   "Succeeded",
				"message": "successfully synced (all tasks run)",
			},
			"reconciledAt": time.Now().UTC().Format(time.RFC3339),
		},
	}}
	for _, opt := range opts {
		opt(app)
	}
	return app
}

func TestDetectStuckDriftLoop_FiresOnTextbookCase(t *testing.T) {
	got := detectStuckDriftLoop(stuckLoopApp(t))
	if got == nil {
		t.Fatal("expected stuck-loop issue, got nil")
	}
	if got.Reason != "StuckDriftLoop" || got.Severity != "critical" {
		t.Errorf("unexpected issue: %+v", got)
	}
	if !got.Stuck {
		t.Error("expected Stuck flag to be true")
	}
}

func TestDetectStuckDriftLoop_DoesNotFireForVariousReasons(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*unstructured.Unstructured)
	}{
		{
			name: "synced",
			mut: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(u.Object, "Synced", "status", "sync", "status")
			},
		},
		{
			name: "operation still running",
			mut: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(u.Object, "Running", "status", "operationState", "phase")
			},
		},
		{
			name: "operation failed (not stuck loop — it's a real failure)",
			mut: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(u.Object, "Failed", "status", "operationState", "phase")
			},
		},
		{
			name: "auto-sync disabled",
			mut: func(u *unstructured.Unstructured) {
				unstructured.RemoveNestedField(u.Object, "spec", "syncPolicy", "automated")
			},
		},
		{
			name: "stale reconcile (>30min)",
			mut: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(u.Object, time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339), "status", "reconciledAt")
			},
		},
		{
			name: "no reconcile timestamp at all",
			mut: func(u *unstructured.Unstructured) {
				unstructured.RemoveNestedField(u.Object, "status", "reconciledAt")
			},
		},
		{
			// Auto-sync configured but self-heal off: persistent post-sync drift
			// is expected (Argo won't re-correct it), so this is not a loop —
			// detectAutoDriftSelfHealOff owns it.
			name: "auto-sync on but self-heal off",
			mut: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(u.Object, false, "spec", "syncPolicy", "automated", "selfHeal")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectStuckDriftLoop(stuckLoopApp(t, tc.mut))
			if got != nil {
				t.Errorf("expected no issue, got %+v", got)
			}
		})
	}
}

func TestDetectAutoDriftSelfHealOff(t *testing.T) {
	cases := []struct {
		name     string
		mut      func(*unstructured.Unstructured)
		wantFire bool
	}{
		{
			name: "OutOfSync + auto-sync + self-heal off → fires",
			mut: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(u.Object, false, "spec", "syncPolicy", "automated", "selfHeal")
			},
			wantFire: true,
		},
		{
			// stuckLoopApp defaults to selfHeal: true.
			name:     "OutOfSync + auto-sync + self-heal on → no fire (StuckDriftLoop owns it)",
			mut:      func(u *unstructured.Unstructured) {},
			wantFire: false,
		},
		{
			name: "OutOfSync + manual → no fire (ManualDrift owns it)",
			mut: func(u *unstructured.Unstructured) {
				unstructured.RemoveNestedField(u.Object, "spec", "syncPolicy", "automated")
			},
			wantFire: false,
		},
		{
			name: "Synced + auto-sync + self-heal off → no fire",
			mut: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(u.Object, false, "spec", "syncPolicy", "automated", "selfHeal")
				_ = unstructured.SetNestedField(u.Object, "Synced", "status", "sync", "status")
			},
			wantFire: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectAutoDriftSelfHealOff(stuckLoopApp(t, tc.mut))
			if (got != nil) != tc.wantFire {
				t.Errorf("fire = %v, want %v; issue=%+v", got != nil, tc.wantFire, got)
			}
			if got != nil && got.Reason != "SelfHealDisabled" {
				t.Errorf("Reason = %q, want SelfHealDisabled", got.Reason)
			}
		})
	}
}

// The gap that dropping per-resource OutOfSync issues could otherwise open:
// auto-sync configured but self-heal off + an OutOfSync app. No per-resource
// issue is emitted for plain drift, so the app-level SelfHealDisabled warning
// must be the band signal that nothing will reconcile this.
func TestBuildIssuesArgoAutoSyncSelfHealOffSurfacesDrift(t *testing.T) {
	root := argoApp(map[string]any{
		"sync":           map[string]any{"status": "OutOfSync"},
		"operationState": map[string]any{"phase": "Succeeded"},
	})
	_ = unstructured.SetNestedMap(root.Object, map[string]any{"prune": true}, "spec", "syncPolicy", "automated")
	issues := buildIssues(root, nil, "argocd", nil)
	var found bool
	for _, iss := range issues {
		if iss.Reason == "SelfHealDisabled" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a SelfHealDisabled issue for auto-sync-without-self-heal drift; got %+v", issues)
	}
}

func TestDetectManualDriftWithoutAutoSync(t *testing.T) {
	cases := []struct {
		name     string
		mut      func(*unstructured.Unstructured)
		wantFire bool
	}{
		{
			name: "OutOfSync + manual sync → fires",
			mut: func(u *unstructured.Unstructured) {
				unstructured.RemoveNestedField(u.Object, "spec", "syncPolicy", "automated")
			},
			wantFire: true,
		},
		{
			name:     "OutOfSync + auto-sync → no fire (StuckDriftLoop owns this case)",
			mut:      func(u *unstructured.Unstructured) {},
			wantFire: false,
		},
		{
			name: "Synced + manual → no fire",
			mut: func(u *unstructured.Unstructured) {
				unstructured.RemoveNestedField(u.Object, "spec", "syncPolicy", "automated")
				_ = unstructured.SetNestedField(u.Object, "Synced", "status", "sync", "status")
			},
			wantFire: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectManualDriftWithoutAutoSync(stuckLoopApp(t, tc.mut))
			if (got != nil) != tc.wantFire {
				t.Errorf("fire = %v, want %v; issue=%+v", got != nil, tc.wantFire, got)
			}
			if got != nil && got.Reason != "ManualDrift" {
				t.Errorf("Reason = %q, want ManualDrift", got.Reason)
			}
		})
	}
}

func TestArgoApplicationConditions_MapsTypesToSeverity(t *testing.T) {
	root := argoApp(map[string]any{
		"conditions": []any{
			map[string]any{"type": "ComparisonError", "message": "rpc error: code = Unknown desc = revision not found"},
			map[string]any{"type": "OrphanedResourceWarning", "message": "ConfigMap foo has no owner"},
			map[string]any{"type": "SomeUnrelatedInfo", "message": "noise"},
			map[string]any{"type": "", "message": ""}, // skipped
		},
	})
	got := argoApplicationConditions(root)
	if len(got) != 3 {
		t.Fatalf("expected 3 conditions (one filtered), got %d: %+v", len(got), got)
	}
	bySev := map[string]Severity{}
	for _, iss := range got {
		bySev[iss.Reason] = iss.Severity
	}
	byMessage := map[string]string{}
	for _, iss := range got {
		byMessage[iss.Reason] = iss.Message
	}
	if bySev["ComparisonError"] != SeverityCritical {
		t.Errorf("ComparisonError severity = %q, want critical", bySev["ComparisonError"])
	}
	if byMessage["ComparisonError"] != "revision not found" {
		t.Errorf("ComparisonError message = %q, want cleaned revision error", byMessage["ComparisonError"])
	}
	if strings.Contains(byMessage["ComparisonError"], "rpc error") || strings.Contains(byMessage["ComparisonError"], "Unknown desc") {
		t.Errorf("ComparisonError message leaked gRPC envelope: %q", byMessage["ComparisonError"])
	}
	if bySev["OrphanedResourceWarning"] != SeverityWarning {
		t.Errorf("OrphanedResourceWarning severity = %q, want warning", bySev["OrphanedResourceWarning"])
	}
	if bySev["SomeUnrelatedInfo"] != "info" {
		t.Errorf("unrecognized condition should default to info; got %q", bySev["SomeUnrelatedInfo"])
	}
}

// Argo's initiatedBy.automated is a *bool* (true when the auto-sync
// controller fires), not a string. The previous code did
// gitops.StringValue(ib["automated"]) which always yielded "" — automated
// history rows showed an empty initiator. Verify the bool is now coerced
// to "automated".
func TestBuildHistoryArgo_AutomatedBoolBecomesInitiator(t *testing.T) {
	root := argoApp(map[string]any{
		"history": []any{
			map[string]any{
				"id":         int64(7),
				"revision":   "abcdef0",
				"deployedAt": "2026-05-03T12:00:00Z",
				"initiatedBy": map[string]any{
					"automated": true,
				},
			},
		},
	})
	hist := buildHistory(root, "argocd")
	// First entry should be the only history row (no operationState set).
	if len(hist) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(hist))
	}
	if hist[0].InitiatedBy != "automated" {
		t.Errorf("InitiatedBy = %q, want %q", hist[0].InitiatedBy, "automated")
	}
}

// A running operation has finishedAt="" and used to fall to the bottom of
// history due to the descending DeployedAt sort. Falling back to startedAt
// keeps it at the top where it belongs.
func TestBuildHistoryArgo_RunningOpStaysOnTop(t *testing.T) {
	root := argoApp(map[string]any{
		"operationState": map[string]any{
			"phase":     "Running",
			"message":   "syncing",
			"startedAt": "2026-05-03T13:00:00Z",
			// finishedAt intentionally absent
		},
		"history": []any{
			map[string]any{
				"id":         int64(1),
				"revision":   "old",
				"deployedAt": "2026-05-03T11:00:00Z",
			},
			map[string]any{
				"id":         int64(2),
				"revision":   "newer",
				"deployedAt": "2026-05-03T12:00:00Z",
			},
		},
	})
	hist := buildHistory(root, "argocd")
	if len(hist) < 1 || hist[0].Phase != "Running" {
		t.Fatalf("expected the running operation to sort to the top; got hist=%+v", hist)
	}
}

// TestDetectPendingDeletion_SeverityRamp pins the age-based severity
// thresholds: <5min=info, 5-30min=warning, >30min=alert. Without explicit
// boundaries, a refactor that "tidies up" the cutoffs would silently
// downgrade the user-visible severity for a stuck-deletion case.
func TestDetectPendingDeletion_SeverityRamp(t *testing.T) {
	withDeletion := func(ago time.Duration, finalizers []string) *unstructured.Unstructured {
		obj := argoApp(map[string]any{})
		md, _ := obj.Object["metadata"].(map[string]any)
		md["deletionTimestamp"] = time.Now().Add(-ago).UTC().Format(time.RFC3339)
		if len(finalizers) > 0 {
			anyFinalizers := make([]any, len(finalizers))
			for i, f := range finalizers {
				anyFinalizers[i] = f
			}
			md["finalizers"] = anyFinalizers
		}
		return obj
	}

	tests := []struct {
		name      string
		age       time.Duration
		want      Severity
		wantStuck bool
	}{
		{"just deleted is info", 30 * time.Second, SeverityInfo, false},
		{"under 5min is info", 4 * time.Minute, SeverityInfo, false},
		// Inclusive boundary at 5min — 4m59s stays info, 5m exactly escalates.
		{"4m59s stays info", 4*time.Minute + 59*time.Second, SeverityInfo, false},
		{"5min escalates to warning", 5 * time.Minute, SeverityWarning, false},
		{"29min is warning", 29 * time.Minute, SeverityWarning, false},
		{"29m59s stays warning", 29*time.Minute + 59*time.Second, SeverityWarning, false},
		{"30min escalates to alert", 30 * time.Minute, SeverityAlert, true},
		{"21d is alert and stuck", 21 * 24 * time.Hour, SeverityAlert, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectPendingDeletion(withDeletion(tt.age, []string{"finalizers.fluxcd.io"}), nil)
			if got == nil {
				t.Fatalf("expected an issue, got nil")
			}
			if got.Severity != tt.want {
				t.Fatalf("severity = %q, want %q", got.Severity, tt.want)
			}
			if got.Stuck != tt.wantStuck {
				t.Fatalf("stuck = %v, want %v", got.Stuck, tt.wantStuck)
			}
			if got.Scope != ScopeLifecycle {
				t.Fatalf("scope = %q, want %q", got.Scope, ScopeLifecycle)
			}
			if got.Reason != "Terminating" {
				t.Fatalf("reason = %q, want Terminating", got.Reason)
			}
			// Finalizer is mentioned in the message — the most actionable
			// piece for the operator. Without this they have to drill
			// into YAML to find what's blocking cleanup.
			if !strings.Contains(got.Message, "finalizers.fluxcd.io") {
				t.Fatalf("expected message to mention finalizer; got %q", got.Message)
			}
		})
	}
}

// TestDetectPendingDeletion_SkipsHealthyResource ensures we don't fire
// the issue on resources that aren't being deleted. Cheap regression
// guard against accidentally returning a non-nil zero-value Issue.
func TestDetectPendingDeletion_SkipsHealthyResource(t *testing.T) {
	if got := detectPendingDeletion(argoApp(map[string]any{}), nil); got != nil {
		t.Fatalf("expected nil for healthy resource, got %+v", got)
	}
}

// TestBuildIssues_TerminatingFiresFirst pins the ordering: when a
// resource is Terminating AND has degraded children, the lifecycle
// issue surfaces first. This is the user-experience contract — knowing
// the resource is being deleted dominates everything else.
func TestBuildIssues_TerminatingFiresFirst(t *testing.T) {
	root := argoApp(map[string]any{
		"sync":   map[string]any{"status": "OutOfSync"},
		"health": map[string]any{"status": "Degraded"},
	})
	md, _ := root.Object["metadata"].(map[string]any)
	md["deletionTimestamp"] = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	md["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}

	issues := buildIssues(root, &gitopstree.ResourceTree{Summary: gitopstree.Summary{Degraded: 2}}, "argocd", nil)
	if len(issues) == 0 {
		t.Fatalf("expected at least one issue")
	}
	// Lifecycle scope must be the first issue regardless of severity sort
	// — we append it before the others, and severity-stable-sort preserves
	// position when severity ties.
	if issues[0].Scope != "lifecycle" {
		t.Fatalf("expected lifecycle issue first; got scope=%q", issues[0].Scope)
	}
}

// fakeResolver lets us assert the detector wires the finalizer key + root
// through to FinalizerOwnerStatus and surfaces the returned text in
// Issue.Cause.
type fakeResolver struct {
	statuses map[string]string            // finalizer → status string
	calls    []string                     // finalizers passed (in order)
	problems map[string][]ResourceProblem // resource name → workload problems
}

func (f *fakeResolver) GetLive(string, string, string, string) *unstructured.Unstructured {
	return nil
}
func (f *fakeResolver) RecentEvents(string, string, string, string) []EventSummary {
	return nil
}
func (f *fakeResolver) ResourceProblems(_, _, _, name string) []ResourceProblem {
	return f.problems[name]
}
func (f *fakeResolver) FinalizerOwnerStatus(finalizer string, _ *unstructured.Unstructured) string {
	f.calls = append(f.calls, finalizer)
	return f.statuses[finalizer]
}

// TestBuildIssues_EnrichesDegradedResourceWithWorkloadCause pins the L4 bridge:
// a managed resource Argo reports as Degraded gets the concrete workload "why"
// (from the issues engine, via the resolver) attached as Issue.Cause — instead
// of only "open the resource drawer". A nil resolver keeps the generic guidance.
func TestBuildIssues_EnrichesDegradedResourceWithWorkloadCause(t *testing.T) {
	root := argoApp(map[string]any{
		"resources": []any{
			map[string]any{
				"group": "apps", "kind": "Deployment", "namespace": "argocd", "name": "billing",
				"health": map[string]any{"status": "Degraded"},
				"status": "Synced",
			},
		},
	})
	resourceIssue := func(out []Issue) *Issue {
		for i := range out {
			if out[i].Scope == ScopeResource {
				return &out[i]
			}
		}
		return nil
	}

	// With a resolver that classifies the Deployment, the cause is attached.
	r := &fakeResolver{problems: map[string][]ResourceProblem{
		"billing": {{Reason: "CrashLoopBackOff", Message: "1/1 pods crashlooping (last exit OOMKilled)", Category: "crashloop", Severity: "critical"}},
	}}
	got := resourceIssue(buildIssues(root, nil, "argocd", r))
	if got == nil {
		t.Fatalf("expected a resource-scope issue for the Degraded Deployment")
	}
	if got.Cause != "1/1 pods crashlooping (last exit OOMKilled)" {
		t.Errorf("Cause = %q, want the workload reason from the issues engine", got.Cause)
	}

	// With a nil resolver, the issue still emits but carries no fabricated cause.
	plain := resourceIssue(buildIssues(root, nil, "argocd", nil))
	if plain == nil || plain.Cause != "" {
		t.Errorf("nil resolver should yield a resource issue with empty Cause, got %+v", plain)
	}
}

func TestResourceProblemCause(t *testing.T) {
	if got := resourceProblemCause(nil); got != "" {
		t.Errorf("no problems → %q, want empty", got)
	}
	// Critical wins over a warning regardless of order.
	got := resourceProblemCause([]ResourceProblem{
		{Reason: "OutOfSync", Message: "drifted", Severity: "warning"},
		{Reason: "OOMKilled", Message: "out of memory", Severity: "critical"},
	})
	if got != "out of memory" {
		t.Errorf("critical should win, got %q", got)
	}
	// Falls back to Reason when Message is empty.
	if got := resourceProblemCause([]ResourceProblem{{Reason: "ImagePullBackOff", Severity: "critical"}}); got != "ImagePullBackOff" {
		t.Errorf("empty message → reason, got %q", got)
	}
}

// TestDetectPendingDeletion_EnrichesWithControllerHealth pins the contract
// that the lifecycle detector consults the resolver for finalizer-owner
// status and embeds the result in Issue.Cause. Without this contract,
// stuck deletions remain "investigate the controller" instead of
// "argocd-application-controller is CrashLoopBackOff — start there".
func TestDetectPendingDeletion_EnrichesWithControllerHealth(t *testing.T) {
	t.Run("warning tier surfaces controller status", func(t *testing.T) {
		obj := argoApp(map[string]any{})
		md, _ := obj.Object["metadata"].(map[string]any)
		md["deletionTimestamp"] = time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
		md["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}
		r := &fakeResolver{statuses: map[string]string{
			"resources-finalizer.argocd.argoproj.io": "argocd-application-controller is CrashLoopBackOff (2/2 pods)",
		}}
		got := detectPendingDeletion(obj, r)
		if got == nil {
			t.Fatal("expected issue, got nil")
		}
		if got.Cause != "argocd-application-controller is CrashLoopBackOff (2/2 pods)" {
			t.Fatalf("expected controller status in Cause, got %q", got.Cause)
		}
	})

	t.Run("info tier (recent deletion) skips controller probe", func(t *testing.T) {
		// Recent deletions are presumably fine — adding a controller-health
		// line on a healthy controller would overstate urgency. The detector
		// must skip the resolver entirely below the warning threshold.
		obj := argoApp(map[string]any{})
		md, _ := obj.Object["metadata"].(map[string]any)
		md["deletionTimestamp"] = time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
		md["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}
		r := &fakeResolver{statuses: map[string]string{
			"resources-finalizer.argocd.argoproj.io": "should never be returned",
		}}
		got := detectPendingDeletion(obj, r)
		if got == nil {
			t.Fatal("expected issue, got nil")
		}
		if len(r.calls) != 0 {
			t.Fatalf("expected no resolver calls at info tier, got %v", r.calls)
		}
		if got.Cause != "" {
			t.Fatalf("expected empty Cause at info tier, got %q", got.Cause)
		}
	})

	t.Run("first informative finalizer wins", func(t *testing.T) {
		// Resources can carry multiple finalizers (Argo + foreground-cascade
		// is common); only the first one we can identify should drive the
		// controller-health enrichment. Concatenating all of them would
		// produce noisy Cause text.
		obj := argoApp(map[string]any{})
		md, _ := obj.Object["metadata"].(map[string]any)
		md["deletionTimestamp"] = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
		md["finalizers"] = []any{"unknown.example.com/cleanup", "resources-finalizer.argocd.argoproj.io"}
		r := &fakeResolver{statuses: map[string]string{
			"resources-finalizer.argocd.argoproj.io": "argocd-application-controller is healthy (1 pod ready)",
		}}
		got := detectPendingDeletion(obj, r)
		if got == nil {
			t.Fatal("expected issue, got nil")
		}
		if got.Cause != "argocd-application-controller is healthy (1 pod ready)" {
			t.Fatalf("expected the recognized finalizer to win; got %q", got.Cause)
		}
	})

	t.Run("nil resolver leaves Cause empty", func(t *testing.T) {
		// Production handler may pass a nil resolver in some paths
		// (preview, tests) — must not panic.
		obj := argoApp(map[string]any{})
		md, _ := obj.Object["metadata"].(map[string]any)
		md["deletionTimestamp"] = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
		md["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}
		got := detectPendingDeletion(obj, nil)
		if got == nil {
			t.Fatal("expected issue, got nil")
		}
		if got.Cause != "" {
			t.Fatalf("expected empty Cause with nil resolver, got %q", got.Cause)
		}
	})
}

// TestDedupeIssues_CollapsesIdenticalConditions pins the dedup contract
// for the Flux case where Released=False *and* Reconciling=False both
// carry the same UpgradeFailed reason+message — without dedup, the
// Issues panel renders two identical "Helm upgrade failed" rows.
func TestDedupeIssues_CollapsesIdenticalConditions(t *testing.T) {
	in := []Issue{
		{Scope: "condition", Severity: "critical", Reason: "UpgradeFailed", Message: "Helm upgrade failed for release demo/auth"},
		{Scope: "condition", Severity: "critical", Reason: "UpgradeFailed", Message: "Helm upgrade failed for release demo/auth"},
		{Scope: "condition", Severity: "critical", Reason: "Stalled", Message: "Different message"},
	}
	got := dedupeIssues(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 issues after dedup, got %d: %+v", len(got), got)
	}
	if got[0].Reason != "UpgradeFailed" || got[1].Reason != "Stalled" {
		t.Fatalf("expected first occurrence preserved; got %+v", got)
	}
}

// TestDedupeIssues_PerResourceIssuesAreDistinct ensures resource-scoped
// issues with different Refs but the same reason+message stay separate.
// Two pods both ImagePullBackOff with the same message are different
// problems — collapsing them would hide pod #2.
func TestDedupeIssues_PerResourceIssuesAreDistinct(t *testing.T) {
	in := []Issue{
		{Scope: "resource", Severity: "critical", Reason: "Degraded", Message: "Pod is Degraded", Refs: []Ref{{Kind: "Pod", Name: "p1"}}},
		{Scope: "resource", Severity: "critical", Reason: "Degraded", Message: "Pod is Degraded", Refs: []Ref{{Kind: "Pod", Name: "p2"}}},
	}
	got := dedupeIssues(in)
	if len(got) != 2 {
		t.Fatalf("expected both per-resource issues kept, got %d: %+v", len(got), got)
	}
}

// TestResolveFinalizerOwner pins the static finalizer→controller mapping.
// New Flux/Argo versions may introduce new keys; this test catches
// regressions when entries get accidentally renamed.
func TestResolveFinalizerOwner(t *testing.T) {
	mkObj := func(api string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": api,
			"kind":       "Whatever",
		}}
	}
	tests := []struct {
		name      string
		finalizer string
		obj       *unstructured.Unstructured
		want      string // expected Controller name; "" means nil owner
	}{
		{"argo standard", "resources-finalizer.argocd.argoproj.io", mkObj(""), "argocd-application-controller"},
		{"argo legacy cascade", "foreground-cascade.argocd.argoproj.io", mkObj(""), "argocd-application-controller"},
		{"flux generic on Kustomization", "finalizers.fluxcd.io", mkObj("kustomize.toolkit.fluxcd.io/v1"), "kustomize-controller"},
		{"flux generic on HelmRelease", "finalizers.fluxcd.io", mkObj("helm.toolkit.fluxcd.io/v2"), "helm-controller"},
		{"flux generic on GitRepository", "finalizers.fluxcd.io", mkObj("source.toolkit.fluxcd.io/v1"), "source-controller"},
		{"flux specific kustomize", "finalizers.kustomize.toolkit.fluxcd.io", mkObj(""), "kustomize-controller"},
		{"flux specific helm", "finalizers.helm.toolkit.fluxcd.io", mkObj(""), "helm-controller"},
		{"flux specific source", "finalizers.source.toolkit.fluxcd.io", mkObj(""), "source-controller"},
		{"unknown finalizer returns nil", "example.com/cleanup", mkObj(""), ""},
		{"unknown apiVersion for generic flux returns nil", "finalizers.fluxcd.io", mkObj("custom.example.com/v1"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveFinalizerOwner(tt.finalizer, tt.obj)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("expected nil owner, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected owner with controller %q, got nil", tt.want)
			}
			if got.Controller != tt.want {
				t.Fatalf("Controller = %q, want %q", got.Controller, tt.want)
			}
		})
	}
}

// TestBuildIssues_LifecycleVsCriticalOperation pins what happens when a
// Terminating resource also has a critical operation failure. The
// backend severity-stable sort puts critical (rank 0) above alert (rank
// 1), so the lifecycle Issue is *not* at index 0 in the array — the
// frontend's GitOpsIssuesBand extracts scope=lifecycle to render as a
// banner, which is what makes the user-facing "lifecycle dominates"
// promise true. This test pins the *current backend ordering* so a
// future refactor that hoists lifecycle pre-sort doesn't silently break
// the frontend banner extraction (or vice versa). If the contract
// changes, both this test and the GitOpsIssuesBand renderer must change
// together.
func TestBuildIssues_LifecycleVsCriticalOperation(t *testing.T) {
	root := argoApp(map[string]any{
		"operationState": map[string]any{
			"phase":   "Failed",
			"message": "context deadline exceeded",
		},
	})
	md, _ := root.Object["metadata"].(map[string]any)
	md["deletionTimestamp"] = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	md["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}

	issues := buildIssues(root, nil, "argocd", nil)
	if len(issues) < 2 {
		t.Fatalf("expected both lifecycle + operation issues, got %d", len(issues))
	}
	// Critical operation failure (severity rank 0) wins the array sort
	// over alert-tier lifecycle (rank 1). The user-facing banner pattern
	// is enforced by the frontend extracting scope=lifecycle separately.
	if issues[0].Severity != "critical" || issues[0].Scope != "operation" {
		t.Fatalf("expected critical operation issue at index 0, got severity=%q scope=%q", issues[0].Severity, issues[0].Scope)
	}
	// Lifecycle still appears in the slice — it just sorts after the
	// critical operation. Verify it survived the sort and dedup.
	foundLifecycle := false
	for _, iss := range issues {
		if iss.Scope == "lifecycle" && iss.Reason == "Terminating" {
			foundLifecycle = true
			break
		}
	}
	if !foundLifecycle {
		t.Fatal("expected lifecycle Terminating issue in the slice")
	}
}

// TestBuildIssues_LifecycleBeatsRunningOperation: an alert-tier
// lifecycle issue (1h pending deletion) must sort above an info-tier
// running operation. This is the inverse of the critical case above
// and confirms the severity-rank ordering works in both directions.
func TestBuildIssues_LifecycleBeatsRunningOperation(t *testing.T) {
	root := argoApp(map[string]any{
		"operationState": map[string]any{"phase": "Running"},
	})
	md, _ := root.Object["metadata"].(map[string]any)
	md["deletionTimestamp"] = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	md["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}

	issues := buildIssues(root, nil, "argocd", nil)
	if len(issues) < 2 {
		t.Fatalf("expected both issues, got %d", len(issues))
	}
	if issues[0].Scope != "lifecycle" || issues[0].Severity != "alert" {
		t.Fatalf("expected alert-tier lifecycle at index 0; got scope=%q severity=%q", issues[0].Scope, issues[0].Severity)
	}
}

// TestBuildSummary_TerminatingFields pins the Summary fields that drive
// the UI's [Terminating] chip. Renaming or omitting these silently
// regresses the chip to "Healthy"-looking which is the entire bug
// this work fixes.
func TestBuildSummary_TerminatingFields(t *testing.T) {
	root := argoApp(map[string]any{})
	md, _ := root.Object["metadata"].(map[string]any)
	stamp := "2026-04-13T13:14:42Z"
	md["deletionTimestamp"] = stamp
	md["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}

	s := buildSummary(root, "argocd")
	if !s.Terminating {
		t.Fatal("expected Terminating=true")
	}
	if s.TerminationStartedAt != stamp {
		t.Fatalf("TerminationStartedAt = %q, want %q", s.TerminationStartedAt, stamp)
	}
	if len(s.Finalizers) != 1 || s.Finalizers[0] != "resources-finalizer.argocd.argoproj.io" {
		t.Fatalf("Finalizers = %v, want [resources-finalizer.argocd.argoproj.io]", s.Finalizers)
	}
}

// TestBuildSummary_IgnoredDifferences pins the Argo comparison-coverage
// disclosure: RuleCount counts every spec.ignoreDifferences entry,
// UnsupportedRuleCount counts those using jqPathExpressions OR
// managedFieldsManagers (Radar's drift filter applies neither — the drift
// panel may surface fields Argo's UI suppresses), and Kinds is the sorted
// unique Group/Kind targets with a group-wildcard rule (kind omitted)
// rendered as "group/*".
func TestBuildSummary_IgnoredDifferences(t *testing.T) {
	root := argoApp(map[string]any{})
	root.Object["spec"] = map[string]any{
		"ignoreDifferences": []any{
			// jsonPointers rule, namespaced group + kind → "apps/Deployment".
			map[string]any{
				"group":        "apps",
				"kind":         "Deployment",
				"jsonPointers": []any{"/spec/replicas"},
			},
			// jqPathExpressions rule, core resource (empty group) → "ConfigMap".
			map[string]any{
				"kind":              "ConfigMap",
				"jqPathExpressions": []any{".data.checksum"},
			},
			// managedFieldsManagers rule — the other exclusion shape Radar's
			// drift filter doesn't apply.
			map[string]any{
				"group":                 "apps",
				"kind":                  "Deployment",
				"managedFieldsManagers": []any{"kube-controller-manager"},
			},
			// Group-wildcard rule: group set, kind omitted → "networking.k8s.io/*".
			map[string]any{
				"group":        "networking.k8s.io",
				"jsonPointers": []any{"/spec/rules"},
			},
		},
	}

	s := buildSummary(root, "argocd")
	if s.IgnoredDifferences == nil {
		t.Fatal("expected IgnoredDifferences to be populated")
	}
	if s.IgnoredDifferences.RuleCount != 4 {
		t.Errorf("RuleCount = %d, want 4", s.IgnoredDifferences.RuleCount)
	}
	if s.IgnoredDifferences.UnsupportedRuleCount != 2 {
		t.Errorf("UnsupportedRuleCount = %d, want 2", s.IgnoredDifferences.UnsupportedRuleCount)
	}
	want := []string{"ConfigMap", "apps/Deployment", "networking.k8s.io/*"}
	if !reflect.DeepEqual(s.IgnoredDifferences.Kinds, want) {
		t.Errorf("Kinds = %v, want %v", s.IgnoredDifferences.Kinds, want)
	}
}

// TestBuildSummary_IgnoredDifferences_NilWhenAbsent confirms the field stays
// nil (and json-omitted) when the Application declares no exclusions.
func TestBuildSummary_IgnoredDifferences_NilWhenAbsent(t *testing.T) {
	s := buildSummary(argoApp(map[string]any{}), "argocd")
	if s.IgnoredDifferences != nil {
		t.Errorf("expected nil IgnoredDifferences when spec.ignoreDifferences absent, got %+v", s.IgnoredDifferences)
	}
}

// TestBuildSummary_IgnoredDifferences_NilForFlux pins that the disclosure is
// Argo-only: it describes Argo's comparison pipeline, so a Flux root leaves the
// field nil even if it somehow carried spec.ignoreDifferences.
func TestBuildSummary_IgnoredDifferences_NilForFlux(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]any{"namespace": "flux-system", "name": "apps"},
		"spec": map[string]any{
			"ignoreDifferences": []any{
				map[string]any{"kind": "ConfigMap", "jsonPointers": []any{"/data"}},
			},
		},
		"status": map[string]any{},
	}}
	s := buildSummary(root, "fluxcd")
	if s.IgnoredDifferences != nil {
		t.Errorf("expected nil IgnoredDifferences for Flux root, got %+v", s.IgnoredDifferences)
	}
}

// TestCategorizeArgoChange pins the closed mapping from Argo's per-resource
// sync + health vocabularies onto the typed Category constants. A mapping
// gap would silently drop a row into changeRank's default bucket and
// misorder the Changes view (Progressing sorting alongside Synced).
func TestCategorizeArgoChange(t *testing.T) {
	cases := []struct {
		name   string
		sync   string
		health string
		want   Category
	}{
		{"healthy + Synced → Synced", "Synced", "Healthy", CategorySynced},
		{"OutOfSync overrides Healthy", "OutOfSync", "Healthy", CategoryOutOfSync},
		{"Degraded health wins over Synced", "Synced", "Degraded", CategoryDegraded},
		{"Missing health wins over OutOfSync", "OutOfSync", "Missing", CategoryMissing},
		{"Progressing health surfaced", "Synced", "Progressing", CategoryProgressing},
		{"Suspended health surfaced", "Synced", "Suspended", CategorySuspended},
		{"Pruned sync without health", "Pruned", "", CategoryPruned},
		{"empty sync + empty health → Unknown", "", "", CategoryUnknown},
		{"explicit Unknown sync", "Unknown", "Healthy", CategoryUnknown},
		{"unrecognized values → Unknown (and log)", "Lol", "Wat", CategoryUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := categorizeArgoChange(tc.sync, tc.health); got != tc.want {
				t.Errorf("categorizeArgoChange(%q, %q) = %q, want %q", tc.sync, tc.health, got, tc.want)
			}
		})
	}
}

// TestCategorizeFluxChange covers the Flux variant which reads from tree
// nodes rather than Argo status. Flux's inventory check means "no degraded
// health, no OutOfSync" → Synced (a known limitation: per-field drift
// against the desired manifest isn't computed).
func TestCategorizeFluxChange(t *testing.T) {
	cases := []struct {
		name   string
		sync   string
		health string
		want   Category
	}{
		{"healthy → Synced", "Synced", "Healthy", CategorySynced},
		{"empty → Synced (inventory pass)", "", "", CategorySynced},
		{"Degraded health surfaces", "Synced", "Degraded", CategoryDegraded},
		{"Missing health surfaces", "Synced", "Missing", CategoryMissing},
		{"OutOfSync sync surfaces", "OutOfSync", "Healthy", CategoryOutOfSync},
		{"Progressing health surfaces", "Synced", "Progressing", CategoryProgressing},
		{"Suspended health surfaces", "Synced", "Suspended", CategorySuspended},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := categorizeFluxChange(tc.sync, tc.health); got != tc.want {
				t.Errorf("categorizeFluxChange(%q, %q) = %q, want %q", tc.sync, tc.health, got, tc.want)
			}
		})
	}
}

// TestChangeRank pins the rank of every Category constant. Adding a new
// Category to vocab.go without giving it an explicit rank here lands it
// in the default bucket — making this test fail is the canary.
func TestChangeRank(t *testing.T) {
	cases := []struct {
		category Category
		want     int
	}{
		{CategoryDegraded, 0},
		{CategoryMissing, 0},
		{CategoryOutOfSync, 1},
		{CategoryProgressing, 2},
		{CategoryReconciling, 2},
		{CategoryUnknown, 3},
		{CategorySynced, 4},
		{CategoryPruned, 4},
		{CategoryHook, 4},
		{CategorySuspended, 4},
	}
	for _, tc := range cases {
		t.Run(string(tc.category), func(t *testing.T) {
			if got := changeRank(tc.category); got != tc.want {
				t.Errorf("changeRank(%q) = %d, want %d", tc.category, got, tc.want)
			}
		})
	}
	// The default branch lives below the named-constant ranks so unknown
	// values don't silently mix with valid ones in the sorted output.
	if got := changeRank(Category("Bogus")); got <= 4 {
		t.Errorf("changeRank(Bogus) = %d, want > 4 (default branch must rank below all named constants)", got)
	}
}

func TestDedupeIssues_SameNameDifferentNamespaceKept(t *testing.T) {
	// Two genuinely distinct Degraded resources sharing a kind+name across
	// namespaces (an ApplicationSet fanning out an identically-named workload)
	// must BOTH survive dedup — the namespace is part of the key.
	in := []Issue{
		{Scope: "resource", Severity: "critical", Reason: "Degraded", Message: "Deployment api is Degraded", Refs: []Ref{{Kind: "Deployment", Namespace: "team-a", Name: "api"}}},
		{Scope: "resource", Severity: "critical", Reason: "Degraded", Message: "Deployment api is Degraded", Refs: []Ref{{Kind: "Deployment", Namespace: "team-b", Name: "api"}}},
	}
	got := dedupeIssues(in)
	if len(got) != 2 {
		t.Fatalf("expected both namespaces' issues kept, got %d: %+v", len(got), got)
	}
}

func TestEnrichChangeHealthFromTree_BackfillsEmptyHealth(t *testing.T) {
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{
		{Ref: gitopstree.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "staging", Name: "radar-hub"}, Health: "Degraded"},
		{Ref: gitopstree.ResourceRef{Kind: "Service", Namespace: "staging", Name: "radar-hub"}, Health: "Healthy"},
		{Ref: gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "staging", Name: "vars"}, Health: ""},
	}}
	changes := []Change{
		{Ref: Ref{Group: "apps", Kind: "Deployment", Namespace: "staging", Name: "radar-hub"}, Health: ""},        // backfilled → Degraded
		{Ref: Ref{Kind: "Service", Namespace: "staging", Name: "radar-hub"}, Health: "Progressing"},              // already set → unchanged
		{Ref: Ref{Kind: "ConfigMap", Namespace: "staging", Name: "vars"}, Health: ""},                            // tree node empty → stays empty
		{Ref: Ref{Kind: "SealedSecret", Namespace: "staging", Name: "x"}, Health: ""},                            // not in tree → stays empty
	}
	enrichChangeHealthFromTree(changes, tree)
	if changes[0].Health != "Degraded" {
		t.Errorf("Deployment health = %q, want Degraded (backfilled from tree)", changes[0].Health)
	}
	if changes[1].Health != "Progressing" {
		t.Errorf("Service health = %q, want Progressing (already set, unchanged)", changes[1].Health)
	}
	if changes[2].Health != "" || changes[3].Health != "" {
		t.Errorf("resources with no tree health must stay empty: %q %q", changes[2].Health, changes[3].Health)
	}
}
