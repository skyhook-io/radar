package k8s

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ContextSwitchTimeout is the maximum time allowed for a context switch operation
const ContextSwitchTimeout = 30 * time.Second

// ConnectionTestTimeout is the maximum time allowed for initial connection test
// This is a short timeout for quick fail detection
const ConnectionTestTimeout = 5 * time.Second

// ContextSwitchCallback is called when the context is switched
type ContextSwitchCallback func(newContext string)

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

	// operationCtx is canceled at the start of every context switch and retry.
	// API calls that should not survive a context switch (RBAC checks, capability
	// probes) derive their context from this instead of context.Background().
	operationCtx    context.Context
	operationCancel context.CancelFunc
	operationMu     sync.Mutex
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

// OnContextSwitch registers a callback to be called when the context is switched
func OnContextSwitch(callback ContextSwitchCallback) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	contextSwitchCallbacks = append(contextSwitchCallbacks, callback)
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
func PerformContextSwitch(newContext string) error {
	switchStart := time.Now()
	log.Printf("[ops] Context switch START → %q", newContext)

	// Cancel any in-flight API calls from the previous context (RBAC checks,
	// capability probes, etc.) so they don't serialize through the old exec
	// plugin and block the new context's connectivity test.
	CancelOngoingOperations()

	// Step 1: Tear down all subsystems
	reportProgress("Stopping caches...")
	t := time.Now()
	ResetAllSubsystems()
	logTiming("   [ops] ResetAllSubsystems: %v", time.Since(t))

	// Step 2: Switch the K8s client to the new context
	reportProgress("Connecting to cluster...")
	t = time.Now()
	log.Printf("Switching K8s client to context %q...", newContext)
	if err := SwitchContext(newContext); err != nil {
		log.Printf("[ops] Context switch FAILED at SwitchContext: %v (%v)", err, time.Since(switchStart))
		return fmt.Errorf("failed to switch context: %w", err)
	}
	logTiming("   [ops] SwitchContext: %v", time.Since(t))

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
		log.Printf("[ops] Context switch FAILED at connectivity test: %v (%v since switch start)", err, time.Since(switchStart))
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
		log.Printf("[ops] Context switch FAILED at subsystem init: %v (%v since switch start)", err, time.Since(switchStart))
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
