package traffic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	observerpb "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	hubbleRelayService    = "hubble-relay"
	hubbleRelayLabel      = "k8s-app=hubble-relay"
	hubbleRelayCertSecret = "hubble-relay-client-certs"
)

// HubbleSource implements TrafficSource for Hubble/Cilium
type HubbleSource struct {
	k8sClient      kubernetes.Interface
	grpcConn       *grpc.ClientConn
	observerClient observerpb.ObserverClient
	localPort      int    // Port-forward local port
	currentContext string // K8s context for port-forward validation
	relayNamespace string // Discovered namespace where hubble-relay lives
	relayPort      int    // Hubble relay container port (for port-forward)
	servicePort    int    // Hubble relay service port (443 hints TLS, 80 hints plaintext)
	useTLS         bool   // Whether TLS certs are available
	tlsConfig      *tls.Config
	isConnected    bool
	mu             sync.RWMutex
}

// NewHubbleSource creates a new Hubble traffic source
func NewHubbleSource(client kubernetes.Interface) *HubbleSource {
	return &HubbleSource{
		k8sClient: client,
	}
}

// Name returns the source identifier
func (h *HubbleSource) Name() string {
	return "hubble"
}

// Detect checks if Hubble is available in the cluster using label-based discovery
func (h *HubbleSource) Detect(ctx context.Context) (*DetectionResult, error) {
	result := &DetectionResult{
		Available: false,
	}

	// Step 1: Find hubble-relay pods by label across ALL namespaces
	relayPods, err := h.k8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: hubbleRelayLabel,
	})
	if err != nil {
		return result, fmt.Errorf("failed to search for hubble-relay pods: %w", err)
	}

	if len(relayPods.Items) == 0 {
		result.Message = "Hubble Relay not found. Install Cilium with Hubble enabled for traffic visibility."
		return result, nil
	}

	// Count running pods and get the namespace
	var relayNamespace string
	runningPods := 0
	for _, pod := range relayPods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			runningPods++
			if relayNamespace == "" {
				relayNamespace = pod.Namespace
			}
		}
	}

	if runningPods == 0 {
		result.Message = fmt.Sprintf("Hubble Relay pods found (%d) but none are running", len(relayPods.Items))
		return result, nil
	}

	log.Printf("[hubble] Found hubble-relay in namespace %q with %d running pod(s)", relayNamespace, runningPods)

	// Step 2: Find the hubble-relay service in the same namespace
	relaySvc, err := h.k8sClient.CoreV1().Services(relayNamespace).Get(ctx, hubbleRelayService, metav1.GetOptions{})
	if err != nil {
		result.Message = fmt.Sprintf("Hubble Relay pods running but service not found in namespace %s", relayNamespace)
		return result, nil
	}

	// Step 3: Check for TLS certs
	tlsConfig, useTLS := h.loadTLSConfig(ctx, relayNamespace)

	// Step 4: Determine service port and whether it's TLS
	servicePort := 80
	if len(relaySvc.Spec.Ports) > 0 {
		servicePort = int(relaySvc.Spec.Ports[0].Port)
	}

	// Port 443 typically means TLS is required
	if servicePort == 443 && !useTLS {
		result.Message = fmt.Sprintf("Hubble Relay requires TLS (port 443) but client certs not found in secret %s/%s", relayNamespace, hubbleRelayCertSecret)
		return result, nil
	}

	// Step 5: Store discovered configuration
	h.mu.Lock()
	h.relayNamespace = relayNamespace
	h.relayPort = h.resolveTargetPort(ctx, relaySvc)
	h.servicePort = servicePort
	h.useTLS = useTLS
	h.tlsConfig = tlsConfig
	h.mu.Unlock()

	// Determine if this is GKE native Hubble
	isNative := h.isNativeHubble(ctx)

	result.Available = true
	result.Native = isNative

	tlsStatus := "plaintext"
	if useTLS {
		tlsStatus = "TLS"
	}
	result.Message = fmt.Sprintf("Hubble Relay detected in %s with %d running pod(s) (%s)", relayNamespace, runningPods, tlsStatus)

	// Try to get version from Cilium config
	ciliumConfig, err := h.k8sClient.CoreV1().ConfigMaps(relayNamespace).Get(ctx, "cilium-config", metav1.GetOptions{})
	if err == nil && ciliumConfig.Labels != nil {
		if ver, ok := ciliumConfig.Labels["cilium.io/version"]; ok {
			result.Version = ver
		}
	}

	return result, nil
}

