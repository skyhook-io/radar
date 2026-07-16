package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
// Runs live in memory and (when a RunStore is configured) persist to SQLite so
// history survives restarts. The feature is gated to no-auth standalone radar,
// so a single local user owns all runs.
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

	// store persists runs + event logs across restarts (nil = memory-only,
	// the historical behavior). Owned here: Shutdown closes it.
	store RunStore

	mu            sync.Mutex
	runs          map[string]*Run
	order         []string // insertion order, for eviction
	maxRetained   int      // total runs kept in memory (running + finished)
	maxConcurrent int      // concurrent IN-FLIGHT turns (= live agent processes)
	sweptCtx      string   // last kube-context the loaded-run staleness sweep ran for
	// historyUnavailable marks that persistence was requested but is broken
	// (store failed to open, or its existing contents couldn't be loaded) — the
	// UI must say history won't survive a restart instead of implying it will.
	historyUnavailable bool
	// brokenDBPath is the unusable history DB's location. Kept so ClearHistory
	// can still honor the user's intent by removing the files — otherwise a
	// later healthy startup would resurrect investigations they "cleared".
	brokenDBPath string
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
	// OwnerPID is the process that owns this run's lifecycle. Persisted so a
	// second process sharing the history DB (standalone beside a long-running
	// instance) can tell a LIVE foreign run from one orphaned by a crash.
	OwnerPID int

	// store mirrors RunManager.store (nil = memory-only) so the event hot path
	// can persist without reaching back to the manager.
	store RunStore

	mu        sync.Mutex
	status    string // running | done | error | stopped | stale
	sessionID string
	preview   string // last root cause, for the list
	updatedAt time.Time
	events    []RunEvent
	// hydrated marks that r.events holds the run's full log. Runs created live
	// are born hydrated; runs loaded from the store hydrate lazily on first
	// read/mutation (ensureHydrated) so startup doesn't pay for old transcripts.
	hydrated bool
	inFlight bool
	subs     map[int]chan RunEvent
	nextSub  int
	cancel   context.CancelFunc
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
	OwnerPID  int                   `json:"ownerPid,omitempty"`
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
	// ErrHistoryUnavailable is returned when a run's persisted transcript can't
	// be loaded — appending without it could overwrite stored history.
	ErrHistoryUnavailable = errors.New("investigation history is unavailable right now — try again")
)

