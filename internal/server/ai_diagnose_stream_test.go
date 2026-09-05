package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/ai"
)

func TestDiagnoseReplayCompleteFrameIsUnsequenced(t *testing.T) {
	recorder := httptest.NewRecorder()
	sendSSEEvent(recorder, recorder, "replay_complete", map[string]string{
		"type": "replay_complete",
	})

	got := recorder.Body.String()
	if strings.Contains(got, "id:") {
		t.Fatalf("replay boundary must not advance Last-Event-ID, got %q", got)
	}
	want := "event: replay_complete\ndata: {\"type\":\"replay_complete\"}\n\n"
	if got != want {
		t.Fatalf("replay boundary frame = %q, want %q", got, want)
	}
}

func TestSendDiagnoseClosedIfFinalizedDistinguishesSlowSubscriber(t *testing.T) {
	t.Run("live slow subscriber gets reconnectable EOF", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		sendDiagnoseClosedIfFinalized(recorder, recorder, false)

		if body := recorder.Body.String(); body != "" {
			t.Fatalf("live subscriber close wrote terminal SSE frame: %q", body)
		}
		if recorder.Flushed {
			t.Fatal("live subscriber close flushed a terminal SSE frame")
		}
	})

	t.Run("already finalized subscription gets terminal control frame", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		sendDiagnoseClosedIfFinalized(recorder, recorder, true)

		want := "event: closed\ndata: {\"type\":\"closed\"}\n\n"
		if body := recorder.Body.String(); body != want {
			t.Fatalf("finalized subscriber frame = %q, want %q", body, want)
		}
		if strings.Contains(recorder.Body.String(), "id:") {
			t.Fatal("terminal control frame advanced the durable replay cursor")
		}
		if !recorder.Flushed {
			t.Fatal("terminal control frame was not flushed")
		}
	})
}

func TestHandleDiagnoseRunStreamReturnsRetryableSSEWhenHydrationFails(t *testing.T) {
	store, err := ai.OpenRunStore(filepath.Join(t.TempDir(), "ai-runs.db"))
	if err != nil {
		t.Fatalf("OpenRunStore: %v", err)
	}
	now := time.Now().UTC()
	store.SaveRun(ai.RunSummary{
		ID: "run-1", Kind: "Pod", Namespace: "default", Name: "web", Context: "ctx-a",
		Agent: "claude", Profile: ai.ExecutionProfileSafeguarded, Status: "done",
		CreatedAt: now, UpdatedAt: now,
	})
	manager := ai.NewRunManager(nil, func() int { return 9280 }, "", func() string { return "ctx-a" }, store)
	t.Cleanup(manager.Shutdown)
	if manager.Get("run-1") == nil {
		t.Fatal("persisted run was not loaded")
	}
	store.Close() // fail the first lazy transcript hydration

	server := &Server{aiRuns: manager}
	request := httptest.NewRequest(http.MethodGet, "/api/diagnose/runs/run-1/stream", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "run-1")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()

	server.handleDiagnoseRunStream(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	for header, want := range map[string]string{
		"Cache-Control":     "no-cache",
		"Connection":        "keep-alive",
		"X-Accel-Buffering": "no",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Fatalf("SSE header %s=%q, want %q", header, got, want)
		}
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"event: history_unavailable",
		`"type":"history_unavailable"`,
		`"retryable":true`,
		ai.ErrHistoryUnavailable.Error(),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("hydration failure body = %q, want %q", body, want)
		}
	}
	if strings.Contains(body, "id: ") || strings.Contains(body, "replay_complete") {
		t.Fatalf("hydration failure advanced durable replay state: %q", body)
	}
	if strings.Contains(body, "event: closed") {
		t.Fatalf("transient hydration failure permanently closed a retryable stream: %q", body)
	}
	if !recorder.Flushed {
		t.Fatal("history_unavailable frame was not flushed before the stream closed")
	}
}

