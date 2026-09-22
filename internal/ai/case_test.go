package ai

import (
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/pkg/investigation"
)

func caseJSON(fields string) string {
	return "```json\n{" + fields + "}\n```"
}

// bindCaseForTest runs the run manager's own eligibility over events and
// binds the citations the parser read from text, the way finishTurn does.
func bindCaseForTest(events []RunEvent, text string, scope string) Diagnosis {
	issued := make(investigationrefs.Records)
	for _, event := range events {
		if step := event.Event.Step; step != nil && step.EvidenceRef != "" {
			issued[step.EvidenceRef] = step.Result
		}
	}
	diagnosis := diagnosisFromText(text)
	diagnosis.evidenceScope = scope
	diagnosis.issuedEvidence = issued
	run := &Run{events: events}
	run.mu.Lock()
	run.bindRootCauseEvidenceLocked(&diagnosis)
	run.mu.Unlock()
	return diagnosis
}

// The run's eligibility feeds the shared binder: an item whose ref this run
// never issued is unlinked, and every loss is counted alongside the entries
// the cap cut before parsing.
func TestBindCaseCountsUnlinkedEvidenceAgainstTheRun(t *testing.T) {
	scope := strings.Repeat("a", 26)
	good := "ev_" + scope + "_" + strings.Repeat("b", 26)
	foreign := "ev_" + strings.Repeat("z", 26) + "_" + strings.Repeat("d", 26)
	events := []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(good, nil)}
	items := []string{
		`{"ref":"` + good + `","role":"cause","claim":"kept"}`,
		`{"ref":"bad","role":"cause","claim":"rejected at parse time"}`,
		`{"ref":"` + foreign + `","role":"cause","claim":"other scope"}`,
	}
	for range 3 + investigation.MaxEvidenceItems - len(items) {
		items = append(items, `{"ref":"`+good+`","role":"context","claim":"filler"}`)
	}
	got := bindCaseForTest(events, caseJSON(`"root_cause":"x","evidence":[`+strings.Join(items, ",")+`]`), scope)
	if got.UnlinkedEvidence != 5 {
		t.Fatalf("unlinked evidence = %d, want 3 cut by the cap plus 2 that failed", got.UnlinkedEvidence)
	}
	if got.Evidence[0].Status != investigation.Linked || got.Evidence[0].Ref != good {
		t.Fatalf("the issued ref must link: %+v", got.Evidence[0])
	}
	clean := bindCaseForTest(events, caseJSON(`"root_cause":"x","evidence":[{"ref":"`+good+`","role":"cause","claim":"kept"}]`), scope)
	if clean.UnlinkedEvidence != 0 {
		t.Fatalf("a fully linked case must report no loss, got %d", clean.UnlinkedEvidence)
	}
	if !reflect.DeepEqual(clean.citations, investigation.Citations{}) || clean.issuedEvidence != nil {
		t.Fatal("private citation material must be cleared after binding")
	}
}

func TestBindCaseFailsClosedWithoutScope(t *testing.T) {
	scope := strings.Repeat("a", 26)
	ref := "ev_" + scope + "_" + strings.Repeat("b", 26)
	events := []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(ref, nil)}
	text := caseJSON(`"root_cause":"x","evidence":[{"ref":"` + ref + `","role":"cause","claim":"c"}]`)
	got := bindCaseForTest(events, text, "")
	if len(got.Evidence) != 1 || got.Evidence[0].Status != investigation.Unlinked {
		t.Fatalf("missing scope must unlink: %+v", got.Evidence)
	}
	empty := bindCaseForTest(events, caseJSON(`"root_cause":"x"`), scope)
	if empty.Evidence != nil || empty.RuledOut != nil {
		t.Fatalf("no request must leave the case absent: %+v", empty)
	}
}
