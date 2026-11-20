package issues

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func evidenceIssue(id string, reads ...EvidenceRead) Issue {
	return Issue{ID: id, Kind: "Widget", Group: "example.com", Namespace: "team-a", Name: "w", RequiredReads: reads}
}

var secretList = EvidenceRead{Resource: "secrets", Namespace: "team-a", Verb: "get"}

func degradedIssue(id string) Issue {
	i := evidenceIssue(id, secretList)
	i.Message, i.RawMessage = "Secret/db key password changed at 10:02", "Secret/db key password changed at 10:02"
	i.Cause, i.Action = "Secret/db changed after the container started", "Restart the pod"
	i.Hazard = "Secret/db private warning"
	i.ActionPrimary, i.ActionSecondaryTitle, i.ActionSecondary = "Secret/db primary", "Secret/db details", []string{"password changed at 10:02"}
	i.FirstSeen, i.IssueTiming, i.IssueTimingBasis = time.Date(2026, 10, 1, 10, 2, 0, 0, time.UTC), "started_after_resource_was_healthy", "exact"
	i.Degraded = &DegradedFinding{Message: "A container may hold stale values", Cause: "A Secret it reads changed", Action: "Restart the pod"}
	return i
}

func TestViewEvidence(t *testing.T) {
	podList := EvidenceRead{Resource: "pods", Namespace: "team-a", Verb: "list"}
	widgetList := EvidenceRead{Group: "example.com", Resource: "widgets", Namespace: "team-a", Verb: "list"}
	allow := func(EvidenceRead) bool { return true }
	denyPods := func(read EvidenceRead) bool { return read.Resource != "pods" }
	degraded := &DegradedFinding{Message: "m"}

	for _, tc := range []struct {
		name            string
		reads           []EvidenceRead
		degraded        *DegradedFinding
		canRead         func(EvidenceRead) bool
		allowUnfiltered bool
		want            EvidenceView
		expected        bool
	}{
		{"expected, every read allowed", []EvidenceRead{podList}, degraded, allow, false, EvidenceExpected, true},
		{"expected, a read denied", []EvidenceRead{podList, widgetList}, degraded, denyPods, false, EvidenceDegraded, true},
		{"expected, internal composition keeps it unprojected", []EvidenceRead{podList}, degraded, nil, true, EvidenceFull, true},
		{"expected, no authorizer", []EvidenceRead{podList}, degraded, nil, false, EvidenceDegraded, true},
		{"no dependencies, no authorizer", nil, nil, nil, false, EvidenceFull, false},
		{"dependencies, no authorizer", []EvidenceRead{podList}, nil, nil, false, EvidenceWithheld, false},
		{"dependencies, no authorizer, degraded form", []EvidenceRead{podList}, degraded, nil, false, EvidenceDegraded, false},
		{"dependencies, internal composition", []EvidenceRead{podList}, nil, nil, true, EvidenceFull, false},
		{"every read allowed", []EvidenceRead{podList, widgetList}, degraded, allow, false, EvidenceFull, false},
		{"one read denied, no degraded form", []EvidenceRead{podList, widgetList}, nil, denyPods, false, EvidenceWithheld, false},
		{"one read denied, degraded form", []EvidenceRead{podList, widgetList}, degraded, denyPods, false, EvidenceDegraded, false},
		{"an authorizer overrides internal composition", []EvidenceRead{podList}, nil, denyPods, true, EvidenceWithheld, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ViewEvidence(tc.reads, tc.degraded, tc.expected, tc.canRead, tc.allowUnfiltered); got != tc.want {
				t.Fatalf("ViewEvidence = %v, want %v", got, tc.want)
			}
		})
	}
}

