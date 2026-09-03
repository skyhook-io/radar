package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRunWorkDirUnderPrivateRoot pins that per-run scratch dirs live UNDER the
// manager's private root (so they can't collide across Radar restarts / co-running
// processes or sit at a predictable /tmp path), and that a missing root degrades to
// "" rather than a guessable path.
func TestRunWorkDirUnderPrivateRoot(t *testing.T) {
	m := &RunManager{workRoot: filepath.Join(t.TempDir(), "root")}
	a, b := m.runWorkDir("run-1"), m.runWorkDir("run-2")
	if a == b {
		t.Errorf("per-run dirs must differ: %q == %q", a, b)
	}
	if filepath.Dir(a) != m.workRoot {
		t.Errorf("run dir %q is not under workRoot %q", a, m.workRoot)
	}
	none := &RunManager{workRoot: ""}
	if none.runWorkDir("run-1") != "" {
		t.Error("no root must yield empty workdir, not a predictable path")
	}
}

// TestRunSubscribeReplay pins the SSE-replay contract: a subscriber gets the
// backlog after its last-seen seq, then live events, then a close on terminal.
func TestRunSubscribeReplay(t *testing.T) {
	r := &Run{subs: map[int]chan RunEvent{}}
	r.append(StreamEvent{Type: "turn"})                 // seq 1
	r.append(StreamEvent{Type: "phase"})                // seq 2
	r.append(StreamEvent{Type: "thinking", Token: "x"}) // seq 3

	backlog, ch, alreadyFinalized, cancel, err := r.Subscribe(1) // everything after seq 1
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if alreadyFinalized {
		t.Fatal("live subscription reported an already-finalized run")
	}
	if len(backlog) != 2 || backlog[0].Seq != 2 || backlog[1].Seq != 3 {
		t.Fatalf("backlog = %+v, want seq 2,3", backlog)
	}

	r.append(StreamEvent{Type: "done"}) // seq 4 → live
	live, ok := <-ch
	if !ok || live.Seq != 4 || live.Event.Type != "done" {
		t.Fatalf("live = %+v ok=%v, want seq 4 done", live, ok)
	}

	// A completed turn must NOT close the subscription (multi-turn keeps it alive).
	r.append(StreamEvent{Type: "turn", Question: "follow-up"}) // seq 5
	if next := <-ch; next.Seq != 5 {
		t.Fatalf("expected live follow-up turn at seq 5, got %+v", next)
	}

	// finalize (stale/evict) is what closes it.
	r.finalize()
	for range ch { // drain the trailing "closed" sentinel
	}
}

// TestSubscribeAfterFinalize: reopening a finalized (stale/evicted) run replays
// its full log then ends, rather than hanging.
func TestSubscribeAfterFinalize(t *testing.T) {
	r := &Run{subs: map[int]chan RunEvent{}}
	r.append(StreamEvent{Type: "turn"})
	r.append(StreamEvent{Type: "done"})
	r.finalize() // appends a "closed" sentinel + drops subs

	backlog, ch, alreadyFinalized, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if !alreadyFinalized {
		t.Fatal("finalized run returned a reconnectable live subscription")
	}
	if len(backlog) != 3 { // turn, done, closed
		t.Fatalf("backlog = %d, want 3", len(backlog))
	}
	if _, ok := <-ch; ok {
		t.Errorf("channel should be closed for a finalized run")
	}

	closedSeq := backlog[len(backlog)-1].Seq
	afterClosed, afterClosedCh, afterClosedFinalized, afterClosedCancel, err := r.Subscribe(closedSeq)
	if err != nil {
		t.Fatalf("Subscribe after closed sequence: %v", err)
	}
	defer afterClosedCancel()
	if len(afterClosed) != 0 {
		t.Fatalf("backlog after closed sequence = %+v, want empty", afterClosed)
	}
	if !afterClosedFinalized {
		t.Fatal("subscription after durable closed sequence did not report finalized")
	}
	if _, ok := <-afterClosedCh; ok {
		t.Fatal("subscription after durable closed sequence remained open")
	}
}

func TestSlowSubscriberClosureRemainsReplayableAcrossFinalization(t *testing.T) {
	r := &Run{status: "running", inFlight: true, hydrated: true, subs: map[int]chan RunEvent{}}
	_, ch, alreadyFinalized, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if alreadyFinalized {
		t.Fatal("live subscription reported an already-finalized run")
	}

	// Leave the subscription unread. Event 257 cannot fit in its 256-entry
	// buffer, so the subscriber is evicted while every event remains durable.
	for i := 1; i <= 257; i++ {
		r.append(StreamEvent{Type: "thinking", Token: fmt.Sprintf("%d", i)})
	}
	r.mu.Lock()
	if r.subs == nil || len(r.subs) != 0 {
		t.Fatalf("overflow changed run lifecycle: subs=%v, want live empty map", r.subs)
	}
	if len(r.events) != 257 || r.events[len(r.events)-1].Event.Type == "closed" {
		t.Fatalf("overflow changed durable log: events=%d last=%+v", len(r.events), r.events[len(r.events)-1])
	}
	r.mu.Unlock()

	// Finalize before the slow reader observes EOF. The subscription's atomic
	// snapshot must stay false; checking current run state here would lose seq 257.
	r.finalize()
	var delivered []RunEvent
	for event := range ch {
		delivered = append(delivered, event)
	}
	if len(delivered) != 256 || delivered[0].Seq != 1 || delivered[len(delivered)-1].Seq != 256 {
		t.Fatalf("overflowed subscription delivered seqs %+v, want 1..256", delivered)
	}
	if alreadyFinalized {
		t.Fatal("later run finalization mutated the old subscription snapshot")
	}

	backlog, replay, replayFinalized, replayCancel, err := r.Subscribe(256)
	if err != nil {
		t.Fatalf("Subscribe replay: %v", err)
	}
	defer replayCancel()
	if !replayFinalized {
		t.Fatal("reconnect to finalized run did not report finalized")
	}
	if len(backlog) != 2 || backlog[0].Seq != 257 || backlog[1].Seq != 258 || backlog[1].Event.Type != "closed" {
		t.Fatalf("reconnect backlog = %+v, want seq 257 then closed seq 258", backlog)
	}
	if _, ok := <-replay; ok {
		t.Fatal("finalized replay channel remained open")
	}
}

func TestSubscribePreservesGenuinelyEmptyRun(t *testing.T) {
	store, _ := testStore(t)
	store.SaveRun(RunSummary{
		ID: "empty-run", Kind: "Pod", Name: "p", Context: "ctx-a", Status: "done",
		CreatedAt: nowUTC(), UpdatedAt: nowUTC(),
	})
	store.(*sqliteRunStore).barrier()
	manager := persistedManager(t, store, "ctx-a")
	r := manager.Get("empty-run")
	if r == nil {
		t.Fatal("empty persisted run was not loaded")
	}

	backlog, ch, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if len(backlog) != 0 {
		t.Fatalf("empty run backlog = %+v, want empty", backlog)
	}
	if ch == nil || cancel == nil {
		t.Fatal("empty persisted run must return a usable live subscription")
	}
	r.append(StreamEvent{Type: "turn"})
	if event := <-ch; event.Seq != 1 || event.Event.Type != "turn" {
		t.Fatalf("empty run subscription did not become live: %+v", event)
	}
}

// TestBeginTurnCapAndRace: a turn is gated by the concurrency cap and can't be
// double-started (the AddTurn race) — beginTurn reserves the slot atomically.
func TestBeginTurnCapAndRace(t *testing.T) {
	m := &RunManager{runs: map[string]*Run{}, maxConcurrent: 1}
	mk := func(id, status string, inFlight bool, session string) *Run {
		r := &Run{ID: id, status: status, inFlight: inFlight, sessionID: session,
			subs: map[int]chan RunEvent{}}
		m.runs[id] = r
		m.order = append(m.order, id)
		return r
	}
	mk("busy", "running", true, "s1") // occupies the only slot
	idle := mk("idle", "done", false, "s2")

	if _, err := m.beginTurn(idle, true); err != ErrAtCapacity {
		t.Fatalf("at cap: want ErrAtCapacity, got %v", err)
	}
	m.runs["busy"].inFlight = false // free the slot
	if _, err := m.beginTurn(idle, true); err != nil {
		t.Fatalf("free slot: want success, got %v", err)
	}
	if !idle.inFlight {
		t.Error("beginTurn must mark the run in-flight")
	}

	// Below the cap, a second begin on an already-in-flight run is rejected as
	// in-flight (no double agent on the same run).
	m.maxConcurrent = 5
	if _, err := m.beginTurn(idle, true); err != ErrTurnInFlight {
		t.Fatalf("double-start: want ErrTurnInFlight, got %v", err)
	}
}

// TestBeginTurnRequiresSession: follow-ups can't run before a resumable session.
func TestBeginTurnRequiresSession(t *testing.T) {
	m := &RunManager{runs: map[string]*Run{}, maxConcurrent: 3}
	r := &Run{ID: "a", status: "done", subs: map[int]chan RunEvent{}}
	m.runs["a"] = r
	m.order = append(m.order, "a")
	if _, err := m.beginTurn(r, true); err != ErrNoSession {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
}

type controlledDiagnoseResponse struct {
	diag Diagnosis
	err  error
}

type controlledDiagnoseCall struct {
	request  Request
	emit     func(StreamEvent)
	respond  chan controlledDiagnoseResponse
	returned chan struct{}
}

// controlledRunManager provides a deterministic DiagnoseStream boundary: each
// execution step is delivered to calls and does not return until the test
// responds (or the run context is cancelled).
func controlledRunManager(t *testing.T, store RunStore) (*RunManager, *Run, <-chan controlledDiagnoseCall) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan controlledDiagnoseCall, 4)
	m := &RunManager{
		mcpPort: func() int { return 9280 }, ctxLabel: func() string { return "ctx" },
		baseCtx: ctx, baseCancel: cancel, store: store,
		runs: map[string]*Run{}, maxConcurrent: 3, maxRetained: 10,
	}
	m.diagnose = func(ctx context.Context, req Request, onEvent func(StreamEvent)) (Diagnosis, error) {
		call := controlledDiagnoseCall{
			request: req, emit: onEvent,
			respond: make(chan controlledDiagnoseResponse, 1), returned: make(chan struct{}),
		}
		defer close(call.returned)
		select {
		case calls <- call:
		case <-ctx.Done():
			return Diagnosis{}, ctx.Err()
		}
		select {
		case response := <-call.respond:
			return response.diag, response.err
		case <-ctx.Done():
			return Diagnosis{}, ctx.Err()
		}
	}
	r := &Run{
		ID: "run-compound", Kind: "Deployment", Namespace: "prod", Name: "api", Context: "ctx",
		Agent: "claude", Profile: ExecutionProfileSafeguarded,
		store: store, status: "done", sessionID: "read-session", hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{},
	}
	m.runs[r.ID] = r
	m.order = []string{r.ID}
	t.Cleanup(cancel)
	return m, r, calls
}

func emitControlledWriteResult(call controlledDiagnoseCall, id string, isError *bool) {
	call.emit(StreamEvent{Type: "step", Step: &StepInfo{
		ID: id, Tool: "patch_resource", Status: "running", Summary: `{"dry_run":false}`,
	}})
	// Claude terminal tool-result events intentionally omit Tool; the production
	// outcome tracker must correlate this row to the started write by ID.
	call.emit(StreamEvent{Type: "step", Step: &StepInfo{
		ID: id, Status: "done", IsError: isError,
		Result: `{"status":"ok","dry_run":false}`,
	}})
}

func boolPointer(value bool) *bool { return &value }

func receiveDiagnoseCall(t *testing.T, calls <-chan controlledDiagnoseCall) controlledDiagnoseCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for DiagnoseStream call")
		return controlledDiagnoseCall{}
	}
}

