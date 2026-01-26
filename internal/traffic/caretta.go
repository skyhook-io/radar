package traffic

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	carettaNamespace = "caretta"
	carettaAppLabel  = "app.kubernetes.io/name=caretta"
)

// Known Prometheus/VictoriaMetrics service locations to check
var metricsServiceLocations = []struct {
	namespace string
	name      string
	port      int // 0 means use service's first port
}{
	// VictoriaMetrics (Caretta's default)
	{"caretta", "caretta-vm", 8428},
	// Standard Prometheus locations
	{"opencost", "prometheus-server", 0},
	{"monitoring", "prometheus-server", 0},
	{"prometheus", "prometheus-server", 0},
	{"kube-system", "prometheus", 0},
	{"default", "prometheus", 0},
	{"caretta", "prometheus", 0},
}

// CarettaSource implements TrafficSource for Caretta
type CarettaSource struct {
	k8sClient        kubernetes.Interface
	httpClient       *http.Client
	prometheusAddr   string
	metricsNamespace string // namespace where metrics service was found
	metricsService   string // service name for port-forward
	metricsPort      int    // port for port-forward
	isConnected      bool
	currentContext   string // current K8s context name
	mu               sync.RWMutex
}

// NewCarettaSource creates a new Caretta traffic source
func NewCarettaSource(client kubernetes.Interface) *CarettaSource {
	return &CarettaSource{
		k8sClient: client,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Name returns the source identifier
func (c *CarettaSource) Name() string {
	return "caretta"
}

// Detect checks if Caretta is available in the cluster
func (c *CarettaSource) Detect(ctx context.Context) (*DetectionResult, error) {
	result := &DetectionResult{
		Available: false,
	}

	// Check for Caretta namespace
	_, err := c.k8sClient.CoreV1().Namespaces().Get(ctx, carettaNamespace, metav1.GetOptions{})
	if err != nil {
		// Try default namespace as fallback
		log.Printf("[caretta] Namespace %s not found, checking default namespace", carettaNamespace)
	}

	// Check for Caretta pods in caretta namespace or kube-system
	namespacesToCheck := []string{carettaNamespace, "default", "kube-system"}

	for _, ns := range namespacesToCheck {
		pods, err := c.k8sClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: carettaAppLabel,
		})
		if err != nil {
			continue
		}

		if len(pods.Items) > 0 {
			runningPods := 0
			for _, pod := range pods.Items {
				if pod.Status.Phase == "Running" {
					runningPods++
				}
			}

			if runningPods > 0 {
				c.mu.Lock()
				c.isConnected = true
				c.mu.Unlock()

				result.Available = true
				result.Message = fmt.Sprintf("Caretta detected with %d running pod(s) in namespace %s", runningPods, ns)

				// Try to get version from pod labels
				if len(pods.Items) > 0 {
					if ver, ok := pods.Items[0].Labels["app.kubernetes.io/version"]; ok {
						result.Version = ver
					}
				}

				return result, nil
			}

			result.Message = fmt.Sprintf("Caretta pods found in %s but none are running (%d total)", ns, len(pods.Items))
			return result, nil
		}
	}

	// Also check for DaemonSet
	for _, ns := range namespacesToCheck {
		ds, err := c.k8sClient.AppsV1().DaemonSets(ns).Get(ctx, "caretta", metav1.GetOptions{})
		if err == nil {
			// DaemonSet exists, check its status
			if ds.Status.NumberReady > 0 {
				c.mu.Lock()
				c.isConnected = true
				c.mu.Unlock()

				result.Available = true
				result.Message = fmt.Sprintf("Caretta DaemonSet detected with %d ready pods in namespace %s", ds.Status.NumberReady, ns)
				return result, nil
			}

			result.Message = fmt.Sprintf("Caretta DaemonSet found in %s but no pods are ready", ns)
			return result, nil
		}
	}

	result.Message = "Caretta not detected. Install Caretta for eBPF-based traffic visibility."
	return result, nil
}

