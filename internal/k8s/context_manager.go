package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/errorlog"
)

// ContextSwitchTimeout is now defined in deadlines.go as a package variable
// so operators can override it via flag/env without recompiling. The default
// value (30 * time.Second) is preserved for backwards compatibility.

// ConnectionTestTimeout is the maximum time allowed for initial connection test
// This is a short timeout for quick fail detection
const ConnectionTestTimeout = 5 * time.Second

// ContextSwitchCallback is called when the context is switched
type ContextSwitchCallback func(newContext string)

// NamespaceRescopeCallback is called when the active cache namespace changes
// without switching kubeconfig contexts.
type NamespaceRescopeCallback func(namespace string)

// ContextSwitchProgressCallback is called with progress updates during context switch
type ContextSwitchProgressCallback func(message string)

// HelmResetFunc is called to reset the Helm client
type HelmResetFunc func()

// HelmReinitFunc is called to reinitialize the Helm client
type HelmReinitFunc func(kubeconfig string) error

// TimelineResetFunc is called to reset the timeline store
type TimelineResetFunc func()

// TimelineReinitFunc is called to reinitialize the timeline store
// Returns error if reinitialization fails
type TimelineReinitFunc func() error

// TrafficResetFunc is called to reset the traffic manager
type TrafficResetFunc func()

// TrafficReinitFunc is called to reinitialize the traffic manager
// Returns error if reinitialization fails
type TrafficReinitFunc func() error

// PrometheusResetFunc is called to reset the Prometheus metrics client
type PrometheusResetFunc func()

// PrometheusReinitFunc is called to reinitialize the Prometheus metrics client
type PrometheusReinitFunc func() error

var (
	contextSwitchCallbacks         []ContextSwitchCallback
	namespaceRescopeCallbacks      []NamespaceRescopeCallback
	contextSwitchProgressCallbacks []ContextSwitchProgressCallback
	contextSwitchMu                sync.RWMutex
	helmResetFunc                  HelmResetFunc
	helmReinitFunc                 HelmReinitFunc
	timelineResetFunc              TimelineResetFunc
	timelineReinitFunc             TimelineReinitFunc
	trafficResetFunc               TrafficResetFunc
	trafficReinitFunc              TrafficReinitFunc
	prometheusResetFunc            PrometheusResetFunc
	prometheusReinitFunc           PrometheusReinitFunc
	// sessionStopFunc terminates active port-forward / exec sessions. Invoked at
	// the point of no return — immediately before the cache is torn down — so a
	// pre-teardown failure (connectivity test, scope-target validation) doesn't
	// kill the user's sessions for an operation that never changed the cache.
	sessionStopFunc func()

	// contextOpMu serializes the destructive context-changing operations
	// (PerformContextSwitch, PerformNamespaceRescope). operationGen only decides
	// which operation gets to roll back / notify; it does NOT stop two operations
	// from running ResetAllSubsystems + InitAllSubsystems concurrently on the
	// shared cache singletons. This mutex does — a second request waits for the
	// first to finish rather than interleaving teardown/init.
	contextOpMu sync.Mutex

	// operationCtx is canceled at the start of every context switch and retry.
	// API calls that should not survive a context switch (RBAC checks, capability
	// probes) derive their context from this instead of context.Background().
	operationCtx    context.Context
	operationCancel context.CancelFunc
	operationMu     sync.Mutex
	// operationGen bumps on every CancelOngoingOperations (i.e. at the start of
	// every context switch / rescope). An operation captures the generation it
	// owns; if a newer operation has since started, the older one must not apply
	// destructive side effects (e.g. a namespace-rescope rollback against the
	// context a newer switch already moved to).
	operationGen uint64
)

func init() {
	operationCtx, operationCancel = context.WithCancel(context.Background())
}

// CancelOngoingOperations cancels any in-flight API calls from previous
// operations (capabilities checks, RBAC checks, etc.) and creates a fresh
// operation context. Called at the start of context switch and retry.
func CancelOngoingOperations() {
	operationMu.Lock()
	defer operationMu.Unlock()
	log.Printf("[ops] Canceling ongoing operations (previous API calls will be interrupted)")
	operationCancel()
	operationCtx, operationCancel = context.WithCancel(context.Background())
	operationGen++
}

