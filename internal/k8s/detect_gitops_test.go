package k8s

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/pkg/gitops/diagnose"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
)

func argoApp(name, ns, health, sync, phase string, automated bool, conds []any) *unstructured.Unstructured {
	status := map[string]any{}
	if health != "" {
		status["health"] = map[string]any{"status": health}
	}
	if sync != "" {
		status["sync"] = map[string]any{"status": sync}
	}
	if phase != "" {
		status["operationState"] = map[string]any{"phase": phase}
	}
	if conds != nil {
		status["conditions"] = conds
	}
	spec := map[string]any{}
	if automated {
		spec["syncPolicy"] = map[string]any{"automated": map[string]any{}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
		"status":     status,
	}}
}

func fluxKust(name, ns string, suspend bool, generation, observed int64, readyStatus, reason string) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": ns}
	if generation > 0 {
		meta["generation"] = generation
	}
	status := map[string]any{
		"conditions": []any{
			map[string]any{"type": "Ready", "status": readyStatus, "reason": reason, "message": reason + " detail"},
		},
	}
	if observed > 0 {
		status["observedGeneration"] = observed
	}
	spec := map[string]any{}
	if suspend {
		spec["suspend"] = true
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   meta,
		"spec":       spec,
		"status":     status,
	}}
}

func TestDetectGitOpsProblems(t *testing.T) {
	defer ResetTestDynamicState()

	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	kustGVR := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}

	comparisonErr := []any{
		map[string]any{"type": "ComparisonError", "message": "app path does not exist"},
	}

	objs := []runtime.Object{
		// Argo — should flag.
		argoApp("degraded", "argocd", "Degraded", "Synced", "", true, nil),                      // critical HealthDegraded
		argoApp("missing-auto", "argocd", "Missing", "OutOfSync", "", true, nil),                // high HealthMissing
		argoApp("drift-auto", "argocd", "Healthy", "OutOfSync", "", true, nil),                  // high OutOfSync
		argoApp("comparison", "argocd", "Healthy", "Unknown", "", false, comparisonErr),         // high ComparisonError (even manual)
		argoApp("degraded-and-error", "argocd", "Degraded", "Unknown", "", true, comparisonErr), // critical: Degraded outranks the error condition
		// Argo — should NOT flag.
		argoApp("missing-manual", "argocd", "Missing", "OutOfSync", "", false, nil), // manual app: expected un-synced
		argoApp("suspended", "argocd", "Suspended", "OutOfSync", "", true, nil),     // intentionally paused
		argoApp("progressing", "argocd", "Progressing", "OutOfSync", "", true, nil), // mid-sync
		argoApp("syncing", "argocd", "Degraded", "OutOfSync", "Running", true, nil), // operation in flight
		argoApp("healthy", "argocd", "Healthy", "Synced", "", true, nil),            // all good
		// Flux — should flag.
		fluxKust("recon-failed", "flux", false, 0, 0, "False", "ReconciliationFailed"),
		fluxKust("artifact-failed", "flux", false, 0, 0, "False", "ArtifactFailed"), // genuine stuck (narrow transient set)
		// Flux — should NOT flag.
		fluxKust("reconciling", "flux", false, 0, 0, "False", "Progressing"),
		fluxKust("suspended", "flux", true, 0, 0, "False", "ReconciliationFailed"),
		fluxKust("stale-gen", "flux", false, 5, 3, "False", "ReconciliationFailed"),
		fluxKust("ready", "flux", false, 0, 0, "True", "ReconciliationSucceeded"),
	}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{
			appGVR:  "ApplicationList",
			kustGVR: "KustomizationList",
		},
		objs...,
	)
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application", Name: "applications", Namespaced: true, Verbs: []string{"list", "watch"}},
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization", Name: "kustomizations", Namespaced: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := GetDynamicResourceCache()
	discovery := GetResourceDiscovery()
	for _, gvr := range []schema.GroupVersionResource{appGVR, kustGVR} {
		if err := dynCache.EnsureWatching(gvr); err != nil {
			t.Fatalf("EnsureWatching %s: %v", gvr, err)
		}
		if !dynCache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("dynamic cache for %s did not sync", gvr)
		}
	}

	problems := DetectGitOpsProblems(dynCache, discovery, "")

	bySubject := map[string]Detection{}
	for _, p := range problems {
		bySubject[p.Name] = p
	}

	wantFlag := map[string]struct {
		severity, reason string
	}{
		"degraded":           {"critical", "HealthDegraded"},
		"degraded-and-error": {"critical", "HealthDegraded"},
		"missing-auto":       {"critical", "HealthMissing"},   // auto-synced resources gone → critical
		"drift-auto":         {"high", "OutOfSync"},           // drift self-heals → stays warning
		"comparison":         {"critical", "ComparisonError"}, // sync failure → critical
		"recon-failed":       {"critical", "ReconciliationFailed"},
		"artifact-failed":    {"critical", "ArtifactFailed"},
	}
	for name, want := range wantFlag {
		p, ok := bySubject[name]
		if !ok {
			t.Errorf("expected %q to be flagged, but it was not. got=%+v", name, problems)
			continue
		}
		if p.Severity != want.severity || p.Reason != want.reason {
			t.Errorf("%q: got severity=%q reason=%q, want %q/%q", name, p.Severity, p.Reason, want.severity, want.reason)
		}
	}

	// The ComparisonError condition branch must attach its condition-specific
	// action (not leave it empty).
	if c, ok := bySubject["comparison"]; ok && c.Action == "" {
		t.Errorf("ComparisonError problem should carry an Action, got empty")
	}

	wantSkip := []string{"missing-manual", "suspended", "progressing", "syncing", "healthy", "reconciling", "stale-gen", "ready"}
	// Two Flux objects share the name "suspended"/"ready" semantics but live in
	// different namespaces from Argo ones with similar names; assert by checking
	// no flagged problem carries a skip-name that isn't also a flagged name.
	for _, name := range wantSkip {
		if _, ok := wantFlag[name]; ok {
			continue
		}
		if p, ok := bySubject[name]; ok {
			t.Errorf("%q should NOT be flagged, but got %+v", name, p)
		}
	}
}

