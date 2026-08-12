package igdebug

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type executorFunc func(context.Context, Request, Reporter) error

func (f executorFunc) Execute(ctx context.Context, request Request, reporter Reporter) error {
	return f(ctx, request, reporter)
}

func requestFor(owner Owner, aspect Aspect) Request {
	duration := time.Duration(0)
	if aspect == AspectDNS {
		duration = time.Second
	}
	return Request{Owner: owner, Aspect: aspect, Duration: duration, Target: Target{Namespace: "app", Pod: "api", Container: "api"}}
}

func TestSnapshotDataReadyDoesNotWaitForCleanup(t *testing.T) {
	manager := NewManager(DefaultLimits())
	owner := Owner{User: "alice", Context: "test"}
	cleanup := make(chan struct{})
	run, err := manager.Start(requestFor(owner, AspectProcesses), executorFunc(func(_ context.Context, req Request, reporter Reporter) error {
		reporter.Started(req.Target)
		reporter.Result(Result{Processes: []Process{{Command: "api", PID: 1}}})
		<-cleanup
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	view, err := manager.WaitData(waitCtx, run.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StateStopping || view.Result == nil || len(view.Result.Processes) != 1 {
		t.Fatalf("unexpected data-ready view: %#v", view)
	}
	close(cleanup)
	waitForState(t, manager, run.ID, owner, StateComplete)
}

func TestDNSDataReadyMovesToStoppingUntilExecutorReturns(t *testing.T) {
	manager := NewManager(DefaultLimits())
	owner := Owner{User: "alice", Context: "test"}
	cleanup := make(chan struct{})
	run, err := manager.Start(requestFor(owner, AspectDNS), executorFunc(func(_ context.Context, req Request, reporter Reporter) error {
		reporter.Started(req.Target)
		reporter.Result(Result{DNS: []DNSGroup{{Name: "example.com", Count: 1}}})
		<-cleanup
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	view, err := manager.WaitData(waitCtx, run.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StateStopping || view.Result == nil || len(view.Result.DNS) != 1 {
		t.Fatalf("unexpected data-ready view: %#v", view)
	}
	close(cleanup)
	waitForState(t, manager, run.ID, owner, StateComplete)
}

func TestConcurrencyCapsAreTotalAndPerOwner(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxConcurrent = 2
	limits.MaxConcurrentPerUser = 1
	manager := NewManager(limits)
	block := func(ctx context.Context, _ Request, _ Reporter) error { <-ctx.Done(); return ctx.Err() }
	alice := Owner{User: "alice", Context: "test"}
	bob := Owner{User: "bob", Context: "test"}
	first, err := manager.Start(requestFor(alice, AspectDNS), executorFunc(block))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(requestFor(alice, AspectDNS), executorFunc(block)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected per-owner capacity error, got %v", err)
	}
	second, err := manager.Start(requestFor(bob, AspectDNS), executorFunc(block))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(requestFor(Owner{User: "carol", Context: "test"}, AspectDNS), executorFunc(block)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected total capacity error, got %v", err)
	}
	_, _ = manager.Stop(first.ID, alice)
	_, _ = manager.Stop(second.ID, bob)
}

func TestTotalConcurrencyCapUnderRacingStarts(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxConcurrent = 4
	limits.MaxConcurrentPerUser = 2
	manager := NewManager(limits)
	executor := executorFunc(func(ctx context.Context, _ Request, _ Reporter) error {
		<-ctx.Done()
		return ctx.Err()
	})
	type startedRun struct {
		run   *Run
		owner Owner
	}
	started := make([]startedRun, 0, limits.MaxConcurrent)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for index := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner := Owner{User: fmt.Sprintf("user-%d", index), Context: "test"}
			run, err := manager.Start(requestFor(owner, AspectDNS), executor)
			if err == nil {
				mu.Lock()
				started = append(started, startedRun{run: run, owner: owner})
				mu.Unlock()
				return
			}
			if !errors.Is(err, ErrCapacity) {
				t.Errorf("Start error = %v", err)
			}
		}()
	}
	wg.Wait()
	if len(started) != limits.MaxConcurrent {
		t.Fatalf("started %d runs, want %d", len(started), limits.MaxConcurrent)
	}
	for _, entry := range started {
		_, _ = manager.Stop(entry.run.ID, entry.owner)
	}
}

func TestContextSwitchCancelsRunAsPartial(t *testing.T) {
	manager := NewManager(DefaultLimits())
	owner := Owner{User: "alice", Context: "old"}
	cancelled := make(chan struct{})
	run, err := manager.Start(requestFor(owner, AspectDNS), executorFunc(func(ctx context.Context, _ Request, _ Reporter) error {
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager.OnContextSwitch()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("executor was not cancelled")
	}
	if _, err := manager.Get(run.ID, owner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("context-switched run should be discarded, got %v", err)
	}
}

func TestRunOwnershipIsContextAndUserScoped(t *testing.T) {
	manager := NewManager(DefaultLimits())
	owner := Owner{User: "alice", Context: "one"}
	run, err := manager.Start(requestFor(owner, AspectDNS), executorFunc(func(ctx context.Context, _ Request, _ Reporter) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get(run.ID, Owner{User: "bob", Context: "one"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected user denial, got %v", err)
	}
	if _, err := manager.Get(run.ID, Owner{User: "alice", Context: "two"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected context denial, got %v", err)
	}
	_, _ = manager.Stop(run.ID, owner)
}

func TestWaitDataPreservesExecutorFailure(t *testing.T) {
	manager := NewManager(DefaultLimits())
	owner := Owner{User: "alice", Context: "test"}
	want := errors.New("proxy forbidden")
	run, err := manager.Start(requestFor(owner, AspectProcesses), executorFunc(func(context.Context, Request, Reporter) error {
		return want
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := manager.WaitData(ctx, run.ID, owner); !errors.Is(err, want) {
		t.Fatalf("WaitData error = %v, want %v", err, want)
	}
}

func TestManagerRetainsOnlyConfiguredTerminalRuns(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxRetained = 2
	manager := NewManager(limits)
	owner := Owner{User: "alice", Context: "test"}
	ids := make([]string, 0, 3)
	for range 3 {
		run, err := manager.Start(requestFor(owner, AspectProcesses), executorFunc(func(_ context.Context, req Request, reporter Reporter) error {
			reporter.Result(Result{Target: req.Target})
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		waitForState(t, manager, run.ID, owner, StateComplete)
		ids = append(ids, run.ID)
	}
	if _, err := manager.Get(ids[0], owner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest run should be evicted, got %v", err)
	}
	for _, id := range ids[1:] {
		if _, err := manager.Get(id, owner); err != nil {
			t.Fatalf("retained run %s: %v", id, err)
		}
	}
}

func TestRetentionSkipsOldestActiveRun(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxRetained = 2
	limits.MaxConcurrentPerUser = 2
	manager := NewManager(limits)
	owner := Owner{User: "alice", Context: "test"}
	active, err := manager.Start(requestFor(owner, AspectDNS), executorFunc(func(ctx context.Context, _ Request, _ Reporter) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	terminalIDs := make([]string, 0, 2)
	for range 2 {
		run, err := manager.Start(requestFor(owner, AspectProcesses), executorFunc(func(_ context.Context, _ Request, reporter Reporter) error {
			reporter.Result(Result{})
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		waitForState(t, manager, run.ID, owner, StateComplete)
		terminalIDs = append(terminalIDs, run.ID)
	}
	if _, err := manager.Get(active.ID, owner); err != nil {
		t.Fatalf("active run was evicted: %v", err)
	}
	if _, err := manager.Get(terminalIDs[0], owner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest terminal run should be evicted, got %v", err)
	}
	_, _ = manager.Stop(active.ID, owner)
}

func TestDNSAggregateDoesNotClaimRawStreamTruncation(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxEvents = 1
	manager := NewManager(limits)
	owner := Owner{User: "alice", Context: "test"}
	run, err := manager.Start(requestFor(owner, AspectDNS), executorFunc(func(_ context.Context, _ Request, reporter Reporter) error {
		reporter.Event([]byte(`{"event":1}`))
		reporter.Event([]byte(`{"event":2}`))
		reporter.Result(Result{DNS: []DNSGroup{{Name: "example.com", Count: 2}}})
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	view := waitForState(t, manager, run.ID, owner, StateComplete)
	if view.Result == nil || view.Result.Truncated {
		t.Fatalf("DNS aggregate should remain complete: %#v", view.Result)
	}
}

func waitForState(t *testing.T, manager *Manager, id string, owner Owner, state State) *Run {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		view, err := manager.Get(id, owner)
		if err != nil {
			t.Fatal(err)
		}
		if view.State == state {
			return view
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run did not reach %s", state)
	return nil
}