const (
	defaultMaxConcurrent = 3   // running child processes
	defaultMaxRetained   = 100 // total runs kept (memory rows + store)

	// defaultHistoryAge is how long finished runs are kept in the store; older
	// ones are dropped at startup. Count-based eviction still applies first.
	defaultHistoryAge = 30 * 24 * time.Hour

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
// store persists history across restarts (nil = memory-only); persisted runs are
// hydrated into the manager here.
func NewRunManager(d *Diagnoser, mcpPort func() int, ctxLabel func() string, store RunStore) *RunManager {
	ctx, cancel := context.WithCancel(context.Background())
	// Best-effort: a failure here just means runs get no shared workdir (logged).
	root, err := os.MkdirTemp("", "radar-ai-")
	if err != nil {
		log.Printf("[ai] could not create AI scratch root: %v (Cursor resume will be degraded)", err)
		root = ""
	}
	m := &RunManager{
		d:             d,
		mcpPort:       mcpPort,
		ctxLabel:      ctxLabel,
		baseCtx:       ctx,
		baseCancel:    cancel,
		workRoot:      root,
		store:         store,
		runs:          map[string]*Run{},
		maxRetained:   defaultMaxRetained,
		maxConcurrent: defaultMaxConcurrent,
	}
	m.loadPersisted()
	return m
}

// loadPersisted hydrates run ROWS from the store (event logs stay lazy — see
// ensureHydrated) and normalizes state that can't carry across a process:
//   - a persisted "running" run was interrupted by the restart → error, with a
//     terminal event appended so replay still ends in a terminal marker;
//   - Cursor sessions are workspace-scoped and the workspace was a process-
//     lifetime temp dir → drop the sessionID so follow-ups report "no session"
//     instead of spawning an agent guaranteed to fail;
//   - run ids are random (newRunID), so ids can't collide across processes
//     sharing the history DB (an ephemeral `radar diagnose --standalone` next
//     to a long-running instance) or across restarts.
func (m *RunManager) loadPersisted() {
	if m.store == nil {
		return
	}
	sums, err := m.store.LoadRuns()
	if err != nil {
		// Refusing the store entirely is the only safe response: with the
		// existing contents unknown, new runs would mint colliding run-N ids and
		// INSERT OR REPLACE would overwrite the stored transcripts.
		log.Printf("[ai] could not load run history — running memory-only to protect it: %v", err)
		m.brokenDBPath = m.store.Path()
		m.store.Close()
		m.store = nil
		m.historyUnavailable = true
		return
	}
	cutoff := nowUTC().Add(-defaultHistoryAge)
	for _, s := range sums {
		if s.ID == "" {
			continue
		}
		if s.UpdatedAt.Before(cutoff) {
			m.store.DeleteRun(s.ID) // age-based retention
			continue
		}
		if s.Agent == "cursor-agent" {
			s.SessionID = ""
		}
		// A "running" row owned by another LIVE process is not interrupted — it
		// belongs to a long-running instance (or another standalone) sharing
		// this DB right now. Repairing it would falsely fail their active run;
		// adopting it would show a run this process can't stream. Leave it to
		// its owner; a later boot repairs it once the owner is gone.
		// At construction this manager owns nothing yet, so any alive-owner
		// running row is foreign — even a same-pid one (another manager in
		// this process).
		if s.Status == "running" && pidAlive(s.OwnerPID) {
			continue
		}
		r := &Run{
			ID: s.ID, Kind: s.Kind, Namespace: s.Namespace, Name: s.Name,
			Context: s.Context, Agent: s.Agent, Isolated: s.Isolated,
			Model: s.Model, Effort: s.Effort, ManagedBy: s.ManagedBy,
			Health: s.Health, CreatedAt: s.CreatedAt, OwnerPID: s.OwnerPID,
			store:  m.store,
			status: s.Status, sessionID: s.SessionID, preview: s.Preview,
			updatedAt: s.UpdatedAt,
			subs:      map[int]chan RunEvent{},
		}
		if s.Status == "running" {
			// Interrupted by the restart. Terminal statuses are written in the
			// same transaction as their terminal event, so a "running" row means
			// the log has no terminal marker yet — append one (store-assigned
			// seq; the in-memory log stays lazy).
			r.status = "error"
			r.updatedAt = nowUTC()
			sum := r.summaryLocked()
			m.store.AppendEvent(r.ID, RunEvent{Event: StreamEvent{
				Type:  "error",
				Error: "Radar restarted while this investigation was running. Re-run Diagnose to analyze the current cluster.",
			}}, &sum)
		}
		if r.status == "stale" {
			r.subs = nil // stale runs were finalized — streams replay then close
		}
		m.runs[r.ID] = r
		m.order = append(m.order, r.ID)
	}
	m.evictLocked() // the retention cap may have shrunk since the DB was written
}

// newRunID mints a process-independent id. Random (not a counter) because
// several processes can share the history DB — an ephemeral standalone run
// minting counter ids next to a long-running instance would collide and
// INSERT OR REPLACE another investigation's transcript.
func newRunID() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback keeps ids unique within this process; collision across
		// processes needs the same nanosecond, which the entropy above exists
		// to avoid anyway.
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return "run-" + hex.EncodeToString(b[:])
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
// stop so local agents don't outlive radar. In-flight runs are marked stopped
// BEFORE their contexts are cancelled so the run goroutines' terminal-status
// guard keeps them from persisting a spurious "context canceled" error; the
// store then drains and closes, and anything appended after that is a no-op.
func (m *RunManager) Shutdown() {
	m.mu.Lock()
	runs := make([]*Run, 0, len(m.runs))
	for _, r := range m.runs {
		runs = append(runs, r)
	}
	m.mu.Unlock()
	for _, r := range runs {
		r.mu.Lock()
		inFlight := r.inFlight
		if inFlight {
			r.status = "stopped"
			r.updatedAt = nowUTC()
		}
		c := r.cancel
		r.mu.Unlock()
		if inFlight {
			// Terminal marker BEFORE cancelling: replay must never end mid-turn
			// (the UI would spin forever), and the error-typed event carries the
			// stopped summary in one store transaction.
			r.append(StreamEvent{Type: "error", Error: "Investigation stopped — Radar was shutting down."})
		}
		if c != nil {
			c()
		}
	}
	m.baseCancel()
	if m.store != nil {
		m.store.Close()
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
	// Persist the running transition NOW: if Radar dies mid-turn, restart
	// recovery keys off the status column — a stale terminal status here would
	// leave an unterminated turn in the replayed transcript with no repair.
	if r.store != nil {
		r.store.SaveRun(r.summaryLocked())
	}
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
	id := newRunID()
	r := &Run{
		ID: id, Kind: kind, Namespace: namespace,
		Name: name, Context: cur, Agent: agent, WorkDir: m.runWorkDir(id), Isolated: isolated,
		Model: model, Effort: effort, ManagedBy: managedBy, Health: health, CreatedAt: nowUTC(),
		OwnerPID: os.Getpid(),
		store:    m.store,
		status:   "running", inFlight: true, updatedAt: nowUTC(),
		hydrated: true, // born live — its full log is in memory by construction
		subs:     map[int]chan RunEvent{},
	}
	m.runs[r.ID] = r
	m.order = append(m.order, r.ID)
	m.evictLocked()
	m.mu.Unlock()
	if m.store != nil {
		m.store.SaveRun(r.Summary())
	}

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
	// A follow-up on a run loaded from history must extend the PERSISTED log —
	// hydrate before beginTurn so the new turn's sequence numbers continue it.
	// Refusing on failure protects the stored transcript: appending against an
	// unknown prefix would re-sequence from 1 and overwrite it.
	if !r.ensureHydrated() {
		return ErrHistoryUnavailable
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
		}, func(ev StreamEvent) {
			// The agent can keep streaming briefly after Stop/context-switch
			// cancel it (process-group kill has a WaitDelay). Those events must
			// not land after the terminal marker — replay ordering is the
			// contract every subscriber rebuilds from.
			if st := r.snapshotStatus(); st == "stopped" || st == "stale" {
				return
			}
			r.append(ev)
		})

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
	// An in-flight run is always hydrated (born live); a loaded run can't be
	// in-flight, so its early return below makes a failed hydration harmless.
	r.ensureHydrated()
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
		inFlight := r.inFlight
		hydrated := r.hydrated
		alreadyStale := r.status == "stale"
		r.mu.Unlock()
		// A second switch (A→B→C) must not re-terminalize an already-stale run:
		// its log already ends in the closed sentinel, and appending after it
		// would break the replay contract (durably, now that logs persist).
		if alreadyStale {
			continue
		}
		if !inFlight && !hydrated {
			// Loaded, never-touched history: mark stale without paying to load
			// its transcript (terminal markers get store-assigned seqs).
			r.markStale()
			r.removeWorkDir()
			continue
		}
		r.mu.Lock()
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
	m.sweepForeignLocked()
	return m.runs[id]
}

// List returns run summaries, newest first.
func (m *RunManager) List() []RunSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepForeignLocked()
	out := make([]RunSummary, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, m.runs[m.order[i]].Summary())
	}
	return out
}

