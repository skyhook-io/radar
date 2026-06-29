package k8s

import (
	"context"
	"errors"
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
	ErrorType   string          `json:"errorType,omitempty"` // auth, rbac, network, timeout, unknown
	ProgressMsg string          `json:"progressMessage,omitempty"`
}

// ConnectionChangeCallback is called when the connection status changes
type ConnectionChangeCallback func(status ConnectionStatus)

var (
	connectionStatus      ConnectionStatus
	connectionStatusMu    sync.RWMutex
	connectionCallbacks   []ConnectionChangeCallback
	connectionCallbacksMu sync.RWMutex
	clusterLivenessProbe  = defaultClusterLivenessProbe
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
	if connectionStatus == status {
		connectionStatusMu.Unlock()
		return
	}
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

// MarkDisconnectedIfClusterUnreachable updates the shared connection state when
// a live Kubernetes request proves that the current cluster endpoint is gone.
func MarkDisconnectedIfClusterUnreachable(message string) bool {
	if !isClusterUnreachableMessage(message) {
		return false
	}
	current := GetConnectionStatus()
	if current.State == StateDisconnected && current.Error == message {
		return true
	}
	if clusterReachableNow(2 * time.Second) {
		return false
	}

	status := ConnectionStatus{
		State:       StateDisconnected,
		Context:     current.Context,
		ClusterName: current.ClusterName,
		Error:       message,
		ErrorType:   ClassifyError(errors.New(message)),
	}
	SetConnectionStatus(status)
	return true
}

func clusterReachableNow(timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return clusterLivenessProbe(ctx) == nil
}

func defaultClusterLivenessProbe(ctx context.Context) error {
	client := GetClient()
	if client == nil {
		return errors.New("kubernetes client is not initialized")
	}
	restClient := client.Discovery().RESTClient()
	if restClient == nil {
		return errors.New("kubernetes discovery client is not initialized")
	}
	_, err := restClient.Get().AbsPath("/version").Do(ctx).Raw()
	return err
}

func isClusterUnreachableMessage(message string) bool {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "kubernetes cluster unreachable") ||
		strings.Contains(lower, "cluster unreachable") {
		return true
	}
	return isHelmKubernetesOperationError(lower) && isTransportConnectivityError(lower)
}

func isHelmKubernetesOperationError(lower string) bool {
	kubernetesOperationPrefixes := []string{
		"failed to build helm restclientgetter",
		"failed to initialize helm action config",
		"failed to build helm action config",
		"failed to list helm releases",
		"failed to get helm release",
		"failed to get release",
		"failed to get current release",
		"failed to get current values",
		"failed to get helm release history",
		"failed to get helm release manifest",
		"failed to get helm release values",
		"failed to get manifest for revision",
		"failed to get values for revision",
		"failed to get release revision",
		"failed to inspect release storage namespaces",
		"failed to inspect existing release",
		"failed to preview values change",
		"failed to apply values",
		"rollback failed",
		"uninstall failed",
		"upgrade failed",
		"install failed",
	}
	for _, prefix := range kubernetesOperationPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func isTransportConnectivityError(lower string) bool {
	transportMarkers := []string{
		"connection refused",
		"no such host",
		"dial tcp",
		"i/o timeout",
		"context deadline exceeded",
		"no route to host",
		"connection reset by peer",
		"tls handshake timeout",
	}
	for _, marker := range transportMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
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
	// Exception: "cluster unreachable" means the connectivity test ran
	// (exec plugin didn't block), so the cluster itself is down/offline.
	if strings.Contains(errLower, "i/o timeout") ||
		strings.Contains(errLower, "context deadline exceeded") ||
		strings.Contains(errLower, "timeout") {
		if UsesExecAuth() && !strings.Contains(errLower, "cluster unreachable") {
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
