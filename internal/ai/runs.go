package ai

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RunManager owns AI investigations as durable, server-side jobs. An investigation
// runs in a goroutine bound to a manager-owned context — NOT to any HTTP request —
// so it keeps going when the browser closes the panel, navigates away, or refreshes.
// Clients subscribe to a run's event stream (with replay) to watch live or catch up.
//
// In-memory only: runs are lost when the radar process restarts. The feature is
// gated to no-auth standalone radar, so a single local user owns all runs.
//
// Locking: the manager mutex (m.mu) guards the runs map/order. Each Run's mutable
// state (status, session, events, subs, …) is guarded by r.mu. Immutable identity
// fields (ID/Kind/Namespace/Name/Context/CreatedAt) are set once and read freely.
// Lock order is always m.mu → r.mu, never the reverse; the run goroutine never
// takes m.mu.
type RunManager struct {
	d        *Diagnoser
	mcpPort  func() int    // resolved lazily — the listener port isn't known at construction
	ctxLabel func() string // current kube-context label, for the run's baseline

	baseCtx    context.Context // parent of every run ctx; cancelled on Shutdown
	baseCancel context.CancelFunc

	// workRoot is a private, randomly-named scratch root created once per process
	// (mode 0700). Per-run dirs live UNDER it, so run scratch can't collide across
	// Radar restarts or co-running processes and isn't at a predictable /tmp path.
	// "" only if creating it failed (then runs get no workdir — Cursor falls back to
	// its own temp workspace per turn, losing cross-turn resume but staying correct).
	workRoot string

	mu            sync.Mutex
	runs          map[string]*Run
	order         []string // insertion order, for eviction
	nextID        int
	maxRetained   int // total runs kept in memory (running + finished)
	maxConcurrent int // concurrent IN-FLIGHT turns (= live agent processes)
}

// Run is one investigation: identity, status, the agent session to resume, and the
// canonical append-only event log (every subscriber reconstructs state from it).
type Run struct {
	ID        string // immutable
	Kind      string // immutable
	Namespace string // immutable
	Name      string // immutable
	Context   string // immutable — kube-context the run is about (baseline)
	Agent     string // immutable — backend CLI driving this run ("claude"/"codex")
	WorkDir   string // immutable — per-run scratch dir (under RunManager.workRoot); "" if none
	Isolated  bool   // immutable — isolation mode chosen at Start
	Model     string // immutable — optional model override ("" = agent default)
	Effort    string // immutable — optional reasoning effort (Codex; "" = default)
	ManagedBy string // immutable — GitOps/Helm owner of the target ("" = none), for the Apply warning
	Health    *ResourceHealthSignal
	CreatedAt time.Time

	mu        sync.Mutex
	status    string // running | done | error | stopped | stale
	sessionID string
	preview   string // last root cause, for the list
	updatedAt time.Time
	events    []RunEvent
	inFlight  bool
	subs      map[int]chan RunEvent
	nextSub   int
	cancel    context.CancelFunc
}

// RunEvent is a sequenced stream event. Seq drives SSE id: / Last-Event-ID replay.
type RunEvent struct {
	Seq   int         `json:"seq"`
	Event StreamEvent `json:"event"`
}

// RunSummary is an immutable snapshot of a run (no event log) for JSON responses.
type RunSummary struct {
	ID        string                `json:"id"`
	Kind      string                `json:"kind"`
	Namespace string                `json:"namespace"`
	Name      string                `json:"name"`
	Context   string                `json:"context"`
	Agent     string                `json:"agent,omitempty"`
	Isolated  bool                  `json:"isolated"`
	Model     string                `json:"model,omitempty"`
	Effort    string                `json:"effort,omitempty"`
	ManagedBy string                `json:"managedBy,omitempty"`
	Health    *ResourceHealthSignal `json:"health,omitempty"`
	Status    string                `json:"status"`
	SessionID string                `json:"sessionId,omitempty"`
	Preview   string                `json:"preview,omitempty"`
	CreatedAt time.Time             `json:"createdAt"`
	UpdatedAt time.Time             `json:"updatedAt"`
}

