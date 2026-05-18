package server

import (
	"testing"

	"github.com/skyhook-io/radar/internal/issues"
	bpaudit "github.com/skyhook-io/radar/pkg/audit"
)

// Pin the audit→issue severity normalization on the AuditSummary wire.
// Without it, sibling resourceContext fields disagree on what "highest
// severity" means: audit emits "danger" while issueSummary emits
// "critical". Mirror the same mapping internal/issues.fromAudit uses
// for the unified issue stream so consumers see one vocabulary.
func TestNormalizeAuditSeverity(t *testing.T) {
	cases := map[string]string{
		bpaudit.SeverityDanger:  string(issues.SeverityCritical),
		bpaudit.SeverityWarning: string(issues.SeverityWarning),
		"":                      "",        // empty stays empty — explicit contract
		"unknown":               "unknown", // future audit values pass through
	}
	for in, want := range cases {
		if got := normalizeAuditSeverity(in); got != want {
			t.Errorf("normalizeAuditSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}