func TestHandleDiagnoseRunStreamClosesNonRetryableCorruptHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ai-runs.db")
	store, err := ai.OpenRunStore(dbPath)
	if err != nil {
		t.Fatalf("OpenRunStore: %v", err)
	}
	now := time.Now().UTC()
	store.SaveRun(ai.RunSummary{
		ID: "run-1", Kind: "Pod", Namespace: "default", Name: "web", Context: "ctx-a",
		Agent: "claude", Profile: ai.ExecutionProfileSafeguarded, Status: "done",
		CreatedAt: now, UpdatedAt: now,
	})
	manager := ai.NewRunManager(nil, func() int { return 9280 }, "", func() string { return "ctx-a" }, store)
	t.Cleanup(manager.Shutdown)
	if manager.Get("run-1") == nil {
		t.Fatal("persisted run was not loaded")
	}

	// Write through a second connection after NewRunManager's LoadRuns barrier,
	// leaving the summary valid but its lazily-loaded transcript permanently bad.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open corruption connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(
		`INSERT INTO run_events (run_id, seq, event_json) VALUES (?, ?, ?)`,
		"run-1", 1, `{"type":`,
	); err != nil {
		t.Fatalf("insert corrupt transcript: %v", err)
	}

	server := &Server{aiRuns: manager}
	request := httptest.NewRequest(http.MethodGet, "/api/diagnose/runs/run-1/stream", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "run-1")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()

	server.handleDiagnoseRunStream(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"event: history_unavailable",
		`"type":"history_unavailable"`,
		`"retryable":false`,
		ai.ErrHistoryCorrupt.Error(),
		"event: closed",
		`"reason":"history_unavailable"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("corrupt hydration body = %q, want %q", body, want)
		}
	}
	historyIndex, closedIndex := strings.Index(body, "event: history_unavailable"), strings.Index(body, "event: closed")
	if historyIndex < 0 || closedIndex <= historyIndex {
		t.Fatalf("permanent failure must precede explicit closure: %q", body)
	}
	if strings.Contains(body, "id: ") || strings.Contains(body, "replay_complete") {
		t.Fatalf("corrupt hydration advanced durable replay state: %q", body)
	}
	if !recorder.Flushed {
		t.Fatal("permanent history failure contract was not flushed")
	}
}

func TestHandleDiagnoseRunStreamRepeatsClosedAfterDurableCursor(t *testing.T) {
	store, err := ai.OpenRunStore(filepath.Join(t.TempDir(), "ai-runs.db"))
	if err != nil {
		t.Fatalf("OpenRunStore: %v", err)
	}
	now := time.Now().UTC()
	summary := ai.RunSummary{
		ID: "run-finalized", Kind: "Pod", Namespace: "default", Name: "web", Context: "ctx-a",
		Agent: "claude", Profile: ai.ExecutionProfileSafeguarded, Status: "stale",
		CreatedAt: now, UpdatedAt: now,
	}
	store.AppendEvents(summary.ID, []ai.RunEvent{
		{Seq: 1, Event: ai.StreamEvent{Type: "turn"}},
		{Seq: 2, Event: ai.StreamEvent{Type: "error", Error: "Cluster context changed."}},
		{Seq: 3, Event: ai.StreamEvent{Type: "closed"}},
	}, &summary)

	manager := ai.NewRunManager(nil, func() int { return 9280 }, "", func() string { return "ctx-a" }, store)
	t.Cleanup(manager.Shutdown)
	if manager.Get(summary.ID) == nil {
		t.Fatal("persisted finalized run was not loaded")
	}
	server := &Server{aiRuns: manager}

	for _, test := range []struct {
		name        string
		lastEventID string
	}{
		{name: "cursor equals closed sequence", lastEventID: "3"},
		{name: "cursor is beyond closed sequence", lastEventID: "4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/diagnose/runs/run-finalized/stream", nil)
			request.Header.Set("Last-Event-ID", test.lastEventID)
			routeContext := chi.NewRouteContext()
			routeContext.URLParams.Add("id", summary.ID)
			request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
			recorder := httptest.NewRecorder()

			server.handleDiagnoseRunStream(recorder, request)

			body := recorder.Body.String()
			replayIndex := strings.Index(body, "event: replay_complete")
			closedIndex := strings.Index(body, "event: closed")
			if replayIndex < 0 || closedIndex <= replayIndex {
				t.Fatalf("finalized replay must end after its boundary with closed: %q", body)
			}
			if strings.Count(body, "event: closed") != 1 {
				t.Fatalf("finalized replay emitted an ambiguous terminal sequence: %q", body)
			}
			if strings.Contains(body, "id: ") {
				t.Fatalf("repeated terminal control frame advanced the durable cursor: %q", body)
			}
			if strings.Contains(body, "history_unavailable") {
				t.Fatalf("finalized replay was misclassified as unavailable history: %q", body)
			}
		})
	}
}

func TestHandleDiagnoseRunStreamReplaysPersistedEvidenceProvenance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ai-runs.db")
	writer, err := ai.OpenRunStore(dbPath)
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	now := time.Now().UTC()
	ref := "ev_aaaaaaaaaaaaaaaaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	success := false
	summary := ai.RunSummary{
		ID: "run-evidence", Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "api", Context: "ctx-a",
		Agent: "codex", Profile: ai.ExecutionProfileSafeguarded, Status: "stale",
		CreatedAt: now, UpdatedAt: now,
	}
	writer.AppendEvents(summary.ID, []ai.RunEvent{
		{Seq: 1, Event: ai.StreamEvent{Type: "step", Step: &ai.StepInfo{
			ID: "logs", Tool: "get_pod_logs", Status: "done",
			Result: `{"logs":["authentication failed"]}`, EvidenceRef: ref,
			RadarEvidence: true, IsError: &success,
		}}},
		{Seq: 2, Event: ai.StreamEvent{Type: "done", Diag: &ai.Diagnosis{
			RootCause: "The workload uses a stale database credential.",
			RootCauseEvidence: &ai.RootCauseEvidence{
				Status: ai.EvidenceLinked,
				Refs:   []string{ref},
			},
		}}},
		{Seq: 3, Event: ai.StreamEvent{Type: "closed"}},
	}, &summary)
	writer.Close()

	store, err := ai.OpenRunStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	manager := ai.NewRunManager(nil, func() int { return 9280 }, "", func() string { return "ctx-a" }, store)
	t.Cleanup(manager.Shutdown)
	if manager.Get(summary.ID) == nil {
		t.Fatal("persisted evidence run was not loaded")
	}
	server := &Server{aiRuns: manager}
	request := httptest.NewRequest(http.MethodGet, "/api/diagnose/runs/run-evidence/stream", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", summary.ID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()

	server.handleDiagnoseRunStream(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`"evidenceRef":"` + ref + `"`,
		`"radarEvidence":true`,
		`"rootCauseEvidence":{"status":"linked","refs":["` + ref + `"]}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("persisted replay body = %q, want exact JSON field %q", body, want)
		}
	}

	var replayedStep *ai.StepInfo
	var replayedDiagnosis *ai.Diagnosis
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event ai.StreamEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("decode replayed SSE data %q: %v", line, err)
		}
		switch event.Type {
		case "step":
			replayedStep = event.Step
		case "done":
			replayedDiagnosis = event.Diag
		}
	}
	if replayedStep == nil || replayedStep.EvidenceRef != ref || !replayedStep.RadarEvidence {
		t.Fatalf("replayed step lost evidence provenance: %+v; body=%q", replayedStep, body)
	}
	wantEvidence := &ai.RootCauseEvidence{Status: ai.EvidenceLinked, Refs: []string{ref}}
	if replayedDiagnosis == nil || !reflect.DeepEqual(replayedDiagnosis.RootCauseEvidence, wantEvidence) {
		t.Fatalf("replayed diagnosis lost rootCauseEvidence: %+v; body=%q", replayedDiagnosis, body)
	}
	replayIndex, closedIndex := strings.Index(body, "event: replay_complete"), strings.Index(body, "event: closed")
	if replayIndex < 0 || closedIndex <= replayIndex {
		t.Fatalf("evidence replay closed before its boundary: %q", body)
	}
}

