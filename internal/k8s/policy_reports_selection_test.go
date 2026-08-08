package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	wgReports        = schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"}
	wgReportsBeta    = schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1beta1", Resource: "policyreports"}
	wgClusterReports = schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"}
	orReports        = schema.GroupVersionResource{Group: "openreports.io", Version: "v1alpha1", Resource: "reports"}
	orClusterReports = schema.GroupVersionResource{Group: "openreports.io", Version: "v1alpha1", Resource: "clusterreports"}
)

// servedSet builds a `served` predicate from an explicit GVR list.
func servedSet(gvrs ...schema.GroupVersionResource) func(schema.GroupVersionResource) bool {
	set := make(map[schema.GroupVersionResource]bool, len(gvrs))
	for _, g := range gvrs {
		set[g] = true
	}
	return func(g schema.GroupVersionResource) bool { return set[g] }
}

// counts builds a `probeCount` from an explicit map; anything absent is 0.
func counts(m map[schema.GroupVersionResource]int) func(schema.GroupVersionResource) int {
	return func(g schema.GroupVersionResource) int { return m[g] }
}

func hasGVR(sel reportSelection, want schema.GroupVersionResource) bool {
	for _, g := range sel.gvrs {
		if g == want {
			return true
		}
	}
	return false
}

// The regression this selection logic exists for, reproduced exactly as seen
// on a live cluster: enabling Kyverno's openreports output leaves the
// wgpolicyk8s.io CRDs served but empty while all 36 reports land in
// openreports.io. First-served-wins picks the empty API and reports zero
// findings — indistinguishable from a clean cluster.
func TestSelectReportGVRs_PrefersPopulatedGroupOverServedButEmpty(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, wgClusterReports, orReports, orClusterReports),
		counts(map[schema.GroupVersionResource]int{orReports: 36}),
	)

	if !hasGVR(sel, orReports) {
		t.Fatalf("expected the populated openreports.io GVR to be watched, got %v", sel.gvrs)
	}
	if hasGVR(sel, wgReports) {
		t.Errorf("served-but-empty wgpolicyk8s.io must not be watched, got %v", sel.gvrs)
	}
	if sel.total != 36 {
		t.Errorf("total = %d, want 36", sel.total)
	}
	if len(sel.groups) != 1 || sel.groups[0] != "openreports.io" {
		t.Errorf("groups = %v, want [openreports.io]", sel.groups)
	}
}

// The mirror image: a cluster that has not enabled openreports but has the
// CRDs installed (the openreports chart ships them independently).
func TestSelectReportGVRs_PrefersWgpolicyWhenOpenreportsIsEmpty(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, wgClusterReports, orReports, orClusterReports),
		counts(map[schema.GroupVersionResource]int{wgReports: 12, wgClusterReports: 3}),
	)

	if !hasGVR(sel, wgReports) {
		t.Fatalf("expected wgpolicyk8s.io to be watched, got %v", sel.gvrs)
	}
	if hasGVR(sel, orReports) {
		t.Errorf("empty openreports.io must not be watched, got %v", sel.gvrs)
	}
	if sel.total != 15 {
		t.Errorf("total = %d, want 15", sel.total)
	}
}

// Mid-migration both families can hold data — stale reports linger in the old
// one. Watching only one would hide real findings; identical findings are
// deduplicated when the index is built.
func TestSelectReportGVRs_WatchesBothWhenBothHoldData(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, orReports),
		counts(map[schema.GroupVersionResource]int{wgReports: 5, orReports: 7}),
	)

	if !hasGVR(sel, wgReports) || !hasGVR(sel, orReports) {
		t.Fatalf("expected both populated groups to be watched, got %v", sel.gvrs)
	}
	if sel.total != 12 {
		t.Errorf("total = %d, want 12", sel.total)
	}
	if len(sel.groups) != 2 {
		t.Errorf("groups = %v, want both", sel.groups)
	}
}

