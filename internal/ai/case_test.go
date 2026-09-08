package ai

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/investigationrefs"
)

func caseJSON(fields string) string {
	return "```json\n{" + fields + "}\n```"
}

func TestDiagnosisFromText_ParsesCaseWithoutTouchingLegacyRefs(t *testing.T) {
	first := testEvidenceRef('a', 'b')
	second := testEvidenceRef('c', 'd')
	text := caseJSON(`"root_cause":"bad tag","root_cause_evidence_refs":["` + first + `"],` +
		`"evidence":[` +
		`{"ref":"` + first + `","role":"cause","claim":"The image tag does not exist.","subject":{"kind":"Deployment","group":"apps","namespace":"shop","name":"api","observation":"resource"}},` +
		`{"ref":"` + second + `","role":"Rules_Out","claim":"  Same host, same user, connects fine.  "},` +
		`{"ref":"` + second + `","role":"symptom","claim":"The previous container exited on OOM.","subject":{"kind":"Pod","name":"api-1","container":"app","stream":"previous"}}` +
		`],"ruled_out":[{"hypothesis":"Atlas account locked","evidence_index":1}]`)
	d := diagnosisFromText(text)
	if d.RootCause != "bad tag" || len(d.evidenceRequest.refs) != 1 || d.evidenceRequest.refs[0] != first {
		t.Fatalf("legacy request changed: %+v", d.evidenceRequest)
	}
	if d.Evidence != nil || d.RuledOut != nil {
		t.Fatal("untrusted case must not enter the public diagnosis before run binding")
	}
	items := d.caseRequest.items
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if !items[0].valid || items[0].role != EvidenceRoleCause || items[0].subject == nil ||
		items[0].subject.Group == nil || *items[0].subject.Group != "apps" || items[0].subject.Observation != "resource" {
		t.Fatalf("item 0 = %+v", items[0])
	}
	if !items[1].valid || items[1].role != EvidenceRoleRulesOut || items[1].claim != "Same host, same user, connects fine." || items[1].subject != nil {
		t.Fatalf("item 1 = %+v", items[1])
	}
	if !items[2].valid || items[2].subject == nil || items[2].subject.Stream != "previous" || items[2].subject.Container != "app" ||
		items[2].subject.Group != nil || items[2].subject.Namespace != nil {
		t.Fatalf("item 2 = %+v", items[2])
	}
	explicitCore := diagnosisFromText(caseJSON(`"root_cause":"x","evidence":[{"ref":"` + first + `","role":"cause","claim":"c","subject":{"kind":"Service","group":"","namespace":"","name":"api"}}]`))
	subject := explicitCore.caseRequest.items[0].subject
	if subject == nil || subject.Group == nil || *subject.Group != "" || subject.Namespace == nil || *subject.Namespace != "" {
		t.Fatalf("explicit empty group/namespace must survive parsing: %+v", subject)
	}
	wire, _ := json.Marshal(DiagnosisEvidenceItem{Status: EvidenceLinked, Subject: subject})
	if !strings.Contains(string(wire), `"group":""`) || !strings.Contains(string(wire), `"namespace":""`) {
		t.Fatalf("explicit empty group/namespace must reach the wire: %s", wire)
	}
	if len(d.caseRequest.ruledOut) != 1 || d.caseRequest.ruledOut[0].EvidenceIndex != 1 {
		t.Fatalf("ruled out = %+v", d.caseRequest.ruledOut)
	}
}

func TestDiagnosisFromText_CaseDropsBadItemsIndividually(t *testing.T) {
	ref := testEvidenceRef('a', 'b')
	long := strings.Repeat("x", maxDiagnosisClaimChars+1)
	text := caseJSON(`"root_cause":"x","evidence":[` +
		`{"ref":"not-a-ref","role":"cause","claim":"a"},` +
		`{"ref":"` + ref + `","role":"verdict","claim":"a"},` +
		`{"ref":"` + ref + `","role":"cause","claim":"` + long + `"},` +
		`{"ref":"` + ref + `","role":"cause","claim":"a","subject":{"kind":"Pod"}},` +
		`{"ref":"` + ref + `","role":"cause","claim":"a","subject":{"kind":"Pod","name":"p","stream":"older"}},` +
		`{"ref":"` + ref + `","role":"context","claim":"fine"}` +
		`],"ruled_out":[{"hypothesis":"","evidence_index":5},{"hypothesis":"h","evidence_index":-1},{"hypothesis":"h"},"not-an-object",{"hypothesis":"kept","evidence_index":5}]`)
	d := diagnosisFromText(text)
	items := d.caseRequest.items
	if len(items) != 6 {
		t.Fatalf("items = %d, want 6 (positions preserved)", len(items))
	}
	for i := 0; i < 5; i++ {
		if items[i].valid {
			t.Errorf("item %d should be invalid: %+v", i, items[i])
		}
	}
	if !items[5].valid {
		t.Fatalf("valid sibling dropped: %+v", items[5])
	}
	if len(d.caseRequest.ruledOut) != 1 || d.caseRequest.ruledOut[0].Hypothesis != "kept" {
		t.Fatalf("ruled out = %+v", d.caseRequest.ruledOut)
	}
}

