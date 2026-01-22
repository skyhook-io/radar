package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/skyhook-io/skyhook-explorer/internal/k8s"
)

// PortForwardSession represents an active port forward
type PortForwardSession struct {
	ID          string    `json:"id"`
	Namespace   string    `json:"namespace"`
	PodName     string    `json:"podName"`
	PodPort     int       `json:"podPort"`
	LocalPort   int       `json:"localPort"`
	ServiceName string    `json:"serviceName,omitempty"` // If forwarding to a service
	StartedAt   time.Time `json:"startedAt"`
	Status      string    `json:"status"` // "running", "stopped", "error"
	Error       string    `json:"error,omitempty"`

	cancel context.CancelFunc
	stopCh chan struct{}
}

// PortForwardManager manages active port forward sessions
type PortForwardManager struct {
	sessions map[string]*PortForwardSession
	mu       sync.RWMutex
	nextID   int
}

var pfManager = &PortForwardManager{
	sessions: make(map[string]*PortForwardSession),
}

// handleListPortForwards returns all active port forward sessions
func (s *Server) handleListPortForwards(w http.ResponseWriter, r *http.Request) {
	pfManager.mu.RLock()
	defer pfManager.mu.RUnlock()

	sessions := make([]*PortForwardSession, 0, len(pfManager.sessions))
	for _, session := range pfManager.sessions {
		sessions = append(sessions, session)
	}

	s.writeJSON(w, sessions)
}

