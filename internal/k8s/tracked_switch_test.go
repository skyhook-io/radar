package k8s

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/errorlog"
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
	if tracker.Snapshot().CanStop || tracker.StopSwitch(op.ID, op.Target) {
		close(release)
		t.Fatal("advertised Stop during serialized timeline setup")
	}
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

func TestRetryCannotReplaceACommittedSwitch(t *testing.T) {
	var hits atomic.Int32
	setupCheckedSwitch(t, versionOnlyServer(t, &hits), true)
	entered, release := make(chan struct{}), make(chan struct{})
	OnBeforeContextSwitch(func(name string) {
		if name == "target" {
			close(entered)
			<-release
		}
	})
	var tracker ContextSwitchTracker
	tracker.Start("target", nil)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("switch never reached commit")
	}
	err := RetryCurrentConnection()
	close(release)
	awaitSwitchPhase(t, &tracker, "succeeded")
	if !errors.Is(err, ErrReconnectSuperseded) || GetContextName() != "target" {
		t.Fatalf("retry = %v, selected context = %q", err, GetContextName())
	}
}

func TestStopCommittedSwitchWhileWaitingForDiscovery(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var held atomic.Bool
	var versionHits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			versionHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"gitVersion":"v1.31.0"}`))
			return
		}
		if (r.URL.Path == "/api" || r.URL.Path == "/apis") && held.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
	}))
	defer api.Close()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	_, sessionsClosed := setupCheckedSwitch(t, api.URL, true)
	var completed atomic.Int32
	var tracker ContextSwitchTracker
	OnContextSwitchComplete(func(name, id string) {
		if tracker.StopSwitch(id, name) {
			t.Error("accepted Stop after completion callbacks began")
		}
		completed.Add(1)
	})
	op, _ := tracker.Start("target", nil)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery never started")
	}
	for deadline := time.Now().Add(5 * time.Second); tracker.Snapshot().Progress != "Waiting for Kubernetes API discovery…"; {
		if time.Now().After(deadline) {
			t.Fatalf("discovery wait not reached: %+v", tracker.Snapshot())
		}
		time.Sleep(time.Millisecond)
	}
	if !tracker.Snapshot().CanStop || tracker.StopSwitch("old", "target") || tracker.StopSwitch(op.ID, "different") || !tracker.StopSwitch(op.ID, "target") {
		t.Fatalf("could not stop only the matching switch: %+v", tracker.Snapshot())
	}
	awaitSwitchPhase(t, &tracker, "stopped")
	assertNoSwitchFailureLogged(t, op)
	status := GetConnectionStatus()
	if !*sessionsClosed || status.State != StateDisconnected || status.Context != "target" || status.ErrorType != switchStoppedErrorType || RuntimeAuthRecoveryOwed() || completed.Load() != 0 {
		t.Fatalf("stopped switch: %+v, callbacks %d", status, completed.Load())
	}
	if GetClient() != nil || GetResourceDiscovery() != nil || GetResourceCache() != nil || GetSyncingResourceCache() != nil {
		t.Fatal("stopped cluster still has live clients or caches")
	}
	if err := RetryCurrentConnection(); !errors.Is(err, ErrReconnectSuperseded) {
		t.Fatalf("retry after Stop = %v", err)
	}
	next, ok := tracker.Start("target", nil)
	if !ok || next.ID == op.ID {
		t.Fatal("explicit reselection after Stop failed")
	}
	awaitSwitchPhase(t, &tracker, "succeeded")
	unblock()
	if GetConnectionStatus().State != StateConnected || completed.Load() != 1 || versionHits.Load() != 2 {
		t.Fatalf("replacement switch: %+v, callbacks %d, probes %d", GetConnectionStatus(), completed.Load(), versionHits.Load())
	}
}

func TestStopCommittedSwitchDuringConnectivityCheck(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer api.Close()
	defer close(release)
	_, sessionsClosed := setupCheckedSwitch(t, api.URL, false)
	var tracker ContextSwitchTracker
	op, _ := tracker.Start("target", nil)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("target connectivity check never started")
	}
	if !tracker.StopSwitch(op.ID, op.Target) {
		t.Fatalf("could not stop target connectivity check: %+v", tracker.Snapshot())
	}
	awaitSwitchPhase(t, &tracker, "stopped")
	if !*sessionsClosed || GetConnectionStatus().ErrorType != switchStoppedErrorType || GetClient() != nil {
		t.Fatalf("stopped connectivity check: %+v", GetConnectionStatus())
	}
	assertNoSwitchFailureLogged(t, op)
}

func assertNoSwitchFailureLogged(t *testing.T, op ContextSwitchStatus) {
	t.Helper()
	for _, entry := range errorlog.GetEntries() {
		if entry.Source == "context-switch" && entry.Level == "error" && !entry.Time.Before(op.StartedAt) {
			t.Errorf("user Stop recorded as a failure: %s", entry.Message)
		}
	}
}

func TestTrackedSwitchWaitsForAllUntrackedAdmissions(t *testing.T) {
	var hits atomic.Int32
	setupCheckedSwitch(t, versionOnlyServer(t, &hits), true)
	var tracker ContextSwitchTracker
	first, second := tracker.BeginUntracked(), tracker.BeginUntracked()
	if first == nil || second == nil {
		t.Fatal("synchronous requests must retain their existing serialization")
	}
	first()
	first()
	if _, ok := tracker.Start("target", nil); ok {
		t.Fatal("tracked switch admitted over a synchronous request")
	}
	second()
	if _, ok := tracker.Start("target", nil); !ok {
		t.Fatal("tracked switch refused after all synchronous operations returned")
	}
	awaitSwitchPhase(t, &tracker, "succeeded")
}

func TestTrackedSwitchReportsSupersededPreflight(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gitVersion":"v1.31.0"}`))
	}))
	defer api.Close()
	_, closed := setupCheckedSwitch(t, api.URL, true)
	var tracker ContextSwitchTracker
	tracker.Start("target", nil)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("preflight did not start")
	}
	CancelOngoingOperations()
	close(release)
	op := awaitSwitchPhase(t, &tracker, "failed")
	if op.Error == "" || *closed || GetConnectionStatus().Context != "current" {
		t.Fatalf("superseded preflight = %+v, previous sessions closed = %t", op, *closed)
	}
}