// GetFlows retrieves flows from Caretta via Prometheus metrics
func (c *CarettaSource) GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error) {
	c.mu.RLock()
	connected := c.isConnected
	promAddr := c.prometheusAddr
	c.mu.RUnlock()

	if !connected {
		result, err := c.Detect(ctx)
		if err != nil || !result.Available {
			return nil, fmt.Errorf("Caretta not available: %s", result.Message)
		}
		c.mu.RLock()
		promAddr = c.prometheusAddr
		c.mu.RUnlock()
	}

	// Discover Prometheus if not already found
	if promAddr == "" {
		promAddr = c.discoverPrometheus(ctx)
		if promAddr != "" {
			c.mu.Lock()
			c.prometheusAddr = promAddr
			c.mu.Unlock()
		}
	}

	if promAddr == "" {
		log.Printf("[caretta] Prometheus not found, returning empty flows")
		return &FlowsResponse{
			Source:    "caretta",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   "Prometheus/VictoriaMetrics service not found. Ensure Caretta's metrics backend is deployed.",
		}, nil
	}

	// Query Prometheus for Caretta metrics
	flows, err := c.queryPrometheusForFlows(ctx, promAddr, opts)
	if err != nil {
		log.Printf("[caretta] Error querying Prometheus: %v", err)
		return &FlowsResponse{
			Source:    "caretta",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   fmt.Sprintf("Failed to query Prometheus: %v", err),
		}, nil
	}

	return &FlowsResponse{
		Source:    "caretta",
		Timestamp: time.Now(),
		Flows:     flows,
	}, nil
}

// discoverPrometheus finds and connects to the metrics service
// Uses the managed port-forward if running locally
func (c *CarettaSource) discoverPrometheus(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If we have a cached address, verify it's still valid
	if c.prometheusAddr != "" {
		if c.tryMetricsEndpointLocked(ctx, c.prometheusAddr) {
			return c.prometheusAddr
		}
		// Clear stale address
		c.prometheusAddr = ""
	}

	// Check for active managed port-forward first
	if pfAddr := GetMetricsAddress(c.currentContext); pfAddr != "" {
		if c.tryMetricsEndpointLocked(ctx, pfAddr) {
			log.Printf("[caretta] Using managed port-forward at %s", pfAddr)
			c.prometheusAddr = pfAddr
			return pfAddr
		}
	}

	// Find and try cluster-internal address
	info := c.findMetricsServiceLocked(ctx)
	if info == nil {
		log.Printf("[caretta] No Prometheus/VictoriaMetrics service found")
		return ""
	}

	c.metricsNamespace = info.namespace
	c.metricsService = info.name
	c.metricsPort = info.port

	// Try cluster address (works when running in-cluster)
	if c.tryMetricsEndpointLocked(ctx, info.clusterAddr) {
		log.Printf("[caretta] Found metrics service at %s", info.clusterAddr)
		c.prometheusAddr = info.clusterAddr
		return info.clusterAddr
	}

	// Not reachable - need to call Connect() to establish port-forward
	log.Printf("[caretta] Metrics service %s/%s found but not reachable. Call Connect() to establish port-forward.",
		info.namespace, info.name)
	return ""
}

