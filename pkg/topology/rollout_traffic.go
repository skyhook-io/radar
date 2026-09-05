package topology

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// rolloutPodTemplateHashLabel mirrors pkg/rollouts.PodTemplateHashLabel — kept
// as a local string literal rather than an import, matching how every other
// owner/grouping label in this package (workflows.argoproj.io/workflow,
// batch.kubernetes.io/job-name, app.kubernetes.io/name, ...) is already an
// inline literal rather than a cross-package constant.
const rolloutPodTemplateHashLabel = "rollouts-pod-template-hash"

// rolloutTrafficInfo is a Rollout's live traffic-routing state, read once
// while building the Rollout's own node and reused when matching Services
// and classifying its Pods/ReplicaSets by role (canary/stable/active/preview).
type rolloutTrafficInfo struct {
	// currentPodHash/stableRS classify a canary Rollout's revisions.
	// stableRS is checked first (see rolloutTrafficRole) so a fully-promoted
	// Rollout — where the two coincide — reads as "stable", not "canary".
	currentPodHash string
	stableRS       string
	// activeSelector/previewSelector classify a blueGreen Rollout's revisions.
	activeSelector  string
	previewSelector string

	canaryService  string
	stableService  string
	activeService  string
	previewService string

	// nil means the Rollout hasn't reported a weight yet (e.g. before the
	// first canary step sets one) — distinct from a real 0%.
	canaryWeight *int64
	stableWeight *int64
}

// rolloutTrafficRole classifies a pod-template-hash value against a Rollout's
// live status pointers. Returns "" when the hash matches none of them (e.g.
// an old, no-longer-relevant revision, or the Rollout has no status yet).
//
// activeSelector/previewSelector are checked first, not stableRS/
// currentPodHash — those two are generic, strategy-agnostic status fields
// the Rollout controller maintains for EVERY Rollout (canary or blueGreen),
// so a blueGreen Rollout's ReplicaSets/Pods have real, non-empty values
// there too. Checking them first would misclassify blueGreen revisions as
// canary/stable. activeSelector/previewSelector live under status.blueGreen,
// which is only ever populated for a blueGreen-strategy Rollout — a canary
// Rollout's pod-template-hash can never coincidentally match either (both
// are empty strings there, and podTemplateHash is never empty, already
// guarded above), so checking them first is safe for canary too.
func rolloutTrafficRole(podTemplateHash string, info rolloutTrafficInfo) string {
	if podTemplateHash == "" {
		return ""
	}
	switch podTemplateHash {
	case info.activeSelector:
		return "active"
	case info.previewSelector:
		return "preview"
	case info.stableRS:
		return "stable"
	case info.currentPodHash:
		return "canary"
	default:
		return ""
	}
}

// rolloutTrafficEdgeLabel builds the "Canary · 20%" / "Stable · 80%" /
// "Active" / "Preview" edge label for a given role — the single place this
// text is built, used for every edge along the traffic path (Service->
// Rollout, Rollout->ReplicaSet, ReplicaSet->Pod) so the same role always
// reads identically no matter which hop it's labeling. Returns "" for a
// role the switch doesn't recognize (defensive; every caller already only
// invokes this with a value rolloutTrafficRole itself returned).
func rolloutTrafficEdgeLabel(role string, info rolloutTrafficInfo) string {
	switch role {
	case "canary":
		if info.canaryWeight != nil {
			return fmt.Sprintf("Canary · %d%%", *info.canaryWeight)
		}
		return "Canary"
	case "stable":
		if info.stableWeight != nil {
			return fmt.Sprintf("Stable · %d%%", *info.stableWeight)
		}
		return "Stable"
	case "active":
		return "Active"
	case "preview":
		return "Preview"
	default:
		return ""
	}
}

// canaryStepWeight derives the canary/stable split from the step definition
// itself when status.canary.weights is absent — which, confirmed live on the
// demo cluster, is EVERY canary Rollout that has no trafficRouting plugin
// configured (Istio/SMI/ALB/NGINX). That's the common "basic canary" case:
// the controller drives the split purely by scaling the canary ReplicaSet's
// replica count to approximate the last setWeight step reached, and never
// writes status.canary.weights at all — only a service-mesh-routed Rollout
// gets that field, since only then can the live routed percentage actually
// differ from the replica ratio. Without this fallback, a basic-canary
// Rollout's traffic edges showed a bare "Canary"/"Stable" role with no
// percentage for the entire time it was progressing.
//
// Walks backward from currentStepIndex to the most recent setWeight step —
// intervening pause/analysis/experiment steps don't change the target ratio,
// so the last setWeight passed is still the one in effect. Returns (nil, nil)
// when currentStepIndex is past the end of steps (fully promoted — no
// active canary split to report; the settled "Stable" label correctly shows
// no percentage, implying 100%) or when no setWeight has been reached yet.
func canaryStepWeight(spec, status map[string]any) (canaryWeight, stableWeight *int64) {
	steps, _, _ := unstructured.NestedSlice(spec, "strategy", "canary", "steps")
	stepIdx, ok, _ := unstructured.NestedInt64(status, "currentStepIndex")
	if !ok || len(steps) == 0 || stepIdx < 0 || stepIdx >= int64(len(steps)) {
		return nil, nil
	}
	for i := stepIdx; i >= 0; i-- {
		step, ok := steps[i].(map[string]any)
		if !ok {
			continue
		}
		raw, ok := step["setWeight"]
		if !ok {
			continue
		}
		w, ok := toInt64Value(raw)
		if !ok {
			continue
		}
		stable := int64(100) - w
		return &w, &stable
	}
	return nil, nil
}

// toInt64Value normalizes a JSON-decoded numeric value (unstructured objects
// decode integers as int64, but a step read back out of a slice literal in
// tests may arrive as a plain int) to int64.
func toInt64Value(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

// podRolloutTrafficRole resolves a Pod's traffic role via its owning
// ReplicaSet — nil/"" when the pod isn't owned by a Rollout-owned ReplicaSet,
// or the owning Rollout has no traffic info recorded.
func podRolloutTrafficRole(pod *corev1.Pod, replicaSetToRollout map[string]string, rolloutTrafficByID map[string]rolloutTrafficInfo) string {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind != "ReplicaSet" {
			continue
		}
		rolloutID, ok := replicaSetToRollout[pod.Namespace+"/"+ref.Name]
		if !ok {
			continue
		}
		info, ok := rolloutTrafficByID[rolloutID]
		if !ok {
			continue
		}
		return rolloutTrafficRole(pod.Labels[rolloutPodTemplateHashLabel], info)
	}
	return ""
}