var (
	// ErrAtCapacity is returned by Start when too many investigations are running.
	ErrAtCapacity = errors.New("too many investigations running")
	// ErrRunNotFound is returned for an unknown run id.
	ErrRunNotFound = errors.New("investigation not found")
	// ErrTurnInFlight is returned when a turn is already running for a run.
	ErrTurnInFlight = errors.New("a turn is already running")
	// ErrNoSession is returned when a follow-up/apply is attempted before the
	// agent has produced a resumable session id.
	ErrNoSession = errors.New("investigation has no resumable session yet")
	// ErrStale is returned when continuing a run whose cluster context changed.
	ErrStale = errors.New("investigation ran against a different cluster")
)

const (
	defaultMaxConcurrent = 3  // running child processes
	defaultMaxRetained   = 20 // total runs kept in memory

	// defaultTurnTimeout bounds one agent turn's wall-clock time. Generous — a
	// deep multi-tool investigation runs minutes, not tens of minutes — while
	// guaranteeing a hung CLI eventually frees its concurrency slot.
	defaultTurnTimeout = 15 * time.Minute
)

// turnTimeout returns the per-turn wall-clock ceiling (RADAR_AI_TURN_TIMEOUT
// accepts a Go duration, e.g. "30m", for unusually slow setups).
func turnTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("RADAR_AI_TURN_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultTurnTimeout
}

// NewRunManager builds a manager over a resolved Diagnoser. mcpPort/ctxLabel are
// callbacks because the listener port and kube-context are only known at runtime.
func NewRunManager(d *Diagnoser, mcpPort func() int, ctxLabel func() string) *RunManager {
	ctx, cancel := context.WithCancel(context.Background())
	// Best-effort: a failure here just means runs get no shared workdir (logged).
	root, err := os.MkdirTemp("", "radar-ai-")
	if err != nil {
		log.Printf("[ai] could not create AI scratch root: %v (Cursor resume will be degraded)", err)
		root = ""
	}
	return &RunManager{
		d:             d,
		mcpPort:       mcpPort,
		ctxLabel:      ctxLabel,
		baseCtx:       ctx,
		baseCancel:    cancel,
		workRoot:      root,
		runs:          map[string]*Run{},
		maxRetained:   defaultMaxRetained,
		maxConcurrent: defaultMaxConcurrent,
	}
}

// runWorkDir is the per-run scratch dir under the manager's private root — stable
// across a run's turns so a workspace-scoped resume (Cursor) reattaches to the
// prior turn's session. "" when no root exists (backends then self-manage).
func (m *RunManager) runWorkDir(id string) string {
	if m.workRoot == "" {
		return ""
	}
	return filepath.Join(m.workRoot, id)
}

// Shutdown cancels every run (killing agent child processes) — called on server
// stop so local agents don't outlive radar.
func (m *RunManager) Shutdown() {
	m.baseCancel()
	m.mu.Lock()
	runs := make([]*Run, 0, len(m.runs))
	for _, r := range m.runs {
		runs = append(runs, r)
	}
	m.mu.Unlock()
	for _, r := range runs {
		r.mu.Lock()
		c := r.cancel
		r.mu.Unlock()
		if c != nil {
			c()
		}
	}
	// Drop every run's scratch in one shot — the process is going away.
	if m.workRoot != "" {
		_ = os.RemoveAll(m.workRoot)
	}
}

// countInFlightLocked counts runs with a live agent turn. Caller holds m.mu.
func (m *RunManager) countInFlightLocked() int {
	n := 0
	for _, r := range m.runs {
		r.mu.Lock()
		if r.inFlight {
			n++
		}
		r.mu.Unlock()
	}
	return n
}

