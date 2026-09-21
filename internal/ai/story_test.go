package ai

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/pkg/investigation"
)

// The flag alone can never retire the assessment a reader is looking at: a
// revision must carry a headline and a verdict, or the turn is an answer.
func TestFinishTurnKeepsRevisionOnlyForCompleteVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		diag investigation.Verdict
		want bool
	}{
		{"complete", investigation.Verdict{RevisesAssessment: true, Summary: "It changed.", RootCause: "x"}, true},
		{"healthy", investigation.Verdict{RevisesAssessment: true, Summary: "Fine now.", Healthy: true}, true},
		{"no summary", investigation.Verdict{RevisesAssessment: true, RootCause: "x"}, false},
		{"no verdict", investigation.Verdict{RevisesAssessment: true, Summary: "Words."}, false},
	} {
		r := &Run{ID: "run-1", status: "running", inFlight: true, subs: map[int]chan RunEvent{}}
		r.append(StreamEvent{Type: "turn", Question: "did it change?"})
		r.finishTurn(Diagnosis{Verdict: tc.diag}, nil, false, time.Minute)
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
	earlierRef := "ev_" + earlierScope + "_" + strings.Repeat("b", 26)
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
	bind := func(refs ...string) investigation.LinkStatus {
		return bindEvidenceWithIssued(events, rootCauseCitations(refs), currentScope, issued).Status
	}
	if got := bind(earlierRef, currentRef); got != investigation.Linked {
		t.Fatalf("earlier-turn and current-turn refs together: %s, want linked", got)
	}
	if got := bind(unflaggedRef); got != investigation.Invalid {
		t.Fatalf("an earlier step without the persisted RadarEvidence flag must not bind: %s", got)
	}
	if got := bind(forgedCurrentRef); got != investigation.Invalid {
		t.Fatalf("a current-scope ref replayed in an earlier turn must not bind: %s", got)
	}
	if got := bind("ev_" + strings.Repeat("z", 26) + "_" + strings.Repeat("y", 26)); got != investigation.Invalid {
		t.Fatalf("a ref from another run must not bind: %s", got)
	}
}
