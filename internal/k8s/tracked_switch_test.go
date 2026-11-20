package k8s

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func awaitSwitchPhase(t *testing.T, tracker *ContextSwitchTracker, phase string) ContextSwitchStatus {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if op := tracker.Snapshot(); op != nil && op.Phase == phase {
			return *op
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected phase %q, last snapshot %+v", phase, tracker.Snapshot())
	return ContextSwitchStatus{}
}

func TestTrackedSwitchCancellationPreservesCurrentCluster(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer api.Close()
	defer close(release)
	_, stopped := setupCheckedSwitch(t, api.URL, true)
	var tracker ContextSwitchTracker
	op, ok := tracker.Start("target", nil)
	if !ok || op.Phase != "preflight" || op.PreflightTimeoutSeconds != ContextSwitchTimeout.Seconds() || op.SyncTimeoutSeconds != FirstPaintBackstop.Seconds() {
		t.Fatalf("start: %+v", op)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no preflight")
	}
	again, ok := tracker.Start("target", nil)
	if !ok || again.ID != op.ID {
		t.Fatal("duplicate switch restarted preflight")
	}
	if _, ok := tracker.Start("another", nil); ok {
		t.Fatal("queued conflicting target")
	}
	if tracker.Cancel(op.ID, "another") || tracker.Cancel("wrong-id", "target") {
		t.Fatal("cancelled different switch")
	}
	if !tracker.Cancel(op.ID, "target") {
		t.Fatal("couldn't cancel during preflight")
	}
	awaitSwitchPhase(t, &tracker, "cancelled")
	if *stopped || GetConnectionStatus().Context != "current" {
		t.Fatal("cancelled preflight left previous cluster")
	}
}

func TestTrackedSwitchRefusalKeepsOldConnection(t *testing.T) {
	_, stopped := setupCheckedSwitch(t, unreachableServer(t), true)
	var tracker ContextSwitchTracker
	tracker.Start("target", nil)
	op := awaitSwitchPhase(t, &tracker, "failed")
	if op.ErrorType != "network" || *stopped || GetConnectionStatus().State != StateConnected {
		t.Fatalf("refusal %+v, old sessions stopped: %t", op, *stopped)
	}
}

func TestTrackedSwitchFinishesAfterPreflightBudget(t *testing.T) {
	var versionHits atomic.Int32
	setupCheckedSwitch(t, versionOnlyServer(t, &versionHits), true)
	old := ContextSwitchTimeout
	ContextSwitchTimeout = 250 * time.Millisecond
	defer func() { ContextSwitchTimeout = old }()
	release := make(chan struct{})
	entered := make(chan struct{})
	RegisterTimelineFuncs(nil, func() error { close(entered); <-release; return nil })
	t.Cleanup(func() { RegisterTimelineFuncs(nil, nil) })
	var tracker ContextSwitchTracker
	op, _ := tracker.Start("target", nil)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("never started warmup")
	}
	phase := tracker.Snapshot().Phase
	cancelled := tracker.Cancel(op.ID, "target")
	time.Sleep(ContextSwitchTimeout + 10*time.Millisecond)
	close(release)
	result := awaitSwitchPhase(t, &tracker, "succeeded")
	status := GetConnectionStatus()
	if phase != "switching" || cancelled || result.ID != op.ID || status.State != StateConnected || status.Context != "target" || versionHits.Load() != 1 {
		t.Fatalf("switch %+v connection %+v cancel %t version reads %d", result, status, cancelled, versionHits.Load())
	}
}

func TestTrackedSwitchInitFailureReportsTargetDisconnected(t *testing.T) {
	for name, init := range map[string]func() error{
		"error": func() error { return errors.New("timeline unavailable") },
		"panic": func() error { panic("callback failed") },
	} {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			_, stopped := setupCheckedSwitch(t, versionOnlyServer(t, &hits), true)
			RegisterTimelineFuncs(nil, init)
			t.Cleanup(func() { RegisterTimelineFuncs(nil, nil) })
			var tracker ContextSwitchTracker
			tracker.Start("target", nil)
			op := awaitSwitchPhase(t, &tracker, "failed")
			status := GetConnectionStatus()
			if !*stopped || status.State != StateDisconnected || status.Context != "target" || op.Error == "" || op.Error != status.Error || op.ErrorType != "" {
				t.Fatalf("post-teardown failure: operation %+v, connection %+v", op, status)
			}
		})
	}
}