// A degraded finding keeps what is wrong and what to do, and loses every fact
// only the reads reveal: the evidence wording and the time it changed.
func TestForEvidenceDegradesWithoutTheEvidence(t *testing.T) {
	denySecrets := func(read EvidenceRead) bool { return read.Resource != "secrets" }
	shown, ok := ForEvidence(degradedIssue("stale"), denySecrets, false)
	if !ok {
		t.Fatal("a finding with a degraded form was withheld")
	}
	if shown.Message != "A container may hold stale values" || shown.RawMessage != shown.Message || shown.Cause != "A Secret it reads changed" || shown.Action != "Restart the pod" {
		t.Fatalf("degraded wording = %+v", shown)
	}
	if !shown.FirstSeen.IsZero() || !shown.OnsetUnknown || shown.IssueTiming != "" || shown.IssueTimingBasis != "" || shown.OnsetCoverage != nil {
		t.Fatalf("degraded finding kept the evidence's onset: %+v", shown)
	}
	if shown.Hazard != "" || shown.ActionPrimary != "" || shown.ActionSecondaryTitle != "" || shown.ActionSecondary != nil {
		t.Fatalf("degraded finding kept original action presentation: %+v", shown)
	}
	if shown.RequiredReads != nil || shown.Degraded != nil {
		t.Fatalf("degraded finding kept its evidence bookkeeping: %+v", shown)
	}
	data, _ := json.Marshal(shown)
	for _, leak := range []string{"Secret/db", "password", "10:02"} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("degraded finding exposes %q: %s", leak, data)
		}
	}

	full, ok := ForEvidence(degradedIssue("stale"), func(EvidenceRead) bool { return true }, false)
	if !ok || full.Message != "Secret/db key password changed at 10:02" || full.FirstSeen.IsZero() {
		t.Fatalf("a caller who can read the Secret lost the full finding: %+v", full)
	}
}

func TestApplyEvidenceAccessDegradesOrWithholds(t *testing.T) {
	denySecrets := func(read EvidenceRead) bool { return read.Resource != "secrets" }
	var withheld []string
	out := applyEvidenceAccess([]Issue{degradedIssue("degradable"), evidenceIssue("bare", secretList), evidenceIssue("plain")}, denySecrets, false, func(i Issue) {
		withheld = append(withheld, i.ID)
	})
	if len(out) != 2 || out[0].ID != "degradable" || out[0].Degraded != nil || out[1].ID != "plain" {
		t.Fatalf("applyEvidenceAccess kept %+v", out)
	}
	if len(withheld) != 1 || withheld[0] != "bare" {
		t.Fatalf("withheld %v, want only the finding without a degraded form", withheld)
	}
}

func expectedIssue(id string) Issue {
	i := degradedIssue(id)
	i.ExpectedWhenVerified = true
	i.Degraded.KeepSubjectOnset = true
	return i
}

func TestApplyEvidenceAccessDropsVerifiedExpectedFindings(t *testing.T) {
	var withheld []string
	onWithheld := func(i Issue) { withheld = append(withheld, i.ID) }
	if out := applyEvidenceAccess([]Issue{expectedIssue("expected")}, func(EvidenceRead) bool { return true }, false, onWithheld); len(out) != 0 || len(withheld) != 0 {
		t.Fatalf("verified expected finding shown or counted as withheld: %+v %v", out, withheld)
	}
	out := applyEvidenceAccess([]Issue{expectedIssue("expected")}, func(EvidenceRead) bool { return false }, false, onWithheld)
	if len(out) != 1 || out[0].Message != "A container may hold stale values" || out[0].FirstSeen.IsZero() || out[0].ExpectedWhenVerified || len(withheld) != 0 {
		t.Fatalf("unverifiable expected finding must read as the degraded warning with its onset: %+v %v", out, withheld)
	}
	out = applyEvidenceAccess([]Issue{expectedIssue("expected")}, nil, true, onWithheld)
	if len(out) != 1 || !out[0].ExpectedWhenVerified || len(out[0].RequiredReads) == 0 || out[0].Degraded == nil {
		t.Fatalf("an internal memo must keep the finding unprojected: %+v", out)
	}
}

