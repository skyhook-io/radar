package mcp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/upgrade"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

func upgradeScanFixture() *upgradereadiness.ScanResults {
	findings := make([]upgradereadiness.Finding, 0, 30)
	levels := []upgradereadiness.Level{upgradereadiness.LevelReview, upgradereadiness.LevelWarning, upgradereadiness.LevelBlocker}
	for i := range 30 {
		findings = append(findings, upgradereadiness.Finding{
			Title: fmt.Sprintf("finding-%02d", i),
			Level: levels[i%3],
			Evidence: upgradereadiness.Evidence{
				Source: "Helm",
				Path:   fmt.Sprintf("spec.path.%d", i),
			},
			Impact:      "impact",
			Remediation: "remediation",
			References: []upgradereadiness.Reference{
				{Title: "first", URL: "https://example.com/first"},
				{Title: "second", URL: "https://example.com/second"},
			},
		})
	}
	return &upgradereadiness.ScanResults{
		CurrentVersion:  "1.33",
		TargetVersion:   "1.34",
		ReviewedThrough: "1.36",
		Verdict:         upgradereadiness.VerdictBlocked,
		Summary:         upgradereadiness.Summary{Blocked: 1, Passed: 1, Unknown: 2, Findings: 30},
		Coverage: upgradereadiness.Coverage{
			Source:           "live",
			State:            "partial",
			ScopedNamespaces: []string{"team-a"},
			ScopedKinds:      map[string][]string{"pods": {"team-a", "team-b"}},
		},
		Checks: []upgradereadiness.Check{
			{
				ID: "passed-metrics", Category: "APIs", Title: "Deprecated API requests",
				Status:       upgradereadiness.CheckPassed,
				Summary:      "No deprecated API requests observed.",
				EvidenceNote: "API usage metrics cover one API server process; they are not cluster-wide history.",
			},
			{
				ID: "blocked-drain", Category: "Upgrade operations", Title: "Node drain feasibility",
				Status:   upgradereadiness.CheckBlocked,
				Summary:  "2 workloads block drains.",
				Scope:    "Live pods and PodDisruptionBudget status",
				Findings: findings,
			},
			{
				ID: "unknown-runtime", Category: "Nodes", Title: "Container runtime support",
				Status: upgradereadiness.CheckUnknown,
				Caveat: "kubelet metrics unavailable for 3 nodes",
			},
		},
	}
}

func upgradeFixtureOutcome() upgrade.ScanOutcome {
	return upgrade.ScanOutcome{
		Results:    upgradeScanFixture(),
		ObservedAt: time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC),
		ScanID:     "sc_fixture",
		FromCache:  true,
	}
}

