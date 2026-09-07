package ai

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTurnPrompt_MetricsNudgeOnlyOnReadOnlyTurnsWhenConnected(t *testing.T) {
	connected := MetricsAvailability{Connected: true, Address: "http://prometheus.monitoring:9090"}
	base := Request{Kind: "Deployment", Namespace: "prod", Name: "api"}

	initial := base
	initial.Metrics = connected
	if p := turnPrompt(initial); !strings.Contains(p, "Prometheus is connected at http://prometheus.monitoring:9090") ||
		!strings.Contains(p, "`query_prometheus` range query") || !strings.HasSuffix(strings.TrimSpace(p), "cite it.") {
		t.Fatalf("initial turn lacks the metrics nudge after the task prompt:\n%s", p)
	}
	if p := turnPrompt(initial); strings.Index(p, diagnosisJSONInstruction) > strings.Index(p, "Prometheus is connected") {
		t.Fatalf("nudge must follow prompt selection, not precede the JSON contract:\n%s", p)
	}

	followUp := base
	followUp.Metrics = connected
	followUp.Question = "Why is it restarting?"
	followUp.SessionID = "session"
	if p := turnPrompt(followUp); !strings.HasPrefix(p, "Why is it restarting?") || !strings.Contains(p, "Prometheus is connected at") {
		t.Fatalf("follow-up lost the question or the nudge:\n%s", p)
	}

	notConnected := base
	notConnected.Metrics = MetricsAvailability{Connected: false, Address: "http://prometheus.monitoring:9090"}
	if p := turnPrompt(notConnected); strings.Contains(p, "Prometheus") {
		t.Fatalf("a failed probe must say nothing to the agent:\n%s", p)
	}
	if p := turnPrompt(base); strings.Contains(p, "Prometheus") {
		t.Fatalf("an unprobed turn must say nothing to the agent:\n%s", p)
	}

	explanation := base
	explanation.Metrics = connected
	explanation.Explanation = &Diagnosis{RootCause: "Missing Secret", Report: "The saved analysis."}
	if p := turnPrompt(explanation); strings.Contains(p, "Prometheus") {
		t.Fatalf("an explanation turn must not be sent to gather metrics:\n%s", p)
	}

	apply := base
	apply.Metrics = connected
	apply.Apply = true
	apply.Fix = "set replicas to 2"
	if p := turnPrompt(apply); strings.Contains(p, "Prometheus") {
		t.Fatalf("an apply turn must not be sent to gather metrics:\n%s", p)
	}
}

// TestRunManagerProbesMetricsOnlyForReadOnlyTurns pins that the RunManager
// asks the probe before investigation turns (initial and follow-up), skips it
// for explanation and apply turns, and bounds it with a deadline.
func TestRunManagerProbesMetricsOnlyForReadOnlyTurns(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	var probes atomic.Int32
	m.MetricsAvailability = func(ctx context.Context) MetricsAvailability {
		probes.Add(1)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("metrics probe ran without a deadline")
		}
		return MetricsAvailability{Connected: true, Address: "http://prom:9090"}
	}
	r.append(StreamEvent{Type: "turn"})
	r.append(StreamEvent{Type: "done", Diag: &Diagnosis{RootCause: "Missing Secret", Report: "The saved analysis."}})

	next := func(t *testing.T) controlledDiagnoseCall {
		t.Helper()
		select {
		case call := <-calls:
			call.respond <- controlledDiagnoseResponse{diag: Diagnosis{Report: "ok", SessionID: "read-session"}}
			<-call.returned
			return call
		case <-time.After(2 * time.Second):
			t.Fatal("agent not invoked")
			return controlledDiagnoseCall{}
		}
	}
	waitDone := func(t *testing.T) {
		t.Helper()
		for i := 0; i < 200; i++ {
			r.mu.Lock()
			inFlight := r.inFlight
			r.mu.Unlock()
			if !inFlight {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("turn did not finish")
	}

	if err := m.AddTurn(r.ID, "Why is it restarting?", false, "", false); err != nil {
		t.Fatalf("AddTurn(follow-up): %v", err)
	}
	if call := next(t); !call.request.Metrics.Connected || call.request.Metrics.Address != "http://prom:9090" {
		t.Fatalf("follow-up request did not carry the probe result: %+v", call.request.Metrics)
	}
	waitDone(t)
	if got := probes.Load(); got != 1 {
		t.Fatalf("follow-up probes = %d, want 1", got)
	}

	if err := m.AddExplanation(r.ID, 2); err != nil {
		t.Fatalf("AddExplanation: %v", err)
	}
	if call := next(t); call.request.Explanation == nil || call.request.Metrics.Connected {
		t.Fatalf("explanation request must not carry a probe result: %+v", call.request.Metrics)
	}
	waitDone(t)
	if got := probes.Load(); got != 1 {
		t.Fatalf("explanation turn ran the probe (probes = %d)", got)
	}

	if err := m.AddTurn(r.ID, "", true, "set replicas to 2", false); err != nil {
		t.Fatalf("AddTurn(apply): %v", err)
	}
	if call := next(t); !call.request.Apply || call.request.Metrics.Connected {
		t.Fatalf("apply request must not carry a probe result: %+v", call.request.Metrics)
	}
	waitDone(t)
	if got := probes.Load(); got != 1 {
		t.Fatalf("apply turn ran the probe (probes = %d)", got)
	}
}

func TestRunManagerWithoutMetricsProbeSaysNothing(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	if err := m.AddTurn(r.ID, "Why?", false, "", false); err != nil {
		t.Fatalf("AddTurn: %v", err)
	}
	select {
	case call := <-calls:
		if call.request.Metrics != (MetricsAvailability{}) {
			t.Fatalf("request carried metrics without a probe: %+v", call.request.Metrics)
		}
		call.respond <- controlledDiagnoseResponse{diag: Diagnosis{Report: "ok", SessionID: "read-session"}}
		<-call.returned
	case <-time.After(2 * time.Second):
		t.Fatal("agent not invoked")
	}
}