// queryPrometheusForFlows queries Prometheus for caretta_links_observed metrics
func (c *CarettaSource) queryPrometheusForFlows(ctx context.Context, promAddr string, opts FlowOptions) ([]Flow, error) {
	// Build PromQL query for Caretta's link metric
	// caretta_links_observed{client_name, client_namespace, server_name, server_namespace, server_port, ...}
	query := "caretta_links_observed"
	if opts.Namespace != "" {
		// Filter by namespace (either client or server)
		query = fmt.Sprintf(`caretta_links_observed{client_namespace="%s"} or caretta_links_observed{server_namespace="%s"}`,
			opts.Namespace, opts.Namespace)
	}

	// Query Prometheus API
	queryURL := fmt.Sprintf("%s/api/v1/query?query=%s", promAddr, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying prometheus: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned status %d", resp.StatusCode)
	}

	var promResp prometheusResponse
	if err := json.NewDecoder(resp.Body).Decode(&promResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if promResp.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed: %s", promResp.Status)
	}

	// Parse results into flows
	flows := make([]Flow, 0, len(promResp.Data.Result))
	for _, result := range promResp.Data.Result {
		metric := result.Metric

		// Parse connection count from value
		connections := int64(1)
		if len(result.Value) >= 2 {
			if valStr, ok := result.Value[1].(string); ok {
				if val, err := strconv.ParseFloat(valStr, 64); err == nil {
					connections = int64(val)
				}
			}
		}

		// Parse port
		port := 0
		if portStr, ok := metric["server_port"]; ok {
			if p, err := strconv.Atoi(portStr); err == nil {
				port = p
			}
		}

		flow := Flow{
			Source: Endpoint{
				Name:      metric["client_name"],
				Namespace: metric["client_namespace"],
				Kind:      metric["client_kind"],
				Workload:  metric["client_name"], // Caretta typically uses workload names
			},
			Destination: Endpoint{
				Name:      metric["server_name"],
				Namespace: metric["server_namespace"],
				Kind:      metric["server_kind"],
				Port:      port,
				Workload:  metric["server_name"],
			},
			Protocol:    "tcp", // Caretta tracks TCP connections
			Port:        port,
			Connections: connections,
			Verdict:     "forwarded",
			LastSeen:    time.Now(),
		}

		// Handle external endpoints
		if flow.Source.Kind == "" {
			flow.Source.Kind = "Pod"
		}
		if flow.Destination.Kind == "" {
			flow.Destination.Kind = "Pod"
		}
		if flow.Source.Namespace == "" && flow.Source.Name != "" {
			flow.Source.Kind = "External"
		}
		if flow.Destination.Namespace == "" && flow.Destination.Name != "" {
			flow.Destination.Kind = "External"
		}

		flows = append(flows, flow)
	}

	log.Printf("[caretta] Retrieved %d flows from Prometheus", len(flows))
	return flows, nil
}

// prometheusResponse represents the Prometheus API response structure
type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"` // [timestamp, value]
		} `json:"result"`
	} `json:"data"`
}

// StreamFlows returns a channel of flows for real-time updates
func (c *CarettaSource) StreamFlows(ctx context.Context, opts FlowOptions) (<-chan Flow, error) {
	flowCh := make(chan Flow, 100)

	go func() {
		defer close(flowCh)

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				response, err := c.GetFlows(ctx, opts)
				if err != nil {
					log.Printf("[caretta] Error fetching flows: %v", err)
					continue
				}

				for _, flow := range response.Flows {
					select {
					case flowCh <- flow:
					case <-ctx.Done():
						return
					default:
					}
				}
			}
		}
	}()

	return flowCh, nil
}

// Close cleans up resources
func (c *CarettaSource) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.isConnected = false
	c.prometheusAddr = ""
	c.currentContext = ""
	return nil
}

