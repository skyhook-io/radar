package k8s

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/conditions"
	"github.com/skyhook-io/radar/pkg/gitops/diagnose"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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

const (
	// manualDriftGate is how long a manual-sync Application must sit
	// continuously OutOfSync before we warn: short enough to catch a forgotten
	// change, long enough not to nag about an in-progress one an operator is
	// about to sync.
	manualDriftGate = 24 * time.Hour
	// argoDriftRetain drops drift entries for apps we stop seeing (deleted
	// without a final in-sync observation); it must exceed the compose cadence
	// by a wide margin so a live app is never purged mid-drift.
	argoDriftRetain = time.Hour

	// argoStaleFloor is the minimum "sync verdict is stale" threshold, used when
	// argocd-cm's timeout.reconciliation is unreadable or small.
	argoStaleFloor = 30 * time.Minute

	// argoStuckDriftMinDuration is how long an auto-synced app must sit
	// continuously OutOfSync-after-a-successful-sync before we call it a stuck
	// drift loop rather than a healthy app briefly OutOfSync in the window between
	// a self-healing sync completing and the next reconcile flipping it to Synced.
	// Without this floor a single transient snapshot trips a critical issue.
	argoStuckDriftMinDuration = 5 * time.Minute

	argoControllerLabelKey = "app.kubernetes.io/name"
	argoControllerLabelVal = "argocd-application-controller"
)

type argoDriftEntry struct {
	firstSeen    time.Time
	lastObserved time.Time
}

// argoDriftTracker records how long each manual-sync Application has been
// continuously OutOfSync. It hangs off the per-cluster ResourceCache, so a
// kubeconfig context switch drops it naturally; a Radar restart resets every
// clock, which is why the gated warning is deliberately conservative (we would
// rather under-warn than invent a drift-onset time we never observed).
type argoDriftTracker struct {
	mu      sync.Mutex
	entries map[types.UID]argoDriftEntry
}

func newArgoDriftTracker() *argoDriftTracker {
	return &argoDriftTracker{entries: map[types.UID]argoDriftEntry{}}
}

// observe records the app's current sync state and returns how long it has been
// continuously OutOfSync. An in-sync observation clears the entry (drift
// resolved) and returns 0.
func (t *argoDriftTracker) observe(uid types.UID, outOfSync bool, now time.Time) time.Duration {
	if t == nil || uid == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !outOfSync {
		delete(t.entries, uid)
		return 0
	}
	e, ok := t.entries[uid]
	if !ok {
		e.firstSeen = now
	}
	e.lastObserved = now
	t.entries[uid] = e
	return now.Sub(e.firstSeen)
}

// purge drops entries not observed within retain, covering apps deleted without
// a final in-sync observation.
func (t *argoDriftTracker) purge(now time.Time, retain time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for uid, e := range t.entries {
		if now.Sub(e.lastObserved) > retain {
			delete(t.entries, uid)
		}
	}
}

