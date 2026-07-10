package ai

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	backlog, ch, cancel := r.Subscribe(1) // everything after seq 1
	defer cancel()
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

	backlog, ch, cancel := r.Subscribe(0)
	defer cancel()
	if len(backlog) != 3 { // turn, done, closed
		t.Fatalf("backlog = %d, want 3", len(backlog))
	}
	if _, ok := <-ch; ok {
		t.Errorf("channel should be closed for a finalized run")
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

// TestRunMatchesTarget pins the Start focus-existing key: same resource+cluster
// focuses only when the agent AND isolation mode also match, so a different mode
// starts its own run instead of silently reusing one.
func TestRunMatchesTarget(t *testing.T) {
	r := &Run{
		Kind: "Deployment", Namespace: "ns", Name: "app",
		Context: "ctx", Agent: "codex", Isolated: true, Model: "o3", Effort: "high",
	}
	if !r.matchesTarget("Deployment", "ns", "app", "ctx", "codex", true, "o3", "high") {
		t.Error("identical target+mode should match")
	}
	if r.matchesTarget("Deployment", "ns", "app", "ctx", "claude", true, "o3", "high") {
		t.Error("different agent must NOT match")
	}
	if r.matchesTarget("Deployment", "ns", "app", "ctx", "codex", false, "o3", "high") {
		t.Error("different isolation mode must NOT match")
	}
	if r.matchesTarget("Deployment", "ns", "app", "other", "codex", true, "o3", "high") {
		t.Error("different cluster context must NOT match")
	}
	if r.matchesTarget("Deployment", "ns", "app", "ctx", "codex", true, "", "high") {
		t.Error("different model must NOT match")
	}
	if r.matchesTarget("Deployment", "ns", "app", "ctx", "codex", true, "o3", "low") {
		t.Error("different effort must NOT match")
	}
}

// persistedManager builds a manager over a store with no live diagnoser — good
// enough for persistence-path tests (nothing spawns an agent).
func persistedManager(t *testing.T, store RunStore, ctx string) *RunManager {
	t.Helper()
	m := NewRunManager(nil, func() int { return 0 }, func() string { return ctx }, store)
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
		ID: "run-1", Kind: "Pod", Namespace: "ns", Name: "p", Context: "ctx-a",
		Agent: "claude", store: st, status: "running", hydrated: true,
		CreatedAt: nowUTC(), updatedAt: nowUTC(), subs: map[int]chan RunEvent{},
	}
	m1.mu.Lock()
	m1.runs[r.ID] = r
	m1.order = append(m1.order, r.ID)
	m1.mu.Unlock()
	st.SaveRun(r.Summary())

	r.append(StreamEvent{Type: "turn"})
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
	if len(runs) != 1 || runs[0].Status != "done" || runs[0].SessionID != "sess-42" || runs[0].Preview != "bad image" {
		t.Fatalf("restart lost state: %+v", runs)
	}
	// Replay parity: Subscribe hydrates the transcript from the store.
	r2 := m2.Get("run-1")
	backlog, _, cancel := r2.Subscribe(0)
	defer cancel()
	if len(backlog) != 3 || backlog[2].Event.Type != "done" || backlog[2].Event.Diag == nil {
		t.Fatalf("replay after restart = %+v", backlog)
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
	backlog, _, cancel := r.Subscribe(0)
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
		Agent: "cursor-agent", Status: "done", SessionID: "cursor-sess",
		CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	st.(*sqliteRunStore).barrier()

	m := persistedManager(t, st, "ctx-a")
	if err := m.AddTurn("run-1", "and?", false, ""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("cursor follow-up after restart = %v, want ErrNoSession", err)
	}
}

// TestPersistenceForeignContextSweep pins that history from another kube-context
// loads view-only: stale status, closed stream after replay, follow-ups refused.
func TestPersistenceForeignContextSweep(t *testing.T) {
	st, _ := testStore(t)
	st.SaveRun(RunSummary{ID: "run-1", Kind: "Pod", Name: "p", Context: "ctx-OLD",
		Agent: "claude", Status: "done", SessionID: "s", CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	st.AppendEvent("run-1", RunEvent{Seq: 1, Event: StreamEvent{Type: "turn"}}, nil)
	st.(*sqliteRunStore).barrier()

	m := persistedManager(t, st, "ctx-NEW")
	runs := m.List() // sweep runs here (context label resolved)
	if len(runs) != 1 || runs[0].Status != "stale" {
		t.Fatalf("foreign-context run = %+v, want stale", runs)
	}
	if err := m.AddTurn("run-1", "and?", false, ""); !errors.Is(err, ErrStale) {
		t.Fatalf("foreign-context follow-up = %v, want ErrStale", err)
	}
	st.(*sqliteRunStore).barrier()
	// The persisted log gained terminal markers (store-assigned seqs), so a
	// fresh subscribe replays and then CLOSES instead of hanging.
	r := m.Get("run-1")
	backlog, ch, cancel := r.Subscribe(0)
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
	backlog, _, cancel := m2.Get("run-1").Subscribe(0)
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
	m := NewRunManager(nil, func() int { return 0 }, func() string { return "ctx-a" }, st)
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
// the persisted log can't be loaded, follow-ups are refused (never sequenced
// against an unknown prefix) and the run stays retryable.
func TestHydrationFailureRefusesAppends(t *testing.T) {
	st, _ := testStore(t)
	st.SaveRun(RunSummary{ID: "run-1", Kind: "Pod", Name: "p", Context: "ctx-a",
		Agent: "claude", Status: "done", SessionID: "s", CreatedAt: nowUTC(), UpdatedAt: nowUTC()})
	st.(*sqliteRunStore).barrier()
	m := persistedManager(t, st, "ctx-a")
	st.Close() // simulate the DB becoming unreadable before first hydration

	if err := m.AddTurn("run-1", "and?", false, ""); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("AddTurn with unloadable transcript = %v, want ErrHistoryUnavailable", err)
	}
	// Subscribe degrades to an immediately-closed stream (client retries).
	backlog, ch, cancel := m.Get("run-1").Subscribe(0)
	defer cancel()
	if len(backlog) != 0 {
		t.Fatalf("backlog on failed hydration = %+v", backlog)
	}
	if _, ok := <-ch; ok {
		t.Error("channel must be closed on failed hydration")
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
	m := NewRunManager(nil, func() int { return 0 }, func() string { return "ctx" }, nil)
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
	m2 := NewRunManager(nil, func() int { return 0 }, func() string { return "ctx" }, st)
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
	if err := m.AddTurn("run-1", "revive?", false, ""); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("follow-up after clear = %v, want ErrRunNotFound", err)
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