func waitForEvent(t *testing.T, ch <-chan RunEvent, eventType string, occurrence int) RunEvent {
	t.Helper()
	seen := 0
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed before %s occurrence %d", eventType, occurrence)
			}
			if event.Event.Type == eventType {
				seen++
				if seen == occurrence {
					return event
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s occurrence %d", eventType, occurrence)
		}
	}
}

func waitForRunNotInFlight(t *testing.T, r *Run) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		inFlight := r.inFlight
		r.mu.Unlock()
		if !inFlight {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("run did not release its in-flight reservation")
}

func TestApplyRunsImmediateAutomaticVerificationAsOneJob(t *testing.T) {
	store := newBarrierRunStore("")
	m, r, calls := controlledRunManager(t, store)
	_, live, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	if err := m.AddTurn(r.ID, "", true, "set replicas to 2", false); err != nil {
		t.Fatalf("AddTurn(apply): %v", err)
	}
	apply := receiveDiagnoseCall(t, calls)
	if !apply.request.Apply || apply.request.Verify || apply.request.Fix != "set replicas to 2" {
		t.Fatalf("apply request = %+v", apply.request)
	}
	emitControlledWriteResult(apply, "write-1", boolPointer(false))
	apply.respond <- controlledDiagnoseResponse{diag: Diagnosis{
		Report: "scaled Deployment prod/api", SessionID: "write-session",
	}}

	// The next call is emitted directly by the same execution loop: there is no
	// client callback, public AddTurn, or delay between the apply result and it.
	verify := receiveDiagnoseCall(t, calls)
	if verify.request.Apply || !verify.request.Verify {
		t.Fatalf("automatic verification mode = apply %v verify %v, want false/true", verify.request.Apply, verify.request.Verify)
	}
	if verify.request.Question != automaticVerificationPrompt {
		t.Fatalf("verification prompt = %q, want canonical %q", verify.request.Question, automaticVerificationPrompt)
	}
	if verify.request.SessionID != "read-session" {
		t.Fatalf("verification resumed %q, want canonical pre-apply session", verify.request.SessionID)
	}
	if verify.request.Fix != "" {
		t.Fatalf("verification inherited confirmed write text: %q", verify.request.Fix)
	}

	// Apply and verification are one compound reservation. No follow-up can
	// interleave after the apply-done marker while verification is blocked.
	if got := r.Summary(); got.Status != "running" {
		t.Fatalf("status during verification = %q, want running", got.Status)
	}
	if err := m.AddTurn(r.ID, "interleave", false, "", false); !errors.Is(err, ErrTurnInFlight) {
		t.Fatalf("concurrent AddTurn = %v, want ErrTurnInFlight", err)
	}

	verify.respond <- controlledDiagnoseResponse{diag: Diagnosis{Healthy: true, SessionID: "read-session-2"}}
	waitForEvent(t, live, "done", 2)
	waitForRunNotInFlight(t, r)

	r.mu.Lock()
	events := append([]RunEvent(nil), r.events...)
	r.mu.Unlock()
	wantTypes := []string{"turn", "step", "step", "done", "turn", "done"}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d: %+v", len(events), len(wantTypes), events)
	}
	for i, want := range wantTypes {
		if events[i].Event.Type != want {
			t.Fatalf("event[%d] = %+v, want type %s", i, events[i], want)
		}
	}
	if events[3].Event.ApplyOutcome != ApplyMutationConfirmed {
		t.Fatalf("apply outcome = %q, want confirmed", events[3].Event.ApplyOutcome)
	}
	if !events[0].Event.Apply || events[0].Event.Verify || events[4].Event.Apply || !events[4].Event.Verify {
		t.Fatalf("turn markers = apply %+v verify %+v", events[0].Event, events[4].Event)
	}
	if countOp(store.snapshot(), "event:done:running") != 1 || countOp(store.snapshot(), "event:done:done") != 1 {
		t.Fatalf("durable terminal summaries = %v, want apply done/running then verify done/done", store.snapshot())
	}
	if got := r.Summary(); got.Status != "done" || got.SessionID != "read-session-2" {
		t.Fatalf("final run = %+v", got)
	}
}

