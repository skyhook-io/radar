package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/errorlog"
)

// ContextSwitchTimeout is now defined in deadlines.go as a package variable
// so operators can override it via flag/env without recompiling. The default
// value (30 * time.Second) is preserved for backwards compatibility.

// ConnectionTestTimeout is the maximum time allowed for non-exec-auth
// connection tests. This is a short timeout for quick fail detection.
const ConnectionTestTimeout = 5 * time.Second
const execAuthConnectionProbeHTTPTimeout = 10 * time.Second
const connectionProbeTimeoutHeadroom = time.Second

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

type CostResetFunc func()

var (
	beforeContextSwitchCallbacks   []ContextSwitchCallback
	contextSwitchCallbacks         []func(string, string)
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
	costResetFunc                  CostResetFunc
	// sessionStopFunc terminates active port-forward / exec sessions. Invoked at
	// the point of no return — immediately before the cache is torn down — so a
	// pre-teardown failure (connectivity test, scope-target validation) doesn't
	// kill the user's sessions for an operation that never changed the cache.
	sessionStopFunc func()

	// contextOpMu serializes the destructive context-changing operations
	// (PerformContextSwitch, PerformNamespaceRescope, and runtime auth-loss
	// demotion). operationGen only decides which operation gets to roll back /
	// notify; it does NOT stop two operations from running ResetAllSubsystems +
	// InitAllSubsystems concurrently on the shared cache singletons. This mutex
	// does — a second request waits for the first to finish rather than
	// interleaving teardown/init.
	contextOpMu sync.RWMutex
	// Incremented BEFORE contextOpMu is acquired — that ordering is the
	// mechanism: it makes a queued-but-blocked operation visible to runtime
	// auth-loss candidate intake, which a try-lock could never see.
	activeContextOperations atomic.Int32

	// operationCtx is canceled at the start of every context switch and retry.
	// API calls that should not survive a context switch (RBAC checks, capability
	// probes) derive their context from this instead of context.Background().
	operationCtx    context.Context
	operationCancel context.CancelFunc
	operationMu     sync.Mutex
	// operationGen bumps on every CancelOngoingOperations (i.e. at the start of
	// every context switch / rescope, and on runtime auth-loss demotion). An
	// operation captures the generation it owns; if a newer operation has since
	// started, the older one must not apply destructive side effects (e.g. a
	// namespace-rescope rollback against the context a newer switch already
	// moved to).
	operationGen uint64
)

func init() {
	operationCtx, operationCancel = context.WithCancel(context.Background())
}

