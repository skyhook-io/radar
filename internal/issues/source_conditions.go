package issues

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/logsafe"
	"github.com/skyhook-io/radar/pkg/conditions"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// detectGenericCRDIssues walks every watched dynamic CRD and emits a
// warning Issue for each object that has a False Ready/Available/etc.
// condition. Skips kinds owned by curated checkers (Cluster API today)
// to avoid double-reporting.
//
// When f.Kinds is non-empty (e.g. summaryContext building a per-resource
// issue index for a list_resources call on a single kind), GVRs whose
// kind isn't in the filter are skipped BEFORE the ListDynamic call —
// without this gate, a pods-only request still scanned every watched
// CRD up front and applyFilters discarded the rows afterward. Kind
// comparison mirrors applyFilters: lowercase for case-insensitive
// match against the user's filter (which itself is canonicalized to
// the singular form upstream).
func detectGenericCRDIssues(p Provider, f Filters) []Issue {
	gvrs := p.WatchedDynamic()
	if len(gvrs) == 0 {
		return nil
	}
	wantKind := map[string]bool{}
	for _, k := range f.Kinds {
		wantKind[strings.ToLower(k)] = true
	}
	var out []Issue
	for _, gvr := range gvrs {
		kind := p.KindForGVR(gvr)
		if kind == "" {
			continue
		}
		if isCuratedCRDKind(gvr.Group, kind) {
			continue
		}
		// applyFilters runs after Compose returns — but on hot paths that
		// pin a single kind (summaryContext per-row index), routing the
		// kind filter through here skips the per-GVR ListDynamic call
		// entirely. Match in lowercase (same as applyFilters) so
		// "Pod"/"pod" and CRD-typed "MyResource"/"myresource" both
		// compare equal.
		if len(wantKind) > 0 && !wantKind[strings.ToLower(kind)] {
			continue
		}
		clusterScoped, _, _ := classifyDynamicScope(p, gvr, kind)
		if clusterScoped && len(f.Namespaces) > 0 {
			continue
		}
		if clusterScoped && f.CanReadClusterScoped != nil && !f.CanReadClusterScoped(kind, gvr.Group) {
			continue
		}
		// Gather candidate objects RBAC-safely:
		//  - cluster-scoped CRD → one cluster-wide list (already access-gated above).
		//  - namespaced CRD with an explicit namespace set → list each (the set is
		//    auth-filtered upstream by the handler).
		//  - namespaced CRD with NO namespace set → the caller is cluster-wide
		//    authorized (restricted users always have their set injected), so union
		//    across every watched scope. A plain ListDynamic(gvr,"") would read only
		//    a cluster-wide informer and silently miss namespace-scoped ones.
		var items []*unstructured.Unstructured
		switch {
		case clusterScoped:
			its, err := p.ListDynamic(gvr, "")
			if err != nil {
				log.Printf("[issues] Failed to list %s (%s): %s", logsafe.Sanitize(gvr.Resource), logsafe.Sanitize(gvr.Group), logsafe.Sanitize(err.Error()))
				continue
			}
			items = its
		case len(f.Namespaces) > 0:
			for _, ns := range f.Namespaces {
				its, err := p.ListDynamic(gvr, ns)
				if err != nil {
					log.Printf("[issues] Failed to list %s (%s) in %s: %s", logsafe.Sanitize(gvr.Resource), logsafe.Sanitize(gvr.Group), logsafe.Sanitize(ns), logsafe.Sanitize(err.Error()))
					continue
				}
				items = append(items, its...)
			}
		default:
			its, err := p.ListDynamicAllNamespaces(gvr)
			if err != nil {
				log.Printf("[issues] Failed to list %s (%s) across namespaces: %s", logsafe.Sanitize(gvr.Resource), logsafe.Sanitize(gvr.Group), logsafe.Sanitize(err.Error()))
				continue
			}
			items = its
		}
		for _, u := range items {
			if curated := detectCuratedConditionIssues(gvr, kind, u); len(curated) > 0 {
				out = append(out, curated...)
				continue
			}
			condType, reason, msg, since, ok := conditions.FindFalseCondition(u)
			if !ok {
				continue
			}
			// Noise-floor suppression: a False Ready/Available on an object that
			// is suspended, still reconciling, or whose controller hasn't yet
			// observed the current spec is NOT a failure — it's in-flight.
			// Emitting a warning for it is the canonical alert-fatigue trap,
			// since auto-refresh keeps it permanently lit. Skip those; keep
			// genuinely-failed objects.
			if isTransientCRDCondition(u, reason) {
				continue
			}
			severity := SeverityWarning
			issReason := condTypeReason(condType, reason)
			issMsg := msg
			// Argo Rollout: FindFalseCondition picks Healthy=False/RolloutHealthy
			// first (Healthy precedes Available in the Rollout's condition list),
			// which reads as "healthy" and buries the real cause. When a
			// definitive failure condition is present, surface it as critical.
			if kind == "Rollout" && strings.Contains(strings.ToLower(gvr.Group), "argoproj.io") {
				if r, m, found := argoRolloutFailure(u); found {
					issReason, issMsg, severity = r, m, SeverityCritical
				}
			}
			lastSeen := time.Now().Add(-since)
			iss := Issue{
				Severity:  severity,
				Source:    SourceCondition,
				Kind:      kind,
				Group:     gvr.Group,
				Namespace: u.GetNamespace(),
				Name:      u.GetName(),
				Reason:    issReason,
				Message:   issMsg,
				FirstSeen: lastSeen,
				LastSeen:  lastSeen,
				Count:     1,
			}
			classifyIssue(&iss)
			enrichIdentity(&iss)
			out = append(out, iss)
		}
	}
	return out
}

func detectCuratedConditionIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	switch gvr.Group {
	case "gateway.networking.k8s.io":
		return detectGatewayConditionIssues(gvr, kind, u)
	case "apiregistration.k8s.io":
		if kind == "APIService" {
			return detectObjectConditionIssues(gvr, kind, u, SeverityCritical, "Available")
		}
	case "apiextensions.k8s.io":
		if kind == "CustomResourceDefinition" {
			return detectObjectConditionIssues(gvr, kind, u, SeverityCritical, "Established", "NamesAccepted")
		}
	}
	return nil
}

func detectGatewayConditionIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	switch kind {
	case "GatewayClass", "Gateway":
		return detectObjectConditionIssues(gvr, kind, u, SeverityWarning, "Accepted", "Programmed")
	case "HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute":
		return detectGatewayRouteParentIssues(gvr, kind, u)
	default:
		return nil
	}
}

func detectObjectConditionIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured, severity Severity, condTypes ...string) []Issue {
	condType, reason, msg, since, ok := conditions.FindFalseCondition(u, condTypes...)
	if !ok || isTransientCRDCondition(u, reason) {
		return nil
	}
	return []Issue{newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), severity, condTypeReason(condType, reason), msg, since, "")}
}

func detectGatewayRouteParentIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	parents, found, _ := unstructured.NestedSlice(u.Object, "status", "parents")
	if !found {
		return nil
	}
	var out []Issue
	for _, parent := range parents {
		pm, ok := parent.(map[string]any)
		if !ok {
			continue
		}
		parentLabel, parentKey := gatewayParentRefLabel(pm)
		conds, _ := pm["conditions"].([]any)
		for _, c := range conds {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			condType, _ := cm["type"].(string)
			if condType != "Accepted" && condType != "ResolvedRefs" && condType != "Programmed" {
				continue
			}
			if status, _ := cm["status"].(string); status != "False" {
				continue
			}
			reason, _ := cm["reason"].(string)
			if conditions.IsInProgressForIssues(reason) {
				continue
			}
			msg, _ := cm["message"].(string)
			if parentLabel != "" {
				if msg != "" {
					msg = parentLabel + ": " + msg
				} else {
					msg = parentLabel
				}
			}
			since := conditionSince(cm)
			fp := condType + ":" + parentKey
			out = append(out, newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), SeverityWarning, condTypeReason(condType, reason), msg, since, fp))
		}
	}
	return out
}

func newConditionIssue(gvr schema.GroupVersionResource, kind, namespace, name string, severity Severity, reason, message string, since time.Duration, fingerprint string) Issue {
	lastSeen := time.Now().Add(-since)
	iss := Issue{
		Severity:    severity,
		Source:      SourceCondition,
		Kind:        kind,
		Group:       gvr.Group,
		Namespace:   namespace,
		Name:        name,
		Reason:      reason,
		Message:     message,
		FirstSeen:   lastSeen,
		LastSeen:    lastSeen,
		Count:       1,
		Fingerprint: fingerprint,
	}
	classifyIssue(&iss)
	enrichIdentity(&iss)
	return iss
}

func conditionSince(cond map[string]any) time.Duration {
	ts, _ := cond["lastTransitionTime"].(string)
	if ts == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0
	}
	return time.Since(t)
}