// Every role the prompt offers must survive the parser. A role the prompt asks
// for but the parser rejects is dropped as unlinked, which is invisible: the
// chip never appears and the healthy-conflict banner can never reframe, with
// nothing anywhere saying why.
func TestDiagnosisFromText_ParserAcceptsEveryPromptedRole(t *testing.T) {
	ref := testEvidenceRef('a', 'b')
	roles := []EvidenceRole{
		EvidenceRoleCause,
		EvidenceRoleSymptom,
		EvidenceRoleContext,
		EvidenceRoleBenign,
		EvidenceRoleDemoted,
		EvidenceRoleRulesOut,
	}
	for _, role := range roles {
		if !strings.Contains(diagnosisJSONInstruction, string(role)) {
			t.Errorf("role %q is accepted but the prompt never offers it", role)
		}
		text := caseJSON(`"root_cause":"x","evidence":[` +
			`{"ref":"` + ref + `","role":"` + string(role) + `","claim":"a"}]`)
		items := diagnosisFromText(text).caseRequest.items
		if len(items) != 1 || !items[0].valid {
			t.Errorf("role %q was rejected by the parser: %+v", role, items)
			continue
		}
		if items[0].role != role {
			t.Errorf("role %q parsed as %q", role, items[0].role)
		}
	}
}

func TestDiagnosisFromText_CaseCapsAndMalformedArrays(t *testing.T) {
	ref := testEvidenceRef('a', 'b')
	var items []string
	for i := 0; i < maxDiagnosisEvidenceItems+3; i++ {
		items = append(items, `{"ref":"`+ref+`","role":"context","claim":"c"}`)
	}
	var ruled []string
	for i := 0; i < maxDiagnosisRuledOut+2; i++ {
		ruled = append(ruled, `{"hypothesis":"h","evidence_index":0}`)
	}
	d := diagnosisFromText(caseJSON(`"root_cause":"x","evidence":[` + strings.Join(items, ",") + `],"ruled_out":[` + strings.Join(ruled, ",") + `]`))
	if len(d.caseRequest.items) != maxDiagnosisEvidenceItems {
		t.Fatalf("items = %d, want cap %d", len(d.caseRequest.items), maxDiagnosisEvidenceItems)
	}
	if len(d.caseRequest.ruledOut) != maxDiagnosisRuledOut {
		t.Fatalf("ruled out = %d, want cap %d", len(d.caseRequest.ruledOut), maxDiagnosisRuledOut)
	}
	if d.caseRequest.dropped != 3 {
		t.Fatalf("dropped = %d, want the 3 items the cap cut", d.caseRequest.dropped)
	}
	for _, field := range []string{`null`, `"text"`, `{"ref":"` + ref + `"}`} {
		d := diagnosisFromText(caseJSON(`"root_cause":"still","evidence":` + field + `,"ruled_out":` + field))
		if d.RootCause != "still" || len(d.caseRequest.items) != 0 || len(d.caseRequest.ruledOut) != 0 {
			t.Errorf("field %s: root cause %q, case %+v", field, d.RootCause, d.caseRequest)
		}
	}
	missing := diagnosisFromText(caseJSON(`"root_cause":"old response"`))
	if len(missing.caseRequest.items) != 0 || len(missing.caseRequest.ruledOut) != 0 {
		t.Fatalf("omitted case = %+v", missing.caseRequest)
	}
}

