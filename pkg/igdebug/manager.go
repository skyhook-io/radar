package igdebug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrTargetChanged  = errors.New("target Pod or container changed before capture")
	ErrNotFound       = errors.New("live debug run not found")
	ErrForbidden      = errors.New("live debug run belongs to another user or cluster context")
	ErrCapacity       = errors.New("live debug concurrency limit reached")
	ErrInvalidRequest = errors.New("invalid live debug request")
)

type Limits struct {
	MaxConcurrent        int
	MaxConcurrentPerUser int
	MaxRetained          int
	MaxEvents            int
	MaxEventBytes        int
}

func DefaultLimits() Limits {
	return Limits{MaxConcurrent: 4, MaxConcurrentPerUser: 2, MaxRetained: 30, MaxEvents: 2_000, MaxEventBytes: 2 << 20}
}

type Executor interface {
	Execute(context.Context, Request, Reporter) error
}

type Reporter interface {
	Started(Target)
	Event(json.RawMessage)
	Result(Result)
	// Interrupted qualifies unfinished evidence; Partial also qualifies uncertain cleanup.
	Interrupted(string)
	Partial(string)
}

type managedRun struct {
	view          Run
	owner         Owner
	cancel        context.CancelFunc
	done          chan struct{}
	dataReady     chan struct{}
	dataReadyOnce sync.Once
	doneOnce      sync.Once
	stopRequested bool
	partialReason string
	failure       error
	discard       bool
	eventBytes    int
	truncated     bool
	subscribers   map[chan StreamEvent]struct{}
}

type Manager struct {
	mu      sync.Mutex
	limits  Limits
	runs    map[string]*managedRun
	order   []string
	active  int
	byOwner map[Owner]int
}

func NewManager(limits Limits) *Manager {
	return &Manager{limits: limits, runs: make(map[string]*managedRun), byOwner: make(map[Owner]int)}
}

func (m *Manager) Start(request Request, executor Executor) (*Run, error) {
	if executor == nil || !request.Aspect.Valid() || request.Target.Namespace == "" || request.Target.Pod == "" {
		return nil, ErrInvalidRequest
	}
	m.mu.Lock()
	if m.active >= m.limits.MaxConcurrent || m.byOwner[request.Owner] >= m.limits.MaxConcurrentPerUser {
		m.mu.Unlock()
		return nil, ErrCapacity
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC()
	run := &managedRun{
		view:  Run{ID: uuid.NewString(), Aspect: request.Aspect, Target: request.Target, State: StateStarting, StartedAt: now, UpdatedAt: now},
		owner: request.Owner, cancel: cancel, done: make(chan struct{}), dataReady: make(chan struct{}),
		subscribers: make(map[chan StreamEvent]struct{}),
	}
	m.runs[run.view.ID] = run
	m.order = append(m.order, run.view.ID)
	m.active++
	m.byOwner[request.Owner]++
	m.evictLocked()
	view := cloneRun(run.view)
	m.mu.Unlock()

	go m.execute(ctx, request, executor, run.view.ID)
	return &view, nil
}

func (m *Manager) execute(ctx context.Context, request Request, executor Executor, id string) {
	err := executor.Execute(ctx, request, reporter{manager: m, id: id})
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.view.State.Terminal() {
		return
	}
	switch {
	case run.partialReason != "":
		run.view.State = StatePartial
		run.view.Reason = run.partialReason
	case run.stopRequested:
		run.view.State = StateStopped
	case err != nil && !errors.Is(err, context.Canceled):
		run.view.State = StateFailed
		run.view.Error = err.Error()
		run.failure = err
	case err != nil:
		run.view.State = StateStopped
	case run.view.Result == nil:
		run.view.State = StateFailed
		run.failure = errors.New("runtime finished without producing capture evidence")
		run.view.Error = run.failure.Error()
	default:
		run.view.State = StateComplete
	}
	m.finishLocked(run)
}

func (m *Manager) Get(id string, owner Owner) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.authorizeLocked(id, owner)
	if err != nil {
		return nil, err
	}
	view := cloneRun(run.view)
	return &view, nil
}