func TestDetectFluxProblems_HelmRelease(t *testing.T) {
	now := time.Now()
	hr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]any{"name": "hr", "namespace": "flux"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "InstallFailed", "message": "chart install failed"},
		}},
	}}
	got := detectFluxProblems([]*unstructured.Unstructured{hr}, "HelmRelease", fluxHelmGrp, now)
	if len(got) != 1 || got[0].Kind != "HelmRelease" || got[0].Reason != "InstallFailed" {
		t.Fatalf("want 1 HelmRelease InstallFailed problem, got %+v", got)
	}
	// The reason-specific action must be copied onto the Detection (not just
	// computed and dropped).
	if got[0].Action == "" {
		t.Errorf("Flux problem should carry an Action for reason InstallFailed")
	}
}

// TestDetectArgoAppProblems_OperationFailedOutranksDegraded pins the category
// contract: when an app is BOTH Degraded and has a Failed sync operation, the
// failed apply is the actionable root cause, so the row is OperationFailed
// (gitops_operation_failed) carrying the parsed cause/remediation — NOT
// HealthDegraded with the diagnosis dropped. The degraded health is a
// downstream symptom, surfaced separately as the managed resources' own issues.
func TestDetectArgoAppProblems_OperationFailedOutranksDegraded(t *testing.T) {
	now := time.Now()
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "both", "namespace": "argocd"},
		"spec":     map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{}}},
		"status": map[string]any{
			"health":         map[string]any{"status": "Degraded"},
			"sync":           map[string]any{"status": "OutOfSync"},
			"operationState": map[string]any{"phase": "Failed", "message": `namespaces "demo-x" not found`},
		},
	}}
	got := detectArgoAppProblems([]*unstructured.Unstructured{app}, now)
	if len(got) != 1 || got[0].Reason != "OperationFailed" {
		t.Fatalf("a Failed operation must win over Degraded (honest category), got %+v", got)
	}
	if got[0].Cause == "" || got[0].RemediationKind != diagnose.RemediationCreateNamespace {
		t.Errorf("the parsed operation diagnosis must survive: cause=%q remediation=%q", got[0].Cause, got[0].RemediationKind)
	}
}

