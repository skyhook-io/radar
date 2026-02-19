package traffic

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	carettaNamespace = "caretta"
	carettaAppLabel  = "app.kubernetes.io/name=caretta"
)

// Known Prometheus/VictoriaMetrics service locations to check.
// Each entry triggers a direct GET for fast O(n) lookup (Layer 2).
var metricsServiceLocations = []struct {
	namespace string
	name      string
	port      int    // 0 means use service's first port
	basePath  string // sub-path for Prometheus API (empty = root)
}{
	// VictoriaMetrics (Caretta's default)
	{"caretta", "caretta-vm", 8428, ""},
	// VictoriaMetrics common patterns
	{"victoria-metrics", "victoria-metrics-single-server", 8428, ""},
	{"victoria-metrics", "vmsingle", 8428, ""},
	{"monitoring", "victoria-metrics-single-server", 8428, ""},
	{"monitoring", "vmsingle", 8428, ""},
	// VictoriaMetrics vmselect (cluster mode) - uses sub-path
	{"victoria-metrics", "vmselect", 8481, "/select/0/prometheus"},
	{"monitoring", "vmselect", 8481, "/select/0/prometheus"},
	// kube-prometheus-stack (any release name uses this service name pattern)
	{"monitoring", "prometheus-kube-prometheus-prometheus", 9090, ""},
	{"monitoring", "prometheus-operated", 9090, ""},
	// Standard Prometheus locations
	{"opencost", "prometheus-server", 0, ""},
	{"monitoring", "prometheus-server", 0, ""},
	{"prometheus", "prometheus-server", 0, ""},
	{"observability", "prometheus-server", 0, ""},
	{"metrics", "prometheus-server", 0, ""},
	{"kube-system", "prometheus", 0, ""},
	{"default", "prometheus", 0, ""},
	{"caretta", "prometheus", 0, ""},
}

