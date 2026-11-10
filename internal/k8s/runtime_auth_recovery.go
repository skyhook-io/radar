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
	defaultRuntimeAuthRecoveryInitialInterval = 5 * time.Second
	defaultRuntimeAuthRecoveryMaxInterval     = 5 * time.Minute
	// A hung exec plugin leaks a blocked goroutine per probe (client-go
	// serializes plugin invocations process-wide, so at most one child
	// process accumulates), and probing rarely bounds the leak while still
	// self-healing.
	defaultRuntimeAuthRecoveryHungInterval = 30 * time.Minute
)

var (
	runtimeAuthRecoveryInitialInterval = defaultRuntimeAuthRecoveryInitialInterval
	runtimeAuthRecoveryMaxInterval     = defaultRuntimeAuthRecoveryMaxInterval
	runtimeAuthRecoveryHungInterval    = defaultRuntimeAuthRecoveryHungInterval

	runtimeAuthRecoveryActive atomic.Bool
	// Explicit recovery debt. The live ConnectionStatus.ErrorType cannot carry
	// this: while credentials are dead it legitimately flips to non-auth
	// values (a hung-plugin retry classifies "timeout", Helm handlers mark the
	// cluster unreachable), and inferring exit from that string would kill the
	// only reconnect path headless deployments have. Set on any auth-shaped
	// disconnect, cleared only by a successful connect.
	runtimeAuthRecoveryOwed atomic.Bool
	// Wakes a sleeping worker so a new auth-loss episode isn't stuck waiting
	// out the previous episode's backoff — worst case the 30min hung-plugin
	// interval.
	runtimeAuthRecoveryNudge = make(chan struct{}, 1)
	// nil selects the production reconnect path; tests replace it to isolate
	// recovery-loop timing and retry behavior.
	runtimeAuthReconnect func(string, uint64) error

	// Zero means retry forever. The desktop app sets a limit: someone is at
	// the screen to click Retry, and each retry re-runs the credential
	// plugin, which for a browser sign-in opens a new tab.
	runtimeAuthRecoveryRetryLimit atomic.Int32
	// Failed automatic retries since the last connect, Retry, or switch.
	runtimeAuthRecoveryFailures atomic.Int32
	// Bumped by each reset, so a probe that started before a Retry or
	// switch doesn't count against the next attempt's budget.
	runtimeAuthRecoveryResets atomic.Uint64
)

// SetAutomaticRetryLimit stops automatic reconnects after limit consecutive
// failures, until the user retries or switches context. Desktop only. It
// applies only to contexts that use an exec credential plugin: retrying a
// static token or certificate runs nothing, and keeps picking up a re-login
// that rewrites the kubeconfig (oc login).
func SetAutomaticRetryLimit(limit int) {
	runtimeAuthRecoveryRetryLimit.Store(int32(limit))
}

func automaticRetriesStopped() bool {
	limit := runtimeAuthRecoveryRetryLimit.Load()
	return limit > 0 && runtimeAuthRecoveryFailures.Load() >= limit
}

// resetAutomaticRetries is called when the user retries or switches context,
// and on every successful connect.
func resetAutomaticRetries() {
	runtimeAuthRecoveryResets.Add(1)
	runtimeAuthRecoveryFailures.Store(0)
}

