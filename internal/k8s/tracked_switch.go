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

// ContextSwitchStatus is the last user-requested switch. Only preflight can be
// cancelled: after teardown the previous cluster's sessions cannot be restored.
type ContextSwitchStatus struct {
	ID                      string    `json:"id"`
	Target                  string    `json:"target"`
	Phase                   string    `json:"phase"`
	StartedAt               time.Time `json:"startedAt"`
	PreflightTimeoutSeconds float64   `json:"preflightTimeoutSeconds"`
	SyncTimeoutSeconds      float64   `json:"syncTimeoutSeconds"`
	Progress                string    `json:"progress"`
	Error                   string    `json:"error,omitempty"`
	ErrorType               string    `json:"errorType,omitempty"`
}

type ContextSwitchTracker struct {
	mu     sync.Mutex
	status ContextSwitchStatus
	cancel context.CancelFunc
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

func (t *ContextSwitchTracker) Start(target string, onFailure func(error)) (ContextSwitchStatus, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		return t.status, t.status.Target == target
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.status = ContextSwitchStatus{
		ID: rand.Text(), Target: target, StartedAt: time.Now().UTC(),
		Phase: "preflight", Progress: "Checking target cluster…",
		PreflightTimeoutSeconds: ContextSwitchTimeout.Seconds(), SyncTimeoutSeconds: FirstPaintBackstop.Seconds(),
	}
	go t.run(ctx, t.status, onFailure)
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

// Stop also cancels committed work when its owning server shuts down.
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

func (t *ContextSwitchTracker) progress(stage string) {
	t.mu.Lock()
	t.status.Progress = stage
	t.mu.Unlock()
}

func (t *ContextSwitchTracker) run(ctx context.Context, op ContextSwitchStatus, onFailure func(error)) {
	var err error
	defer func() {
		if p := recover(); p != nil {
			err = contextSwitchPanic(p)
		}
		t.finish(ctx, op, err, onFailure)
	}()
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
	case ctx.Err() != nil || errors.Is(err, ErrReconnectSuperseded):
		t.status.Phase = "cancelled"
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