func TestTrackedSwitchAdmittedDuringLocalRetry(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	current := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		http.Error(w, "unreachable", http.StatusForbidden)
	}))
	defer current.Close()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var targetProbes atomic.Int32
	targetPath, _ := setupCheckedSwitch(t, versionOnlyServer(t, &targetProbes), false)
	writeServerKubeconfig(t, filepath.Dir(targetPath), "current.yaml", "current", current.URL)
	var tracker ContextSwitchTracker
	finish := tracker.BeginRetry()
	if finish == nil {
		t.Fatal("initial retry refused")
	}
	retried := make(chan error, 1)
	go func() {
		err := RetryCurrentConnection()
		finish()
		retried <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("retry did not reach its connectivity probe")
	}
	queued := tracker.BeginRetry()
	if queued == nil {
		t.Fatal("second retry was not admitted before the selection")
	}
	op, ok := tracker.Start("target", nil)
	if !ok || op.Phase != "preflight" || op.Progress != "Waiting for the current connection retry…" || tracker.BeginRetry() != nil {
		t.Fatalf("selection during Retry: %+v, accepted %t", op, ok)
	}
	if !tracker.Cancel(op.ID, op.Target) {
		t.Fatal("could not cancel the waiting selection")
	}
	next, ok := tracker.Start("target", nil)
	if !ok || next.ID == op.ID || targetProbes.Load() != 0 {
		t.Fatalf("replacement selection: %+v, probes %d", next, targetProbes.Load())
	}
	unblock()
	if err := <-retried; err == nil {
		t.Fatal("unreachable current cluster unexpectedly recovered")
	}
	if snapshot := tracker.Snapshot(); snapshot.ID != next.ID || snapshot.Progress != next.Progress || targetProbes.Load() != 0 {
		t.Fatalf("selection skipped an admitted retry: %+v", snapshot)
	}
	queued()
	awaitSwitchPhase(t, &tracker, "succeeded")
	if status := GetConnectionStatus(); status.Context != "target" || status.State != StateConnected || targetProbes.Load() != 1 {
		t.Fatalf("Retry overwrote the selected target: %+v, target probes %d", status, targetProbes.Load())
	}
}
