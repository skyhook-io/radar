package traffic

import (
	"context"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/portforward"
)

// MetricsConnectionInfo is an alias for portforward.ConnectionInfo.
// Kept for API compatibility with traffic handlers and sources.
type MetricsConnectionInfo = portforward.ConnectionInfo

// SetK8sClients delegates to the portforward package.
func SetK8sClients(client kubernetes.Interface, config *rest.Config) {
	portforward.SetK8sClients(client, config)
}

// StartMetricsPortForward delegates to the portforward package.
func StartMetricsPortForward(ctx context.Context, namespace, serviceName string, targetPort int, contextName string) (*portforward.ConnectionInfo, error) {
	return portforward.Start(ctx, namespace, serviceName, targetPort, contextName)
}

// StopMetricsPortForward delegates to the portforward package.
func StopMetricsPortForward() {
	portforward.Stop()
}

// GetMetricsAddress delegates to the portforward package.
func GetMetricsAddress(currentContext string) string {
	return portforward.GetAddress(currentContext)
}

// GetConnectionInfo delegates to the portforward package.
func GetConnectionInfo() *portforward.ConnectionInfo {
	return portforward.GetConnectionInfo()
}

// IsConnectedForContext delegates to the portforward package.
func IsConnectedForContext(contextName string) bool {
	return portforward.IsConnectedForContext(contextName)
}