func TestDetectArgoAppProblems_OperationFailedOutranksProgressing(t *testing.T) {
	now := time.Now()
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "progressing-failed", "namespace": "argocd"},
		"spec":     map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{}}},
		"status": map[string]any{
			"health":         map[string]any{"status": "Progressing"},
			"sync":           map[string]any{"status": "OutOfSync"},
			"operationState": map[string]any{"phase": "Failed", "message": `namespaces "demo-x" not found`},
		},
	}}
	got := detectArgoAppProblems([]*unstructured.Unstructured{app}, now)
	if len(got) != 1 || got[0].Reason != "OperationFailed" {
		t.Fatalf("a Failed operation must win over Progressing health, got %+v", got)
	}
	if got[0].Cause == "" || got[0].RemediationKind != diagnose.RemediationCreateNamespace {
		t.Errorf("the parsed operation diagnosis must survive: cause=%q remediation=%q", got[0].Cause, got[0].RemediationKind)
	}
}

func TestDetectArgoAppProblems_EnabledFalseIsManual(t *testing.T) {
	now := time.Now()
	// automated present but enabled:false => manual => Missing/OutOfSync must NOT flag.
	disabled := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "auto-off", "namespace": "argocd"},
		"spec":     map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"enabled": false}}},
		"status":   map[string]any{"health": map[string]any{"status": "Missing"}, "sync": map[string]any{"status": "OutOfSync"}},
	}}
	if got := detectArgoAppProblems([]*unstructured.Unstructured{disabled}, now); len(got) != 0 {
		t.Errorf("automated.enabled:false is manual — Missing/OutOfSync must NOT flag, got %+v", got)
	}
	// automated present without enabled (the common case) => automated => flags.
	enabled := disabled.DeepCopy()
	_ = unstructured.SetNestedMap(enabled.Object, map[string]any{}, "spec", "syncPolicy", "automated")
	if got := detectArgoAppProblems([]*unstructured.Unstructured{enabled}, now); len(got) != 1 {
		t.Errorf("automated present (no enabled key) should flag Missing, got %+v", got)
	}
}

// TestDetectArgoAppProblems_OperationFailedParsesCause pins that a Failed sync
// operation is emitted from status.operationState.message with a parsed Cause +
// structured remediation, and that the operation failure supersedes the
// parallel SyncError condition encoding so a single failure isn't
// double-reported.
func TestDetectArgoAppProblems_OperationFailedParsesCause(t *testing.T) {
	now := time.Now()
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "broken-sync", "namespace": "argocd"},
		"spec":     map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{}}},
		"status": map[string]any{
			"health": map[string]any{"status": "Healthy"},
			"sync":   map[string]any{"status": "OutOfSync"},
			"operationState": map[string]any{
				"phase":   "Failed",
				"message": `failed to create resource: namespaces "demo-broken-sync" not found`,
			},
			// Argo writes a SyncError condition that parallel-encodes the same
			// failure; the operation branch must supersede it (one row, not two).
			"conditions": []any{
				map[string]any{"type": "SyncError", "message": `namespaces "demo-broken-sync" not found`},
			},
		},
	}}
	got := detectArgoAppProblems([]*unstructured.Unstructured{app}, now)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 problem (operation failure supersedes SyncError), got %d: %+v", len(got), got)
	}
	d := got[0]
	if d.Severity != "critical" || d.Reason != "OperationFailed" {
		t.Errorf("got severity=%q reason=%q, want critical/OperationFailed", d.Severity, d.Reason)
	}
	if d.Cause == "" {
		t.Error("expected parsed Cause from operationState.message, got empty")
	}
	if d.RemediationKind != diagnose.RemediationCreateNamespace || d.RemediationTarget != "demo-broken-sync" {
		t.Errorf("got remediation kind=%q target=%q, want %q/demo-broken-sync", d.RemediationKind, d.RemediationTarget, diagnose.RemediationCreateNamespace)
	}
	if d.Message == "" {
		t.Error("expected the raw operation message preserved on Message")
	}
}