// currentOperationGen returns the generation of the most recently started
// operation. Compare against a captured generation to detect supersession.
func currentOperationGen() uint64 {
	operationMu.Lock()
	defer operationMu.Unlock()
	return operationGen
}

// NewOperationContext returns a context derived from the current operation
// context with the given timeout. Use this instead of context.Background()
// for API calls that should be canceled on context switch.
func NewOperationContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	operationMu.Lock()
	parent := operationCtx
	operationMu.Unlock()
	return context.WithTimeout(parent, timeout)
}

// OperationContext returns the current operation context. Callers that need
// WithCancel semantics (instead of WithTimeout) should derive from this.
func OperationContext() context.Context {
	operationMu.Lock()
	defer operationMu.Unlock()
	return operationCtx
}

// SetSessionStopper registers the callback that terminates active port-forward /
// exec sessions. The destructive cache operations call it once they commit to
// tearing the cache down. Registered by the server (which owns sessions) to
// avoid a server→k8s import cycle.
func SetSessionStopper(fn func()) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	sessionStopFunc = fn
}

func stopActiveSessions() {
	contextSwitchMu.RLock()
	fn := sessionStopFunc
	contextSwitchMu.RUnlock()
	if fn != nil {
		fn()
	}
}

// OnContextSwitch registers a callback to be called when the context is switched
func OnContextSwitch(callback ContextSwitchCallback) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	contextSwitchCallbacks = append(contextSwitchCallbacks, callback)
}

// OnNamespaceRescope registers a callback for local --namespace-scope cache
// rescope completion.
func OnNamespaceRescope(callback NamespaceRescopeCallback) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	namespaceRescopeCallbacks = append(namespaceRescopeCallbacks, callback)
}

// OnContextSwitchProgress registers a callback for progress updates during context switch
func OnContextSwitchProgress(callback ContextSwitchProgressCallback) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	contextSwitchProgressCallbacks = append(contextSwitchProgressCallbacks, callback)
}

// reportProgress notifies all registered progress callbacks
func reportProgress(message string) {
	contextSwitchMu.RLock()
	callbacks := make([]ContextSwitchProgressCallback, len(contextSwitchProgressCallbacks))
	copy(callbacks, contextSwitchProgressCallbacks)
	contextSwitchMu.RUnlock()

	for _, callback := range callbacks {
		callback(message)
	}
}

// RegisterHelmFuncs registers the Helm reset/reinit functions
// This breaks the import cycle by allowing helm package to register its functions
func RegisterHelmFuncs(reset HelmResetFunc, reinit HelmReinitFunc) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	helmResetFunc = reset
	helmReinitFunc = reinit
}

// RegisterTimelineFuncs registers the timeline store reset/reinit functions
// This breaks the import cycle by allowing main to register timeline functions
func RegisterTimelineFuncs(reset TimelineResetFunc, reinit TimelineReinitFunc) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	timelineResetFunc = reset
	timelineReinitFunc = reinit
}

// RegisterTrafficFuncs registers the traffic manager reset/reinit functions
// This breaks the import cycle by allowing main to register traffic functions
func RegisterTrafficFuncs(reset TrafficResetFunc, reinit TrafficReinitFunc) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	trafficResetFunc = reset
	trafficReinitFunc = reinit
}

// RegisterPrometheusFuncs registers the Prometheus client reset/reinit functions.
func RegisterPrometheusFuncs(reset PrometheusResetFunc, reinit PrometheusReinitFunc) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	prometheusResetFunc = reset
	prometheusReinitFunc = reinit
}