// PortForwardRequest is the request body for creating a port forward
type PortForwardRequest struct {
	Namespace   string `json:"namespace"`
	PodName     string `json:"podName,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
	PodPort     int    `json:"podPort"`
	LocalPort   int    `json:"localPort,omitempty"` // 0 = auto-assign
}

// handleStartPortForward creates a new port forward session
func (s *Server) handleStartPortForward(w http.ResponseWriter, r *http.Request) {
	var req PortForwardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Namespace == "" || req.PodPort == 0 {
		s.writeError(w, http.StatusBadRequest, "namespace and podPort are required")
		return
	}

	if req.PodName == "" && req.ServiceName == "" {
		s.writeError(w, http.StatusBadRequest, "either podName or serviceName is required")
		return
	}

	client := k8s.GetClient()
	config := k8s.GetConfig()
	if client == nil || config == nil {
		s.writeError(w, http.StatusServiceUnavailable, "K8s client not initialized")
		return
	}

	// If service name provided, find a pod backing it
	podName := req.PodName
	if req.ServiceName != "" && podName == "" {
		foundPod, err := findPodForService(r.Context(), req.Namespace, req.ServiceName, req.PodPort)
		if err != nil {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("No pod found for service %s: %v", req.ServiceName, err))
			return
		}
		podName = foundPod
	}

	// Validate that the pod actually exposes this port
	if err := validatePodPort(r.Context(), req.Namespace, podName, req.PodPort); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Find available local port if not specified
	localPort := req.LocalPort
	if localPort == 0 {
		port, err := findFreePort()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "Failed to find free port")
			return
		}
		localPort = port
	}

	// Create session
	pfManager.mu.Lock()
	pfManager.nextID++
	sessionID := fmt.Sprintf("pf-%d", pfManager.nextID)

	ctx, cancel := context.WithCancel(context.Background())
	stopCh := make(chan struct{})

	session := &PortForwardSession{
		ID:          sessionID,
		Namespace:   req.Namespace,
		PodName:     podName,
		PodPort:     req.PodPort,
		LocalPort:   localPort,
		ServiceName: req.ServiceName,
		StartedAt:   time.Now(),
		Status:      "starting",
		cancel:      cancel,
		stopCh:      stopCh,
	}
	pfManager.sessions[sessionID] = session
	pfManager.mu.Unlock()

	// Start port forward in goroutine
	go func() {
		err := runPortForward(ctx, session)
		pfManager.mu.Lock()
		if err != nil {
			session.Status = "error"
			// Make error message more user-friendly
			errMsg := err.Error()
			if strings.Contains(errMsg, "connection refused") {
				errMsg = fmt.Sprintf("Connection refused - nothing listening on port %d in the pod", session.PodPort)
			} else if strings.Contains(errMsg, "lost connection") {
				errMsg = fmt.Sprintf("Lost connection to pod - port %d may not be available", session.PodPort)
			}
			session.Error = errMsg
			log.Printf("Port forward %s error: %v", sessionID, err)
		} else {
			session.Status = "stopped"
		}
		pfManager.mu.Unlock()
	}()

	// Wait briefly for port forward to start
	time.Sleep(100 * time.Millisecond)

	pfManager.mu.RLock()
	session = pfManager.sessions[sessionID]
	pfManager.mu.RUnlock()

	if session.Status == "error" {
		s.writeError(w, http.StatusInternalServerError, session.Error)
		return
	}

	session.Status = "running"
	s.writeJSON(w, session)
}

// handleStopPortForward stops an active port forward session
func (s *Server) handleStopPortForward(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")

	pfManager.mu.Lock()
	session, ok := pfManager.sessions[sessionID]
	if !ok {
		pfManager.mu.Unlock()
		s.writeError(w, http.StatusNotFound, "Session not found")
		return
	}

	// Signal stop
	session.cancel()
	close(session.stopCh)
	session.Status = "stopped"
	delete(pfManager.sessions, sessionID)
	pfManager.mu.Unlock()

	s.writeJSON(w, map[string]string{"status": "stopped"})
}

func runPortForward(ctx context.Context, session *PortForwardSession) error {
	client := k8s.GetClient()
	config := k8s.GetConfig()

	// Build port forward request
	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(session.PodName).
		Namespace(session.Namespace).
		SubResource("portforward").
		VersionedParams(&corev1.PodPortForwardOptions{
			Ports: []int32{int32(session.PodPort)},
		}, scheme.ParameterCodec)

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return fmt.Errorf("failed to create round tripper: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())

	ports := []string{fmt.Sprintf("%d:%d", session.LocalPort, session.PodPort)}
	readyCh := make(chan struct{})

	// Discard output - in production you might want to capture this
	out := io.Discard
	errOut := io.Discard

	pf, err := portforward.New(dialer, ports, session.stopCh, readyCh, out, errOut)
	if err != nil {
		return fmt.Errorf("failed to create port forwarder: %w", err)
	}

	// Run in goroutine and wait for ready or error
	errCh := make(chan error, 1)
	go func() {
		errCh <- pf.ForwardPorts()
	}()

	select {
	case <-readyCh:
		// Port forward is ready
		pfManager.mu.Lock()
		session.Status = "running"
		pfManager.mu.Unlock()
		log.Printf("Port forward %s: localhost:%d -> %s/%s:%d",
			session.ID, session.LocalPort, session.Namespace, session.PodName, session.PodPort)
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}

	// Wait for completion
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}
}

func findPodForService(ctx context.Context, namespace, serviceName string, targetPort int) (string, error) {
	client := k8s.GetClient()

	// Get service
	svc, err := client.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get service: %w", err)
	}

	if svc.Spec.Selector == nil || len(svc.Spec.Selector) == 0 {
		return "", fmt.Errorf("service has no selector")
	}

	// Validate that the service has this port
	portFound := false
	for _, port := range svc.Spec.Ports {
		if int(port.Port) == targetPort || int(port.TargetPort.IntVal) == targetPort {
			portFound = true
			break
		}
	}
	if !portFound {
		return "", fmt.Errorf("service does not expose port %d", targetPort)
	}

	// Build label selector
	var selector string
	for k, v := range svc.Spec.Selector {
		if selector != "" {
			selector += ","
		}
		selector += k + "=" + v
	}

	// Find pods matching selector
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found matching selector")
	}

	// Return first running pod that has the port
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			if podHasPort(&pod, targetPort) {
				return pod.Name, nil
			}
		}
	}

	return "", fmt.Errorf("no running pod found with port %d", targetPort)
}

// validatePodPort checks if the pod actually exposes the requested port
func validatePodPort(ctx context.Context, namespace, podName string, port int) error {
	client := k8s.GetClient()

	pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get pod: %w", err)
	}

	if pod.Status.Phase != corev1.PodRunning {
		return fmt.Errorf("pod is not running (status: %s)", pod.Status.Phase)
	}

	if !podHasPort(pod, port) {
		// List available ports for better error message
		var availablePorts []string
		for _, container := range pod.Spec.Containers {
			for _, p := range container.Ports {
				availablePorts = append(availablePorts, fmt.Sprintf("%d/%s", p.ContainerPort, p.Protocol))
			}
		}
		if len(availablePorts) == 0 {
			return fmt.Errorf("pod does not expose any ports")
		}
		return fmt.Errorf("pod does not expose port %d. Available ports: %s", port, strings.Join(availablePorts, ", "))
	}

	return nil
}

// podHasPort checks if a pod has a container exposing the given port
func podHasPort(pod *corev1.Pod, port int) bool {
	for _, container := range pod.Spec.Containers {
		for _, p := range container.Ports {
			if int(p.ContainerPort) == port {
				return true
			}
		}
	}
	return false
}

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

// GetPortForwardURL returns a helper to format port forward URLs
func GetPortForwardURL(localPort int) string {
	return "http://localhost:" + strconv.Itoa(localPort)
}

// AvailablePort represents a port that can be forwarded
type AvailablePort struct {
	Port          int    `json:"port"`
	Protocol      string `json:"protocol"`
	ContainerName string `json:"containerName"`
	Name          string `json:"name,omitempty"` // Named port
}

// AvailablePortsResponse is the response for the available ports endpoint
type AvailablePortsResponse struct {
	Ports []AvailablePort `json:"ports"`
}

// handleGetAvailablePorts returns the ports available for forwarding on a pod or service
func (s *Server) handleGetAvailablePorts(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	resourceType := chi.URLParam(r, "type") // "pod" or "service"
	name := chi.URLParam(r, "name")

	client := k8s.GetClient()
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "K8s client not initialized")
		return
	}

	var ports []AvailablePort

	switch resourceType {
	case "pod", "pods":
		pod, err := client.CoreV1().Pods(namespace).Get(r.Context(), name, metav1.GetOptions{})
		if err != nil {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("Pod not found: %v", err))
			return
		}

		for _, container := range pod.Spec.Containers {
			for _, p := range container.Ports {
				ports = append(ports, AvailablePort{
					Port:          int(p.ContainerPort),
					Protocol:      string(p.Protocol),
					ContainerName: container.Name,
					Name:          p.Name,
				})
			}
		}

	case "service", "services":
		svc, err := client.CoreV1().Services(namespace).Get(r.Context(), name, metav1.GetOptions{})
		if err != nil {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("Service not found: %v", err))
			return
		}

		for _, p := range svc.Spec.Ports {
			port := AvailablePort{
				Port:     int(p.Port),
				Protocol: string(p.Protocol),
				Name:     p.Name,
			}
			// If targetPort is different, note it
			if p.TargetPort.IntVal > 0 && int(p.TargetPort.IntVal) != int(p.Port) {
				port.Name = fmt.Sprintf("%s (-> %d)", p.Name, p.TargetPort.IntVal)
			}
			ports = append(ports, port)
		}

	default:
		s.writeError(w, http.StatusBadRequest, "Invalid resource type. Use 'pod' or 'service'")
		return
	}

	s.writeJSON(w, AvailablePortsResponse{Ports: ports})
}