func TestDetectArgoAppProblems_OperationFailedUsesOperationTimestamp(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "fresh-failure", "namespace": "argocd"},
		"spec":     map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{}}},
		"status": map[string]any{
			"health": map[string]any{"status": "Healthy"},
			"sync":   map[string]any{"status": "OutOfSync"},
			"operationState": map[string]any{
				"phase":      "Failed",
				"message":    `namespaces "demo-x" not found`,
				"finishedAt": now.Add(-2 * time.Minute).Format(time.RFC3339),
			},
		},
	}}
	app.SetCreationTimestamp(metav1.NewTime(now.Add(-365 * 24 * time.Hour)))
	got := detectArgoAppProblems([]*unstructured.Unstructured{app}, now)
	if len(got) != 1 {
		t.Fatalf("want one failed operation, got %+v", got)
	}
	if got[0].DurationSeconds != int64((2 * time.Minute).Seconds()) {
		t.Fatalf("failed operation should age from operation finishedAt, got duration=%ds", got[0].DurationSeconds)
	}
}

func TestDetectArgoAppProblems_ErrorConditionUsesTransitionTimestamp(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "fresh-condition", "namespace": "argocd"},
		"spec":     map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{}}},
		"status": map[string]any{
			"health": map[string]any{"status": "Healthy"},
			"sync":   map[string]any{"status": "Unknown"},
			"conditions": []any{
				map[string]any{
					"type":               "ComparisonError",
					"message":            "app path does not exist",
					"lastTransitionTime": now.Add(-3 * time.Minute).Format(time.RFC3339),
				},
			},
		},
	}}
	app.SetCreationTimestamp(metav1.NewTime(now.Add(-365 * 24 * time.Hour)))
	got := detectArgoAppProblems([]*unstructured.Unstructured{app}, now)
	if len(got) != 1 || got[0].Reason != "ComparisonError" {
		t.Fatalf("want one ComparisonError, got %+v", got)
	}
	if got[0].DurationSeconds != int64((3 * time.Minute).Seconds()) {
		t.Fatalf("condition should age from lastTransitionTime, got duration=%ds", got[0].DurationSeconds)
	}
}

// TestDetectArgoAppProblems_StuckDriftLoop pins the "applied but still drifting"
// upgrade: an auto-synced app whose last sync Succeeded yet remains OutOfSync
// with a recent reconcile is StuckDriftLoop (critical), not ordinary OutOfSync
// (high). A stale reconcile (controller likely down) stays ordinary OutOfSync.
func TestDetectArgoAppProblems_StuckDriftLoop(t *testing.T) {
	now := time.Now()
	// phase="Succeeded" + sync=OutOfSync + auto-sync is the precondition; the
	// reconciledAt recency decides stuck-loop vs ordinary drift.
	mk := func(phase, reconciledAt string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": "stuck", "namespace": "argocd"},
			"spec":     map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{}}},
			"status": map[string]any{
				"health":         map[string]any{"status": "Healthy"},
				"sync":           map[string]any{"status": "OutOfSync"},
				"operationState": map[string]any{"phase": phase},
				"reconciledAt":   reconciledAt,
			},
		}}
	}
	ago := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339) }
	cases := []struct {
		name       string
		phase      string
		reconciled string
		wantReason string
	}{
		{"recent reconcile is stuck", "Succeeded", ago(2 * time.Minute), "StuckDriftLoop"},
		{"within the 30m window is stuck", "Succeeded", ago(29 * time.Minute), "StuckDriftLoop"},
		{"past the 30m window is ordinary drift", "Succeeded", ago(31 * time.Minute), "OutOfSync"},
		{"missing reconciledAt is ordinary drift", "Succeeded", "", "OutOfSync"},
		{"unparseable reconciledAt is ordinary drift", "Succeeded", "not-a-timestamp", "OutOfSync"},
		{"no completed operation is ordinary drift", "", ago(2 * time.Minute), "OutOfSync"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectArgoAppProblems([]*unstructured.Unstructured{mk(tc.phase, tc.reconciled)}, now)
			if len(got) != 1 || got[0].Reason != tc.wantReason {
				t.Fatalf("want 1 %s, got %+v", tc.wantReason, got)
			}
			if tc.wantReason == "StuckDriftLoop" {
				if got[0].Severity != "critical" {
					t.Errorf("StuckDriftLoop severity = %q, want critical", got[0].Severity)
				}
				if got[0].Action == "" || got[0].Cause == "" {
					t.Errorf("StuckDriftLoop should carry both a Cause and an Action; got cause=%q action=%q", got[0].Cause, got[0].Action)
				}
			}
		})
	}
}

