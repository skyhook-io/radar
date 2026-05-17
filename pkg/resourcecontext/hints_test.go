package resourcecontext

import (
	"reflect"
	"testing"
)

func TestSynthesizeHints_NilCtx(t *testing.T) {
	if got := SynthesizeHints(nil, TierBasic); got != nil {
		t.Errorf("nil ctx: got %v, want nil", got)
	}
}

func TestSynthesizeHints_EmptyCtx(t *testing.T) {
	rc := &ResourceContext{Tier: TierBasic}
	got := SynthesizeHints(rc, TierBasic)
	if got != nil {
		t.Errorf("empty rc: got %v, want nil", got)
	}
}

func TestSynthesizeHints_DeterministicOrdering(t *testing.T) {
	rc := &ResourceContext{
		ManagedBy: []ContextRef{{Kind: "Application", Name: "store"}},
		Exposes:   []ContextRef{{Kind: "Service", Name: "api"}},
		SelectedBy: []ContextRef{
			{Kind: "NetworkPolicy", Name: "deny"},
			{Kind: "PodDisruptionBudget", Name: "pdb"},
		},
		ScaledBy:     []ContextRef{{Kind: "HorizontalPodAutoscaler", Name: "hpa"}},
		RunsOn:       &ContextRef{Kind: "Node", Name: "n1"},
		Uses:         &UsesBlock{ConfigMaps: []ContextRef{{Kind: "ConfigMap", Name: "c"}}},
		IssueSummary: &IssueSummary{Count: 2, HighestSeverity: "warning", TopReason: "Backoff"},
		AuditSummary: &AuditSummary{Count: 3, HighestSeverity: "danger"},
	}
	want := []string{
		"Managed by Application store",
		"2 issues (warning: Backoff)",
		"3 audit findings (danger)",
		"Running on node n1",
		"Exposed by 1 Service",
		"1 NetworkPolicy and 1 PodDisruptionBudget select this resource",
		"Scaled by 1 HorizontalPodAutoscaler",
		"Uses 1 ConfigMap",
	}
	got := SynthesizeHints(rc, TierBasic)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hints mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestSynthesizeHints_BasicTierCapped(t *testing.T) {
	// Synthesize a maxed-out context and verify the basic tier caps at
	// maxHintsBasic lines. This guards against unbounded hint growth.
	rc := &ResourceContext{
		ManagedBy:     []ContextRef{{Kind: "App", Name: "a"}},
		Exposes:       []ContextRef{{Kind: "Service", Name: "svc"}},
		SelectedBy:    []ContextRef{{Kind: "PodDisruptionBudget", Name: "p"}, {Kind: "NetworkPolicy", Name: "n"}},
		ScaledBy:      []ContextRef{{Kind: "HorizontalPodAutoscaler", Name: "h"}},
		RunsOn:        &ContextRef{Kind: "Node", Name: "n1"},
		Uses:          &UsesBlock{ConfigMaps: []ContextRef{{Kind: "ConfigMap", Name: "c"}}, Secrets: []ContextRef{{Kind: "Secret", Name: "s"}}},
		IssueSummary:  &IssueSummary{Count: 1, HighestSeverity: "critical", TopReason: "Crash"},
		AuditSummary:  &AuditSummary{Count: 1, HighestSeverity: "danger", TopFinding: "CKV_K8S_1"},
		PolicySummary: &PolicySummary{Kyverno: &KyvernoSummary{Fail: 1, Warn: 1}},
	}
	got := SynthesizeHints(rc, TierBasic)
	if len(got) > maxHintsBasic {
		t.Errorf("basic tier exceeded cap: got %d hints, want ≤%d (%v)", len(got), maxHintsBasic, got)
	}
}

func TestSynthesizeHints_IssueHint_NoSeverity(t *testing.T) {
	rc := &ResourceContext{IssueSummary: &IssueSummary{Count: 1, TopReason: "Pending"}}
	got := SynthesizeHints(rc, TierBasic)
	want := []string{"1 issue: Pending"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSynthesizeHints_PolicyHint_OnlyPass_Skipped(t *testing.T) {
	rc := &ResourceContext{PolicySummary: &PolicySummary{Kyverno: &KyvernoSummary{Pass: 3}}}
	got := SynthesizeHints(rc, TierBasic)
	if got != nil {
		t.Errorf("only-pass summary should not emit a hint; got %v", got)
	}
}

func TestUsesHint_PVCSingular(t *testing.T) {
	rc := &ResourceContext{Uses: &UsesBlock{PVCs: []ContextRef{{Kind: "PersistentVolumeClaim", Name: "data"}}}}
	got := SynthesizeHints(rc, TierBasic)
	want := []string{"Uses 1 PVC"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSelectVerb(t *testing.T) {
	if selectVerb(1) != "selects this resource" {
		t.Errorf("verb(1): %q", selectVerb(1))
	}
	if selectVerb(2) != "select this resource" {
		t.Errorf("verb(2): %q", selectVerb(2))
	}
}

func TestSummarizeKindsCounts_AlphabeticalOrder(t *testing.T) {
	refs := []ContextRef{
		{Kind: "Service", Name: "a"},
		{Kind: "Ingress", Name: "b"},
		{Kind: "Service", Name: "c"},
	}
	got := summarizeKindsCounts(refs)
	want := "1 Ingress, 2 Services"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
