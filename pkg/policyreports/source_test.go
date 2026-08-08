package policyreports

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildIndex_ExtractsSource(t *testing.T) {
	now := time.Now()
	r := makeReport(t, "PolicyReport", "prod", "pr-1", nil, now, []map[string]any{
		{
			"policy":    "require-run-as-nonroot",
			"rule":      "check-containers",
			"result":    "fail",
			"source":    "KyvernoValidatingPolicy",
			"message":   "runAsNonRoot must be true",
			"resources": []any{resourceRefWithGroup("apps/v1", "Deployment", "prod", "api")},
		},
	})

	got := BuildIndex([]*unstructured.Unstructured{r}).FindingsFor("apps", "Deployment", "prod", "api")
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(got))
	}
	if got[0].Source != "KyvernoValidatingPolicy" {
		t.Errorf("Source = %q, want %q", got[0].Source, "KyvernoValidatingPolicy")
	}
	if got[0].Engine() != EngineKyverno {
		t.Errorf("Engine() = %q, want %q", got[0].Engine(), EngineKyverno)
	}
}

// A report entry with no `source` must not fabricate one. Older engines
// omit the field entirely and the resulting finding has to stay
// attributable-as-unknown rather than defaulting to Kyverno just because
// this index started life as a Kyverno-only feature.
func TestBuildIndex_MissingSourceStaysUnknown(t *testing.T) {
	now := time.Now()
	r := makeReport(t, "PolicyReport", "prod", "pr-1", nil, now, []map[string]any{
		{
			"policy":    "legacy-policy",
			"rule":      "some-rule",
			"result":    "fail",
			"resources": []any{resourceRef("Pod", "prod", "api-1")},
		},
	})

	got := BuildIndex([]*unstructured.Unstructured{r}).FindingsFor("", "Pod", "prod", "api-1")
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(got))
	}
	if got[0].Source != "" {
		t.Errorf("Source = %q, want empty", got[0].Source)
	}
	if got[0].Engine() != EngineUnknown {
		t.Errorf("Engine() = %q, want %q", got[0].Engine(), EngineUnknown)
	}
}

// The multi-engine case the taxonomy exists for: one subject carrying
// findings from legacy Kyverno, modern Kyverno, VAP and Trivy at once.
// Filtering by EngineKyverno must return BOTH Kyverno results and nothing
// else — the exact behaviour a raw-source match would get wrong.
func TestFindingsForEngine_CollapsesProducerTypesAndExcludesOthers(t *testing.T) {
	now := time.Now()
	r := makeReport(t, "PolicyReport", "prod", "pr-1", nil, now, []map[string]any{
		{
			"policy": "legacy-check", "result": "fail", "source": "kyverno",
			"resources": []any{resourceRef("Pod", "prod", "api-1")},
		},
		{
			"policy": "modern-check", "result": "fail", "source": "KyvernoValidatingPolicy",
			"resources": []any{resourceRef("Pod", "prod", "api-1")},
		},
		{
			"policy": "vap-check", "result": "fail", "source": "ValidatingAdmissionPolicy",
			"resources": []any{resourceRef("Pod", "prod", "api-1")},
		},
		{
			"policy": "cve-check", "result": "fail", "source": "Trivy Vulnerability",
			"resources": []any{resourceRef("Pod", "prod", "api-1")},
		},
	})

	idx := BuildIndex([]*unstructured.Unstructured{r})

	kyverno := idx.FindingsForEngine("", "Pod", "prod", "api-1", EngineKyverno)
	if len(kyverno) != 2 {
		t.Fatalf("EngineKyverno returned %d findings, want 2 (legacy + modern)", len(kyverno))
	}
	for _, f := range kyverno {
		if f.Engine() != EngineKyverno {
			t.Errorf("policy %q leaked into the kyverno bucket with engine %q", f.Policy, f.Engine())
		}
	}

	if got := idx.FindingsForEngine("", "Pod", "prod", "api-1", EngineVAP); len(got) != 1 {
		t.Errorf("EngineVAP returned %d findings, want 1", len(got))
	}
	if got := idx.FindingsForEngine("", "Pod", "prod", "api-1", EngineTrivy); len(got) != 1 {
		t.Errorf("EngineTrivy returned %d findings, want 1", len(got))
	}
	if got := idx.FindingsForEngine("", "Pod", "prod", "api-1", EngineFalco); got != nil {
		t.Errorf("EngineFalco returned %d findings, want nil", len(got))
	}

	// The unfiltered accessor keeps returning everything.
	if got := idx.FindingsFor("", "Pod", "prod", "api-1"); len(got) != 4 {
		t.Errorf("FindingsFor returned %d findings, want 4", len(got))
	}
}

func TestFindingsForEngine_NilSafe(t *testing.T) {
	var idx *Index
	if got := idx.FindingsForEngine("", "Pod", "prod", "api-1", EngineKyverno); got != nil {
		t.Errorf("nil index returned %d findings, want nil", len(got))
	}
}