// loadTLSConfig attempts to load TLS credentials from the hubble-relay-client-certs secret
func (h *HubbleSource) loadTLSConfig(ctx context.Context, namespace string) (*tls.Config, bool) {
	secret, err := h.k8sClient.CoreV1().Secrets(namespace).Get(ctx, hubbleRelayCertSecret, metav1.GetOptions{})
	if err != nil {
		log.Printf("[hubble] TLS cert secret not found in %s/%s: %v", namespace, hubbleRelayCertSecret, err)
		return nil, false
	}

	caCert, hasCa := secret.Data["ca.crt"]
	tlsCert, hasCert := secret.Data["tls.crt"]
	tlsKey, hasKey := secret.Data["tls.key"]

	if !hasCa || !hasCert || !hasKey {
		log.Printf("[hubble] TLS secret missing required keys (need ca.crt, tls.crt, tls.key)")
		return nil, false
	}

	// Parse CA cert
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		log.Printf("[hubble] Failed to parse CA certificate")
		return nil, false
	}

	// Parse client cert
	clientCert, err := tls.X509KeyPair(tlsCert, tlsKey)
	if err != nil {
		log.Printf("[hubble] Failed to parse client certificate: %v", err)
		return nil, false
	}

	// ServerName must match the certificate's SAN
	// GKE uses: *.gke-managed-dpv2-observability.svc.cluster.local
	// Standard Cilium uses: *.hubble-grpc.cilium.io or similar
	serverName := fmt.Sprintf("hubble-relay.%s.svc.cluster.local", namespace)

	tlsConfig := &tls.Config{
		RootCAs:      caCertPool,
		Certificates: []tls.Certificate{clientCert},
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}

	log.Printf("[hubble] Loaded TLS credentials from %s/%s (ServerName: %s)", namespace, hubbleRelayCertSecret, serverName)
	return tlsConfig, true
}

// discoverTLSServerName probes the server certificate to discover the correct TLS ServerName.
// This handles environments like AKS where the Hubble Relay cert has a different SAN
// (e.g., *.hubble-relay.cilium.io) than the default k8s service DNS name.
func (h *HubbleSource) discoverTLSServerName(address string) (string, error) {
	probeCfg := &tls.Config{
		InsecureSkipVerify: true,
	}
	// Include client certs in case the server requires mTLS
	if h.tlsConfig != nil && len(h.tlsConfig.Certificates) > 0 {
		probeCfg.Certificates = h.tlsConfig.Certificates
	}

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", address, probeCfg)
	if err != nil {
		return "", fmt.Errorf("failed to probe server certificate: %w", err)
	}
	defer conn.Close()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", fmt.Errorf("server returned no certificates")
	}

	serverCert := certs[0]
	if len(serverCert.DNSNames) == 0 {
		return "", fmt.Errorf("server certificate has no DNS SANs")
	}

	san := serverCert.DNSNames[0]
	if strings.HasPrefix(san, "*.") {
		// Wildcard cert (e.g., *.hubble-relay.cilium.io) — construct a concrete match
		san = "relay" + san[1:]
	}
	return san, nil
}

// isNativeHubble checks if this is GKE Dataplane V2 (native Hubble)
func (h *HubbleSource) isNativeHubble(ctx context.Context) bool {
	// Check for GKE by looking at node provider ID
	nodes, err := h.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil || len(nodes.Items) == 0 {
		return false
	}

	node := nodes.Items[0]

	// GKE nodes have gce:// provider ID
	if strings.HasPrefix(node.Spec.ProviderID, "gce://") {
		// Check for Dataplane V2 specific labels or annotations
		if _, ok := node.Labels["cloud.google.com/gke-nodepool"]; ok {
			return true
		}
	}

	return false
}

