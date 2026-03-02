package prometheus

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/portforward"
)

// Well-known Prometheus/VictoriaMetrics service locations
// (similar to traffic/caretta.go but with different ordering for workload metrics discovery).
var wellKnownLocations = []struct {
	namespace string
	name      string
	port      int    // 0 = use service's first port
	basePath  string // sub-path for Prometheus API
}{
	// VictoriaMetrics — monitoring namespace first (workload metrics)
	{"monitoring", "victoria-metrics-victoria-metrics-single-server", 8428, ""},
	{"monitoring", "victoria-metrics-single-server", 8428, ""},
	{"monitoring", "vmsingle", 8428, ""},
	{"monitoring", "vmselect", 8481, "/select/0/prometheus"},
	{"victoria-metrics", "victoria-metrics-victoria-metrics-single-server", 8428, ""},
	{"victoria-metrics", "victoria-metrics-single-server", 8428, ""},
	{"victoria-metrics", "vmsingle", 8428, ""},
	{"victoria-metrics", "vmselect", 8481, "/select/0/prometheus"},
	// kube-prometheus-stack
	{"monitoring", "kube-prometheus-stack-prometheus", 9090, ""},
	{"monitoring", "prometheus-kube-prometheus-prometheus", 9090, ""},
	{"monitoring", "prometheus-operated", 9090, ""},
	// Standard Prometheus
	{"opencost", "prometheus-server", 0, ""},
	{"monitoring", "prometheus-server", 0, ""},
	{"prometheus", "prometheus-server", 0, ""},
	{"observability", "prometheus-server", 0, ""},
	{"metrics", "prometheus-server", 0, ""},
	{"kube-system", "prometheus", 0, ""},
	{"default", "prometheus", 0, ""},
	// VictoriaMetrics — caretta namespace (traffic-specific, may lack workload metrics)
	{"caretta", "caretta-vm", 8428, ""},
}

// Namespaces commonly used for metrics services
var metricsNamespaces = map[string]bool{
	"monitoring":       true,
	"prometheus":       true,
	"observability":    true,
	"metrics":          true,
	"victoria-metrics": true,
	"caretta":          true,
	"opencost":         true,
}

// Namespaces to skip during dynamic discovery
var skipNamespaces = map[string]bool{
	"kube-public":     true,
	"kube-node-lease": true,
}