// startRuntimeAuthRecovery keeps a headless deployment (MCP-only, no browser
// tab) from wedging permanently after an auth failure: nothing else would
// ever retry once the user restores credentials, so the server re-probes on a
// backoff and reconnects itself. Browser sessions benefit too — the reconnect
// surfaces through SSE and the status poll. The worker reads its target from
// the live connection status, so one worker serves consecutive episodes.
func startRuntimeAuthRecovery() {
	if automaticRetriesStopped() {
		return
	}
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
	defer func() {
		runtimeAuthRecoveryActive.Store(false)
		// A demotion racing this exit saw the active flag still true and only
		// nudged; re-check so its episode isn't stranded without a worker.
		if runtimeAuthRecoveryStillOwed() {
			startRuntimeAuthRecovery()
		}
	}()
	interval, _, _ := runtimeAuthRecoveryIntervals()
	for {
		// Re-read each pass: a worker can outlive the episode that started it
		// and adopt the next one, which must not inherit a stale scale.
		initialInterval, maxInterval, hungInterval := runtimeAuthRecoveryIntervals()
		select {
		case <-time.After(interval):
		case <-runtimeAuthRecoveryNudge:
			// A nudge marks a new auth-loss episode; don't make it wait out
			// the previous episode's backoff.
			interval = initialInterval
		}
		if !runtimeAuthRecoveryOwed.Load() {
			return
		}
		status := GetConnectionStatus()
		if status.State == StateConnected {
			// The publish-side hook clears the debt on connect; this is a
			// guard against direct status writes that bypass it.
			runtimeAuthRecoveryOwed.Store(false)
			return
		}
		if status.State == StateConnecting || activeContextOperations.Load() != 0 {
			// A connect attempt owns the moment (user-driven switch or retry);
			// its failure republishes a disconnected status and the debt
			// persists, so check again soon. Don't re-sleep the accumulated
			// backoff (worst case 30min): if the operation's failure publish
			// is byte-identical to the current status, the dedupe in
			// SetConnectionStatus means no nudge will arrive.
			interval = initialInterval
			continue
		}
		contextName := status.Context
		observedGen := currentOperationGen()
		observedResets := runtimeAuthRecoveryResets.Load()
		ctx, cancel := context.WithTimeout(context.Background(), connectionTestOperationTimeout())
		err := getRuntimeAuthProbe()(ctx)
		cancel()
		if err != nil && isAuthClassification(ClassifyError(err)) && runtimeAuthCredentialsAreStatic() {
			// Probing the in-memory config can never observe new INLINE
			// credentials (token:/client-certificate-data: are read once at
			// connect), but a full reconnect re-reads the kubeconfig from
			// disk. Without this, users whose re-login rewrites the file
			// (oc login's daily tokens, a fresh k3s.yaml) never self-heal —
			// the pre-recovery-loop browser retry re-read disk every attempt.
			switchErr := getRuntimeAuthReconnect()(contextName, observedGen)
			if switchErr == nil {
				return
			}
			if !errors.Is(switchErr, ErrReconnectSuperseded) {
				err = switchErr
			}
		}
		if err != nil {
			if recordAutomaticRetryFailure(observedResets) {
				return
			}
			interval = nextRuntimeAuthRecoveryInterval(interval, err, maxInterval, hungInterval)
			continue
		}
		log.Printf("[k8s] Credentials restored for context %q; reconnecting automatically", SanitizeForLog(contextName))
		switchErr := getRuntimeAuthReconnect()(contextName, observedGen)
		if errors.Is(switchErr, ErrReconnectSuperseded) {
			// A user operation won the race; its outcome decides whether the
			// debt persists.
			continue
		}
		if switchErr != nil {
			log.Printf("[k8s] Automatic reconnect failed for context %q: %v", SanitizeForLog(contextName), switchErr)
			if recordAutomaticRetryFailure(observedResets) {
				return
			}
			interval = nextRuntimeAuthRecoveryInterval(interval, switchErr, maxInterval, hungInterval)
			continue
		}
		// performContextSwitch published the connected status (and cleared
		// the debt) while still holding contextOpMu.
		return
	}
}

// recordAutomaticRetryFailure counts a failed automatic retry and reports
// whether automatic retries have now stopped.
func recordAutomaticRetryFailure(observedResets uint64) bool {
	if runtimeAuthRecoveryResets.Load() != observedResets || !UsesExecAuth() {
		return false
	}
	runtimeAuthRecoveryFailures.Add(1)
	if !automaticRetriesStopped() {
		return false
	}
	log.Printf("[k8s] Stopped automatic reconnects after %d failed attempts; waiting for Retry", runtimeAuthRecoveryFailures.Load())
	publishAutomaticRetriesStopped()
	return true
}

func runtimeAuthRecoveryStillOwed() bool {
	return runtimeAuthRecoveryOwed.Load() && GetConnectionStatus().State != StateConnected
}

// RuntimeAuthRecoveryOwed reports whether the server-side reconnect loop owns
// the current disconnect. The browser reads this to suppress its own auto-retry
// for the whole episode — the live ErrorType can flip to non-auth values while
// credentials are dead, and browser retries would re-invoke the exec plugin on
// a much shorter cadence than the loop's backoff.
func RuntimeAuthRecoveryOwed() bool {
	return runtimeAuthRecoveryOwed.Load()
}

// Static means nothing in-process can mint fresh credentials: no exec plugin
// to re-run, no auth provider to refresh, no token/cert file client-go
// re-reads. Only a kubeconfig re-read (a full reconnect) can pick up new ones.
func runtimeAuthCredentialsAreStatic() bool {
	config := GetConfig()
	if config == nil {
		return false
	}
	return config.ExecProvider == nil &&
		config.AuthProvider == nil &&
		config.BearerTokenFile == "" &&
		config.TLSClientConfig.CertFile == ""
}

func nextRuntimeAuthRecoveryInterval(current time.Duration, err error, maxInterval, hungInterval time.Duration) time.Duration {
	// The long interval is strictly for a wedged exec plugin (its marker is
	// the exec-specific deadline branch) — a generic timeout under exec auth,
	// e.g. a subsystem-init deadline during a reconnect whose credentials
	// already worked, must keep the normal backoff or a transient stall
	// costs headless deployments half an hour.
	if err != nil && UsesExecAuth() && ClassifyError(err) == "timeout" &&
		strings.Contains(strings.ToLower(err.Error()), "auth plugin timeout") {
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
	if runtimeAuthReconnect == nil {
		return reconnectRuntimeAuth
	}
	return runtimeAuthReconnect
}

func reconnectRuntimeAuth(contextName string, observedOperationGen uint64) error {
	if IsInCluster() {
		return reinitializeCurrentContextIfOperationCurrent(contextName, observedOperationGen, InitAllSubsystems)
	}
	return PerformContextSwitchIfOperationCurrent(contextName, observedOperationGen)
}

func setRuntimeAuthReconnect(fn func(string, uint64) error) {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	runtimeAuthReconnect = fn
}
