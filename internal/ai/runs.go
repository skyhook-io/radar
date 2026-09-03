package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	d           *Diagnoser
	diagnose    func(context.Context, Request, func(StreamEvent)) (Diagnosis, error)
	mcpPort     func() int    // resolved lazily — the listener port isn't known at construction
	mcpBasePath string        // --base-path prefix the MCP mounts sit under ("" at the root)
	ctxLabel    func() string // current kube-context label, for the run's baseline

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
	ID        string           // immutable
	Kind      string           // immutable
	Group     string           // immutable — Kubernetes API group (empty = core)
	Namespace string           // immutable
	Name      string           // immutable
	Issue     *RunIssue        // immutable — issue the run was initiated from ("" = plain investigate)
	Context   string           // immutable — kube-context the run is about (baseline)
	Agent     string           // immutable — backend CLI driving this run ("claude"/"codex")
	WorkDir   string           // immutable — per-run scratch dir (under RunManager.workRoot); "" if none
	Profile   ExecutionProfile // immutable — execution profile chosen at Start
	Model     string           // immutable — optional model override ("" = agent default)
	Effort    string           // immutable — optional reasoning effort (Codex; "" = default)
	ManagedBy string           // immutable — GitOps/Helm owner of the target ("" = none), for the Apply warning
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
// RunIssue is the issue the user initiated this investigation from — display
// identity only. It never shapes the prompt: two runs on the same target read
// the same evidence regardless of which symptom row launched them.
type RunIssue struct {
	Reason   string `json:"reason"`
	Severity string `json:"severity,omitempty"`
}

type RunSummary struct {
	ID        string                `json:"id"`
	Kind      string                `json:"kind"`
	Group     string                `json:"group"`
	Namespace string                `json:"namespace"`
	Name      string                `json:"name"`
	Issue     *RunIssue             `json:"issue,omitempty"`
	Context   string                `json:"context"`
	Agent     string                `json:"agent,omitempty"`
	Profile   ExecutionProfile      `json:"profile"`
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
	// ErrHistoryCorrupt is returned when the persisted transcript is structurally
	// invalid. Unlike ErrHistoryUnavailable, retrying cannot repair it; the user
	// must clear the retained history or start a new investigation.
	ErrHistoryCorrupt = errors.New("this investigation's retained history is corrupt and cannot be replayed")
	// ErrInvalidTurn is returned when mutually exclusive turn modes are combined.
	ErrInvalidTurn = errors.New("apply and verify cannot be requested together")
	// ErrVerificationQuestionRequired rejects a manual verification that carries
	// no instruction. Automatic post-apply verification uses the server-owned
	// prompt and does not enter through AddTurn.
	ErrVerificationQuestionRequired = errors.New("verification requires a question")
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

	// automaticVerificationPrompt is owned by the server so every successful
	// apply gets the same immediate read-only check even if the initiating browser
	// disconnects. It deliberately asks for current evidence, not an optimistic
	// confirmation that the write worked.
	automaticVerificationPrompt = "Did the fix resolve the issue? Re-check the resource's current status and health now, and say whether it's healthy."

	// An interrupted or structurally ambiguous apply must be checked without
	// presuming either success or failure. This still runs through the canonical
	// read-only session, never the short-lived write-enabled session.
	automaticUncertainVerificationPrompt = "An apply attempt ended with an uncertain outcome. Re-check the resource's current status and health now, determine whether the requested change took effect, and say what the current evidence proves."
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
func NewRunManager(d *Diagnoser, mcpPort func() int, mcpBasePath string, ctxLabel func() string, store RunStore) *RunManager {
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
		mcpBasePath:   mcpBasePath,
		ctxLabel:      ctxLabel,
		baseCtx:       ctx,
		baseCancel:    cancel,
		workRoot:      root,
		store:         store,
		runs:          map[string]*Run{},
		maxRetained:   defaultMaxRetained,
		maxConcurrent: defaultMaxConcurrent,
	}
	if d != nil {
		m.diagnose = d.DiagnoseStream
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
			ID: s.ID, Kind: s.Kind, Group: s.Group, Namespace: s.Namespace, Name: s.Name,
			Issue: s.Issue, Context: s.Context, Agent: s.Agent, Profile: s.Profile,
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
			events, loadErr := m.store.LoadEvents(r.ID)
			terminal := restartInterruptionEvent(events, loadErr)
			r.status = "error"
			r.updatedAt = nowUTC()
			sum := r.summaryLocked()
			m.store.AppendEvent(r.ID, RunEvent{Event: terminal}, &sum)
		}
		if r.status == "stale" {
			r.subs = nil // stale runs were finalized — streams replay then close
		}
		m.runs[r.ID] = r
		m.order = append(m.order, r.ID)
	}
	m.evictLocked() // the retention cap may have shrunk since the DB was written
}

func restartInterruptionEvent(events []RunEvent, loadErr error) StreamEvent {
	if loadErr != nil {
		// Without the transcript we cannot prove that the active turn was
		// read-only. Preserve the worst credible state: an apply may have reached
		// the API before Radar restarted, and retrying it blindly could duplicate
		// or overwrite work.
		return StreamEvent{
			Type:         "error",
			ApplyOutcome: ApplyMutationUnknown,
			Error:        "Radar restarted while this investigation was running, and its active transcript could not be reloaded. An apply may have completed without verification; re-check the original cluster before retrying.",
		}
	}
	if lastOpenTurnWasApply(events) {
		return StreamEvent{
			Type:         "error",
			ApplyOutcome: ApplyMutationUnknown,
			Error:        "Radar restarted during an apply. The change may have completed, but Radar could not verify it; re-check the original cluster before retrying.",
		}
	}
	return StreamEvent{
		Type:  "error",
		Error: "Radar restarted while this investigation was running. Start a new investigation to analyze the current cluster.",
	}
}

