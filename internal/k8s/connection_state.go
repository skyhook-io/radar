package k8s

import (
	"strings"
	"sync"
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
	ErrorType   string          `json:"errorType,omitempty"` // auth, rbac, network, timeout, unknown
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

// ClassifyError analyzes an error and returns its type (auth, rbac, network, timeout, unknown).
// Uses the kubeconfig AuthInfo to improve classification — timeouts when an
// exec credential plugin is configured are classified as "auth" since the
// plugin hangs when tokens expire.
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

	// Network errors
	if strings.Contains(errLower, "connection refused") ||
		strings.Contains(errLower, "no such host") ||
		strings.Contains(errLower, "dial tcp") ||
		strings.Contains(errLower, "tls handshake timeout") ||
		strings.Contains(errLower, "network is unreachable") ||
		strings.Contains(errLower, "no route to host") {
		return "network"
	}

	// Timeout errors — but when an exec credential plugin is configured,
	// timeouts almost always mean expired credentials (the plugin hangs
	// trying to refresh), not actual network timeouts.
	if strings.Contains(errLower, "i/o timeout") ||
		strings.Contains(errLower, "context deadline exceeded") ||
		strings.Contains(errLower, "timeout") {
		if UsesExecAuth() {
			return "auth"
		}
		return "timeout"
	}

	return "unknown"
}

// IsConnected returns true if currently connected to a cluster
func IsConnected() bool {
	connectionStatusMu.RLock()
	defer connectionStatusMu.RUnlock()
	return connectionStatus.State == StateConnected
}
