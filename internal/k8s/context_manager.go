package k8s

import (
	"fmt"
	"log"
	"sync"
)

// ContextSwitchCallback is called when the context is switched
type ContextSwitchCallback func(newContext string)

// ContextSwitchProgressCallback is called with progress updates during context switch
type ContextSwitchProgressCallback func(message string)

// HelmResetFunc is called to reset the Helm client
type HelmResetFunc func()

// HelmReinitFunc is called to reinitialize the Helm client
type HelmReinitFunc func(kubeconfig string) error

var (
	contextSwitchCallbacks         []ContextSwitchCallback
	contextSwitchProgressCallbacks []ContextSwitchProgressCallback
	contextSwitchMu                sync.RWMutex
	helmResetFunc                  HelmResetFunc
	helmReinitFunc                 HelmReinitFunc
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

// PerformContextSwitch orchestrates a full context switch:
// 1. Stops all caches
// 2. Switches the K8s client to the new context
// 3. Reinitializes all caches
// 4. Notifies all registered callbacks
func PerformContextSwitch(newContext string, historyLimit int, historyPath string) error {
	log.Printf("Performing context switch to %q", newContext)
	reportProgress("Stopping caches...")

	// Step 1: Stop all caches (order matters - stop dependent caches first)
	log.Println("Stopping resource cache...")
	ResetResourceCache()

	log.Println("Stopping dynamic resource cache...")
	ResetDynamicResourceCache()

	log.Println("Stopping resource discovery...")
	ResetResourceDiscovery()

	log.Println("Stopping change history...")
	ResetChangeHistory()

	// Reset Helm client if registered
	contextSwitchMu.RLock()
	resetFunc := helmResetFunc
	contextSwitchMu.RUnlock()
	if resetFunc != nil {
		log.Println("Stopping Helm client...")
		resetFunc()
	}

	// Step 2: Switch the K8s client to the new context
	reportProgress("Connecting to cluster...")
	log.Printf("Switching K8s client to context %q...", newContext)
	if err := SwitchContext(newContext); err != nil {
		return fmt.Errorf("failed to switch context: %w", err)
	}

	// Step 3: Reinitialize all caches with new client
	reportProgress("Discovering API resources...")
	log.Println("Reinitializing resource discovery...")
	if err := ReinitResourceDiscovery(); err != nil {
		return fmt.Errorf("failed to reinit resource discovery: %w", err)
	}

	reportProgress("Loading custom resources...")
	log.Println("Reinitializing dynamic resource cache...")
	if err := ReinitDynamicResourceCache(); err != nil {
		return fmt.Errorf("failed to reinit dynamic resource cache: %w", err)
	}

	reportProgress("Loading workloads...")
	log.Println("Reinitializing resource cache...")
	if err := ReinitResourceCache(); err != nil {
		return fmt.Errorf("failed to reinit resource cache: %w", err)
	}

	reportProgress("Loading change history...")
	log.Println("Reinitializing change history...")
	ReinitChangeHistory(historyLimit, historyPath)

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