// HistoryDegraded reports that run persistence isn't working — history will
// not survive a restart. Surfaced on the runs-list response so the UI can say
// so. True when the store broke mid-flight (write failures) OR never became
// usable (open/load failure with persistence requested).
func (m *RunManager) HistoryDegraded() bool {
	m.mu.Lock()
	unavailable := m.historyUnavailable
	m.mu.Unlock()
	return unavailable || (m.store != nil && m.store.Degraded())
}

// MarkHistoryUnavailable records that persistence was requested but couldn't be
// set up (the DB at dbPath failed to open) so the UI can surface it — and so
// ClearHistory can still remove the files.
func (m *RunManager) MarkHistoryUnavailable(dbPath string) {
	m.mu.Lock()
	m.historyUnavailable = true
	m.brokenDBPath = dbPath
	m.mu.Unlock()
}

// sweepForeignLocked marks runs loaded from a PREVIOUS process against a
// different kube-context as stale — the same treatment OnContextSwitch gives
// live runs when the context changes under them. Runs once per observed context
// label; the label callback resolves only after the cluster connects, which is
// why this can't happen at load time. Caller holds m.mu.
func (m *RunManager) sweepForeignLocked() {
	cur := m.ctx()
	if cur == "" || cur == m.sweptCtx {
		return
	}
	m.sweptCtx = cur
	for _, r := range m.runs {
		if r.Context != cur {
			r.markStale()
		}
	}
}