// beginTurn atomically reserves a turn slot for r: enforces the concurrency cap
// and the run's preconditions, then marks it in-flight — so two concurrent turn
// requests can't both spawn an agent. Returns the session to resume.
func (m *RunManager) beginTurn(r *Run, requireSession bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.countInFlightLocked() >= m.maxConcurrent {
		return "", ErrAtCapacity
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.inFlight:
		return "", ErrTurnInFlight
	case r.status == "stale":
		return "", ErrStale
	case requireSession && r.sessionID == "":
		return "", ErrNoSession
	}
	r.inFlight = true
	r.status = "running"
	r.updatedAt = nowUTC()
	return r.sessionID, nil
}

// AgentName normalizes a client-requested backend name against the available
// backends (falls back to the default).
func (m *RunManager) AgentName(name string) string { return m.d.AgentName(name) }

func (m *RunManager) ctx() string {
	if m.ctxLabel != nil {
		return m.ctxLabel()
	}
	return ""
}

// Start creates and launches an investigation, or focuses an existing live run for
// the same target+context instead of duplicating it. Returns ErrAtCapacity when
// the concurrent-running cap is reached.
func (m *RunManager) Start(kind, namespace, name, agent string, isolated bool, model, effort, managedBy string, health *ResourceHealthSignal) (RunSummary, error) {
	cur := m.ctx()
	m.mu.Lock()
	// Focus an existing live run for this exact target+mode rather than duplicate it.
	for _, id := range m.order {
		r := m.runs[id]
		if r.matchesTarget(kind, namespace, name, cur, agent, isolated, model, effort) &&
			r.snapshotStatus() == "running" {
			m.mu.Unlock()
			return r.Summary(), nil
		}
	}
	if m.countInFlightLocked() >= m.maxConcurrent {
		m.mu.Unlock()
		return RunSummary{}, ErrAtCapacity
	}
	m.nextID++
	id := fmt.Sprintf("run-%d", m.nextID)
	r := &Run{
		ID: id, Kind: kind, Namespace: namespace,
		Name: name, Context: cur, Agent: agent, WorkDir: m.runWorkDir(id), Isolated: isolated,
		Model: model, Effort: effort, ManagedBy: managedBy, Health: health, CreatedAt: nowUTC(),
		status: "running", inFlight: true, updatedAt: nowUTC(),
		subs: map[int]chan RunEvent{},
	}
	m.runs[r.ID] = r
	m.order = append(m.order, r.ID)
	m.evictLocked()
	m.mu.Unlock()

	m.launchTurn(r, "", false, "", "")
	return r.Summary(), nil
}

// AddTurn runs a follow-up (question) or an apply turn (with the confirmed fix).
// beginTurn atomically enforces the cap + preconditions and marks the run in-flight.
func (m *RunManager) AddTurn(id, question string, apply bool, fix string) error {
	r := m.get(id)
	if r == nil {
		return ErrRunNotFound
	}
	session, err := m.beginTurn(r, true)
	if err != nil {
		return err
	}
	m.launchTurn(r, question, apply, fix, session)
	return nil
}

