// Package conditions holds the neutral controller-condition-state vocabulary
// shared across the platform: which condition reasons mean "still reconciling"
// (transient) vs. which look transient but are genuine stuck failures. It is a
// dependency-free leaf — package inventory (pkg/packages), the GitOps detector
// (internal/k8s), and the issues classifier (internal/issues) all depend on it
// rather than on each other, so the two interpretations can't drift.
package conditions

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DefaultFalseConditionTypes is the set of "is this resource healthy?" condition
// types the generic CRD fallback flags when False. Order matters only for
// tiebreaking when multiple are False — first hit wins, biasing toward the
// most-fundamental signal. Curated across the integration matrix: Argo
// (Synced/Healthy), Flux (Ready/Reconciled/Released), cert-manager/Knative/KEDA/
// Crossplane/CNPG (Ready/Synced/Active/…), Cluster API (Ready/…, though CAPI has
// its own curated checker).
var DefaultFalseConditionTypes = []string{
	"Ready", "Available", "Reconciled", "Healthy", "Synced", "Released",
}

// FindFalseCondition walks an unstructured object's status.conditions (with a
// status.v1beta2.conditions fallback) and returns the first entry whose type is
// in condTypes (or DefaultFalseConditionTypes when none given) AND whose status
// is "False". Returns the type, reason, message, age-since-lastTransitionTime,
// and whether one was found. The single shared reader for every condition-state
// consumer (issues generic fallback, the CAPI detector, the Flux detector) so
// they can't drift.
func FindFalseCondition(obj *unstructured.Unstructured, condTypes ...string) (condType, reason, message string, since time.Duration, found bool) {
	if obj == nil {
		return "", "", "", 0, false
	}
	if len(condTypes) == 0 {
		condTypes = DefaultFalseConditionTypes
	}
	now := time.Now()
	condSlices := [][]any{}
	if v1b2, ok, _ := unstructured.NestedSlice(obj.Object, "status", "v1beta2", "conditions"); ok {
		condSlices = append(condSlices, v1b2)
	}
	if v1b1, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions"); ok {
		condSlices = append(condSlices, v1b1)
	}
	for _, conds := range condSlices {
		for _, c := range conds {
			cond, ok := c.(map[string]any)
			if !ok {
				continue
			}
			ct, _ := cond["type"].(string)
			if status, _ := cond["status"].(string); status != "False" {
				continue
			}
			for _, wanted := range condTypes {
				if ct == wanted {
					r, _ := cond["reason"].(string)
					m, _ := cond["message"].(string)
					var dur time.Duration
					if ts, _ := cond["lastTransitionTime"].(string); ts != "" {
						if t, err := time.Parse(time.RFC3339, ts); err == nil {
							dur = now.Sub(t)
						}
					}
					return ct, r, m, dur, true
				}
			}
		}
	}
	return "", "", "", 0, false
}

// transientConditionReasons is the canonical set of controller condition reasons
// that mean "still reconciling / not done yet", NOT "failed". A False
// Ready/Available condition carrying one of these is in-progress, not broken —
// this is the CRD-condition noise floor. Curated across the controller families
// Radar integrates with:
//
//   - Flux:         Progressing, DependencyNotReady, ReconciliationInProgress,
//     ChartNotReady, ArtifactFailed
//   - Argo/Crossplane: Reconciling, Creating
//   - cert-manager: Issuing, Pending
//   - generic:      InProgress, Initializing, Waiting (NOT "Unknown" — ambiguous,
//     not in-progress; it stays loud/unhealthy)
var transientConditionReasons = map[string]bool{
	"Progressing":              true,
	"DependencyNotReady":       true,
	"ReconciliationInProgress": true,
	"ChartNotReady":            true,
	"ArtifactFailed":           true,
	"Reconciling":              true,
	"Creating":                 true,
	"Issuing":                  true,
	"Pending":                  true,
	"InProgress":               true,
	"Initializing":             true,
	"Waiting":                  true,
}

// genuineFailureReason holds reasons that APPEAR in transientConditionReasons
// (the health-display path softens them to "degraded", still visible) but are
// actually persistent stuck failures the live issue queue must surface, never
// suppress: a Flux source reporting ArtifactFailed can't produce an artifact;
// ChartNotReady can't resolve a chart. The issue detectors subtract these from
// the transient set; the health badge may keep them transient.
var genuineFailureReason = map[string]bool{
	"ArtifactFailed": true,
	"ChartNotReady":  true,
}

// IsTransientConditionReason reports whether a reason denotes an in-progress /
// not-yet-settled state rather than a genuine failure. Used by the GitOps
// health mapping and (minus the genuine-failure carve-out) the issue detectors.
func IsTransientConditionReason(r string) bool {
	return transientConditionReasons[r]
}

// IsGenuineFailureReason reports whether a reason is a stuck failure that looks
// transient but must NOT be suppressed from the live issue stream.
func IsGenuineFailureReason(r string) bool {
	return genuineFailureReason[r]
}

// IsInProgressForIssues is the NARROW "still reconciling" predicate the issue
// detectors use: transient MINUS the genuine-failure reasons, so ArtifactFailed/
// ChartNotReady surface as issues instead of being softened away.
func IsInProgressForIssues(r string) bool {
	return transientConditionReasons[r] && !genuineFailureReason[r]
}
