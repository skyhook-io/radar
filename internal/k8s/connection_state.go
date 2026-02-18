package k8s

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// ConnectionState represents the current connection status to the cluster
type ConnectionState string

const (
	StateConnected    ConnectionState = "connected"
	StateDisconnected ConnectionState = "disconnected"
	StateConnecting   ConnectionState = "connecting"
)

// ConnectionStatus holds detailed information about the cluster connection
type ConnectionStatus struct {
	State       ConnectionState `json:"state"`
	Context     string          `json:"context"`
	ClusterName string          `json:"clusterName,omitempty"`
	Error       string          `json:"error,omitempty"`
	ErrorType   string          `json:"errorType,omitempty"` // auth, network, timeout, unknown
	ProgressMsg string          `json:"progressMessage,omitempty"`
}

// ConnectionChangeCallback is called when the connection status changes
type ConnectionChangeCallback func(status ConnectionStatus)

var (
	connectionStatus    ConnectionStatus
	connectionStatusMu  sync.RWMutex
	connectionCallbacks []ConnectionChangeCallback
	connectionCallbacksMu sync.RWMutex
)

// GetConnectionStatus returns the current connection status
func GetConnectionStatus() ConnectionStatus {
	connectionStatusMu.RLock()
	defer connectionStatusMu.RUnlock()
	return connectionStatus
}

// SetConnectionStatus updates the connection status and notifies callbacks
func SetConnectionStatus(status ConnectionStatus) {
	connectionStatusMu.Lock()
	connectionStatus = status
	connectionStatusMu.Unlock()

	// Notify callbacks
	connectionCallbacksMu.RLock()
	callbacks := make([]ConnectionChangeCallback, len(connectionCallbacks))
	copy(callbacks, connectionCallbacks)
	connectionCallbacksMu.RUnlock()

	for _, cb := range callbacks {
		cb(status)
	}
}

// UpdateConnectionProgress updates the progress message while connecting
func UpdateConnectionProgress(msg string) {
	connectionStatusMu.Lock()
	status := connectionStatus
	status.ProgressMsg = msg
	connectionStatus = status
	connectionStatusMu.Unlock()

	// Notify callbacks
	connectionCallbacksMu.RLock()
	callbacks := make([]ConnectionChangeCallback, len(connectionCallbacks))
	copy(callbacks, connectionCallbacks)
	connectionCallbacksMu.RUnlock()

	for _, cb := range callbacks {
		cb(status)
	}
}

// OnConnectionChange registers a callback to be called when connection status changes
func OnConnectionChange(callback ConnectionChangeCallback) {
	connectionCallbacksMu.Lock()
	defer connectionCallbacksMu.Unlock()
	connectionCallbacks = append(connectionCallbacks, callback)
}

// ClassifyError analyzes an error and returns its type (auth, network, timeout, unknown)
func ClassifyError(err error) string {
	if err == nil {
		return ""
	}

	errStr := err.Error()
	errLower := strings.ToLower(errStr)

	// RBAC errors (403 Forbidden - authenticated but insufficient permissions)
	if strings.Contains(errLower, "forbidden") {
		return "rbac"
	}

	// Authentication errors (401 Unauthorized - bad credentials)
	if strings.Contains(errLower, "unauthorized") ||
		strings.Contains(errLower, "authentication required") ||
		strings.Contains(errLower, "token has expired") ||
		strings.Contains(errLower, "credentials") ||
		strings.Contains(errLower, "exec plugin") ||
		strings.Contains(errLower, "gke-gcloud-auth-plugin") ||
		strings.Contains(errLower, "unable to connect to the server") && strings.Contains(errLower, "oauth2") {
		return "auth"
	}

	// Network errors (including http2 connection drops)
	if strings.Contains(errLower, "connection refused") ||
		strings.Contains(errLower, "no such host") ||
		strings.Contains(errLower, "dial tcp") ||
		strings.Contains(errLower, "tls handshake timeout") ||
		strings.Contains(errLower, "network is unreachable") ||
		strings.Contains(errLower, "no route to host") ||
		strings.Contains(errLower, "http2: client connection lost") ||
		strings.Contains(errLower, "http2: client connection force closed") ||
		strings.Contains(errLower, "connection reset by peer") ||
		strings.Contains(errLower, "use of closed network connection") ||
		strings.Contains(errLower, "broken pipe") ||
		strings.Contains(errLower, "transport is closing") {
		return "network"
	}

	// Timeout errors
	if strings.Contains(errLower, "i/o timeout") ||
		strings.Contains(errLower, "context deadline exceeded") ||
		strings.Contains(errLower, "timeout") {
		return "timeout"
	}

	return "unknown"
}