// insightsOpDiagnosis extracts the operation-failure issue's parsed diagnosis
// from a detail-page Insight (the operation scope, excluding the in-flight /
// stuck-drift / manual-drift rows that share that scope).
type opDiagnosis struct {
	cause, remKind, remTarget string
	retryCount                int
	stuck                     bool
}

func insightsOpDiagnosis(in gitopsinsights.Insight) (opDiagnosis, bool) {
	for _, iss := range in.Issues {
		if iss.Scope != gitopsinsights.ScopeOperation {
			continue
		}
		if iss.Reason == "Running" || iss.Reason == "StuckDriftLoop" || iss.Reason == "ManualDrift" {
			continue
		}
		d := opDiagnosis{cause: iss.Cause, retryCount: iss.RetryCount, stuck: iss.Stuck}
		if iss.Remediation != nil {
			d.remKind = string(iss.Remediation.Kind)
			d.remTarget = iss.Remediation.Target
		}
		return d, true
	}
	return opDiagnosis{}, false
}

// TestGitOpsOperationDiagnosisParity is the parity guard for the FAILED-
// OPERATION path specifically: the cluster-wide issues engine
// (detectArgoAppProblems) and the GitOps detail page (insights.Build) must
// surface the SAME parsed cause + remediation. Both call pkg/gitops/diagnose,
// so this pins that the two authors agree on failed-operation diagnosis — and
// catches precedence drift that would drop it on one surface (the
// Degraded-shadows-OperationFailed bug). It is intentionally narrow: it does
// NOT prove general no-drift across every scope. Reason/category framing
// differs by design; detail-only scopes (lifecycle / per-resource / tree) and
// deferred advisories (manual-drift / *Warning) are out of scope.
func TestGitOpsOperationDiagnosisParity(t *testing.T) {
	now := time.Now()
	cases := []struct{ name, health, phase, msg string }{
		// The regression case: a failed apply while the app is also Degraded.
		{"missing namespace while degraded", "Degraded", "Failed", `one or more objects failed to apply, reason: namespaces "demo-x" not found`},
		{"rbac forbidden", "Healthy", "Error", `Deployment.apps "billing" is forbidden: User "x" cannot patch resource`},
		{"webhook denied", "Healthy", "Failed", `admission webhook "v.gatekeeper.sh" denied the request: missing label`},
		// Carries a retry suffix → both authors must agree retry_count=5, stuck.
		{"stuck after retries", "Degraded", "Failed", `one or more objects failed to apply, reason: namespaces "demo-x" not found (retried 5 times)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
				"metadata": map[string]any{"name": "app", "namespace": "argocd"},
				"spec":     map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{}}},
				"status": map[string]any{
					"health":         map[string]any{"status": tc.health},
					"sync":           map[string]any{"status": "OutOfSync"},
					"operationState": map[string]any{"phase": tc.phase, "message": tc.msg},
				},
			}}
			canon := detectArgoAppProblems([]*unstructured.Unstructured{app}, now)
			if len(canon) != 1 || canon[0].Reason != "OperationFailed" {
				t.Fatalf("issues engine: want 1 OperationFailed, got %+v", canon)
			}
			d, found := insightsOpDiagnosis(gitopsinsights.Build(app, nil, nil))
			if !found {
				t.Fatalf("detail page produced no operation issue for %q", tc.msg)
			}
			if canon[0].Cause != d.cause {
				t.Errorf("cause drift between authors:\n issues = %q\n detail = %q", canon[0].Cause, d.cause)
			}
			if canon[0].RemediationKind != d.remKind || canon[0].RemediationTarget != d.remTarget {
				t.Errorf("remediation drift: issues=(%q,%q) detail=(%q,%q)", canon[0].RemediationKind, canon[0].RemediationTarget, d.remKind, d.remTarget)
			}
			if canon[0].OperationRetryCount != d.retryCount || canon[0].Stuck != d.stuck {
				t.Errorf("retry/stuck drift: issues=(%d,%v) detail=(%d,%v)", canon[0].OperationRetryCount, canon[0].Stuck, d.retryCount, d.stuck)
			}
			// Every failed-operation row needs a next step: a structured
			// remediation, or (RBAC / webhook / unrecognized) an Action.
			if canon[0].RemediationKind == "" && canon[0].Action == "" {
				t.Errorf("a failed operation without remediation must still carry an Action")
			}
		})
	}
}

// TestDetectArgoAppProblems_EmptyOpMessagePrefersCondition pins that a Failed
// operation with NO message defers to a specific error condition (which holds
// the actionable guidance) rather than masking it with a generic operation row
// — but still emits a generic OperationFailed when there's no condition either.
func TestDetectArgoAppProblems_EmptyOpMessagePrefersCondition(t *testing.T) {
	now := time.Now()
	mk := func(conds []any) *unstructured.Unstructured {
		status := map[string]any{
			"health":         map[string]any{"status": "Healthy"},
			"sync":           map[string]any{"status": "OutOfSync"},
			"operationState": map[string]any{"phase": "Failed"}, // no message
		}
		if conds != nil {
			status["conditions"] = conds
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": "app", "namespace": "argocd"},
			"spec":     map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{}}},
			"status":   status,
		}}
	}
	// Empty op message + ComparisonError → the condition wins.
	got := detectArgoAppProblems([]*unstructured.Unstructured{mk([]any{
		map[string]any{"type": "ComparisonError", "message": "app path does not exist"},
	})}, now)
	if len(got) != 1 || got[0].Reason != "ComparisonError" || got[0].Action == "" {
		t.Fatalf("empty op message should defer to the ComparisonError condition with an action, got %+v", got)
	}
	// Empty op message + SyncError → the condition wins, but its Argo-shaped
	// message is still parsed so remediation/retry data survives.
	got = detectArgoAppProblems([]*unstructured.Unstructured{mk([]any{
		map[string]any{"type": "SyncError", "message": `namespaces "demo-x" not found (retried 5 times)`},
	})}, now)
	if len(got) != 1 || got[0].Reason != "SyncError" {
		t.Fatalf("empty op message should defer to the SyncError condition, got %+v", got)
	}
	if got[0].Cause == "" || got[0].RemediationKind != diagnose.RemediationCreateNamespace || got[0].OperationRetryCount != 5 || !got[0].Stuck {
		t.Fatalf("SyncError condition diagnosis was not parsed: %+v", got[0])
	}
	if got[0].Action != "" {
		t.Fatalf("structured remediation should be the next step; got extra action %q", got[0].Action)
	}
	// Empty op message + no condition → generic OperationFailed (not dropped).
	got = detectArgoAppProblems([]*unstructured.Unstructured{mk(nil)}, now)
	if len(got) != 1 || got[0].Reason != "OperationFailed" {
		t.Fatalf("empty op message without a condition should still emit OperationFailed, got %+v", got)
	}
}

func TestDetectCronJobProblems_CadenceAware(t *testing.T) {
	now := time.Now()
	mk := func(name, schedule string, lastRunAgo time.Duration) *batchv1.CronJob {
		last := metav1.NewTime(now.Add(-lastRunAgo))
		return &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-365 * 24 * time.Hour))},
			Spec:       batchv1.CronJobSpec{Schedule: schedule},
			Status:     batchv1.CronJobStatus{LastScheduleTime: &last},
		}
	}
	cjs := []*batchv1.CronJob{
		mk("quarterly", "0 0 1 */4 *", 29*24*time.Hour), // ran 29d ago, on schedule → NOT stale (the hubble FP)
		mk("daily-stale", "0 0 * * *", 3*24*time.Hour),  // daily, silent 3d → stale
	}
	stale := map[string]bool{}
	for _, p := range DetectCronJobProblems(cjs) {
		if p.Problem == "stale" {
			stale[p.Name] = true
		}
	}
	if stale["quarterly"] {
		t.Error("on-schedule quarterly CronJob must NOT be flagged stale")
	}
	if !stale["daily-stale"] {
		t.Error("daily CronJob silent for 3 days must be flagged stale")
	}
}
