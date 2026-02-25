// Package portforward provides shared metrics port-forwarding infrastructure.
// It is used by both the traffic package (for Caretta/Hubble) and the prometheus
// package (for resource metrics), breaking what would otherwise be an import cycle.
package portforward

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// MetricsPortForward manages port-forwarding to metrics services
type MetricsPortForward struct {
	mu sync.RWMutex

	// Active port-forward state
	active      bool
	localPort   int
	namespace   string
	serviceName string
	podName     string
	targetPort  int
	contextName string // K8s context this forward belongs to

	// Control channels
	stopCh chan struct{}
	cancel context.CancelFunc

	// K8s clients
	k8sClient kubernetes.Interface
	k8sConfig *rest.Config
}

// ConnectionInfo contains info about the metrics connection
type ConnectionInfo struct {
	Connected   bool   `json:"connected"`
	LocalPort   int    `json:"localPort,omitempty"`
	Address     string `json:"address,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
	ContextName string `json:"contextName,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Global metrics port-forward instance
var metricsPortForward = &MetricsPortForward{}

// SetK8sClients sets the K8s client and config for port-forwarding.
// Must be called before using port-forward features.
func SetK8sClients(client kubernetes.Interface, config *rest.Config) {
	metricsPortForward.mu.Lock()
	defer metricsPortForward.mu.Unlock()
	metricsPortForward.k8sClient = client
	metricsPortForward.k8sConfig = config
}

// Start starts a port-forward to the specified metrics service.
func Start(ctx context.Context, namespace, serviceName string, targetPort int, contextName string) (*ConnectionInfo, error) {
	metricsPortForward.mu.Lock()
	defer metricsPortForward.mu.Unlock()

	// If already forwarding to the same service in the same context, return existing
	if metricsPortForward.active &&
		metricsPortForward.namespace == namespace &&
		metricsPortForward.serviceName == serviceName &&
		metricsPortForward.contextName == contextName {
		return &ConnectionInfo{
			Connected:   true,
			LocalPort:   metricsPortForward.localPort,
			Address:     fmt.Sprintf("http://localhost:%d", metricsPortForward.localPort),
			Namespace:   namespace,
			ServiceName: serviceName,
			ContextName: contextName,
		}, nil
	}

	// Stop any existing port-forward first
	stopLocked()

	client := metricsPortForward.k8sClient
	config := metricsPortForward.k8sConfig

	if client == nil || config == nil {
		return nil, fmt.Errorf("K8s client not initialized")
	}

	// Find a pod backing the service
	podName, err := findPodForService(ctx, client, namespace, serviceName)
	if err != nil {
		return nil, fmt.Errorf("failed to find pod for service %s: %w", serviceName, err)
	}

	// Find a free local port
	localPort, err := findFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to find free port: %w", err)
	}

	// Create control channels
	stopCh := make(chan struct{})
	pfCtx, cancel := context.WithCancel(context.Background())

	// Store state
	metricsPortForward.active = true
	metricsPortForward.localPort = localPort
	metricsPortForward.namespace = namespace
	metricsPortForward.serviceName = serviceName
	metricsPortForward.podName = podName
	metricsPortForward.targetPort = targetPort
	metricsPortForward.contextName = contextName
	metricsPortForward.stopCh = stopCh
	metricsPortForward.cancel = cancel

	// Start port-forward in background
	readyCh := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		err := runPortForward(pfCtx, client, config, namespace, podName, localPort, targetPort, stopCh, readyCh)
		if err != nil {
			errCh <- err
		}
		close(errCh)

		// Mark as inactive when done
		metricsPortForward.mu.Lock()
		if metricsPortForward.podName == podName && metricsPortForward.localPort == localPort {
			metricsPortForward.active = false
		}
		metricsPortForward.mu.Unlock()
	}()

	// Wait for ready or error (with timeout)
	select {
	case <-readyCh:
		log.Printf("[portforward] Ready: localhost:%d -> %s/%s:%d (context: %s)",
			localPort, namespace, serviceName, targetPort, contextName)
		return &ConnectionInfo{
			Connected:   true,
			LocalPort:   localPort,
			Address:     fmt.Sprintf("http://localhost:%d", localPort),
			Namespace:   namespace,
			ServiceName: serviceName,
			ContextName: contextName,
		}, nil

	case err := <-errCh:
		stopLocked()
		return nil, fmt.Errorf("port-forward failed: %w", err)

	case <-time.After(10 * time.Second):
		stopLocked()
		return nil, fmt.Errorf("port-forward timed out")

	case <-ctx.Done():
		stopLocked()
		return nil, ctx.Err()
	}
}