// CancelOngoingOperations cancels any in-flight API calls from previous
// operations (capabilities checks, RBAC checks, etc.) and creates a fresh
// operation context. Called at the start of context switch and retry, and by
// runtime auth-loss demotion.
func CancelOngoingOperations() (context.Context, uint64) {
	operationMu.Lock()
	defer operationMu.Unlock()
	operationCancel()
	operationCtx, operationCancel = context.WithCancel(context.Background())
	operationGen++
	return operationCtx, operationGen
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

func newOperationContextForGeneration(generation uint64, timeout time.Duration) (context.Context, context.CancelFunc, bool) {
	operationMu.Lock()
	defer operationMu.Unlock()
	if operationGen != generation {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(operationCtx, timeout)
	return ctx, cancel, true
}

func ContextOperationInProgress() bool { return activeContextOperations.Load() != 0 }

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

// OnBeforeContextSwitch registers a callback fired at the very start of
// PerformContextSwitch, BEFORE the client is repointed at the new cluster — for
// teardown that must happen against the old context (e.g. cancelling in-flight
// AI investigations so their agent can't touch the new cluster). Runtime
// auth-loss demotion also fires it as a quiesce-in-place: no switch follows and
// the argument is the CURRENT context, so callbacks must not infer "changed"
// by comparing it against the active context name.
func OnBeforeContextSwitch(callback ContextSwitchCallback) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	beforeContextSwitchCallbacks = append(beforeContextSwitchCallbacks, callback)
}

// OnContextSwitch registers a callback to be called when the context is switched
func OnContextSwitch(callback ContextSwitchCallback) {
	OnContextSwitchComplete(func(name, _ string) { callback(name) })
}

// OnContextSwitchComplete includes the operation ID for a tracked user switch.
func OnContextSwitchComplete(callback func(name, switchID string)) {
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

// notifyBeforeContextSwitch fires before-switch callbacks (old context still active).
func notifyBeforeContextSwitch(newContext string) {
	contextSwitchMu.RLock()
	callbacks := make([]ContextSwitchCallback, len(beforeContextSwitchCallbacks))
	copy(callbacks, beforeContextSwitchCallbacks)
	contextSwitchMu.RUnlock()
	for _, callback := range callbacks {
		callback(newContext)
	}
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

// RestartTrafficSubsystem tears down and rebuilds the traffic manager for the
// current cluster, the way a context switch does, serialized with any switch
// or rescope in flight so the two can't interleave on the manager singleton.
// A live configuration change that the traffic sources copied at construction
// (the metrics URL and headers) reaches them only this way. No-op while
// disconnected: the next connect builds the manager from current config.
func RestartTrafficSubsystem() error {
	contextSwitchMu.RLock()
	resetFn, reinitFn := trafficResetFunc, trafficReinitFunc
	contextSwitchMu.RUnlock()
	if resetFn == nil || reinitFn == nil {
		return nil
	}
	activeContextOperations.Add(1)
	contextOpMu.Lock()
	defer func() {
		activeContextOperations.Add(-1)
		contextOpMu.Unlock()
	}()
	if GetClient() == nil {
		return nil
	}
	resetFn()
	return reinitFn()
}

// RegisterPrometheusFuncs registers the Prometheus client reset/reinit functions.
func RegisterPrometheusFuncs(reset PrometheusResetFunc, reinit PrometheusReinitFunc) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	prometheusResetFunc = reset
	prometheusReinitFunc = reinit
}

func RegisterCostResetFunc(reset CostResetFunc) {
	contextSwitchMu.Lock()
	defer contextSwitchMu.Unlock()
	costResetFunc = reset
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
	return testConnection(ctx, config, UsesExecAuth())
}

// testConnection runs TestClusterConnection's probe against any config, so a
// context can be checked before it becomes the active one.
func testConnection(ctx context.Context, config *rest.Config, usesExec bool) error {
	// Create a copy of the config with a short timeout
	// rest.CopyConfig properly copies all fields including TLS settings
	testConfig := rest.CopyConfig(config)
	testConfig.Timeout = connectionProbeHTTPTimeout(ctx)

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
		if !usesExec {
			return fmt.Errorf("cluster unreachable: %w", ctx.Err())
		}
		return fmt.Errorf("auth plugin timeout: %w", ctx.Err())
	}
}

func connectionTestOperationTimeout() time.Duration {
	return connectionTestTimeoutFor(UsesExecAuth())
}

func connectionTestTimeoutFor(usesExec bool) time.Duration {
	if usesExec {
		return execAuthConnectionProbeHTTPTimeout + connectionProbeTimeoutHeadroom
	}
	return ConnectionTestTimeout
}

func connectionProbeHTTPTimeout(ctx context.Context) time.Duration {
	timeout := ConnectionTestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > connectionProbeTimeoutHeadroom {
			timeout = remaining - connectionProbeTimeoutHeadroom
		} else if remaining > 0 {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		return ConnectionTestTimeout
	}
	return timeout
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

// ErrReconnectSuperseded is returned by PerformContextSwitchIfOperationCurrent
// when another operation started after the caller captured its generation.
var ErrReconnectSuperseded = errors.New("reconnect superseded by a newer operation")

func PerformContextSwitch(newContext string) error {
	return performContextSwitch(newContext, 0, false, nil)
}

var contextOperationsBlocked atomic.Pointer[error]

// BlockClusterAccess keeps Radar off the cluster for the life of the process:
// it drops the clients a successful kubeconfig load built, leaving the same
// no-client state a failed load leaves, and makes every later context switch
// and reconnect fail with err before any teardown, as a preflight rejection.
// Radar Desktop calls it when its settings fail the startup check that the
// CLI exits on. The kubeconfig's context metadata stays, so the window can
// still name the context. Settings are read at startup, so nothing lifts it.
func BlockClusterAccess(err error) {
	contextOperationsBlocked.Store(&err)
	dropClusterClients()
}

func dropClusterClients() {
	clientMu.Lock()
	defer clientMu.Unlock()
	k8sConfig = nil
	k8sClient = nil
	discoveryClient = nil
	dynamicClient = nil
	activeClients = nil
	activeClientGeneration = clientGenerationCounter.Add(1)
}

func contextOperationsBlockedErr() error {
	if p := contextOperationsBlocked.Load(); p != nil {
		return *p
	}
	return nil
}

// ContextProbeError is a failed reachability check of the context Radar was
// about to switch to. The switch was refused before any teardown, so the
// current cluster is untouched.
type ContextProbeError struct {
	Context string
	Err     error
	// PluginStuck: the exec credential plugin never returned while the API
	// endpoint itself answered, so the fix is the credential command, not
	// the network.
	PluginStuck bool
}

func (e *ContextProbeError) Error() string {
	return fmt.Sprintf("cannot connect to %q: %v", e.Context, e.Err)
}

func (e *ContextProbeError) Unwrap() error { return e.Err }

// Classification is the connection errorType for this failure.
func (e *ContextProbeError) Classification() string {
	if e.PluginStuck {
		return "auth-plugin-stuck"
	}
	return ClassifyError(e.Err)
}

// credentialPluginStuck applies the runtime check's rule for a hung plugin to
// a switch target: the probe ran out of time inside the exec plugin, and an
// anonymous request shows the endpoint is reachable. An offline machine fails
// that request too and keeps its network classification.
func credentialPluginStuck(ctx context.Context, prepared *preparedContext, probeErr error) bool {
	if !prepared.usesExec || ctx.Err() != nil || !strings.Contains(probeErr.Error(), "auth plugin timeout") {
		return false
	}
	endpointCtx, cancel := context.WithTimeout(ctx, runtimeAuthEndpointProbeTimeout)
	defer cancel()
	return getRuntimeAuthEndpointProbe()(endpointCtx, prepared.config) == nil
}

// checkedSwitch carries a context that was prepared and found reachable
// before the switch took contextOpMu.
type checkedSwitch struct {
	ctx      context.Context
	prepared *preparedContext
	tracker  *ContextSwitchTracker
}

// PerformCheckedContextSwitch is the switch a person asks for. While the
// current cluster is connected, it first checks that the new context answers,
// and refuses the switch before tearing anything down when it does not. The
// check runs outside contextOpMu and under ctx, so a slow or abandoned check
// never holds up another operation. Disconnected sessions switch as before:
// there is no working cluster to protect.
func PerformCheckedContextSwitch(ctx context.Context, newContext string) error {
	return performCheckedContextSwitch(ctx, newContext, nil)
}

func performCheckedContextSwitch(ctx context.Context, newContext string, tracker *ContextSwitchTracker) error {
	checked := &checkedSwitch{ctx: ctx, tracker: tracker}
	if !shouldCheckBeforeSwitch(newContext) {
		return performContextSwitch(newContext, 0, false, checked)
	}
	observedOperationGen := currentOperationGen()
	prepared, err := prepareContext(newContext)
	if err != nil {
		// A context that can't even be loaded is refused by the switch's own
		// preflight, with its existing wording.
		return performContextSwitch(newContext, 0, false, checked)
	}
	timeout := connectionTestTimeoutFor(prepared.usesExec)
	if tracker != nil {
		timeout = ContextSwitchTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	probeErr := testConnection(probeCtx, prepared.config, prepared.usesExec)
	cancel()
	if probeErr != nil {
		log.Printf("[ops] Context switch to %q refused before teardown: %v", newContext, probeErr)
		errorlog.Record("context-switch", "warning",
			"target=%q unreachable, switch refused before teardown: %v", newContext, probeErr)
		return fmt.Errorf("%w: %w", ErrContextSwitchPreflight, &ContextProbeError{
			Context:     newContext,
			Err:         probeErr,
			PluginStuck: credentialPluginStuck(ctx, prepared, probeErr),
		})
	}
	checked.prepared = prepared
	return performContextSwitch(newContext, observedOperationGen, true, checked)
}

func shouldCheckBeforeSwitch(newContext string) bool {
	if IsInCluster() || newContext == GetContextName() {
		return false
	}
	return GetConnectionStatus().State == StateConnected
}

// RetryCurrentConnection reconnects the active context. In-cluster mode has no
// kubeconfig source to reload, so it verifies connectivity before rebuilding
// caches around the existing client configuration.
func RetryCurrentConnection() error {
	if err := contextOperationsBlockedErr(); err != nil {
		return fmt.Errorf("%w: %w", ErrContextSwitchPreflight, err)
	}
	if !IsInCluster() {
		var contextName string
		var generation uint64
		var stopped bool
		if !CaptureClusterReads(func() {
			contextName = GetContextName()
			generation = currentOperationGen()
			stopped = GetConnectionStatus().ErrorType == switchStoppedErrorType
		}) || stopped {
			return ErrReconnectSuperseded
		}
		if contextName == "" {
			return errors.New("no context configured")
		}
		return performContextSwitch(contextName, generation, true, &checkedSwitch{ctx: context.Background()})
	}

	contextName := GetContextName()
	if contextName == "" {
		return errors.New("no context configured")
	}
	// In-cluster recovery can be blocked in cache initialization while
	// holding contextOpMu. A manual retry must interrupt it before probing.
	_, observedOperationGen := CancelOngoingOperations()
	probeCtx, probeCancel, ok := newOperationContextForGeneration(
		observedOperationGen,
		connectionTestOperationTimeout(),
	)
	if !ok {
		return ErrReconnectSuperseded
	}
	probeErr := getRuntimeAuthProbe()(probeCtx)
	probeCancel()
	if currentOperationGen() != observedOperationGen {
		return ErrReconnectSuperseded
	}
	var err error
	if probeErr != nil {
		err = fmt.Errorf("cluster connection failed: %w", probeErr)
	} else {
		err = getRuntimeAuthReconnect()(contextName, observedOperationGen)
	}
	if err != nil && !errors.Is(err, ErrReconnectSuperseded) {
		contextOpMu.Lock()
		defer contextOpMu.Unlock()
		if currentOperationGen() != observedOperationGen {
			return ErrReconnectSuperseded
		}
		SetConnectionStatus(ConnectionStatus{State: StateDisconnected, Context: contextName, Error: err.Error(), ErrorType: ClassifyError(err)})
	}
	return err
}

// PerformContextSwitchIfOperationCurrent is the conditional variant used by
// automatic recovery: it aborts with ErrReconnectSuperseded — before any
// teardown — if any other operation (user switch, rescope, retry) started
// since observedOperationGen was captured. The check runs under contextOpMu,
// which closes the TOCTOU a check-then-switch caller would have: a user
// switch that started mid-probe bumps the generation before this can.
func PerformContextSwitchIfOperationCurrent(newContext string, observedOperationGen uint64) error {
	return performContextSwitch(newContext, observedOperationGen, true, nil)
}

// reinitializeCurrentContextIfOperationCurrent restores subsystems after an
// in-cluster auth failure. There is no kubeconfig source to reload or context
// to switch; projected credentials are read by the existing rest.Config. The
// generation check keeps a user operation that started during the recovery
// probe authoritative.
func reinitializeCurrentContextIfOperationCurrent(
	contextName string,
	observedOperationGen uint64,
	initSubsystems func(context.Context, func(string)) error,
) error {
	reconnectStart := time.Now()
	activeContextOperations.Add(1)
	contextOpMu.Lock()
	defer func() {
		activeContextOperations.Add(-1)
		contextOpMu.Unlock()
	}()

	if currentOperationGen() != observedOperationGen {
		return ErrReconnectSuperseded
	}
	if !IsInCluster() || GetContextName() != contextName {
		return ErrReconnectSuperseded
	}

	reportProgress("Restoring cluster caches...")
	ResetAllSubsystems()
	InvalidateCapabilitiesCache()
	InvalidateResourcePermissionsCache()
	InvalidateServerVersionCache()
	InvalidatePlatformCache()

	initCtx, initCancel := NewOperationContext(ContextSwitchTimeout)
	defer initCancel()
	if err := initSubsystems(initCtx, reportProgress); err != nil {
		if currentOperationGen() != observedOperationGen {
			return ErrReconnectSuperseded
		}
		return fmt.Errorf("subsystem init failed: %w", err)
	}
	if currentOperationGen() != observedOperationGen {
		return ErrReconnectSuperseded
	}

	reportProgress("Building topology...")
	notifyContextSwitchCallbacks(contextName, "")
	SetConnectionStatus(ConnectionStatus{
		State:       StateConnected,
		Context:     contextName,
		ClusterName: GetClusterName(),
	})
	log.Printf("[ops] In-cluster reconnect to %q COMPLETE (%v total)", contextName, time.Since(reconnectStart))
	return nil
}

func performContextSwitch(newContext string, observedOperationGen uint64, requireOperationCurrent bool, checked *checkedSwitch) (switchErr error) {
	switchStart := time.Now()
	log.Printf("[ops] Context switch START → %q", newContext)

	// Serialize against any other in-flight switch / rescope so their teardown +
	// init can't interleave on the shared cache. A queued request waits here.
	activeContextOperations.Add(1)
	contextOpMu.Lock()
	defer func() {
		activeContextOperations.Add(-1)
		contextOpMu.Unlock()
	}()

	if requireOperationCurrent && currentOperationGen() != observedOperationGen {
		return ErrReconnectSuperseded
	}
	if err := contextOperationsBlockedErr(); err != nil {
		return fmt.Errorf("%w: %w", ErrContextSwitchPreflight, err)
	}
	if IsInCluster() {
		return fmt.Errorf("%w: context switching is unavailable in in-cluster mode", ErrContextSwitchPreflight)
	}

	contexts, err := GetAvailableContexts()
	if err != nil {
		return fmt.Errorf("%w: target context is unavailable", ErrContextSwitchPreflight)
	}
	targetAvailable := false
	for _, context := range contexts {
		if context.Name == newContext {
			targetAvailable = true
			break
		}
	}
	if !targetAvailable {
		return fmt.Errorf("%w: context %q is no longer available", ErrContextSwitchPreflight, newContext)
	}
	if err := validateContextSwitchTarget(newContext); err != nil {
		reason := kubeconfigDiagnosticError(err)
		log.Printf("[ops] Context switch preflight rejected target %q: %s (%v)", newContext, reason, err)
		errorlog.Record("context-switch", "warning",
			"preflight target=%q rejected before teardown: %s", newContext, reason)
		return fmt.Errorf("%w: context %q cannot be loaded from its current source: %s", ErrContextSwitchPreflight, newContext, reason)
	}

	// Under --namespace-scope, validate the new context has a usable scope target
	// BEFORE tearing anything down. Otherwise a switch to a context with no
	// namespace (and no saved pick) would reset and repoint the client, then fail
	// at requireNamespaceScopeTarget below — leaving us on the new context with no
	// informer caches until a full reconnect succeeds. This is a preflight failure
	// (nothing torn down yet) so the caller must keep the current connection intact.
	if ForceNamespaceScope && ProspectiveNamespaceScopeTarget(newContext) == "" {
		return fmt.Errorf("%w: --namespace-scope requires --namespace, a namespace on context %q, or a saved namespace pick", ErrContextSwitchPreflight, newContext)
	}

	// A checked switch probed its target before taking the lock. Don't act on
	// it if the person gave up waiting, or if the name now resolves to
	// something else: a CAPI merge or a kubeconfig edit doesn't bump the
	// operation generation.
	if checked != nil && checked.ctx.Err() != nil {
		return fmt.Errorf("%w: switch to %q was cancelled", ErrContextSwitchPreflight, newContext)
	}
	if checked != nil && checked.prepared != nil {
		fingerprint, err := currentContextSourceFingerprint(newContext)
		if err != nil || fingerprint != checked.prepared.fingerprint {
			return fmt.Errorf("%w: the kubeconfig for %q changed while Radar was checking it, try again", ErrContextSwitchPreflight, newContext)
		}
	}

	progress := reportProgress
	var tracker *ContextSwitchTracker
	if checked != nil {
		tracker = checked.tracker
	}
	if tracker != nil {
		if !tracker.commit(checked.ctx) {
			return context.Canceled
		}
		progress = func(message string) {
			tracker.progress(message)
			if status := GetConnectionStatus(); status.State == StateConnecting && status.Context == newContext {
				UpdateConnectionProgress(message)
			}
			reportProgress(message)
		}
	}
	defer func() {
		if tracker != nil {
			if p := recover(); p != nil {
				switchErr = contextSwitchPanic(p)
			}
			if switchErr != nil && !tracker.setCanStop(checked.ctx, false) {
				CancelOngoingOperations()
				dropClusterClients()
				ResetAllSubsystems()
				InvalidateCapabilitiesCache()
				InvalidateResourcePermissionsCache()
				InvalidateServerVersionCache()
				InvalidatePlatformCache()
				SetConnectionStatus(ConnectionStatus{
					State: StateDisconnected, Context: newContext, ErrorType: switchStoppedErrorType,
					Error: "Cluster switch stopped. The previous connection was closed. Choose a cluster to connect.",
				})
				return
			}
		}
		if switchErr != nil && (checked != nil || !requireOperationCurrent) {
			SetConnectionStatus(ConnectionStatus{State: StateDisconnected, Context: GetContextName(), Error: switchErr.Error(), ErrorType: ClassifyError(switchErr)})
		}
	}()
	// Fire before-switch callbacks while the OLD context is still active, so
	// teardown (e.g. cancelling in-flight AI investigations) can't leak onto the
	// cluster we're about to connect to. After the preflight above — a failed
	// preflight leaves the current connection (and its runs) intact.
	notifyBeforeContextSwitch(newContext)

	// Cancel any in-flight API calls from the previous context (RBAC checks,
	// capability probes, etc.) so they don't serialize through the old exec
	// plugin and block the new context's connectivity test.
	CancelOngoingOperations()

	// Step 1: Tear down all subsystems. Stop sessions first — past this point the
	// previous cluster's caches are gone, so lingering port-forwards / exec
	// terminals would be reading state we can no longer serve.
	stopActiveSessions()
	progress("Stopping caches...")
	t := time.Now()
	ResetAllSubsystems()
	logTiming("   [ops] ResetAllSubsystems: %v", time.Since(t))
	// Drop the previous cluster's permission result before the client swap, so
	// nothing pairs it with the new client, including when the switch fails
	// past this point. Not before the reset above: until then the previous
	// cluster's resource cache is live, and several readers treat a nil
	// permission result as cluster-wide access. The invalidations right after
	// SwitchContext retire any probe still in flight against the previous
	// client.
	InvalidateResourcePermissionsCache()

	// Step 2: Switch the K8s client to the new context
	progress("Connecting to cluster...")
	t = time.Now()
	log.Printf("Switching K8s client to context %q...", newContext)
	if checked != nil && checked.prepared != nil {
		installPreparedContext(checked.prepared)
	} else if err := SwitchContext(newContext); err != nil {
		elapsed := time.Since(switchStart).Truncate(time.Millisecond)
		log.Printf("[ops] Context switch FAILED at SwitchContext: %v (%v since switch start)", err, elapsed)
		errorlog.Record("context-switch", "error",
			"stage=SwitchContext target=%q elapsed=%v: %s", newContext, elapsed, kubeconfigDiagnosticError(err))
		return fmt.Errorf("failed to switch context: %w", err)
	}
	logTiming("   [ops] SwitchContext: %v", time.Since(t))
	if tracker != nil {
		SetConnectionStatus(ConnectionStatus{State: StateConnecting, Context: newContext, ClusterName: GetClusterName(), ProgressMsg: "Loading cluster data…"})
	}
	ClearNamespaceScopeOverride()
	RestoreNamespaceScopePreference(GetContextName())
	if err := requireNamespaceScopeTarget(newContext); err != nil {
		return err
	}

	// Invalidate caches - permissions and cluster info may differ between clusters
	InvalidateCapabilitiesCache()
	InvalidateResourcePermissionsCache()
	InvalidateServerVersionCache()
	InvalidatePlatformCache()

	// Step 3: Test connectivity before proceeding with initialization. A
	// checked switch already probed the same client config before teardown.
	// Contexts are derived from the operation context so they're canceled
	// if another context switch starts while this one is in progress.
	if checked == nil || checked.prepared == nil {
		progress("Testing cluster connectivity...")
		t = time.Now()
		log.Println("Testing cluster connectivity...")
		timeout := connectionTestOperationTimeout()
		if tracker != nil {
			timeout = ContextSwitchTimeout
		}
		connCtx, connCancel := NewOperationContext(timeout)
		defer connCancel()
		if tracker != nil {
			if !tracker.setCanStop(checked.ctx, true) {
				return context.Canceled
			}
			stop := context.AfterFunc(checked.ctx, connCancel)
			defer stop()
		}
		if err := TestClusterConnection(connCtx); err != nil {
			if tracker != nil && !tracker.setCanStop(checked.ctx, false) {
				return context.Canceled
			}
			elapsed := time.Since(switchStart).Truncate(time.Millisecond)
			log.Printf("[ops] Context switch FAILED at connectivity test: %v (%v since switch start)", err, elapsed)
			errorlog.Record("context-switch", "error",
				"stage=TestClusterConnection target=%q elapsed=%v: %v", newContext, elapsed, err)
			return fmt.Errorf("cluster connection failed: %w", err)
		}
		if tracker != nil && !tracker.setCanStop(checked.ctx, false) {
			return context.Canceled
		}
		log.Printf("[ops] Cluster connectivity verified (%v)", time.Since(t))
	}

	// Step 4: Initialize all subsystems (same function as initial boot).
	// Teardown above is non-blocking, so old informers may still be draining.

	t = time.Now()
	var initCtx context.Context
	var initCancel context.CancelFunc
	var discoveryWait time.Duration
	if tracker != nil {
		// Once the target answers, informer readiness uses the same first-paint
		// backstop as startup. A cluster replacement still cancels this work.
		initCtx, initCancel = context.WithCancel(OperationContext())
		// A stuck exec plugin can outlive the discovery client HTTP timeout.
		// Start this wait budget only once the typed cache is ready.
		discoveryWait = FirstPaintBackstop
		stop := context.AfterFunc(checked.ctx, initCancel)
		defer stop()
	} else {
		initCtx, initCancel = NewOperationContext(ContextSwitchTimeout)
	}
	defer initCancel()
	if err := initAllSubsystems(initCtx, progress, discoveryWait, tracker); err != nil {
		if tracker != nil && !tracker.setCanStop(checked.ctx, false) {
			return context.Canceled
		}
		elapsed := time.Since(switchStart).Truncate(time.Millisecond)
		log.Printf("[ops] Context switch FAILED at subsystem init: %v (%v since switch start)", err, elapsed)
		errorlog.Record("context-switch", "error",
			"stage=InitAllSubsystems target=%q elapsed=%v: %v", newContext, elapsed, err)
		return fmt.Errorf("subsystem init failed: %w", err)
	}
	logTiming("   [ops] InitAllSubsystems: %v", time.Since(t))

	// Step 5: Notify all registered callbacks
	progress("Building topology...")
	var switchID string
	if tracker != nil {
		if checked.ctx.Err() != nil {
			return context.Canceled
		}
		switchID = tracker.Snapshot().ID
	}
	notifyContextSwitchCallbacks(newContext, switchID)

	// Publish success while still holding contextOpMu: a caller publishing
	// after this returns races the next queued operation's teardown, and the
	// window would advertise Connected over dead caches. HTTP handlers'
	// duplicate publishes dedupe inside SetConnectionStatus.
	SetConnectionStatus(ConnectionStatus{
		State:       StateConnected,
		Context:     newContext,
		ClusterName: GetClusterName(),
	})
	log.Printf("[ops] Context switch to %q COMPLETE (%v total)", newContext, time.Since(switchStart))

	return nil
}

func notifyContextSwitchCallbacks(contextName, switchID string) {
	contextSwitchMu.RLock()
	callbacks := make([]func(string, string), len(contextSwitchCallbacks))
	copy(callbacks, contextSwitchCallbacks)
	contextSwitchMu.RUnlock()

	for _, callback := range callbacks {
		callback(contextName, switchID)
	}
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
	activeContextOperations.Add(1)
	contextOpMu.Lock()
	defer func() {
		activeContextOperations.Add(-1)
		contextOpMu.Unlock()
	}()
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
	connCtx, connCancel := NewOperationContext(connectionTestOperationTimeout())
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
	InvalidateServerVersionCache()
	InvalidatePlatformCache()

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