// discover finds and connects to Prometheus using a multi-layer approach:
//  1. Manual URL override (--prometheus-url)
//  2. Existing traffic system port-forward
//  3. Well-known service locations
//  4. Dynamic cluster-wide discovery with scoring
//
// The lock is only held briefly to read/write state, not during network I/O.
func (c *Client) discover(ctx context.Context) (string, string, error) {
	// Layer 1: Manual URL override (read under lock)
	c.mu.RLock()
	manualURL := c.manualURL
	contextName := c.contextName
	k8sClient := c.k8sClient
	c.mu.RUnlock()

	if manualURL != "" {
		addr := strings.TrimRight(manualURL, "/")
		if c.probe(ctx, addr) {
			log.Printf("[prometheus] Using manual URL: %s", addr)
			c.mu.Lock()
			c.baseURL = addr
			c.basePath = ""
			c.discovered = true
			c.mu.Unlock()
			return addr, "", nil
		}
		errorlog.Record("prometheus", "error", "manual Prometheus URL %s not reachable", addr)
		return "", "", fmt.Errorf("manual Prometheus URL %s not reachable", addr)
	}

	// Layer 2: Check if traffic system already has a port-forward
	if pfAddr := portforward.GetAddress(contextName); pfAddr != "" {
		if c.probe(ctx, pfAddr) {
			log.Printf("[prometheus] Using traffic system port-forward: %s", pfAddr)
			c.mu.Lock()
			c.baseURL = pfAddr
			c.basePath = ""
			c.discovered = true
			c.mu.Unlock()
			return pfAddr, "", nil
		}
	}

	if k8sClient == nil {
		return "", "", fmt.Errorf("no Kubernetes client available for discovery")
	}

	// Layer 3: Well-known service locations
	info := c.findWellKnownService(ctx)
	if info == nil {
		// Layer 4: Dynamic discovery
		info = c.discoverDynamic(ctx)
	}

	if info == nil {
		c.mu.Lock()
		c.discoveryService = nil
		c.mu.Unlock()
		errorlog.Record("prometheus", "warning", "no Prometheus service found in cluster")
		return "", "", fmt.Errorf("no Prometheus service found in cluster")
	}

	c.mu.Lock()
	c.discoveryService = &ServiceInfo{
		Namespace: info.namespace,
		Name:      info.name,
		Port:      info.port,
		BasePath:  info.basePath,
	}
	c.mu.Unlock()

	// Try cluster-internal address (no lock held during probe)
	if c.probe(ctx, info.clusterAddr+info.basePath) {
		log.Printf("[prometheus] Connected to %s/%s at %s", info.namespace, info.name, info.clusterAddr)
		c.mu.Lock()
		c.baseURL = info.clusterAddr
		c.basePath = info.basePath
		c.discovered = true
		c.mu.Unlock()
		return info.clusterAddr, info.basePath, nil
	}

	// Not reachable in-cluster — try port-forward
	log.Printf("[prometheus] Service %s/%s not reachable in-cluster, starting port-forward...", info.namespace, info.name)
	connInfo, err := portforward.Start(ctx, info.namespace, info.name, info.targetPort, contextName)
	if err != nil {
		errorlog.Record("prometheus", "error", "port-forward to %s/%s failed: %v", info.namespace, info.name, err)
		return "", "", fmt.Errorf("port-forward to %s/%s failed: %w", info.namespace, info.name, err)
	}

	addr := connInfo.Address
	if info.basePath != "" {
		if c.probe(ctx, addr+info.basePath) {
			c.mu.Lock()
			c.baseURL = addr
			c.basePath = info.basePath
			c.discovered = true
			c.mu.Unlock()
			return addr, info.basePath, nil
		}
	} else if c.probe(ctx, addr) {
		c.mu.Lock()
		c.baseURL = addr
		c.basePath = ""
		c.discovered = true
		c.mu.Unlock()
		return addr, "", nil
	}

	portforward.Stop()
	errorlog.Record("prometheus", "error", "Prometheus at %s/%s not responding after port-forward", info.namespace, info.name)
	return "", "", fmt.Errorf("Prometheus at %s/%s not responding after port-forward", info.namespace, info.name)
}

type serviceInfo struct {
	namespace   string
	name        string
	port        int // service port (for cluster-internal address)
	targetPort  int // container port (for port-forwarding to pod)
	clusterAddr string
	basePath    string
}

func (c *Client) findWellKnownService(ctx context.Context) *serviceInfo {
	c.mu.RLock()
	k8sClient := c.k8sClient
	c.mu.RUnlock()

	for _, loc := range wellKnownLocations {
		svc, err := k8sClient.CoreV1().Services(loc.namespace).Get(ctx, loc.name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				log.Printf("[prometheus] Error checking well-known service %s/%s: %v", loc.namespace, loc.name, err)
			}
			continue
		}

		port := resolvePort(*svc, loc.port)
		addr := buildClusterAddr(svc.Name, svc.Namespace, svc.Spec.ClusterIP, port)
		tp := resolveTargetPort(*svc, port)

		log.Printf("[prometheus] Found well-known service: %s/%s:%d (targetPort=%d)", svc.Namespace, svc.Name, port, tp)
		return &serviceInfo{
			namespace:   svc.Namespace,
			name:        svc.Name,
			port:        port,
			targetPort:  tp,
			clusterAddr: addr,
			basePath:    loc.basePath,
		}
	}
	return nil
}

type scoredCandidate struct {
	info  serviceInfo
	score int
}