func TestApplyVerificationHandoffPersistsRunningUntilFinalVerdict(t *testing.T) {
	store, _ := testStore(t)
	sqlite := store.(*sqliteRunStore)
	m, r, calls := controlledRunManager(t, store)
	if err := m.AddTurn(r.ID, "", true, "set replicas to 2", false); err != nil {
		t.Fatal(err)
	}
	apply := receiveDiagnoseCall(t, calls)
	emitControlledWriteResult(apply, "write-1", boolPointer(false))
	apply.respond <- controlledDiagnoseResponse{diag: Diagnosis{Report: "changed"}}
	verify := receiveDiagnoseCall(t, calls) // verification remains blocked here

	sqlite.barrier()
	runs, err := store.LoadRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != "running" {
		t.Fatalf("persisted handoff summary = %+v, want running", runs)
	}
	events, err := store.LoadEvents(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 || events[0].Event.Type != "turn" || !events[0].Event.Apply ||
		events[3].Event.Type != "done" || events[3].Event.ApplyOutcome != ApplyMutationConfirmed ||
		events[4].Event.Type != "turn" || !events[4].Event.Verify {
		t.Fatalf("persisted handoff events = %+v", events)
	}

	verify.respond <- controlledDiagnoseResponse{diag: Diagnosis{Healthy: true, SessionID: "read-session-2"}}
	waitForRunNotInFlight(t, r)
	sqlite.barrier()
	runs, err = store.LoadRuns()
	if err != nil {
		t.Fatal(err)
	}
	events, err = store.LoadEvents(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != "done" || len(events) != 6 || events[5].Event.Type != "done" {
		t.Fatalf("persisted final compound job = runs %+v events %+v", runs, events)
	}
}

func TestRestartDuringAutomaticVerificationRemainsAnHonestInterruption(t *testing.T) {
	store, _ := testStore(t)
	summary := RunSummary{
		ID: "run-interrupted-verify", Kind: "Deployment", Namespace: "prod", Name: "api", Context: "ctx",
		Agent: "claude", Profile: ExecutionProfileSafeguarded,
		Status: "running", SessionID: "read-session", OwnerPID: 1 << 30,
		CreatedAt: nowUTC(), UpdatedAt: nowUTC(),
	}
	store.AppendEvents(summary.ID, []RunEvent{
		{Seq: 1, Event: StreamEvent{Type: "turn", Apply: true}},
		{Seq: 2, Event: StreamEvent{Type: "done", Diag: &Diagnosis{Report: "changed"}}},
		{Seq: 3, Event: StreamEvent{Type: "turn", Verify: true, Question: automaticVerificationPrompt}},
	}, &summary)
	store.(*sqliteRunStore).barrier()

	m := persistedManager(t, store, "ctx")
	r := m.Get(summary.ID)
	if r == nil || r.Summary().Status != "error" {
		t.Fatalf("restarted run = %v, want repaired error", r)
	}
	backlog, _, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if len(backlog) != 4 || !backlog[2].Event.Verify || backlog[3].Event.Type != "error" ||
		!strings.Contains(backlog[3].Event.Error, "restarted") {
		t.Fatalf("restarted verification replay = %+v", backlog)
	}
}

func TestRestartDuringApplyPersistsUnknownMutationOutcome(t *testing.T) {
	store, _ := testStore(t)
	summary := RunSummary{
		ID: "run-interrupted-apply", Kind: "Deployment", Namespace: "prod", Name: "api", Context: "ctx",
		Agent: "claude", Profile: ExecutionProfileSafeguarded,
		Status: "running", SessionID: "read-session", OwnerPID: 1 << 30,
		CreatedAt: nowUTC(), UpdatedAt: nowUTC(),
	}
	store.AppendEvents(summary.ID, []RunEvent{
		{Seq: 1, Event: StreamEvent{Type: "turn", Apply: true}},
		{Seq: 2, Event: StreamEvent{Type: "step", Step: &StepInfo{
			ID: "write", Tool: "patch_resource", Status: "running",
		}}},
	}, &summary)
	store.(*sqliteRunStore).barrier()

	m := persistedManager(t, store, "ctx")
	r := m.Get(summary.ID)
	if r == nil || r.Summary().Status != "error" {
		t.Fatalf("restarted run = %v, want repaired error", r)
	}
	backlog, _, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	terminal := backlog[len(backlog)-1].Event
	if terminal.Type != "error" || terminal.ApplyOutcome != ApplyMutationUnknown ||
		!strings.Contains(terminal.Error, "may have completed") {
		t.Fatalf("restarted apply terminal = %+v, want persisted unknown outcome", terminal)
	}
}

func TestRestartWithUnreadableActiveTranscriptPreservesApplyUncertainty(t *testing.T) {
	terminal := restartInterruptionEvent(nil, errors.New("database temporarily unavailable"))
	if terminal.Type != "error" || terminal.ApplyOutcome != ApplyMutationUnknown {
		t.Fatalf("restart terminal = %+v, want unknown apply outcome", terminal)
	}
	if !strings.Contains(terminal.Error, "transcript could not be reloaded") ||
		!strings.Contains(terminal.Error, "apply may have completed") ||
		!strings.Contains(terminal.Error, "original cluster") {
		t.Fatalf("restart terminal did not preserve mutation uncertainty: %q", terminal.Error)
	}
}

func TestApplyErrorDoesNotStartVerification(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	_, live, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if err := m.AddTurn(r.ID, "", true, "bad fix", false); err != nil {
		t.Fatal(err)
	}
	apply := receiveDiagnoseCall(t, calls)
	apply.respond <- controlledDiagnoseResponse{err: errors.New("write rejected")}
	terminal := waitForEvent(t, live, "error", 1)
	if terminal.Event.ApplyOutcome != ApplyMutationFailed {
		t.Fatalf("apply outcome = %q, want failed", terminal.Event.ApplyOutcome)
	}
	waitForRunNotInFlight(t, r)
	select {
	case call := <-calls:
		t.Fatalf("apply error unexpectedly started another call: %+v", call.request)
	default:
	}
	if got := r.Summary().Status; got != "error" {
		t.Fatalf("status = %q, want error", got)
	}
}

func TestErroredWriteToolRemainsUnknownAndVerifiesOnZeroExit(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	_, live, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if err := m.AddTurn(r.ID, "", true, "scale to 2", false); err != nil {
		t.Fatal(err)
	}
	apply := receiveDiagnoseCall(t, calls)
	emitControlledWriteResult(apply, "failed-write", boolPointer(true))
	apply.respond <- controlledDiagnoseResponse{diag: Diagnosis{
		Inconclusive: true,
		Report:       "RBAC denied the patch",
	}}

	terminal := waitForEvent(t, live, "error", 1)
	verify := receiveDiagnoseCall(t, calls)
	if !verify.request.Verify || verify.request.Question != automaticUncertainVerificationPrompt {
		t.Fatalf("errored-write verification request = %+v", verify.request)
	}
	verify.respond <- controlledDiagnoseResponse{diag: Diagnosis{Healthy: true, SessionID: "read-session-2"}}
	waitForEvent(t, live, "done", 1)
	waitForRunNotInFlight(t, r)
	if terminal.Event.ApplyOutcome != ApplyMutationUnknown {
		t.Fatalf("apply outcome = %q, want unknown", terminal.Event.ApplyOutcome)
	}
	if !terminal.Event.VerificationScheduled {
		t.Fatal("unknown mutation terminal must announce its adjacent verification turn")
	}
	if terminal.Event.Type != "error" || !strings.Contains(terminal.Event.Error, "may have completed") {
		t.Fatalf("errored write was presented as definitive: %+v", terminal.Event)
	}
}

func TestZeroExitWithoutWriteCannotBecomeApplied(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	_, live, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if err := m.AddTurn(r.ID, "", true, "scale to 2", false); err != nil {
		t.Fatal(err)
	}
	apply := receiveDiagnoseCall(t, calls)
	apply.respond <- controlledDiagnoseResponse{diag: Diagnosis{
		Inconclusive: true,
		Report:       "I could not find a write tool",
	}}

	terminal := waitForEvent(t, live, "error", 1)
	waitForRunNotInFlight(t, r)
	if terminal.Event.ApplyOutcome != ApplyMutationFailed {
		t.Fatalf("zero-exit/no-write outcome = %q, want failed", terminal.Event.ApplyOutcome)
	}
	select {
	case call := <-calls:
		t.Fatalf("zero-exit/no-write apply started verification: %+v", call.request)
	default:
	}
}

func TestAmbiguousApplyFailureRunsReadOnlyVerification(t *testing.T) {
	store := newBarrierRunStore("")
	m, r, calls := controlledRunManager(t, store)
	_, live, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if err := m.AddTurn(r.ID, "", true, "scale to 2", false); err != nil {
		t.Fatal(err)
	}
	apply := receiveDiagnoseCall(t, calls)
	apply.emit(StreamEvent{Type: "step", Step: &StepInfo{
		ID: "interrupted-write", Tool: "patch_resource", Status: "running",
	}})
	apply.respond <- controlledDiagnoseResponse{err: errors.New("agent connection lost")}

	verify := receiveDiagnoseCall(t, calls)
	if verify.request.Apply || !verify.request.Verify || verify.request.Question != automaticUncertainVerificationPrompt {
		t.Fatalf("uncertain verification request = %+v", verify.request)
	}
	if verify.request.SessionID != "read-session" {
		t.Fatalf("uncertain verification resumed %q, want canonical read-only session", verify.request.SessionID)
	}
	verify.respond <- controlledDiagnoseResponse{diag: Diagnosis{Healthy: true, SessionID: "read-session-2"}}
	waitForEvent(t, live, "done", 1)
	waitForRunNotInFlight(t, r)

	r.mu.Lock()
	events := append([]RunEvent(nil), r.events...)
	r.mu.Unlock()
	var uncertain *StreamEvent
	for i := range events {
		if events[i].Event.ApplyOutcome == ApplyMutationUnknown {
			uncertain = &events[i].Event
			break
		}
	}
	if uncertain == nil || uncertain.Type != "error" || !uncertain.VerificationScheduled ||
		!strings.Contains(uncertain.Error, "scheduled a current-state verification") {
		t.Fatalf("uncertain apply terminal = %+v, events %+v", uncertain, events)
	}
	if countOp(store.snapshot(), "event:error:running") != 1 {
		t.Fatalf("uncertain apply was not durably recorded before verification: %v", store.snapshot())
	}
}

func TestConfirmedWriteStillVerifiesWhenAgentExitFails(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	_, live, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if err := m.AddTurn(r.ID, "", true, "scale to 2", false); err != nil {
		t.Fatal(err)
	}
	apply := receiveDiagnoseCall(t, calls)
	emitControlledWriteResult(apply, "confirmed-before-exit", boolPointer(false))
	apply.respond <- controlledDiagnoseResponse{err: errors.New("agent report stream failed")}

	verify := receiveDiagnoseCall(t, calls)
	if !verify.request.Verify || verify.request.Question != automaticVerificationPrompt {
		t.Fatalf("confirmed-write verification request = %+v", verify.request)
	}
	verify.respond <- controlledDiagnoseResponse{diag: Diagnosis{Healthy: true, SessionID: "read-session-2"}}
	waitForEvent(t, live, "done", 1)
	waitForRunNotInFlight(t, r)

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range r.events {
		if event.Event.ApplyOutcome != "" {
			if event.Event.ApplyOutcome != ApplyMutationConfirmed || event.Event.Type != "error" {
				t.Fatalf("confirmed mutation with failed report terminal = %+v", event.Event)
			}
			return
		}
	}
	t.Fatal("missing explicit confirmed apply outcome")
}

func TestApplyDoesNotVerifyAgainstDifferentClusterContext(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	_, live, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if err := m.AddTurn(r.ID, "", true, "scale to 2", false); err != nil {
		t.Fatal(err)
	}
	apply := receiveDiagnoseCall(t, calls)
	emitControlledWriteResult(apply, "confirmed", boolPointer(false))
	m.ctxLabel = func() string { return "different-cluster" }
	apply.respond <- controlledDiagnoseResponse{diag: Diagnosis{Report: "changed"}}

	terminal := waitForEvent(t, live, "error", 1)
	waitForRunNotInFlight(t, r)
	if terminal.Event.ApplyOutcome != ApplyMutationConfirmed ||
		!strings.Contains(terminal.Event.Error, "original cluster") {
		t.Fatalf("cross-context apply terminal = %+v", terminal.Event)
	}
	select {
	case call := <-calls:
		t.Fatalf("apply verified against a different context: %+v", call.request)
	default:
	}
}

func TestApplyMutationTrackerTreatsMixedOrUnresolvedWritesAsUnknown(t *testing.T) {
	var mixed applyMutationTracker
	mixed.observe(StreamEvent{Type: "step", Step: &StepInfo{ID: "ok", Tool: "radar.manage_workload", Status: "running"}})
	mixed.observe(StreamEvent{Type: "step", Step: &StepInfo{ID: "ok", Status: "done", IsError: boolPointer(false), Result: `{"status":"ok"}`}})
	mixed.observe(StreamEvent{Type: "step", Step: &StepInfo{ID: "bad", Tool: "manage_workload", Status: "running"}})
	mixed.observe(StreamEvent{Type: "step", Step: &StepInfo{ID: "bad", Status: "done", IsError: boolPointer(true), Result: "write failed after starting"}})
	if got := mixed.outcome(ExecutionProfileSafeguarded); got != ApplyMutationUnknown {
		t.Fatalf("mixed write outcome = %q, want unknown", got)
	}

	var previewThenWrite applyMutationTracker
	previewThenWrite.observe(StreamEvent{Type: "step", Step: &StepInfo{ID: "preview", Tool: "patch_resource", Status: "done", IsError: boolPointer(false), Result: `{"status":"ok","dry_run":true}`}})
	previewThenWrite.observe(StreamEvent{Type: "step", Step: &StepInfo{ID: "write", Tool: "patch_resource", Status: "done", IsError: boolPointer(false), Result: `{"status":"ok","dry_run":false}`}})
	if got := previewThenWrite.outcome(ExecutionProfileSafeguarded); got != ApplyMutationConfirmed {
		t.Fatalf("preview followed by confirmed write = %q, want confirmed", got)
	}

	var unresolved applyMutationTracker
	unresolved.observe(StreamEvent{Type: "step", Step: &StepInfo{ID: "pending", Tool: "apply_resource", Status: "running"}})
	if got := unresolved.outcome(ExecutionProfileSafeguarded); got != ApplyMutationUnknown {
		t.Fatalf("unresolved write outcome = %q, want unknown", got)
	}

	var noWrite applyMutationTracker
	if got := noWrite.outcome(ExecutionProfileSafeguarded); got != ApplyMutationFailed {
		t.Fatalf("no-write outcome = %q, want failed", got)
	}
	if got := noWrite.outcome(ExecutionProfileFullLocal); got != ApplyMutationUnknown {
		t.Fatalf("full-local no-write outcome = %q, want unknown", got)
	}

	var collidingFullLocalTool applyMutationTracker
	collidingFullLocalTool.observe(StreamEvent{Type: "step", Step: &StepInfo{
		ID: "foreign-write", Tool: "patch_resource", Status: "done",
		IsError: boolPointer(false), Result: `{"status":"ok"}`,
	}})
	if got := collidingFullLocalTool.outcome(ExecutionProfileFullLocal); got != ApplyMutationUnknown {
		t.Fatalf("full-local colliding tool outcome = %q, want unknown", got)
	}
}

func TestWriteProducerEvidenceClassification(t *testing.T) {
	success := boolPointer(false)
	failure := boolPointer(true)
	tests := []struct {
		name string
		step applyMutationStep
		want mutationStepEvidence
	}{
		{
			name: "apply persisted",
			step: applyMutationStep{tool: "apply_resource", done: true, isError: success,
				result: `{"status":"ok","kind":"Deployment"}`},
			want: mutationEvidenceConfirmed,
		},
		{
			name: "apply single dry run",
			step: applyMutationStep{tool: "apply_resource", done: true, isError: success,
				result: `{"status":"ok","dry_run":true}`},
			want: mutationEvidenceNone,
		},
		{
			name: "apply multi document dry run from args",
			step: applyMutationStep{tool: "apply_resource", summary: `{"dry_run":true}`, done: true, isError: success,
				result: `{"status":"ok","resources":[{"status":"applied"},{"status":"created"}]}`},
			want: mutationEvidenceNone,
		},
		{
			name: "apply multi document dry run from result",
			step: applyMutationStep{tool: "apply_resource", done: true, isError: success,
				result: `{"status":"ok","resources":[{"status":"applied","dry_run":true}]}`},
			want: mutationEvidenceNone,
		},
		{
			name: "apply partial failure",
			step: applyMutationStep{tool: "apply_resource", done: true, isError: success,
				result: `{"status":"partial_failure","resources":[{"status":"applied"},{"status":"failed"}]}`},
			want: mutationEvidenceUnknown,
		},
		{
			name: "patch persisted",
			step: applyMutationStep{tool: "patch_resource", done: true, isError: success,
				result: `{"status":"ok","dry_run":false}`},
			want: mutationEvidenceConfirmed,
		},
		{
			name: "patch dry run",
			step: applyMutationStep{tool: "patch_resource", done: true, isError: success,
				result: `{"status":"ok","dry_run":true}`},
			want: mutationEvidenceNone,
		},
		{
			name: "workload mutation",
			step: applyMutationStep{tool: "manage_workload", done: true, isError: success,
				result: `{"status":"ok","replicas":2}`},
			want: mutationEvidenceConfirmed,
		},
		{
			name: "rollout mutation",
			step: applyMutationStep{tool: "manage_rollout", done: true, isError: success,
				result: `{"status":"ok","operation":"abort"}`},
			want: mutationEvidenceConfirmed,
		},
		{
			name: "rollout explicit no op",
			step: applyMutationStep{tool: "manage_rollout", done: true, isError: success,
				result: `{"status":"ok","operation":"promote","noChange":true}`},
			want: mutationEvidenceNone,
		},
		{
			name: "cronjob mutation",
			step: applyMutationStep{tool: "manage_cronjob", done: true, isError: success,
				result: `{"status":"ok","jobName":"nightly-manual"}`},
			want: mutationEvidenceConfirmed,
		},
		{
			name: "node mutation",
			step: applyMutationStep{tool: "manage_node", done: true, isError: success,
				result: `{"status":"ok","evictedPods":["prod/api"]}`},
			want: mutationEvidenceConfirmed,
		},
		{
			name: "node partial drain",
			step: applyMutationStep{tool: "manage_node", done: true, isError: success,
				result: `{"status":"partial","evictedPods":["prod/api"],"errors":["prod/db: blocked"]}`},
			want: mutationEvidenceUnknown,
		},
		{
			name: "gitops mutation",
			step: applyMutationStep{tool: "manage_gitops", summary: `{"tool":"argocd","action":"sync"}`, done: true, isError: success,
				result: `{"status":"ok","requestedAt":"now"}`},
			want: mutationEvidenceConfirmed,
		},
		{
			name: "gitops dry run",
			step: applyMutationStep{tool: "manage_gitops", summary: `{"tool":"argocd","action":"rollback","dry_run":true}`, done: true, isError: success,
				result: `{"status":"ok","requestedAt":"now"}`},
			want: mutationEvidenceNone,
		},
		{
			name: "gitops missing args cannot rule out dry run",
			step: applyMutationStep{tool: "manage_gitops", done: true, isError: success,
				result: `{"status":"ok","requestedAt":"now"}`},
			want: mutationEvidenceUnknown,
		},
		{
			name: "host error can follow partial mutation",
			step: applyMutationStep{tool: "manage_rollout", done: true, isError: failure,
				result: "failed to patch Rollout spec"},
			want: mutationEvidenceUnknown,
		},
		{
			name: "missing host terminal state",
			step: applyMutationStep{tool: "manage_workload", done: true,
				result: `{"status":"ok"}`},
			want: mutationEvidenceUnknown,
		},
		{
			name: "truncated producer result",
			step: applyMutationStep{tool: "manage_workload", done: true, isError: success,
				result: `{"status":"ok"}`, truncated: true},
			want: mutationEvidenceUnknown,
		},
		{
			name: "unparseable producer result",
			step: applyMutationStep{tool: "manage_workload", done: true, isError: success,
				result: "Successfully scaled"},
			want: mutationEvidenceUnknown,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.step.evidence(); got != test.want {
				t.Fatalf("evidence = %v, want %v for %+v", got, test.want, test.step)
			}
		})
	}
}

func TestManageRolloutIsTrackedAsAWriteAcrossAgentNameShapes(t *testing.T) {
	for _, tool := range []string{"manage_rollout", "radar.manage_rollout", "mcp__radar__manage_rollout"} {
		t.Run(tool, func(t *testing.T) {
			var tracker applyMutationTracker
			tracker.observe(StreamEvent{Type: "step", Step: &StepInfo{
				ID: "rollout", Tool: tool, Status: "running", Summary: `{"action":"abort"}`,
			}})
			tracker.observe(StreamEvent{Type: "step", Step: &StepInfo{
				ID: "rollout", Status: "done", IsError: boolPointer(false),
				Result: `{"status":"ok","operation":"abort"}`,
			}})
			if got := tracker.outcome(ExecutionProfileSafeguarded); got != ApplyMutationConfirmed {
				t.Fatalf("outcome = %q, want confirmed", got)
			}
		})
	}
}

func TestStoppedOrStaleApplyDoesNotStartVerification(t *testing.T) {
	tests := []struct {
		name       string
		terminate  func(*RunManager, string) error
		wantStatus string
	}{
		{
			name: "stop",
			terminate: func(m *RunManager, id string) error {
				return m.Stop(id)
			},
			wantStatus: "stopped",
		},
		{
			name: "context switch",
			terminate: func(m *RunManager, _ string) error {
				m.OnContextSwitch()
				return nil
			},
			wantStatus: "stale",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m, r, calls := controlledRunManager(t, nil)
			if err := m.AddTurn(r.ID, "", true, "fix", false); err != nil {
				t.Fatal(err)
			}
			apply := receiveDiagnoseCall(t, calls)
			if err := test.terminate(m, r.ID); err != nil {
				t.Fatal(err)
			}
			<-apply.returned
			waitForRunNotInFlight(t, r)
			select {
			case call := <-calls:
				t.Fatalf("cancelled apply started verification: %+v", call.request)
			default:
			}
			if got := r.Summary().Status; got != test.wantStatus {
				t.Fatalf("status = %q, want %q", got, test.wantStatus)
			}
			r.mu.Lock()
			var outcome ApplyMutationOutcome
			for i := len(r.events) - 1; i >= 0; i-- {
				if r.events[i].Event.Type == "error" {
					outcome = r.events[i].Event.ApplyOutcome
					break
				}
			}
			r.mu.Unlock()
			if outcome != ApplyMutationUnknown {
				t.Fatalf("cancelled apply outcome = %q, want unknown", outcome)
			}
		})
	}
}