// launchTurn emits a turn marker then runs the agent in a manager-owned goroutine.
// The caller has already marked the run in-flight (atomically with the cap check).
// Subscribers stay attached across turns — only stale / evict closes them (a
// stopped run can still take follow-up turns, so Stop leaves streams open).
func (m *RunManager) launchTurn(r *Run, question string, apply bool, fix, session string) {
	// Wall-clock ceiling per turn: a wedged CLI would otherwise hold one of the
	// maxConcurrent slots forever (maxTurns caps model turns, not real time).
	timeout := turnTimeout()
	ctx, cancel := context.WithTimeout(m.baseCtx, timeout)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()

	r.append(StreamEvent{Type: "turn", Question: question, Apply: apply})

	go func() {
		defer cancel()
		diag, err := m.d.DiagnoseStream(ctx, Request{
			Kind: r.Kind, Namespace: r.Namespace, Name: r.Name,
			MCPPort: m.mcpPort(), SessionID: session,
			Question: question, Apply: apply, Fix: fix,
			Agent: r.Agent, Isolated: r.Isolated, Model: r.Model, Effort: r.Effort,
			Health: r.Health, WorkDir: r.WorkDir,
		}, func(ev StreamEvent) { r.append(ev) })

		r.mu.Lock()
		r.inFlight = false
		r.updatedAt = nowUTC()
		// If Stop/OnContextSwitch already terminalized the run, don't overwrite its
		// status or append after the sentinel — even when the agent exited cleanly.
		if r.status == "stopped" || r.status == "stale" {
			r.mu.Unlock()
			return
		}
		if err != nil {
			r.status = "error"
			r.mu.Unlock()
			msg := err.Error()
			if errors.Is(err, context.DeadlineExceeded) {
				msg = fmt.Sprintf("The investigation timed out after %s and was stopped. Re-run Diagnose, or ask a narrower follow-up.", timeout)
			}
			r.append(StreamEvent{Type: "error", Error: msg})
			return
		}
		// Keep the read-only investigation session as the canonical resume target.
		// An apply turn runs in its OWN fresh, write-enabled session (injection
		// hardening) — adopting it would make follow-ups resume the write transcript
		// and collapse the read/write context separation.
		if diag.SessionID != "" && !apply {
			r.sessionID = diag.SessionID
		}
		if diag.RootCause != "" {
			r.preview = diag.RootCause
		} else if diag.Healthy {
			r.preview = "Healthy"
		}
		r.status = "done"
		r.mu.Unlock()
		r.append(StreamEvent{Type: "done", Diag: &diag})
	}()
}

// Stop cancels a run's in-flight agent (killing its process group) and marks it stopped.
func (m *RunManager) Stop(id string) error {
	r := m.get(id)
	if r == nil {
		return ErrRunNotFound
	}
	r.mu.Lock()
	if !r.inFlight {
		r.mu.Unlock()
		return nil // nothing to stop
	}
	r.status = "stopped"
	c := r.cancel
	r.mu.Unlock()
	if c != nil {
		c() // the run goroutine sees status=stopped and won't overwrite it
	}
	r.append(StreamEvent{Type: "error", Error: "Investigation stopped."})
	return nil
}

// OnContextSwitch cancels running investigations and marks every run stale + closed:
// their reasoning is about the previous cluster, so they can't safely continue or
// apply against the newly-connected one.
func (m *RunManager) OnContextSwitch() {
	m.mu.Lock()
	runs := make([]*Run, 0, len(m.runs))
	for _, r := range m.runs {
		runs = append(runs, r)
	}
	m.mu.Unlock()
	for _, r := range runs {
		r.mu.Lock()
		c := r.cancel
		r.status = "stale"
		r.mu.Unlock()
		if c != nil {
			c()
		}
		r.append(StreamEvent{Type: "error", Error: "Cluster context changed — this investigation was about a different cluster."})
		r.finalize()
		r.removeWorkDir() // stale runs can't resume — their workspace is dead weight
	}
}

// Get returns a run by id (nil if unknown).
func (m *RunManager) Get(id string) *Run { return m.get(id) }

func (m *RunManager) get(id string) *Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[id]
}

// List returns run summaries, newest first.
func (m *RunManager) List() []RunSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RunSummary, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, m.runs[m.order[i]].Summary())
	}
	return out
}

// evictLocked drops the oldest finished run when over the retention cap. Running
// runs are never evicted. Caller holds m.mu.
func (m *RunManager) evictLocked() {
	for len(m.order) > m.maxRetained {
		idx := -1
		for i, id := range m.order {
			if m.runs[id].snapshotStatus() != "running" {
				idx = i
				break
			}
		}
		if idx < 0 {
			return // all running — keep them
		}
		id := m.order[idx]
		victim := m.runs[id]
		delete(m.runs, id)
		m.order = append(m.order[:idx], m.order[idx+1:]...)
		victim.finalize()
		victim.removeWorkDir() // best-effort: drop the evicted run's scratch dir
	}
}

