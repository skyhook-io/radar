package insights

import (
	"strings"
	"testing"
	"time"

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
	issues := buildIssues(root, nil, "argocd", nil)
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

func TestParseArgoOperationError(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		wantCause string // substring match — full text is brittle to copy edits
		wantKind  string
		wantName  string
		wantRetry int
		wantStuck bool
	}{
		{
			name:      "annotation too long with affected CRD and retry suffix",
			msg:       `one or more objects failed to apply, reason: error when patching "/dev/shm/foo": CustomResourceDefinition.apiextensions.k8s.io "scaledjobs.keda.sh" is invalid: metadata.annotations: Too long: may not be more than 262144 bytes (retried 5 times)`,
			wantCause: "256 KB metadata limit",
			wantKind:  "CustomResourceDefinition",
			wantName:  "scaledjobs.keda.sh",
			wantRetry: 5,
			wantStuck: true,
		},
		{
			name:      "admission webhook rejection",
			msg:       `admission webhook "validation.gatekeeper.sh" denied the request: missing required label "owner"`,
			wantCause: "admission webhook rejected",
			wantRetry: 0,
			wantStuck: false,
		},
		{
			name:      "rbac forbidden with resource extracted",
			msg:       `Deployment.apps "billing" is forbidden: User "system:serviceaccount:argocd:argocd-controller" cannot patch resource`,
			wantCause: "RBAC denied",
			wantKind:  "Deployment",
			wantName:  "billing",
		},
		{
			name:      "unrecognized message → no cause but raw still preserved by caller",
			msg:       "something completely novel went wrong",
			wantCause: "",
		},
		{
			name:      "single retry → not stuck",
			msg:       `whatever (retried 1 times)`,
			wantRetry: 1,
			wantStuck: false,
		},
		{
			name: "empty input → all zero values",
			msg:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseArgoOperationError(tc.msg)
			if tc.wantCause != "" && !strings.Contains(got.Cause, tc.wantCause) {
				t.Errorf("Cause = %q, want substring %q", got.Cause, tc.wantCause)
			}
			if tc.wantCause == "" && got.Cause != "" {
				t.Errorf("Cause = %q, want empty (unrecognized pattern)", got.Cause)
			}
			if got.AffectedKind != tc.wantKind {
				t.Errorf("AffectedKind = %q, want %q", got.AffectedKind, tc.wantKind)
			}
			if got.AffectedName != tc.wantName {
				t.Errorf("AffectedName = %q, want %q", got.AffectedName, tc.wantName)
			}
			if got.RetryCount != tc.wantRetry {
				t.Errorf("RetryCount = %d, want %d", got.RetryCount, tc.wantRetry)
			}
			if got.Stuck != tc.wantStuck {
				t.Errorf("Stuck = %v, want %v", got.Stuck, tc.wantStuck)
			}
		})
	}
}

func TestBuildIssuesSuppressesResourceIssueDuplicatedByOperationFailure(t *testing.T) {
	// When the operation message names CRD scaledjobs.keda.sh AND the
	// resources[] list also flags the same CRD as OutOfSync, we want only
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
		}},
	})
	issues := buildIssues(root, nil, "argocd", nil)
	for _, iss := range issues {
		if iss.Scope == "resource" && iss.Reason == "OutOfSync" {
			for _, ref := range iss.Refs {
				if ref.Kind == "CustomResourceDefinition" && ref.Name == "scaledjobs.keda.sh" {
					t.Fatalf("expected the resource OutOfSync issue for the same CRD to be suppressed when the operation failure already names it; issues=%v", issues)
				}
			}
		}
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

func TestParseArgoOperationError_HookFailures(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		wantCause string
	}{
		{
			name:      "PreSync hook failed",
			msg:       `PreSync phase failed: hook "db-migration" exited with status 1`,
			wantCause: "sync hook failed",
		},
		{
			name:      "generic hook failed wording",
			msg:       `hook "drain-cache" failed: timed out after 5m`,
			wantCause: "sync hook failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseArgoOperationError(tc.msg)
			if !strings.Contains(strings.ToLower(got.Cause), tc.wantCause) {
				t.Errorf("Cause = %q, want substring %q", got.Cause, tc.wantCause)
			}
		})
	}
}

func TestArgoApplicationConditions_MapsTypesToSeverity(t *testing.T) {
	root := argoApp(map[string]any{
		"conditions": []any{
			map[string]any{"type": "ComparisonError", "message": "rpc error: revision not found"},
			map[string]any{"type": "OrphanedResourceWarning", "message": "ConfigMap foo has no owner"},
			map[string]any{"type": "SomeUnrelatedInfo", "message": "noise"},
			map[string]any{"type": "", "message": ""}, // skipped
		},
	})
	got := argoApplicationConditions(root)
	if len(got) != 3 {
		t.Fatalf("expected 3 conditions (one filtered), got %d: %+v", len(got), got)
	}
	bySev := map[string]string{}
	for _, iss := range got {
		bySev[iss.Reason] = iss.Severity
	}
	if bySev["ComparisonError"] != "critical" {
		t.Errorf("ComparisonError severity = %q, want critical", bySev["ComparisonError"])
	}
	if bySev["OrphanedResourceWarning"] != "warning" {
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
		want      string
		wantStuck bool
	}{
		{"just deleted is info", 30 * time.Second, "info", false},
		{"under 5min is info", 4 * time.Minute, "info", false},
		// Inclusive boundary: at 5min exactly we escalate to warning.
		// Test uses 4m59s to keep the "still info" case observable
		// without relying on sub-second clock precision.
		{"4m59s stays info", 4*time.Minute + 59*time.Second, "info", false},
		{"5min escalates to warning", 5 * time.Minute, "warning", false},
		{"29min is warning", 29 * time.Minute, "warning", false},
		// Same inclusive semantics for the 30min cutoff.
		{"29m59s stays warning", 29*time.Minute + 59*time.Second, "warning", false},
		{"30min escalates to alert", 30 * time.Minute, "alert", true},
		{"21d is alert and stuck", 21 * 24 * time.Hour, "alert", true},
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
			if got.Scope != "lifecycle" {
				t.Fatalf("scope = %q, want %q", got.Scope, "lifecycle")
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
	statuses map[string]string // finalizer → status string
	calls    []string          // finalizers passed (in order)
}

func (f *fakeResolver) GetLive(string, string, string, string) *unstructured.Unstructured {
	return nil
}
func (f *fakeResolver) RecentEvents(string, string, string, string) []EventSummary {
	return nil
}
func (f *fakeResolver) FinalizerOwnerStatus(finalizer string, _ *unstructured.Unstructured) string {
	f.calls = append(f.calls, finalizer)
	return f.statuses[finalizer]
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
