package gitops

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func fluxObj(suspend bool, gen, obs any, conds ...map[string]any) *unstructured.Unstructured {
	raw := make([]any, 0, len(conds))
	for _, c := range conds {
		raw = append(raw, c)
	}
	meta := map[string]any{"name": "k1", "namespace": "flux-system"}
	if gen != nil {
		meta["generation"] = gen
	}
	status := map[string]any{"conditions": raw}
	if obs != nil {
		status["observedGeneration"] = obs
	}
	obj := map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   meta,
		"status":     status,
	}
	if suspend {
		obj["spec"] = map[string]any{"suspend": true}
	}
	return &unstructured.Unstructured{Object: obj}
}

func cond(typ, status string) map[string]any {
	return map[string]any{"type": typ, "status": status}
}

func condGen(c map[string]any, generation int64) map[string]any {
	c["observedGeneration"] = generation
	return c
}

func condReason(typ, status, reason string) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": reason}
}

func TestFluxStatusTruthTable(t *testing.T) {
	cases := []struct {
		name       string
		obj        *unstructured.Unstructured
		wantSync   string
		wantHealth string
	}{
		{
			// The reported bug. Ready and Reconciling are independent upstream, so a
			// stale Reconciling sat on a fully-applied object and outvoted it, while
			// kubectl and k9s read the printer columns and showed it Ready.
			name:     "stale Reconciling does not outvote an applied Ready",
			obj:      fluxObj(false, int64(1), int64(1), cond("Ready", "True"), cond("Healthy", "True"), cond("Reconciling", "True")),
			wantSync: "Synced", wantHealth: "Healthy",
		},
		{
			name:     "ready and applied",
			obj:      fluxObj(false, int64(1), int64(1), cond("Ready", "True")),
			wantSync: "Synced", wantHealth: "Healthy",
		},
		{
			// Generation drift is the only state that genuinely means "not applied",
			// and it holds even with Ready=True from the previous spec.
			name:     "generation drift outranks a stale Ready",
			obj:      fluxObj(false, int64(2), int64(1), cond("Ready", "True")),
			wantSync: "Reconciling", wantHealth: "Progressing",
		},
		{
			// Flux writes -1 on an object it has never reconciled. Present and
			// different, so it counts. (A first reconcile reports Ready=Unknown; a
			// reported failure is covered below and outranks drift.)
			name:     "the -1 sentinel counts as drift",
			obj:      fluxObj(false, int64(1), int64(-1), cond("Ready", "Unknown"), cond("Reconciling", "True")),
			wantSync: "Reconciling", wantHealth: "Progressing",
		},
		{
			// Absence is no signal. Reading it as drift would mark every healthy
			// object on kinds that never set it as out of date.
			name:     "absent observedGeneration is not drift",
			obj:      fluxObj(false, int64(3), nil, cond("Ready", "True")),
			wantSync: "Synced", wantHealth: "Healthy",
		},
		{
			name:     "absent generation is not drift",
			obj:      fluxObj(false, nil, int64(3), cond("Ready", "True")),
			wantSync: "Synced", wantHealth: "Healthy",
		},
		{
			// A behaviour change from the previous ordering, asserted rather than
			// assumed: the controller has given up, whatever the last apply left.
			name:     "Stalled outranks Ready",
			obj:      fluxObj(false, int64(1), int64(1), cond("Ready", "True"), cond("Stalled", "True")),
			wantSync: "OutOfSync", wantHealth: "Degraded",
		},
		{
			name:     "reconciling while not yet ready",
			obj:      fluxObj(false, int64(1), int64(1), cond("Ready", "Unknown"), cond("Reconciling", "True")),
			wantSync: "Reconciling", wantHealth: "Progressing",
		},
		{
			name:     "not ready",
			obj:      fluxObj(false, int64(1), int64(1), cond("Ready", "False")),
			wantSync: "OutOfSync", wantHealth: "Degraded",
		},
		{
			// Applied but the workloads are unhealthy — two different facts, and
			// the sync axis must not absorb the health one.
			name:     "unhealthy deployed resources are applied but degraded",
			obj:      fluxObj(false, int64(1), int64(1), cond("Ready", "True"), cond("Healthy", "False")),
			wantSync: "Synced", wantHealth: "Degraded",
		},
		{
			// Suspending does not un-apply what was applied.
			name:     "suspended and applied stays Synced",
			obj:      fluxObj(true, int64(1), int64(1), cond("Ready", "True")),
			wantSync: "Synced", wantHealth: "Suspended",
		},
		{
			// A spec change made while suspended will never be picked up.
			name:     "suspended with drift is not Synced",
			obj:      fluxObj(true, int64(2), int64(1), cond("Ready", "True")),
			wantSync: "Unknown", wantHealth: "Suspended",
		},
		{
			// A retry or remediation in flight is out of sync but not settled
			// failure. Mirrors the frontend mapper, which has always drawn this
			// distinction — the two must agree.
			name:     "a retrying Ready=False is progressing, not degraded",
			obj:      fluxObj(false, int64(1), int64(1), condReason("Ready", "False", "ProgressingWithRetry")),
			wantSync: "OutOfSync", wantHealth: "Progressing",
		},
		{
			name:     "a settled Ready=False is degraded",
			obj:      fluxObj(false, int64(1), int64(1), condReason("Ready", "False", "BuildFailed")),
			wantSync: "OutOfSync", wantHealth: "Degraded",
		},
		{
			// Live shape of a GitRepository whose repository doesn't exist:
			// never reconciled (-1) and retrying, but the clone failure is settled.
			name: "a failure while retrying and never reconciled is degraded",
			obj: fluxObj(false, int64(1), int64(-1),
				condReason("Reconciling", "True", "ProgressingWithRetry"),
				condGen(condReason("Ready", "False", "GitOperationFailed"), 1)),
			wantSync: "OutOfSync", wantHealth: "Degraded",
		},
		{
			// A source edited to a ref that doesn't exist: the new generation is
			// never recorded while the fetch fails.
			name:     "a failure after a spec change is degraded",
			obj:      fluxObj(false, int64(2), int64(1), condGen(condReason("Ready", "False", "ArtifactFailed"), 2)),
			wantSync: "OutOfSync", wantHealth: "Degraded",
		},
		{
			// The spec was fixed (generation 3); the failure still describes
			// generation 2, so it says nothing about the new spec yet.
			name:     "a failure about an older generation does not outrank a new spec",
			obj:      fluxObj(false, int64(3), int64(1), condGen(condReason("Ready", "False", "BuildFailed"), 2)),
			wantSync: "Reconciling", wantHealth: "Progressing",
		},
		{
			name:     "a failure without a condition generation does not outrank drift",
			obj:      fluxObj(false, int64(2), int64(1), condReason("Ready", "False", "BuildFailed")),
			wantSync: "Reconciling", wantHealth: "Progressing",
		},
		{
			name:     "a retry while reconciling stays progressing",
			obj:      fluxObj(false, int64(2), int64(1), condReason("Reconciling", "True", "Progressing"), condReason("Ready", "False", "ProgressingWithRetry")),
			wantSync: "Reconciling", wantHealth: "Progressing",
		},
		{
			name:     "no conditions at all",
			obj:      fluxObj(false, int64(1), int64(1)),
			wantSync: "Unknown", wantHealth: "Unknown",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FluxStatus(tc.obj)
			if got.Sync != tc.wantSync || got.Health != tc.wantHealth {
				t.Errorf("FluxStatus = %s/%s, want %s/%s", got.Sync, got.Health, tc.wantSync, tc.wantHealth)
			}
		})
	}
}

