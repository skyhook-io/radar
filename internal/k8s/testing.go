package k8s

import (
	"maps"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/policyreports"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// InitLoadTestResourceCache creates a resource cache from a fake client using
// the live path's deferred-informer split, so the critical/deferred phases and
// the warmup window they drive actually exist. InitTestResourceCache syncs
// everything at once, which is what unit tests want and makes it useless for
// measuring anything that depends on startup phases.
//
// Intended for load-test harnesses, not unit tests.
func InitLoadTestResourceCache(client kubernetes.Interface) error {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	enabled := allTestResourceTypes()
	deferred := make(map[string]bool, len(deferredResources))
	maps.Copy(deferred, deferredResources)

	secretWriteTimes := newSecretDataManagerWriteIndex()
	cronJobScheduleObservations := newCronJobScheduleObservationTracker()
	cfg := k8score.CacheConfig{
		Client:        client,
		ResourceTypes: enabled,
		DeferredTypes: deferred,
		OnTransform: func(obj any) {
			secretWriteTimes.capture(obj)
		},
		OnObservedChange: observedChangeHandler(client, secretWriteTimes.observeChange, cronJobScheduleObservations.observeChange),
	}

	core, err := k8score.NewResourceCache(cfg)
	if err != nil {
		return err
	}

	initialSyncComplete.Store(core.IsSyncComplete())

	resourceCache.Store(&ResourceCache{
		ResourceCache:               core,
		secretsEnabled:              true,
		cronJobScheduleObservations: cronJobScheduleObservations,
		secretWriteTimes:            secretWriteTimes,
		generation:                  cacheGeneration,
	})

	cacheOnce = new(sync.Once)
	cacheOnce.Do(func() {})

	return nil
}

// InitTestPromotedSyncingCache builds a cache whose Phase-1 wait returns via
// the patience/minimal-set path while some critical kinds are still syncing
// (delay their informer start via syncDelays), promoting them at runtime — the
// state a SyncTimeout produces in production. syncTimeout bounds Phase 1 and
// deferredSyncTimeout bounds the promoted kinds' background sync, so a test
// can walk the full progressive contract: kind_sync_pending -> kind_sync_timed_out
// -> served after a late sync. Call ResetResourceCache to clean up.
//
// This is intended for integration tests only.
func InitTestPromotedSyncingCache(client kubernetes.Interface, syncTimeout, deferredSyncTimeout time.Duration, syncDelays map[string]time.Duration) error {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	secretWriteTimes := newSecretDataManagerWriteIndex()
	cronJobScheduleObservations := newCronJobScheduleObservationTracker()
	cfg := k8score.CacheConfig{
		Client:              client,
		ResourceTypes:       allTestResourceTypes(),
		DeferredTypes:       map[string]bool{},
		PatienceWindow:      20 * time.Millisecond,
		MinimalSet:          map[string]bool{"services": true},
		SyncTimeout:         syncTimeout,
		DeferredSyncTimeout: deferredSyncTimeout,
		// Blocking a list reactor wedges other kinds under the fake's lock.
		// Delay informer start or return errors immediately to hold it unsynced.
		DebugSyncDelays: syncDelays,
	}

	core, err := k8score.NewResourceCache(cfg)
	if err != nil {
		return err
	}

	initialSyncComplete.Store(core.IsSyncComplete())
	resourceCache.Store(&ResourceCache{
		ResourceCache:               core,
		secretsEnabled:              true,
		cronJobScheduleObservations: cronJobScheduleObservations,
		secretWriteTimes:            secretWriteTimes,
		generation:                  cacheGeneration,
	})
	cacheOnce = new(sync.Once)
	cacheOnce.Do(func() {})
	return nil
}

// InitTestResourceCache creates a resource cache from a fake or test client,
// bypassing RBAC checks and the normal Initialize/InitResourceCache flow.
// All resource types are enabled. Call ResetTestState to clean up.
//
// This is intended for integration tests only.
func InitTestResourceCache(client kubernetes.Interface) error {
	return initTestResourceCache(client, nil, nil)
}

// InitScopedTestResourceCache is InitTestResourceCache with per-kind scopes,
// the shape probe-based RBAC gating produces when a kind cannot be listed
// cluster-wide. It is what lets a test reach the paths that must refuse to
// read an informer covering other namespaces.
func InitScopedTestResourceCache(client kubernetes.Interface, scopes map[string]k8score.ResourceScope) error {
	return initTestResourceCache(client, scopes, nil)
}

// InitScopedTestResourceCacheNamespaces is InitScopedTestResourceCache for
// kinds the probe found listable in several namespaces.
func InitScopedTestResourceCacheNamespaces(client kubernetes.Interface, scopes map[string]k8score.ResourceScope, namespaces map[string][]string) error {
	return initTestResourceCache(client, scopes, namespaces)
}

func initTestResourceCache(client kubernetes.Interface, scopes map[string]k8score.ResourceScope, scopeNamespaces map[string][]string) error {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	enabled := allTestResourceTypes()
	if scopes != nil {
		enabled = map[string]bool{}
		for kind, scope := range scopes {
			if scope.Enabled {
				enabled[kind] = true
			}
		}
	}

	secretWriteTimes := newSecretDataManagerWriteIndex()
	cronJobScheduleObservations := newCronJobScheduleObservationTracker()
	cfg := k8score.CacheConfig{
		Client:        client,
		ResourceTypes: enabled,
		// No deferred types for tests — all sync immediately
		DeferredTypes:           map[string]bool{},
		ResourceScopes:          scopes,
		ResourceScopeNamespaces: scopeNamespaces,
		OnTransform: func(obj any) {
			secretWriteTimes.capture(obj)
		},
		OnObservedChange: observedChangeHandler(client, secretWriteTimes.observeChange, cronJobScheduleObservations.observeChange),
	}

	core, err := k8score.NewResourceCache(cfg)
	if err != nil {
		return err
	}

	initialSyncComplete.Store(true)

	resourceCache.Store(&ResourceCache{
		ResourceCache:               core,
		secretsEnabled:              true,
		cronJobScheduleObservations: cronJobScheduleObservations,
		secretWriteTimes:            secretWriteTimes,
		generation:                  cacheGeneration,
	})

	// Mark cacheOnce as "already executed" so InitResourceCache is a no-op.
	cacheOnce = new(sync.Once)
	cacheOnce.Do(func() {})

	waitForInformerStatuses(resourceCache.Load())

	return nil
}

// InitTestResourceCacheFromConfig installs a cache built from cfg as given and
// does not wait for it to sync, for tests whose informers must stay loading
// (a namespace that never answers, a deadline that has to fire).
func InitTestResourceCacheFromConfig(cfg k8score.CacheConfig) error {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	core, err := k8score.NewResourceCache(cfg)
	if err != nil {
		return err
	}
	initialSyncComplete.Store(true)
	resourceCache.Store(&ResourceCache{ResourceCache: core, generation: cacheGeneration})
	cacheOnce = new(sync.Once)
	cacheOnce.Do(func() {})
	return nil
}

// waitForInformerStatuses blocks until every informer's InformerSyncStatus
// reports synced.
//
// NewResourceCache returns as soon as the informers themselves report synced,
// but InformerSyncStatus.Synced is a mirror of that, flipped by a goroutine per
// informer. Nothing in the product waits on the mirror, so the gap is invisible
// there — but IsKindReady reads it, and the missing-reference detectors gate on
// IsKindReady and fail toward silence, so a test that queries on the next line
// can see a fully-populated cache report nothing at all.
//
// The gap is normally sub-millisecond and closes on its own; it only widens
// when the machine is loaded enough to delay scheduling those goroutines, which
// is why this reproduced in CI and never locally.
//
// Best-effort: on timeout the caller proceeds and its own assertions report the
// real problem, rather than this returning an error that every call site would
// have to handle.
func waitForInformerStatuses(rc *ResourceCache) {
	if rc == nil {
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := rc.GetSyncStatus()
		if len(st.PendingCritical) == 0 && len(st.PendingDeferred) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// InitTestDynamicResourceCache wires the dynamic resource cache and discovery
// singletons against test fakes. Pass a dynamic client (typically from
// dynamicfake.NewSimpleDynamicClientWithCustomListKinds) and the set of
// APIResources to register in discovery. Each registered resource gets a GVR
// entry that group-qualified lookups (GetGVRWithGroup) and dynamic informers
// can resolve.
//
// Callers should defer ResetTestDynamicState — without it, the dynamic
// singletons leak into other tests that share TestMain state.
//
// This is intended for integration tests only.
func InitTestDynamicResourceCache(dynClient dynamic.Interface, resources []APIResource) error {
	clientMu.Lock()
	dynamicClient = dynClient
	clientMu.Unlock()

	// Bootstrap discovery from a fake clientset so NewResourceDiscovery has a
	// non-nil discovery client; AddAPIResource then registers the test-only
	// GVRs (e.g. serving.knative.dev/Service) the test depends on.
	fakeDisc := fakeclientset.NewSimpleClientset().Discovery()
	core, err := k8score.NewResourceDiscovery(fakeDisc)
	if err != nil {
		clientMu.Lock()
		dynamicClient = nil
		clientMu.Unlock()
		return err
	}
	for _, r := range resources {
		core.AddAPIResource(r)
	}

	// Installing the singleton directly is what marks discovery initialized —
	// InitResourceDiscovery returns early when it is already set. The binding
	// marks it valid for whatever client the test environment has (usually
	// nil): the singleton is served only while that binding stays current.
	discoveryMu.Lock()
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
	resourceDiscoveryClient = GetDiscoveryClient()
	discoveryMu.Unlock()

	return InitDynamicResourceCache(nil)
}

// SetTestDynamicSyncStallAfter sets how long a dynamic informer may stay
// unsynced before it reads as stalled, for dynamic caches initialized after
// the call. The production threshold, 30 seconds, is longer than a test should
// wait. Returns a restore func.
func SetTestDynamicSyncStallAfter(d time.Duration) func() {
	previous := dynamicSyncStallAfter
	dynamicSyncStallAfter = d
	return func() { dynamicSyncStallAfter = previous }
}

// ResetTestDynamicState tears down the dynamic cache + discovery singletons
// and clears the dynamic client. Pairs with InitTestDynamicResourceCache.
func ResetTestDynamicState() {
	ResetDynamicResourceCache()
	ResetResourceDiscovery()
	clientMu.Lock()
	dynamicClient = nil
	clientMu.Unlock()
}

// SetTestContextName is a test-only helper that overrides the package-level
// kubeconfig context name. Used by tests that exercise per-context state
// (e.g. namespace preferences) without needing to spin up a real client.
// Returns the previous value so callers can restore it on cleanup.
func SetTestContextName(name string) string {
	clientMu.Lock()
	prev := contextName
	contextName = name
	clientMu.Unlock()
	return prev
}

// SetTestResourceDiscovery installs discovery backed by client and returns a
// restore func.
func SetTestResourceDiscovery(client discovery.DiscoveryInterface) (func(), error) {
	core, err := k8score.NewResourceDiscovery(client)
	if err != nil {
		return nil, err
	}
	ResetResourceDiscovery()
	discoveryMu.Lock()
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
	discoveryMu.Unlock()
	return ResetResourceDiscovery, nil
}

// SetTestLocalMode makes IsInCluster report local mode and returns a restore func.
func SetTestLocalMode() func() {
	clientMu.Lock()
	previousInitializationStarted := initializationStarted
	previousKubeconfigMode := kubeconfigMode
	previousForceInCluster := ForceInCluster
	initializationStarted = true
	kubeconfigMode = "single"
	ForceInCluster = false
	clientMu.Unlock()

	return func() {
		clientMu.Lock()
		initializationStarted = previousInitializationStarted
		kubeconfigMode = previousKubeconfigMode
		ForceInCluster = previousForceInCluster
		clientMu.Unlock()
	}
}

// SetTestKubeconfigMode makes IsInCluster and GetKubeconfigSummary().Mode both
// follow mode ("in-cluster", "single", ...) and returns a restore func.
func SetTestKubeconfigMode(mode string) func() {
	restore := SetTestLocalMode()
	clientMu.Lock()
	kubeconfigMode = mode
	clientMu.Unlock()
	return restore
}

// SetTestRegistryEntry is a test-only helper that registers one context in the
// isolated-load registry, so callers can exercise resolution against a
// multi-kubeconfig layout. Returns a restore func.
func SetTestRegistryEntry(qualifiedName, sourceFile, inFileName string) func() {
	clientMu.Lock()
	prev := contextRegistry
	next := make(map[string]contextEntry, len(prev)+1)
	for k, v := range prev {
		next[k] = v
	}
	next[qualifiedName] = contextEntry{SourceFile: sourceFile, InFileName: inFileName}
	contextRegistry = next
	clientMu.Unlock()
	return func() {
		clientMu.Lock()
		contextRegistry = prev
		clientMu.Unlock()
	}
}

// SetTestContextNamespace is a test-only helper that overrides the package-level
// kubeconfig context namespace. Returns the previous value so callers can
// restore it on cleanup.
func SetTestContextNamespace(ns string) string {
	clientMu.Lock()
	prev := contextNamespace
	contextNamespace = ns
	clientMu.Unlock()
	return prev
}

// SetTestContextUsesExec overrides whether the current context uses exec auth
// and returns the previous value so callers can restore it on cleanup.
func SetTestContextUsesExec(enabled bool) bool {
	clientMu.Lock()
	prev := contextUsesExec
	contextUsesExec = enabled
	clientMu.Unlock()
	return prev
}

// SetTestClient overrides the package-level client and returns the previous
// value so tests in other packages can restore it.
func SetTestClient(c *kubernetes.Clientset) *kubernetes.Clientset {
	clientMu.Lock()
	prev := k8sClient
	k8sClient = c
	clientMu.Unlock()
	return prev
}

// SetTestPolicyReportIndex publishes a PolicyReport index directly, bypassing
// CRD discovery and the informer warmup that normally build it.
//
// Publishing an index is also what makes GetPolicyReportStatus report Ready, so
// this is the whole seam: the policy surfaces read exactly these two together.
// Without it the per-policy handlers return their empty response before any of
// the authorization logic runs, and a test against them would assert nothing
// while appearing to cover the endpoint.
//
// Returns the previous index so a test can restore it.
//
// This is intended for integration tests only.
// SetTestConfig publishes a rest.Config directly, so a handler that resolves a
// per-request config can run without a real cluster connection. SetTestClient
// publishes the clientset but not the config, and a handler that needs both
// bails out early with "cluster client not available" if only one is set.
//
// Returns the previous config so a test can restore it.
//
// This is intended for integration tests only.
func SetTestConfig(c *rest.Config) *rest.Config {
	clientMu.Lock()
	prev := k8sConfig
	k8sConfig = c
	clientMu.Unlock()
	return prev
}

// SetTestProfileSource supplies the source identity used by local configuration
// handlers. SetTestConfig supplies its matching REST transport separately.
func SetTestProfileSource(source, name, user string) func() {
	clientMu.Lock()
	oldBinding, oldName, oldFile, oldSourceName, oldSourceConfig, oldGeneration := contextBinding, contextName, activeSourceFile, activeSourceName, activeSourceConfig, activeClientGeneration
	contextBinding = sourceContextBinding(source, name)
	contextName, activeSourceFile, activeSourceName = name, source, name
	activeSourceConfig = clientcmdapi.NewConfig()
	activeSourceConfig.Contexts[name] = &clientcmdapi.Context{AuthInfo: user}
	activeClientGeneration++
	clientMu.Unlock()
	return func() {
		clientMu.Lock()
		contextBinding, contextName, activeSourceFile, activeSourceName, activeSourceConfig, activeClientGeneration = oldBinding, oldName, oldFile, oldSourceName, oldSourceConfig, oldGeneration
		clientMu.Unlock()
	}
}

func SetTestContextCount(count int) func() {
	clientMu.Lock()
	previous := totalContextCount
	totalContextCount = count
	clientMu.Unlock()
	return func() {
		clientMu.Lock()
		totalContextCount = previous
		clientMu.Unlock()
	}
}

func SetTestPolicyReportIndex(idx *policyreports.Index) *policyreports.Index {
	prev := policyReportIndex.Load()
	policyReportIndex.Store(idx)
	return prev
}

// ResetTestState tears down the resource cache and resets all package-level
// state so the next test starts clean.
//
// This is intended for integration tests only.
func ResetTestState() {
	// Join the old recovery worker before replacing hooks or state it may still use.
	runtimeAuthRecoveryOwed.Store(false)
	deadline := time.Now().Add(connectionTestOperationTimeout() + time.Second)
	for runtimeAuthRecoveryActive.Load() {
		select {
		case runtimeAuthRecoveryNudge <- struct{}{}:
		default:
		}
		if time.Now().After(deadline) {
			panic("runtime authentication recovery worker did not stop during test cleanup")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-runtimeAuthRecoveryNudge:
	default:
	}

	policyReportIndex.Store(nil)

	// Reset resource cache
	ResetResourceCache()

	// Reset connection state
	connectionStatusMu.Lock()
	connectionStatus = ConnectionStatus{}
	clusterLivenessProbe = defaultClusterLivenessProbe
	connectionStatusMu.Unlock()

	// Reset connection callbacks
	connectionCallbacksMu.Lock()
	connectionCallbacks = nil
	connectionCallbacksMu.Unlock()

	contextSwitchMu.Lock()
	beforeContextSwitchCallbacks = nil
	contextSwitchCallbacks = nil
	namespaceRescopeCallbacks = nil
	contextSwitchProgressCallbacks = nil
	contextSwitchMu.Unlock()

	runtimeAuthChecksMu.Lock()
	runtimeAuthChecks = make(map[uint64]struct{})
	runtimeAuthCooldownGeneration = 0
	runtimeAuthProbeNotBefore = time.Time{}
	runtimeAuthInconclusiveStreak = 0
	runtimeAuthProbe = TestClusterConnection
	runtimeAuthEndpointProbe = defaultRuntimeAuthEndpointProbe
	runtimeAuthReconnect = nil
	runtimeAuthRecoveryInitialInterval = defaultRuntimeAuthRecoveryInitialInterval
	runtimeAuthRecoveryMaxInterval = defaultRuntimeAuthRecoveryMaxInterval
	runtimeAuthRecoveryHungInterval = defaultRuntimeAuthRecoveryHungInterval
	runtimeAuthChecksMu.Unlock()
	activeContextOperations.Store(0)
	clientMu.Lock()
	k8sConfig = nil
	k8sClient = nil
	discoveryClient = nil
	dynamicClient = nil
	activeClientGeneration = 0
	kubeconfigMode = ""
	contextBinding = ""
	activeSourceFile = ""
	activeSourceName = ""
	activeSourceConfig = nil
	initializationStarted = false
	kubeconfigDirectoryFileCount = 0
	kubeconfigDirectoryPaths = nil
	kubeconfigEnvWasIgnored = false
	kubeconfigEnvIgnoredReason = ""
	capiKubeconfigs = make(map[string]string)
	preCapiPromotion = nil
	clientMu.Unlock()

	// Reset capabilities cache
	capabilitiesMu.Lock()
	cachedCapabilities = nil
	capabilitiesMu.Unlock()

	// Reset resource permissions cache
	resourcePermsMu.Lock()
	invalidateResourcePermissionsCacheLocked()
	resourcePermsMu.Unlock()
	ForceNamespaceScope = false
	SetFallbackNamespace("")
	ClearNamespaceScopeOverride()
	SetNamespaceScopePreferenceResolver(nil)

	// Reset operation context so stale cancellations don't leak between tests
	CancelOngoingOperations()
}

// allTestResourceTypes enables every kind the typed cache knows about.
func allTestResourceTypes() map[string]bool {
	return map[string]bool{
		"pods":                     true,
		"services":                 true,
		"deployments":              true,
		"daemonsets":               true,
		"statefulsets":             true,
		"replicasets":              true,
		"ingresses":                true,
		"configmaps":               true,
		"secrets":                  true,
		"events":                   true,
		"persistentvolumeclaims":   true,
		"resourcequotas":           true,
		"nodes":                    true,
		"namespaces":               true,
		"jobs":                     true,
		"cronjobs":                 true,
		"horizontalpodautoscalers": true,
		"persistentvolumes":        true,
		"storageclasses":           true,
		"poddisruptionbudgets":     true,
		"roles":                    true,
		"clusterroles":             true,
		"rolebindings":             true,
		"clusterrolebindings":      true,
		"serviceaccounts":          true,
		"ingressclasses":           true,
		"networkpolicies":          true,
		"limitranges":              true,
	}
}

// SetTestPermissionProbeClient installs a dynamic client that grants every
// list a permission probe makes, calling onList before each one answers.
// Returns a restore func.
//
// Permission probes list resources this package chooses, so a test in another
// package cannot build a fake that knows them all.
func SetTestPermissionProbeClient(onList func()) func() {
	gvrToListKind := map[schema.GroupVersionResource]string{}
	for _, p := range resourceProbeTargets(&ResourcePermissions{}) {
		for _, gvr := range resolveProbeGVRs(p) {
			gvrToListKind[gvr] = gvr.Resource + "List"
		}
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind)
	client.PrependReactor("list", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
		onList()
		return false, nil, nil // fall through to the default reactor (empty list)
	})

	clientMu.Lock()
	prev := dynamicClient
	dynamicClient = client
	clientMu.Unlock()
	return func() {
		clientMu.Lock()
		dynamicClient = prev
		clientMu.Unlock()
	}
}

// SetTestPermissionResult publishes a permission probe result as if a probe had
// just produced it. Returns a restore func.
//
// The cache is otherwise only writable by a real probe, so without this seam a
// test in another package cannot reach any of the code that reads it - the
// handlers fall through to their "no probe data yet" path and assert nothing
// while appearing to cover the endpoint.
func SetTestPermissionResult(result *PermissionCheckResult) func() {
	resourcePermsMu.Lock()
	prevResult, prevExpiry := cachedPermResult, resourcePermsExpiry
	cachedPermResult = result
	resourcePermsExpiry = time.Now().Add(time.Minute)
	resourcePermsMu.Unlock()
	cache := GetResourceCache()
	var previousBound *PermissionCheckResult
	if cache != nil {
		previousBound = cache.permissionResult
		cache.permissionResult = clonePermissionCheckResult(result)
	}

	return func() {
		resourcePermsMu.Lock()
		cachedPermResult, resourcePermsExpiry = prevResult, prevExpiry
		if cache != nil {
			cache.permissionResult = previousBound
		}
		resourcePermsMu.Unlock()
	}
}

// SetTestConnectionStatus sets the connection status without notifying
// callbacks or starting auth recovery, and returns a func that restores the
// previous status.
func SetTestConnectionStatus(status ConnectionStatus) func() {
	connectionStatusMu.Lock()
	prev := connectionStatus
	connectionStatus = status
	connectionStatusMu.Unlock()
	return func() {
		connectionStatusMu.Lock()
		connectionStatus = prev
		connectionStatusMu.Unlock()
	}
}

// InitTestDynamicResourceCacheWithFallbacks is InitTestDynamicResourceCache
// for an identity that may not list CRDs cluster-wide: the dynamic cache then
// probes fallbacks and watches each kind namespace by namespace.
func InitTestDynamicResourceCacheWithFallbacks(dynClient dynamic.Interface, resources []APIResource, fallbacks []string) error {
	resourcePermsMu.Lock()
	prev, prevExpiry := cachedPermResult, resourcePermsExpiry
	cachedPermResult = &PermissionCheckResult{Perms: &ResourcePermissions{}, ScopeCandidates: fallbacks}
	resourcePermsExpiry = time.Now().Add(time.Hour)
	resourcePermsMu.Unlock()
	defer func() {
		resourcePermsMu.Lock()
		cachedPermResult, resourcePermsExpiry = prev, prevExpiry
		resourcePermsMu.Unlock()
	}()
	return InitTestDynamicResourceCache(dynClient, resources)
}
