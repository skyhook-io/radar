package k8s

import (
	"context"
	"crypto/rand"
	"errors"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// ContextSwitchStatus is the last user-requested switch.
type ContextSwitchStatus struct {
	ID                      string    `json:"id"`
	Target                  string    `json:"target"`
	Phase                   string    `json:"phase"`
	StartedAt               time.Time `json:"startedAt"`
	PreflightTimeoutSeconds float64   `json:"preflightTimeoutSeconds,omitempty"`
	SyncTimeoutSeconds      float64   `json:"syncTimeoutSeconds"`
	CanStop                 bool      `json:"canStop,omitempty"`
	Progress                string    `json:"progress"`
	Error                   string    `json:"error,omitempty"`
	ErrorType               string    `json:"errorType,omitempty"`
}

type ContextSwitchTracker struct {
	mu            sync.Mutex
	status        ContextSwitchStatus
	cancel        context.CancelFunc
	untracked     int
	retries       int
	retriesDone   chan struct{}
	retryContext  context.Context
	cancelRetries context.CancelFunc
}

func (t *ContextSwitchTracker) Snapshot() *ContextSwitchStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.ID == "" {
		return nil
	}
	s := t.status
	return &s
}

// BeginUntracked reserves a synchronous switch or CAPI connect. Those
// operations keep their existing serialization with each other; tracked
// switches must never overlap them, including during either side's preflight.
func (t *ContextSwitchTracker) BeginUntracked() (release func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		return nil
	}
	t.untracked++
	return sync.OnceFunc(func() {
		t.mu.Lock()
		t.untracked--
		t.mu.Unlock()
	})
}

// BeginRetry lets a later picker selection join the current retries' wait
// before taking its preflight generation. Once selected, it excludes new retries.
func (t *ContextSwitchTracker) BeginRetry() (ctx context.Context, release func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		return nil, nil
	}
	if t.retries == 0 {
		t.retriesDone = make(chan struct{})
		t.retryContext, t.cancelRetries = context.WithCancel(context.Background())
	}
	t.retries++
	return t.retryContext, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.retries--
		if t.retries == 0 {
			t.cancelRetries()
			t.retryContext, t.cancelRetries = nil, nil
			close(t.retriesDone)
			t.retriesDone = nil
		}
	}
}

func (t *ContextSwitchTracker) Start(target string, onFailure func(error)) (ContextSwitchStatus, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.untracked != 0 {
		return t.status, false
	}
	if t.cancel != nil {
		return t.status, t.status.Target == target
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.status = ContextSwitchStatus{
		ID: rand.Text(), Target: target, StartedAt: time.Now().UTC(),
		Phase: "preflight", Progress: "Checking target cluster…",
		SyncTimeoutSeconds: FirstPaintBackstop.Seconds(),
	}
	if t.retriesDone != nil {
		t.cancelRetries()
		t.status.Progress = "Waiting for the current connection retry…"
	}
	go t.run(ctx, t.status, onFailure, t.retriesDone)
	return t.status, true
}

func (t *ContextSwitchTracker) Cancel(id, target string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.ID != id || t.status.Target != target || t.status.Phase != "preflight" {
		return false
	}
	t.cancel()
	t.cancel = nil
	t.status.Phase = "cancelled"
	t.status.Progress = ""
	return true
}

// StopSwitch abandons the target after teardown. A preflight cancellation
// that races teardown must never be interpreted as permission to disconnect.
func (t *ContextSwitchTracker) StopSwitch(id, target string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.ID != id || t.status.Target != target {
		return false
	}
	if t.status.Phase == "stopping" || t.status.Phase == "stopped" {
		return true
	}
	if !t.status.CanStop {
		return false
	}
	t.status.CanStop = false
	t.status.Phase = "stopping"
	t.status.Progress = "Stopping cluster switch…"
	t.cancel()
	return true
}

// Stop cancels work when its owning server shuts down.
func (t *ContextSwitchTracker) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
}

func (t *ContextSwitchTracker) commit(ctx context.Context) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	t.status.Phase = "switching"
	t.status.Progress = "Stopping previous cluster sessions…"
	return true
}

// Serialized integration callbacks cannot be interrupted. A Stop accepted
// before they start must also prevent their completion notifications.
func (t *ContextSwitchTracker) setCanStop(ctx context.Context, canStop bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx.Err() != nil || t.status.Phase != "switching" {
		return false
	}
	t.status.CanStop = canStop
	return true
}

func (t *ContextSwitchTracker) recordProbeTimeout(ctx context.Context, timeout time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx.Err() == nil {
		t.status.PreflightTimeoutSeconds = timeout.Seconds()
	}
}

func (t *ContextSwitchTracker) progress(stage string) {
	t.mu.Lock()
	if t.status.Phase == "switching" {
		t.status.Progress = stage
	}
	t.mu.Unlock()
}

func (t *ContextSwitchTracker) run(ctx context.Context, op ContextSwitchStatus, onFailure func(error), retriesDone <-chan struct{}) {
	var err error
	defer func() {
		if p := recover(); p != nil {
			err = contextSwitchPanic(p)
		}
		t.finish(ctx, op, err, onFailure)
	}()
	if retriesDone != nil {
		select {
		case <-ctx.Done():
			return
		case <-retriesDone:
		}
		t.mu.Lock()
		if ctx.Err() != nil {
			t.mu.Unlock()
			return
		}
		t.status.Progress = "Checking target cluster…"
		t.mu.Unlock()
	}
	err = performCheckedContextSwitch(ctx, op.Target, t)
}

func contextSwitchPanic(p any) error {
	log.Printf("[ops] Context switch panicked: %v\n%s", p, debug.Stack())
	return errors.New("Radar could not finish the cluster switch. See the local Radar logs.")
}

func (t *ContextSwitchTracker) finish(ctx context.Context, op ContextSwitchStatus, err error, onFailure func(error)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.ID != op.ID || t.status.Phase == "cancelled" {
		return
	}
	switch {
	case t.status.Phase == "stopping":
		t.status.Phase = "stopped"
	case ctx.Err() != nil:
		t.status.Phase = "cancelled"
	case errors.Is(err, ErrReconnectSuperseded):
		t.status.Phase = "failed"
		t.status.Error = "Another cluster operation interrupted the switch. Select the cluster to try again."
	case err != nil:
		t.status.Phase = "failed"
		t.status.Error = err.Error()
		var probe *ContextProbeError
		if errors.As(err, &probe) {
			t.status.Error = probe.Error()
			t.status.ErrorType = probe.Classification()
		}
		log.Printf("[ops] Context switch to %q failed: %v", op.Target, err)
		if onFailure != nil {
			onFailure(err)
		}
	default:
		t.status.Phase = "succeeded"
	}
	t.status.Progress = ""
	t.cancel()
	t.cancel = nil
}
