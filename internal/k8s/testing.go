package k8s

import (
	"maps"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/policyreports"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
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
		OnObservedChange: func(change k8score.ResourceChange, obj, _ any) {
			secretWriteTimes.reconcile(change, obj)
			if cj, ok := obj.(*batchv1.CronJob); ok {
				cronJobScheduleObservations.observe(change.Operation, cj)
			}
		},
	}

	core, err := k8score.NewResourceCache(cfg)
	if err != nil {
		return err
	}

	initialSyncComplete = core.IsSyncComplete()

	resourceCache = &ResourceCache{
		ResourceCache:               core,
		secretsEnabled:              true,
		cronJobScheduleObservations: cronJobScheduleObservations,
		secretWriteTimes:            secretWriteTimes,
	}

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
	cacheMu.Lock()
	defer cacheMu.Unlock()

	enabled := allTestResourceTypes()

	secretWriteTimes := newSecretDataManagerWriteIndex()
	cronJobScheduleObservations := newCronJobScheduleObservationTracker()
	cfg := k8score.CacheConfig{
		Client:        client,
		ResourceTypes: enabled,
		// No deferred types for tests — all sync immediately
		DeferredTypes: map[string]bool{},
		OnTransform: func(obj any) {
			secretWriteTimes.capture(obj)
		},
		OnObservedChange: func(change k8score.ResourceChange, obj, _ any) {
			secretWriteTimes.reconcile(change, obj)
			if cj, ok := obj.(*batchv1.CronJob); ok {
				cronJobScheduleObservations.observe(change.Operation, cj)
			}
		},
	}

	core, err := k8score.NewResourceCache(cfg)
	if err != nil {
		return err
	}

	initialSyncComplete = true

	resourceCache = &ResourceCache{
		ResourceCache:               core,
		secretsEnabled:              true,
		cronJobScheduleObservations: cronJobScheduleObservations,
		secretWriteTimes:            secretWriteTimes,
	}

	// Mark cacheOnce as "already executed" so InitResourceCache is a no-op.
	cacheOnce = new(sync.Once)
	cacheOnce.Do(func() {})

	return nil
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
	// Clear the debt and nudge rather than forcing the active flag: a
	// surviving worker wakes, sees no debt, and exits through its own defer.
	// Forcing the flag false would let a second worker coexist with it. With
	// no worker alive, drain instead — a stray token would give the next
	// test's worker a spurious immediate tick.
	runtimeAuthRecoveryOwed.Store(false)
	if runtimeAuthRecoveryActive.Load() {
		select {
		case runtimeAuthRecoveryNudge <- struct{}{}:
		default:
		}
	} else {
		select {
		case <-runtimeAuthRecoveryNudge:
		default:
		}
	}
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
	cachedPermResult = nil
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
