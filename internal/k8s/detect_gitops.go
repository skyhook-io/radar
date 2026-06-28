package k8s

import (
	"log"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/conditions"
	"github.com/skyhook-io/radar/pkg/gitops/diagnose"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// listScoped reads gvr at the right scope for a curated detector: an explicit
// namespace lists just that namespace; "" (the cluster-wide "all visible scope"
// intent) uses ListWatched, which UNIONS cluster-wide AND per-namespace caches —
// unlike List(gvr,"") which is cluster-wide-only and silently drops namespace-
// scoped contents in a namespace-restricted install.
func listScoped(dc *DynamicResourceCache, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if namespace == "" {
		return dc.ListWatched(gvr)
	}
	return dc.List(gvr, namespace)
}

const (
	argoGroup   = "argoproj.io"
	fluxKustGrp = "kustomize.toolkit.fluxcd.io"
	fluxHelmGrp = "helm.toolkit.fluxcd.io"
)

// DetectGitOpsProblems surfaces failing GitOps reconcilers — ArgoCD Applications
// and Flux Kustomizations/HelmReleases — that the generic CRD-condition fallback
// structurally misses. Argo encodes health and sync in dedicated status
// sub-objects (status.health.status, status.sync.status) rather than as
// status.conditions[type=Ready] entries, so FindFalseCondition never sees a
// Degraded/Missing/OutOfSync app; and Argo "ComparisonError" lives only in
// status.conditions[].type (no status=False). This detector reads each
// controller's real shape. Wired like DetectCAPIProblems; detectGenericCRDIssues
// skips exactly the kinds handled here (isCuratedCRDKind) so there is no
// double-report, while leaving sibling kinds (e.g. Argo Rollout) to the generic
// path.
func DetectGitOpsProblems(dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string) []Detection {
	if dynamicCache == nil || discovery == nil {
		return nil
	}
	now := time.Now()
	list := func(kind, group string) []*unstructured.Unstructured {
		gvr, ok := discovery.GetGVRWithGroup(kind, group)
		if !ok {
			return nil // controller not installed — expected
		}
		items, err := listScoped(dynamicCache, gvr, namespace)
		if err != nil {
			log.Printf("[gitops-problems] Failed to list %s.%s: %v", kind, group, err)
			return nil
		}
		return items
	}

	var problems []Detection
	problems = append(problems, detectArgoAppProblems(list("Application", argoGroup), now)...)
	problems = append(problems, detectFluxProblems(list("Kustomization", fluxKustGrp), "Kustomization", fluxKustGrp, now)...)
	problems = append(problems, detectFluxProblems(list("HelmRelease", fluxHelmGrp), "HelmRelease", fluxHelmGrp, now)...)
	return problems
}

func gitopsProblem(kind, group, ns, name, severity, reason, message string, age time.Duration) Detection {
	return Detection{
		Kind:            kind,
		Group:           group,
		Namespace:       ns,
		Name:            name,
		Severity:        severity,
		Reason:          reason,
		Message:         message,
		Age:             FormatAge(age),
		AgeSeconds:      int64(age.Seconds()),
		Duration:        FormatAge(age),
		DurationSeconds: int64(age.Seconds()),
	}
}

// detectArgoAppProblems reads ArgoCD Application health/sync. Precision gates,
// all load-bearing (a manual or suspended app legitimately sits OutOfSync/Missing
// and must NOT flag): skip an in-flight sync (operationState.phase=Running);
// flag failed operations before health rollups because the operation message is
// the actionable root cause; skip Suspended/Progressing health for non-failed
// apps; flag Degraded regardless of policy (critical — live resources are
// unhealthy); then flag a ComparisonError/InvalidSpecError/SyncError condition
// (the sync=Unknown app-path-not-found case the generic path can't see); flag
// Missing/OutOfSync only for auto-synced apps. One row per app, most-severe
// cause first.
func detectArgoAppProblems(apps []*unstructured.Unstructured, now time.Time) []Detection {
	var out []Detection
	for _, app := range apps {
		ns, name := app.GetNamespace(), app.GetName()
		age := now.Sub(app.GetCreationTimestamp().Time)
		health, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
		healthMsg, _, _ := unstructured.NestedString(app.Object, "status", "health", "message")
		sync, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
		phase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
		opMsg, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "message")
		// Argo's own per-app health message ("Deployment X has 0/3 replicas…")
		// is far more decisive than a generic string; fall back when empty.
		orMsg := func(fallback string) string {
			if strings.TrimSpace(healthMsg) != "" {
				return healthMsg
			}
			return fallback
		}

		if strings.EqualFold(phase, "Running") {
			continue
		}

		// A failed sync operation is the most actionable signal and outranks a
		// Degraded health rollup. status.operationState.message names the failing
		// resource + reason, which we parse into a plain-English cause + one-click
		// remediation. When an app is BOTH Degraded and carries a failed
		// operation, the failed apply is the root cause while the degraded health
		// is a downstream symptom (already surfaced as the managed resources'
		// own issues, grouped by their owner). Emitting gitops_operation_failed
		// keeps the category honest so a consumer filtering for operation
		// failures (MCP / Issues) doesn't miss it under a health bucket. Also
		// checked before the condition branch: Argo's SyncError condition
		// parallel-encodes this same message, so emitting both would
		// double-report one failure.
		if strings.EqualFold(phase, "Failed") || strings.EqualFold(phase, "Error") {
			// An empty operation message carries no detail. If a specific error
			// condition (ComparisonError / InvalidSpecError / SyncError) is
			// present, it holds the actionable guidance — prefer it over a
			// generic "operation failed" row rather than masking it.
			if strings.TrimSpace(opMsg) == "" {
				if ct, cmsg, since, hasSince, ok := argoErrorCondition(app, now); ok {
					d := gitopsProblem("Application", argoGroup, ns, name, "critical", ct, cmsg, fallbackDuration(since, hasSince, age))
					if ct == "SyncError" {
						applyArgoOperationDiagnosis(&d, cmsg)
					}
					if d.RemediationKind == "" {
						d.Action = diagnose.ActionForCondition(ct)
					}
					out = append(out, d)
					continue
				}
			}
			msg := opMsg
			if strings.TrimSpace(msg) == "" {
				msg = "Last sync operation failed"
			}
			d := gitopsProblem("Application", argoGroup, ns, name, "critical", "OperationFailed", msg, argoOperationIssueAge(app, now, age))
			applyArgoOperationDiagnosis(&d, opMsg)
			// When there's a structured remediation, that one-click fix IS the
			// next step. Otherwise (RBAC / webhook / immutable field, or an
			// unrecognized message) point the operator at the operation details
			// so every failure has a next step, not just a diagnosis.
			if d.RemediationKind == "" {
				d.Action = "Open the application's sync operation details for the full error and history."
			}
			out = append(out, d)
			continue
		}
		if strings.EqualFold(health, "Suspended") || strings.EqualFold(health, "Progressing") {
			continue
		}
		// Degraded (live resources unhealthy) without a failed operation — the
		// managed resources are unhealthy on their own. Outranks the error
		// conditions below so a Degraded app stays critical-Degraded rather than
		// reframed as a lower-information condition row.
		if strings.EqualFold(health, "Degraded") {
			dd := gitopsProblem("Application", argoGroup, ns, name, "critical",
				"HealthDegraded", orMsg("Application health is Degraded (managed resources are unhealthy)"), age)
			dd.Action = "Open the resource tree and inspect the unhealthy managed resource(s)."
			out = append(out, dd)
			continue
		}
		// ComparisonError / InvalidSpecError are source/spec failures that occur
		// without a sync operation (so operationState above won't catch them) —
		// genuine reconciliation failures, critical, with the same condition-
		// specific guidance the detail page shows.
		if ct, msg, since, hasSince, ok := argoErrorCondition(app, now); ok {
			d := gitopsProblem("Application", argoGroup, ns, name, "critical", ct, msg, fallbackDuration(since, hasSince, age))
			d.Action = diagnose.ActionForCondition(ct)
			out = append(out, d)
			continue
		}
		automated := argoIsAutomated(app)
		if strings.EqualFold(health, "Missing") && automated {
			// Auto-synced app whose managed resources are GONE is critical — the
			// declared state isn't running at all.
			dd := gitopsProblem("Application", argoGroup, ns, name, "critical",
				"HealthMissing", orMsg("auto-synced Application's managed resources are missing from the cluster"), age)
			dd.Action = "Sync the Application to recreate its managed resources."
			out = append(out, dd)
			continue
		}
		if strings.EqualFold(sync, "OutOfSync") && automated {
			// Stuck-drift loop: the last sync Succeeded yet the app is still
			// OutOfSync and reconciled recently — something is mutating resources
			// after each apply (mutating webhook, sibling controller, conversion
			// webhook). Critical and distinct from ordinary drift, where the apply
			// simply hasn't run.
			if isArgoStuckDriftLoop(app, now) {
				d := gitopsProblem("Application", argoGroup, ns, name, "critical",
					"StuckDriftLoop", "Sync succeeded but the application is still OutOfSync — a controller or admission webhook is likely mutating resources after each apply.", age)
				d.Stuck = true
				d.Cause = "Auto-sync applied cleanly and reconciled recently, yet live state keeps diverging from Git. Common causes: a mutating admission webhook adds defaults Argo isn't told to ignore; a sibling controller (Karpenter, Istio, cert-manager) writes back into spec; or a conversion webhook rewrites a deprecated API schema."
				d.Action = "Open Changes to see the per-resource drift, then match it against your Git manifest, the resource's controller, and any mutating webhooks."
				out = append(out, d)
			} else {
				dd := gitopsProblem("Application", argoGroup, ns, name, "high",
					"OutOfSync", "auto-synced Application has drifted from the desired manifests", age)
				dd.Action = "Review the diff, then fix Git (or ignoreDifferences / the mutating controller) and refresh; check Argo events if it keeps drifting."
				out = append(out, dd)
			}
		}
	}
	return out
}