// Connect establishes connection to metrics service, starting port-forward if needed
// contextName is the current K8s context name, used to validate port-forward belongs to right cluster
func (c *CarettaSource) Connect(ctx context.Context, contextName string) (*MetricsConnectionInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If already connected to the same context, check if still valid
	if c.prometheusAddr != "" && c.currentContext == contextName {
		if c.tryMetricsEndpointLocked(ctx, c.prometheusAddr) {
			return &MetricsConnectionInfo{
				Connected:   true,
				Address:     c.prometheusAddr,
				Namespace:   c.metricsNamespace,
				ServiceName: c.metricsService,
				ContextName: contextName,
			}, nil
		}
		// Connection lost, clear it
		c.prometheusAddr = ""
	}

	// Clear stale state if context changed
	if c.currentContext != contextName {
		c.prometheusAddr = ""
		c.currentContext = contextName
	}

	// Find the metrics service
	metricsInfo := c.findMetricsServiceLocked(ctx)
	if metricsInfo == nil {
		return &MetricsConnectionInfo{
			Connected: false,
			Error:     "No Prometheus/VictoriaMetrics service found for Caretta",
		}, nil
	}

	c.metricsNamespace = metricsInfo.namespace
	c.metricsService = metricsInfo.name
	c.metricsPort = metricsInfo.port

	// Try cluster-internal address first (works when running in-cluster)
	clusterAddr := metricsInfo.clusterAddr
	if c.tryMetricsEndpointLocked(ctx, clusterAddr) {
		log.Printf("[caretta] Connected to metrics service at %s", clusterAddr)
		c.prometheusAddr = clusterAddr
		return &MetricsConnectionInfo{
			Connected:   true,
			Address:     clusterAddr,
			Namespace:   metricsInfo.namespace,
			ServiceName: metricsInfo.name,
			ContextName: contextName,
		}, nil
	}

	// Check if there's already a valid managed port-forward for this context
	if pfAddr := GetMetricsAddress(contextName); pfAddr != "" {
		if c.tryMetricsEndpointLocked(ctx, pfAddr) {
			log.Printf("[caretta] Using existing port-forward at %s", pfAddr)
			c.prometheusAddr = pfAddr
			return &MetricsConnectionInfo{
				Connected:   true,
				Address:     pfAddr,
				Namespace:   metricsInfo.namespace,
				ServiceName: metricsInfo.name,
				ContextName: contextName,
			}, nil
		}
	}

	// Start a new managed port-forward
	log.Printf("[caretta] Starting port-forward to %s/%s:%d", metricsInfo.namespace, metricsInfo.name, metricsInfo.port)
	connInfo, err := StartMetricsPortForward(ctx, metricsInfo.namespace, metricsInfo.name, metricsInfo.port, contextName)
	if err != nil {
		return &MetricsConnectionInfo{
			Connected:   false,
			Namespace:   metricsInfo.namespace,
			ServiceName: metricsInfo.name,
			Error:       fmt.Sprintf("Failed to start port-forward: %v", err),
		}, nil
	}

	c.prometheusAddr = connInfo.Address
	log.Printf("[caretta] Connected via port-forward at %s", connInfo.Address)

	return connInfo, nil
}

// metricsServiceInfo holds info about a discovered metrics service
type metricsServiceInfo struct {
	namespace   string
	name        string
	port        int
	clusterAddr string
}

// findMetricsServiceLocked finds a metrics service (caller must hold lock)
func (c *CarettaSource) findMetricsServiceLocked(ctx context.Context) *metricsServiceInfo {
	for _, loc := range metricsServiceLocations {
		svc, err := c.k8sClient.CoreV1().Services(loc.namespace).Get(ctx, loc.name, metav1.GetOptions{})
		if err != nil {
			continue
		}

		// Determine port
		port := loc.port
		if port == 0 && len(svc.Spec.Ports) > 0 {
			port = int(svc.Spec.Ports[0].Port)
		}
		if port == 0 {
			port = 80
		}

		// Build cluster-internal address
		var clusterAddr string
		if svc.Spec.ClusterIP == "None" {
			// For headless services, use pod-0 directly
			clusterAddr = fmt.Sprintf("http://%s-0.%s.%s.svc.cluster.local:%d", svc.Name, svc.Name, svc.Namespace, port)
		} else {
			clusterAddr = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", svc.Name, svc.Namespace, port)
		}

		log.Printf("[caretta] Found metrics service: %s/%s:%d", svc.Namespace, svc.Name, port)
		return &metricsServiceInfo{
			namespace:   svc.Namespace,
			name:        svc.Name,
			port:        port,
			clusterAddr: clusterAddr,
		}
	}

	return nil
}

// tryMetricsEndpointLocked checks if endpoint is reachable (caller must hold lock)
func (c *CarettaSource) tryMetricsEndpointLocked(ctx context.Context, addr string) bool {
	testCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(testCtx, "GET", addr+"/api/v1/query?query=up", nil)
	if err != nil {
		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// GetMetricsServiceInfo returns info about the detected metrics service for display
func (c *CarettaSource) GetMetricsServiceInfo() (namespace, service string, port int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metricsNamespace, c.metricsService, c.metricsPort
}