// resolveTargetPort resolves the actual container port from the service
// The service may use a named targetPort (e.g., "grpc") that maps to a container port
func (h *HubbleSource) resolveTargetPort(ctx context.Context, svc *corev1.Service) int {
	if len(svc.Spec.Ports) == 0 {
		return 80
	}

	svcPort := svc.Spec.Ports[0]

	// If targetPort is a number, use it directly
	if svcPort.TargetPort.IntValue() > 0 {
		return svcPort.TargetPort.IntValue()
	}

	// If targetPort is a named port, we need to find the actual port from pods
	if svcPort.TargetPort.StrVal != "" {
		// Find a pod backing this service
		if svc.Spec.Selector != nil {
			var labelSelector string
			for k, v := range svc.Spec.Selector {
				if labelSelector != "" {
					labelSelector += ","
				}
				labelSelector += k + "=" + v
			}

			pods, err := h.k8sClient.CoreV1().Pods(svc.Namespace).List(ctx, metav1.ListOptions{
				LabelSelector: labelSelector,
				Limit:         1,
			})
			if err == nil && len(pods.Items) > 0 {
				pod := pods.Items[0]
				for _, container := range pod.Spec.Containers {
					for _, port := range container.Ports {
						if port.Name == svcPort.TargetPort.StrVal {
							log.Printf("[hubble] Resolved named port %q to %d", svcPort.TargetPort.StrVal, port.ContainerPort)
							return int(port.ContainerPort)
						}
					}
				}
			}
		}
	}

	// Fallback to service port
	if svcPort.Port > 0 {
		return int(svcPort.Port)
	}
	return 80
}