// The parser must reject what it cannot understand without inventing a
// reading of it: no truncated claims, no unknown role mapped onto a known
// one, and an empty claim treated as the non-answer it is. Only the marker
// wrapper is unwrapped, because that text is Radar's own, quoted back.
func TestDiagnosisFromText_CaseRejectsEmptyClaimAndUnwrapsMarkerRef(t *testing.T) {
	ref := testEvidenceRef('a', 'b')
	long := strings.Repeat("x", maxDiagnosisClaimChars+1)
	d := diagnosisFromText(caseJSON(`"root_cause":"x","evidence":[` +
		`{"ref":"` + ref + `","role":"cause","claim":""},` +
		`{"ref":"` + ref + `","role":"cause","claim":"   "},` +
		`{"ref":"[[radar:evidence-ref=` + ref + `]]","role":"cause","claim":"Pasted the whole marker."},` +
		`{"ref":"` + ref + `","role":"cause","claim":"` + long + `"},` +
		`{"ref":"` + ref + `","role":"verdict","claim":"An unknown role."}` +
		`]`))
	items := d.caseRequest.items
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5 positions", len(items))
	}
	if items[0].valid || items[1].valid {
		t.Errorf("an empty claim must be rejected: %+v %+v", items[0], items[1])
	}
	if !items[2].valid || items[2].ref != ref {
		t.Errorf("a marker-wrapped ref must be unwrapped, got %+v", items[2])
	}
	if items[3].valid {
		t.Errorf("an over-long claim must be dropped, not truncated: %+v", items[3])
	}
	if items[4].valid {
		t.Errorf("an unknown role must be dropped, not mapped: %+v", items[4])
	}
}

// Every entry the case loses is counted, whether it was rejected at parse
// time, failed to bind, or was cut by the cap before it could get a slot.
func TestBindCaseCountsUnlinkedEvidence(t *testing.T) {
	scope := strings.Repeat("a", 26)
	good := "ev_" + scope + "_" + strings.Repeat("b", 26)
	foreign := "ev_" + strings.Repeat("z", 26) + "_" + strings.Repeat("d", 26)
	events := []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(good, nil)}
	got := bindCaseForTest(events, Diagnosis{RootCause: "x", caseRequest: caseRequest{
		dropped: 3,
		items: []caseItemRequest{
			{valid: true, ref: good, role: EvidenceRoleCause, claim: "kept"},
			{valid: false},
			{valid: true, ref: foreign, role: EvidenceRoleCause, claim: "other scope"},
		},
	}}, scope)
	if got.UnlinkedEvidence != 5 {
		t.Fatalf("unlinked evidence = %d, want 3 cut by the cap plus 2 that failed", got.UnlinkedEvidence)
	}
	clean := bindCaseForTest(events, Diagnosis{RootCause: "x", caseRequest: caseRequest{
		items: []caseItemRequest{{valid: true, ref: good, role: EvidenceRoleCause, claim: "kept"}},
	}}, scope)
	if clean.UnlinkedEvidence != 0 {
		t.Fatalf("a fully linked case must report no loss, got %d", clean.UnlinkedEvidence)
	}
}

func bindCaseForTest(events []RunEvent, diagnosis Diagnosis, scope string) Diagnosis {
	issued := make(investigationrefs.Records)
	for _, event := range events {
		if step := event.Event.Step; step != nil && step.EvidenceRef != "" {
			issued[step.EvidenceRef] = step.Result
		}
	}
	diagnosis.evidenceScope = scope
	diagnosis.issuedEvidence = issued
	run := &Run{events: events}
	run.mu.Lock()
	run.bindRootCauseEvidenceLocked(&diagnosis)
	run.mu.Unlock()
	return diagnosis
}