func TestSendDiagnoseBacklogPlacesReplayBoundaryBeforeClosed(t *testing.T) {
	backlog := []ai.RunEvent{
		{Seq: 1, Event: ai.StreamEvent{Type: "turn"}},
		{Seq: 2, Event: ai.StreamEvent{Type: "done"}},
		{Seq: 3, Event: ai.StreamEvent{Type: "closed"}},
	}
	var order []string
	terminal, ok := sendDiagnoseBacklog(
		backlog,
		func(event ai.RunEvent) bool {
			order = append(order, event.Event.Type)
			return true
		},
		func() { order = append(order, "replay_complete") },
	)

	if !ok || !terminal {
		t.Fatalf("sendDiagnoseBacklog() = terminal %v, ok %v; want true, true", terminal, ok)
	}
	want := []string{"turn", "done", "replay_complete", "closed"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("replay order = %v, want %v", order, want)
	}
}

func TestSendDiagnoseBacklogOpensLiveTailAfterBoundary(t *testing.T) {
	var order []string
	terminal, ok := sendDiagnoseBacklog(
		[]ai.RunEvent{{Seq: 1, Event: ai.StreamEvent{Type: "turn"}}},
		func(event ai.RunEvent) bool {
			order = append(order, event.Event.Type)
			return true
		},
		func() { order = append(order, "replay_complete") },
	)

	if !ok || terminal {
		t.Fatalf("sendDiagnoseBacklog() = terminal %v, ok %v; want false, true", terminal, ok)
	}
	want := []string{"turn", "replay_complete"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("replay order = %v, want %v", order, want)
	}
}