// Connect establishes connection to Hubble Relay via port-forward and gRPC
func (h *HubbleSource) Connect(ctx context.Context, contextName string) (*MetricsConnectionInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	namespace := h.relayNamespace
	if namespace == "" {
		namespace = "kube-system" // fallback
	}

	// If already connected to the same context, verify connection is still valid
	if h.grpcConn != nil && h.currentContext == contextName {
		// Test the connection
		if h.testConnection(ctx) {
			return &MetricsConnectionInfo{
				Connected:   true,
				LocalPort:   h.localPort,
				Address:     fmt.Sprintf("localhost:%d", h.localPort),
				Namespace:   namespace,
				ServiceName: hubbleRelayService,
				ContextName: contextName,
			}, nil
		}
		// Connection lost, clean up
		h.closeConnectionLocked()
	}

	// Clear stale state if context changed
	if h.currentContext != contextName {
		h.closeConnectionLocked()
		h.currentContext = contextName
	}

	// Get the relay port from detection if not already set
	if h.relayPort == 0 {
		relaySvc, err := h.k8sClient.CoreV1().Services(namespace).Get(ctx, hubbleRelayService, metav1.GetOptions{})
		if err != nil {
			return &MetricsConnectionInfo{
				Connected: false,
				Error:     fmt.Sprintf("Hubble Relay service not found in %s: %v", namespace, err),
			}, nil
		}
		h.relayPort = h.resolveTargetPort(ctx, relaySvc)
	}

	// Start port-forward to Hubble Relay
	log.Printf("[hubble] Starting port-forward to %s/%s:%d", namespace, hubbleRelayService, h.relayPort)
	connInfo, err := StartMetricsPortForward(ctx, namespace, hubbleRelayService, h.relayPort, contextName)
	if err != nil {
		return &MetricsConnectionInfo{
			Connected:   false,
			Namespace:   namespace,
			ServiceName: hubbleRelayService,
			Error:       fmt.Sprintf("Failed to start port-forward: %v", err),
		}, nil
	}

	if !connInfo.Connected {
		return connInfo, nil
	}

	h.localPort = connInfo.LocalPort
	grpcAddr := fmt.Sprintf("localhost:%d", h.localPort)

	// Use service port as heuristic: port 443 suggests TLS, otherwise try plaintext first
	// This avoids unnecessary latency from failed connection attempts
	tryTLSFirst := h.servicePort == 443 && h.tlsConfig != nil

	var conn *grpc.ClientConn
	var lastErr error

	// Define connection attempt functions
	tryPlaintext := func() bool {
		log.Printf("[hubble] Connecting to gRPC at %s (plaintext)", grpcAddr)
		var err error
		conn, err = grpc.NewClient(grpcAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			lastErr = err
			return false
		}
		h.grpcConn = conn
		h.observerClient = observerpb.NewObserverClient(conn)
		h.isConnected = true
		if h.testConnection(ctx) {
			log.Printf("[hubble] Connected to Hubble Relay at %s (plaintext)", grpcAddr)
			return true
		}
		lastErr = fmt.Errorf("plaintext gRPC connection test failed")
		h.closeConnectionLocked()
		return false
	}

	tryTLSWith := func(serverName string) bool {
		if h.tlsConfig == nil {
			return false
		}
		cfg := h.tlsConfig.Clone()
		cfg.ServerName = serverName
		log.Printf("[hubble] Connecting to gRPC at %s (TLS, ServerName: %s)", grpcAddr, serverName)
		var err error
		conn, err = grpc.NewClient(grpcAddr,
			grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		)
		if err != nil {
			lastErr = fmt.Errorf("TLS connection failed: %w", err)
			return false
		}
		h.grpcConn = conn
		h.observerClient = observerpb.NewObserverClient(conn)
		h.isConnected = true
		if h.testConnection(ctx) {
			log.Printf("[hubble] Connected to Hubble Relay at %s (TLS)", grpcAddr)
			return true
		}
		h.closeConnectionLocked()
		return false
	}

	tryTLS := func() bool {
		if h.tlsConfig == nil {
			return false
		}

		if tryTLSWith(h.tlsConfig.ServerName) {
			return true
		}

		// TLS may have failed due to ServerName mismatch (e.g., AKS cert uses *.hubble-relay.cilium.io).
		// Probe the server certificate to discover the correct ServerName.
		discoveredName, err := h.discoverTLSServerName(grpcAddr)
		if err != nil {
			log.Printf("[hubble] Could not discover server name: %v", err)
			lastErr = fmt.Errorf("TLS gRPC connection test failed")
			return false
		}
		if discoveredName == h.tlsConfig.ServerName {
			lastErr = fmt.Errorf("TLS gRPC connection test failed")
			return false
		}

		log.Printf("[hubble] Retrying TLS with discovered ServerName: %s (was: %s)", discoveredName, h.tlsConfig.ServerName)

		if tryTLSWith(discoveredName) {
			return true
		}

		lastErr = fmt.Errorf("TLS gRPC connection test failed (tried default and discovered ServerName %s)", discoveredName)
		return false
	}

	// Try connections in order based on service port heuristic
	var connected bool
	if tryTLSFirst {
		connected = tryTLS() || tryPlaintext()
	} else {
		connected = tryPlaintext() || tryTLS()
	}

	if connected {
		return &MetricsConnectionInfo{
			Connected:   true,
			LocalPort:   h.localPort,
			Address:     grpcAddr,
			Namespace:   namespace,
			ServiceName: hubbleRelayService,
			ContextName: contextName,
		}, nil
	}

	// Both attempts failed
	StopMetricsPortForward()
	h.localPort = 0
	return &MetricsConnectionInfo{
		Connected: false,
		Error:     fmt.Sprintf("Failed to connect to Hubble Relay: %v", lastErr),
	}, nil
}

// testConnection tests the gRPC connection by calling ServerStatus
func (h *HubbleSource) testConnection(ctx context.Context) bool {
	if h.observerClient == nil {
		return false
	}

	testCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, err := h.observerClient.ServerStatus(testCtx, &observerpb.ServerStatusRequest{})
	if err != nil {
		log.Printf("[hubble] Connection test failed: %v", err)
		return false
	}
	return true
}

// closeConnectionLocked closes the gRPC connection (caller must hold lock)
func (h *HubbleSource) closeConnectionLocked() {
	if h.grpcConn != nil {
		h.grpcConn.Close()
		h.grpcConn = nil
	}
	h.observerClient = nil
	h.isConnected = false
	h.localPort = 0
}

// GetFlows retrieves flows from Hubble via gRPC
func (h *HubbleSource) GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error) {
	h.mu.RLock()
	client := h.observerClient
	connected := h.isConnected
	h.mu.RUnlock()

	if !connected || client == nil {
		// Not connected yet - return empty with message
		return &FlowsResponse{
			Source:    "hubble",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   "Not connected to Hubble Relay. Call Connect() first or use the Traffic view to establish connection.",
		}, nil
	}

	flows, err := h.fetchFlowsViaGRPC(ctx, opts)
	if err != nil {
		log.Printf("[hubble] gRPC error: %v", err)
		return &FlowsResponse{
			Source:    "hubble",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   fmt.Sprintf("Failed to fetch flows: %v", err),
		}, nil
	}

	return &FlowsResponse{
		Source:    "hubble",
		Timestamp: time.Now(),
		Flows:     flows,
	}, nil
}