// CarettaSource implements TrafficSource for Caretta
type CarettaSource struct {
	k8sClient        kubernetes.Interface
	httpClient       *http.Client
	prometheusAddr   string
	metricsBasePath  string // sub-path for Prometheus API (e.g. "/select/0/prometheus" for vmselect)
	metricsNamespace string // namespace where metrics service was found
	metricsService   string // service name for port-forward
	metricsPort      int    // port for port-forward
	metricsURL       string // manual override URL from --prometheus-url flag
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
	basePath := c.metricsBasePath
	c.mu.RUnlock()

	if !connected {
		result, err := c.Detect(ctx)
		if err != nil || !result.Available {
			return nil, fmt.Errorf("Caretta not available: %s", result.Message)
		}
		c.mu.RLock()
		promAddr = c.prometheusAddr
		basePath = c.metricsBasePath
		c.mu.RUnlock()
	}

	// Discover Prometheus if not already found
	if promAddr == "" {
		promAddr = c.discoverPrometheus(ctx)
		if promAddr != "" {
			c.mu.RLock()
			basePath = c.metricsBasePath
			c.mu.RUnlock()
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
	flows, err := c.queryPrometheusForFlows(ctx, promAddr, basePath, opts)
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

// discoverPrometheus finds and connects to the metrics service.
// Uses a 3-layer approach:
//  1. Manual URL override (--prometheus-url flag) — does NOT fall through on failure
//  2. Well-known service locations (fast direct lookups)
//  3. Dynamic cluster-wide discovery with scoring
func (c *CarettaSource) discoverPrometheus(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If we have a cached address, verify it's still valid
	if c.prometheusAddr != "" {
		testAddr := c.prometheusAddr + c.metricsBasePath
		if c.tryMetricsEndpointLocked(ctx, testAddr) {
			return c.prometheusAddr
		}
		// Clear stale address
		c.prometheusAddr = ""
		c.metricsBasePath = ""
	}

	// Layer 1: Manual URL override — if set, use it exclusively (don't fall through)
	if c.metricsURL != "" {
		addr := strings.TrimRight(c.metricsURL, "/")
		if c.tryMetricsEndpointLocked(ctx, addr) {
			log.Printf("[caretta] Using manual metrics URL: %s", addr)
			c.prometheusAddr = addr
			c.metricsBasePath = ""
			return addr
		}
		log.Printf("[caretta] Manual metrics URL %s not reachable", addr)
		return ""
	}

	// Check for active managed port-forward first
	if pfAddr := GetMetricsAddress(c.currentContext); pfAddr != "" {
		if c.tryMetricsEndpointLocked(ctx, pfAddr) {
			log.Printf("[caretta] Using managed port-forward at %s", pfAddr)
			c.prometheusAddr = pfAddr
			return pfAddr
		}
	}

	// Layer 2+3: Well-known locations, then dynamic discovery
	info := c.discoverServiceLocked(ctx)
	if info == nil {
		log.Printf("[caretta] No Prometheus/VictoriaMetrics service found via any discovery method")
		return ""
	}

	// Try cluster address (works when running in-cluster)
	if c.tryClusterAddrLocked(ctx, info) {
		log.Printf("[caretta] Found metrics service at %s (basePath=%q)", info.clusterAddr, info.basePath)
		return info.clusterAddr
	}

	// Service exists but not reachable in-cluster - will need port-forward
	log.Printf("[caretta] Metrics service %s/%s found but not reachable in-cluster. Call Connect() for port-forward.",
		info.namespace, info.name)
	return ""
}

// discoverServiceLocked finds a metrics service via Layer 2 (well-known) then Layer 3 (dynamic).
// Sets metricsNamespace, metricsService, metricsPort on success. Caller must hold lock.
func (c *CarettaSource) discoverServiceLocked(ctx context.Context) *metricsServiceInfo {
	info := c.findMetricsServiceLocked(ctx)
	if info == nil {
		info = c.discoverMetricsServiceDynamic(ctx)
	}
	if info != nil {
		c.metricsNamespace = info.namespace
		c.metricsService = info.name
		c.metricsPort = info.port
	}
	return info
}

// tryClusterAddrLocked tries the cluster address with basePath and stores the result on success.
// Caller must hold lock.
func (c *CarettaSource) tryClusterAddrLocked(ctx context.Context, info *metricsServiceInfo) bool {
	testAddr := info.clusterAddr + info.basePath
	if c.tryMetricsEndpointLocked(ctx, testAddr) {
		c.prometheusAddr = info.clusterAddr
		c.metricsBasePath = info.basePath
		return true
	}
	return false
}

// queryPrometheusForFlows queries Prometheus for caretta_links_observed metrics
func (c *CarettaSource) queryPrometheusForFlows(ctx context.Context, promAddr string, basePath string, opts FlowOptions) ([]Flow, error) {
	// Build PromQL query for Caretta's link metric
	// caretta_links_observed{client_name, client_namespace, server_name, server_namespace, server_port, ...}
	query := "caretta_links_observed"
	if opts.Namespace != "" {
		// Filter by namespace (either client or server)
		query = fmt.Sprintf(`caretta_links_observed{client_namespace="%s"} or caretta_links_observed{server_namespace="%s"}`,
			opts.Namespace, opts.Namespace)
	}

	queryURL := fmt.Sprintf("%s%s/api/v1/query?query=%s", promAddr, basePath, url.QueryEscape(query))

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
			Value  []any             `json:"value"` // [timestamp, value]
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
	c.metricsBasePath = ""
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
		testAddr := c.prometheusAddr + c.metricsBasePath
		if c.tryMetricsEndpointLocked(ctx, testAddr) {
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
		c.metricsBasePath = ""
	}

	// Clear stale state if context changed
	if c.currentContext != contextName {
		c.prometheusAddr = ""
		c.metricsBasePath = ""
		c.currentContext = contextName
	}

	// Layer 1: Manual URL override — if set, use it exclusively (don't fall through)
	if c.metricsURL != "" {
		addr := strings.TrimRight(c.metricsURL, "/")
		if c.tryMetricsEndpointLocked(ctx, addr) {
			log.Printf("[caretta] Connected using manual metrics URL: %s", addr)
			c.prometheusAddr = addr
			c.metricsBasePath = ""
			return &MetricsConnectionInfo{
				Connected:   true,
				Address:     addr,
				ContextName: contextName,
			}, nil
		}
		return &MetricsConnectionInfo{
			Connected: false,
			Error:     fmt.Sprintf("Manual metrics URL %s is not reachable. Check the URL and ensure the service is running.", addr),
		}, nil
	}

	// Layer 2+3: Well-known locations, then dynamic discovery
	metricsInfo := c.discoverServiceLocked(ctx)
	if metricsInfo == nil {
		return &MetricsConnectionInfo{
			Connected: false,
			Error:     "No Prometheus/VictoriaMetrics service found. Use --prometheus-url to specify manually.",
		}, nil
	}

	// Try cluster-internal address first (works when running in-cluster)
	if c.tryClusterAddrLocked(ctx, metricsInfo) {
		log.Printf("[caretta] Connected to metrics service at %s (basePath=%q)", metricsInfo.clusterAddr, metricsInfo.basePath)
		return &MetricsConnectionInfo{
			Connected:   true,
			Address:     metricsInfo.clusterAddr,
			Namespace:   metricsInfo.namespace,
			ServiceName: metricsInfo.name,
			ContextName: contextName,
		}, nil
	}

	// Check if there's already a valid managed port-forward for this context
	if pfAddr := GetMetricsAddress(contextName); pfAddr != "" {
		pfTestAddr := pfAddr + metricsInfo.basePath
		if c.tryMetricsEndpointLocked(ctx, pfTestAddr) {
			log.Printf("[caretta] Using existing port-forward at %s", pfAddr)
			c.prometheusAddr = pfAddr
			c.metricsBasePath = metricsInfo.basePath
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
	c.metricsBasePath = metricsInfo.basePath
	log.Printf("[caretta] Connected via port-forward at %s (basePath=%q)", connInfo.Address, metricsInfo.basePath)

	return connInfo, nil
}

// metricsServiceInfo holds info about a discovered metrics service
type metricsServiceInfo struct {
	namespace   string
	name        string
	port        int
	clusterAddr string
	basePath    string // sub-path for Prometheus API (e.g. "/select/0/prometheus" for vmselect)
}

// resolveServicePort determines the port to use for a service
func resolveServicePort(svc corev1.Service, defaultPort int) int {
	if defaultPort != 0 {
		return defaultPort
	}
	if len(svc.Spec.Ports) > 0 {
		return int(svc.Spec.Ports[0].Port)
	}
	return 80
}

// findMetricsServiceLocked finds a metrics service from well-known locations (caller must hold lock)
func (c *CarettaSource) findMetricsServiceLocked(ctx context.Context) *metricsServiceInfo {
	for _, loc := range metricsServiceLocations {
		svc, err := c.k8sClient.CoreV1().Services(loc.namespace).Get(ctx, loc.name, metav1.GetOptions{})
		if err != nil {
			continue
		}

		port := resolveServicePort(*svc, loc.port)
		clusterAddr := buildClusterAddr(svc.Name, svc.Namespace, svc.Spec.ClusterIP, port)

		log.Printf("[caretta] Found metrics service: %s/%s:%d", svc.Namespace, svc.Name, port)
		return &metricsServiceInfo{
			namespace:   svc.Namespace,
			name:        svc.Name,
			port:        port,
			clusterAddr: clusterAddr,
			basePath:    loc.basePath,
		}
	}

	return nil
}

// Namespaces to skip during dynamic discovery - never contain metrics services
var skipNamespaces = map[string]bool{
	"kube-public":     true,
	"kube-node-lease": true,
}

// metricsNamespaces commonly used for metrics services
var metricsNamespaces = map[string]bool{
	"monitoring":       true,
	"prometheus":       true,
	"observability":    true,
	"metrics":          true,
	"victoria-metrics": true,
	"caretta":          true,
	"opencost":         true,
}

// scoredService is a candidate from dynamic discovery
type scoredService struct {
	info  metricsServiceInfo
	score int
}

// scoreMetricsService computes a heuristic score for a service being a Prometheus-compatible endpoint.
// Only services with score > 0 are considered candidates.
func scoreMetricsService(svc corev1.Service) (score int, basePath string) {
	labels := svc.Labels
	name := svc.Name
	ns := svc.Namespace

	// Skip ExternalName services
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return 0, ""
	}

	// Skip filtered namespaces
	if skipNamespaces[ns] {
		return 0, ""
	}

	// --- Label signals ---
	appName := labels["app.kubernetes.io/name"]
	appLabel := labels["app"]
	component := labels["app.kubernetes.io/component"]

	switch appName {
	case "prometheus":
		score += 100
	case "victoria-metrics-single", "vmsingle":
		score += 100
	case "vmselect":
		score += 90
		basePath = "/select/0/prometheus"
	case "thanos-query", "thanos-querier":
		score += 80
	}

	switch appLabel {
	case "prometheus", "prometheus-server":
		score += 80
	case "vmsingle":
		score += 80
	case "vmselect":
		score += 80
		basePath = "/select/0/prometheus"
	}

	// Component disambiguator (only useful when already scored)
	if score > 0 && component == "server" {
		score += 20
	}

	// --- Port signals ---
	for _, p := range svc.Spec.Ports {
		switch p.Port {
		case 9090:
			score += 30
		case 8428:
			score += 30
		case 8481:
			score += 25
		case 9009:
			score += 25
		}
		if strings.Contains(strings.ToLower(p.Name), "prometheus") {
			score += 10
		}
	}

	// --- Name signals ---
	nameLower := strings.ToLower(name)
	if strings.Contains(nameLower, "prometheus") {
		score += 20
	}
	if strings.Contains(nameLower, "victoria") || strings.Contains(nameLower, "vmsingle") || strings.Contains(nameLower, "vmselect") {
		score += 20
		if strings.Contains(nameLower, "vmselect") && basePath == "" {
			basePath = "/select/0/prometheus"
		}
	}
	if strings.Contains(nameLower, "thanos") {
		score += 15
	}

	// --- Namespace signal ---
	if metricsNamespaces[ns] {
		score += 10
	}

	return score, basePath
}

// discoverMetricsServiceDynamic lists all services cluster-wide, scores them, and validates top candidates (Layer 3).
// Caller must hold the mutex lock.
func (c *CarettaSource) discoverMetricsServiceDynamic(ctx context.Context) *metricsServiceInfo {
	log.Printf("[caretta] Starting dynamic metrics service discovery...")

	svcs, err := c.k8sClient.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("[caretta] Failed to list services for dynamic discovery: %v", err)
		return nil
	}

	// Score all services
	var candidates []scoredService
	for _, svc := range svcs.Items {
		score, bp := scoreMetricsService(svc)
		if score <= 0 {
			continue
		}

		port := resolveServicePort(svc, 0)
		candidates = append(candidates, scoredService{
			info: metricsServiceInfo{
				namespace:   svc.Namespace,
				name:        svc.Name,
				port:        port,
				clusterAddr: buildClusterAddr(svc.Name, svc.Namespace, svc.Spec.ClusterIP, port),
				basePath:    bp,
			},
			score: score,
		})
	}

	if len(candidates) == 0 {
		log.Printf("[caretta] Dynamic discovery found no candidates")
		return nil
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	log.Printf("[caretta] Dynamic discovery found %d candidates, top scores:", len(candidates))
	limit := min(len(candidates), 5)
	for i := range limit {
		log.Printf("[caretta]   %s/%s (score=%d, basePath=%q)",
			candidates[i].info.namespace, candidates[i].info.name,
			candidates[i].score, candidates[i].info.basePath)
	}

	// Validate top candidates via API probe (works when running in-cluster)
	for i := range limit {
		cand := &candidates[i]
		addr := cand.info.clusterAddr

		// Try root path first
		if c.tryMetricsEndpointLocked(ctx, addr) {
			log.Printf("[caretta] Dynamic discovery validated: %s/%s at %s", cand.info.namespace, cand.info.name, addr)
			cand.info.basePath = ""
			return &cand.info
		}

		// If candidate has a sub-path (e.g. vmselect), try that too
		if cand.info.basePath != "" {
			subAddr := addr + cand.info.basePath
			if c.tryMetricsEndpointLocked(ctx, subAddr) {
				log.Printf("[caretta] Dynamic discovery validated: %s/%s at %s (sub-path: %s)",
					cand.info.namespace, cand.info.name, addr, cand.info.basePath)
				return &cand.info
			}
		}
	}

	// No candidate was reachable in-cluster (common when running locally).
	// Return the highest-scored candidate — the caller can establish a port-forward.
	best := &candidates[0]
	log.Printf("[caretta] Dynamic discovery: no candidates reachable in-cluster, returning best candidate: %s/%s (score=%d)",
		best.info.namespace, best.info.name, best.score)
	return &best.info
}

// buildClusterAddr builds a cluster-internal address for a service
func buildClusterAddr(name, namespace, clusterIP string, port int) string {
	if clusterIP == "None" {
		return fmt.Sprintf("http://%s-0.%s.%s.svc.cluster.local:%d", name, name, namespace, port)
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", name, namespace, port)
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
