package ai

import "testing"

// TestRunSubscribeReplay pins the SSE-replay contract: a subscriber gets the
// backlog after its last-seen seq, then live events, then a close on terminal.
func TestRunSubscribeReplay(t *testing.T) {
	r := &Run{subs: map[int]chan RunEvent{}}
	r.append(StreamEvent{Type: "turn"})              // seq 1
	r.append(StreamEvent{Type: "phase"})             // seq 2
	r.append(StreamEvent{Type: "token", Token: "x"}) // seq 3

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
