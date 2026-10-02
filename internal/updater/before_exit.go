package updater

import "sync"

// Relaunch ends in os.Exit, which runs no deferred function and no shutdown
// hook, so anything that must happen on a deliberate exit has to be registered
// here. Relaunch is called from its own goroutine while registration happens on
// the startup path, so the slice is guarded rather than left bare.
var (
	beforeExitMu   sync.Mutex
	beforeExit     []func()
	beforeExitOnce sync.Once
)

// OnBeforeExit registers cleanup to run immediately before a self-update
// relaunch terminates this process.
func OnBeforeExit(fn func()) {
	if fn == nil {
		return
	}
	beforeExitMu.Lock()
	defer beforeExitMu.Unlock()
	beforeExit = append(beforeExit, fn)
}

// runBeforeExit invokes the registered cleanup once. Running twice would let a
// retried relaunch repeat side effects that are only safe to perform on the
// way out.
func runBeforeExit() {
	beforeExitOnce.Do(func() {
		beforeExitMu.Lock()
		callbacks := make([]func(), len(beforeExit))
		copy(callbacks, beforeExit)
		beforeExitMu.Unlock()

		for _, fn := range callbacks {
			fn()
		}
	})
}
