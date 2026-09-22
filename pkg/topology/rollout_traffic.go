package topology

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
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

	// settled is true once nothing is actually in flight: a canary fully
	// promoted (stableRS == currentPodHash) or a blue-green fully promoted
	// with no preview tracked (activeSelector == previewSelector, both
	// non-empty — the Rollout controller hasn't decided a revision yet
	// otherwise, and there's nothing meaningful to classify). While settled,
	// rolloutTrafficRole reports no role at all: a retained old ReplicaSet
	// or a resting canary/stable split has nothing in transit to highlight.
	settled bool
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
	if info.settled || podTemplateHash == "" {
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
// text is built, used for the Service->Rollout and Rollout->ReplicaSet hops
// so the same role always reads identically wherever it's shown. Returns ""
// for a role the switch doesn't recognize (defensive; every caller already
// only invokes this with a value rolloutTrafficRole itself returned).
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

// rolloutTrafficRoleLabel is rolloutTrafficEdgeLabel without the percentage —
// used on the pod-level hop (ReplicaSet->Pod, and the Rollout->Pod shortcut
// that substitutes for it when the ReplicaSet is hidden), where three
// stacked "Stable · 75%" labels on adjacent pods overlap each other.
func rolloutTrafficRoleLabel(role string) string {
	switch role {
	case "canary":
		return "Canary"
	case "stable":
		return "Stable"
	case "active":
		return "Active"
	case "preview":
		return "Preview"
	default:
		return ""
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
