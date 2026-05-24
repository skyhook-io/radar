package k8s

import (
	"sync"

	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

// InitTestResourceCache creates a resource cache from a fake or test client,
// bypassing RBAC checks and the normal Initialize/InitResourceCache flow.
// All resource types are enabled. Call ResetTestState to clean up.
//
// This is intended for integration tests only.
func InitTestResourceCache(client kubernetes.Interface) error {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	enabled := map[string]bool{
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
	}

	cfg := k8score.CacheConfig{
		Client:        client,
		ResourceTypes: enabled,
		// No deferred types for tests — all sync immediately
		DeferredTypes: map[string]bool{},
	}

	core, err := k8score.NewResourceCache(cfg)
	if err != nil {
		return err
	}

	initialSyncComplete = true

	resourceCache = &ResourceCache{
		ResourceCache:  core,
		secretsEnabled: true,
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

	discoveryMu.Lock()
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
	discoveryOnce = new(sync.Once)
	discoveryOnce.Do(func() {})
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

// ResetTestState tears down the resource cache and resets all package-level
// state so the next test starts clean.
//
// This is intended for integration tests only.
func ResetTestState() {
	// Reset resource cache
	ResetResourceCache()

	// Reset connection state
	connectionStatusMu.Lock()
	connectionStatus = ConnectionStatus{}
	connectionStatusMu.Unlock()

	// Reset connection callbacks
	connectionCallbacksMu.Lock()
	connectionCallbacks = nil
	connectionCallbacksMu.Unlock()

	// Reset capabilities cache
	capabilitiesMu.Lock()
	cachedCapabilities = nil
	capabilitiesMu.Unlock()

	// Reset resource permissions cache
	resourcePermsMu.Lock()
	cachedPermResult = nil
	resourcePermsMu.Unlock()

	// Reset operation context so stale cancellations don't leak between tests
	CancelOngoingOperations()
}