func applyArgoOperationDiagnosis(d *Detection, msg string) {
	parsed := diagnose.ParseArgoOperationError(msg)
	d.Cause = parsed.Cause
	d.RemediationKind = parsed.RemediationKind
	d.RemediationTarget = parsed.RemediationTarget
	d.OperationRetryCount = parsed.RetryCount
	d.Stuck = parsed.Stuck
}

func argoOperationIssueAge(app *unstructured.Unstructured, now time.Time, fallback time.Duration) time.Duration {
	if finishedAt, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "finishedAt"); finishedAt != "" {
		if d, ok := durationFromTimestamp(now, finishedAt); ok {
			return d
		}
	}
	if startedAt, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "startedAt"); startedAt != "" {
		if d, ok := durationFromTimestamp(now, startedAt); ok {
			return d
		}
	}
	return fallback
}

func durationFromTimestamp(now time.Time, ts string) (time.Duration, bool) {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil || t.After(now) {
		return 0, false
	}
	return now.Sub(t), true
}

func fallbackDuration(d time.Duration, ok bool, fallback time.Duration) time.Duration {
	if ok {
		return d
	}
	return fallback
}

// isArgoStuckDriftLoop reports the "applied but still drifting" case: the last
// sync operation Succeeded, yet the app is still OutOfSync and reconciled
// recently. Caller has already gated on sync=OutOfSync + auto-sync on. Uses the
// same 30-minute reconciledAt window as the GitOps detail-page detector so the
// two surfaces agree on severity. An unparseable reconciledAt yields false (the
// app stays an ordinary OutOfSync row) — Argo writes RFC3339, so this is a
// shouldn't-happen guard, not a swallowed error.
func isArgoStuckDriftLoop(app *unstructured.Unstructured, now time.Time) bool {
	phase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	if !strings.EqualFold(phase, "Succeeded") {
		return false
	}
	reconciledAt, _, _ := unstructured.NestedString(app.Object, "status", "reconciledAt")
	if reconciledAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, reconciledAt)
	if err != nil {
		return false
	}
	// 30-minute window mirrors the detail-page detector: long enough for a
	// slow-converging resource to settle, short enough that "stale for an hour"
	// (a different problem — controller down) doesn't trip the stuck signal.
	return now.Sub(t) <= 30*time.Minute
}