func gatewayParentRefLabel(parent map[string]any) (label, key string) {
	ref, _ := parent["parentRef"].(map[string]any)
	if len(ref) == 0 {
		return "", "unknown"
	}
	group, _ := ref["group"].(string)
	kind, _ := ref["kind"].(string)
	namespace, _ := ref["namespace"].(string)
	name, _ := ref["name"].(string)
	sectionName, _ := ref["sectionName"].(string)
	port := ""
	if p, ok := ref["port"]; ok {
		port = toString(p)
	}
	if group == "" {
		group = "gateway.networking.k8s.io"
	}
	if kind == "" {
		kind = "Gateway"
	}
	parts := []string{group, kind, namespace, name, sectionName, port}
	key = strings.Join(parts, "/")
	displayName := name
	if namespace != "" {
		displayName = namespace + "/" + name
	}
	label = kind + " " + displayName
	if sectionName != "" {
		label += " listener " + sectionName
	}
	if port != "" {
		label += " port " + port
	}
	return label, key
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// isTransientCRDCondition reports whether a False Ready/Available condition on
// a CRD object should be suppressed as in-flight rather than emitted as a
// failure. Three independent signals, any of which means "not a real problem":
//
//  1. The condition reason is in-progress per conditions.IsInProgressForIssues
//     — the shared transient set MINUS the genuine-failure reasons
//     (ArtifactFailed / ChartNotReady): the health badge may soften those to
//     "degraded" (still visible), but the Issues queue must surface them, not
//     drop them. This is the one place the Issues noise-floor deliberately
//     diverges from the health-display transient set.
//  2. spec.suspend == true — the object is intentionally paused (Flux
//     Kustomization/HelmRelease, Argo with suspend, suspended CronJob-style
//     CRDs); a paused object reporting not-Ready is expected.
//  3. status.observedGeneration < metadata.generation — the controller has not
//     yet reconciled the current spec, so the stale condition reflects the old
//     generation, not the live state.
func isTransientCRDCondition(u *unstructured.Unstructured, reason string) bool {
	if conditions.IsInProgressForIssues(reason) {
		return true
	}
	if suspend, ok, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); ok && suspend {
		return true
	}
	// observedGeneration lags generation → controller hasn't caught up yet.
	gen := u.GetGeneration()
	if gen > 0 {
		if observed, ok, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration"); ok && observed > 0 && observed < gen {
			return true
		}
	}
	return false
}

func classifyDynamicScope(p Provider, gvr schema.GroupVersionResource, kind string) (bool, string, string) {
	if sp, ok := p.(dynamicScopeProvider); ok {
		if namespaced, known := sp.NamespacedForGVR(gvr); known {
			return !namespaced, gvr.Group, gvr.Resource
		}
	}
	return k8s.ClassifyKindScope(kind, gvr.Group)
}

// isCuratedCRDKind reports whether a curated detector already owns this
// (group, kind), so the generic CRD fallback must skip it to avoid a
// double-report. CAPI is deliberately kind-specific: the curated detector owns
// core Cluster/Machine/KCP/MHC shapes, while provider CRDs such as AWSMachine
// and bootstrap configs still need the generic condition fallback.
func isCuratedCRDKind(group, kind string) bool {
	switch group {
	case "cluster.x-k8s.io":
		switch kind {
		case "Cluster", "Machine", "MachineDeployment", "MachineHealthCheck":
			return true
		}
	case "controlplane.cluster.x-k8s.io":
		return kind == "KubeadmControlPlane"
	case "argoproj.io":
		return kind == "Application"
	case "kustomize.toolkit.fluxcd.io":
		return kind == "Kustomization"
	case "helm.toolkit.fluxcd.io":
		return kind == "HelmRelease"
	}
	return false
}

// condTypeReason combines the condition type (e.g. "Ready") and the
// optional reason ("CrashLoopBackOff") into one display string. When
// reason is empty, falls back to "<Type>=False".
func condTypeReason(condType, reason string) string {
	if reason != "" {
		return condType + ": " + reason
	}
	return condType + "=False"
}

// argoRolloutFailure returns the definitive failing condition of an Argo
// Rollout, in root-cause-first order: an invalid spec, then a progress-deadline
// stall. Both are unambiguous failures (no in-progress ambiguity), so the
// caller promotes them to critical and uses their reason instead of the generic
// Healthy=False/RolloutHealthy the condition reader would otherwise surface.
// ok=false leaves the generic reason untouched.
func argoRolloutFailure(u *unstructured.Unstructured) (reason, message string, ok bool) {
	conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !found {
		return "", "", false
	}
	cond := func(condType string) (status, reason, message string) {
		for _, c := range conds {
			cm, isMap := c.(map[string]any)
			if !isMap {
				continue
			}
			if ct, _ := cm["type"].(string); ct == condType {
				status, _ = cm["status"].(string)
				reason, _ = cm["reason"].(string)
				message, _ = cm["message"].(string)
				return
			}
		}
		return
	}
	if s, r, m := cond("InvalidSpec"); s == "True" {
		// Match the "CondType: reason" shape every other condition row uses; keep
		// the bare condType when reason is empty or just restates it.
		rolloutReason := "InvalidSpec"
		if r != "" && r != "InvalidSpec" {
			rolloutReason = condTypeReason("InvalidSpec", r)
		}
		return rolloutReason, m, true
	}
	if s, r, m := cond("Progressing"); s == "False" && r == "ProgressDeadlineExceeded" {
		return condTypeReason("Progressing", r), m, true
	}
	return "", "", false
}

// ---------------------------------------------------------------------------
// Source-specific normalization
// ---------------------------------------------------------------------------
