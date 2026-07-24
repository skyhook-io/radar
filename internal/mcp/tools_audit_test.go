package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/checks"
)

func TestCollectAuditToolFindingsUsesCanonicalSeverity(t *testing.T) {
	raw := []bp.Finding{
		{
			Kind: "Deployment", Namespace: "prod", Name: "api",
			CheckID: "privileged", Category: bp.CategorySecurity,
			Severity: bp.SeverityDanger, Message: "privileged",
		},
		{
			Kind: "Deployment", Namespace: "prod", Name: "api",
			CheckID: "cpuRequestMissing", Category: bp.CategoryEfficiency,
			Severity: bp.SeverityWarning, Message: "missing CPU request",
		},
	}

	categories, findings := collectAuditToolFindings(raw, bp.CheckRegistry, nil, "", "")
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(findings))
	}
	if findings[0].Severity != checks.SeverityHigh || findings[1].Severity != checks.SeverityMedium {
		t.Fatalf("severities = [%q, %q], want [high, medium]", findings[0].Severity, findings[1].Severity)
	}

	summary := summarizeAuditToolFindings(findings, categories)
	if summary.Critical != 0 || summary.High != 1 || summary.Medium != 1 || summary.Low != 0 {
		t.Fatalf("summary = %+v, want zero critical/low and one high/medium", summary)
	}
	if summary.Resources != 1 {
		t.Fatalf("resources = %d, want 1", summary.Resources)
	}
}

func TestCollectAuditToolFindingsFiltersCanonicalSeverity(t *testing.T) {
	raw := []bp.Finding{
		{
			Kind: "Pod", Namespace: "prod", Name: "api",
			CheckID: "privileged", Category: bp.CategorySecurity, Severity: bp.SeverityDanger,
		},
		{
			Kind: "Pod", Namespace: "prod", Name: "api",
			CheckID: "cpuRequestMissing", Category: bp.CategoryEfficiency, Severity: bp.SeverityWarning,
		},
	}

	for _, tc := range []struct {
		filter checks.Severity
		want   checks.Severity
	}{
		{filter: checks.SeverityHigh, want: checks.SeverityHigh},
		{filter: checks.SeverityMedium, want: checks.SeverityMedium},
	} {
		t.Run(string(tc.filter), func(t *testing.T) {
			categories, findings := collectAuditToolFindings(raw, bp.CheckRegistry, nil, "", tc.filter)
			if len(findings) != 1 || findings[0].Severity != tc.want {
				t.Fatalf("findings = %+v, want one %s finding", findings, tc.want)
			}
			if len(categories) != 1 || categories[findings[0].Category] != 1 {
				t.Fatalf("categories = %+v, want only the category available at severity %s", categories, tc.filter)
			}
		})
	}
}

func TestParseAuditSeverityRejectsLegacyAndUnknownValues(t *testing.T) {
	for _, value := range []string{"danger", "warning", "urgent"} {
		_, err := parseAuditSeverity(value)
		if err == nil {
			t.Fatalf("parseAuditSeverity(%q) succeeded, want clear contract error", value)
		}
		if !strings.Contains(err.Error(), "critical, high, medium, low") {
			t.Fatalf("parseAuditSeverity(%q) error = %q, want canonical values", value, err)
		}
	}
}

func TestGetClusterAuditRejectsLegacySeverityBeforeClusterAccess(t *testing.T) {
	_, _, err := handleGetAudit(context.Background(), nil, auditInput{Severity: "danger"})
	if err == nil || !strings.Contains(err.Error(), "critical, high, medium, low") {
		t.Fatalf("legacy severity error = %v, want canonical contract error", err)
	}
}

func TestAuditSummaryAlwaysEmitsAllSeverityKeys(t *testing.T) {
	data, err := json.Marshal(auditSummary{})
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, key := range []string{`"critical":0`, `"high":0`, `"medium":0`, `"low":0`} {
		if !strings.Contains(got, key) {
			t.Fatalf("zero summary = %s, missing %s", got, key)
		}
	}
	if strings.Contains(got, `"warning"`) || strings.Contains(got, `"danger"`) {
		t.Fatalf("zero summary exposes raw severity vocabulary: %s", got)
	}
}
