package k8s

import (
	"sync"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
)

// InitTestResourceCache creates a resource cache from a fake or test client,
// bypassing RBAC checks and the normal Initialize/InitResourceCache flow.
// All resource types are enabled. Call ResetTestState to clean up.
//
// This is intended for integration tests only.
func InitTestResourceCache(client kubernetes.Interface) error {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	factory := informers.NewSharedInformerFactoryWithOptions(client, 0,
		informers.WithTransform(dropManagedFields),
	)

	stopCh := make(chan struct{})
	changes := make(chan ResourceChange, 10000)

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
	}

	// Touch every informer so the factory creates them before Start.
	factory.Core().V1().Pods().Informer()
	factory.Core().V1().Services().Informer()
	factory.Core().V1().Nodes().Informer()
	factory.Core().V1().Namespaces().Informer()
	factory.Core().V1().ConfigMaps().Informer()
	factory.Core().V1().Secrets().Informer()
	factory.Core().V1().Events().Informer()
	factory.Core().V1().PersistentVolumeClaims().Informer()
	factory.Core().V1().PersistentVolumes().Informer()
	factory.Apps().V1().Deployments().Informer()
	factory.Apps().V1().DaemonSets().Informer()
	factory.Apps().V1().StatefulSets().Informer()
	factory.Apps().V1().ReplicaSets().Informer()
	factory.Networking().V1().Ingresses().Informer()
	factory.Batch().V1().Jobs().Informer()
	factory.Batch().V1().CronJobs().Informer()
	factory.Autoscaling().V2().HorizontalPodAutoscalers().Informer()
	factory.Storage().V1().StorageClasses().Informer()
	factory.Policy().V1().PodDisruptionBudgets().Informer()

	factory.Start(stopCh)
	factory.WaitForCacheSync(stopCh)

	initialSyncComplete = true

	deferredSynced := make(map[string]bool)
	for k := range deferredResources {
		deferredSynced[k] = true
	}
	deferredDone := make(chan struct{})
	close(deferredDone)

	resourceCache = &ResourceCache{
		factory:          factory,
		changes:          changes,
		stopCh:           stopCh,
		secretsEnabled:   true,
		enabledResources: enabled,
		deferredSynced:   deferredSynced,
		deferredDone:     deferredDone,
	}

	// Mark cacheOnce as "already executed" so InitResourceCache is a no-op.
	cacheOnce = new(sync.Once)
	cacheOnce.Do(func() {})

	return nil
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
