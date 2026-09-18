package updater

import (
	"sync"
	"testing"
)

// resetBeforeExit clears the process-wide hook state so each test starts from
// the same place; sync.Once cannot be reset, so it is replaced outright.
func resetBeforeExit(t *testing.T) {
	t.Helper()
	beforeExitMu.Lock()
	original := beforeExit
	beforeExit = nil
	beforeExitOnce = sync.Once{}
	beforeExitMu.Unlock()

	t.Cleanup(func() {
		beforeExitMu.Lock()
		beforeExit = original
		beforeExitOnce = sync.Once{}
		beforeExitMu.Unlock()
	})
}

// Relaunch ends in os.Exit, which runs no deferred function and no shutdown
// hook. Anything that must happen on a deliberate exit only happens if these
// callbacks fire, in the order they were registered.
func TestRunBeforeExitInvokesRegisteredCleanup(t *testing.T) {
	resetBeforeExit(t)

	var order []string
	OnBeforeExit(func() { order = append(order, "first") })
	OnBeforeExit(func() { order = append(order, "second") })

	runBeforeExit()

	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("callbacks ran as %v, want [first second]", order)
	}
}

// A retried relaunch must not repeat cleanup that is only safe on the way out.
func TestRunBeforeExitRunsOnlyOnce(t *testing.T) {
	resetBeforeExit(t)

	calls := 0
	OnBeforeExit(func() { calls++ })

	runBeforeExit()
	runBeforeExit()

	if calls != 1 {
		t.Errorf("callback ran %d times, want 1", calls)
	}
}

func TestRunBeforeExitWithNoneRegistered(t *testing.T) {
	resetBeforeExit(t)
	runBeforeExit()
}

func TestOnBeforeExitIgnoresNil(t *testing.T) {
	resetBeforeExit(t)

	OnBeforeExit(nil)
	runBeforeExit() // must not panic
}

// Relaunch runs on its own goroutine while registration happens on the startup
// path, so both sides have to be safe under -race.
func TestOnBeforeExitIsSafeUnderConcurrentUse(t *testing.T) {
	resetBeforeExit(t)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			OnBeforeExit(func() {})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		runBeforeExit()
	}()
	wg.Wait()
}