// fetchFlowsViaGRPC fetches flows using gRPC client
func (h *HubbleSource) fetchFlowsViaGRPC(ctx context.Context, opts FlowOptions) ([]Flow, error) {
	h.mu.RLock()
	client := h.observerClient
	h.mu.RUnlock()

	if client == nil {
		return nil, fmt.Errorf("not connected to Hubble Relay")
	}

	// Build request
	req := &observerpb.GetFlowsRequest{
		Number: 1000, // Default limit
		Follow: false,
	}

	if opts.Limit > 0 {
		req.Number = uint64(opts.Limit)
	}

	// Add namespace filter if specified
	// Use separate filters for source OR destination (each filter is AND within itself,
	// but multiple filters are OR'd together)
	if opts.Namespace != "" {
		req.Whitelist = []*flowpb.FlowFilter{
			{SourcePod: []string{opts.Namespace + "/"}},
			{DestinationPod: []string{opts.Namespace + "/"}},
		}
	}

	// Add time filter based on Since
	if opts.Since > 0 {
		since := time.Now().Add(-opts.Since)
		req.Since = timestamppb.New(since)
	}

	// Create context with timeout
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	stream, err := client.GetFlows(reqCtx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get flows stream: %w", err)
	}

	var flows []Flow
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Check if we got any flows before the error
			if len(flows) > 0 {
				log.Printf("[hubble] Stream ended with partial results: %v", err)
				break
			}
			return nil, fmt.Errorf("stream error: %w", err)
		}

		// Extract flow from response
		pbFlow := resp.GetFlow()
		if pbFlow == nil {
			continue
		}

		flow := convertHubbleFlow(pbFlow)
		flows = append(flows, flow)
	}

	log.Printf("[hubble] Retrieved %d flows", len(flows))
	return flows, nil
}

// convertHubbleFlow converts a Hubble protobuf Flow to our internal Flow type
func convertHubbleFlow(pbFlow *flowpb.Flow) Flow {
	// Extract IP addresses safely (IP may be nil for some flow types)
	var srcIP, dstIP string
	if ip := pbFlow.GetIP(); ip != nil {
		srcIP = ip.GetSource()
		dstIP = ip.GetDestination()
	}

	flow := Flow{
		Source:      convertEndpoint(pbFlow.GetSource(), srcIP),
		Destination: convertEndpoint(pbFlow.GetDestination(), dstIP),
		Verdict:     strings.ToLower(pbFlow.GetVerdict().String()),
		Connections: 1,
	}

	// Extract L4 info
	l4 := pbFlow.GetL4()
	if l4 != nil {
		if tcp := l4.GetTCP(); tcp != nil {
			flow.Protocol = "tcp"
			flow.Port = int(tcp.GetDestinationPort())
		} else if udp := l4.GetUDP(); udp != nil {
			flow.Protocol = "udp"
			flow.Port = int(udp.GetDestinationPort())
		} else if icmpv4 := l4.GetICMPv4(); icmpv4 != nil {
			flow.Protocol = "icmp"
		} else if icmpv6 := l4.GetICMPv6(); icmpv6 != nil {
			flow.Protocol = "icmpv6"
		} else if sctp := l4.GetSCTP(); sctp != nil {
			flow.Protocol = "sctp"
			flow.Port = int(sctp.GetDestinationPort())
		}
	}

	// Extract L7 info if available
	l7 := pbFlow.GetL7()
	if l7 != nil {
		if http := l7.GetHttp(); http != nil {
			flow.L7Protocol = "HTTP"
			flow.HTTPMethod = http.GetMethod()
			flow.HTTPPath = http.GetUrl()
			flow.HTTPStatus = int(http.GetCode())
		} else if dns := l7.GetDns(); dns != nil {
			flow.L7Protocol = "DNS"
		}
	}

	// Parse timestamp
	if ts := pbFlow.GetTime(); ts != nil {
		flow.LastSeen = ts.AsTime()
	} else {
		flow.LastSeen = time.Now()
	}

	return flow
}

