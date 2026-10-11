package issues

import (
	"encoding/json"
	"strings"
	"testing"
)

func evidenceIssue(id string, reads ...EvidenceRead) Issue {
	return Issue{ID: id, Kind: "Widget", Group: "example.com", Namespace: "team-a", Name: "w", RequiredReads: reads}
}

func TestCanReadIssueEvidence(t *testing.T) {
	podList := EvidenceRead{Resource: "pods", Namespace: "team-a", Verb: "list"}
	widgetList := EvidenceRead{Group: "example.com", Resource: "widgets", Namespace: "team-a", Verb: "list"}
	allow := func(EvidenceRead) bool { return true }
	denyPods := func(read EvidenceRead) bool { return read.Resource != "pods" }

	for _, tc := range []struct {
		name            string
		issue           Issue
		canRead         func(EvidenceRead) bool
		allowUnfiltered bool
		want            bool
	}{
		{"no dependencies, no authorizer", evidenceIssue("plain"), nil, false, true},
		{"dependencies, no authorizer", evidenceIssue("derived", podList), nil, false, false},
		{"dependencies, internal composition", evidenceIssue("derived", podList), nil, true, true},
		{"every read allowed", evidenceIssue("derived", podList, widgetList), allow, false, true},
		{"one read denied", evidenceIssue("derived", podList, widgetList), denyPods, false, false},
		{"an authorizer overrides internal composition", evidenceIssue("derived", podList), denyPods, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanReadIssueEvidence(tc.issue, tc.canRead, tc.allowUnfiltered); got != tc.want {
				t.Fatalf("CanReadIssueEvidence = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRelatedIssuesFromAuthorizesEvidence(t *testing.T) {
	podList := EvidenceRead{Resource: "pods", Namespace: "team-a", Verb: "list"}
	flat := []Issue{evidenceIssue("flat", podList)}
	grouped := []Issue{evidenceIssue("grouped", podList)}
	lookup := func(opts RelatedIssueOptions) []Issue {
		return RelatedIssuesFrom(flat, grouped, opts, "example.com", "Widget", "team-a", "w")
	}
	if got := lookup(RelatedIssueOptions{}); len(got) != 0 {
		t.Fatalf("findings with inventory dependencies returned without an authorizer: %+v", got)
	}
	if got := lookup(RelatedIssueOptions{CanReadEvidence: func(EvidenceRead) bool { return false }}); len(got) != 0 {
		t.Fatalf("denied evidence returned: %+v", got)
	}
	if got := lookup(RelatedIssueOptions{CanReadEvidence: func(EvidenceRead) bool { return true }}); len(got) == 0 {
		t.Fatal("authorized evidence withheld")
	}
}

// RequiredReads is internal bookkeeping for authorization, never part of an
// issue a client receives.
func TestRequiredReadsAreNotSerialized(t *testing.T) {
	data, err := json.Marshal(evidenceIssue("derived", EvidenceRead{Resource: "pods", Namespace: "team-a", Verb: "list"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "RequiredReads") || strings.Contains(string(data), `"verb"`) {
		t.Fatalf("issue JSON exposes its evidence reads: %s", data)
	}
}

func TestEvidenceWithheldFindingsAreReported(t *testing.T) {
	podList := EvidenceRead{Resource: "pods", Namespace: "team-a", Verb: "list"}
	issue := evidenceIssue("derived", podList)
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
	if out := filterEvidenceAccess([]Issue{issue, evidenceIssue("plain")}, opts.CanReadEvidence, false, opts.OnEvidenceWithheld); len(out) != 1 || out[0].ID != "plain" {
		t.Fatalf("filterEvidenceAccess kept %+v", out)
	}
	if len(seen) != 1 || seen[0] != "derived" {
		t.Fatalf("filterEvidenceAccess reported %v", seen)
	}
}