// Within one group the versions are the same resource; watching two would
// double-count every finding.
func TestSelectReportGVRs_WatchesOneVersionPerGroup(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, wgReportsBeta),
		counts(map[schema.GroupVersionResource]int{wgReports: 4, wgReportsBeta: 4}),
	)

	if hasGVR(sel, wgReportsBeta) {
		t.Errorf("only the preferred version should be watched, got %v", sel.gvrs)
	}
	if sel.total != 4 {
		t.Errorf("total = %d, want 4 (not double-counted)", sel.total)
	}
}

// A genuinely empty cluster must still end up watching something, so the
// result reads "ready, no findings" rather than "not installed".
func TestSelectReportGVRs_FallsBackToServedGroupWhenNothingHasData(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, wgClusterReports),
		counts(nil),
	)

	if len(sel.gvrs) == 0 {
		t.Fatal("expected a fallback watch on the served group, got none")
	}
	if sel.total != 0 {
		t.Errorf("total = %d, want 0", sel.total)
	}
	if sel.reason != "" {
		t.Errorf("reason = %q, want empty (this is a legitimately empty cluster)", sel.reason)
	}
}

// "We were blocked from looking" must never be reported as "there is nothing
// to see" — the distinction the reason codes exist for.
func TestSelectReportGVRs_ReportsRBACDenialDistinctly(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports),
		counts(map[schema.GroupVersionResource]int{wgReports: -1}),
	)

	if len(sel.gvrs) != 0 {
		t.Errorf("must not watch a GVR whose cost could not be bounded, got %v", sel.gvrs)
	}
	if sel.reason != ReasonRBACDenied {
		t.Errorf("reason = %q, want %q", sel.reason, ReasonRBACDenied)
	}
	if sel.total != -1 {
		t.Errorf("total = %d, want -1 (unknown, not zero)", sel.total)
	}
}

func TestSelectReportGVRs_ReportsProbeFailureDistinctly(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports),
		counts(map[schema.GroupVersionResource]int{wgReports: -2}),
	)

	if sel.reason != ReasonProbeFailed {
		t.Errorf("reason = %q, want %q", sel.reason, ReasonProbeFailed)
	}
}

// A denial in one group must not hide another group that is readable and
// populated.
func TestSelectReportGVRs_DeniedGroupDoesNotMaskAReadableOne(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, orReports),
		counts(map[schema.GroupVersionResource]int{wgReports: -1, orReports: 9}),
	)

	if !hasGVR(sel, orReports) {
		t.Fatalf("expected the readable populated group to be watched, got %v", sel.gvrs)
	}
	if sel.total != 9 {
		t.Errorf("total = %d, want 9", sel.total)
	}
}

func TestSelectReportGVRs_NoReportCRDsServed(t *testing.T) {
	sel := selectReportGVRs(servedSet(), counts(nil))

	if len(sel.gvrs) != 0 {
		t.Errorf("expected nothing watched, got %v", sel.gvrs)
	}
	if sel.reason != ReasonNoReportCRDs {
		t.Errorf("reason = %q, want %q", sel.reason, ReasonNoReportCRDs)
	}
}

