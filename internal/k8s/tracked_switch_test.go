package k8s

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
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
	if !ok || op.Phase != "preflight" || op.PreflightTimeoutSeconds != 0 || op.SyncTimeoutSeconds != FirstPaintBackstop.Seconds() {
		t.Fatalf("start: %+v", op)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no preflight")
	}
	if limit := tracker.Snapshot().PreflightTimeoutSeconds; limit != ConnectionTestTimeout.Seconds() {
		t.Fatalf("plain target advertised a %v-second probe", limit)
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
	withDeadlinesRestored(t, func() {
		ConfigureDeadlines(DeadlineOptions{ContextSwitchTimeout: 250 * time.Millisecond})
	})
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

func TestContextSwitchRestoresPreviousConnection(t *testing.T) {
	for _, mode := range []string{"tracked", "synchronous", "CAPI", "restore fails"} {
		t.Run(mode, func(t *testing.T) {
			var currentHits, targetHits atomic.Int32
			targetPath, stopped := setupCheckedSwitch(t, versionOnlyServer(t, &targetHits), true)
			writeServerKubeconfig(t, filepath.Dir(targetPath), "current.yaml", "current", versionOnlyServer(t, &currentHits))
			if err := SwitchContext("current"); err != nil {
				t.Fatal(err)
			}
			_, originalGeneration := GetClientWithGeneration()
			originalOperation := OperationContext()
			targetErr := errors.New("target timeline unavailable")
			var builds, completed, messages []string
			RegisterTimelineFuncs(nil, func() error {
				name := GetContextName()
				builds = append(builds, name)
				if name == "target" {
					return targetErr
				}
				if mode == "restore fails" {
					return errors.New("current timeline unavailable")
				}
				return nil
			})
			t.Cleanup(func() { RegisterTimelineFuncs(nil, nil) })
			OnContextSwitchProgress(func(message string) { messages = append(messages, message) })
			OnContextSwitchComplete(func(name, id string) { completed = append(completed, name+"/"+id) })

			var id string
			switch mode {
			case "tracked", "restore fails":
				var tracker ContextSwitchTracker
				tracker.Start("target", nil)
				op := awaitSwitchPhase(t, &tracker, "failed")
				id = op.ID
				if mode == "restore fails" {
					status := GetConnectionStatus()
					if op.RestoredContext != "" || status.State != StateDisconnected || status.Error != op.Error ||
						!strings.Contains(op.Error, targetErr.Error()) || !strings.Contains(op.Error, "current timeline unavailable") ||
						!strings.Contains(op.Error, `switch to "target"`) || !strings.Contains(op.Error, `reconnect to "current"`) ||
						len(completed) != 0 || !slices.Equal(builds, []string{"target", "current"}) {
						t.Fatalf("dual failure: switch %+v, status %+v, completed %v, builds %v", op, status, completed, builds)
					}
					return
				}
				if op.Target != "target" || op.RestoredContext != "current" || !strings.Contains(op.Error, targetErr.Error()) {
					t.Fatalf("switch: %+v", op)
				}
			case "synchronous":
				if err := PerformCheckedContextSwitch(context.Background(), "target"); !errors.Is(err, targetErr) {
					t.Fatalf("switch error = %v", err)
				}
			case "CAPI":
				if err := PerformContextSwitch("target"); !errors.Is(err, targetErr) {
					t.Fatalf("switch error = %v", err)
				}
			}

			status := GetConnectionStatus()
			_, generation := GetClientWithGeneration()
			if status.State != StateConnected || status.Context != "current" || GetContextName() != "current" ||
				GetResourceCache() == nil || generation == originalGeneration || originalOperation.Err() == nil || OperationContext().Err() != nil {
				t.Fatalf("restore did not admit fresh clients and caches: %+v, generation %d", status, generation)
			}
			if !*stopped || !slices.Equal(builds, []string{"target", "current"}) || !slices.Equal(completed, []string{"current/" + id}) ||
				!slices.Contains(messages, "Couldn't connect to target. Reconnecting to current…") || currentHits.Load() != 1 {
				t.Fatalf("builds %v, completed %v, messages %v, restore probes %d", builds, completed, messages, currentHits.Load())
			}
		})
	}
}

func TestContextSwitchAbandonsRestoreOnSupersessionOrShutdown(t *testing.T) {
	for _, action := range []string{"supersede", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			var targetHits, restoreHits atomic.Int32
			target, _ := setupCheckedSwitch(t, versionOnlyServer(t, &targetHits), true)
			writeServerKubeconfig(t, filepath.Dir(target), "current.yaml", "current", versionOnlyServer(t, &restoreHits))
			var tracker ContextSwitchTracker
			RegisterTimelineFuncs(nil, func() error {
				if action == "shutdown" {
					tracker.Stop()
				} else {
					CancelOngoingOperations()
				}
				return errors.New("timeline unavailable")
			})
			t.Cleanup(func() { RegisterTimelineFuncs(nil, nil) })
			tracker.Start("target", nil)
			phase := "failed"
			if action == "shutdown" {
				phase = "cancelled"
			}
			op := awaitSwitchPhase(t, &tracker, phase)
			if op.RestoredContext != "" || restoreHits.Load() != 0 {
				t.Fatalf("interrupted switch %+v, restore probes %d", op, restoreHits.Load())
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
	targetPath, sessionsClosed := setupCheckedSwitch(t, api.URL, true)
	var restoreProbes atomic.Int32
	writeServerKubeconfig(t, filepath.Dir(targetPath), "current.yaml", "current", versionOnlyServer(t, &restoreProbes))
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
	if restoreProbes.Load() != 0 {
		t.Fatal("Stop probed the previous cluster for a restore")
	}
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
	retryCtx, finish := tracker.BeginRetry()
	if finish == nil {
		t.Fatal("initial retry refused")
	}
	retried := make(chan error, 1)
	go func() {
		err := RetryLocalConnection(retryCtx)
		finish()
		retried <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("retry did not reach its connectivity probe")
	}
	_, queued := tracker.BeginRetry()
	if queued == nil {
		t.Fatal("second retry was not admitted before the selection")
	}
	op, ok := tracker.Start("target", nil)
	_, later := tracker.BeginRetry()
	if !ok || op.Phase != "preflight" || op.Progress != "Stopping the current connection retry…" || later != nil {
		t.Fatalf("selection during Retry: %+v, accepted %t", op, ok)
	}
	if !tracker.Cancel(op.ID, op.Target) {
		t.Fatal("could not cancel the waiting selection")
	}
	next, ok := tracker.Start("target", nil)
	if !ok || next.ID == op.ID || targetProbes.Load() != 0 {
		t.Fatalf("replacement selection: %+v, probes %d", next, targetProbes.Load())
	}
	select {
	case err := <-retried:
		if !errors.Is(err, ErrReconnectSuperseded) {
			t.Fatalf("picker interruption = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("picker waited for the old target connectivity check")
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

func TestLocalRetryWarmupOutlivesSwitchTimeoutAndYieldsToPicker(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		name := "ready"
		if interrupt {
			name = "select"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var first atomic.Bool
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/version" {
					_, _ = w.Write([]byte(`{"gitVersion":"v1.31.0"}`))
					return
				}
				if (r.URL.Path == "/api" || r.URL.Path == "/apis") && first.CompareAndSwap(false, true) {
					close(entered)
					<-release
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
			}))
			defer api.Close()
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var hits atomic.Int32
			target, _ := setupCheckedSwitch(t, versionOnlyServer(t, &hits), false)
			writeServerKubeconfig(t, filepath.Dir(target), "current.yaml", "current", api.URL)
			withDeadlinesRestored(t, func() {
				ConfigureDeadlines(DeadlineOptions{ContextSwitchTimeout: 100 * time.Millisecond, FirstPaintBackstop: 5 * time.Second})
			})
			var tracker ContextSwitchTracker
			ctx, finish := tracker.BeginRetry()
			done := make(chan error, 1)
			go func() { err := RetryLocalConnection(ctx); finish(); done <- err }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("discovery did not start")
			}
			select {
			case err := <-done:
				t.Fatalf("warmup stopped at the connectivity deadline: %v", err)
			case <-time.After(2 * ContextSwitchTimeout):
			}
			if !interrupt {
				unblock()
				if err := <-done; err != nil {
					t.Fatalf("slow reachable cluster failed: %v", err)
				}
				return
			}
			if _, ok := tracker.Start("target", nil); !ok {
				t.Fatal("picker refused during warmup")
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrReconnectSuperseded) {
					t.Fatalf("superseded warmup: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("picker did not interrupt discovery")
			}
			awaitSwitchPhase(t, &tracker, "succeeded")
			if GetContextName() != "target" || hits.Load() != 1 {
				t.Fatal("picker did not connect to the replacement")
			}
		})
	}
}
