package k8s

import (
	"context"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultRuntimeAuthRecoveryInitialInterval = 30 * time.Second
	defaultRuntimeAuthRecoveryMaxInterval     = 5 * time.Minute
	// A hung exec plugin leaks a blocked goroutine and a child process per
	// probe, so probing rarely bounds the leak while still self-healing.
	defaultRuntimeAuthRecoveryHungInterval = 30 * time.Minute
)

var (
	runtimeAuthRecoveryInitialInterval = defaultRuntimeAuthRecoveryInitialInterval
	runtimeAuthRecoveryMaxInterval     = defaultRuntimeAuthRecoveryMaxInterval
	runtimeAuthRecoveryHungInterval    = defaultRuntimeAuthRecoveryHungInterval

	runtimeAuthRecoveryActive atomic.Bool
	runtimeAuthReconnect      = PerformContextSwitch
)

// startRuntimeAuthRecovery keeps a headless deployment (MCP-only, no browser
// tab) from wedging permanently after an auth demotion: nothing else would
// ever retry once the user restores credentials, so the server re-probes on a
// backoff and reconnects itself. Browser sessions benefit too — the reconnect
// surfaces through SSE and the status poll.
func startRuntimeAuthRecovery(contextName string) {
	if !runtimeAuthRecoveryActive.CompareAndSwap(false, true) {
		return
	}
	go runRuntimeAuthRecovery(contextName)
}

func runRuntimeAuthRecovery(contextName string) {
	defer runtimeAuthRecoveryActive.Store(false)
	initialInterval, maxInterval, hungInterval := runtimeAuthRecoveryIntervals()
	interval := initialInterval
	for {
		time.Sleep(interval)
		if !runtimeAuthRecoveryStillNeeded(contextName) {
			return
		}
		if activeContextOperations.Load() != 0 {
			// A user-driven switch or retry is queued or running; let it win
			// this tick. If it fails, the demoted state persists and the loop
			// re-checks next tick.
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), connectionTestOperationTimeout())
		err := getRuntimeAuthProbe()(ctx)
		cancel()
		if err == nil {
			if !runtimeAuthRecoveryStillNeeded(contextName) {
				return
			}
			log.Printf("[k8s] Credentials restored for context %q; reconnecting automatically", SanitizeForLog(contextName))
			if switchErr := getRuntimeAuthReconnect()(contextName); switchErr != nil {
				log.Printf("[k8s] Automatic reconnect failed for context %q: %v", SanitizeForLog(contextName), switchErr)
				interval = nextRuntimeAuthRecoveryInterval(interval, switchErr, maxInterval, hungInterval)
				continue
			}
			return
		}
		interval = nextRuntimeAuthRecoveryInterval(interval, err, maxInterval, hungInterval)
	}
}

func runtimeAuthRecoveryIntervals() (initial, maxInterval, hung time.Duration) {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	return runtimeAuthRecoveryInitialInterval, runtimeAuthRecoveryMaxInterval, runtimeAuthRecoveryHungInterval
}

// The ErrorType prefix check keeps the loop owning every auth-shaped
// demotion variant without enumerating them.
func runtimeAuthRecoveryStillNeeded(contextName string) bool {
	status := GetConnectionStatus()
	return status.State == StateDisconnected &&
		status.Context == contextName &&
		strings.HasPrefix(status.ErrorType, "auth")
}

func nextRuntimeAuthRecoveryInterval(current time.Duration, err error, maxInterval, hungInterval time.Duration) time.Duration {
	if ClassifyError(err) == "timeout" && UsesExecAuth() {
		return hungInterval
	}
	return min(current*2, maxInterval)
}

func getRuntimeAuthReconnect() func(string) error {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	return runtimeAuthReconnect
}

func setRuntimeAuthReconnect(fn func(string) error) {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	runtimeAuthReconnect = fn
}
