package investigation

import (
	"reflect"
	"strings"
	"testing"
)

func linkedSet(refs ...string) func(string) bool {
	set := map[string]bool{}
	for _, ref := range refs {
		set[ref] = true
	}
	return func(ref string) bool { return set[ref] }
}

func TestBindRootCauseStatuses(t *testing.T) {
	good := testRef('a', 'b')
	bad := testRef('a', 'c')
	for _, tc := range []struct {
		name   string
		fields string
		want   LinkStatus
		refs   int
	}{
		{"omitted", `"root_cause":"x"`, Missing, 0},
		{"empty", `"root_cause":"x","root_cause_evidence_refs":[]`, Missing, 0},
		{"parser rejected", `"root_cause":"x","root_cause_evidence_refs":null`, Invalid, 0},
		{"linked", `"root_cause":"x","root_cause_evidence_refs":["` + good + `"]`, Linked, 1},
		// One unlinkable ref invalidates the set: a cause resting on a forged
		// citation has no provenance at all.
		{"one bad invalidates all", `"root_cause":"x","root_cause_evidence_refs":["` + good + `","` + bad + `"]`, Invalid, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Parse(verdictJSON(tc.fields))
			Bind(&p.Verdict, p.Citations, linkedSet(good))
			got := p.Verdict.RootCauseEvidence
			if got == nil || got.Status != tc.want || len(got.Refs) != tc.refs {
				t.Fatalf("evidence = %+v, want %s with %d refs", got, tc.want, tc.refs)
			}
		})
	}
	healthy := Parse(verdictJSON(`"healthy":true,"root_cause_evidence_refs":["` + good + `"]`))
	Bind(&healthy.Verdict, healthy.Citations, linkedSet(good))
	if healthy.Verdict.RootCauseEvidence != nil {
		t.Fatalf("a verdict without a root cause must carry no root-cause provenance: %+v", healthy.Verdict.RootCauseEvidence)
	}
}

func TestBindCaseLinksItemsIndividuallyAndKeepsIndexes(t *testing.T) {
	good := testRef('a', 'b')
	failed := testRef('a', 'c')
	foreign := testRef('z', 'd')
	p := Parse(verdictJSON(`"healthy":true,"evidence":[` +
		`{"ref":"` + good + `","role":"context","claim":"Checked, fine.","subject":{"kind":"Pod","namespace":"shop","name":"api-1","container":"app","stream":"current"}},` +
		`{"ref":"` + failed + `","role":"cause","claim":"failed step"},` +
		`{"ref":"` + foreign + `","role":"cause","claim":"other scope"},` +
		`{"ref":"not-a-ref","role":"cause","claim":"invalid at parse time"},` +
		`{"ref":"` + good + `","role":"rules_out","claim":"Same ref, second item."}` +
		`],"ruled_out":[` +
		`{"hypothesis":"kept","evidence_index":4},` +
		`{"hypothesis":"unlinked target","evidence_index":1},` +
		`{"hypothesis":"out of range","evidence_index":5},` +
		`{"hypothesis":"context target is fine","evidence_index":0}]`))
	subject := p.Citations.theCase.items[0].subject
	Bind(&p.Verdict, p.Citations, linkedSet(good))
	got := p.Verdict
	if got.RootCauseEvidence != nil {
		t.Fatalf("healthy assessment must not gain root-cause provenance: %+v", got.RootCauseEvidence)
	}
	if len(got.Evidence) != 5 {
		t.Fatalf("evidence = %+v, want 5 positional items", got.Evidence)
	}
	want := []LinkStatus{Linked, Unlinked, Unlinked, Unlinked, Linked}
	for i, item := range got.Evidence {
		if item.Status != want[i] {
			t.Errorf("item %d status = %s, want %s", i, item.Status, want[i])
		}
		if item.Status == Unlinked && (item.Ref != "" || item.Claim != "" || item.Role != "" || item.Subject != nil) {
			t.Errorf("unlinked item %d leaked request data: %+v", i, item)
		}
	}
	if got.Evidence[0].Subject == nil || !reflect.DeepEqual(*got.Evidence[0].Subject, *subject) ||
		got.Evidence[0].Subject == subject || got.Evidence[0].Subject.Namespace == subject.Namespace {
		t.Fatalf("subject must be copied verbatim: %+v", got.Evidence[0].Subject)
	}
	if got.Evidence[4].Role != RoleRulesOut || got.Evidence[4].Ref != good {
		t.Fatalf("second item on a shared ref = %+v", got.Evidence[4])
	}
	if len(got.RuledOut) != 2 || got.RuledOut[0].Hypothesis != "kept" || got.RuledOut[1].EvidenceIndex != 0 {
		t.Fatalf("ruled out = %+v", got.RuledOut)
	}
	if got.UnlinkedEvidence != 3 {
		t.Fatalf("unlinked evidence = %d, want the 3 items that failed", got.UnlinkedEvidence)
	}
}

// Every entry the case loses is counted, whether it was rejected at parse
// time, failed to bind, or was cut by the cap before it could get a slot.
func TestBindCaseCountsUnlinkedEvidence(t *testing.T) {
	good := testRef('a', 'b')
	foreign := testRef('z', 'd')
	items := []string{
		`{"ref":"` + good + `","role":"cause","claim":"kept"}`,
		`{"ref":"bad","role":"cause","claim":"rejected"}`,
		`{"ref":"` + foreign + `","role":"cause","claim":"other scope"}`,
	}
	for i := 0; i < MaxEvidenceItems; i++ {
		items = append(items, `{"ref":"`+good+`","role":"context","claim":"filler"}`)
	}
	p := Parse(verdictJSON(`"root_cause":"x","evidence":[` + strings.Join(items, ",") + `]`))
	Bind(&p.Verdict, p.Citations, linkedSet(good))
	if p.Verdict.UnlinkedEvidence != 5 {
		t.Fatalf("unlinked evidence = %d, want 3 cut by the cap plus 2 that failed", p.Verdict.UnlinkedEvidence)
	}
	clean := Parse(verdictJSON(`"root_cause":"x","evidence":[{"ref":"` + good + `","role":"cause","claim":"kept"}]`))
	Bind(&clean.Verdict, clean.Citations, linkedSet(good))
	if clean.Verdict.UnlinkedEvidence != 0 {
		t.Fatalf("a fully linked case must report no loss, got %d", clean.Verdict.UnlinkedEvidence)
	}
	malformed := Parse(verdictJSON(`"root_cause":"x","evidence":{"ref":"` + good + `"}`))
	Bind(&malformed.Verdict, malformed.Citations, linkedSet(good))
	if !malformed.Verdict.EvidenceMalformed || malformed.Verdict.Evidence != nil {
		t.Fatalf("a malformed envelope must be reported as such: %+v", malformed.Verdict)
	}
	none := Parse(verdictJSON(`"root_cause":"x"`))
	Bind(&none.Verdict, none.Citations, linkedSet(good))
	if none.Verdict.Evidence != nil || none.Verdict.RuledOut != nil {
		t.Fatalf("no request must leave the case absent: %+v", none.Verdict)
	}
}
