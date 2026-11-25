package k8s

import (
	"fmt"
	"maps"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/policyreports"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// Test fixtures often supply only one client. Copy the bundle as well as the
// connection so an already captured fixture keeps its original clients.
func copyTestConnectionLocked() *Connection {
	var clients sharedKubernetesClients
	if installedConn.Load() != nil && installedConn.Load().clients != nil {
		clients = *installedConn.Load().clients
	}
	c := installedConnectionLocked(installedConn.Load().Config(), &clients)
	c.discovery.Store(installedConn.Load().Discovery())
	// Partial fixture updates supply another dependency of the same cluster.
	if prev := Current(); prev != nil {
		c.cache.Store(prev.cacheState())
		c.dynamicCache.Store(prev.dynamicCache.Load())
	}
	return c
}

// swapTestConnectionLocked installs next in place of the test's connection.
// Test helpers change one dependency of a connection the test may already
// have admitted, so an admitted connection's ID and serving status move to the
// replacement and bound requests see the change. clientMu must be held.
func swapTestConnectionLocked(next *Connection) {
	prev := installedConn.Load()
	installedConn.Store(next)
	connectionStatusMu.Lock()
	if serving != nil && serving == prev && next != nil {
		next.id = prev.id
		serving = next
	}
	connectionStatusMu.Unlock()
}

func restoreTestConnection(c *Connection) {
	clientMu.Lock()
	swapTestConnectionLocked(c)
	clientMu.Unlock()
}

func setTestDiscovery(d *ResourceDiscovery) func() {
	clientMu.Lock()
	prev := installedConn.Load()
	next := copyTestConnectionLocked()
	next.discovery.Store(d)
	swapTestConnectionLocked(next)
	clientMu.Unlock()
	return func() { restoreTestConnection(prev) }
}

// Test cache fixtures can start without clients or rebuild after a standalone
// reset. Production installs the fresh connection in ResetAllSubsystems.
func testCacheState() *connectionCache {
	clientMu.Lock()
	defer clientMu.Unlock()
	conn := Current()
	if conn == nil || !conn.cacheIsCurrent() {
		next := copyTestConnectionLocked()
		next.cache.Store(nil)
		if conn != nil {
			next.perms.Store(conn.perms.Load())
		}
		swapTestConnectionLocked(next)
		conn = next
	}
	return conn.cacheState()
}

// InitLoadTestResourceCache creates a resource cache from a fake client using
// the live path's deferred-informer split, so the critical/deferred phases and
// the warmup window they drive actually exist. InitTestResourceCache syncs
// everything at once, which is what unit tests want and makes it useless for
// measuring anything that depends on startup phases.
//
// Intended for load-test harnesses, not unit tests.
func InitLoadTestResourceCache(client kubernetes.Interface) error {
	state := testCacheState()

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

	return state.installTestCache(&ResourceCache{
		ResourceCache:               core,
		secretsEnabled:              true,
		cronJobScheduleObservations: cronJobScheduleObservations,
		secretWriteTimes:            secretWriteTimes,
	}, core.IsSyncComplete())
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
	state := testCacheState()

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

	return state.installTestCache(&ResourceCache{
		ResourceCache:               core,
		secretsEnabled:              true,
		cronJobScheduleObservations: cronJobScheduleObservations,
		secretWriteTimes:            secretWriteTimes,
	}, core.IsSyncComplete())
}

func (c *ResourceCache) SetTestAPIDiscoveryPending(pending bool) {
	c.apiDiscoveryPending.Store(pending)
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
	state := testCacheState()

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

	cache := &ResourceCache{
		ResourceCache:               core,
		secretsEnabled:              true,
		cronJobScheduleObservations: cronJobScheduleObservations,
		secretWriteTimes:            secretWriteTimes,
	}
	if err := state.installTestCache(cache, true); err != nil {
		return err
	}
	waitForInformerStatuses(cache)

	return nil
}

// InitTestResourceCacheFromConfig installs a cache built from cfg as given and
// does not wait for it to sync, for tests whose informers must stay loading
// (a namespace that never answers, a deadline that has to fire).
func InitTestResourceCacheFromConfig(cfg k8score.CacheConfig) error {
	state := testCacheState()
	core, err := k8score.NewResourceCache(cfg)
	if err != nil {
		return err
	}
	return state.installTestCache(&ResourceCache{ResourceCache: core}, true)
}

func (s *connectionCache) installTestCache(cache *ResourceCache, complete bool) error {
	s.once.Do(func() {})
	cache.generation = clusterBuildSequence.Add(1)
	if !s.promote(cache, complete) {
		return fmt.Errorf("cluster changed during cache initialization")
	}
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
	if err := installTestDynamicClient(dynClient, resources); err != nil {
		return err
	}
	return InitDynamicResourceCache(nil)
}

func installTestDynamicClient(dynClient dynamic.Interface, resources []APIResource) error {
	// Bootstrap discovery from a fake clientset so NewResourceDiscovery has a
	// non-nil discovery client; AddAPIResource then registers the test-only
	// GVRs (e.g. serving.knative.dev/Service) the test depends on. The fake
	// serves the same resources, so a later discovery refresh finds them
	// again instead of dropping them.
	fake := fakeclientset.NewSimpleClientset()
	fake.Resources = servedAPIResources(resources)
	fakeDisc := fake.Discovery()
	core, err := k8score.NewResourceDiscovery(fakeDisc)
	if err != nil {
		return err
	}
	for _, r := range resources {
		core.AddAPIResource(r)
	}

	clientMu.Lock()
	c := copyTestConnectionLocked()
	c.dynamicCache.Store(nil)
	c.clients.dynamic = dynClient
	c.clients.informerDynamic = nil
	c.discovery.Store(&ResourceDiscovery{ResourceDiscovery: core})
	// A test may seed the same cluster fixture's permissions before adding
	// its dynamic client. Carry only its published result to the new fixture.
	if prev := Current(); prev != nil {
		old := prev.permissions()
		old.resourcePermsMu.RLock()
		state := c.permissions()
		state.cachedPermResult = clonePermissionCheckResult(old.cachedPermResult)
		state.resourcePermsExpiry = old.resourcePermsExpiry
		old.resourcePermsMu.RUnlock()
	}
	swapTestConnectionLocked(c)
	// Permission fixtures may temporarily copy this connection while building its cache.
	c.dynamicCacheState()
	clientMu.Unlock()
	return nil
}

// servedAPIResources is resources as a discovery endpoint lists them.
func servedAPIResources(resources []APIResource) []*metav1.APIResourceList {
	byGV := map[string]*metav1.APIResourceList{}
	var lists []*metav1.APIResourceList
	for _, r := range resources {
		gv := schema.GroupVersion{Group: r.Group, Version: r.Version}.String()
		list := byGV[gv]
		if list == nil {
			list = &metav1.APIResourceList{GroupVersion: gv}
			byGV[gv] = list
			lists = append(lists, list)
		}
		list.APIResources = append(list.APIResources, metav1.APIResource{Name: r.Name, Kind: r.Kind, Namespaced: r.Namespaced, Verbs: r.Verbs})
	}
	return lists
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

// SetTestDynamicObjectReadSyncWait sets how long a read of one object waits
// for an unsynced dynamic informer, for dynamic caches initialized after the
// call. The production wait, 2 seconds, is what a test of a read that never
// loads would otherwise sit through. Returns a restore func.
func SetTestDynamicObjectReadSyncWait(d time.Duration) func() {
	previous := dynamicObjectReadSyncWait
	dynamicObjectReadSyncWait = d
	return func() { dynamicObjectReadSyncWait = previous }
}

// ResetTestDynamicState tears down the dynamic cache + discovery singletons
// and clears the dynamic client. Pairs with InitTestDynamicResourceCache.
func ResetTestDynamicState() {
	ResetDynamicResourceCache()
	clientMu.Lock()
	next := copyTestConnectionLocked()
	next.discovery.Store(nil)
	next.dynamicCache.Store(nil)
	next.clients.dynamic = nil
	next.clients.informerDynamic = nil
	swapTestConnectionLocked(next)
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
	swapTestConnectionLocked(copyTestConnectionLocked())
	clientMu.Unlock()
	return prev
}

// HoldTestClientWriteLock simulates kubeconfig file I/O under the writer lock.
func HoldTestClientWriteLock() func() {
	clientMu.Lock()
	return clientMu.Unlock
}

// SetTestResourceDiscovery installs discovery backed by client and returns a
// restore func.
func SetTestResourceDiscovery(client discovery.DiscoveryInterface) (func(), error) {
	core, err := k8score.NewResourceDiscovery(client)
	if err != nil {
		return nil, err
	}
	return setTestDiscovery(&ResourceDiscovery{ResourceDiscovery: core}), nil
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
	swapTestConnectionLocked(copyTestConnectionLocked())
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
	prev := installedConn.Load().Client()
	next := copyTestConnectionLocked()
	next.clients.clientset = c
	next.clients.informerClientset = nil
	swapTestConnectionLocked(next)
	clientMu.Unlock()
	return prev
}

// SetTestInformerClient installs request as the request clientset and
// informer as the informer clientset of the same client set, the pairing
// production builds, and returns a restore func.
func SetTestInformerClient(request *kubernetes.Clientset, informer kubernetes.Interface) func() {
	clientMu.Lock()
	prev := installedConn.Load()
	next := copyTestConnectionLocked()
	next.clients.clientset, next.clients.informerClientset = request, informer
	swapTestConnectionLocked(next)
	clientMu.Unlock()
	return func() { restoreTestConnection(prev) }
}

// ReconnectTestClient installs c as a new connection under the current context
// name, as a reconnect does: a connection that was serving is retired and the
// new one admitted with a new ID. Returns a func that restores the previous state.
func ReconnectTestClient(c *kubernetes.Clientset) func() {
	restore := SnapshotTestConnection()
	wasServing := ServingConnectionID() != ""
	clientMu.Lock()
	next := copyTestConnectionLocked()
	next.clients.clientset, next.clients.informerClientset = c, nil
	next.clients.generation = clusterBuildSequence.Add(1)
	next.cache.Store(nil)
	next.dynamicCache.Store(nil)
	clientMu.Unlock()
	RetireServingConnection()
	clientMu.Lock()
	installedConn.Store(next)
	clientMu.Unlock()
	if wasServing {
		status := GetConnectionStatus()
		status.State = StateConnected
		SetConnectionStatus(status)
	}
	return restore
}

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
	prev := installedConn.Load().Config()
	next := copyTestConnectionLocked()
	next.config = c
	swapTestConnectionLocked(next)
	clientMu.Unlock()
	return prev
}

// SetTestProfileSource supplies the source identity used by local configuration
// handlers. SetTestConfig supplies its matching REST transport separately.
func SetTestProfileSource(source, name, user string) func() {
	clientMu.Lock()
	oldBinding, oldName, oldFile, oldSourceName, oldSourceConfig, previous := contextBinding, contextName, activeSourceFile, activeSourceName, activeSourceConfig, installedConn.Load()
	contextBinding = sourceContextBinding(source, name)
	contextName, activeSourceFile, activeSourceName = name, source, name
	activeSourceConfig = clientcmdapi.NewConfig()
	activeSourceConfig.Contexts[name] = &clientcmdapi.Context{AuthInfo: user}
	next := copyTestConnectionLocked()
	next.clients.generation++
	swapTestConnectionLocked(next)
	clientMu.Unlock()
	return func() {
		clientMu.Lock()
		contextBinding, contextName, activeSourceFile, activeSourceName, activeSourceConfig = oldBinding, oldName, oldFile, oldSourceName, oldSourceConfig
		swapTestConnectionLocked(previous)
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

// SetTestPolicyReportIndex publishes a PolicyReport index directly, bypassing
// CRD discovery and the informer warmup that normally build it.
//
// Publishing an index is also what makes GetPolicyReportStatus report Ready, so
// this is the whole seam: the policy surfaces read exactly these two together.
// Without it the per-policy handlers return their empty response before any of
// the authorization logic runs, and a test against them would assert nothing
// while appearing to cover the endpoint.
//
// Seed after installing the test caches. The returned function restores the fixture.
func SetTestPolicyReportIndex(idx *policyreports.Index) func() {
	restore := SnapshotTestConnection()
	clientMu.Lock()
	conn := Current()
	if conn == nil {
		conn = copyTestConnectionLocked()
		swapTestConnectionLocked(conn)
	}
	clientMu.Unlock()

	s := &conn.dynamicCacheState().reports
	s.mu.Lock()
	snapshot := conn.policyReportSnapshot()
	snapshot.index = idx
	if idx != nil {
		snapshot.status.Status = KyvernoStatusReady
		snapshot.status.ReasonCode = ""
	}
	old := s.published.Swap(&snapshot)
	s.mu.Unlock()
	return func() {
		s.published.CompareAndSwap(&snapshot, old)
		restore()
	}
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

	// Stop the informers before dropping the fixture that owns them.
	ResetDynamicResourceCache()
	ResetMetricsHistory()
	ResetResourceCache()

	// Reset connection state
	connectionStatusMu.Lock()
	connectionStatus = ConnectionStatus{}
	serving = nil
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
	installedConn.Store(nil)
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
	prev := installedConn.Load()
	next := copyTestConnectionLocked()
	next.clients.dynamic, next.clients.informerDynamic = client, nil
	swapTestConnectionLocked(next)
	clientMu.Unlock()
	return func() { restoreTestConnection(prev) }
}

// SetTestPermissionResult publishes a permission probe result as if a probe had
// just produced it. Returns a restore func.
//
// The cache is otherwise only writable by a real probe, so without this seam a
// test in another package cannot reach any of the code that reads it - the
// handlers fall through to their "no probe data yet" path and assert nothing
// while appearing to cover the endpoint.
func SetTestPermissionResult(result *PermissionCheckResult) func() {
	clientMu.Lock()
	prev := Current()
	conn := copyTestConnectionLocked()
	state := conn.permissions()
	state.cachedPermResult = result
	state.resourcePermsExpiry = time.Now().Add(time.Minute)
	swapTestConnectionLocked(conn)
	clientMu.Unlock()
	cache := GetResourceCache()
	var previousBound *PermissionCheckResult
	if cache != nil {
		previousBound = cache.permissionResult
		cache.permissionResult = clonePermissionCheckResult(result)
	}

	return func() {
		if cache != nil {
			cache.permissionResult = previousBound
		}
		restoreTestConnection(prev)
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
	if err := installTestDynamicClient(dynClient, resources); err != nil {
		return err
	}
	defer SetTestPermissionResult(&PermissionCheckResult{Perms: &ResourcePermissions{}, ScopeCandidates: fallbacks})()
	return InitDynamicResourceCache(nil)
}

func SetTestContextOperationInProgress(active bool) func() {
	var value int32
	if active {
		value = 1
	}
	previous := activeContextOperations.Swap(value)
	return func() { activeContextOperations.Store(previous) }
}

// SwapResourceCacheForTest installs a fixture with its own cache lifecycle.
// Tests can hide or rebuild its cache without closing the shared fixture
// that the restore function reinstalls.
func SwapResourceCacheForTest(rc *ResourceCache) (restore func()) {
	clientMu.Lock()
	previous := Current()
	next := copyTestConnectionLocked()
	state := newConnectionCache()
	state.promoted.Store(rc)
	if previous != nil {
		next.perms.Store(previous.perms.Load())
		state.initialSyncComplete.Store(previous.cacheState().initialSyncComplete.Load())
	}
	next.cache.Store(state)
	swapTestConnectionLocked(next)
	clientMu.Unlock()
	return func() { restoreTestConnection(previous) }
}

// InitTestReplayClients retains the supplied discovery client so refreshes see
// the frozen catalog, rather than reverting to an empty bootstrap catalog.
func InitTestReplayClients(client dynamic.Interface, catalog discovery.DiscoveryInterface) error {
	clientMu.Lock()
	next := copyTestConnectionLocked()
	next.dynamicCache.Store(nil)
	next.clients.dynamic, next.clients.informerDynamic = client, nil
	swapTestConnectionLocked(next)
	clientMu.Unlock()
	if _, err := SetTestResourceDiscovery(catalog); err != nil {
		return err
	}
	return InitDynamicResourceCache(nil)
}

// InstallTestServingConnection installs a named connection's config, dynamic
// client and discovery and admits it, the way a switch ends: whatever was
// serving is retired first. It returns the new connection's ID.
func InstallTestServingConnection(name string, cfg *rest.Config, dyn dynamic.Interface, disc discovery.DiscoveryInterface) (string, error) {
	RetireServingConnection()
	clientMu.Lock()
	contextName = name
	installedConn.Store(installedConnectionLocked(cfg, &sharedKubernetesClients{dynamic: dyn}))
	clientMu.Unlock()
	if _, err := SetTestResourceDiscovery(disc); err != nil {
		return "", err
	}
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: name})
	return ServingConnectionID(), nil
}

// SnapshotTestConnection records the connection status, serving connection,
// clients and discovery, and returns a func that restores them, so a test
// that installs or retires connections leaves the package as it found it.
func SnapshotTestConnection() func() {
	// clientMu before connectionStatusMu, the order swapTestConnectionLocked uses.
	clientMu.RLock()
	connectionStatusMu.RLock()
	status, admitted, closed := connectionStatus, serving, admissionsClosed
	// The ID by value: re-admitting the same connection changes it in place.
	admittedID := servingIDLocked()
	previous, name := installedConn.Load(), contextName
	connectionStatusMu.RUnlock()
	clientMu.RUnlock()
	return func() {
		clientMu.Lock()
		connectionStatusMu.Lock()
		connectionStatus, serving, admissionsClosed = status, admitted, closed
		if admitted != nil {
			admitted.id = admittedID
		}
		installedConn.Store(previous)
		contextName = name
		connectionStatusMu.Unlock()
		clientMu.Unlock()
	}
}