// convertEndpoint converts a Hubble Endpoint to our internal Endpoint type
func convertEndpoint(ep *flowpb.Endpoint, ip string) Endpoint {
	if ep == nil {
		return Endpoint{
			Kind: "External",
			IP:   ip,
			Name: ip,
		}
	}

	endpoint := Endpoint{
		Namespace: ep.GetNamespace(),
		IP:        ip,
	}

	// Determine the name and kind
	if podName := ep.GetPodName(); podName != "" {
		endpoint.Name = podName
		endpoint.Kind = "Pod"
	} else if ep.GetIdentity() != 0 {
		// Use identity for reserved labels (like host, world, etc.)
		labels := ep.GetLabels()
		for _, label := range labels {
			if strings.HasPrefix(label, "reserved:") {
				endpoint.Kind = "External"
				endpoint.Name = strings.TrimPrefix(label, "reserved:")
				break
			}
		}
		if endpoint.Name == "" {
			endpoint.Kind = "External"
			endpoint.Name = ip
		}
	} else {
		endpoint.Kind = "External"
		endpoint.Name = ip
	}

	// Extract workload name from labels
	endpoint.Workload = extractWorkloadFromHubbleLabels(ep.GetLabels())

	return endpoint
}

// extractWorkloadFromHubbleLabels extracts workload name from Hubble labels
func extractWorkloadFromHubbleLabels(labels []string) string {
	labelMap := make(map[string]string)
	for _, l := range labels {
		parts := strings.SplitN(l, "=", 2)
		if len(parts) == 2 {
			labelMap[parts[0]] = parts[1]
		}
	}

	// Common workload labels in order of preference
	for _, key := range []string{"app", "app.kubernetes.io/name", "k8s-app", "name"} {
		if name, ok := labelMap[key]; ok {
			return name
		}
	}

	return ""
}

// StreamFlows returns a channel of flows for real-time updates
func (h *HubbleSource) StreamFlows(ctx context.Context, opts FlowOptions) (<-chan Flow, error) {
	flowCh := make(chan Flow, 100)

	go func() {
		defer close(flowCh)

		h.mu.RLock()
		client := h.observerClient
		h.mu.RUnlock()

		if client == nil {
			log.Printf("[hubble] Cannot stream: not connected")
			return
		}

		// Build streaming request
		req := &observerpb.GetFlowsRequest{
			Follow: true,
		}

		if opts.Namespace != "" {
			req.Whitelist = []*flowpb.FlowFilter{
				{SourcePod: []string{opts.Namespace + "/"}},
				{DestinationPod: []string{opts.Namespace + "/"}},
			}
		}

		stream, err := client.GetFlows(ctx, req)
		if err != nil {
			log.Printf("[hubble] Failed to start flow stream: %v", err)
			return
		}

		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				if ctx.Err() != nil {
					return // Context cancelled
				}
				log.Printf("[hubble] Stream error: %v", err)
				return
			}

			pbFlow := resp.GetFlow()
			if pbFlow == nil {
				continue
			}

			flow := convertHubbleFlow(pbFlow)

			select {
			case flowCh <- flow:
			case <-ctx.Done():
				return
			default:
				// Channel full, drop flow
			}
		}
	}()

	return flowCh, nil
}

// Close cleans up resources
func (h *HubbleSource) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closeConnectionLocked()
	h.currentContext = ""
	h.relayNamespace = ""
	return nil
}

// GetPortForwardInstructions returns kubectl commands for manual access
func (h *HubbleSource) GetPortForwardInstructions() string {
	h.mu.RLock()
	namespace := h.relayNamespace
	h.mu.RUnlock()

	if namespace == "" {
		namespace = "kube-system"
	}

	return fmt.Sprintf(`To access Hubble flows directly, run:

# Port-forward Hubble Relay (gRPC API)
kubectl -n %s port-forward svc/hubble-relay 4245:80

# Then use Hubble CLI:
hubble observe --server localhost:4245

# Or port-forward Hubble UI (if installed):
kubectl -n %s port-forward svc/hubble-ui 12000:80
# Then open http://localhost:12000`, namespace, namespace)
}
