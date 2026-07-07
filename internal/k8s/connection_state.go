package k8s

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	ErrorType   string          `json:"errorType,omitempty"` // config, auth, rbac, network, timeout, tls, unknown
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
	return isTransportNetworkMessage(lower) || isTransportTimeoutMessage(lower)
}

func isTransportNetworkMessage(lower string) bool {
	transportMarkers := []string{
		"connection refused",
		"no such host",
		"dial tcp",
		"dial udp",
		"no route to host",
		"connection reset by peer",
		"network is unreachable",
	}
	for _, marker := range transportMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isTransportTimeoutMessage(lower string) bool {
	timeoutMarkers := []string{
		"i/o timeout",
		"context deadline exceeded",
		"tls handshake timeout",
		"timed out",
		"timeout",
	}
	for _, marker := range timeoutMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isAuthErrorMessage(lower string) bool {
	authMarkers := []string{
		"unauthorized",
		"authentication required",
		"token has expired",
		"expired token",
		"sso session",
		"sso token",
		"ssoproviderinvalidtoken",
		"aws sso login",
		"getting credentials",
		"provide credentials",
		"credentials expired",
		"credential plugin",
		"exec credential",
		"exec plugin",
		"gke-gcloud-auth-plugin",
	}
	for _, marker := range authMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.Contains(lower, "unable to connect to the server") && strings.Contains(lower, "oauth2")
}

func isRBACErrorMessage(lower string) bool {
	return strings.Contains(lower, " is forbidden") ||
		strings.Contains(lower, "forbidden:") ||
		strings.Contains(lower, "cannot list resource") ||
		strings.Contains(lower, "cannot get resource")
}

func isConfigErrorMessage(lower string) bool {
	configMarkers := []string{
		"no context configured",
		"no current context",
		"current-context is not set",
		"context was not found for specified context",
		"no configuration has been provided",
		"failed to build kubeconfig",
		"no valid kubeconfig files found",
		"no usable context found",
		"no contexts found",
		"k8s config not initialized",
		"kubernetes client is not initialized",
		"kubernetes discovery client is not initialized",
	}
	for _, marker := range configMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isTLSCertificateMessage(lower string) bool {
	return strings.Contains(lower, "x509:") ||
		strings.Contains(lower, "certificate signed by unknown authority") ||
		strings.Contains(lower, "certificate is valid for") ||
		strings.Contains(lower, "cannot validate certificate") ||
		strings.Contains(lower, "certificate has expired") ||
		strings.Contains(lower, "certificate is not yet valid")
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

// ClassifyError analyzes a cluster connection error and returns its type.
func ClassifyError(err error) string {
	if err == nil {
		return ""
	}

	if apierrors.IsForbidden(err) {
		return "rbac"
	}
	if apierrors.IsUnauthorized(err) {
		return "auth"
	}
	if apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	errStr := err.Error()
	errLower := strings.ToLower(errStr)

	// RBAC/auth markers may be carried inside generic transport wrappers such as
	// url.Error when client-go exec credential plugins fail before a request is sent.
	if isConfigErrorMessage(errLower) {
		return "config"
	}
	if isRBACErrorMessage(errLower) {
		return "rbac"
	}
	if isAuthErrorMessage(errLower) {
		return "auth"
	}

	if isTLSCertificateError(err) || isTLSCertificateMessage(errLower) {
		return "tls"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "network"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "network"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "network"
	}

	// Network errors
	if isTransportNetworkMessage(errLower) {
		return "network"
	}

	// Bare deadlines lack enough evidence to distinguish a hung exec plugin from
	// an unreachable API endpoint, so keep them in the timeout bucket.
	if isTransportTimeoutMessage(errLower) {
		return "timeout"
	}

	return "unknown"
}

func isTLSCertificateError(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var unknownAuthorityPtr *x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityPtr) {
		return true
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return true
	}
	var hostnameErrPtr *x509.HostnameError
	if errors.As(err, &hostnameErrPtr) {
		return true
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return true
	}
	var certInvalidPtr *x509.CertificateInvalidError
	if errors.As(err, &certInvalidPtr) {
		return true
	}
	return false
}

// IsConnected returns true if currently connected to a cluster
func IsConnected() bool {
	connectionStatusMu.RLock()
	defer connectionStatusMu.RUnlock()
	return connectionStatus.State == StateConnected
}
