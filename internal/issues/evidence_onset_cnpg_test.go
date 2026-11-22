package issues

import (
	"testing"
	"time"
)

func TestDegradedFindingKeepsOnlyItsSubjectsOnset(t *testing.T) {
	issue := degradedIssue("subject-owned")
	issue.LastSeen = issue.FirstSeen.Add(time.Hour)
	issue.Degraded.KeepSubjectOnset = true
	shown, ok := ForEvidence(issue, func(EvidenceRead) bool { return false }, false)
	if !ok || !shown.FirstSeen.Equal(issue.FirstSeen) || shown.OnsetUnknown || shown.IssueTiming != issue.IssueTiming || shown.IssueTimingBasis != issue.IssueTimingBasis || shown.LastSeen != issue.LastSeen {
		t.Fatalf("independent subject timing changed: before=%+v after=%+v", issue, shown)
	}
	issue.Degraded.KeepSubjectOnset = false
	shown, ok = ForEvidence(issue, func(EvidenceRead) bool { return false }, false)
	if !ok || !shown.FirstSeen.IsZero() || !shown.OnsetUnknown || shown.IssueTiming != "" || shown.IssueTimingBasis != "" {
		t.Fatalf("evidence-derived timing must remain withheld by default: %+v", shown)
	}
	issue.RawMessage = ""
	if shown, _ := ForEvidence(issue, func(EvidenceRead) bool { return false }, false); shown.RawMessage != "" {
		t.Fatalf("degrading invented a raw message: %+v", shown)
	}
}
