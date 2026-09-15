package ai

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/investigationrefs"
)

func TestDiagnosisFromText_ParsesStoryFields(t *testing.T) {
	text := "The app crashes on start.\n\n[[radar:evidence=0]]\n\nSo the build changed.\n\n" + caseJSON(
		`"summary":"  The app cannot log in to its database, and it started with the last deploy.  ",`+
			`"certainty":"Likely","unresolved":["Atlas password rotated?","","what would settle it","a fourth item"],`+
			`"root_cause":"Auth fails after revision 8.",`+
			`"steps":[{"text":"Roll back to revision 7","kind":"mitigate","precondition":"if revision 7 still authenticates"},`+
			`{"text":"Test the stored password with mongosh","kind":"VERIFY"},`+
			`{"text":"","kind":"investigate"},{"text":"Compare the two builds","kind":"guess"}],`+
			`"recommended_index":1,"recommended_reason":"reversible","revises_assessment":true`)
	d := diagnosisFromText(text)
	if d.Summary != "The app cannot log in to its database, and it started with the last deploy." {
		t.Fatalf("summary = %q", d.Summary)
	}
	if len(d.Unresolved) != 3 || d.Unresolved[0] != "Atlas password rotated?" || d.Unresolved[1] != "what would settle it" {
		t.Fatalf("unresolved = %v (empty dropped, capped at %d)", d.Unresolved, maxDiagnosisUnresolved)
	}
	if len(d.Steps) != 2 || d.Steps[0].Kind != StepMitigate || d.Steps[1].Kind != StepVerify ||
		d.Steps[0].Precondition != "if revision 7 still authenticates" {
		t.Fatalf("steps = %+v (empty text and unknown kind dropped, kind lower-cased)", d.Steps)
	}
	if len(d.Remediation) != 2 || d.Remediation[0] != "Roll back to revision 7" {
		t.Fatalf("remediation must be derived from steps, got %v", d.Remediation)
	}
	if d.RecommendedIndex != nil {
		t.Fatalf("a mitigate step with an unverified precondition is not a one-click fix: %v", d.RecommendedIndex)
	}
	if d.Certainty != CertaintyLikely {
		t.Fatalf("certainty = %q", d.Certainty)
	}
	if !d.RevisesAssessment {
		t.Fatal("revises_assessment not parsed")
	}
	if !strings.Contains(d.Report, "[[radar:evidence=0]]") || strings.Contains(d.Report, "```json") {
		t.Fatalf("report must keep placement markers and lose the verdict block: %q", d.Report)
	}
}

func TestDiagnosisFromText_EstablishedCannotCoexistWithUnresolved(t *testing.T) {
	d := diagnosisFromText(caseJSON(`"certainty":"established","unresolved":["whether the password was rotated"],"root_cause":"x"`))
	if d.Certainty != CertaintyLikely {
		t.Fatalf("established with open questions must read as likely, got %q", d.Certainty)
	}
	d = diagnosisFromText(caseJSON(`"certainty":"established","unresolved":[],"root_cause":"x","steps":[{"text":"roll back","kind":"mitigate"}],"recommended_index":1`))
	if d.Certainty != CertaintyEstablished || d.RecommendedIndex == nil {
		t.Fatalf("an unconditional mitigate step stays recommendable: %+v", d)
	}
}

func TestDiagnosisFromText_StoryFieldCapsAndValidation(t *testing.T) {
	d := diagnosisFromText(caseJSON(`"summary":"` + strings.Repeat("s", maxDiagnosisSummaryRune+40) + `","certainty":"certain"`))
	if len([]rune(d.Summary)) != maxDiagnosisSummaryRune+1 || !strings.HasSuffix(d.Summary, "…") {
		t.Fatalf("summary not clamped with an ellipsis: %d %q", len([]rune(d.Summary)), d.Summary[len(d.Summary)-6:])
	}
	sentence := strings.Repeat("Word ", 40) + "end. " + strings.Repeat("more ", 40)
	d = diagnosisFromText(caseJSON(`"summary":"` + sentence + `"`))
	if !strings.HasSuffix(d.Summary, "end.") {
		t.Fatalf("an over-long summary must cut at the last whole sentence, got %q", d.Summary)
	}
	if d.Certainty != "" {
		t.Fatalf("unknown certainty must be dropped, got %q", d.Certainty)
	}
	d = diagnosisFromText(caseJSON(`"steps":[` + strings.Repeat(`{"text":"x","kind":"verify"},`, maxDiagnosisSteps+2) + `{"text":"last","kind":"verify"}]`))
	if len(d.Steps) != maxDiagnosisSteps {
		t.Fatalf("steps not capped: %d", len(d.Steps))
	}
}

// A mitigation never establishes a cause: with typed steps, "cause unresolved;
// roll back to restore service" stays inconclusive. The legacy remediation-only
// shape keeps its old precedence so older transcripts do not change meaning.
func TestDiagnosisFromText_InconclusiveSurvivesTypedSteps(t *testing.T) {
	typed := diagnosisFromText(caseJSON(`"inconclusive":true,"steps":[{"text":"Roll back","kind":"mitigate"},{"text":"Check Atlas","kind":"verify"}],"recommended_index":2`))
	if !typed.Inconclusive || typed.Healthy {
		t.Fatalf("typed steps must not clear inconclusive: %+v", typed)
	}
	if typed.RecommendedIndex != nil {
		t.Fatalf("a verify step can never be the Apply target: %v", typed.RecommendedIndex)
	}
	legacy := diagnosisFromText(caseJSON(`"inconclusive":true,"remediation":["Roll back"],"recommended_index":1`))
	if legacy.Inconclusive || legacy.RecommendedIndex == nil {
		t.Fatalf("legacy remediation keeps the old precedence: %+v", legacy)
	}
	cause := diagnosisFromText(caseJSON(`"inconclusive":true,"healthy":true,"root_cause":"x","steps":[{"text":"Roll back","kind":"mitigate"}]`))
	if cause.Inconclusive || cause.Healthy {
		t.Fatalf("a root cause clears both flags: %+v", cause)
	}
}

