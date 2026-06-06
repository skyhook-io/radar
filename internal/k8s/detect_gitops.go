package k8s

import (
	"log"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/conditions"
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
// and must NOT flag): skip Suspended/Progressing health and an in-flight sync
// (operationState.phase=Running); flag Degraded regardless of policy (critical —
// live resources are unhealthy, checked first so it outranks an error condition);
// then flag a ComparisonError/InvalidSpecError/SyncError condition (the sync=
// Unknown app-path-not-found case the generic path can't see); flag Missing/
// OutOfSync only for auto-synced apps. One row per app, most-severe cause first.
func detectArgoAppProblems(apps []*unstructured.Unstructured, now time.Time) []Detection {
	var out []Detection
	for _, app := range apps {
		ns, name := app.GetNamespace(), app.GetName()
		age := now.Sub(app.GetCreationTimestamp().Time)
		health, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
		healthMsg, _, _ := unstructured.NestedString(app.Object, "status", "health", "message")
		sync, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
		phase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
		// Argo's own per-app health message ("Deployment X has 0/3 replicas…")
		// is far more decisive than a generic string; fall back when empty.
		orMsg := func(fallback string) string {
			if strings.TrimSpace(healthMsg) != "" {
				return healthMsg
			}
			return fallback
		}

		if strings.EqualFold(health, "Suspended") || strings.EqualFold(health, "Progressing") || strings.EqualFold(phase, "Running") {
			continue
		}

		// Degraded (live resources unhealthy) is the most severe state and is
		// checked first: an app that is BOTH Degraded and carrying an error
		// condition must stay critical, not be downgraded to the high-severity
		// error branch below.
		if strings.EqualFold(health, "Degraded") {
			out = append(out, gitopsProblem("Application", argoGroup, ns, name, "critical",
				"HealthDegraded", orMsg("Application health is Degraded (managed resources are unhealthy)"), age))
			continue
		}
		// A failed sync (ComparisonError/SyncError/InvalidSpecError) is a genuine
		// reconciliation failure, not drift — critical, matching the GitOps detail
		// view rather than under-ranking it as a warning.
		if ct, msg, ok := argoErrorCondition(app); ok {
			out = append(out, gitopsProblem("Application", argoGroup, ns, name, "critical", ct, msg, age))
			continue
		}
		automated := argoIsAutomated(app)
		if strings.EqualFold(health, "Missing") && automated {
			// Auto-synced app whose managed resources are GONE is critical — the
			// declared state isn't running at all.
			out = append(out, gitopsProblem("Application", argoGroup, ns, name, "critical",
				"HealthMissing", orMsg("auto-synced Application's managed resources are missing from the cluster"), age))
			continue
		}
		if strings.EqualFold(sync, "OutOfSync") && automated {
			out = append(out, gitopsProblem("Application", argoGroup, ns, name, "high",
				"OutOfSync", "auto-synced Application has drifted from the desired manifests", age))
		}
	}
	return out
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
func argoErrorCondition(app *unstructured.Unstructured) (condType, message string, found bool) {
	conds, ok, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	if !ok {
		return "", "", false
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
			return ct, msg, true
		}
	}
	return "", "", false
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
		out = append(out, p)
	}
	return out
}