func TestBindCaseLinksItemsIndividuallyAndKeepsIndexes(t *testing.T) {
	scope := strings.Repeat("a", 26)
	good := "ev_" + scope + "_" + strings.Repeat("b", 26)
	failed := "ev_" + scope + "_" + strings.Repeat("c", 26)
	foreign := "ev_" + strings.Repeat("z", 26) + "_" + strings.Repeat("d", 26)
	events := []RunEvent{
		{Event: StreamEvent{Type: "turn"}},
		evidenceStep(good, nil),
		evidenceStep(failed, func(step *StepInfo) {
			step.ID = "call-2"
			step.IsError = boolPointer(true)
		}),
	}
	namespace := "shop"
	subject := &DiagnosisEvidenceSubject{Kind: "Pod", Namespace: &namespace, Name: "api-1", Container: "app", Stream: "current"}
	got := bindCaseForTest(events, Diagnosis{
		Healthy: true,
		caseRequest: caseRequest{
			items: []caseItemRequest{
				{valid: true, ref: good, role: EvidenceRoleContext, claim: "Checked, fine.", subject: subject},
				{valid: true, ref: failed, role: EvidenceRoleCause, claim: "failed step"},
				{valid: true, ref: foreign, role: EvidenceRoleCause, claim: "other scope"},
				{valid: false},
				{valid: true, ref: good, role: EvidenceRoleRulesOut, claim: "Same ref, second item."},
			},
			ruledOut: []DiagnosisRuledOut{
				{Hypothesis: "kept", EvidenceIndex: 4},
				{Hypothesis: "unlinked target", EvidenceIndex: 1},
				{Hypothesis: "out of range", EvidenceIndex: 5},
				{Hypothesis: "context target is fine", EvidenceIndex: 0},
			},
		},
	}, scope)
	if got.RootCauseEvidence != nil {
		t.Fatalf("healthy assessment must not gain root-cause provenance: %+v", got.RootCauseEvidence)
	}
	if len(got.Evidence) != 5 {
		t.Fatalf("evidence = %+v, want 5 positional items", got.Evidence)
	}
	want := []EvidenceLinkStatus{EvidenceLinked, EvidenceUnlinked, EvidenceUnlinked, EvidenceUnlinked, EvidenceLinked}
	for i, item := range got.Evidence {
		if item.Status != want[i] {
			t.Errorf("item %d status = %s, want %s", i, item.Status, want[i])
		}
		if item.Status == EvidenceUnlinked && (item.Ref != "" || item.Claim != "" || item.Role != "" || item.Subject != nil) {
			t.Errorf("unlinked item %d leaked request data: %+v", i, item)
		}
	}
	if got.Evidence[0].Subject == nil || !reflect.DeepEqual(*got.Evidence[0].Subject, *subject) ||
		got.Evidence[0].Subject == subject || got.Evidence[0].Subject.Namespace == subject.Namespace {
		t.Fatalf("subject must be copied verbatim: %+v", got.Evidence[0].Subject)
	}
	if got.Evidence[4].Role != EvidenceRoleRulesOut || got.Evidence[4].Ref != good {
		t.Fatalf("second item on a shared ref = %+v", got.Evidence[4])
	}
	if len(got.RuledOut) != 2 || got.RuledOut[0].Hypothesis != "kept" || got.RuledOut[1].EvidenceIndex != 0 {
		t.Fatalf("ruled out = %+v", got.RuledOut)
	}
	if got.caseRequest.items != nil || got.issuedEvidence != nil {
		t.Fatal("private case request must be cleared after binding")
	}
}

func TestBindCaseFailsClosedWithoutScopeAndClearsOnExplanation(t *testing.T) {
	scope := strings.Repeat("a", 26)
	ref := "ev_" + scope + "_" + strings.Repeat("b", 26)
	events := []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(ref, nil)}
	request := caseRequest{items: []caseItemRequest{{valid: true, ref: ref, role: EvidenceRoleCause, claim: "c"}}}
	got := bindCaseForTest(events, Diagnosis{RootCause: "x", caseRequest: request}, "")
	if len(got.Evidence) != 1 || got.Evidence[0].Status != EvidenceUnlinked {
		t.Fatalf("missing scope must unlink: %+v", got.Evidence)
	}
	empty := bindCaseForTest(events, Diagnosis{RootCause: "x"}, scope)
	if empty.Evidence != nil || empty.RuledOut != nil {
		t.Fatalf("no request must leave the case absent: %+v", empty)
	}
}

func TestExplanationPromptCarriesCaseNotesReadOnly(t *testing.T) {
	prompt := explanationPrompt(Diagnosis{
		RootCause: "stale password",
		Evidence: []DiagnosisEvidenceItem{
			{Status: EvidenceLinked, Role: EvidenceRoleCause, Claim: "Atlas rejects this password."},
			{Status: EvidenceUnlinked},
			{Status: EvidenceLinked, Role: EvidenceRoleContext},
		},
		RuledOut: []DiagnosisRuledOut{{Hypothesis: "Account locked", EvidenceIndex: 0}},
	})
	if !strings.Contains(prompt, `"cause: Atlas rejects this password."`) || !strings.Contains(prompt, `"Account locked"`) {
		t.Fatalf("prompt lacks case notes: %s", prompt)
	}
	if !strings.Contains(prompt, "must not add, change, or reassign") {
		t.Fatalf("prompt does not forbid new roles: %s", prompt)
	}
	bare := explanationPrompt(Diagnosis{RootCause: "x"})
	if strings.Contains(bare, `"evidenceNotes"`) || strings.Contains(bare, `"ruledOut"`) {
		t.Fatalf("empty case must not appear in the prompt: %s", bare)
	}
}
