package issues

import (
	"testing"
	"time"
)

func TestMergeExternalIssuesFiltersSortsAndCaps(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	base := []Issue{
		testIssueForMerge("Deployment", "apps", "web", SeverityCritical, now.Add(-30*time.Minute)),
	}
	extras := []Issue{
		testIssueForMerge("HelmRelease", "apps", "cart", SeverityCritical, now.Add(-5*time.Minute)),
		testIssueForMerge("Pod", "apps", "ignored", SeverityCritical, now.Add(-1*time.Minute)),
	}

	got, stats := MergeExternalIssues(base, ComposeStats{TotalMatched: len(base)}, Filters{
		Kinds: []string{"Deployment", "HelmRelease"},
		Limit: 1,
	}, extras)

	if stats.TotalMatched != 2 {
		t.Fatalf("TotalMatched = %d, want 2", stats.TotalMatched)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want cap 1", len(got))
	}
	if got[0].Kind != "HelmRelease" || got[0].Name != "cart" {
		t.Fatalf("first issue = %s/%s, want HelmRelease/cart", got[0].Kind, got[0].Name)
	}
}

func testIssueForMerge(kind, namespace, name string, severity Severity, firstSeen time.Time) Issue {
	iss := Issue{
		Severity:  severity,
		Source:    SourceProblem,
		Kind:      kind,
		Group:     resolveGroup("", kind),
		Namespace: namespace,
		Name:      name,
		Reason:    "TestReason",
		FirstSeen: firstSeen,
		LastSeen:  firstSeen,
	}
	classifyIssue(&iss)
	enrichIdentity(&iss)
	return iss
}