func TestStopDuringApplyWarnsThatTheWriteOutcomeIsUnknown(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	_, live, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if err := m.AddTurn(r.ID, "", true, "fix", false); err != nil {
		t.Fatal(err)
	}
	apply := receiveDiagnoseCall(t, calls)
	if err := m.Stop(r.ID); err != nil {
		t.Fatal(err)
	}
	terminal := waitForEvent(t, live, "error", 1)
	if !strings.Contains(terminal.Event.Error, "change may have completed") ||
		!strings.Contains(terminal.Event.Error, "re-check current cluster state") {
		t.Fatalf("stop during apply message = %q, want unknown-write warning", terminal.Event.Error)
	}
	if terminal.Event.ApplyOutcome != ApplyMutationUnknown {
		t.Fatalf("stop during apply outcome = %q, want unknown", terminal.Event.ApplyOutcome)
	}
	<-apply.returned
	waitForRunNotInFlight(t, r)
}

func TestManualVerificationDoesNotRecursivelyVerify(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	_, live, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if err := m.AddTurn(r.ID, "check again", false, "", true); err != nil {
		t.Fatal(err)
	}
	verify := receiveDiagnoseCall(t, calls)
	if !verify.request.Verify || verify.request.Apply {
		t.Fatalf("manual verification request = %+v", verify.request)
	}
	verify.respond <- controlledDiagnoseResponse{diag: Diagnosis{Healthy: true, SessionID: "read-session-2"}}
	waitForEvent(t, live, "done", 1)
	waitForRunNotInFlight(t, r)
	select {
	case call := <-calls:
		t.Fatalf("verification recursively started another call: %+v", call.request)
	default:
	}
}