func TestExpectedFindingSurvivesGroupingAndMemoReauthorization(t *testing.T) {
	flat := []Issue{expectedIssue("expected")}
	grouped := GroupIssues(flat)
	if len(grouped) != 1 || !grouped[0].ExpectedWhenVerified {
		t.Fatalf("grouped row lost the flag: %+v", grouped)
	}
	var withheld int
	opts := RelatedIssueOptions{CanReadEvidence: func(EvidenceRead) bool { return true }, OnEvidenceWithheld: func(Issue) { withheld++ }}
	if got := RelatedIssuesFrom(flat, grouped, opts, "example.com", "Widget", "team-a", "w"); len(got) != 0 || withheld != 0 {
		t.Fatalf("full caller got the expected finding from a memo: %+v (withheld %d)", got, withheld)
	}
	opts.CanReadEvidence = func(EvidenceRead) bool { return false }
	if got := RelatedIssuesFrom(flat, grouped, opts, "example.com", "Widget", "team-a", "w"); len(got) != 1 || got[0].Message != "A container may hold stale values" {
		t.Fatalf("restricted caller lost the warning: %+v", got)
	}
}

func TestRelatedIssuesFromAuthorizesEvidence(t *testing.T) {
	lookup := func(flat, grouped []Issue, opts RelatedIssueOptions) []Issue {
		return RelatedIssuesFrom(flat, grouped, opts, "example.com", "Widget", "team-a", "w")
	}
	bare := []Issue{evidenceIssue("bare", secretList)}
	if got := lookup(bare, bare, RelatedIssueOptions{}); len(got) != 0 {
		t.Fatalf("findings with inventory dependencies returned without an authorizer: %+v", got)
	}
	if got := lookup(bare, bare, RelatedIssueOptions{CanReadEvidence: func(EvidenceRead) bool { return false }}); len(got) != 0 {
		t.Fatalf("denied evidence returned: %+v", got)
	}
	if got := lookup(bare, bare, RelatedIssueOptions{CanReadEvidence: func(EvidenceRead) bool { return true }}); len(got) == 0 {
		t.Fatal("authorized evidence withheld")
	}
	degradable := []Issue{degradedIssue("degradable")}
	got := lookup(degradable, degradable, RelatedIssueOptions{CanReadEvidence: func(EvidenceRead) bool { return false }})
	if len(got) != 1 || got[0].Message != "A container may hold stale values" || !got[0].FirstSeen.IsZero() {
		t.Fatalf("a precomposed finding with a degraded form = %+v, want it degraded", got)
	}
}

// RequiredReads and the degraded form are internal bookkeeping for
// authorization, never part of an issue a client receives.
func TestEvidenceBookkeepingIsNotSerialized(t *testing.T) {
	data, err := json.Marshal(degradedIssue("derived"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"RequiredReads", `"verb"`, "Degraded", "may hold stale values"} {
		if strings.Contains(string(data), field) {
			t.Fatalf("issue JSON exposes %s: %s", field, data)
		}
	}
}

func TestEvidenceWithheldFindingsAreReported(t *testing.T) {
	issue := evidenceIssue("derived", secretList)
	var seen []string
	opts := RelatedIssueOptions{
		CanReadEvidence:    func(EvidenceRead) bool { return false },
		OnEvidenceWithheld: func(i Issue) { seen = append(seen, i.ID) },
	}
	if got := RelatedIssuesFrom([]Issue{issue}, []Issue{issue}, opts, "example.com", "Widget", "team-a", "w"); len(got) != 0 {
		t.Fatalf("denied evidence returned: %+v", got)
	}
	if len(seen) == 0 || seen[0] != "derived" {
		t.Fatalf("withheld finding not reported: %v", seen)
	}
	seen = nil
	if got := RelatedIssuesFrom([]Issue{degradedIssue("degraded")}, []Issue{degradedIssue("degraded")}, opts, "example.com", "Widget", "team-a", "w"); len(got) != 1 || len(seen) != 0 {
		t.Fatalf("a degraded finding counted as withheld: got %+v, reported %v", got, seen)
	}
}