func lastOpenTurnWasApply(events []RunEvent) bool {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Event.Type {
		case "turn":
			return events[i].Event.Apply
		case "done", "error":
			// A terminal event after the nearest turn means there is no durable
			// evidence that the later running transition reached an agent.
			return false
		}
	}
	return false
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

func newEvidenceScope() string {
	return strings.ToLower(rand.Text())
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
		// A context switch may already have finalized an in-flight run while its
		// cancelled agent is still unwinding. Never write past that run's durable
		// closed sentinel during shutdown.
		shouldStop := inFlight && r.status == "running" && r.subs != nil
		if shouldStop {
			r.status = "stopped"
			r.updatedAt = nowUTC()
			// Keep the status transition and terminal event enqueue in the same
			// critical section. A later turn can only reserve the run after the old
			// terminal marker is durably ordered in the store queue.
			terminal := StreamEvent{Type: "error", Error: "Investigation stopped — Radar was shutting down."}
			if r.activeTurnIsApplyLocked() {
				terminal.ApplyOutcome = ApplyMutationUnknown
				terminal.Error = "Radar shut down during an apply. The change may have completed, but Radar could not verify it; re-check current cluster state before retrying."
			}
			r.appendLocked(terminal)
		}
		c := r.cancel
		r.mu.Unlock()
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
	// AddTurn resolves a run before entering this manager critical section.
	// ClearHistory/eviction may remove it in that gap; never reserve an agent on
	// an object that is no longer addressable through the manager.
	if current, ok := m.runs[r.ID]; !ok || current != r {
		return "", ErrRunNotFound
	}
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
func (m *RunManager) Start(kind, group, namespace, name, agent string, profile ExecutionProfile, model, effort, managedBy string, health *ResourceHealthSignal, issue *RunIssue) (RunSummary, error) {
	cur := m.ctx()
	m.mu.Lock()
	// Focus an existing live run for this exact target+mode rather than duplicate it.
	for _, id := range m.order {
		r := m.runs[id]
		if r.matchesTarget(kind, group, namespace, name, cur, agent, profile, model, effort) &&
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
		ID: id, Kind: kind, Group: group, Namespace: namespace,
		Name: name, Issue: issue, Context: cur, Agent: agent, WorkDir: m.runWorkDir(id), Profile: profile,
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

	m.launchTurn(r, "", false, "", "", false)
	return r.Summary(), nil
}

// AddTurn runs a follow-up, manual verification, or apply turn (with the
// confirmed fix). beginTurn atomically enforces the cap + preconditions and
// marks the run in-flight.
func (m *RunManager) AddTurn(id, question string, apply bool, fix string, verify bool) error {
	if apply && verify {
		return ErrInvalidTurn
	}
	if verify && strings.TrimSpace(question) == "" {
		return ErrVerificationQuestionRequired
	}
	r := m.get(id)
	if r == nil {
		return ErrRunNotFound
	}
	if !SupportsProfile(r.Agent, r.Profile) {
		return fmt.Errorf("ai: run uses unsupported execution profile %q", r.Profile)
	}
	// A follow-up on a run loaded from history must extend the PERSISTED log —
	// hydrate before beginTurn so the new turn's sequence numbers continue it.
	// Refusing on failure protects the stored transcript: appending against an
	// unknown prefix would re-sequence from 1 and overwrite it.
	if err := r.ensureHydrated(); err != nil {
		return err
	}
	session, err := m.beginTurn(r, true)
	if err != nil {
		return err
	}
	m.launchTurn(r, question, apply, fix, session, verify)
	return nil
}

// runTurn is one step in the private execution state machine. An accepted apply
// owns a single in-flight reservation across TWO steps: the write, then its
// automatic read-only verification. The verification resumes canonicalSession,
// which is the read-only session captured before apply; it never adopts the
// fresh write-enabled session.
type runTurn struct {
	question         string
	apply            bool
	fix              string
	verify           bool
	evidenceScope    string
	canonicalSession string
	ctx              context.Context
	cancel           context.CancelFunc
	timeout          time.Duration
}

// applyMutationTracker derives mutation truth from Radar write-tool results.
// Agent prose and process exit are deliberately excluded: a zero exit cannot
// turn a failed/missing tool result into a confirmed Kubernetes mutation.
//
// Steps are correlated by ID because Claude's terminal tool-result row omits the
// tool name. An incomplete/unknown terminal result remains unknown; a mixture of
// successes and failures is also unknown because the requested change may be
// only partially applied.
type applyMutationTracker struct {
	steps map[string]applyMutationStep
	// Anonymous write steps are not expected from supported agents, but treating
	// them explicitly keeps a missing correlation ID honest instead of silently
	// converting it to "no write attempted".
	anonymousStarted bool
}

type applyMutationStep struct {
	tool      string
	summary   string
	result    string
	started   bool
	done      bool
	isError   *bool
	truncated bool
}

func (t *applyMutationTracker) observe(ev StreamEvent) {
	step := ev.Step
	if ev.Type != "step" || step == nil {
		return
	}
	state, tracked := t.steps[step.ID]
	if !tracked && !isRadarWriteTool(step.Tool) {
		return
	}
	if step.ID == "" {
		// Without a correlation id, a later terminal row cannot be tied to one
		// particular write call. Even a producer-shaped success is therefore not
		// enough to claim that the complete requested mutation landed.
		t.anonymousStarted = true
		return
	}
	if t.steps == nil {
		t.steps = make(map[string]applyMutationStep)
	}
	switch step.Status {
	case "running":
		state.started = true
		state.tool = normalizeRadarToolName(step.Tool)
		state.summary = step.Summary
	case "done":
		state.started = true
		state.done = true
		state.isError = step.IsError
		state.result = step.Result
		state.truncated = step.Truncated
		if step.Tool != "" {
			state.tool = normalizeRadarToolName(step.Tool)
		}
		if step.Summary != "" {
			state.summary = step.Summary
		}
	}
	t.steps[step.ID] = state
}

type mutationStepEvidence uint8

const (
	mutationEvidenceUnknown mutationStepEvidence = iota
	mutationEvidenceNone
	mutationEvidenceConfirmed
)

// evidence interprets the write producer's result contract. The agent host's
// `is_error=false` only says that the MCP call completed; it does not say that a
// real mutation occurred (dry-run and no-op calls also complete successfully),
// nor that a multi-part operation completed in full.
func (s applyMutationStep) evidence() mutationStepEvidence {
	if !s.done || s.isError == nil || *s.isError || s.truncated {
		// A tool error can follow a partial write (node drain, Rollout status+spec,
		// Flux source+target), so the host error bit cannot prove "nothing landed".
		return mutationEvidenceUnknown
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s.result)), &result); err != nil {
		return mutationEvidenceUnknown
	}
	status, _ := result["status"].(string)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "partial", "partial_failure":
		return mutationEvidenceUnknown
	case "ok":
		// Continue below: the producer accepted the operation, but explicit
		// non-mutating modes still override that transport-level success.
	default:
		return mutationEvidenceUnknown
	}

	if resultIsDryRun(result) || boolResultField(result, "noChange") {
		return mutationEvidenceNone
	}

	switch s.tool {
	case "manage_gitops":
		// Unlike apply_resource and patch_resource, manage_gitops does not echo
		// dry_run in its result. Its current input contract is authoritative, so
		// an absent/unparseable running payload cannot safely become confirmed.
		var input struct {
			Action string `json:"action"`
			Tool   string `json:"tool"`
			DryRun *bool  `json:"dry_run"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(s.summary)), &input); err != nil {
			return mutationEvidenceUnknown
		}
		if strings.TrimSpace(input.Action) == "" || strings.TrimSpace(input.Tool) == "" {
			return mutationEvidenceUnknown
		}
		if input.DryRun != nil && *input.DryRun {
			return mutationEvidenceNone
		}
	case "apply_resource":
		// A multi-document dry run carries dry_run on each resource rather than
		// on the top-level envelope. The running args are the simplest complete
		// signal, while the result field above covers a single document.
		if inputDryRun(s.summary) {
			return mutationEvidenceNone
		}
	}

	return mutationEvidenceConfirmed
}

func boolResultField(result map[string]any, field string) bool {
	value, _ := result[field].(bool)
	return value
}

func resultIsDryRun(result map[string]any) bool {
	if boolResultField(result, "dry_run") {
		return true
	}
	resources, _ := result["resources"].([]any)
	for _, resource := range resources {
		entry, _ := resource.(map[string]any)
		if boolResultField(entry, "dry_run") {
			return true
		}
	}
	return false
}

func inputDryRun(summary string) bool {
	var input struct {
		DryRun bool `json:"dry_run"`
	}
	return json.Unmarshal([]byte(strings.TrimSpace(summary)), &input) == nil && input.DryRun
}

func (t *applyMutationTracker) outcome(profile ExecutionProfile) ApplyMutationOutcome {
	if profile == ExecutionProfileFullLocal {
		// Full-local agents may use user-configured MCP servers whose bare tool
		// names collide with Radar's write tools. Until the write transport carries
		// the same exact-payload provenance as read-only investigations, no observed
		// full-local call can authoritatively confirm the mutation. Verification is
		// still scheduled because an unobserved write may have landed.
		return ApplyMutationUnknown
	}
	confirmed := 0
	unknown := t.anonymousStarted
	for _, step := range t.steps {
		switch step.evidence() {
		case mutationEvidenceUnknown:
			unknown = true
		case mutationEvidenceNone:
			// A preview/no-op does not weaken a separate, producer-confirmed
			// mutation. It matters only when no mutation was confirmed at all.
		case mutationEvidenceConfirmed:
			confirmed++
		}
	}
	if unknown {
		return ApplyMutationUnknown
	}
	if confirmed > 0 {
		return ApplyMutationConfirmed
	}
	// No write call, or only producer-confirmed non-mutating calls (dry run / no
	// change): no mutation could have landed through the safeguarded profile's
	// only mutation surface. Tool failures stay unknown above because several
	// producers can fail after partially mutating.
	return ApplyMutationFailed
}

func isRadarWriteTool(tool string) bool {
	tool = normalizeRadarToolName(tool)
	for _, candidate := range radarWriteTools {
		if tool == candidate {
			return true
		}
	}
	return false
}

func isRadarReadTool(tool string) bool {
	tool = normalizeRadarToolName(tool)
	for _, candidate := range radarReadTools {
		if tool == candidate {
			return true
		}
	}
	return false
}

func normalizeRadarToolName(tool string) string {
	tool = strings.TrimPrefix(tool, "mcp__radar__")
	return strings.TrimPrefix(tool, "radar.")
}

// launchTurn emits a turn marker then runs the agent in a manager-owned goroutine.
// The caller has already marked the run in-flight (atomically with the cap check).
// Subscribers stay attached across turns — only stale / evict closes them (a
// stopped run can still take follow-up turns, so Stop leaves streams open).
func (m *RunManager) launchTurn(r *Run, question string, apply bool, fix, session string, verify bool) {
	// Wall-clock ceiling per turn: a wedged CLI would otherwise hold one of the
	// maxConcurrent slots forever (maxTurns caps model turns, not real time).
	timeout := turnTimeout()
	ctx, cancel := context.WithTimeout(m.baseCtx, timeout)
	r.mu.Lock()
	// beginTurn reserves the slot before this method constructs the turn context.
	// Stop/context-switch can race that small hand-off; if either already won,
	// abort without writing a turn after its terminal (or closed) marker.
	if !r.inFlight || r.status != "running" || r.subs == nil {
		r.inFlight = false // no agent was launched, so the reservation is released
		r.mu.Unlock()
		cancel()
		return
	}
	r.cancel = cancel
	r.appendLocked(StreamEvent{Type: "turn", Question: question, Apply: apply, Verify: verify})
	r.mu.Unlock()

	go m.executeTurns(r, runTurn{
		question: question, apply: apply, fix: fix, verify: verify,
		evidenceScope:    newEvidenceScope(),
		canonicalSession: session,
		ctx:              ctx, cancel: cancel, timeout: timeout,
	})
}

// executeTurns runs one ordinary turn, or the two-step apply→verify compound
// job. The continuation stays inside this goroutine and never re-enters public
// AddTurn, so it neither releases nor re-reserves the concurrency slot.
func (m *RunManager) executeTurns(r *Run, turn runTurn) {
	diagnose := m.diagnose
	if diagnose == nil && m.d != nil {
		// Keep manually-constructed managers (primarily focused tests) compatible
		// with the production constructor, which installs this method value.
		diagnose = m.d.DiagnoseStream
	}
	if diagnose == nil {
		if turn.apply {
			r.finishApplyWithoutVerification(applyTerminalEvent(
				Diagnosis{}, ErrNoCLI, ApplyMutationFailed, turn.timeout, false,
			))
		} else {
			r.finishTurn(Diagnosis{}, ErrNoCLI, false, turn.timeout)
		}
		turn.cancel()
		return
	}
	for {
		var mutation applyMutationTracker
		diag, err := diagnose(turn.ctx, Request{
			Kind: r.Kind, Group: r.Group, Namespace: r.Namespace, Name: r.Name,
			MCPPort: m.mcpPort(), MCPBasePath: m.mcpBasePath,
			EvidenceScope: turn.evidenceScope, SessionID: turn.canonicalSession,
			Question: turn.question, Apply: turn.apply, Fix: turn.fix, Verify: turn.verify,
			Agent: r.Agent, Profile: r.Profile, Model: r.Model, Effort: r.Effort,
			Health: r.Health, WorkDir: r.WorkDir,
		}, func(ev StreamEvent) {
			if turn.apply {
				mutation.observe(ev)
			}
			// The agent can keep streaming briefly after Stop/context-switch
			// cancel it (process-group kill has a WaitDelay). Those events must
			// not land after the terminal marker — replay ordering is the
			// contract every subscriber rebuilds from.
			r.appendStreamEvent(ev)
		})

		if turn.apply && !turn.verify {
			outcome := mutation.outcome(r.Profile)
			// A confirmed mutation and an ambiguous attempted mutation both need
			// current-state evidence. Authoritative failure/no-attempt does not: no
			// write could have landed through Radar.
			needsVerification := outcome != ApplyMutationFailed
			sameContext := m.ctx() == r.Context
			if needsVerification && sameContext {
				verifyQuestion := automaticVerificationPrompt
				if outcome == ApplyMutationUnknown {
					verifyQuestion = automaticUncertainVerificationPrompt
				}
				verifyTimeout := turnTimeout()
				verifyCtx, verifyCancel := context.WithTimeout(m.baseCtx, verifyTimeout)
				terminal := applyTerminalEvent(diag, err, outcome, turn.timeout, true)
				continued := r.finishApplyAndBeginVerification(terminal, verifyQuestion, verifyCancel)
				turn.cancel()
				if !continued {
					verifyCancel()
					return
				}
				turn = runTurn{
					question:         verifyQuestion,
					verify:           true,
					evidenceScope:    newEvidenceScope(),
					canonicalSession: turn.canonicalSession,
					ctx:              verifyCtx,
					cancel:           verifyCancel,
					timeout:          verifyTimeout,
				}
				continue
			}

			// A context change makes even read-only verification unsafe: it would
			// inspect a different cluster. Persist the outcome on the apply terminal
			// event so replay never loses the uncertainty.
			terminal := applyTerminalEvent(diag, err, outcome, turn.timeout, false)
			if needsVerification && !sameContext {
				terminal.Type = "error"
				terminal.Diag = nil
				if outcome == ApplyMutationConfirmed {
					terminal.Error = "A Radar write tool confirmed the mutation, but the cluster context changed before Radar could verify current state. Reconnect to the original cluster and check it before retrying."
				} else {
					terminal.Error = "The cluster context changed before Radar could determine whether the apply completed. Reconnect to the original cluster and check current state before retrying."
				}
			}
			r.finishApplyWithoutVerification(terminal)
			turn.cancel()
			return
		}

		r.finishTurn(diag, err, turn.apply, turn.timeout)
		turn.cancel()
		return
	}
}

// applyTerminalEvent turns authoritative mutation evidence into the terminal
// event for the apply turn. Only confirmed write-tool success gets a normal done
// event; failed or unknown outcomes are never rendered as a successful apply.
func applyTerminalEvent(diag Diagnosis, turnErr error, outcome ApplyMutationOutcome, timeout time.Duration, verificationScheduled bool) StreamEvent {
	event := StreamEvent{Type: "error", ApplyOutcome: outcome, VerificationScheduled: verificationScheduled}
	if outcome == ApplyMutationConfirmed && turnErr == nil {
		event.Type = "done"
		event.Diag = &diag
		return event
	}

	verificationSuffix := ""
	if verificationScheduled {
		verificationSuffix = " Radar scheduled a current-state verification."
	}
	switch outcome {
	case ApplyMutationFailed:
		if turnErr != nil {
			event.Error = turnErr.Error()
			if errors.Is(turnErr, context.DeadlineExceeded) {
				event.Error = fmt.Sprintf("The apply timed out after %s before any Radar write was confirmed.", timeout)
			}
		} else {
			event.Error = "No change was applied: no Radar write tool reported a successful mutation. Review the failed or missing write call before retrying."
		}
	case ApplyMutationConfirmed:
		event.Error = "A Radar write tool confirmed the mutation, but the agent ended before completing its report."
		if turnErr != nil && !errors.Is(turnErr, context.DeadlineExceeded) {
			event.Error += " " + turnErr.Error()
		}
		event.Error += verificationSuffix
	case ApplyMutationUnknown:
		event.Error = "The apply attempt ended without an authoritative result, so the change may have completed."
		if turnErr != nil && !errors.Is(turnErr, context.DeadlineExceeded) {
			event.Error += " " + turnErr.Error()
		}
		event.Error += verificationSuffix
	}
	return event
}

// finishApplyAndBeginVerification records the apply outcome and its automatic
// verification marker as one per-run serialization step. status and
// inFlight remain running throughout, so a competing AddTurn cannot enter
// between the two durable events. The apply session is intentionally ignored;
// executeTurns resumes the canonical pre-apply read-only session.
func (r *Run) finishApplyAndBeginVerification(terminal StreamEvent, verifyQuestion string, verifyCancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != "running" || !r.inFlight || r.subs == nil {
		r.inFlight = false
		return false
	}
	if terminal.Diag != nil {
		if terminal.Diag.RootCause != "" {
			r.preview = terminal.Diag.RootCause
		} else if terminal.Diag.Healthy {
			r.preview = "Healthy"
		}
	}
	r.updatedAt = nowUTC()
	r.cancel = verifyCancel
	// Keep status=running before appending the batch: the done event and the
	// summary it carries must describe an interrupted compound job as running.
	// Restart recovery then appends its honest interruption error if Radar exits
	// before verification completes.
	r.appendEventsLocked([]StreamEvent{
		terminal,
		{Type: "turn", Question: verifyQuestion, Verify: true},
	}, true)
	return true
}

// finishApplyWithoutVerification closes an apply attempt whose mutation failed,
// or whose current-state verification cannot safely run. The explicit outcome
// remains durable on the terminal event for replay and future UI treatment.
func (r *Run) finishApplyWithoutVerification(terminal StreamEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != "running" || !r.inFlight || r.subs == nil {
		r.inFlight = false
		return
	}
	r.status = "error"
	r.updatedAt = nowUTC()
	r.appendLocked(terminal)
	r.inFlight = false
}

// finishTurn commits one agent turn's terminal state and terminal event as one
// per-run serialization step. In particular, inFlight remains true until the
// store has accepted the terminal AppendEvent, so beginTurn cannot make a new
// turn durable ahead of the old turn's terminator.
func (r *Run) finishTurn(diag Diagnosis, turnErr error, apply bool, timeout time.Duration) {
	r.finishTurnWithBarrier(diag, turnErr, apply, timeout, nil)
}

// finishTurnWithBarrier exposes the instant after terminal state is prepared but
// before its event is appended. Production passes nil; tests use the barrier to
// deterministically queue a competing turn while this method still owns r.mu.
func (r *Run) finishTurnWithBarrier(diag Diagnosis, turnErr error, apply bool, timeout time.Duration, beforeTerminalAppend func()) {
	msg := ""
	if turnErr != nil {
		msg = turnErr.Error()
		if errors.Is(turnErr, context.DeadlineExceeded) {
			msg = fmt.Sprintf("The investigation timed out after %s and was stopped. Start a new investigation, or ask a narrower follow-up.", timeout)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatedAt = nowUTC()
	// Stop/context-switch owns the terminal marker when it won the race. It is
	// already enqueued under this same mutex, so this goroutine only releases the
	// in-flight reservation and never writes after it (especially after closed).
	if r.status == "stopped" || r.status == "stale" || r.subs == nil {
		r.inFlight = false
		return
	}
	if turnErr != nil {
		r.status = "error"
		if beforeTerminalAppend != nil {
			beforeTerminalAppend()
		}
		r.appendLocked(StreamEvent{Type: "error", Error: msg})
		r.inFlight = false
		return
	}
	// Keep the read-only investigation session as the canonical resume target.
	// An apply turn runs in its OWN fresh, write-enabled session (injection
	// hardening) — adopting it would make follow-ups resume the write transcript
	// and collapse the read/write context separation.
	if diag.SessionID != "" && !apply {
		r.sessionID = diag.SessionID
	}
	if !apply {
		r.bindRootCauseEvidenceLocked(&diag)
	}
	if diag.RootCause != "" {
		r.preview = diag.RootCause
	} else if diag.Healthy {
		r.preview = "Healthy"
	}
	r.status = "done"
	if beforeTerminalAppend != nil {
		beforeTerminalAppend()
	}
	r.appendLocked(StreamEvent{Type: "done", Diag: &diag})
	r.inFlight = false
}

// bindRootCauseEvidenceLocked promotes the model's private reference request
// only when every ref maps to exactly one complete, confirmed-success result in
// the current turn AND to the exact clean producer payload Radar's private MCP
// transport recorded while that turn's scope was active. The model-visible ref
// is correlation data, not authority. The method scans canonical retained events
// while r.mu is held, so a callback rejected after Stop/context-switch can never
// become proof. Radar's read-tool allowlist is retained as defense in depth.
func (r *Run) bindRootCauseEvidenceLocked(diag *Diagnosis) {
	// These fields exist only long enough to authorize this binding. Clear them
	// on every branch so the terminal in-memory event does not retain duplicate
	// producer payloads or untrusted model requests after public provenance exists.
	defer func() {
		diag.evidenceRequest = evidenceReferenceRequest{}
		diag.evidenceScope = ""
		diag.issuedEvidence = nil
	}()
	if diag.RootCause == "" {
		diag.RootCauseEvidence = nil
		return
	}
	request := diag.evidenceRequest
	if request.invalid {
		diag.RootCauseEvidence = &RootCauseEvidence{Status: EvidenceInvalid}
		return
	}
	if !request.present || len(request.refs) == 0 {
		diag.RootCauseEvidence = &RootCauseEvidence{Status: EvidenceMissing}
		return
	}
	if !evidenceScopeRe.MatchString(diag.evidenceScope) {
		diag.RootCauseEvidence = &RootCauseEvidence{Status: EvidenceInvalid}
		return
	}

	turnStart := len(r.events)
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Event.Type == "turn" {
			turnStart = i + 1
			break
		}
	}
	// Claude omits the tool name from terminal result rows, so establish one
	// unambiguous tool identity per host call ID across the current turn before
	// evaluating marker-bearing results. Codex/Cursor repeat the name on done;
	// accepting that exact terminal identity also keeps a dropped running event
	// from needlessly invalidating otherwise complete evidence.
	type toolIdentity struct {
		name      string
		known     bool
		conflicts bool
	}
	toolsByStepID := make(map[string]toolIdentity)
	for _, retained := range r.events[turnStart:] {
		step := retained.Event.Step
		if retained.Event.Type != "step" || step == nil || step.ID == "" || step.Tool == "" {
			continue
		}
		tool := normalizeRadarToolName(step.Tool)
		identity := toolsByStepID[step.ID]
		if !identity.known {
			identity.name = tool
			identity.known = true
		} else if identity.name != tool {
			identity.conflicts = true
		}
		toolsByStepID[step.ID] = identity
	}

	type match struct {
		count int
		valid bool
	}
	matches := make(map[string]match, len(request.refs))
	for _, retained := range r.events[turnStart:] {
		step := retained.Event.Step
		if retained.Event.Type != "step" || step == nil || step.EvidenceRef == "" {
			continue
		}
		candidate := matches[step.EvidenceRef]
		candidate.count++
		tool := toolsByStepID[step.ID]
		issuedPayload, issued := diag.issuedEvidence[step.EvidenceRef]
		candidate.valid = candidate.count == 1 &&
			issued && step.Result == issuedPayload &&
			step.RadarEvidence &&
			step.ID != "" && tool.known && !tool.conflicts && isRadarReadTool(tool.name) &&
			step.Status == "done" &&
			step.IsError != nil && !*step.IsError &&
			!step.Truncated && strings.TrimSpace(step.Result) != ""
		matches[step.EvidenceRef] = candidate
	}

	scopePrefix := "ev_" + diag.evidenceScope + "_"
	for _, ref := range request.refs {
		candidate := matches[ref]
		if !strings.HasPrefix(ref, scopePrefix) || candidate.count != 1 || !candidate.valid {
			diag.RootCauseEvidence = &RootCauseEvidence{Status: EvidenceInvalid}
			return
		}
	}
	diag.RootCauseEvidence = &RootCauseEvidence{
		Status: EvidenceLinked,
		Refs:   append([]string(nil), request.refs...),
	}
}

// Stop cancels a run's in-flight agent (killing its process group) and marks it stopped.
func (m *RunManager) Stop(id string) error {
	return m.stopWithBarrier(id, nil)
}

// activeTurnIsApplyLocked reports whether the currently reserved agent turn is
// the write-enabled apply step (not its subsequent read-only verification).
// Caller holds r.mu.
func (r *Run) activeTurnIsApplyLocked() bool {
	if !r.inFlight {
		return false
	}
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Event.Type == "turn" {
			return r.events[i].Event.Apply
		}
	}
	return false
}

// stopWithBarrier is Stop with a deterministic test seam immediately before
// the stopped marker append, while r.mu is still held.
func (m *RunManager) stopWithBarrier(id string, beforeTerminalAppend func()) error {
	r := m.get(id)
	if r == nil {
		return ErrRunNotFound
	}
	// An in-flight run is always hydrated (born live); a loaded run can't be
	// in-flight, so its early return below makes a failed hydration harmless.
	_ = r.ensureHydrated()
	r.mu.Lock()
	if !r.inFlight || r.status != "running" || r.subs == nil {
		r.mu.Unlock()
		return nil // nothing to stop
	}
	r.status = "stopped"
	c := r.cancel
	// Serialize the status + terminal marker with completion and beginTurn. The
	// agent keeps the in-flight reservation until it observes cancellation, so
	// no follow-up can be inserted ahead of this marker either.
	if beforeTerminalAppend != nil {
		beforeTerminalAppend()
	}
	terminal := StreamEvent{Type: "error", Error: "Investigation stopped."}
	if r.activeTurnIsApplyLocked() {
		terminal.ApplyOutcome = ApplyMutationUnknown
		terminal.Error = "Investigation stopped during an apply. The change may have completed; re-check current cluster state before retrying."
	}
	r.appendLocked(terminal)
	r.mu.Unlock()
	if c != nil {
		c() // the run goroutine sees status=stopped and won't overwrite it
	}
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
		if r.markStale() {
			r.removeWorkDir() // stale runs can't resume — their workspace is dead weight
		}
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
// (running) runs survive, including live runs owned by another process sharing
// the history DB that are intentionally absent from this manager's memory.
func (m *RunManager) ClearHistory() error {
	// Fence the manager from selection through the store transaction. Otherwise a
	// Start after the keep-set snapshot can have its just-created live row deleted,
	// while an AddTurn holding an earlier pointer can revive a dropped run. Hold
	// every dropped run lock too: a context-switch snapshot taken before this clear
	// must not append stale markers after the delete and resurrect cleared history.
	m.mu.Lock()
	origOrder := append([]string(nil), m.order...)
	kept := make([]string, 0, len(m.order))
	var dropped []*Run
	var droppedIDs []string
	for _, id := range m.order {
		r := m.runs[id]
		r.mu.Lock()
		if r.status == "running" {
			r.mu.Unlock()
			kept = append(kept, id)
			continue
		}
		// Keep r.mu held through Clear and detach/finalize below.
		dropped = append(dropped, r)
		droppedIDs = append(droppedIDs, id)
		delete(m.runs, id)
	}
	m.order = kept

	// The store derives its live set from the transaction snapshot rather than
	// this manager's necessarily-incomplete `kept` list. That preserves live rows
	// owned by another process while deleting terminal foreign history. On
	// FAILURE, restore the removed local runs — the UI must keep showing what the
	// DB still holds.
	if m.store != nil {
		if err := m.store.ClearTerminal(); err != nil {
			for i, r := range dropped {
				m.runs[droppedIDs[i]] = r
				r.mu.Unlock()
			}
			m.order = origOrder
			m.mu.Unlock()
			return err
		}
	}
	for _, r := range dropped {
		// Detach before finalizing: the rows were just deleted, and a persisted
		// closed sentinel would recreate them. A pre-clear context-switch snapshot
		// is still blocked on r.mu and will observe subs=nil after this unlock.
		r.store = nil
		r.finalizeLocked()
		r.mu.Unlock()
	}
	m.mu.Unlock()

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
		// The row is about to be deleted, so keep closed as an in-memory stream
		// sentinel only. Persisting closed and deleting in separate async writes
		// could resurrect a done run ending in closed after a crash/queue drop.
		victim.mu.Lock()
		victim.store = nil
		victim.finalizeLocked()
		victim.mu.Unlock()
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
		ID: r.ID, Kind: r.Kind, Group: r.Group, Namespace: r.Namespace, Name: r.Name,
		Issue: r.Issue, Context: r.Context, Agent: r.Agent, Profile: r.Profile,
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
// On a load FAILURE the run stays un-hydrated and callers must not append:
// sequencing against an unknown prefix would restart at seq 1 and overwrite the
// persisted transcript. The returned error distinguishes a retryable store read
// failure from a permanently corrupt transcript.
func (r *Run) ensureHydrated() error {
	r.mu.Lock()
	if r.hydrated {
		r.mu.Unlock()
		return nil
	}
	// ClearHistory/eviction detach the store under this same lock before
	// finalizing the run. Keep a stable interface snapshot for the potentially
	// blocking read so a concurrent detach cannot race this dereference.
	store := r.store
	r.mu.Unlock()
	if store == nil {
		return nil
	}
	events, err := store.LoadEvents(r.ID) // outside r.mu — a DB read may wait on the writer
	if err != nil {
		log.Printf("[ai] could not load transcript for %s: %v", r.ID, err)
		if errors.Is(err, errCorruptRunHistory) {
			return ErrHistoryCorrupt
		}
		return ErrHistoryUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hydrated {
		return nil
	}
	// A concurrent clear/eviction may have detached this run while the store read
	// was in flight. Never install data fetched through that obsolete handle: in
	// particular, ClearHistory must not let an already-started Subscribe replay
	// transcript data after the clear completed.
	if r.store == nil {
		return ErrHistoryUnavailable
	}
	// If the run was finalized while we were loading (a context switch marked it
	// stale and enqueued its terminal markers), the snapshot we hold predates
	// them. Installing it would freeze a short prefix forever — stay
	// un-hydrated so the next touch reloads through the writer barrier.
	if r.subs == nil && (len(events) == 0 || events[len(events)-1].Event.Type != "closed") {
		return ErrHistoryUnavailable
	}
	r.events = events
	r.hydrated = true
	return nil
}

// markStale flips a run to stale and finalizes its stream without requiring
// hydration. For a live/hydrated run, the terminal error + closed pair is
// appended under one run lock; a stream callback therefore lands either before
// the pair or is rejected after it, never after durable closed. For loaded lazy
// history the store assigns sequence numbers and the in-memory log stays lazy.
// Returns whether this call performed the transition.
func (r *Run) markStale() bool {
	r.mu.Lock()
	if r.status == "stale" || r.subs == nil {
		r.mu.Unlock()
		return false
	}
	c := r.cancel
	r.status = "stale"
	r.updatedAt = nowUTC()
	staleEv := StreamEvent{Type: "error", Error: "Cluster context changed — this investigation was about a different cluster."}
	if r.activeTurnIsApplyLocked() {
		staleEv.ApplyOutcome = ApplyMutationUnknown
		staleEv.Error = "Cluster context changed during an apply. The change may have completed on the previous cluster, but Radar cannot verify it here; reconnect to that cluster and check current state before retrying."
	}
	if r.hydrated {
		// The terminal pair and stale summary are one store transaction, while the
		// same ordered pair is published to in-memory subscribers before close.
		r.appendTerminalAndFinalizeLocked(staleEv)
	} else {
		// An unhydrated run has no subscribers (Subscribe hydrates before
		// registering), but close any defensively and let the store continue the
		// unknown persisted prefix with store-assigned sequence numbers.
		sum := r.summaryLocked()
		if r.store != nil {
			r.store.AppendEvents(r.ID, []RunEvent{
				{Event: staleEv},
				{Event: StreamEvent{Type: "closed"}},
			}, &sum)
		}
		for id, ch := range r.subs {
			delete(r.subs, id)
			close(ch)
		}
		r.subs = nil
	}
	r.mu.Unlock()
	// Publish terminal markers before cancellation. Any trailing process output
	// now re-enters through appendStreamEvent, observes stale/closed, and drops.
	if c != nil {
		c()
	}
	return true
}

// matchesTarget reports whether r is the same investigation as a Start request —
// same resource + cluster AND same agent/execution profile. The profile is part of the
// key so starting safeguarded Codex never silently focuses a full-local run
// run for the same resource. Immutable fields, so no lock needed.
func (r *Run) matchesTarget(kind, group, namespace, name, ctx, agent string, profile ExecutionProfile, model, effort string) bool {
	return r.Kind == kind && r.Group == group && r.Namespace == namespace && r.Name == name &&
		r.Context == ctx && r.Agent == agent && r.Profile == profile &&
		r.Model == model && r.Effort == effort
}

func (r *Run) snapshotStatus() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Subscribe returns the backlog after afterSeq plus a channel of future events.
// alreadyFinalized is an atomic snapshot of whether this subscription was born
// after the run finalized. A live subscription's channel can later close either
// after receiving the durable closed event or because the subscriber fell behind;
// transports must reconnect on a bare close in both cases. A turn completing does
// not close the channel, so one subscription sees later turns. A persisted
// transcript that cannot be hydrated returns
// ErrHistoryUnavailable for a retryable read failure or ErrHistoryCorrupt for
// permanent structural damage; callers must not mistake either for a successfully
// replayed empty transcript.
func (r *Run) Subscribe(afterSeq int) (backlog []RunEvent, ch <-chan RunEvent, alreadyFinalized bool, cancel func(), err error) {
	// A run loaded from history replays its persisted transcript. On a load
	// failure, return an error WITHOUT registering. The run stays un-hydrated,
	// so a later EventSource retry can attempt the load again.
	if err := r.ensureHydrated(); err != nil {
		return nil, nil, false, nil, err
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
		return backlog, c, true, func() {}, nil
	}
	id := r.nextSub
	r.nextSub++
	r.subs[id] = c
	return backlog, c, false, func() {
		r.mu.Lock()
		if ch, ok := r.subs[id]; ok {
			delete(r.subs, id)
			close(ch)
		}
		r.mu.Unlock()
	}, nil
}

// append records an event and fans it out non-blockingly. No event may extend a
// finalized log: closed is a durable end-of-stream sentinel, not merely an SSE
// notification. Event-producing run paths use the more specific locked helpers
// below when state transition + event order must be atomic.
func (r *Run) append(ev StreamEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subs == nil {
		return
	}
	r.appendLocked(ev)
}

// appendStreamEvent is the only ingress for agent callback events. The state
// check and append share r.mu, closing the former snapshotStatus→append TOCTOU:
// context-switch/finalize either follows this event or makes this event a no-op.
func (r *Run) appendStreamEvent(ev StreamEvent) bool {
	return r.appendStreamEventWithBarrier(ev, nil)
}

// appendStreamEventWithBarrier lets tests pause an already-admitted callback
// before append while it still owns r.mu, proving finalization cannot interleave.
func (r *Run) appendStreamEventWithBarrier(ev StreamEvent, afterAdmission func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != "running" || !r.inFlight || r.subs == nil {
		return false
	}
	if afterAdmission != nil {
		afterAdmission()
	}
	r.appendLocked(ev)
	return true
}

// appendLocked records and persists an event. Caller holds r.mu. Persistence
// rides along while the serialization boundary is held: store appends are
// ordered, non-blocking enqueues, and terminal events ("done"/"error") carry
// the run summary so status + marker commit in one transaction.
func (r *Run) appendLocked(ev StreamEvent) {
	r.appendEventsLocked([]StreamEvent{ev}, ev.Type == "done" || ev.Type == "error")
}

// appendEventsLocked records and publishes an ordered event batch. When
// persistSummary is true, the store commits the whole batch plus the current run
// summary in one transaction. Caller holds r.mu.
func (r *Run) appendEventsLocked(events []StreamEvent, persistSummary bool) {
	if len(events) == 0 {
		return
	}
	batch := make([]RunEvent, 0, len(events))
	for _, event := range events {
		re := RunEvent{Seq: len(r.events) + 1, Event: event}
		r.events = append(r.events, re)
		batch = append(batch, re)
	}
	r.updatedAt = nowUTC()
	if r.store != nil {
		var sum *RunSummary
		if persistSummary {
			s := r.summaryLocked()
			sum = &s
		}
		r.store.AppendEvents(r.ID, batch, sum)
	}
	for _, re := range batch {
		for id, ch := range r.subs {
			select {
			case ch <- re:
			default:
				delete(r.subs, id)
				close(ch)
			}
		}
	}
}

// finalize emits a terminal sentinel and closes all subscribers; further Subscribe
// calls replay the log then close. Used when a run can no longer produce turns.
// Idempotent: a context-switched (stale) run can later age past the retention cap
// and be finalized again by eviction — the second call must not append a second
// "closed" sentinel to the replay log.
func (r *Run) finalize() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finalizeLocked()
}

// finalizeLocked appends the durable closed sentinel and closes subscribers.
// Caller holds r.mu, allowing a state transition and closure to be one ordered
// step relative to callbacks and follow-up reservation.
func (r *Run) finalizeLocked() {
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

// appendTerminalAndFinalizeLocked appends a terminal event followed by closed,
// persists both plus the current summary in one store transaction, publishes
// them in order, and then closes every surviving subscriber. Caller holds r.mu.
func (r *Run) appendTerminalAndFinalizeLocked(terminal StreamEvent) {
	if r.subs == nil {
		return
	}
	r.appendEventsLocked([]StreamEvent{
		terminal,
		{Type: "closed"},
	}, true)
	for id, ch := range r.subs {
		delete(r.subs, id)
		close(ch)
	}
	r.subs = nil
}

func nowUTC() time.Time { return time.Now().UTC() }
