package ai

import (
	"errors"

	"github.com/skyhook-io/radar/pkg/investigation"
)

var ErrInvalidExplanation = errors.New("explanation requires a completed assessment from this investigation")

// The reference is a durable event sequence, not a client-provided diagnosis.
func (r *Run) assessmentForExplanation(seq int) (*Diagnosis, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq <= 0 || seq > len(r.events) {
		return nil, ErrInvalidExplanation
	}
	ev := r.events[seq-1].Event
	if ev.Type != "done" || ev.Diag == nil || !ev.Diag.Structured() {
		return nil, ErrInvalidExplanation
	}
	for i := seq - 2; i >= 0; i-- {
		turn := r.events[i].Event
		if turn.Type != "turn" {
			continue
		}
		if !ev.Diag.Assesses(investigation.Turn{
			Question: turn.Question, Apply: turn.Apply, Verify: turn.Verify, Explanation: turn.ExplainAssessment != 0,
		}) {
			return nil, ErrInvalidExplanation
		}
		assessment := *ev.Diag
		return &assessment, nil
	}
	return nil, ErrInvalidExplanation
}
