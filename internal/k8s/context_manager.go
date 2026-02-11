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
// 1. Tears down all subsystems
// 2. Switches the K8s client to the new context
// 3. Tests connectivity to ensure cluster is reachable
// 4. Reinitializes all subsystems (same sequence as initial boot)
// 5. Notifies all registered callbacks
func PerformContextSwitch(newContext string) error {
	log.Printf("Performing context switch to %q", newContext)

	// Step 1: Tear down all subsystems
	reportProgress("Stopping caches...")
	ResetAllSubsystems()

	// Step 2: Switch the K8s client to the new context
	reportProgress("Connecting to cluster...")
	log.Printf("Switching K8s client to context %q...", newContext)
	if err := SwitchContext(newContext); err != nil {
		return fmt.Errorf("failed to switch context: %w", err)
	}

	// Invalidate caches - permissions and cluster info may differ between clusters
	InvalidateCapabilitiesCache()
	InvalidateResourcePermissionsCache()
	InvalidateServerVersionCache()

	// Step 3: Test connectivity before proceeding with initialization
	reportProgress("Testing cluster connectivity...")
	log.Println("Testing cluster connectivity...")
	connCtx, connCancel := context.WithTimeout(context.Background(), ConnectionTestTimeout)
	defer connCancel()
	if err := TestClusterConnection(connCtx); err != nil {
		return fmt.Errorf("cluster connection failed: %w", err)
	}
	log.Println("Cluster connectivity verified")

	// Step 4: Initialize all subsystems (same function as initial boot)
	if err := InitAllSubsystems(reportProgress); err != nil {
		return fmt.Errorf("subsystem init failed: %w", err)
	}

	// Step 5: Notify all registered callbacks
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
