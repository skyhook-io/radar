package gitops

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FluxState is the sync/health pair derived from a Flux object's conditions.
type FluxState struct {
	Sync   string
	Health string
	// Reconciling reports that a pass is executing right now. Activity, not
	// sync and not health — it never influences either. Carried so a wedged
	// controller can be surfaced next to an otherwise-correct Synced.
	Reconciling bool
}

// FluxStatus derives sync and health for any Flux object.
//
// Sync answers "is the declared state applied", which is Ready plus
// observedGeneration. Reconciling answers "is the controller mid-pass", which
// is neither: upstream the two are independent conditions (MarkReconciling
// deletes only Stalled, never Ready), so letting activity outvote readiness
// reported a healthy, fully-applied object as not synced — while kubectl and
// k9s, which read the CRD's printer columns, showed it Ready.
//
// Order:
//   - suspend                                   → Suspended (sync from Ready, unless drifted)
//   - Stalled=True                              → OutOfSync / Degraded
//   - Ready=True and no generation drift        → Synced / Healthy
//   - Ready=False with a failure reason about
//     the current generation                    → OutOfSync / Degraded
//   - drift, or Reconciling with Ready not True → Reconciling / Progressing
//   - Ready=False                               → OutOfSync / Degraded, or
//     Progressing for a retry/wait reason
//
// Shared by the insights and tree packages, which previously carried
// near-duplicate copies that had already drifted apart on suspend handling.
func FluxStatus(root *unstructured.Unstructured) FluxState {
	var ready, readyReason, healthy string
	var reconciling, stalled bool
	var readyGeneration int64
	var readyGenerationKnown bool

	for _, c := range fluxConditions(root) {
		switch c.typ {
		case "Ready":
			ready = c.status
			readyReason = c.reason
			readyGeneration, readyGenerationKnown = c.observedGeneration, c.observedGenerationKnown
		case "Healthy":
			healthy = c.status
		case "Reconciling":
			reconciling = reconciling || c.status == "True"
		case "Stalled":
			stalled = stalled || c.status == "True"
		}
	}

	drifted := fluxGenerationDrift(root)

	// Suspending does not un-apply what was applied, so a suspended object that
	// was Ready stays Synced. A spec change made while suspended will never be
	// picked up, though, so drift downgrades it.
	if suspended, _, _ := unstructured.NestedBool(root.Object, "spec", "suspend"); suspended {
		sync := "Unknown"
		if ready == "True" && !drifted {
			sync = "Synced"
		}
		return FluxState{Sync: sync, Health: "Suspended", Reconciling: reconciling}
	}

	// Terminal failure outranks readiness: the controller has given up,
	// whatever the last successful apply left behind.
	if stalled {
		return FluxState{Sync: "OutOfSync", Health: "Degraded", Reconciling: reconciling}
	}

	// Applied. Health comes from the Healthy condition where the kind has one —
	// the workloads can be unhealthy while the manifests are correctly applied,
	// and those are different facts. Source kinds carry no Healthy condition, so
	// absence means healthy rather than unknown.
	if ready == "True" && !drifted {
		health := "Healthy"
		if healthy == "False" {
			health = "Degraded"
		}
		return FluxState{Sync: "Synced", Health: health, Reconciling: reconciling}
	}

	// A failure the controller reported outranks activity too. A source that
	// can't clone, or a release whose chart can't resolve, keeps Reconciling=True
	// and never records observedGeneration while it retries; reading that as
	// "in progress" hid failures that never resolve on their own.
	// Only a failure about the current spec counts: after a fix, the old
	// Ready=False describes an older generation until the controller looks again.
	generation, _, _ := unstructured.NestedInt64(root.Object, "metadata", "generation")
	if ready == "False" && !isTransientFluxReason(readyReason) && readyGenerationKnown && readyGeneration == generation {
		return FluxState{Sync: "OutOfSync", Health: "Degraded", Reconciling: reconciling}
	}

	// Genuinely not applied: the controller has not observed the current spec,
	// or it is working and has not reached Ready.
	if drifted || reconciling {
		return FluxState{Sync: "Reconciling", Health: "Progressing", Reconciling: reconciling}
	}

	if ready == "False" {
		// Flux distinguishes a retry or remediation in flight (ProgressingWithRetry,
		// and the Wait/Retry reasons) from a settled failure. Both are out of sync,
		// but calling a retry Degraded overstates it.
		health := "Degraded"
		if isTransientFluxReason(readyReason) {
			health = "Progressing"
		}
		return FluxState{Sync: "OutOfSync", Health: health, Reconciling: reconciling}
	}

	return FluxState{Sync: "Unknown", Health: "Unknown", Reconciling: reconciling}
}

// fluxGenerationDrift reports that the controller has demonstrably not acted on
// the current spec.
//
// An absent observedGeneration is no signal, not drift: some Flux kinds and
// older controllers never set it, and reading absence as drift would mark every
// healthy object on them as out of date. A present-but-different value counts,
// including the -1 sentinel Flux writes on an object it has never reconciled.
func fluxGenerationDrift(root *unstructured.Unstructured) bool {
	gen, genOK, _ := unstructured.NestedInt64(root.Object, "metadata", "generation")
	obs, obsOK, _ := unstructured.NestedInt64(root.Object, "status", "observedGeneration")
	if !genOK || !obsOK {
		return false
	}
	return gen != obs
}

// isTransientFluxReason mirrors the frontend mapper's substring test so the two
// implementations agree on this row. Substring rather than an enumeration
// because Flux composes reasons (ProgressingWithRetry, HealthCheckFailed after
// a wait) and the set is not stable across controller versions.
func isTransientFluxReason(reason string) bool {
	for _, marker := range []string{"Progress", "Retry", "Wait"} {
		if strings.Contains(reason, marker) {
			return true
		}
	}
	return false
}

type fluxCondition struct {
	typ                     string
	status                  string
	reason                  string
	observedGeneration      int64
	observedGenerationKnown bool
}

func fluxConditions(root *unstructured.Unstructured) []fluxCondition {
	raw, ok, _ := unstructured.NestedSlice(root.Object, "status", "conditions")
	if !ok {
		return nil
	}
	out := make([]fluxCondition, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		c := fluxCondition{
			typ:    StringValue(m["type"]),
			status: StringValue(m["status"]),
			reason: StringValue(m["reason"]),
		}
		switch g := m["observedGeneration"].(type) {
		case int64:
			c.observedGeneration, c.observedGenerationKnown = g, true
		case int:
			c.observedGeneration, c.observedGenerationKnown = int64(g), true
		case float64:
			c.observedGeneration, c.observedGenerationKnown = int64(g), true
		}
		out = append(out, c)
	}
	return out
}