func (m *Manager) WaitData(ctx context.Context, id string, owner Owner) (*Run, error) {
	m.mu.Lock()
	run, err := m.authorizeLocked(id, owner)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	dataReady, done := run.dataReady, run.done
	m.mu.Unlock()
	select {
	case <-dataReady:
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m.mu.Lock()
	run, err = m.authorizeLocked(id, owner)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	view := cloneRun(run.view)
	failure := run.failure
	m.mu.Unlock()
	if failure != nil && view.Result == nil {
		return nil, failure
	}
	return &view, nil
}

func (m *Manager) WaitDone(ctx context.Context, id string, owner Owner) (*Run, error) {
	m.mu.Lock()
	run, err := m.authorizeLocked(id, owner)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	done := run.done
	m.mu.Unlock()
	select {
	case <-done:
		return m.Get(id, owner)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *Manager) Stop(id string, owner Owner) (*Run, error) {
	m.mu.Lock()
	run, err := m.authorizeLocked(id, owner)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if !run.view.State.Terminal() {
		run.stopRequested = true
		run.view.State = StateStopping
		run.view.UpdatedAt = time.Now().UTC()
		run.cancel()
		m.broadcastLocked(run, StreamEvent{Type: "state", Run: runView(run)})
	}
	view := cloneRun(run.view)
	m.mu.Unlock()
	return &view, nil
}

func (m *Manager) Subscribe(id string, owner Owner) (<-chan StreamEvent, func(), error) {
	m.mu.Lock()
	run, err := m.authorizeLocked(id, owner)
	if err != nil {
		m.mu.Unlock()
		return nil, nil, err
	}
	ch := make(chan StreamEvent, len(run.view.Events)+8)
	ch <- StreamEvent{Type: "state", Run: runView(run)}
	for _, event := range run.view.Events {
		ch <- StreamEvent{Type: "data", Data: append(json.RawMessage(nil), event...)}
	}
	if run.view.State.Terminal() {
		close(ch)
	} else {
		run.subscribers[ch] = struct{}{}
	}
	m.mu.Unlock()
	unsubscribe := func() {
		m.mu.Lock()
		if _, ok := run.subscribers[ch]; ok {
			delete(run.subscribers, ch)
			close(ch)
		}
		m.mu.Unlock()
	}
	return ch, unsubscribe, nil
}

func (m *Manager) OnContextSwitch() {
	m.mu.Lock()
	done := make([]<-chan struct{}, 0, len(m.runs))
	for id, run := range m.runs {
		if run.view.State.Terminal() {
			delete(m.runs, id)
			m.removeOrderLocked(id)
			continue
		}
		run.partialReason = "cluster context changed during capture"
		run.discard = true
		run.view.State = StateStopping
		run.cancel()
		for ch := range run.subscribers {
			close(ch)
			delete(run.subscribers, ch)
		}
		done = append(done, run.done)
	}
	m.mu.Unlock()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for _, finished := range done {
		select {
		case <-finished:
		case <-deadline.C:
			return
		}
	}
}

func (m *Manager) reporterStarted(id string, target Target) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.runs[id]; run != nil && !run.view.State.Terminal() {
		run.view.Target = target
		if run.view.State == StateStarting {
			run.view.State = StateRunning
		}
		run.view.UpdatedAt = time.Now().UTC()
		m.broadcastLocked(run, StreamEvent{Type: "state", Run: runView(run)})
	}
}

func (m *Manager) reporterEvent(id string, event json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.view.State.Terminal() {
		return
	}
	if len(run.view.Events) >= m.limits.MaxEvents || run.eventBytes+len(event) > m.limits.MaxEventBytes {
		run.truncated = true
		return
	}
	copyEvent := append(json.RawMessage(nil), event...)
	run.view.Events = append(run.view.Events, copyEvent)
	run.eventBytes += len(copyEvent)
	m.broadcastLocked(run, StreamEvent{Type: "data", Data: copyEvent})
}

func (m *Manager) reporterResult(id string, result Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.view.State.Terminal() {
		return
	}
	if len(result.Processes) > m.limits.MaxEvents {
		result.Processes = result.Processes[:m.limits.MaxEvents]
		result.Truncated = true
	}
	if len(result.Connections) > m.limits.MaxEvents {
		result.Connections = result.Connections[:m.limits.MaxEvents]
		result.Truncated = true
	}
	result.Target = run.view.Target
	result.Aspect = run.view.Aspect
	result.Scope = "container"
	if result.Aspect == AspectConnections {
		result.Scope = "pod-network"
	}
	if result.CapturedAt.IsZero() {
		result.CapturedAt = time.Now().UTC()
	}
	result.CaptureDurationMS = result.CaptureDuration.Milliseconds()
	result.EventCount = max(result.EventCount, len(run.view.Events))
	if run.view.Aspect != AspectDNS {
		result.Truncated = result.Truncated || run.truncated
	}
	if result.EventLoss == "" {
		result.EventLoss = "unmeasurable"
	}
	run.view.Result = &result
	run.view.UpdatedAt = time.Now().UTC()
	run.view.State = StateStopping
	run.dataReadyOnce.Do(func() { close(run.dataReady) })
	m.broadcastLocked(run, StreamEvent{Type: "result", Run: runView(run)})
}

func (m *Manager) reporterPartial(id, reason string, duringCapture bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.runs[id]; run != nil && !run.view.State.Terminal() && (!duringCapture || run.view.Result == nil) {
		if run.partialReason != "" {
			if !duringCapture && reason != run.partialReason {
				run.view.Error = reason
			}
			return
		}
		run.partialReason = reason
		run.view.State = StateStopping
		run.view.UpdatedAt = time.Now().UTC()
		m.broadcastLocked(run, StreamEvent{Type: "state", Run: runView(run)})
		run.cancel()
	}
}

func (m *Manager) authorizeLocked(id string, owner Owner) (*managedRun, error) {
	run := m.runs[id]
	if run == nil || run.discard {
		return nil, ErrNotFound
	}
	if run.owner != owner {
		return nil, ErrForbidden
	}
	return run, nil
}

func (m *Manager) finishLocked(run *managedRun) {
	run.view.UpdatedAt = time.Now().UTC()
	m.active--
	m.byOwner[run.owner]--
	if m.byOwner[run.owner] == 0 {
		delete(m.byOwner, run.owner)
	}
	run.dataReadyOnce.Do(func() { close(run.dataReady) })
	run.doneOnce.Do(func() { close(run.done) })
	m.broadcastLocked(run, StreamEvent{Type: "state", Run: runView(run)})
	for ch := range run.subscribers {
		close(ch)
		delete(run.subscribers, ch)
	}
	if run.discard {
		delete(m.runs, run.view.ID)
		m.removeOrderLocked(run.view.ID)
	}
}

func (m *Manager) removeOrderLocked(id string) {
	for index, candidate := range m.order {
		if candidate == id {
			m.order = append(m.order[:index], m.order[index+1:]...)
			return
		}
	}
}

func (m *Manager) broadcastLocked(run *managedRun, event StreamEvent) {
	for ch := range run.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func (m *Manager) evictLocked() {
	for len(m.order) > m.limits.MaxRetained {
		evictionIndex := -1
		for index, id := range m.order {
			run := m.runs[id]
			if run == nil || run.view.State.Terminal() {
				evictionIndex = index
				break
			}
		}
		if evictionIndex < 0 {
			return
		}
		id := m.order[evictionIndex]
		delete(m.runs, id)
		m.order = append(m.order[:evictionIndex], m.order[evictionIndex+1:]...)
	}
}

func cloneRun(run Run) Run {
	copyRun := run
	if run.Result != nil {
		copyResult := *run.Result
		copyRun.Result = &copyResult
	}
	copyRun.Events = nil
	return copyRun
}

func runView(run *managedRun) *Run {
	view := cloneRun(run.view)
	return &view
}

type reporter struct {
	manager *Manager
	id      string
}

func (r reporter) Started(target Target)       { r.manager.reporterStarted(r.id, target) }
func (r reporter) Event(event json.RawMessage) { r.manager.reporterEvent(r.id, event) }
func (r reporter) Result(result Result)        { r.manager.reporterResult(r.id, result) }
func (r reporter) Interrupted(reason string)   { r.manager.reporterPartial(r.id, reason, true) }
func (r reporter) Partial(reason string)       { r.manager.reporterPartial(r.id, reason, false) }

func ValidateDuration(aspect Aspect, duration time.Duration) error {
	if aspect == AspectDNS {
		if duration < time.Second || duration > 5*time.Minute {
			return fmt.Errorf("%w: DNS duration must be between 1s and 5m", ErrInvalidRequest)
		}
		return nil
	}
	if duration != 0 {
		return fmt.Errorf("%w: snapshots do not accept a duration", ErrInvalidRequest)
	}
	return nil
}