// IsConnected returns true if currently connected to a cluster.
// Also returns true during reconnection so that handlers can serve stale cached data
// instead of returning 503 errors.
func IsConnected() bool {
	connectionStatusMu.RLock()
	defer connectionStatusMu.RUnlock()
	return connectionStatus.State == StateConnected || connectionStatus.State == StateReconnecting
}

// IsFullyConnected returns true only when the connection is fully established
// (not reconnecting). Use this when you need a live API server connection.
func IsFullyConnected() bool {
	connectionStatusMu.RLock()
	defer connectionStatusMu.RUnlock()
	return connectionStatus.State == StateConnected
}

// StateReconnecting indicates the system is reconnecting after a connection drop.
// Distinct from StateConnecting which is the initial connection attempt.
const StateReconnecting ConnectionState = "reconnecting"

// IsConnectionError returns true if the error indicates a broken connection
// (http2 drop, connection reset, etc.) as opposed to an application-level error.
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "http2: client connection lost") ||
		strings.Contains(errStr, "http2: client connection force closed") ||
		strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "transport is closing") ||
		strings.Contains(errStr, "tls handshake timeout") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "network is unreachable")
}

// --- Connection health watchdog ---

var (
	watchdogStopCh chan struct{}
	watchdogOnce   sync.Once
	watchdogMu     sync.Mutex

	// consecutiveFailures tracks how many health checks have failed in a row
	consecutiveFailures   int
	consecutiveFailuresMu sync.Mutex

	// reconnectInProgress prevents multiple goroutines from reconnecting simultaneously
	reconnectInProgress   bool
	reconnectMu           sync.Mutex
)

const (
	// healthCheckInterval is how often the watchdog pings the API server
	healthCheckInterval = 10 * time.Second

	// healthCheckTimeout is the maximum time for a single health check
	healthCheckTimeout = 5 * time.Second

	// failureThreshold is how many consecutive failures before marking disconnected
	failureThreshold = 3
)

// StartConnectionWatchdog starts a background goroutine that periodically
// checks API server health and triggers reconnection when needed.
func StartConnectionWatchdog() {
	watchdogMu.Lock()
	defer watchdogMu.Unlock()

	// Stop any existing watchdog
	if watchdogStopCh != nil {
		close(watchdogStopCh)
	}
	watchdogStopCh = make(chan struct{})
	watchdogOnce = sync.Once{}

	stopCh := watchdogStopCh
	go runWatchdog(stopCh)
}

// StopConnectionWatchdog stops the health monitoring goroutine.
func StopConnectionWatchdog() {
	watchdogMu.Lock()
	defer watchdogMu.Unlock()

	if watchdogStopCh != nil {
		close(watchdogStopCh)
		watchdogStopCh = nil
	}
}

// runWatchdog is the main watchdog loop.
func runWatchdog(stopCh <-chan struct{}) {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	log.Println("[watchdog] Connection health monitoring started")

	for {
		select {
		case <-stopCh:
			log.Println("[watchdog] Connection health monitoring stopped")
			return
		case <-ticker.C:
			// Only check when we think we're connected
			if !IsConnected() {
				continue
			}
			checkConnectionHealth(stopCh)
		}
	}
}

