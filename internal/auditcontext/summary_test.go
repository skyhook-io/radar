package auditcontext

import (
	"reflect"
	"testing"

	bpaudit "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/checks"
)

func TestSummarizeFindingsUsesCanonicalPostureSeverity(t *testing.T) {
	findings := []bpaudit.Finding{
		{
			Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "api",
			CheckID: "runAsRoot", Severity: bpaudit.SeverityDanger,
		},
		{
			Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "api",
			CheckID: "missingResourceRequests", Severity: bpaudit.SeverityWarning,
		},
	}

	summary, matched := summarizeFindings(findings, "apps", "Deployment", "prod", "api")
	if summary == nil {
		t.Fatal("summary is nil")
	}
	if summary.Count != 2 {
		t.Errorf("Count = %d, want 2", summary.Count)
	}
	if summary.HighestSeverity != string(checks.SeverityHigh) {
		t.Errorf("HighestSeverity = %q, want canonical posture severity %q", summary.HighestSeverity, checks.SeverityHigh)
	}
	if summary.TopFinding != "runAsRoot" {
		t.Errorf("TopFinding = %q, want runAsRoot", summary.TopFinding)
	}
	if got := []string{matched[0].CheckID, matched[1].CheckID}; !reflect.DeepEqual(got, []string{"runAsRoot", "missingResourceRequests"}) {
		t.Errorf("matched order = %v, want severity-descending order", got)
	}
}

func TestSummarizeFindingsIsGroupAwareAndDeterministic(t *testing.T) {
	findings := []bpaudit.Finding{
		{
			Group: "serving.knative.dev", Kind: "Service", Namespace: "prod", Name: "api",
			CheckID: "z-check", Severity: bpaudit.SeverityDanger,
		},
		{
			Kind: "Service", Namespace: "prod", Name: "api",
			CheckID: "z-check", Severity: bpaudit.SeverityWarning,
		},
		{
			Kind: "Service", Namespace: "prod", Name: "api",
			CheckID: "a-check", Severity: bpaudit.SeverityWarning,
		},
	}

	summary, matched := summarizeFindings(findings, "", "Service", "prod", "api")
	if summary == nil {
		t.Fatal("summary is nil")
	}
	if summary.Count != 2 || summary.HighestSeverity != string(checks.SeverityMedium) || summary.TopFinding != "a-check" {
		t.Fatalf("summary = %#v, want two medium findings with a-check first", summary)
	}
	if got := []string{matched[0].CheckID, matched[1].CheckID}; !reflect.DeepEqual(got, []string{"a-check", "z-check"}) {
		t.Errorf("matched order = %v, want CheckID tiebreak order", got)
	}
}

func TestSummarizeFindingsOmitsEmpty(t *testing.T) {
	if summary, matched := summarizeFindings(nil, "", "Pod", "prod", "api"); summary != nil || matched != nil {
		t.Fatalf("empty findings = (%#v, %#v), want (nil, nil)", summary, matched)
	}
}