func TestAddTurnRejectsApplyAndVerifyWithoutMutation(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	if err := m.AddTurn(r.ID, "", true, "fix", true); !errors.Is(err, ErrInvalidTurn) {
		t.Fatalf("AddTurn(apply+verify) = %v, want ErrInvalidTurn", err)
	}
	if got := r.Summary(); got.Status != "done" {
		t.Fatalf("rejected turn mutated run: %+v", got)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight || len(r.events) != 0 {
		t.Fatalf("rejected turn mutated execution state: inFlight=%v events=%+v", r.inFlight, r.events)
	}
	select {
	case call := <-calls:
		t.Fatalf("rejected turn started agent: %+v", call.request)
	default:
	}
}

func TestAddTurnRejectsBlankVerificationWithoutMutation(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	if err := m.AddTurn(r.ID, " \t ", false, "", true); !errors.Is(err, ErrVerificationQuestionRequired) {
		t.Fatalf("AddTurn(blank verification) = %v, want ErrVerificationQuestionRequired", err)
	}
	if got := r.Summary(); got.Status != "done" {
		t.Fatalf("rejected verification mutated run: %+v", got)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight || len(r.events) != 0 {
		t.Fatalf("rejected verification mutated execution state: inFlight=%v events=%+v", r.inFlight, r.events)
	}
	select {
	case call := <-calls:
		t.Fatalf("rejected verification started agent: %+v", call.request)
	default:
	}
}

func TestStopCancelsAutomaticVerificationWithoutExtraTerminal(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	if err := m.AddTurn(r.ID, "", true, "fix", false); err != nil {
		t.Fatal(err)
	}
	apply := receiveDiagnoseCall(t, calls)
	emitControlledWriteResult(apply, "write-1", boolPointer(false))
	apply.respond <- controlledDiagnoseResponse{diag: Diagnosis{Report: "changed"}}
	verify := receiveDiagnoseCall(t, calls)
	if err := m.Stop(r.ID); err != nil {
		t.Fatal(err)
	}
	<-verify.returned // proves Stop cancelled the verification context
	waitForRunNotInFlight(t, r)

	r.mu.Lock()
	events := append([]RunEvent(nil), r.events...)
	r.mu.Unlock()
	want := []string{"turn", "step", "step", "done", "turn", "error"}
	if len(events) != len(want) {
		t.Fatalf("events after stop = %+v", events)
	}
	for i, typ := range want {
		if events[i].Event.Type != typ {
			t.Fatalf("events after stop = %+v, want %v", events, want)
		}
	}
	if got := r.Summary().Status; got != "stopped" {
		t.Fatalf("status after stop = %q", got)
	}
}

func TestContextSwitchCancelsAutomaticVerificationAndCloses(t *testing.T) {
	m, r, calls := controlledRunManager(t, nil)
	if err := m.AddTurn(r.ID, "", true, "fix", false); err != nil {
		t.Fatal(err)
	}
	apply := receiveDiagnoseCall(t, calls)
	emitControlledWriteResult(apply, "write-1", boolPointer(false))
	apply.respond <- controlledDiagnoseResponse{diag: Diagnosis{Report: "changed"}}
	verify := receiveDiagnoseCall(t, calls)
	m.OnContextSwitch()
	<-verify.returned
	waitForRunNotInFlight(t, r)

	r.mu.Lock()
	events := append([]RunEvent(nil), r.events...)
	r.mu.Unlock()
	want := []string{"turn", "step", "step", "done", "turn", "error", "closed"}
	if len(events) != len(want) {
		t.Fatalf("events after context switch = %+v", events)
	}
	for i, typ := range want {
		if events[i].Event.Type != typ {
			t.Fatalf("events after context switch = %+v, want %v", events, want)
		}
	}
	if got := r.Summary().Status; got != "stale" {
		t.Fatalf("status after context switch = %q", got)
	}
}

// TestTurnCompletionOrdersTerminalBeforeNextTurn uses a deterministic barrier
// inside finishTurn. While the old turn's done event
// is not yet durably ordered, AddTurn has reached the manager critical section
// but cannot reserve or append its turn. Once released, persistence order must
// be done → running transition → next turn.
func TestTurnCompletionOrdersTerminalBeforeNextTurn(t *testing.T) {
	st := newBarrierRunStore("")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &RunManager{
		d: &Diagnoser{}, mcpPort: func() int { return 0 }, ctxLabel: func() string { return "ctx" },
		baseCtx: ctx, baseCancel: cancel, store: st,
		runs: map[string]*Run{}, maxConcurrent: 3, maxRetained: 10,
	}
	r := &Run{
		ID: "run-order", Kind: "Pod", Name: "p", Context: "ctx",
		Agent: "claude", Profile: ExecutionProfileSafeguarded,
		store: st, status: "running", sessionID: "session", inFlight: true,
		hydrated: true, CreatedAt: nowUTC(), updatedAt: nowUTC(),
		subs: map[int]chan RunEvent{},
	}
	m.runs[r.ID] = r
	m.order = []string{r.ID}

	terminalReady := make(chan struct{})
	releaseTerminal := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		r.finishTurnWithBarrier(Diagnosis{RootCause: "old conclusion"}, nil, false, time.Minute, func() {
			close(terminalReady)
			<-releaseTerminal
		})
		close(finished)
	}()
	<-terminalReady

	addStarted := make(chan struct{})
	addResult := make(chan error, 1)
	go func() {
		close(addStarted)
		_, err := m.beginTurn(r, true) // AddTurn's reservation after hydration
		if err == nil {
			r.mu.Lock()
			r.appendLocked(StreamEvent{Type: "turn", Question: "next question"})
			r.mu.Unlock()
		}
		addResult <- err
	}()
	<-addStarted
	// AddTurn holds m.mu while waiting for r.mu, which finishTurn deliberately
	// keeps across the blocked terminal enqueue. TryLock makes this a barrier,
	// rather than relying on a scheduling sleep.
	waitForMutexHeld(t, &m.mu)

	close(releaseTerminal)
	<-finished
	if err := <-addResult; err != nil {
		t.Fatalf("AddTurn after completion: %v", err)
	}

	ops := st.snapshot()
	doneAt := indexOfOp(ops, "event:done:done")
	runningAt := indexOfOp(ops, "save:running")
	turnAt := indexOfOp(ops, "event:turn")
	if doneAt < 0 || runningAt < 0 || turnAt < 0 || !(doneAt < runningAt && runningAt < turnAt) {
		t.Fatalf("durable order = %v, want done before running transition before next turn", ops)
	}
	r.mu.Lock()
	if len(r.events) < 2 || r.events[0].Event.Type != "done" ||
		r.events[1].Event.Type != "turn" || r.events[1].Event.Question != "next question" {
		t.Errorf("in-memory order = %+v, want old done then next turn", r.events)
	}
	r.mu.Unlock()
}

// TestStopOrdersTerminalBeforeAnyLaterTurn holds Stop at its durable error
// enqueue and proves a competing reservation cannot pass the run lock. After
// the cancelled agent releases inFlight, the next successful turn is still
// ordered strictly after the stopped marker.
func TestStopOrdersTerminalBeforeAnyLaterTurn(t *testing.T) {
	st := newBarrierRunStore("")
	m := &RunManager{runs: map[string]*Run{}, maxConcurrent: 3, maxRetained: 10}
	r := &Run{
		ID: "run-stop", Agent: "claude", Profile: ExecutionProfileSafeguarded,
		store: st, status: "running", sessionID: "session", inFlight: true,
		hydrated: true, CreatedAt: nowUTC(), updatedAt: nowUTC(),
		subs: map[int]chan RunEvent{},
	}
	m.runs[r.ID] = r
	m.order = []string{r.ID}

	terminalReady := make(chan struct{})
	releaseTerminal := make(chan struct{})
	stopResult := make(chan error, 1)
	go func() {
		stopResult <- m.stopWithBarrier(r.ID, func() {
			close(terminalReady)
			<-releaseTerminal
		})
	}()
	<-terminalReady

	competingResult := make(chan error, 1)
	go func() {
		_, err := m.beginTurn(r, true)
		competingResult <- err
	}()
	waitForMutexHeld(t, &m.mu)

	close(releaseTerminal)
	if err := <-stopResult; err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-competingResult; !errors.Is(err, ErrTurnInFlight) {
		t.Fatalf("turn racing Stop = %v, want ErrTurnInFlight", err)
	}
	if err := m.Stop(r.ID); err != nil {
		t.Fatalf("repeated Stop: %v", err)
	}

	// Model the cancelled DiagnoseStream returning, which releases the slot but
	// must not add another terminal marker because Stop owns it.
	r.finishTurn(Diagnosis{}, context.Canceled, false, time.Minute)
	if _, err := m.beginTurn(r, true); err != nil {
		t.Fatalf("turn after stopped agent exited: %v", err)
	}
	r.mu.Lock()
	r.appendLocked(StreamEvent{Type: "turn", Question: "after stop"})
	r.mu.Unlock()

	ops := st.snapshot()
	stoppedAt := indexOfOp(ops, "event:error:stopped")
	runningAt := indexOfOp(ops, "save:running")
	turnAt := indexOfOp(ops, "event:turn")
	if stoppedAt < 0 || runningAt < 0 || turnAt < 0 || !(stoppedAt < runningAt && runningAt < turnAt) {
		t.Fatalf("durable stop order = %v, want stopped marker before later running transition and turn", ops)
	}
	if countOp(ops, "event:error:stopped") != 1 {
		t.Fatalf("repeated Stop appended duplicate terminal markers: %v", ops)
	}
}

// TestContextSwitchWaitsForAdmittedStreamEvent pins the other side of the
// run-order contract. A callback is paused after admission but before append;
// context-switch must wait for it, then append its terminal pair. The callback
// can never cross durable closed.
func TestContextSwitchWaitsForAdmittedStreamEvent(t *testing.T) {
	st := newBarrierRunStore("")
	m := &RunManager{runs: map[string]*Run{}, maxRetained: 10}
	r := &Run{
		ID: "run-context", Context: "ctx-old", store: st,
		status: "running", inFlight: true, hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{},
	}
	m.runs[r.ID] = r
	m.order = []string{r.ID}

	callbackAdmitted := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackResult := make(chan bool, 1)
	go func() {
		callbackResult <- r.appendStreamEventWithBarrier(StreamEvent{Type: "phase", Phase: "at-boundary"}, func() {
			close(callbackAdmitted)
			<-releaseCallback
		})
	}()
	<-callbackAdmitted

	switched := make(chan struct{})
	go func() {
		m.OnContextSwitch()
		close(switched)
	}()
	close(releaseCallback)
	<-switched
	if appended := <-callbackResult; !appended {
		t.Fatal("callback admitted before context switch was incorrectly dropped")
	}

	r.mu.Lock()
	if got := r.events[len(r.events)-1].Event.Type; got != "closed" {
		t.Fatalf("in-memory log ends in %q, want closed: %+v", got, r.events)
	}
	if len(r.events) < 3 || r.events[len(r.events)-3].Event.Phase != "at-boundary" {
		t.Fatalf("admitted callback was not serialized before context terminal pair: %+v", r.events)
	}
	r.mu.Unlock()
	if ops := st.snapshot(); indexOfOp(ops, "event:phase") < 0 ||
		indexOfOp(ops, "event:error:stale") < 0 || ops[len(ops)-1] != "event:closed" {
		t.Fatalf("persisted order = %v, want admitted callback before atomic stale terminal pair", ops)
	}
	if batches := st.snapshotBatches(); len(batches) < 2 || strings.Join(batches[len(batches)-1], ",") != "error,closed" {
		t.Fatalf("persisted batches = %v, want final error+closed batch", batches)
	}
}

func TestMarkStalePersistsTerminalPairAtomically(t *testing.T) {
	for _, hydrated := range []bool{false, true} {
		name := "unhydrated"
		if hydrated {
			name = "hydrated"
		}
		t.Run(name, func(t *testing.T) {
			store := newBarrierRunStore("")
			run := &Run{
				ID: "run-stale", Context: "ctx-old", store: store,
				status: "running", inFlight: true, hydrated: hydrated,
				CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{},
			}
			var subscriber chan RunEvent
			if hydrated {
				subscriber = make(chan RunEvent, 4)
				run.subs[0] = subscriber
			}

			if transitioned := run.markStale(); !transitioned {
				t.Fatal("markStale did not perform the transition")
			}
			if transitioned := run.markStale(); transitioned {
				t.Fatal("repeated markStale performed a second transition")
			}

			batches := store.snapshotBatches()
			if len(batches) != 1 || strings.Join(batches[0], ",") != "error,closed" {
				t.Fatalf("persisted batches = %v, want one error+closed transaction", batches)
			}
			ops := store.snapshot()
			if len(ops) != 2 || ops[0] != "event:error:stale" || ops[1] != "event:closed" {
				t.Fatalf("persisted terminal order = %v, want stale error then closed", ops)
			}

			run.mu.Lock()
			if run.status != "stale" || run.subs != nil {
				t.Fatalf("final run state = status %q subs %v", run.status, run.subs)
			}
			inMemory := append([]RunEvent(nil), run.events...)
			run.mu.Unlock()
			if hydrated {
				if len(inMemory) != 2 || inMemory[0].Event.Type != "error" || inMemory[1].Event.Type != "closed" {
					t.Fatalf("in-memory terminal pair = %+v", inMemory)
				}
				var delivered []string
				for event := range subscriber {
					delivered = append(delivered, event.Event.Type)
				}
				if strings.Join(delivered, ",") != "error,closed" {
					t.Fatalf("subscriber terminal order = %v", delivered)
				}
			} else if len(inMemory) != 0 {
				t.Fatalf("lazy transcript was partially materialized: %+v", inMemory)
			}
		})
	}
}

// TestEvictKeepsRunning: the retention cap never drops a running investigation;
// it evicts the oldest finished one.
func TestEvictKeepsRunning(t *testing.T) {
	m := &RunManager{runs: map[string]*Run{}, maxRetained: 2}
	add := func(id, status string) {
		m.runs[id] = &Run{ID: id, status: status, subs: map[int]chan RunEvent{}}
		m.order = append(m.order, id)
	}
	add("a", "running") // oldest, but running → must survive
	add("b", "done")
	add("c", "done")
	m.evictLocked()

	if _, ok := m.runs["a"]; !ok {
		t.Errorf("running run 'a' was evicted")
	}
	if _, ok := m.runs["b"]; ok {
		t.Errorf("oldest finished run 'b' should have been evicted")
	}
	if len(m.order) != 2 {
		t.Errorf("order = %v, want len 2", m.order)
	}
}

func TestEvictionClosesOnlyInMemoryBeforeDeletingPersistedRun(t *testing.T) {
	store := newBarrierRunStore("")
	subscriber := make(chan RunEvent, 2)
	run := &Run{
		ID: "old", store: store, status: "done", hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(),
		subs: map[int]chan RunEvent{0: subscriber},
	}
	manager := &RunManager{
		store: store, runs: map[string]*Run{"old": run},
		order: []string{"old"}, maxRetained: 0,
	}

	manager.evictLocked()

	if _, ok := manager.runs["old"]; ok || len(manager.order) != 0 {
		t.Fatalf("evicted run remains addressable: runs=%v order=%v", manager.runs, manager.order)
	}
	if batches := store.snapshotBatches(); len(batches) != 0 {
		t.Fatalf("eviction persisted a resurrection-prone terminal event: %v", batches)
	}
	if ops := store.snapshot(); len(ops) != 1 || ops[0] != "delete:old" {
		t.Fatalf("eviction store operations = %v, want only delete:old", ops)
	}
	event, ok := <-subscriber
	if !ok || event.Event.Type != "closed" {
		t.Fatalf("subscriber terminal event = %+v, open=%v; want closed", event, ok)
	}
	if _, ok := <-subscriber; ok {
		t.Fatal("evicted subscriber channel remained open")
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.store != nil || len(run.events) != 1 || run.events[0].Event.Type != "closed" {
		t.Fatalf("evicted in-memory run = store %v events %+v", run.store, run.events)
	}
}

// TestRunMatchesTarget pins the Start focus-existing key: same resource+cluster
// focuses only when the agent AND execution profile also match, so a different profile
// starts its own run instead of silently reusing one.
func TestRunMatchesTarget(t *testing.T) {
	r := &Run{
		Kind: "Deployment", Group: "apps", Namespace: "ns", Name: "app",
		Context: "ctx", Agent: "codex", Profile: ExecutionProfileSafeguarded, Model: "o3", Effort: "high",
	}
	if !r.matchesTarget("Deployment", "apps", "ns", "app", "ctx", "codex", ExecutionProfileSafeguarded, "o3", "high") {
		t.Error("identical target+mode should match")
	}
	if r.matchesTarget("Deployment", "batch", "ns", "app", "ctx", "codex", ExecutionProfileSafeguarded, "o3", "high") {
		t.Error("different API group must NOT match")
	}
	if r.matchesTarget("Deployment", "apps", "ns", "app", "ctx", "claude", ExecutionProfileSafeguarded, "o3", "high") {
		t.Error("different agent must NOT match")
	}
	if r.matchesTarget("Deployment", "apps", "ns", "app", "ctx", "codex", ExecutionProfileFullLocal, "o3", "high") {
		t.Error("different execution profile must NOT match")
	}
	if r.matchesTarget("Deployment", "apps", "ns", "app", "other", "codex", ExecutionProfileSafeguarded, "o3", "high") {
		t.Error("different cluster context must NOT match")
	}
	if r.matchesTarget("Deployment", "apps", "ns", "app", "ctx", "codex", ExecutionProfileSafeguarded, "", "high") {
		t.Error("different model must NOT match")
	}
	if r.matchesTarget("Deployment", "apps", "ns", "app", "ctx", "codex", ExecutionProfileSafeguarded, "o3", "low") {
		t.Error("different effort must NOT match")
	}
}

// persistedManager builds a manager over a store with no live diagnoser — good
// enough for persistence-path tests (nothing spawns an agent).
func persistedManager(t *testing.T, store RunStore, ctx string) *RunManager {
	t.Helper()
	m := NewRunManager(nil, func() int { return 0 }, "", func() string { return ctx }, store)
	t.Cleanup(func() {
		// Don't let Shutdown close the shared test store between phases.
		m.baseCancel()
	})
	return m
}

// TestPersistenceRestartRoundtrip pins the core promise: a finished run's
// summary, transcript, and sessionId survive a "restart" (a second manager over
// the same store), replay parity included.
func TestPersistenceRestartRoundtrip(t *testing.T) {
	st, _ := testStore(t)

	m1 := persistedManager(t, st, "ctx-a")
	r := &Run{
		ID: "run-1", Kind: "Rollout", Group: "argoproj.io", Namespace: "ns", Name: "p", Context: "ctx-a",
		Agent: "claude", store: st, status: "running", hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{},
	}
	m1.mu.Lock()
	m1.runs[r.ID] = r
	m1.order = append(m1.order, r.ID)
	m1.mu.Unlock()
	st.SaveRun(r.Summary())

	r.append(StreamEvent{Type: "turn", Verify: true})
	r.append(StreamEvent{Type: "thinking", Token: "checking"})
	r.mu.Lock()
	r.status = "done"
	r.sessionID = "sess-42"
	r.preview = "bad image"
	r.mu.Unlock()
	r.append(StreamEvent{Type: "done", Diag: &Diagnosis{RootCause: "bad image"}})
	st.(*sqliteRunStore).barrier()

	// "Restart": fresh manager, same store.
	m2 := persistedManager(t, st, "ctx-a")
	runs := m2.List()
	if len(runs) != 1 || runs[0].Status != "done" || runs[0].SessionID != "sess-42" || runs[0].Preview != "bad image" || runs[0].Group != "argoproj.io" {
		t.Fatalf("restart lost state: %+v", runs)
	}
	if got := m2.Get("run-1").Group; got != "argoproj.io" {
		t.Fatalf("hydrated run group = %q, want argoproj.io", got)
	}
	// Replay parity: Subscribe hydrates the transcript from the store.
	r2 := m2.Get("run-1")
	backlog, _, _, cancel, err := r2.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if len(backlog) != 3 || backlog[2].Event.Type != "done" || backlog[2].Event.Diag == nil {
		t.Fatalf("replay after restart = %+v", backlog)
	}
	if !backlog[0].Event.Verify {
		t.Fatalf("replayed turn lost explicit verify marker: %+v", backlog[0])
	}
}

// TestPersistenceInterruptedRun pins crash recovery: a run persisted as
// "running" loads as error with a terminal event appended, so replay still ends
// in a terminal marker and Start won't focus the dead run.
func TestPersistenceInterruptedRun(t *testing.T) {
	st, _ := testStore(t)
	st.SaveRun(RunSummary{ID: "run-3", Kind: "Pod", Name: "p", Context: "ctx-a",
		Agent: "claude", Status: "running", CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	st.AppendEvent("run-3", RunEvent{Seq: 1, Event: StreamEvent{Type: "turn"}}, nil)
	st.(*sqliteRunStore).barrier()

	m := persistedManager(t, st, "ctx-a")
	runs := m.List()
	if len(runs) != 1 || runs[0].Status != "error" {
		t.Fatalf("interrupted run = %+v, want status error", runs)
	}
	r := m.Get("run-3")
	backlog, _, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	last := backlog[len(backlog)-1]
	if last.Event.Type != "error" || !strings.Contains(last.Event.Error, "restarted") {
		t.Fatalf("replay must end in the restart marker, got %+v", last)
	}
}

// TestPersistenceCursorNotResumable pins the accepted Cursor degradation: its
// resume is workspace-scoped and the workspace died with the old process, so a
// loaded Cursor run must refuse follow-ups via ErrNoSession — never spawn an
// agent guaranteed to fail.
func TestPersistenceCursorNotResumable(t *testing.T) {
	st, _ := testStore(t)
	st.SaveRun(RunSummary{ID: "run-1", Kind: "Pod", Name: "p", Context: "ctx-a",
		Agent: "cursor-agent", Profile: ExecutionProfileFullLocal,
		Status: "done", SessionID: "cursor-sess",
		CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	st.(*sqliteRunStore).barrier()

	m := persistedManager(t, st, "ctx-a")
	if err := m.AddTurn("run-1", "and?", false, "", false); !errors.Is(err, ErrNoSession) {
		t.Fatalf("cursor follow-up after restart = %v, want ErrNoSession", err)
	}
}

func TestPersistenceProfilelessRunCannotResume(t *testing.T) {
	st, _ := testStore(t)
	st.SaveRun(RunSummary{ID: "run-legacy", Kind: "Pod", Name: "p", Context: "ctx-a",
		Agent: "codex", Status: "done", SessionID: "codex-sess",
		CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	st.(*sqliteRunStore).barrier()

	m := persistedManager(t, st, "ctx-a")
	err := m.AddTurn("run-legacy", "and?", false, "", false)
	if err == nil || !strings.Contains(err.Error(), "unsupported execution profile") {
		t.Fatalf("profileless follow-up = %v, want unsupported profile", err)
	}
}

// TestPersistenceForeignContextSweep pins that history from another kube-context
// loads view-only: stale status, closed stream after replay, follow-ups refused.
func TestPersistenceForeignContextSweep(t *testing.T) {
	st, _ := testStore(t)
	st.SaveRun(RunSummary{ID: "run-1", Kind: "Pod", Name: "p", Context: "ctx-OLD",
		Agent: "claude", Profile: ExecutionProfileSafeguarded,
		Status: "done", SessionID: "s", CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	st.AppendEvent("run-1", RunEvent{Seq: 1, Event: StreamEvent{Type: "turn"}}, nil)
	st.(*sqliteRunStore).barrier()

	m := persistedManager(t, st, "ctx-NEW")
	runs := m.List() // sweep runs here (context label resolved)
	if len(runs) != 1 || runs[0].Status != "stale" {
		t.Fatalf("foreign-context run = %+v, want stale", runs)
	}
	if err := m.AddTurn("run-1", "and?", false, "", false); !errors.Is(err, ErrStale) {
		t.Fatalf("foreign-context follow-up = %v, want ErrStale", err)
	}
	st.(*sqliteRunStore).barrier()
	// The persisted log gained terminal markers (store-assigned seqs), so a
	// fresh subscribe replays and then CLOSES instead of hanging.
	r := m.Get("run-1")
	backlog, ch, _, cancel, err := r.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	last := backlog[len(backlog)-1]
	if last.Event.Type != "closed" {
		t.Fatalf("stale replay must end in closed, got %+v", backlog)
	}
	if _, ok := <-ch; ok {
		t.Error("stale run's live channel must be closed")
	}
}

// TestPersistenceEvictionDeletesRows pins that count-based eviction removes the
// run from the store too — history and memory can't drift apart.
func TestPersistenceEvictionDeletesRows(t *testing.T) {
	st, _ := testStore(t)
	m := persistedManager(t, st, "ctx-a")
	m.maxRetained = 2
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("run-%d", i)
		r := &Run{ID: id, Kind: "Pod", Name: "p", Context: "ctx-a", store: st,
			status: "done", hydrated: true, CreatedAt: nowUTC(), updatedAt: nowUTC(),
			subs: map[int]chan RunEvent{}}
		st.SaveRun(r.Summary())
		m.mu.Lock()
		m.runs[id] = r
		m.order = append(m.order, id)
		m.evictLocked()
		m.mu.Unlock()
	}
	st.(*sqliteRunStore).barrier()
	runs, _ := st.LoadRuns()
	if len(runs) != 2 {
		t.Fatalf("store kept %d runs after eviction, want 2", len(runs))
	}
	for _, r := range runs {
		if r.ID == "run-1" {
			t.Error("evicted run-1 still in store")
		}
	}
}

// TestClearHistoryKeepsRunning pins that Clear wipes finished runs (memory +
// store) but a live investigation survives, fully re-persisted.
func TestClearHistoryKeepsRunning(t *testing.T) {
	st, _ := testStore(t)
	m := persistedManager(t, st, "ctx-a")
	mk := func(id, status string) *Run {
		r := &Run{ID: id, Kind: "Pod", Name: "p", Context: "ctx-a", store: st,
			status: status, hydrated: true, CreatedAt: nowUTC(), updatedAt: nowUTC(),
			subs: map[int]chan RunEvent{}}
		st.SaveRun(r.Summary())
		m.mu.Lock()
		m.runs[id] = r
		m.order = append(m.order, id)
		m.mu.Unlock()
		return r
	}
	mk("run-1", "done")
	live := mk("run-2", "running")
	live.append(StreamEvent{Type: "turn"})

	if err := m.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	st.(*sqliteRunStore).barrier()
	runs := m.List()
	if len(runs) != 1 || runs[0].ID != "run-2" {
		t.Fatalf("memory after clear = %+v", runs)
	}
	stored, _ := st.LoadRuns()
	if len(stored) != 1 || stored[0].ID != "run-2" {
		t.Fatalf("store after clear = %+v", stored)
	}
	events, _ := st.LoadEvents("run-2")
	if len(events) != 1 || events[0].Event.Type != "turn" {
		t.Fatalf("live run's transcript not re-persisted: %+v", events)
	}
}

// TestPersistenceInterruptedFollowup pins crash recovery for a follow-up: the
// running transition persists at beginTurn, so a crash mid-follow-up (after a
// prior DONE verdict) still loads as error with a terminal restart marker —
// never a done row hiding an unterminated turn.
func TestPersistenceInterruptedFollowup(t *testing.T) {
	st, _ := testStore(t)
	m1 := persistedManager(t, st, "ctx-a")
	r := &Run{ID: "run-1", Kind: "Pod", Name: "p", Context: "ctx-a", store: st,
		status: "done", sessionID: "s", hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{}}
	st.SaveRun(r.Summary())
	r.append(StreamEvent{Type: "turn"})
	r.append(StreamEvent{Type: "done", Diag: &Diagnosis{Healthy: true}})
	m1.mu.Lock()
	m1.runs[r.ID] = r
	m1.order = append(m1.order, r.ID)
	m1.mu.Unlock()

	// A follow-up begins (status flips to running + persists)… then Radar dies.
	if _, err := m1.beginTurn(r, true); err != nil {
		t.Fatal(err)
	}
	r.append(StreamEvent{Type: "turn", Question: "and?"})
	st.(*sqliteRunStore).barrier()

	m2 := persistedManager(t, st, "ctx-a")
	runs := m2.List()
	if len(runs) != 1 || runs[0].Status != "error" {
		t.Fatalf("interrupted follow-up loaded as %+v, want error", runs)
	}
	backlog, _, _, cancel, err := m2.Get("run-1").Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	last := backlog[len(backlog)-1].Event
	if last.Type != "error" || !strings.Contains(last.Error, "restarted") {
		t.Fatalf("replay must end terminal after interrupted follow-up, got %+v", last)
	}
}

// TestPersistenceGracefulShutdown pins that Shutdown leaves an in-flight run's
// persisted log ending in a terminal event (stopped status + marker in one tx),
// so post-restart replay never leaves the UI spinning on an unterminated turn.
func TestPersistenceGracefulShutdown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ai-runs.db")
	st, err := OpenRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	m := NewRunManager(nil, func() int { return 0 }, "", func() string { return "ctx-a" }, st)
	r := &Run{ID: "run-1", Kind: "Pod", Name: "p", Context: "ctx-a", store: st,
		status: "running", inFlight: true, hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{}}
	st.SaveRun(r.Summary())
	r.append(StreamEvent{Type: "turn"})
	m.mu.Lock()
	m.runs[r.ID] = r
	m.order = append(m.order, r.ID)
	m.mu.Unlock()

	m.Shutdown() // marks stopped + appends terminal marker + drains and closes the store

	st2, err := OpenRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	runs, _ := st2.LoadRuns()
	if len(runs) != 1 || runs[0].Status != "stopped" {
		t.Fatalf("after graceful shutdown: %+v, want stopped", runs)
	}
	events, _ := st2.LoadEvents("run-1")
	last := events[len(events)-1].Event
	if last.Type != "error" || !strings.Contains(last.Error, "shutting down") {
		t.Fatalf("persisted log must end terminal after shutdown, got %+v", last)
	}
}

// TestHydrationFailureRefusesAppends pins the transcript-protection rule: when
// the persisted log can't be loaded, follow-ups and subscriptions report
// ErrHistoryUnavailable (never sequencing against or replaying an unknown
// prefix) and the run stays retryable.
func TestHydrationFailureRefusesAppends(t *testing.T) {
	st, _ := testStore(t)
	st.SaveRun(RunSummary{ID: "run-1", Kind: "Pod", Name: "p", Context: "ctx-a",
		Agent: "claude", Profile: ExecutionProfileSafeguarded,
		Status: "done", SessionID: "s", CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	st.(*sqliteRunStore).barrier()
	m := persistedManager(t, st, "ctx-a")
	st.Close() // simulate the DB becoming unreadable before first hydration

	if err := m.AddTurn("run-1", "and?", false, "", false); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("AddTurn with unloadable transcript = %v, want ErrHistoryUnavailable", err)
	}
	backlog, ch, _, cancel, err := m.Get("run-1").Subscribe(0)
	if !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("Subscribe with unloadable transcript = %v, want ErrHistoryUnavailable", err)
	}
	if backlog != nil || ch != nil || cancel != nil {
		t.Fatalf("failed subscription returned partial success: backlog=%+v channel=%t cancel=%t", backlog, ch != nil, cancel != nil)
	}
}

// TestContextSwitchIdempotentOnStale pins that a SECOND context switch doesn't
// re-terminalize an already-stale run — its log must keep ending in the closed
// sentinel (now durable, so a violation would persist and break every replay).
func TestContextSwitchIdempotentOnStale(t *testing.T) {
	st, _ := testStore(t)
	m := persistedManager(t, st, "ctx-b")
	r := &Run{ID: "run-1", Kind: "Pod", Name: "p", Context: "ctx-a", store: st,
		status: "done", hydrated: true, CreatedAt: nowUTC(), updatedAt: nowUTC(),
		subs: map[int]chan RunEvent{}}
	st.SaveRun(r.Summary())
	r.append(StreamEvent{Type: "turn"})
	m.mu.Lock()
	m.runs[r.ID] = r
	m.order = append(m.order, r.ID)
	m.mu.Unlock()

	m.OnContextSwitch() // ctx change #1: stale + error + closed
	before, _ := st.LoadEvents("run-1")
	m.OnContextSwitch() // ctx change #2: must be a no-op for this run
	after, _ := st.LoadEvents("run-1")
	if len(after) != len(before) {
		t.Fatalf("second switch appended events: %d → %d", len(before), len(after))
	}
	if last := after[len(after)-1].Event; last.Type != "closed" {
		t.Fatalf("log must end in closed, got %+v", last)
	}
}

// TestHistoryUnavailableSurfaces pins the degraded-visibility contract for the
// two setup failure modes: a store that never opened (server marks it) and a
// store whose existing contents couldn't be loaded (manager refuses it — new
// runs must not mint colliding ids against unknown DB contents).
func TestHistoryUnavailableSurfaces(t *testing.T) {
	m := NewRunManager(nil, func() int { return 0 }, "", func() string { return "ctx" }, nil)
	if m.HistoryDegraded() {
		t.Error("memory-only by CONFIG must not read as degraded")
	}
	m.MarkHistoryUnavailable("/nonexistent/ai-runs.db")
	if !m.HistoryDegraded() {
		t.Error("open failure must surface as degraded")
	}
	if err := m.ClearHistory(); err != nil {
		t.Errorf("clearing an already-missing broken DB must succeed, got %v", err)
	}

	st, _ := testStore(t)
	st.SaveRun(RunSummary{ID: "run-7", Kind: "Pod", Name: "p", Context: "ctx",
		Status: "done", CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	st.(*sqliteRunStore).barrier()
	st.Close() // LoadRuns will fail in loadPersisted
	m2 := NewRunManager(nil, func() int { return 0 }, "", func() string { return "ctx" }, st)
	if !m2.HistoryDegraded() {
		t.Error("load failure must surface as degraded")
	}
	if m2.store != nil {
		t.Error("load failure must detach the store — writes against unknown contents overwrite history")
	}
	// Clear must honor the user's intent even for a detached DB: remove the
	// files so a later healthy startup can't resurrect "cleared" history.
	if err := m2.ClearHistory(); err != nil {
		t.Fatalf("clear with detached store: %v", err)
	}
	if _, err := os.Stat(m2.brokenDBPath); !os.IsNotExist(err) {
		t.Error("broken history DB file must be removed by clear")
	}
}

// TestNewRunIDUnique pins the cross-process safety property: ids are random,
// not a counter — two processes sharing the history DB (standalone next to a
// long-running instance) must never mint the same id and overwrite each
// other's transcripts.
func TestNewRunIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newRunID()
		if !strings.HasPrefix(id, "run-") || len(id) < 10 {
			t.Fatalf("unexpected id shape %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

// TestLoadSkipsLiveForeignRunning pins the shared-DB ownership rule: a
// "running" row owned by another LIVE process must be neither repaired (that
// would falsely fail their active run) nor adopted; once the owner is dead,
// the next load repairs it as interrupted.
func TestLoadSkipsLiveForeignRunning(t *testing.T) {
	st, _ := testStore(t)
	// Owned by THIS process (alive) but not this manager — must be skipped.
	st.SaveRun(RunSummary{ID: "run-alive", Kind: "Pod", Name: "p", Context: "ctx",
		Status: "running", OwnerPID: os.Getpid(), CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	// Owned by a dead pid — must be repaired to error.
	st.SaveRun(RunSummary{ID: "run-dead", Kind: "Pod", Name: "p2", Context: "ctx",
		Status: "running", OwnerPID: 1 << 30, CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	st.(*sqliteRunStore).barrier()

	m := persistedManager(t, st, "ctx")
	if m.Get("run-alive") != nil {
		t.Error("live foreign running row must not be adopted")
	}
	dead := m.Get("run-dead")
	if dead == nil || dead.snapshotStatus() != "error" {
		t.Fatalf("dead-owner running row must be repaired, got %v", dead)
	}
	st.(*sqliteRunStore).barrier()
	runs, _ := st.LoadRuns()
	for _, r := range runs {
		if r.ID == "run-alive" && r.Status != "running" {
			t.Errorf("live foreign row was mutated: %+v", r)
		}
	}
}

// TestClearHistoryPreservesForeignLiveRun pins the shared-store clear contract.
// A manager intentionally omits another live owner's run from m.runs, so the
// persistence transaction — not an in-memory keep list — must retain that row
// and transcript while deleting terminal history owned by the same process.
func TestClearHistoryPreservesForeignLiveRun(t *testing.T) {
	ownerStore, dbPath := testStore(t)
	clearerStore, err := OpenRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clearerStore.Close)
	now := nowUTC()
	foreignLive := RunSummary{
		ID: "run-foreign-live", Kind: "Pod", Name: "foreign-live", Context: "ctx-a",
		Status: "running", OwnerPID: os.Getpid(), CreatedAt: now, UpdatedAt: now,
	}
	foreignDone := RunSummary{
		ID: "run-foreign-done", Kind: "Pod", Name: "foreign-done", Context: "ctx-a",
		Status: "done", OwnerPID: os.Getpid(), CreatedAt: now, UpdatedAt: now,
	}
	for _, summary := range []RunSummary{foreignLive, foreignDone} {
		ownerStore.SaveRun(summary)
		ownerStore.AppendEvent(summary.ID, RunEvent{Seq: 1, Event: StreamEvent{Type: "turn"}}, nil)
	}
	ownerStore.(*sqliteRunStore).barrier()

	manager := persistedManager(t, clearerStore, "ctx-a")
	if manager.Get(foreignLive.ID) != nil {
		t.Fatal("manager adopted a live run owned by another manager")
	}
	if manager.Get(foreignDone.ID) == nil {
		t.Fatal("terminal foreign history was not loaded for clearing")
	}

	localLive := &Run{
		ID: "run-local-live", Kind: "Pod", Name: "local-live", Context: "ctx-a",
		OwnerPID: os.Getpid(), store: clearerStore, status: "running", hydrated: true,
		CreatedAt: now, updatedAt: now, subs: map[int]chan RunEvent{},
	}
	clearerStore.SaveRun(localLive.Summary())
	localLive.append(StreamEvent{Type: "turn"})
	manager.mu.Lock()
	manager.runs[localLive.ID] = localLive
	manager.order = append(manager.order, localLive.ID)
	manager.mu.Unlock()

	if err := manager.ClearHistory(); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}
	stored, err := ownerStore.LoadRuns()
	if err != nil {
		t.Fatal(err)
	}
	gotRuns := make(map[string]string, len(stored))
	for _, summary := range stored {
		gotRuns[summary.ID] = summary.Status
	}
	if len(gotRuns) != 2 || gotRuns[foreignLive.ID] != "running" || gotRuns[localLive.ID] != "running" {
		t.Fatalf("stored runs after clear = %+v, want local and foreign live rows", stored)
	}
	for _, id := range []string{foreignLive.ID, localLive.ID} {
		events, loadErr := ownerStore.LoadEvents(id)
		if loadErr != nil {
			t.Fatalf("LoadEvents(%q): %v", id, loadErr)
		}
		if len(events) != 1 || events[0].Event.Type != "turn" {
			t.Fatalf("live transcript %q after clear = %+v", id, events)
		}
	}
	terminalEvents, err := ownerStore.LoadEvents(foreignDone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(terminalEvents) != 0 {
		t.Fatalf("terminal foreign transcript survived clear: %+v", terminalEvents)
	}
	if manager.Get(foreignDone.ID) != nil {
		t.Fatal("terminal foreign history survived in manager memory")
	}
	if manager.Get(localLive.ID) != localLive {
		t.Fatal("local live run did not survive manager clear fence")
	}
}

// TestClearHistoryClosesFollowupRace pins the clear-vs-follow-up race: once
// ClearHistory commits to dropping a terminal run, a concurrent follow-up must
// get ErrRunNotFound — never revive a run whose rows are being deleted.
func TestClearHistoryClosesFollowupRace(t *testing.T) {
	st, _ := testStore(t)
	m := persistedManager(t, st, "ctx-a")
	r := &Run{ID: "run-1", Kind: "Pod", Name: "p", Context: "ctx-a", store: st,
		status: "done", sessionID: "s", hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{}}
	st.SaveRun(r.Summary())
	m.mu.Lock()
	m.runs[r.ID] = r
	m.order = append(m.order, r.ID)
	m.mu.Unlock()

	if err := m.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	if err := m.AddTurn("run-1", "revive?", false, "", false); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("follow-up after clear = %v, want ErrRunNotFound", err)
	}
}

// TestClearHistoryFencesStartAndAddTurn pins both sides of the manager fence.
// While the synchronous store transaction is in progress, neither a new run nor
// a follow-up may reserve an agent. After commit, Start creates a reachable row
// after the clear while the dropped run's follow-up fails as not found.
func TestClearHistoryFencesStartAndAddTurn(t *testing.T) {
	store := &blockingClearRunStore{
		barrierRunStore: newBarrierRunStore(""),
		clearEntered:    make(chan struct{}),
		releaseClear:    make(chan struct{}),
	}
	baseCtx, baseCancel := context.WithCancel(context.Background())
	t.Cleanup(baseCancel)
	agentFinished := make(chan struct{})
	manager := &RunManager{
		diagnose: func(context.Context, Request, func(StreamEvent)) (Diagnosis, error) {
			close(agentFinished)
			return Diagnosis{}, errors.New("test agent finished")
		},
		mcpPort:       func() int { return 9280 },
		ctxLabel:      func() string { return "ctx-a" },
		baseCtx:       baseCtx,
		baseCancel:    baseCancel,
		store:         store,
		runs:          map[string]*Run{},
		maxRetained:   10,
		maxConcurrent: 3,
	}
	old := &Run{
		ID: "run-old", Kind: "Pod", Namespace: "default", Name: "old", Context: "ctx-a",
		store: store, status: "done", sessionID: "session-old", hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{},
	}
	manager.runs[old.ID] = old
	manager.order = []string{old.ID}

	clearResult := make(chan error, 1)
	go func() { clearResult <- manager.ClearHistory() }()
	select {
	case <-store.clearEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("ClearHistory did not enter the synchronous store transaction")
	}

	type startResult struct {
		summary RunSummary
		err     error
	}
	startReady := make(chan struct{})
	started := make(chan startResult, 1)
	go func() {
		close(startReady)
		summary, err := manager.Start("Pod", "", "default", "new", "claude", ExecutionProfileSafeguarded, "", "", "", nil, nil)
		started <- startResult{summary: summary, err: err}
	}()
	<-startReady

	addReady := make(chan struct{})
	added := make(chan error, 1)
	go func() {
		close(addReady)
		added <- manager.AddTurn(old.ID, "revive?", false, "", false)
	}()
	<-addReady

	// The manager lock must remain held for the whole store transaction. A short
	// scheduling window lets both goroutines reach that deterministic mutex fence.
	select {
	case result := <-started:
		t.Fatalf("Start escaped an in-progress clear: %+v", result)
	case err := <-added:
		t.Fatalf("AddTurn escaped an in-progress clear: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(store.releaseClear)
	if err := <-clearResult; err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}
	var start startResult
	select {
	case start = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not resume after clear committed")
	}
	if start.err != nil {
		t.Fatalf("Start after clear: %v", start.err)
	}
	if got := manager.Get(start.summary.ID); got == nil {
		t.Fatal("run started after clear is not addressable")
	}
	select {
	case err := <-added:
		if !errors.Is(err, ErrRunNotFound) {
			t.Fatalf("AddTurn after clear = %v, want ErrRunNotFound", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AddTurn did not resume after clear committed")
	}
	select {
	case <-agentFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("new run's agent did not launch")
	}

	ops := store.snapshot()
	clearEnd, saveRunning := indexOfOp(ops, "clear:end"), indexOfOp(ops, "save:running")
	if clearEnd < 0 || saveRunning < 0 || clearEnd >= saveRunning {
		t.Fatalf("new live row was not ordered after clear commit: %v", ops)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run := manager.Get(start.summary.ID)
		run.mu.Lock()
		inFlight := run.inFlight
		run.mu.Unlock()
		if !inFlight {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("new run did not release its agent reservation")
}

// TestBeginTurnRejectsPointerRemovedByClear covers the narrower interleaving in
// which AddTurn resolved a run before ClearHistory removed it. The stale pointer
// must never become in-flight or enqueue a replacement running row afterward.
func TestBeginTurnRejectsPointerRemovedByClear(t *testing.T) {
	store := newBarrierRunStore("")
	manager := &RunManager{
		store: store, runs: map[string]*Run{}, maxRetained: 10, maxConcurrent: 3,
	}
	run := &Run{
		ID: "run-old", Kind: "Pod", Name: "old", Context: "ctx-a", store: store,
		status: "done", sessionID: "session-old", hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{},
	}
	manager.runs[run.ID] = run
	manager.order = []string{run.ID}
	resolvedBeforeClear := run

	if err := manager.ClearHistory(); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}
	if _, err := manager.beginTurn(resolvedBeforeClear, true); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("beginTurn on pre-clear pointer = %v, want ErrRunNotFound", err)
	}
	run.mu.Lock()
	inFlight, status, attached := run.inFlight, run.status, run.store != nil
	run.mu.Unlock()
	if inFlight || status == "running" || attached {
		t.Fatalf("cleared pointer revived: inFlight=%v status=%q storeAttached=%v", inFlight, status, attached)
	}
	if indexOfOp(store.snapshot(), "save:running") >= 0 {
		t.Fatalf("cleared pointer recreated a running row: %v", store.snapshot())
	}
}

func TestClearHistoryDetachesLazySubscriptionInFlight(t *testing.T) {
	store := &blockingLoadRunStore{
		barrierRunStore: newBarrierRunStore(""),
		loadEntered:     make(chan struct{}),
		releaseLoad:     make(chan struct{}),
		events: []RunEvent{
			{Seq: 1, Event: StreamEvent{Type: "turn", Question: "old transcript"}},
		},
	}
	run := &Run{
		ID: "run-lazy", Kind: "Pod", Name: "p", Context: "ctx-a", store: store,
		status: "done", hydrated: false,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{},
	}
	manager := &RunManager{
		store: store, runs: map[string]*Run{run.ID: run},
		order: []string{run.ID}, maxRetained: 10,
	}

	type subscribeResult struct {
		backlog []RunEvent
		ch      <-chan RunEvent
		cancel  func()
		err     error
	}
	result := make(chan subscribeResult, 1)
	go func() {
		backlog, ch, _, cancel, err := run.Subscribe(0)
		result <- subscribeResult{backlog: backlog, ch: ch, cancel: cancel, err: err}
	}()
	<-store.loadEntered

	if err := manager.ClearHistory(); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}
	close(store.releaseLoad)

	var got subscribeResult
	select {
	case got = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("lazy Subscribe did not finish after clear released its store read")
	}
	if !errors.Is(got.err, ErrHistoryUnavailable) {
		t.Fatalf("Subscribe racing clear = %v, want ErrHistoryUnavailable", got.err)
	}
	if got.backlog != nil || got.ch != nil || got.cancel != nil {
		t.Fatalf("cleared subscription replayed old data: backlog=%+v channel=%t cancel=%t", got.backlog, got.ch != nil, got.cancel != nil)
	}
	if manager.Get(run.ID) != nil {
		t.Fatal("cleared run remains addressable")
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.store != nil || run.hydrated || len(run.events) != 1 || run.events[0].Event.Type != "closed" {
		t.Fatalf("detached run state = store %v hydrated %v events %+v", run.store, run.hydrated, run.events)
	}
}

// TestClearHistoryRestoresOnFailure pins the failure path: a failed store
// clear must put the runs back — the UI keeps showing what the DB still holds.
func TestClearHistoryRestoresOnFailure(t *testing.T) {
	st, _ := testStore(t)
	m := persistedManager(t, st, "ctx-a")
	r := &Run{ID: "run-1", Kind: "Pod", Name: "p", Context: "ctx-a", store: st,
		status: "done", hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{}}
	st.SaveRun(r.Summary())
	m.mu.Lock()
	m.runs[r.ID] = r
	m.order = append(m.order, r.ID)
	m.mu.Unlock()
	st.(*sqliteRunStore).barrier()
	st.Close() // Clear will fail

	if err := m.ClearHistory(); err == nil {
		t.Fatal("clear on a closed store must fail — memory was about to drop state the DB still holds")
	}
	if m.Get("run-1") == nil {
		t.Fatal("failed clear must restore the run to the list")
	}
}

// barrierRunStore is an ordered, observable RunStore for run-state race tests.
// Blocking one event intentionally violates the production store's non-blocking
// performance contract so the test can hold the exact serialization boundary;
// it does not change the ordering contract being exercised.
type barrierRunStore struct {
	blockEvent string
	entered    chan struct{}
	release    chan struct{}
	enterOnce  sync.Once

	mu      sync.Mutex
	ops     []string
	batches [][]string
}

// blockingLoadRunStore keeps a lazy hydration in flight while ClearHistory
// detaches the run. The embedded store supplies the rest of the RunStore API.
type blockingLoadRunStore struct {
	*barrierRunStore
	loadEntered chan struct{}
	releaseLoad chan struct{}
	loadOnce    sync.Once
	events      []RunEvent
}

// blockingClearRunStore exposes the exact synchronous Clear transaction so
// tests can prove manager reservations cannot interleave with it.
type blockingClearRunStore struct {
	*barrierRunStore
	clearEntered chan struct{}
	releaseClear chan struct{}
	clearOnce    sync.Once
}

func (s *blockingClearRunStore) ClearTerminal() error {
	s.record("clear:begin")
	s.clearOnce.Do(func() { close(s.clearEntered) })
	<-s.releaseClear
	s.record("clear:end")
	return nil
}

func (s *blockingLoadRunStore) LoadEvents(string) ([]RunEvent, error) {
	s.loadOnce.Do(func() { close(s.loadEntered) })
	<-s.releaseLoad
	return append([]RunEvent(nil), s.events...), nil
}

func (s *blockingLoadRunStore) ClearTerminal() error {
	s.record("clear")
	return nil
}

func newBarrierRunStore(blockEvent string) *barrierRunStore {
	return &barrierRunStore{
		blockEvent: blockEvent,
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (s *barrierRunStore) record(op string) {
	s.mu.Lock()
	s.ops = append(s.ops, op)
	s.mu.Unlock()
}

func (s *barrierRunStore) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ops...)
}

func (s *barrierRunStore) snapshotBatches() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.batches))
	for i, batch := range s.batches {
		out[i] = append([]string(nil), batch...)
	}
	return out
}

func (s *barrierRunStore) SaveRun(sum RunSummary) {
	s.record("save:" + sum.Status)
}

func (s *barrierRunStore) AppendEvent(_ string, event RunEvent, sum *RunSummary) {
	s.AppendEvents("", []RunEvent{event}, sum)
}

func (s *barrierRunStore) AppendEvents(_ string, events []RunEvent, sum *RunSummary) {
	types := make([]string, len(events))
	for i, event := range events {
		types[i] = event.Event.Type
	}
	s.mu.Lock()
	s.batches = append(s.batches, types)
	s.mu.Unlock()

	summaryRecorded := false
	for i, event := range events {
		if event.Event.Type == s.blockEvent {
			s.enterOnce.Do(func() { close(s.entered) })
			<-s.release
		}
		op := "event:" + event.Event.Type
		if sum != nil && !summaryRecorded &&
			(event.Event.Type == "done" || event.Event.Type == "error" || i == len(events)-1) {
			op += ":" + sum.Status
			summaryRecorded = true
		}
		s.record(op)
	}
}

func (s *barrierRunStore) LoadRuns() ([]RunSummary, error) { return nil, nil }
func (s *barrierRunStore) LoadEvents(string) ([]RunEvent, error) {
	return nil, nil
}
func (s *barrierRunStore) DeleteRun(id string)  { s.record("delete:" + id) }
func (s *barrierRunStore) ClearTerminal() error { return nil }
func (s *barrierRunStore) Degraded() bool       { return false }
func (s *barrierRunStore) Path() string         { return "" }
func (s *barrierRunStore) Close()               {}

func indexOfOp(ops []string, want string) int {
	for i, op := range ops {
		if op == want {
			return i
		}
	}
	return -1
}

func countOp(ops []string, want string) int {
	count := 0
	for _, op := range ops {
		if op == want {
			count++
		}
	}
	return count
}

func waitForMutexHeld(t *testing.T, mu *sync.Mutex) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !mu.TryLock() {
			return
		}
		mu.Unlock()
		runtime.Gosched()
	}
	t.Fatal("timed out waiting for concurrent operation to hold mutex")
}