func TestPolicyReportStatusOmittedReason(t *testing.T) {
	tests := []struct {
		name       string
		status     PolicyReportStatus
		wantReason string
		wantOK     bool
	}{
		// Findings are being served — nothing to disclaim.
		{"ready", PolicyReportStatus{Status: KyvernoStatusReady}, "", false},
		// No Kyverno at all: an omitted note on every resource of every
		// non-Kyverno cluster would be noise, not honesty.
		{"not installed stays silent", PolicyReportStatus{Status: KyvernoStatusNotInstalled}, "", false},
		{"warmup is cache cold", PolicyReportStatus{Status: KyvernoStatusWarmup}, "cache_cold", true},
		{"over cap is budget exceeded", PolicyReportStatus{Status: KyvernoStatusDeferred, ReasonCode: ReasonOverCap}, "budget_exceeded", true},
		{"rbac denial is reported as such", PolicyReportStatus{Status: KyvernoStatusDeferred, ReasonCode: ReasonRBACDenied}, "rbac_denied", true},
		{"probe failure falls back to cache cold", PolicyReportStatus{Status: KyvernoStatusDeferred, ReasonCode: ReasonProbeFailed}, "cache_cold", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, ok := tt.status.OmittedReason()
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if string(reason) != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

// Bugbot #2 (HIGH): a denial on one sibling must not discard the other.
// Namespace-scoped RBAC routinely grants `list policyreports` in the user's
// namespaces while denying cluster-scoped `clusterpolicyreports`; the old
// per-family probeFailed flag dropped the whole family and lost every finding
// the user could legitimately see.
func TestSelectReportGVRs_DeniedSiblingKeepsReadableOne(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, wgClusterReports),
		counts(map[schema.GroupVersionResource]int{wgReports: 50, wgClusterReports: -1}),
	)

	if !hasGVR(sel, wgReports) {
		t.Fatalf("readable namespaced GVR was discarded because its cluster-scoped sibling was denied: %v", sel.gvrs)
	}
	if hasGVR(sel, wgClusterReports) {
		t.Errorf("denied GVR must not be watched, got %v", sel.gvrs)
	}
	if sel.total != 50 {
		t.Errorf("total = %d, want 50", sel.total)
	}
}

// A readable-but-empty sibling stays watched when the family has data, so a
// report created later still shows up live.
func TestSelectReportGVRs_KeepsEmptyReadableSibling(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, wgClusterReports),
		counts(map[schema.GroupVersionResource]int{wgReports: 12, wgClusterReports: 0}),
	)

	if !hasGVR(sel, wgClusterReports) {
		t.Errorf("empty but readable sibling should stay watched, got %v", sel.gvrs)
	}
	if sel.total != 12 {
		t.Errorf("total = %d, want 12", sel.total)
	}
}

// A probe error (not a denial) on one sibling is handled the same way.
func TestSelectReportGVRs_ProbeErrorOnSiblingKeepsReadableOne(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, wgClusterReports),
		counts(map[schema.GroupVersionResource]int{wgReports: 7, wgClusterReports: -2}),
	)

	if !hasGVR(sel, wgReports) || hasGVR(sel, wgClusterReports) {
		t.Fatalf("expected only the readable GVR to be watched, got %v", sel.gvrs)
	}
	if sel.total != 7 {
		t.Errorf("total = %d, want 7", sel.total)
	}
}

// Codex #1 (HIGH): when every served family is legitimately empty — an
// openreports-enabled cluster that hasn't produced a violation yet — locking
// onto the first served family leaves the group Kyverno is about to write to
// unwatched. Reports would then appear in the cluster and never in Radar,
// with the status still claiming `ready`.
func TestSelectReportGVRs_AllEmptyWatchesEveryReadableFamily(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, wgClusterReports, orReports, orClusterReports),
		counts(nil),
	)

	for _, want := range []schema.GroupVersionResource{wgReports, orReports} {
		if !hasGVR(sel, want) {
			t.Errorf("all-empty fallback must watch %v, got %v", want, sel.gvrs)
		}
	}
	if len(sel.groups) != 2 {
		t.Errorf("groups = %v, want both families", sel.groups)
	}
	if sel.total != 0 {
		t.Errorf("total = %d, want 0", sel.total)
	}
}

// Codex #3 (MEDIUM): watching what we can read is right, but publishing it as
// unqualified coverage is not — the denied family has to be nameable.
func TestSelectReportGVRs_RecordsDeniedGroupsAlongsideAPartialWatch(t *testing.T) {
	sel := selectReportGVRs(
		servedSet(wgReports, orReports),
		counts(map[schema.GroupVersionResource]int{wgReports: -1, orReports: 9}),
	)

	if !hasGVR(sel, orReports) {
		t.Fatalf("readable populated family must still be watched, got %v", sel.gvrs)
	}
	if len(sel.deniedGroups) != 1 || sel.deniedGroups[0] != "wgpolicyk8s.io" {
		t.Errorf("deniedGroups = %v, want [wgpolicyk8s.io]", sel.deniedGroups)
	}
}
