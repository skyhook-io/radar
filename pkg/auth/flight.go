package auth

import (
	"context"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// cancelWhenIdle, as a flight's budget, ties the run to its callers: it is
// canceled when the last one stops waiting, so sharing never gives a caller a
// longer budget than its own.
const cancelWhenIdle time.Duration = 0

// flight is one run shared by the callers that asked for it while it ran. With
// cancelWhenIdle it lives as long as some caller waits; with a budget it runs
// until it finishes or the budget ends, whoever still waits, so work that
// outlasts every request can still warm a cache. The owner guards waiters, and
// its own index of flights, with one lock.
type flight[T any] struct {
	done           chan struct{}
	cancelWhenIdle context.CancelFunc
	waiters        int
	result         T // written before done closes
}

// startFlight runs do on its own goroutine and returns the flight with its
// first caller counted. The run keeps ctx's values but not its cancellation.
// finish runs under mu once do returns, and its result is what every caller
// gets. A panic in do yields T's zero value, which every caller must treat as
// a failure. Call with mu held.
func startFlight[T any](ctx context.Context, mu sync.Locker, what string, budget time.Duration, do func(context.Context) T, finish func(*flight[T], T) T) *flight[T] {
	f := &flight[T]{done: make(chan struct{}), waiters: 1}
	runCtx := context.WithoutCancel(ctx)
	var cancel context.CancelFunc
	if budget == cancelWhenIdle {
		runCtx, cancel = context.WithCancel(runCtx)
		f.cancelWhenIdle = cancel
	} else {
		runCtx, cancel = context.WithTimeout(runCtx, budget)
	}
	go func() {
		defer cancel()
		v := runRecovered(runCtx, what, do)
		mu.Lock()
		if runCtx.Err() != nil {
			var zero T
			v = zero
		}
		f.result = finish(f, v)
		mu.Unlock()
		close(f.done)
	}()
	return f
}

// runRecovered runs do on the flight's own goroutine, where a panic would end
// the process instead of failing the callers waiting on it.
func runRecovered[T any](ctx context.Context, what string, do func(context.Context) T) (v T) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[auth] %s panicked: %v\n%s", what, r, debug.Stack())
			var zero T
			v = zero
		}
	}()
	return do(ctx)
}

// join counts another caller. Call with mu held.
func (f *flight[T]) join() { f.waiters++ }

// await returns the flight's result, or false when ctx ends first, even if the
// result is ready by then. For a cancelWhenIdle flight the last caller to stop
// waiting cancels the run and calls detach, under mu, so no later caller joins
// a canceled run.
func (f *flight[T]) await(ctx context.Context, mu sync.Locker, detach func()) (T, bool) {
	select {
	case <-f.done:
		if ctx.Err() == nil {
			return f.result, true
		}
	case <-ctx.Done():
	}
	mu.Lock()
	f.waiters--
	if f.waiters == 0 && f.cancelWhenIdle != nil {
		f.cancelWhenIdle()
		detach()
	}
	mu.Unlock()
	var zero T
	return zero, false
}