// A cluster mid-migration can serve both report families with overlapping
// content, and the selection layer watches both when both hold data. The same
// (policy, rule, result, message, source) on the same subject carries no extra
// information whichever report it arrived in, and counting it twice would
// inflate every violation total the UI and agents show.
func TestBuildIndex_DeduplicatesIdenticalFindingsAcrossReports(t *testing.T) {
	now := time.Now()
	result := map[string]any{
		"policy": "require-labels", "rule": "check", "result": "fail",
		"source": "KyvernoValidatingPolicy", "message": "needs a team label",
		"resources": []any{resourceRef("Pod", "prod", "api-1")},
	}
	// Same finding, carried by two reports from two API families.
	wg := makeReport(t, "PolicyReport", "prod", "wg-1", nil, now, []map[string]any{result})
	or := makeReport(t, "Report", "prod", "or-1", nil, now, []map[string]any{result})

	got := BuildIndex([]*unstructured.Unstructured{wg, or}).FindingsFor("", "Pod", "prod", "api-1")
	if len(got) != 1 {
		t.Fatalf("expected the duplicate to be collapsed to 1 finding, got %d", len(got))
	}
}

// Dedup must key on the whole finding, not just the policy — two different
// rules of the same policy failing on one subject are two real findings.
func TestBuildIndex_KeepsDistinctFindingsFromTheSamePolicy(t *testing.T) {
	now := time.Now()
	r := makeReport(t, "PolicyReport", "prod", "pr-1", nil, now, []map[string]any{
		{
			"policy": "baseline", "rule": "no-host-network", "result": "fail", "source": "kyverno",
			"resources": []any{resourceRef("Pod", "prod", "api-1")},
		},
		{
			"policy": "baseline", "rule": "no-privileged", "result": "fail", "source": "kyverno",
			"resources": []any{resourceRef("Pod", "prod", "api-1")},
		},
	})

	got := BuildIndex([]*unstructured.Unstructured{r}).FindingsFor("", "Pod", "prod", "api-1")
	if len(got) != 2 {
		t.Fatalf("expected 2 distinct findings, got %d", len(got))
	}
}

// falcosidekick hardcodes the producer string as the literal "Falco" and puts
// its own detection source ("syscall"/"k8s_audit") in the POLICY field. Pinned
// because the taxonomy has to attribute on Source, never on Policy.
func TestEngineForSource_FalcosidekickLiteral(t *testing.T) {
	f := Finding{Policy: "syscall", Rule: "Terminal shell in container", Source: "Falco"}
	if f.Engine() != EngineFalco {
		t.Errorf("Engine() = %q, want %q", f.Engine(), EngineFalco)
	}
}

// Bugbot #3 (MEDIUM): the Kyverno-labelled rollup must not absorb other
// engines. The index is shared — Trivy, Falco adapters and VAP evaluation all
// write into the same report families — so an unfiltered read presented as
// "kyverno" over-counts as soon as a second engine is installed.
func TestFindingsForAnyEngine_KyvernoAttributionSet(t *testing.T) {
	now := time.Now()
	r := makeReport(t, "PolicyReport", "prod", "pr-1", nil, now, []map[string]any{
		{"policy": "modern", "result": "fail", "source": "KyvernoValidatingPolicy",
			"resources": []any{resourceRef("Pod", "prod", "api-1")}},
		{"policy": "legacy", "result": "fail", "source": "kyverno",
			"resources": []any{resourceRef("Pod", "prod", "api-1")}},
		// No source at all — older Kyverno. Must still count, or the rollup
		// empties out on exactly the legacy clusters we still serve.
		{"policy": "unattributed", "result": "fail",
			"resources": []any{resourceRef("Pod", "prod", "api-1")}},
		{"policy": "vap", "result": "fail", "source": "ValidatingAdmissionPolicy",
			"resources": []any{resourceRef("Pod", "prod", "api-1")}},
		{"policy": "cve", "result": "fail", "source": "Trivy Vulnerability",
			"resources": []any{resourceRef("Pod", "prod", "api-1")}},
		{"policy": "syscall", "result": "fail", "source": "Falco",
			"resources": []any{resourceRef("Pod", "prod", "api-1")}},
	})

	got := BuildIndex([]*unstructured.Unstructured{r}).
		FindingsForAnyEngine("", "Pod", "prod", "api-1", EnginesAttributableToKyverno...)

	if len(got) != 3 {
		t.Fatalf("got %d findings, want 3 (modern + legacy + unattributed)", len(got))
	}
	for _, f := range got {
		switch f.Policy {
		case "modern", "legacy", "unattributed":
		default:
			t.Errorf("%q (source %q) leaked into the Kyverno rollup", f.Policy, f.Source)
		}
	}
}

// An empty filter must mean "nothing matches", never "everything" — a caller
// that accidentally passes no engines should get an obviously empty result
// rather than silently unfiltered data.
func TestFindingsForAnyEngine_EmptyFilterReturnsNothing(t *testing.T) {
	now := time.Now()
	r := makeReport(t, "PolicyReport", "prod", "pr-1", nil, now, []map[string]any{
		{"policy": "a", "result": "fail", "source": "kyverno",
			"resources": []any{resourceRef("Pod", "prod", "api-1")}},
	})

	if got := BuildIndex([]*unstructured.Unstructured{r}).FindingsForAnyEngine("", "Pod", "prod", "api-1"); got != nil {
		t.Errorf("empty engine filter returned %d findings, want nil", len(got))
	}
}