// checkConnectionHealth performs a lightweight API server health check.
func checkConnectionHealth(stopCh <-chan struct{}) {
	client := GetClient()
	if client == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	// Use a cheap API call: server version (no RBAC needed, cached by most API servers)
	_, err := client.Discovery().ServerVersion()

	consecutiveFailuresMu.Lock()
	defer consecutiveFailuresMu.Unlock()

	if err != nil {
		consecutiveFailures++
		if consecutiveFailures >= failureThreshold {
			log.Printf("[watchdog] API server unreachable (%d consecutive failures): %v", consecutiveFailures, err)
			// Transition to reconnecting state
			currentStatus := GetConnectionStatus()
			if currentStatus.State == StateConnected {
				SetConnectionStatus(ConnectionStatus{
					State:       StateReconnecting,
					Context:     currentStatus.Context,
					ClusterName: currentStatus.ClusterName,
					Error:       err.Error(),
					ErrorType:   ClassifyError(err),
					ProgressMsg: "Connection lost, attempting to reconnect...",
				})
				// Trigger reconnection in background
				go triggerReconnection(stopCh)
			}
		}
		_ = ctx // suppress unused warning
	} else {
		if consecutiveFailures > 0 {
			log.Printf("[watchdog] API server connection restored after %d failures", consecutiveFailures)
		}
		consecutiveFailures = 0

		// If we were in reconnecting state, transition back to connected
		currentStatus := GetConnectionStatus()
		if currentStatus.State == StateReconnecting {
			SetConnectionStatus(ConnectionStatus{
				State:       StateConnected,
				Context:     currentStatus.Context,
				ClusterName: currentStatus.ClusterName,
			})
		}
	}
}

// triggerReconnection attempts to reconnect to the API server with exponential backoff.
// Only one reconnection attempt runs at a time.
func triggerReconnection(stopCh <-chan struct{}) {
	reconnectMu.Lock()
	if reconnectInProgress {
		reconnectMu.Unlock()
		return
	}
	reconnectInProgress = true
	reconnectMu.Unlock()

	defer func() {
		reconnectMu.Lock()
		reconnectInProgress = false
		reconnectMu.Unlock()
	}()

	client := GetClient()
	if client == nil {
		return
	}

	// Exponential backoff: 1s, 2s, 4s, 8s, 16s, 30s max
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second
	maxAttempts := 20

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-stopCh:
			return
		case <-time.After(backoff):
		}

		ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
		_, err := client.Discovery().ServerVersion()
		cancel()

		if err == nil {
			log.Printf("[watchdog] Reconnection successful after %d attempts", attempt)

			consecutiveFailuresMu.Lock()
			consecutiveFailures = 0
			consecutiveFailuresMu.Unlock()

			currentStatus := GetConnectionStatus()
			SetConnectionStatus(ConnectionStatus{
				State:       StateConnected,
				Context:     currentStatus.Context,
				ClusterName: currentStatus.ClusterName,
			})

			// Trigger staggered reconnect for dynamic informers
			// The http2 connection drop kills all reflectors, so they need to be restarted
			if dc := GetDynamicResourceCache(); dc != nil {
				go dc.StaggeredReconnect()
			}
			return
		}

		log.Printf("[watchdog] Reconnection attempt %d failed: %v (next in %v)", attempt, err, backoff)
		_ = ctx // suppress unused warning

		// Exponential backoff with cap
		backoff = backoff * 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	log.Printf("[watchdog] Reconnection failed after %d attempts, marking disconnected", maxAttempts)
	currentStatus := GetConnectionStatus()
	SetConnectionStatus(ConnectionStatus{
		State:       StateDisconnected,
		Context:     currentStatus.Context,
		ClusterName: currentStatus.ClusterName,
		Error:       "Failed to reconnect after multiple attempts",
		ErrorType:   "network",
	})
}