// The flag alone can never retire the assessment a reader is looking at: a
// revision must carry a headline and a verdict, or the turn is an answer.
func TestFinishTurnKeepsRevisionOnlyForCompleteVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		diag Diagnosis
		want bool
	}{
		{"complete", Diagnosis{RevisesAssessment: true, Summary: "It changed.", RootCause: "x"}, true},
		{"healthy", Diagnosis{RevisesAssessment: true, Summary: "Fine now.", Healthy: true}, true},
		{"no summary", Diagnosis{RevisesAssessment: true, RootCause: "x"}, false},
		{"no verdict", Diagnosis{RevisesAssessment: true, Summary: "Words."}, false},
	} {
		r := &Run{ID: "run-1", status: "running", inFlight: true, subs: map[int]chan RunEvent{}}
		r.append(StreamEvent{Type: "turn", Question: "did it change?"})
		r.finishTurn(tc.diag, nil, false, time.Minute)
		got := r.events[len(r.events)-1].Event.Diag
		if got.RevisesAssessment != tc.want {
			t.Errorf("%s: revisesAssessment = %v, want %v", tc.name, got.RevisesAssessment, tc.want)
		}
		if tc.want && tc.diag.Summary != "" && r.preview != tc.diag.Summary {
			t.Errorf("%s: preview should be the summary, got %q", tc.name, r.preview)
		}
	}
}

// An assessment may rest on results read in earlier turns of the same run.
// Their proof is the RadarEvidence flag the earlier turn's validator persisted;
// the current turn is still held to the exact private-transport payload.
func TestBindEvidenceAcceptsEarlierTurnsOfTheSameRun(t *testing.T) {
	earlierScope := strings.Repeat("e", 26)
	currentScope := strings.Repeat("c", 26)
	earlierRef := "ev_" + earlierScope + "_" + strings.Repeat("1", 26)
	currentRef := "ev_" + currentScope + "_" + strings.Repeat("2", 26)
	forgedCurrentRef := "ev_" + currentScope + "_" + strings.Repeat("3", 26)
	unflaggedRef := "ev_" + earlierScope + "_" + strings.Repeat("4", 26)
	events := []RunEvent{
		{Event: StreamEvent{Type: "turn"}},
		evidenceStep(earlierRef, func(s *StepInfo) { s.ID = "call-1"; s.Result = `{"kind":"Pod","from":"turn 1"}` }),
		// Same host call ID as the current turn's, with a different tool: the
		// identity table must be per turn or this would read as a conflict.
		evidenceStep(unflaggedRef, func(s *StepInfo) { s.ID = "call-2"; s.Tool = "get_events"; s.RadarEvidence = false }),
		// A ref that carries the CURRENT turn's scope but sits in an earlier
		// turn cannot have been observed then; it is a replay, not evidence.
		evidenceStep(forgedCurrentRef, func(s *StepInfo) { s.ID = "call-3" }),
		{Event: StreamEvent{Type: "turn", Question: "and now?"}},
		evidenceStep(currentRef, func(s *StepInfo) {
			s.ID = "call-2"
			s.Tool = "get_resource"
			s.Result = `{"kind":"Pod","from":"turn 2"}`
		}),
	}
	issued := investigationrefs.Records{currentRef: `{"kind":"Pod","from":"turn 2"}`}
	bind := func(refs ...string) EvidenceLinkStatus {
		got := bindEvidenceWithIssued(events, evidenceReferenceRequest{present: true, refs: refs}, currentScope, issued)
		return got.Status
	}
	if got := bind(earlierRef, currentRef); got != EvidenceLinked {
		t.Fatalf("earlier-turn and current-turn refs together: %s, want linked", got)
	}
	if got := bind(unflaggedRef); got != EvidenceInvalid {
		t.Fatalf("an earlier step without the persisted RadarEvidence flag must not bind: %s", got)
	}
	if got := bind(forgedCurrentRef); got != EvidenceInvalid {
		t.Fatalf("a current-scope ref replayed in an earlier turn must not bind: %s", got)
	}
	if got := bind("ev_" + strings.Repeat("z", 26) + "_" + strings.Repeat("9", 26)); got != EvidenceInvalid {
		t.Fatalf("a ref from another run must not bind: %s", got)
	}
}

func TestExplanationPromptCarriesSummaryAndStripsMarkers(t *testing.T) {
	prompt := explanationPrompt(Diagnosis{
		Summary:    "Plain headline.",
		RootCause:  "Technical cause.",
		Unresolved: []string{"Atlas rotation"},
		Report:     "The pod crashes.\n\n[[radar:evidence=0]]\n\nBecause of auth.",
	})
	if !strings.Contains(prompt, `"summary":"Plain headline."`) || !strings.Contains(prompt, "Atlas rotation") {
		t.Fatalf("summary and unresolved missing from the explanation context: %s", prompt)
	}
	if strings.Contains(prompt, "[[radar:evidence=") {
		t.Fatalf("placement markers must be stripped for the explaining model: %s", prompt)
	}
}