func (c *Client) discoverDynamic(ctx context.Context) *serviceInfo {
	log.Printf("[prometheus] Starting dynamic discovery...")

	c.mu.RLock()
	k8sClient := c.k8sClient
	c.mu.RUnlock()

	svcs, err := k8sClient.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("[prometheus] Failed to list services: %v", err)
		return nil
	}

	var candidates []scoredCandidate
	for _, svc := range svcs.Items {
		score, bp := scoreService(svc)
		if score <= 0 {
			continue
		}
		port := resolvePort(svc, 0)
		candidates = append(candidates, scoredCandidate{
			info: serviceInfo{
				namespace:   svc.Namespace,
				name:        svc.Name,
				port:        port,
				targetPort:  resolveTargetPort(svc, port),
				clusterAddr: buildClusterAddr(svc.Name, svc.Namespace, svc.Spec.ClusterIP, port),
				basePath:    bp,
			},
			score: score,
		})
	}

	if len(candidates) == 0 {
		log.Printf("[prometheus] Dynamic discovery found no candidates")
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	limit := min(len(candidates), 5)
	log.Printf("[prometheus] Found %d candidates, top %d:", len(candidates), limit)
	for i := range limit {
		log.Printf("[prometheus]   %s/%s (score=%d)", candidates[i].info.namespace, candidates[i].info.name, candidates[i].score)
	}

	// Validate top candidates (no lock held during probes)
	for i := range limit {
		cand := &candidates[i]
		addr := cand.info.clusterAddr

		if c.probe(ctx, addr+cand.info.basePath) {
			log.Printf("[prometheus] Validated: %s/%s", cand.info.namespace, cand.info.name)
			return &cand.info
		}
	}

	// Return best unvalidated candidate (caller will port-forward)
	best := &candidates[0]
	log.Printf("[prometheus] No candidates reachable in-cluster, returning best: %s/%s (score=%d)",
		best.info.namespace, best.info.name, best.score)
	return &best.info
}

// scoreService computes a heuristic score for a service being Prometheus-compatible.
func scoreService(svc corev1.Service) (score int, basePath string) {
	labels := svc.Labels
	name := svc.Name
	ns := svc.Namespace

	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return 0, ""
	}
	if skipNamespaces[ns] {
		return 0, ""
	}

	// Label signals
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

	if score > 0 && component == "server" {
		score += 20
	}

	// Port signals
	for _, p := range svc.Spec.Ports {
		switch p.Port {
		case 9090: // Prometheus default
			score += 30
		case 8428: // VictoriaMetrics single-node default
			score += 30
		case 8481: // VictoriaMetrics vmselect default
			score += 25
		case 9009: // Thanos Query default
			score += 25
		}
		if strings.Contains(strings.ToLower(p.Name), "prometheus") {
			score += 10
		}
	}

	// Name signals
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

	// Namespace signal
	if metricsNamespaces[ns] {
		score += 10
	}

	return score, basePath
}

func resolvePort(svc corev1.Service, defaultPort int) int {
	if defaultPort != 0 {
		return defaultPort
	}
	if len(svc.Spec.Ports) > 0 {
		return int(svc.Spec.Ports[0].Port)
	}
	return 80
}

// resolveTargetPort returns the container port for port-forwarding.
// When the service port differs from the container's targetPort (e.g., service:80 → container:9090),
// port-forwarding needs the container port since it bypasses the Service and connects directly to the pod.
func resolveTargetPort(svc corev1.Service, servicePort int) int {
	for _, p := range svc.Spec.Ports {
		if int(p.Port) == servicePort {
			if p.TargetPort.IntVal > 0 {
				return int(p.TargetPort.IntVal)
			}
			// targetPort unset or zero defaults to the service port
			return servicePort
		}
	}
	return servicePort
}

func buildClusterAddr(name, namespace, clusterIP string, port int) string {
	if clusterIP == "None" {
		return fmt.Sprintf("http://%s-0.%s.%s.svc.cluster.local:%d", name, name, namespace, port)
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", name, namespace, port)
}