func driftTracker(cache *ResourceCache) *argoDriftTracker {
	if cache == nil {
		return nil
	}
	return cache.argoDrift
}

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

	// The typed cache carries Radar-tracked state (manual-drift continuity) and
	// the typed listers (controller pods, argocd-cm) the stale detector needs.
	// nil in unit tests that only stand up the dynamic cache — every consumer
	// here degrades gracefully.
	cache := GetResourceCache()
	argoApps := list("Application", argoGroup)

	var problems []Detection
	problems = append(problems, detectArgoAppProblems(argoApps, driftTracker(cache), now)...)
	// Controller-staleness is a cluster-wide signal: it needs global Application
	// counts plus cross-namespace controller-pod health, and a namespace-scoped
	// viewer can't see the controller pods anyway. Compute it only on the
	// cluster-wide pass (namespace == ""), where argoApps is the full fleet.
	// The drift tracker is purged here too, and ONLY here: a namespace-scoped
	// pass observes just its slice of the fleet, so purging on it would drop
	// other namespaces' drift clocks and restart their 24h gate.
	if namespace == "" {
		driftTracker(cache).purge(now, argoDriftRetain)
		problems = append(problems, detectArgoStaleFromCache(cache, argoApps, now)...)
	}
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
func detectArgoAppProblems(apps []*unstructured.Unstructured, tracker *argoDriftTracker, now time.Time) []Detection {
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

		automated := argoIsAutomated(app)
		outOfSync := strings.EqualFold(sync, "OutOfSync")
		// Track drift continuity for every app before any gate short-circuits the
		// loop, so a suspended-then-resumed or mid-degraded app that stays
		// OutOfSync keeps one clock. Manual apps use it for the 24h stale-drift
		// warning; auto-synced apps use it to require SUSTAINED OutOfSync before a
		// stuck-drift-loop verdict, so a transient post-sync snapshot doesn't trip
		// a false critical.
		outOfSyncFor := tracker.observe(app.GetUID(), outOfSync, now)
		var manualDriftFor time.Duration
		if !automated {
			manualDriftFor = outOfSyncFor
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
				if ct, cmsg, rawMsg, since, hasSince, ok := argoErrorCondition(app, now); ok {
					d := gitopsProblem("Application", argoGroup, ns, name, "critical", ct, cmsg, fallbackDuration(since, hasSince, age))
					d.RawMessage = rawMsg
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
			msg, rawMsg := diagnose.CleanArgoControllerMessageWithRaw(opMsg)
			if strings.TrimSpace(msg) == "" {
				msg = "Last sync operation failed"
			}
			d := gitopsProblem("Application", argoGroup, ns, name, "critical", "OperationFailed", msg, argoOperationIssueAge(app, now, age))
			d.RawMessage = rawMsg
			applyArgoOperationDiagnosis(&d, msg)
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
		if ct, msg, rawMsg, since, hasSince, ok := argoErrorCondition(app, now); ok {
			d := gitopsProblem("Application", argoGroup, ns, name, "critical", ct, msg, fallbackDuration(since, hasSince, age))
			d.RawMessage = rawMsg
			d.Action = diagnose.ActionForCondition(ct)
			out = append(out, d)
			continue
		}
		if strings.EqualFold(health, "Missing") && automated {
			// Auto-synced app whose managed resources are GONE is critical — the
			// declared state isn't running at all.
			dd := gitopsProblem("Application", argoGroup, ns, name, "critical",
				"HealthMissing", orMsg("auto-synced Application's managed resources are missing from the cluster"), age)
			dd.Action = "Sync the Application to recreate its managed resources."
			out = append(out, dd)
			continue
		}
		if outOfSync && automated {
			// Stuck-drift loop: the last sync Succeeded yet the app is still
			// OutOfSync and reconciled recently — something is mutating resources
			// after each apply (mutating webhook, sibling controller, conversion
			// webhook). Critical and distinct from ordinary drift, where the apply
			// simply hasn't run.
			if isArgoStuckDriftLoop(app, now) && outOfSyncFor >= argoStuckDriftMinDuration {
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
		// A manual-sync app legitimately sits OutOfSync until an operator syncs
		// it, so unlike the auto-synced branch above we only warn once the drift
		// has persisted past manualDriftGate — long enough to be a forgotten
		// change rather than one mid-review.
		if !automated && outOfSync && manualDriftFor >= manualDriftGate {
			dd := gitopsProblem("Application", argoGroup, ns, name, "warning", "OutOfSyncManual",
				fmt.Sprintf("Application has been out of sync for %s and auto-sync is not enabled", FormatAge(manualDriftFor)), age)
			// Anchor FirstSeen to drift onset, not resource age.
			dd.DurationSeconds = int64(manualDriftFor.Seconds())
			dd.Duration = FormatAge(manualDriftFor)
			dd.Action = "Review the drift in Changes, then Sync the application (or enable auto-sync) if the drift is unintended."
			out = append(out, dd)
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
func argoErrorCondition(app *unstructured.Unstructured, now time.Time) (condType, message, rawMessage string, since time.Duration, hasSince bool, found bool) {
	conds, ok, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	if !ok {
		return "", "", "", 0, false, false
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
			msg, rawMsg := diagnose.CleanArgoControllerMessageWithRaw(msg)
			ts, _ := cm["lastTransitionTime"].(string)
			since, hasSince := durationFromTimestamp(now, ts)
			return ct, msg, rawMsg, since, hasSince, true
		}
	}
	return "", "", "", 0, false, false
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

// argoControllerHealth summarizes the application-controller's pod health for
// the stale-comparison detector. visible reports whether Radar can see the
// controller pods at all — RBAC may hide them — in which case we cannot pin
// staleness on the controller and fall back to per-app rows. subject* name the
// controller workload the pods roll up to, used as the rollup issue's subject.
type argoControllerHealth struct {
	visible          bool
	ready            int
	subjectKind      string
	subjectName      string
	subjectNamespace string
}

func (h argoControllerHealth) healthy() bool { return h.ready >= 1 }

// detectArgoStaleFromCache reads controller health + the reconciliation timeout
// from the typed caches, then runs the pure staleness detector over the app
// fleet. Argo only re-compares sync/drift on refresh, so a down or wedged
// application-controller silently freezes every verdict; this surfaces that.
func detectArgoStaleFromCache(cache *ResourceCache, apps []*unstructured.Unstructured, now time.Time) []Detection {
	if len(apps) == 0 {
		return nil
	}
	ctrl := argoControllerHealthFromCache(cache)
	threshold := argoStaleThreshold(cache, ctrl.subjectNamespace)
	return detectArgoStale(apps, ctrl, threshold, now)
}

// maxReconcileStaleness returns the largest now-lastReconcile across apps (0 when
// none carry a timestamp). It's stamped as the rollup's Age so the issues layer
// anchors FirstSeen to the oldest reconcile — when the freeze began — instead of
// resetting it to the compose time on every poll (which makes a chronic outage
// keep sorting as brand new). See resourceAge in detect.go for the same pattern.
func maxReconcileStaleness(now time.Time, apps []*unstructured.Unstructured) time.Duration {
	var max time.Duration
	for _, app := range apps {
		if since, ok := durationFromTimestamp(now, argoReconciledAt(app)); ok && since > max {
			max = since
		}
	}
	return max
}

// detectArgoStale applies the two-level staleness verdict. Level one: if the
// controller is visible but has no Ready replica, every Application's verdict is
// frozen — one critical rollup on the controller, the individual stale apps
// being the symptom. Level two (controller healthy or hidden): a majority of a
// non-trivial fleet stale rolls up to one warning; otherwise each stale app
// gets its own warning. Pure over its inputs so the rules are unit-testable.
func detectArgoStale(apps []*unstructured.Unstructured, ctrl argoControllerHealth, threshold time.Duration, now time.Time) []Detection {
	if len(apps) == 0 {
		return nil
	}
	if ctrl.visible && !ctrl.healthy() {
		d := gitopsProblem(ctrl.subjectKind, argoControllerGroup(ctrl.subjectKind), ctrl.subjectNamespace, ctrl.subjectName,
			"critical", "GitOpsControllerStalled",
			fmt.Sprintf("Argo CD application-controller is not running — sync status and drift detection are frozen for %s", countApps(len(apps))), maxReconcileStaleness(now, apps))
		d.Stuck = true
		d.Action = "Inspect the application-controller pods (logs, restarts, resource limits) — no Application will sync or re-compare until it is running again."
		return []Detection{d}
	}

	var eligible, stale []*unstructured.Unstructured
	for _, app := range apps {
		if !argoAppEligibleForStale(app) {
			continue
		}
		eligible = append(eligible, app)
		if since, ok := durationFromTimestamp(now, argoReconciledAt(app)); ok && since > threshold {
			stale = append(stale, app)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	// A majority of a non-trivial fleet stale points at the controller, not the
	// apps — one rollup instead of a row per app (noise-storm suppression). The
	// >=3 floor keeps a tiny fleet (where one stale app is not a fleet signal)
	// on the per-app path.
	if ctrl.healthy() && len(eligible) >= 3 && len(stale)*2 > len(eligible) {
		d := gitopsProblem(ctrl.subjectKind, argoControllerGroup(ctrl.subjectKind), ctrl.subjectNamespace, ctrl.subjectName,
			"warning", "GitOpsComparisonsStale",
			fmt.Sprintf("%d of %d Applications have stale sync/drift comparisons — the application-controller may be overloaded or wedged", len(stale), len(eligible)), maxReconcileStaleness(now, stale))
		d.Action = "Check the application-controller for reconcile backlog or throttling; individual Applications' verdicts are older than expected."
		return []Detection{d}
	}
	out := make([]Detection, 0, len(stale))
	for _, app := range stale {
		since, _ := durationFromTimestamp(now, argoReconciledAt(app))
		d := gitopsProblem("Application", argoGroup, app.GetNamespace(), app.GetName(),
			"warning", "ComparisonStale",
			fmt.Sprintf("Sync and drift status may be stale — last re-compared %s ago (the application-controller has not re-evaluated this Application recently)", FormatAge(since)), 0)
		d.DurationSeconds = int64(since.Seconds())
		d.Duration = FormatAge(since)
		d.Action = "Refresh the Application to force a re-comparison; if it stays stale, check the application-controller health."
		out = append(out, d)
	}
	return out
}

// argoAppEligibleForStale filters to apps whose comparison age is meaningful: a
// never-reconciled app has no verdict to be stale, a terminating app is being
// torn down, and a mid-operation app is expected to lag.
func argoAppEligibleForStale(app *unstructured.Unstructured) bool {
	if app.GetDeletionTimestamp() != nil {
		return false
	}
	if strings.TrimSpace(argoReconciledAt(app)) == "" {
		return false
	}
	phase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	return !strings.EqualFold(phase, "Running")
}

func argoReconciledAt(app *unstructured.Unstructured) string {
	v, _, _ := unstructured.NestedString(app.Object, "status", "reconciledAt")
	return v
}

func argoControllerGroup(kind string) string {
	switch kind {
	case "StatefulSet", "Deployment":
		return "apps"
	default:
		return ""
	}
}

func countApps(n int) string {
	if n == 1 {
		return "1 Application"
	}
	return fmt.Sprintf("%d Applications", n)
}

// argoControllerHealthFromCache finds the application-controller pods across ALL
// cached namespaces (the controller is not always in "argocd"), counts Ready
// replicas, and resolves the workload subject the pods belong to.
func argoControllerHealthFromCache(cache *ResourceCache) argoControllerHealth {
	if cache == nil || cache.Pods() == nil {
		return argoControllerHealth{}
	}
	pods, err := cache.Pods().List(labels.SelectorFromSet(labels.Set{argoControllerLabelKey: argoControllerLabelVal}))
	if err != nil || len(pods) == 0 {
		return argoControllerHealth{}
	}
	h := argoControllerHealth{visible: true}
	for _, p := range pods {
		if isPodReadyForProblem(p) {
			h.ready++
		}
	}
	h.subjectKind, h.subjectName, h.subjectNamespace = argoControllerSubject(cache, pods)
	return h
}

// argoControllerSubject resolves the controller workload the pods roll up to
// (StatefulSet in the standard install, Deployment via ReplicaSet in some),
// falling back to a concrete pod so the rollup issue always has a subject.
func argoControllerSubject(cache *ResourceCache, pods []*corev1.Pod) (kind, name, namespace string) {
	for _, p := range pods {
		ref := metav1.GetControllerOf(p)
		if ref == nil {
			continue
		}
		switch ref.Kind {
		case "StatefulSet", "Deployment":
			return ref.Kind, ref.Name, p.Namespace
		case "ReplicaSet":
			if dName, ok := deploymentForReplicaSet(cache, p.Namespace, ref.Name); ok {
				return "Deployment", dName, p.Namespace
			}
		}
	}
	return "Pod", pods[0].Name, pods[0].Namespace
}

func deploymentForReplicaSet(cache *ResourceCache, namespace, rsName string) (string, bool) {
	if cache == nil || cache.ReplicaSets() == nil {
		return "", false
	}
	rs, err := cache.ReplicaSets().ReplicaSets(namespace).Get(rsName)
	if err != nil || rs == nil {
		return "", false
	}
	if ref := metav1.GetControllerOf(rs); ref != nil && ref.Kind == "Deployment" {
		return ref.Name, true
	}
	return "", false
}

// argoStaleThreshold is max(argoStaleFloor, 10× argocd-cm's
// timeout.reconciliation) — 10× the re-compare period tolerates a few missed
// cycles before calling a verdict stale. Unreadable/absent config → the floor.
func argoStaleThreshold(cache *ResourceCache, controllerNamespace string) time.Duration {
	if cache == nil || controllerNamespace == "" || cache.ConfigMaps() == nil {
		return argoStaleFloor
	}
	cm, err := cache.ConfigMaps().ConfigMaps(controllerNamespace).Get("argocd-cm")
	if err != nil || cm == nil {
		return argoStaleFloor
	}
	return argoStaleThresholdFromValue(cm.Data["timeout.reconciliation"])
}

func argoStaleThresholdFromValue(raw string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return argoStaleFloor
	}
	if scaled := 10 * d; scaled > argoStaleFloor {
		return scaled
	}
	return argoStaleFloor
}