// The in-flight fact survives even when it no longer drives the status, so a
// wedged controller can be surfaced next to an otherwise-correct Synced.
func TestFluxStatusCarriesReconcilingActivity(t *testing.T) {
	stuck := fluxObj(false, int64(1), int64(1), cond("Ready", "True"), cond("Reconciling", "True"))
	got := FluxStatus(stuck)
	if got.Sync != "Synced" {
		t.Fatalf("Sync = %q, want Synced", got.Sync)
	}
	if !got.Reconciling {
		t.Error("Reconciling activity was dropped; a wedged controller would be invisible")
	}

	quiet := FluxStatus(fluxObj(false, int64(1), int64(1), cond("Ready", "True")))
	if quiet.Reconciling {
		t.Error("Reconciling reported on an object with no Reconciling condition")
	}
}

// Source kinds carry neither Healthy nor, in some versions, observedGeneration.
// Measured shapes: GitRepository/HelmRepository expose Ready + ArtifactInStorage,
// HelmRelease exposes Ready + Released.
func TestFluxStatusHandlesNonKustomizationKinds(t *testing.T) {
	cases := []struct {
		name  string
		conds []map[string]any
	}{
		{"GitRepository", []map[string]any{cond("Ready", "True"), cond("ArtifactInStorage", "True")}},
		{"HelmRepository", []map[string]any{cond("Ready", "True"), cond("ArtifactInStorage", "True")}},
		{"HelmRelease", []map[string]any{cond("Ready", "True"), cond("Released", "True")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FluxStatus(fluxObj(false, int64(1), int64(1), tc.conds...))
			if got.Sync != "Synced" || got.Health != "Healthy" {
				t.Errorf("FluxStatus = %s/%s, want Synced/Healthy", got.Sync, got.Health)
			}
		})
	}
}
