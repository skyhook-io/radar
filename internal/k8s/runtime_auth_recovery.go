package k8s

import (
	"context"
	"errors"
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
	// Wakes a sleeping worker so a new auth-loss episode (possibly on a
	// different context) isn't stuck waiting out the previous episode's
	// backoff — worst case the 30min hung-plugin interval.
	runtimeAuthRecoveryNudge = make(chan struct{}, 1)
	runtimeAuthReconnect     = PerformContextSwitchIfOperationCurrent
)

// startRuntimeAuthRecovery keeps a headless deployment (MCP-only, no browser
// tab) from wedging permanently after an auth demotion: nothing else would
// ever retry once the user restores credentials, so the server re-probes on a
// backoff and reconnects itself. Browser sessions benefit too — the reconnect
// surfaces through SSE and the status poll. The worker reads its target from
// the live connection status, so one worker serves consecutive episodes.
func startRuntimeAuthRecovery() {
	if !runtimeAuthRecoveryActive.CompareAndSwap(false, true) {
		select {
		case runtimeAuthRecoveryNudge <- struct{}{}:
		default:
		}
		return
	}
	go runRuntimeAuthRecovery()
}

func runRuntimeAuthRecovery() {
	defer runtimeAuthRecoveryActive.Store(false)
	initialInterval, maxInterval, hungInterval := runtimeAuthRecoveryIntervals()
	interval := initialInterval
	lastContext := ""
	for {
		select {
		case <-time.After(interval):
		case <-runtimeAuthRecoveryNudge:
		}
		contextName, needed := runtimeAuthRecoveryTarget()
		if !needed {
			return
		}
		if contextName != lastContext {
			// New episode (different context demoted while this worker was
			// sleeping) — don't make it inherit the old episode's backoff.
			interval = initialInterval
			lastContext = contextName
		}
		if activeContextOperations.Load() != 0 {
			// A user-driven switch or retry is queued or running; let it win
			// this tick. If it fails, the demoted state persists and the loop
			// re-checks next tick.
			continue
		}
		observedGen := currentOperationGen()
		ctx, cancel := context.WithTimeout(context.Background(), connectionTestOperationTimeout())
		err := getRuntimeAuthProbe()(ctx)
		cancel()
		if err != nil {
			interval = nextRuntimeAuthRecoveryInterval(interval, err, maxInterval, hungInterval)
			continue
		}
		log.Printf("[k8s] Credentials restored for context %q; reconnecting automatically", SanitizeForLog(contextName))
		switchErr := getRuntimeAuthReconnect()(contextName, observedGen)
		if errors.Is(switchErr, ErrReconnectSuperseded) {
			// A user operation won the race; its outcome decides whether the
			// next tick still sees a demoted state.
			continue
		}
		if switchErr != nil {
			log.Printf("[k8s] Automatic reconnect failed for context %q: %v", SanitizeForLog(contextName), switchErr)
			interval = nextRuntimeAuthRecoveryInterval(interval, switchErr, maxInterval, hungInterval)
			continue
		}
		// PerformContextSwitch rebuilds clients and caches but leaves status
		// publication to its caller (the HTTP handlers do the same).
		SetConnectionStatus(ConnectionStatus{
			State:       StateConnected,
			Context:     GetContextName(),
			ClusterName: GetClusterName(),
		})
		return
	}
}

// The ErrorType prefix check keeps the loop owning every auth-shaped
// demotion variant without enumerating them.
func runtimeAuthRecoveryTarget() (string, bool) {
	status := GetConnectionStatus()
	if status.State != StateDisconnected || !strings.HasPrefix(status.ErrorType, "auth") {
		return "", false
	}
	return status.Context, true
}

func nextRuntimeAuthRecoveryInterval(current time.Duration, err error, maxInterval, hungInterval time.Duration) time.Duration {
	if ClassifyError(err) == "timeout" && UsesExecAuth() {
		return hungInterval
	}
	return min(current*2, maxInterval)
}

func runtimeAuthRecoveryIntervals() (initial, maxInterval, hung time.Duration) {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	return runtimeAuthRecoveryInitialInterval, runtimeAuthRecoveryMaxInterval, runtimeAuthRecoveryHungInterval
}

func getRuntimeAuthReconnect() func(string, uint64) error {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	return runtimeAuthReconnect
}

func setRuntimeAuthReconnect(fn func(string, uint64) error) {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	runtimeAuthReconnect = fn
}