// ClearHistory drops every terminal run from memory and the store. Live
// (running) runs survive and are re-persisted so their rows aren't orphaned by
// the wipe.
func (m *RunManager) ClearHistory() error {
	// Remove terminal runs from ADDRESSABILITY first, atomically: a follow-up
	// racing the clear would otherwise revive a run whose rows are about to be
	// deleted, leaving a live agent on an orphaned object. Once out of m.runs,
	// AddTurn/Get can't find them (ErrRunNotFound), so the window is closed.
	m.mu.Lock()
	origOrder := append([]string(nil), m.order...)
	kept := make([]string, 0, len(m.order))
	var dropped []*Run
	var droppedIDs []string
	for _, id := range m.order {
		r := m.runs[id]
		if r.snapshotStatus() == "running" {
			kept = append(kept, id)
			continue
		}
		dropped = append(dropped, r)
		droppedIDs = append(droppedIDs, id)
		delete(m.runs, id)
	}
	m.order = kept
	m.mu.Unlock()

	// One transaction deleting only the non-kept rows, so a crash mid-clear
	// can't lose a live investigation. On FAILURE, restore the removed runs —
	// the UI must keep showing what the DB still holds.
	if m.store != nil {
		if err := m.store.Clear(kept); err != nil {
			m.mu.Lock()
			for i, r := range dropped {
				m.runs[droppedIDs[i]] = r
			}
			m.order = origOrder
			m.mu.Unlock()
			return err
		}
	}
	// A broken (detached) history DB still holds investigations on disk — a
	// later healthy startup would resurrect what the user just "cleared".
	// Removing the files IS the recovery for an unopenable/unloadable DB.
	m.mu.Lock()
	broken := m.brokenDBPath
	m.mu.Unlock()
	if broken != "" {
		for _, f := range []string{broken, broken + "-wal", broken + "-shm"} {
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("history DB is unusable and couldn't be removed: %w", err)
			}
		}
	}

	for _, r := range dropped {
		// Detach the store before finalizing: the run's rows were just deleted,
		// and finalize's persisted closed-sentinel would re-create them.
		r.mu.Lock()
		r.store = nil
		r.mu.Unlock()
		r.finalize()
		r.removeWorkDir()
	}
	return nil
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
		if m.store != nil {
			m.store.DeleteRun(id)
		}
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
	return r.summaryLocked()
}

// summaryLocked builds the snapshot; the caller holds r.mu (or has exclusive
// access to a not-yet-shared run).
func (r *Run) summaryLocked() RunSummary {
	return RunSummary{
		ID: r.ID, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name,
		Context: r.Context, Agent: r.Agent, Isolated: r.Isolated,
		Model: r.Model, Effort: r.Effort, ManagedBy: r.ManagedBy,
		Health: r.Health,
		Status: r.status, SessionID: r.sessionID, OwnerPID: r.OwnerPID,
		Preview: r.preview, CreatedAt: r.CreatedAt, UpdatedAt: r.updatedAt,
	}
}