// TestClusterConnection tests connectivity to the current cluster.
// Returns an error if the cluster is unreachable within the timeout.
//
// The API call runs in a goroutine with a select on ctx.Done() to guarantee
// prompt return. client-go's exec credential plugins don't propagate
// per-request context cancellation, so Do(ctx) alone can block indefinitely
// while the plugin retries expired credentials.
func TestClusterConnection(ctx context.Context) error {
	config := GetConfig()
	if config == nil {
		return fmt.Errorf("K8s config not initialized")
	}

	// Create a copy of the config with a short timeout
	// rest.CopyConfig properly copies all fields including TLS settings
	testConfig := rest.CopyConfig(config)
	testConfig.Timeout = ConnectionTestTimeout

	// Create a temporary client with the short-timeout config
	testClient, err := kubernetes.NewForConfig(testConfig)
	if err != nil {
		return fmt.Errorf("failed to create test client: %w", err)
	}

	// Run the API call in a goroutine so we can select on ctx.Done().
	// This guarantees we return when the context expires even if the exec
	// credential plugin is blocking (it doesn't respect request context).
	resultCh := make(chan error, 1)
	go func() {
		_, err := testClient.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
		resultCh <- err
	}()

	select {
	case err := <-resultCh:
		if err != nil {
			return fmt.Errorf("cluster unreachable: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("cluster unreachable: %w", ctx.Err())
	}
}

// PerformContextSwitch orchestrates a full context switch:
// 1. Tears down all subsystems
// 2. Switches the K8s client to the new context
// 3. Tests connectivity to ensure cluster is reachable
// 4. Reinitializes all subsystems (same sequence as initial boot)
// 5. Notifies all registered callbacks
// ErrContextSwitchPreflight is returned by PerformContextSwitch when the switch
// is rejected BEFORE any teardown (e.g. --namespace-scope with no usable target
// for the new context). Callers must treat it as "request rejected, still
// connected to the current cluster" and NOT mark the connection disconnected.
var ErrContextSwitchPreflight = errors.New("context switch preflight rejected")

func PerformContextSwitch(newContext string) error {
	switchStart := time.Now()
	log.Printf("[ops] Context switch START → %q", newContext)

	// Serialize against any other in-flight switch / rescope so their teardown +
	// init can't interleave on the shared cache. A queued request waits here.
	contextOpMu.Lock()
	defer contextOpMu.Unlock()

	// Under --namespace-scope, validate the new context has a usable scope target
	// BEFORE tearing anything down. Otherwise a switch to a context with no
	// namespace (and no saved pick) would reset and repoint the client, then fail
	// at requireNamespaceScopeTarget below — leaving us on the new context with no
	// informer caches until a full reconnect succeeds. This is a preflight failure
	// (nothing torn down yet) so the caller must keep the current connection intact.
	if ForceNamespaceScope && ProspectiveNamespaceScopeTarget(newContext) == "" {
		return fmt.Errorf("%w: --namespace-scope requires --namespace, a namespace on context %q, or a saved namespace pick", ErrContextSwitchPreflight, newContext)
	}

	// Cancel any in-flight API calls from the previous context (RBAC checks,
	// capability probes, etc.) so they don't serialize through the old exec
	// plugin and block the new context's connectivity test.
	CancelOngoingOperations()

	// Step 1: Tear down all subsystems. Stop sessions first — past this point the
	// previous cluster's caches are gone, so lingering port-forwards / exec
	// terminals would be reading state we can no longer serve.
	stopActiveSessions()
	reportProgress("Stopping caches...")
	t := time.Now()
	ResetAllSubsystems()
	logTiming("   [ops] ResetAllSubsystems: %v", time.Since(t))

	// Step 2: Switch the K8s client to the new context
	reportProgress("Connecting to cluster...")
	t = time.Now()
	log.Printf("Switching K8s client to context %q...", newContext)
	if err := SwitchContext(newContext); err != nil {
		elapsed := time.Since(switchStart).Truncate(time.Millisecond)
		log.Printf("[ops] Context switch FAILED at SwitchContext: %v (%v since switch start)", err, elapsed)
		errorlog.Record("context-switch", "error",
			"stage=SwitchContext target=%q elapsed=%v: %v", newContext, elapsed, err)
		return fmt.Errorf("failed to switch context: %w", err)
	}
	logTiming("   [ops] SwitchContext: %v", time.Since(t))
	ClearNamespaceScopeOverride()
	RestoreNamespaceScopePreference(GetContextName())
	if err := requireNamespaceScopeTarget(newContext); err != nil {
		return err
	}

	// Invalidate caches - permissions and cluster info may differ between clusters
	InvalidateCapabilitiesCache()
	InvalidateResourcePermissionsCache()
	InvalidateServerVersionCache()

	// Step 3: Test connectivity before proceeding with initialization.
	// Contexts are derived from the operation context so they're canceled
	// if another context switch starts while this one is in progress.
	reportProgress("Testing cluster connectivity...")
	t = time.Now()
	log.Println("Testing cluster connectivity...")
	connCtx, connCancel := NewOperationContext(ConnectionTestTimeout)
	defer connCancel()
	if err := TestClusterConnection(connCtx); err != nil {
		elapsed := time.Since(switchStart).Truncate(time.Millisecond)
		log.Printf("[ops] Context switch FAILED at connectivity test: %v (%v since switch start)", err, elapsed)
		errorlog.Record("context-switch", "error",
			"stage=TestClusterConnection target=%q elapsed=%v: %v", newContext, elapsed, err)
		return fmt.Errorf("cluster connection failed: %w", err)
	}
	log.Printf("[ops] Cluster connectivity verified (%v)", time.Since(t))

	// Step 4: Initialize all subsystems (same function as initial boot).
	// Teardown above is non-blocking, so old informers may still be draining.
	// Use a timeout to prevent context switch from hanging indefinitely.
	t = time.Now()
	initCtx, initCancel := NewOperationContext(ContextSwitchTimeout)
	defer initCancel()
	if err := InitAllSubsystems(initCtx, reportProgress); err != nil {
		elapsed := time.Since(switchStart).Truncate(time.Millisecond)
		log.Printf("[ops] Context switch FAILED at subsystem init: %v (%v since switch start)", err, elapsed)
		errorlog.Record("context-switch", "error",
			"stage=InitAllSubsystems target=%q elapsed=%v: %v", newContext, elapsed, err)
		return fmt.Errorf("subsystem init failed: %w", err)
	}
	logTiming("   [ops] InitAllSubsystems: %v", time.Since(t))

	// Step 5: Notify all registered callbacks
	reportProgress("Building topology...")
	log.Printf("[ops] Context switch to %q COMPLETE (%v total)", newContext, time.Since(switchStart))
	contextSwitchMu.RLock()
	callbacks := make([]ContextSwitchCallback, len(contextSwitchCallbacks))
	copy(callbacks, contextSwitchCallbacks)
	contextSwitchMu.RUnlock()

	for _, callback := range callbacks {
		callback(newContext)
	}

	return nil
}

func requireNamespaceScopeTarget(contextName string) error {
	if !ForceNamespaceScope || GetNamespaceScopeTarget() != "" {
		return nil
	}
	return fmt.Errorf("--namespace-scope requires --namespace, a namespace on context %q, or a saved namespace pick", contextName)
}

// PerformNamespaceRescope rebuilds all cache-backed subsystems for a new
// namespace while keeping the current kubeconfig context. It is intended for
// local --namespace-scope sessions only; multi-user deployments must not let
// one user's namespace pick reshape the process-wide cache for everyone.
func PerformNamespaceRescope(namespace string) error {
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	// Serialize against any other in-flight switch / rescope (see contextOpMu).
	// previousNamespace is read under the lock so two rescopes can't both decide
	// the namespace changed and run teardown/init concurrently.
	contextOpMu.Lock()
	defer contextOpMu.Unlock()
	previousNamespace := GetNamespaceScopeTarget()
	if namespace == previousNamespace {
		return nil
	}

	rescopeStart := time.Now()
	safeNamespace := SanitizeForLog(namespace)
	safePreviousNamespace := SanitizeForLog(previousNamespace)
	log.Printf("[ops] Namespace rescope START → %q", safeNamespace)

	CancelOngoingOperations()
	myGen := currentOperationGen()
	reportProgress("Testing cluster connectivity...")
	t := time.Now()
	connCtx, connCancel := NewOperationContext(ConnectionTestTimeout)
	defer connCancel()
	if err := TestClusterConnection(connCtx); err != nil {
		elapsed := time.Since(rescopeStart).Truncate(time.Millisecond)
		log.Printf("[ops] Namespace rescope FAILED at connectivity test: %v (%v since rescope start)", err, elapsed)
		errorlog.Record("namespace-rescope", "error",
			"stage=TestClusterConnection namespace=%q elapsed=%v: %v", safeNamespace, elapsed, err)
		return fmt.Errorf("cluster connection failed: %w", err)
	}
	log.Printf("[ops] Cluster connectivity verified (%v)", time.Since(t))

	// Connectivity passed, so we're committed to rebuilding the cache for the new
	// namespace. Stop sessions now (not before the connectivity test) so a failed
	// pre-rescope check doesn't tear down port-forwards / exec for nothing.
	stopActiveSessions()
	if err := reinitializeNamespaceScope(namespace, "Stopping caches..."); err != nil {
		elapsed := time.Since(rescopeStart).Truncate(time.Millisecond)
		log.Printf("[ops] Namespace rescope FAILED at subsystem init: %v (%v since rescope start)", err, elapsed)
		errorlog.Record("namespace-rescope", "error",
			"stage=InitAllSubsystems namespace=%q elapsed=%v: %v", safeNamespace, elapsed, err)
		// If a newer context switch / rescope started while our init was running,
		// it canceled our operation context (hence this failure) and now owns the
		// cache state. Rolling back here would reset the newer operation's
		// subsystems and pin the cache to OUR previous namespace — clobbering it.
		// Bail without rolling back; the newer operation is authoritative.
		if currentOperationGen() != myGen {
			log.Printf("[ops] Namespace rescope to %q superseded by a newer operation; skipping rollback", safeNamespace)
			return fmt.Errorf("namespace rescope to %q superseded by a newer operation", namespace)
		}
		if rollbackErr := reinitializeNamespaceScope(previousNamespace, "Restoring previous namespace..."); rollbackErr != nil {
			log.Printf("[ops] Namespace rescope rollback to %q FAILED: %v", safePreviousNamespace, rollbackErr)
			errorlog.Record("namespace-rescope", "error",
				"stage=Rollback namespace=%q elapsed=%v: %v", safePreviousNamespace, time.Since(rescopeStart).Truncate(time.Millisecond), rollbackErr)
			return fmt.Errorf("subsystem init failed: %w; rollback to previous namespace %q failed: %v", err, previousNamespace, rollbackErr)
		}
		notifyNamespaceRescopeCallbacks(previousNamespace)
		return fmt.Errorf("subsystem init failed: %w; restored previous namespace %q", err, previousNamespace)
	}

	// A newer operation may have started after our init completed; it now owns
	// the cache, so don't announce our (now-stale) namespace to callbacks.
	if currentOperationGen() != myGen {
		log.Printf("[ops] Namespace rescope to %q superseded by a newer operation after init", safeNamespace)
		return fmt.Errorf("namespace rescope to %q superseded by a newer operation", namespace)
	}

	reportProgress("Building topology...")
	log.Printf("[ops] Namespace rescope to %q COMPLETE (%v total)", safeNamespace, time.Since(rescopeStart))
	notifyNamespaceRescopeCallbacks(namespace)

	return nil
}

func reinitializeNamespaceScope(namespace, resetMessage string) error {
	reportProgress(resetMessage)
	t := time.Now()
	ResetAllSubsystems()
	logTiming("   [ops] ResetAllSubsystems: %v", time.Since(t))

	SetNamespaceScopeOverride(namespace)
	InvalidateCapabilitiesCache()
	InvalidateResourcePermissionsCache()
	InvalidateServerVersionCache()

	t = time.Now()
	initCtx, initCancel := NewOperationContext(ContextSwitchTimeout)
	defer initCancel()
	if err := InitAllSubsystems(initCtx, reportProgress); err != nil {
		return err
	}
	logTiming("   [ops] InitAllSubsystems: %v", time.Since(t))
	return nil
}

func notifyNamespaceRescopeCallbacks(namespace string) {
	contextSwitchMu.RLock()
	callbacks := make([]NamespaceRescopeCallback, len(namespaceRescopeCallbacks))
	copy(callbacks, namespaceRescopeCallbacks)
	contextSwitchMu.RUnlock()

	for _, callback := range callbacks {
		callback(namespace)
	}
}
