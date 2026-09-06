package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAssessmentForExplanation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marker StreamEvent
		result StreamEvent
		valid  bool
	}{
		{"initial", StreamEvent{Type: "turn"}, StreamEvent{Type: "done", Diag: &Diagnosis{RootCause: "Missing Secret"}}, true},
		{"verification", StreamEvent{Type: "turn", Question: "Recheck", Verify: true}, StreamEvent{Type: "done", Diag: &Diagnosis{RootCause: "Still missing"}}, true},
		{"ordinary question", StreamEvent{Type: "turn", Question: "Why?"}, StreamEvent{Type: "done", Diag: &Diagnosis{RootCause: "An answer"}}, false},
		{"explanation", StreamEvent{Type: "turn", ExplainAssessment: 2}, StreamEvent{Type: "done", Diag: &Diagnosis{RootCause: "An explanation"}}, false},
		{"apply", StreamEvent{Type: "turn", Apply: true}, StreamEvent{Type: "done", Diag: &Diagnosis{RootCause: "Applied"}}, false},
		{"error", StreamEvent{Type: "turn"}, StreamEvent{Type: "error"}, false},
		{"no assessment", StreamEvent{Type: "turn"}, StreamEvent{Type: "done", Diag: &Diagnosis{Report: "No conclusion"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Run{subs: map[int]chan RunEvent{}}
			r.append(tc.marker)
			r.append(tc.result)
			got, err := r.assessmentForExplanation(2)
			if (err == nil) != tc.valid {
				t.Fatalf("assessment=%v err=%v valid=%v", got, err, tc.valid)
			}
			for _, seq := range []int{-1, 0, 1, 3} {
				if _, err := r.assessmentForExplanation(seq); !errors.Is(err, ErrInvalidExplanation) {
					t.Fatalf("seq %d accepted: %v", seq, err)
				}
			}
		})
	}
}

func TestExplanationReusesTurnAndPersistsOrigin(t *testing.T) {
	store, _ := testStore(t)
	m := persistedManager(t, store, "ctx")
	requests := make(chan Request, 1)
	release := make(chan struct{})
	m.diagnose = func(ctx context.Context, req Request, emit func(StreamEvent)) (Diagnosis, error) {
		requests <- req
		select {
		case <-release:
		case <-ctx.Done():
			return Diagnosis{}, ctx.Err()
		}
		return Diagnosis{Report: "Your app cannot start because its required configuration is missing.", SessionID: "session"}, nil
	}
	r := &Run{ID: "explain", Context: "ctx", Agent: "claude", Profile: ExecutionProfileSafeguarded,
		status: "done", sessionID: "session", hydrated: true, store: store, subs: map[int]chan RunEvent{}, CreatedAt: nowUTC(), updatedAt: nowUTC()}
	m.runs[r.ID] = r
	m.order = []string{r.ID}
	store.SaveRun(r.Summary())
	r.append(StreamEvent{Type: "turn"})
	r.append(StreamEvent{Type: "done", Diag: &Diagnosis{RootCause: "Missing Secret", Report: "The saved analysis.", Remediation: []string{"Restore the configuration."}}})
	if err := m.AddExplanation(r.ID, 2); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-requests:
		if req.Explanation == nil || req.Explanation.RootCause != "Missing Secret" || req.SessionID != "session" || req.Apply || req.Verify {
			t.Fatalf("wrong explanation request: %+v", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent not invoked")
	}
	if err := m.AddExplanation(r.ID, 2); !errors.Is(err, ErrTurnInFlight) {
		t.Fatalf("concurrent explanation: %v", err)
	}
	store.(*sqliteRunStore).barrier()
	events, err := store.LoadEvents(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[2].Event.ExplainAssessment != 2 || events[2].Event.Question != "Explain simply" {
		t.Fatalf("origin missing on replay: %+v", events)
	}
	_, live, _, cancel, err := r.Subscribe(3)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	close(release)
	select {
	case event := <-live:
		if event.Event.Type != "done" || event.Event.Diag == nil || event.Event.Diag.Report == "" {
			t.Fatalf("no final explanation: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("explanation did not finish")
	}
	store.(*sqliteRunStore).barrier()
	restarted := persistedManager(t, store, "ctx")
	backlog, _, _, unsubscribe, err := restarted.Get(r.ID).Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if len(backlog) != 4 || backlog[2].Event.ExplainAssessment != 2 || backlog[3].Event.Diag.Report == "" {
		t.Fatalf("completed explanation lost on restart: %+v", backlog)
	}
}

func TestExplanationUsesSavedAssessmentAndBoundedInstruction(t *testing.T) {
	prompt := explanationPrompt(Diagnosis{RootCause: "Missing Secret", Report: "Not established whether it was deleted.", Remediation: []string{"Restore configuration."}})
	for _, want := range []string{"Missing Secret", "Not established whether it was deleted.", "Restore configuration.", "Do not recheck the cluster or call tools", "Do not apply anything", "120-180", "Preserve uncertainty"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(prompt, "root_cause_evidence_refs") {
		t.Fatal("explanation must not request fresh evidence refs")
	}
	parsed := diagnosisFromText("The saved configuration is missing. Restore the intended configuration, then verify the app starts.")
	if parsed.Report == "" || parsed.RootCause != "" || len(parsed.Remediation) != 0 {
		t.Fatalf("plain explanation did not stay conversational: %+v", parsed)
	}
}