// ensureHydrated loads the run's event log from the store on first touch. Every
// path that reads or extends the log (Subscribe, follow-up turns, Stop) calls it
// first, so sequence numbers always continue from the persisted log. Idempotent
// and safe under concurrency: a racing second load just re-installs the same
// immutable prefix before either appends.
//
// On a load FAILURE the run stays un-hydrated (retryable) and callers must not
// append: sequencing against an unknown prefix would restart at seq 1 and
// overwrite the persisted transcript.
func (r *Run) ensureHydrated() bool {
	if r.store == nil {
		return true
	}
	r.mu.Lock()
	if r.hydrated {
		r.mu.Unlock()
		return true
	}
	r.mu.Unlock()
	events, err := r.store.LoadEvents(r.ID) // outside r.mu — a DB read may wait on the writer
	if err != nil {
		log.Printf("[ai] could not load transcript for %s: %v", r.ID, err)
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hydrated {
		return true
	}
	// If the run was finalized while we were loading (a context switch marked it
	// stale and enqueued its terminal markers), the snapshot we hold predates
	// them. Installing it would freeze a short prefix forever — stay
	// un-hydrated so the next touch reloads through the writer barrier.
	if r.subs == nil && (len(events) == 0 || events[len(events)-1].Event.Type != "closed") {
		return false
	}
	r.events = events
	r.hydrated = true
	return true
}

// markStale flips a non-running run to stale and finalizes its stream without
// requiring hydration: the terminal error + closed markers are appended in the
// STORE with store-assigned sequence numbers, and the in-memory log stays lazy —
// the next Subscribe hydrates and replays them. Used for runs loaded from a
// previous process whose kube-context no longer matches.
func (r *Run) markStale() {
	r.mu.Lock()
	if r.status == "stale" || r.inFlight {
		r.mu.Unlock()
		return
	}
	r.status = "stale"
	r.updatedAt = nowUTC()
	sum := r.summaryLocked()
	hydrated := r.hydrated
	// Deliver the terminal pair to live subscribers BEFORE closing their
	// channels (finalize's pattern) — an abrupt close forces an EventSource
	// reconnect round-trip instead of a clean terminal replay. Sequence
	// numbers: continue the in-memory log for hydrated runs; the unhydrated
	// case has no subscribers to speak of (Subscribe hydrates first), so the
	// in-flight delivery uses provisional seqs and the STORE keeps the
	// authoritative ones.
	staleEv := StreamEvent{Type: "error", Error: "Cluster context changed — this investigation was about a different cluster."}
	closedEv := StreamEvent{Type: "closed"}
	for i, ev := range []StreamEvent{staleEv, closedEv} {
		re := RunEvent{Seq: len(r.events) + 1 + i, Event: ev}
		for _, ch := range r.subs {
			select {
			case ch <- re:
			default:
			}
		}
	}
	for id, ch := range r.subs {
		delete(r.subs, id)
		close(ch)
	}
	r.subs = nil
	r.mu.Unlock()
	if r.store == nil {
		return
	}
	if hydrated {
		// The in-memory log is authoritative — persist with explicit seqs so
		// memory and store stay aligned.
		r.mu.Lock()
		stale := RunEvent{Seq: len(r.events) + 1, Event: staleEv}
		closed := RunEvent{Seq: len(r.events) + 2, Event: closedEv}
		r.events = append(r.events, stale, closed)
		r.mu.Unlock()
		r.store.AppendEvent(r.ID, stale, nil)
		r.store.AppendEvent(r.ID, closed, &sum)
		return
	}
	// The stale status rides BOTH events: if the second write is lost, the DB
	// must never show a non-terminal status over a log that already carries the
	// cluster-change marker.
	r.store.AppendEvent(r.ID, RunEvent{Event: staleEv}, &sum)
	r.store.AppendEvent(r.ID, RunEvent{Event: closedEv}, &sum)
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
	// A run loaded from history replays its persisted transcript. On a load
	// failure, return an immediately-closed stream WITHOUT registering — the
	// client's EventSource reconnect retries hydration (it stayed un-hydrated).
	if !r.ensureHydrated() {
		c := make(chan RunEvent)
		close(c)
		return nil, c, func() {}
	}
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
// Persistence rides along: the event is enqueued to the store under r.mu (enqueue
// never blocks), and TERMINAL events ("done"/"error") carry the run's summary so
// the status column and its terminal event commit in one transaction — crash
// recovery can then trust that a "running" row has no terminal marker.
func (r *Run) append(ev StreamEvent) {
	r.mu.Lock()
	re := RunEvent{Seq: len(r.events) + 1, Event: ev}
	r.events = append(r.events, re)
	r.updatedAt = nowUTC()
	if r.store != nil {
		var sum *RunSummary
		if ev.Type == "done" || ev.Type == "error" {
			s := r.summaryLocked()
			sum = &s
		}
		r.store.AppendEvent(r.ID, re, sum)
	}
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
	if r.store != nil && r.hydrated {
		// Unhydrated finalize only happens on eviction, where the rows are
		// deleted right after — persisting a wrong-seq sentinel would be noise.
		sum := r.summaryLocked()
		r.store.AppendEvent(r.ID, re, &sum)
	}
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