// removeWorkDir deletes a run's scratch dir (best-effort, async). Safe once the run
// is finalized/evicted: it can no longer produce turns, so nothing will read it.
func (r *Run) removeWorkDir() {
	if r.WorkDir != "" {
		go os.RemoveAll(r.WorkDir)
	}
}

// Summary snapshots a run's current state under r.mu.
func (r *Run) Summary() RunSummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return RunSummary{
		ID: r.ID, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name,
		Context: r.Context, Agent: r.Agent, Isolated: r.Isolated,
		Model: r.Model, Effort: r.Effort, ManagedBy: r.ManagedBy,
		Health: r.Health,
		Status: r.status, SessionID: r.sessionID,
		Preview: r.preview, CreatedAt: r.CreatedAt, UpdatedAt: r.updatedAt,
	}
}

// matchesTarget reports whether r is the same investigation as a Start request —
// same resource + cluster AND same agent/isolation mode. The mode is part of the
// key so starting codex-isolated never silently focuses a live claude or my-setup
// run for the same resource. Immutable fields, so no lock needed.
func (r *Run) matchesTarget(kind, namespace, name, ctx, agent string, isolated bool, model, effort string) bool {
	return r.Kind == kind && r.Namespace == namespace && r.Name == name &&
		r.Context == ctx && r.Agent == agent && r.Isolated == isolated &&
		r.Model == model && r.Effort == effort
}

func (r *Run) snapshotStatus() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Subscribe returns the backlog after afterSeq plus a channel of future events.
// The channel is closed only when the run is finalized (stale/evicted) — NOT when
// a turn completes, so the same subscription sees later turns.
func (r *Run) Subscribe(afterSeq int) (backlog []RunEvent, ch <-chan RunEvent, cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.Seq > afterSeq {
			backlog = append(backlog, e)
		}
	}
	c := make(chan RunEvent, 256)
	if r.subs == nil { // finalized run — replay then close
		close(c)
		return backlog, c, func() {}
	}
	id := r.nextSub
	r.nextSub++
	r.subs[id] = c
	return backlog, c, func() {
		r.mu.Lock()
		if ch, ok := r.subs[id]; ok {
			delete(r.subs, id)
			close(ch)
		}
		r.mu.Unlock()
	}
}

// append records an event and fans it out non-blockingly. A subscriber whose buffer
// is full is dropped (it reconnects with Last-Event-ID to replay).
func (r *Run) append(ev StreamEvent) {
	r.mu.Lock()
	re := RunEvent{Seq: len(r.events) + 1, Event: ev}
	r.events = append(r.events, re)
	r.updatedAt = nowUTC()
	for id, ch := range r.subs {
		select {
		case ch <- re:
		default:
			delete(r.subs, id)
			close(ch)
		}
	}
	r.mu.Unlock()
}

// finalize emits a terminal sentinel and closes all subscribers; further Subscribe
// calls replay the log then close. Used when a run can no longer produce turns.
// Idempotent: a context-switched (stale) run can later age past the retention cap
// and be finalized again by eviction — the second call must not append a second
// "closed" sentinel to the replay log.
func (r *Run) finalize() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subs == nil {
		return
	}
	re := RunEvent{Seq: len(r.events) + 1, Event: StreamEvent{Type: "closed"}}
	r.events = append(r.events, re)
	r.updatedAt = nowUTC()
	for id, ch := range r.subs {
		select {
		case ch <- re:
		default: // full buffer — the close below still ends the stream
		}
		delete(r.subs, id)
		close(ch)
	}
	r.subs = nil
}

func nowUTC() time.Time { return time.Now().UTC() }