// argoIsAutomated reports whether spec.syncPolicy.automated is present — i.e. the
// app is expected to self-heal, so OutOfSync/Missing is a real failure rather
// than an operator who simply hasn't synced a manual app yet.
func argoIsAutomated(app *unstructured.Unstructured) bool {
	automated, found, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
	if !found {
		return false
	}
	// Newer Argo CD can disable auto-sync without removing the block, via
	// spec.syncPolicy.automated.enabled: false — treat that as manual so an
	// intentionally-unsynced app isn't flagged for OutOfSync/Missing.
	if enabled, ok, _ := unstructured.NestedBool(automated, "enabled"); ok && !enabled {
		return false
	}
	return true
}

// argoErrorCondition returns the first status.conditions entry whose type names
// an error (ComparisonError / InvalidSpecError / SyncError). Argo writes these
// as {type, message} without a status field, so FindFalseCondition can't match
// them.
func argoErrorCondition(app *unstructured.Unstructured, now time.Time) (condType, message string, since time.Duration, hasSince bool, found bool) {
	conds, ok, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	if !ok {
		return "", "", 0, false, false
	}
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		ct, _ := cm["type"].(string)
		switch ct {
		case "ComparisonError", "InvalidSpecError", "SyncError":
			msg, _ := cm["message"].(string)
			ts, _ := cm["lastTransitionTime"].(string)
			since, hasSince := durationFromTimestamp(now, ts)
			return ct, msg, since, hasSince, true
		}
	}
	return "", "", 0, false, false
}

