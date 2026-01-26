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
)

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

// TestClusterConnection tests connectivity to the current cluster
// Returns an error if the cluster is unreachable within the timeout
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

	// Try to get server version - this is a lightweight call that tests connectivity
	_, err = testClient.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("cluster unreachable: %w", err)
	}
	return nil
}

// PerformContextSwitch orchestrates a full context switch:
// 1. Stops all caches
// 2. Switches the K8s client to the new context
// 3. Tests connectivity to ensure cluster is reachable
// 4. Reinitializes all caches
// 5. Notifies all registered callbacks
func PerformContextSwitch(newContext string) error {
	log.Printf("Performing context switch to %q", newContext)
	reportProgress("Stopping caches...")

	// Step 1: Stop all caches (order matters - stop dependent caches first)
	log.Println("Stopping resource cache...")
	ResetResourceCache()

	log.Println("Stopping dynamic resource cache...")
	ResetDynamicResourceCache()

	log.Println("Stopping resource discovery...")
	ResetResourceDiscovery()


	// Reset timeline store if registered
	contextSwitchMu.RLock()
	tlResetFunc := timelineResetFunc
	contextSwitchMu.RUnlock()
	if tlResetFunc != nil {
		log.Println("Stopping timeline store...")
		tlResetFunc()
	}

	// Reset Helm client if registered
	contextSwitchMu.RLock()
	resetFunc := helmResetFunc
	contextSwitchMu.RUnlock()
	if resetFunc != nil {
		log.Println("Stopping Helm client...")
		resetFunc()
	}

	// Reset traffic manager if registered
	contextSwitchMu.RLock()
	trResetFunc := trafficResetFunc
	contextSwitchMu.RUnlock()
	if trResetFunc != nil {
		log.Println("Stopping traffic manager...")
		trResetFunc()
	}

	// Step 2: Switch the K8s client to the new context
	reportProgress("Connecting to cluster...")
	log.Printf("Switching K8s client to context %q...", newContext)
	if err := SwitchContext(newContext); err != nil {
		return fmt.Errorf("failed to switch context: %w", err)
	}

	// Invalidate capabilities cache - RBAC permissions may differ between clusters
	InvalidateCapabilitiesCache()

	// Step 2.5: Test connectivity before proceeding with cache initialization
	// This prevents hanging if the cluster is unreachable
	reportProgress("Testing cluster connectivity...")
	log.Println("Testing cluster connectivity...")
	connCtx, connCancel := context.WithTimeout(context.Background(), ConnectionTestTimeout)
	defer connCancel()
	if err := TestClusterConnection(connCtx); err != nil {
		return fmt.Errorf("cluster connection failed: %w", err)
	}
	log.Println("Cluster connectivity verified")

	// Step 3: Reinitialize all caches with new client
	// Order matters: typed cache first (provides change channel), then dynamic cache
	reportProgress("Loading workloads...")
	log.Println("Reinitializing resource cache...")
	if err := ReinitResourceCache(); err != nil {
		return fmt.Errorf("failed to reinit resource cache: %w", err)
	}

	reportProgress("Discovering API resources...")
	log.Println("Reinitializing resource discovery...")
	if err := ReinitResourceDiscovery(); err != nil {
		return fmt.Errorf("failed to reinit resource discovery: %w", err)
	}

	reportProgress("Loading custom resources...")
	log.Println("Reinitializing dynamic resource cache...")
	changeCh := GetResourceCache().ChangesRaw()
	if err := ReinitDynamicResourceCache(changeCh); err != nil {
		return fmt.Errorf("failed to reinit dynamic resource cache: %w", err)
	}

	// Warm up common CRDs so they appear in timeline
	WarmupCommonCRDs()

	// Reinit timeline store before change history (so it's ready to receive events)
	contextSwitchMu.RLock()
	tlReinitFunc := timelineReinitFunc
	contextSwitchMu.RUnlock()
	if tlReinitFunc != nil {
		reportProgress("Reinitializing timeline store...")
		log.Println("Reinitializing timeline store...")
		if err := tlReinitFunc(); err != nil {
			// Timeline store failure is non-fatal (will use fallback)
			log.Printf("Warning: failed to reinit timeline store: %v", err)
		}
	}


	// Reinit Helm client if registered
	contextSwitchMu.RLock()
	reinitFunc := helmReinitFunc
	contextSwitchMu.RUnlock()
	if reinitFunc != nil {
		reportProgress("Loading Helm releases...")
		log.Println("Reinitializing Helm client...")
		if err := reinitFunc(GetKubeconfigPath()); err != nil {
			// Helm client failure is non-fatal
			log.Printf("Warning: failed to reinit Helm client: %v", err)
		}
	}

	// Reinit traffic manager if registered
	contextSwitchMu.RLock()
	trReinitFunc := trafficReinitFunc
	contextSwitchMu.RUnlock()
	if trReinitFunc != nil {
		log.Println("Reinitializing traffic manager...")
		if err := trReinitFunc(); err != nil {
			// Traffic manager failure is non-fatal
			log.Printf("Warning: failed to reinit traffic manager: %v", err)
		}
	}

	// Step 4: Notify all registered callbacks
	reportProgress("Building topology...")
	log.Printf("Context switch to %q complete, notifying callbacks...", newContext)
	contextSwitchMu.RLock()
	callbacks := make([]ContextSwitchCallback, len(contextSwitchCallbacks))
	copy(callbacks, contextSwitchCallbacks)
	contextSwitchMu.RUnlock()

	for _, callback := range callbacks {
		callback(newContext)
	}

	return nil
}
