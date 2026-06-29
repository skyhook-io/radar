package checks

import "testing"

func eff(kind, group, ns, name, checkID, category, rawSev, msg string) EffectiveFinding {
	return EffectiveFinding{
		Source:            SourceRadarBuiltin,
		Resource:          ResourceRef{ClusterID: "cl_1", Group: group, Kind: kind, Namespace: ns, Name: name},
		CheckID:           checkID,
		Category:          category,
		OriginalSeverity:  rawSev,
		EffectiveSeverity: MapSeverity(rawSev),
		Message:           msg,
		State:             DefaultEffectiveState(),
	}
}

func fixture() []EffectiveFinding {
	return []EffectiveFinding{
		eff("Deployment", "apps", "prod", "api", "no-limits", CategoryReliability, rawWarning, "no resource limits"),
		eff("Deployment", "apps", "prod", "web", "no-limits", CategoryReliability, rawWarning, "no resource limits"),
		eff("Pod", "", "kube-system", "coredns", "run-as-root", CategorySecurity, rawDanger, "runs as root"),
		eff("Pod", "", "prod", "api-xyz", "run-as-root", CategorySecurity, rawDanger, "runs as root"),
	}
}

var catalog = map[string]CheckMeta{
	"no-limits":   {ID: "no-limits", Title: "Set resource limits", Description: "Containers should set CPU/memory limits", Remediation: "Add resources.limits"},
	"run-as-root": {ID: "run-as-root", Title: "Avoid running as root", Description: "Containers should not run as root", Remediation: "Set runAsNonRoot"},
}

func TestMapSeverity(t *testing.T) {
	if MapSeverity(rawDanger) != SeverityHigh {
		t.Errorf("danger should map to high")
	}
	if MapSeverity(rawWarning) != SeverityMedium {
		t.Errorf("warning should map to medium")
	}
	if MapSeverity("nonsense") != SeverityMedium {
		t.Errorf("unknown should fall back to medium")
	}
}

func TestBuildChecks_GroupByCheck(t *testing.T) {
	out := BuildChecks(fixture(), catalog, "cl_1", "prod")
	if len(out) != 2 {
		t.Fatalf("expected 2 checks (one per checkID), got %d", len(out))
	}
	byID := map[string]Check{}
	for _, c := range out {
		byID[c.CheckID] = c
	}

	nl := byID["no-limits"]
	if nl.AffectedResources != 2 || nl.AffectedFindings != 2 {
		t.Errorf("no-limits: affectedResources=%d affectedFindings=%d, want 2/2", nl.AffectedResources, nl.AffectedFindings)
	}
	if nl.Title != "Set resource limits" {
		t.Errorf("title should come from catalog, got %q", nl.Title)
	}
	if nl.EffectiveSeverity != SeverityMedium {
		t.Errorf("no-limits effective severity = %q, want medium", nl.EffectiveSeverity)
	}

	rr := byID["run-as-root"]
	if rr.EffectiveSeverity != SeverityHigh {
		t.Errorf("run-as-root effective severity = %q, want high", rr.EffectiveSeverity)
	}
	if rr.ID != "cl_1|radar_builtin|run-as-root" {
		t.Errorf("check id = %q", rr.ID)
	}
}

func TestBuildChecks_WorstFirstOrder(t *testing.T) {
	out := BuildChecks(fixture(), catalog, "cl_1", "prod")
	// Security/high (run-as-root) should outrank Reliability/medium (no-limits).
	if out[0].CheckID != "run-as-root" {
		t.Errorf("highest-priority check should be run-as-root, got %q", out[0].CheckID)
	}
}

func TestBuildChecks_VisibleOrder(t *testing.T) {
	// Worst-first matches the rendered queue: severity dominates, then blast
	// radius (affected resources), then checkID. "big" and "small" are both
	// high; "med" is medium. Severity outranks blast radius, so "med" sorts
	// last despite checkID; among the highs, the bigger blast wins.
	findings := []EffectiveFinding{
		eff("Pod", "", "prod", "a", "big", CategorySecurity, rawDanger, "x"),
		eff("Pod", "", "prod", "b", "big", CategorySecurity, rawDanger, "x"),
		eff("Pod", "", "prod", "c", "small", CategorySecurity, rawDanger, "x"),
		eff("Deployment", "apps", "prod", "d", "med", CategoryReliability, rawWarning, "x"),
	}
	out := BuildChecks(findings, map[string]CheckMeta{}, "cl_1", "")
	got := make([]string, len(out))
	for i, c := range out {
		got[i] = c.CheckID
	}
	want := []string{"big", "small", "med"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("queue order = %v, want %v", got, want)
		}
	}
}

func TestBuildChecks_EnvironmentContext(t *testing.T) {
	// env flows through to the Environment context tag; OSS (env "") leaves it
	// empty — there is no fleet environment in standalone Radar.
	for _, c := range BuildChecks(fixture(), catalog, "cl_1", "prod") {
		if c.Environment != "prod" {
			t.Errorf("%s: Environment = %q, want prod", c.CheckID, c.Environment)
		}
	}
	for _, c := range BuildChecks(fixture(), catalog, "cl_1", "") {
		if c.Environment != "" {
			t.Errorf("%s: Environment = %q, want empty for OSS", c.CheckID, c.Environment)
		}
	}
}

func TestBuildChecks_DedupsResourcesWithinCheck(t *testing.T) {
	// Two findings on the SAME resource for one check → 1 affected resource, 2
	// findings (e.g. a workload failing the check on two containers).
	out := BuildChecks([]EffectiveFinding{
		eff("Deployment", "apps", "prod", "api", "no-limits", CategoryReliability, rawWarning, "container a"),
		eff("Deployment", "apps", "prod", "api", "no-limits", CategoryReliability, rawWarning, "container b"),
	}, catalog, "cl_1", "")
	if len(out) != 1 {
		t.Fatalf("expected 1 check, got %d", len(out))
	}
	if out[0].AffectedResources != 1 {
		t.Errorf("affectedResources = %d, want 1 (same resource)", out[0].AffectedResources)
	}
	if out[0].AffectedFindings != 2 {
		t.Errorf("affectedFindings = %d, want 2", out[0].AffectedFindings)
	}
}