// detectFluxProblems flags Flux Kustomizations/HelmReleases whose Ready condition
// is False for a genuine (non-in-progress) reason. Unlike the broad
// conditions.IsTransientConditionReason set used for health display, this uses a
// NARROW in-progress set (conditions.IsInProgressForIssues) so genuinely-stuck
// states the health path treats as transient (ArtifactFailed, ChartNotReady) DO
// surface as issues. Skips suspended objects and stale-generation conditions
// (controller hasn't observed the current spec).
func detectFluxProblems(items []*unstructured.Unstructured, kind, group string, now time.Time) []Detection {
	var out []Detection
	for _, obj := range items {
		if suspend, ok, _ := unstructured.NestedBool(obj.Object, "spec", "suspend"); ok && suspend {
			continue
		}
		_, reason, msg, since, ok := conditions.FindFalseCondition(obj, "Ready")
		if !ok || conditions.IsInProgressForIssues(reason) {
			continue
		}
		// status.conditions stale relative to spec → mid-reconcile, not failed.
		if gen := obj.GetGeneration(); gen > 0 {
			if observed, ok, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration"); ok && observed > 0 && observed < gen {
				continue
			}
		}
		age := now.Sub(obj.GetCreationTimestamp().Time)
		d := since
		if d == 0 {
			d = age
		}
		displayReason := reason
		if displayReason == "" {
			displayReason = "Ready=False"
		}
		// A Flux Ready=False for a genuine (non-in-progress) reason is a real
		// reconciliation failure — critical, aligning Issues with the GitOps
		// detail view instead of under-ranking it as a warning.
		p := gitopsProblem(kind, group, obj.GetNamespace(), obj.GetName(), "critical", displayReason, msg, age)
		p.DurationSeconds = int64(d.Seconds())
		p.Duration = FormatAge(d)
		if since > 0 {
			timingR := IssueTimingFromConditionLTT(now.Add(-since), obj.GetCreationTimestamp().Time, "condition")
			p.IssueTiming, p.IssueTimingBasis = timingR.IssueTiming, timingR.Basis
		}
		p.Action = diagnose.ActionForFluxReason(reason)
		out = append(out, p)
	}
	return out
}