func TestShapeUpgradeReadinessTier1(t *testing.T) {
	out, err := shapeUpgradeReadiness(upgradeFixtureOutcome(), upgradeReadinessInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.ScanID != "sc_fixture" || out.ObservedAt.IsZero() || !out.FromCache {
		t.Fatalf("freshness fields = scanId=%q observedAt=%v fromCache=%v — staleness must stay visible", out.ScanID, out.ObservedAt, out.FromCache)
	}
	if out.Verdict != upgradereadiness.VerdictBlocked || out.Summary == nil || out.Summary.Unknown != 2 {
		t.Fatalf("verdict/summary = %v/%+v, want blocked with unknown=2 preserved", out.Verdict, out.Summary)
	}
	if !strings.Contains(out.VerdictCaveat, "2 checks had incomplete evidence") {
		t.Fatalf("verdictCaveat = %q, want the unknown-checks caveat next to the verdict", out.VerdictCaveat)
	}
	if len(out.Coverage.ScopedKinds["pods"]) != 2 {
		t.Fatal("coverage.scopedKinds dropped by minification — per-kind ceilings would be overstated")
	}
	if len(out.Checks) != 3 || out.Checks[0].ID != "blocked-drain" || out.Checks[1].ID != "unknown-runtime" || out.Checks[2].ID != "passed-metrics" {
		t.Fatalf("check order = %v, want required-action order (blocked → unknown → passed)", out.Checks)
	}
	if out.Checks[0].Findings != 30 {
		t.Fatalf("tier-1 findings count = %d, want 30", out.Checks[0].Findings)
	}
	if out.Checks[2].EvidenceNote == "" {
		t.Fatal("evidenceNote on a passed check dropped — evidence limits must survive on passes")
	}
	if out.Checks[1].Caveat == "" {
		t.Fatal("caveat on an unknown check dropped")
	}
	if out.Check != nil {
		t.Fatal("tier 1 must not include an expanded check")
	}
}

func TestShapeUpgradeReadinessNoAccessShortCircuits(t *testing.T) {
	outcome := upgradeFixtureOutcome()
	outcome.Results = &upgradereadiness.ScanResults{
		CurrentVersion:  "1.33",
		TargetVersion:   "1.34",
		ReviewedThrough: "1.36",
		Verdict:         upgradereadiness.VerdictUnknown,
		Coverage:        upgradereadiness.Coverage{Source: "live", State: "no_access"},
	}
	out, err := shapeUpgradeReadiness(outcome, upgradeReadinessInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != "" || out.Summary != nil || out.Checks != nil {
		t.Fatalf("no_access output = verdict=%q summary=%v checks=%v — a verdict computed from nothing must not be printed", out.Verdict, out.Summary, out.Checks)
	}
	if out.Notice == "" || !strings.Contains(out.Notice, "not a pass") {
		t.Fatalf("notice = %q, want an explicit access-limitation sentence", out.Notice)
	}
}

func TestShapeUpgradeReadinessTier2Paging(t *testing.T) {
	first, err := shapeUpgradeReadiness(upgradeFixtureOutcome(), upgradeReadinessInput{Check: "blocked-drain"})
	if err != nil {
		t.Fatal(err)
	}
	check := first.Check
	if check == nil || check.FindingsTotal != 30 || len(check.Findings) != upgradeFindingsPageSize || check.FindingsTruncated != 5 {
		t.Fatalf("first page = total=%d shown=%d truncated=%d, want 30/25/5", check.FindingsTotal, len(check.Findings), check.FindingsTruncated)
	}
	for i := range 10 {
		if check.Findings[i].Level != upgradereadiness.LevelBlocker {
			t.Fatalf("finding %d level = %s, want blockers first", i, check.Findings[i].Level)
		}
	}
	if check.Findings[0].Reference == nil || check.Findings[0].Reference.Title != "first" {
		t.Fatal("finding should keep exactly the first reference")
	}

	second, err := shapeUpgradeReadiness(upgradeFixtureOutcome(), upgradeReadinessInput{Check: "blocked-drain", Offset: 25, ScanID: "sc_fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Check.Findings) != 5 || second.Check.FindingsTruncated != 0 || second.Check.Offset != 25 {
		t.Fatalf("second page = shown=%d truncated=%d offset=%d, want 5/0/25", len(second.Check.Findings), second.Check.FindingsTruncated, second.Check.Offset)
	}

	past, err := shapeUpgradeReadiness(upgradeFixtureOutcome(), upgradeReadinessInput{Check: "blocked-drain", Offset: 40, ScanID: "sc_fixture"})
	if err != nil || len(past.Check.Findings) != 0 || past.Check.FindingsTruncated != 0 {
		t.Fatalf("past-the-end page = %v (err=%v), want empty page with zero truncated", past.Check, err)
	}
}

func TestShapeUpgradeReadinessLevelFilter(t *testing.T) {
	out, err := shapeUpgradeReadiness(upgradeFixtureOutcome(), upgradeReadinessInput{Check: "blocked-drain", Level: "warning"})
	if err != nil {
		t.Fatal(err)
	}
	check := out.Check
	if check.FindingsTotal != 30 {
		t.Fatalf("findingsTotal = %d, want the unfiltered total so the filter can't masquerade as completeness", check.FindingsTotal)
	}
	matching := len(check.Findings) + check.FindingsTruncated
	if matching != 20 {
		t.Fatalf("matching findings = %d, want 20 (blockers + warnings)", matching)
	}
	for _, finding := range check.Findings {
		if finding.Level == upgradereadiness.LevelReview {
			t.Fatal("level=warning returned a review finding")
		}
	}
}

func TestShapeUpgradeReadinessScanChangedError(t *testing.T) {
	_, err := shapeUpgradeReadiness(upgradeFixtureOutcome(), upgradeReadinessInput{Check: "blocked-drain", Offset: 25, ScanID: "sc_older"})
	if err == nil || !strings.Contains(err.Error(), "scan changed") || !strings.Contains(err.Error(), "restart from offset 0") {
		t.Fatalf("mismatched scan_id error = %v, want a scan-changed restart error", err)
	}
}

func TestShapeUpgradeReadinessRefreshSupersedesScanIDBinding(t *testing.T) {
	// A fix-then-rescan caller may carry the previous scan_id alongside
	// refresh=true; the id being replaced is the requested outcome, not a
	// paging hazard — the fresh results must come back, not "scan changed".
	out, err := shapeUpgradeReadiness(upgradeFixtureOutcome(), upgradeReadinessInput{Check: "blocked-drain", ScanID: "sc_older", Refresh: true})
	if err != nil || out.Check == nil {
		t.Fatalf("refresh with stale scan_id = check=%v err=%v, want the fresh scan's results", out.Check, err)
	}
	if out.ScanID != "sc_fixture" {
		t.Fatalf("response scanId = %q, want the fresh snapshot's id for subsequent paging", out.ScanID)
	}
}

func TestShapeUpgradeReadinessUnknownCheckListsValidIDs(t *testing.T) {
	_, err := shapeUpgradeReadiness(upgradeFixtureOutcome(), upgradeReadinessInput{Check: "nope"})
	if err == nil || !strings.Contains(err.Error(), "blocked-drain") || !strings.Contains(err.Error(), "passed-metrics") {
		t.Fatalf("unknown check error = %v, want valid ids listed for self-correction", err)
	}
}

func TestValidateUpgradeReadinessInput(t *testing.T) {
	for name, tc := range map[string]struct {
		input   upgradeReadinessInput
		wantErr string
	}{
		"defaults valid":           {input: upgradeReadinessInput{}},
		"expansion valid":          {input: upgradeReadinessInput{Check: "x", Level: "blocker"}},
		"paging valid":             {input: upgradeReadinessInput{Check: "x", Offset: 25, ScanID: "sc_1"}},
		"negative offset":          {input: upgradeReadinessInput{Check: "x", Offset: -1}, wantErr: "offset must be"},
		"offset without check":     {input: upgradeReadinessInput{Offset: 5}, wantErr: "offset applies only"},
		"offset without scan_id":   {input: upgradeReadinessInput{Check: "x", Offset: 5}, wantErr: "requires scan_id"},
		"refresh while paging":     {input: upgradeReadinessInput{Check: "x", Offset: 5, ScanID: "sc_1", Refresh: true}, wantErr: "refresh cannot be combined"},
		"level without check":      {input: upgradeReadinessInput{Level: "blocker"}, wantErr: "level applies only"},
		"unknown level":            {input: upgradeReadinessInput{Check: "x", Level: "sev1"}, wantErr: "unknown level"},
		"refresh at offset 0 fine": {input: upgradeReadinessInput{Check: "x", Refresh: true}},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateUpgradeReadinessInput(tc.input)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}