// Stop stops the active metrics port-forward.
func Stop() {
	metricsPortForward.mu.Lock()
	defer metricsPortForward.mu.Unlock()
	stopLocked()
}

// stopLocked stops the port-forward (caller must hold lock)
func stopLocked() {
	if !metricsPortForward.active {
		return
	}

	log.Printf("[portforward] Stopping: localhost:%d -> %s/%s",
		metricsPortForward.localPort, metricsPortForward.namespace, metricsPortForward.serviceName)

	if metricsPortForward.cancel != nil {
		metricsPortForward.cancel()
	}
	if metricsPortForward.stopCh != nil {
		select {
		case <-metricsPortForward.stopCh:
			// Already closed
		default:
			close(metricsPortForward.stopCh)
		}
	}

	metricsPortForward.active = false
	metricsPortForward.localPort = 0
	metricsPortForward.namespace = ""
	metricsPortForward.serviceName = ""
	metricsPortForward.podName = ""
	metricsPortForward.targetPort = 0
	metricsPortForward.contextName = ""
	metricsPortForward.stopCh = nil
	metricsPortForward.cancel = nil
}

// GetAddress returns the current metrics address if connected.
// Returns empty string if not connected or if the connection is for a different context.
func GetAddress(currentContext string) string {
	metricsPortForward.mu.RLock()
	defer metricsPortForward.mu.RUnlock()

	if !metricsPortForward.active {
		return ""
	}

	// Validate context matches
	if metricsPortForward.contextName != currentContext {
		log.Printf("[portforward] Context mismatch: have %q, want %q",
			metricsPortForward.contextName, currentContext)
		return ""
	}

	return fmt.Sprintf("http://localhost:%d", metricsPortForward.localPort)
}

// GetConnectionInfo returns current connection info.
func GetConnectionInfo() *ConnectionInfo {
	metricsPortForward.mu.RLock()
	defer metricsPortForward.mu.RUnlock()

	if !metricsPortForward.active {
		return &ConnectionInfo{Connected: false}
	}

	return &ConnectionInfo{
		Connected:   true,
		LocalPort:   metricsPortForward.localPort,
		Address:     fmt.Sprintf("http://localhost:%d", metricsPortForward.localPort),
		Namespace:   metricsPortForward.namespace,
		ServiceName: metricsPortForward.serviceName,
		ContextName: metricsPortForward.contextName,
	}
}

// IsConnectedForContext checks if we have an active connection for the given context.
func IsConnectedForContext(contextName string) bool {
	metricsPortForward.mu.RLock()
	defer metricsPortForward.mu.RUnlock()
	return metricsPortForward.active && metricsPortForward.contextName == contextName
}

// runPortForward runs the actual port-forward
func runPortForward(ctx context.Context, client kubernetes.Interface, config *rest.Config,
	namespace, podName string, localPort, targetPort int, stopCh chan struct{}, readyCh chan struct{}) error {

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("portforward").
		VersionedParams(&corev1.PodPortForwardOptions{
			Ports: []int32{int32(targetPort)},
		}, scheme.ParameterCodec)

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return fmt.Errorf("failed to create round tripper: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())

	ports := []string{fmt.Sprintf("%d:%d", localPort, targetPort)}

	pf, err := portforward.New(dialer, ports, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("failed to create port forwarder: %w", err)
	}

	return pf.ForwardPorts()
}

// findPodForService finds a running pod backing the given service
func findPodForService(ctx context.Context, client kubernetes.Interface, namespace, serviceName string) (string, error) {
	svc, err := client.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get service: %w", err)
	}

	if svc.Spec.ClusterIP == "None" || svc.Spec.ClusterIP == "" {
		if svc.Spec.Selector == nil || len(svc.Spec.Selector) == 0 {
			return "", fmt.Errorf("headless service has no selector")
		}
	} else if svc.Spec.Selector == nil || len(svc.Spec.Selector) == 0 {
		return "", fmt.Errorf("service has no selector")
	}

	var selector string
	for k, v := range svc.Spec.Selector {
		if selector != "" {
			selector += ","
		}
		selector += k + "=" + v
	}

	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found matching selector")
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}

	return "", fmt.Errorf("no running pod found for service %s", serviceName)
}

// findFreePort finds an available local port
func findFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port, nil
}
