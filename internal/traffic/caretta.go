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

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/prom"
)

const (
	carettaNamespace = "caretta"
	carettaAppLabel  = "app.kubernetes.io/name=caretta"

	// Caretta's chart pins its bundled VictoriaMetrics to the service name
	// caretta-vm and to the subchart's own name label, but the namespace follows
	// the Helm release — so the store is located by (detected namespace, label),
	// with the name as the fallback for a store that lost the label.
	carettaStoreLabel    = "app.kubernetes.io/name=victoria-metrics-single"
	carettaStoreService  = "caretta-vm"
	carettaInstanceLabel = "app.kubernetes.io/instance"

	// maxMetricsCandidates bounds how many backends Connect will port-forward to
	// and probe before giving up. Each rejected candidate costs a forward setup.
	// It bounds the local walk only: in-cluster, candidates are reached by cluster
	// address in single-digit ms and never port-forwarded, so the cap is dropped.
	maxMetricsCandidates = 4
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
	{"monitoring", "victoria-metrics-victoria-metrics-single-server", 8428, ""},
	{"monitoring", "vmsingle", 8428, ""},
	// VictoriaMetrics vmselect (cluster mode) - uses sub-path
	{"victoria-metrics", "vmselect", 8481, "/select/0/prometheus"},
	{"monitoring", "vmselect", 8481, "/select/0/prometheus"},
	// kube-prometheus-stack (any release name uses this service name pattern)
	{"monitoring", "kube-prometheus-stack-prometheus", 9090, ""},
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
	k8sClient           kubernetes.Interface
	httpClient          *http.Client
	prometheusAddr      string
	metricsBasePath     string // sub-path for Prometheus API (e.g. "/select/0/prometheus" for vmselect)
	metricsNamespace    string // namespace where metrics service was found
	metricsService      string // service name for port-forward
	metricsPort         int    // port for port-forward
	metricsURL          string // manual override URL from --prometheus-url flag
	headers             map[string]string
	isConnected         bool
	currentContext      string // current K8s context name
	detectedNamespace   string // namespace Caretta itself was detected in
	detectedInstance    string // Helm release Caretta was installed as, for store ownership
	backendVerified     bool   // bound backend proved it holds Caretta metrics
	boundIsCarettaStore bool   // bound backend is Caretta's own store, trusted on identity
	backendWarning      string // why no backend could be bound, surfaced to the UI
	closed              bool   // set by Close; a late Connect must not resurrect the source
	// inCluster records how Radar reaches the cluster, captured once at
	// construction from the same detection the connection context reports as
	// "in-cluster". It flips discovery's cost model: in-cluster a Service address
	// resolves and answers in single-digit ms but pods/portforward is normally
	// denied, so discovery probes every candidate by cluster address and never
	// port-forwards; local is the reverse — a cluster address can't resolve and
	// costs a guaranteed dead-wait, so discovery skips it and port-forwards.
	inCluster bool
	mu        sync.RWMutex
}

// applyHeaders attaches the configured custom headers to a Prometheus
// request. No lock: c.headers is assigned exactly once inside
// manager.go's initOnce.Do and never mutated afterwards (a context
// switch builds a fresh CarettaSource). Locking here would deadlock the
// tryMetricsEndpointLocked path, which holds c.mu.Lock() and cannot
// re-enter as a reader — sync.RWMutex isn't reentrant.
func (c *CarettaSource) applyHeaders(req *http.Request) {
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
}

// NewCarettaSource creates a new Caretta traffic source
func NewCarettaSource(client kubernetes.Interface) *CarettaSource {
	return &CarettaSource{
		k8sClient: client,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		inCluster: k8s.IsInCluster(),
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

	// Pods left behind by a dead install, kept only as a last resort: returning on
	// them would hide a healthy Caretta running in another namespace and pin store
	// discovery to the wrong release.
	var stopped []corev1.Pod

	for _, ns := range namespacesToCheck {
		pods, err := c.k8sClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: carettaAppLabel,
		})
		if err != nil || len(pods.Items) == 0 {
			continue
		}

		if anyRunning(pods.Items) {
			return c.resultFromPods(pods.Items, result), nil
		}
		if stopped == nil {
			stopped = pods.Items
		}
	}

	// The chart's namespace follows the Helm release, so an install that landed
	// outside the three names above is still a real Caretta. One labelled
	// cluster-wide list finds it, and covers the namespaces above too, so its
	// result supersedes anything held back. Where that list is denied we fall back
	// to what the fixed names turned up, which is the behavior without this lookup.
	if pods, err := c.k8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: carettaAppLabel}); err == nil && len(pods.Items) > 0 {
		return c.resultFromPods(pods.Items, result), nil
	}

	if stopped != nil {
		return c.resultFromPods(stopped, result), nil
	}

	// Also check for DaemonSet
	for _, ns := range namespacesToCheck {
		ds, err := c.k8sClient.AppsV1().DaemonSets(ns).Get(ctx, "caretta", metav1.GetOptions{})
		if err == nil {
			c.mu.Lock()
			c.detectedNamespace = ns
			c.detectedInstance = ds.Labels[carettaInstanceLabel]
			if ds.Status.NumberReady > 0 {
				c.isConnected = true
			}
			c.mu.Unlock()

			// DaemonSet exists, check its status
			if ds.Status.NumberReady > 0 {
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

func anyRunning(pods []corev1.Pod) bool {
	for i := range pods {
		if pods[i].Status.Phase == corev1.PodRunning {
			return true
		}
	}
	return false
}

// resultFromPods records where Caretta was found and fills in the detection
// result. Pods are sorted so a multi-namespace match resolves the same way every
// call — the recorded namespace decides where the metrics store is looked up.
func (c *CarettaSource) resultFromPods(pods []corev1.Pod, result *DetectionResult) *DetectionResult {
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})

	// A running pod represents the install better than a crashlooping one, and
	// namespace and release must come from the same pod — they together decide
	// where the metrics store is looked up and which store is trusted as its own.
	running := 0
	chosen := &pods[0]
	for i := range pods {
		if pods[i].Status.Phase == "Running" {
			if running == 0 {
				chosen = &pods[i]
			}
			running++
		}
	}

	c.mu.Lock()
	c.detectedNamespace = chosen.Namespace
	c.detectedInstance = chosen.Labels[carettaInstanceLabel]
	if running > 0 {
		c.isConnected = true
	}
	c.mu.Unlock()

	if running == 0 {
		result.Message = fmt.Sprintf("Caretta pods found in %s but none are running (%d total)", chosen.Namespace, len(pods))
		return result
	}

	result.Available = true
	result.Message = fmt.Sprintf("Caretta detected with %d running pod(s) in namespace %s", running, chosen.Namespace)
	if ver, ok := chosen.Labels["app.kubernetes.io/version"]; ok {
		result.Version = ver
	}
	return result
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
		c.mu.RLock()
		warning := c.backendWarning
		c.mu.RUnlock()
		if warning == "" {
			warning = noBackendWarning(nil, nil)
		}
		return &FlowsResponse{
			Source:    "caretta",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   warning,
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

	response := &FlowsResponse{
		Source:    "caretta",
		Timestamp: time.Now(),
		Flows:     flows,
	}

	// An unverified backend that returns nothing is indistinguishable from a quiet
	// cluster in the UI. Say which backend answered so the user can tell "no
	// traffic" apart from "reading the wrong database".
	if len(flows) == 0 {
		c.mu.RLock()
		verified, ns, svc := c.backendVerified, c.metricsNamespace, c.metricsService
		c.mu.RUnlock()
		if !verified {
			target := "the configured metrics URL"
			if ns != "" && svc != "" {
				target = fmt.Sprintf("%s/%s", ns, svc)
			}
			response.Warning = fmt.Sprintf("Connected to %s, which holds no Caretta metrics. "+
				"Point Radar at Caretta's own metrics store (caretta-vm) with --prometheus-url, "+
				"or reinstall Caretta with its bundled VictoriaMetrics.", target)
		}
	}

	return response, nil
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
		if c.revalidateBoundLocked(ctx, testAddr) {
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
			c.boundIsCarettaStore = false
			c.backendVerified = c.carettaMetricsPresentLocked(ctx, addr)
			c.backendWarning = ""
			return addr
		}
		log.Printf("[caretta] Manual metrics URL %s not reachable", addr)
		return ""
	}

	// Layer 2+3: Well-known locations, then dynamic discovery
	candidates := c.discoverServiceLocked(ctx)
	if len(candidates) == 0 {
		log.Printf("[caretta] No Prometheus/VictoriaMetrics service found via any discovery method")
		c.backendWarning = noBackendWarning(nil, nil)
		return ""
	}

	var noData, unreachable []string
	for _, info := range candidates {
		wrongData := false

		if c.inCluster {
			// In-cluster: the Service address resolves and answers in single-digit
			// ms. Probe it directly — a managed forward isn't expected here (no
			// pods/portforward RBAC), so a cluster-address probe is the only path,
			// and one that can't be reached is a real diagnosis (not the local
			// "Connect() will port-forward" case), so record it.
			switch c.acceptBackendLocked(ctx, info, info.clusterAddr+info.basePath) {
			case backendAccepted:
				log.Printf("[caretta] Found metrics service at %s (basePath=%q)", info.clusterAddr, info.basePath)
				c.bindLocked(info.clusterAddr, info)
				return info.clusterAddr
			case backendNoCarettaData:
				wrongData = true
			case backendUnreachable:
				unreachable = append(unreachable, fmt.Sprintf("%s/%s", info.namespace, info.name))
			}
		} else {
			// Local: a cluster address can't resolve from here, so only an already
			// running managed forward is reachable. Reuse one only if it targets the
			// SAME service we just discovered. A generic reachability probe can't tell
			// caretta-vm apart from the cluster's general Prometheus (both answer
			// /api/v1/query), so match on (namespace, service) — otherwise we'd adopt
			// the general-metrics forward and query it for caretta_links_observed,
			// which returns 0 flows silently. Starting a new forward is Connect()'s job.
			if pfAddr := portforward.GetAddressForService(portforward.OwnerTraffic, c.currentContext, info.namespace, info.name); pfAddr != "" {
				switch c.acceptBackendLocked(ctx, info, pfAddr+info.basePath) {
				case backendAccepted:
					log.Printf("[caretta] Using managed port-forward at %s for %s/%s", pfAddr, info.namespace, info.name)
					c.bindLocked(pfAddr, info)
					return pfAddr
				case backendNoCarettaData:
					wrongData = true
				}
			}
		}

		if wrongData {
			noData = append(noData, fmt.Sprintf("%s/%s", info.namespace, info.name))
		}
	}

	// Nothing bound. In-cluster, a candidate that couldn't be reached is a real
	// diagnosis, so name it. Local, a cluster address unreachable here is the
	// normal case — Connect() port-forwards to the candidates — so nothing is
	// recorded as unreachable and the warning falls back to the generic message.
	if c.inCluster {
		log.Printf("[caretta] No Caretta-backed metrics service reachable in-cluster.")
	} else {
		log.Printf("[caretta] No Caretta-backed metrics service reachable locally. Call Connect() for port-forward.")
	}
	c.backendWarning = noBackendWarning(noData, unreachable)
	return ""
}

// discoverServiceLocked returns candidate metrics backends in Caretta priority
// order: Caretta's own store (Layer 1), then a well-known Prometheus (Layer 2),
// then dynamic discovery (Layer 3) when the earlier layers found nothing.
//
// It returns a list rather than a single service because existence is not proof:
// a cluster's general Prometheus answers PromQL just as well as Caretta's store
// but holds no caretta_links_observed, so the caller probes down the list.
// Caller must hold lock.
func (c *CarettaSource) discoverServiceLocked(ctx context.Context) []*metricsServiceInfo {
	var candidates []*metricsServiceInfo
	seen := map[string]bool{}
	add := func(info *metricsServiceInfo) {
		if info == nil || seen[info.namespace+"/"+info.name] {
			return
		}
		seen[info.namespace+"/"+info.name] = true
		candidates = append(candidates, info)
	}

	add(c.findCarettaStoreLocked(ctx))
	for _, info := range c.findMetricsServicesLocked(ctx) {
		add(info)
	}
	// Dynamic discovery is the last resort: it costs a cluster-wide Service list
	// plus a scoring pass, and the well-known list already covers the mainstream
	// installs. Run it only when nothing else turned anything up.
	if len(candidates) == 0 {
		add(c.discoverMetricsServiceDynamic(ctx))
	}

	// The cap bounds how long a local Connect can take — every candidate past the
	// first costs a port-forward attempt. In-cluster each candidate costs only a
	// cheap cluster-address probe and is never port-forwarded, so the cap is
	// dropped there and every candidate is considered. Log what a local truncation
	// drops: a silent truncation reads as "nothing else was available".
	if !c.inCluster && len(candidates) > maxMetricsCandidates {
		for _, dropped := range candidates[maxMetricsCandidates:] {
			log.Printf("[caretta] Not trying %s/%s: candidate limit of %d reached", dropped.namespace, dropped.name, maxMetricsCandidates)
		}
		candidates = candidates[:maxMetricsCandidates]
	}
	return candidates
}

// findCarettaStoreLocked looks for Caretta's own metrics store in the namespace
// Caretta was detected in. The chart pins the service name but its namespace
// follows the Helm release, so a hardcoded namespace/name pair misses every
// install that didn't land in "caretta" — and discovery then walks on to the
// cluster's general Prometheus, which holds no Caretta metrics.
// Caller must hold lock.
func (c *CarettaSource) findCarettaStoreLocked(ctx context.Context) *metricsServiceInfo {
	// Connect can run before anything has called Detect, leaving the namespace
	// unknown. Look where Detect would have looked rather than assuming the
	// default namespace name, or Caretta's own store is invisible on exactly the
	// installs this lookup exists to handle.
	if c.detectedNamespace != "" {
		return c.carettaStoreInLocked(ctx, c.detectedNamespace)
	}

	for _, ns := range []string{carettaNamespace, "default", "kube-system"} {
		if info := c.carettaStoreInLocked(ctx, ns); info != nil {
			return info
		}
	}

	// Still nothing: the install may be in a namespace nobody has named yet. One
	// cluster-wide list finds a store carrying the name the chart pins.
	svcs, err := c.k8sClient.CoreV1().Services("").List(ctx, metav1.ListOptions{LabelSelector: carettaStoreLabel})
	if err != nil {
		return nil
	}
	sort.Slice(svcs.Items, func(i, j int) bool { return svcs.Items[i].Name < svcs.Items[j].Name })
	for _, svc := range svcs.Items {
		if svc.Name == carettaStoreService {
			return carettaStoreInfo(svc, true)
		}
	}
	return nil
}

// carettaStoreInLocked looks for Caretta's metrics store in one namespace.
// Caller must hold lock.
func (c *CarettaSource) carettaStoreInLocked(ctx context.Context, ns string) *metricsServiceInfo {
	svcs, err := c.k8sClient.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: carettaStoreLabel})
	if err == nil && len(svcs.Items) > 0 {
		sort.Slice(svcs.Items, func(i, j int) bool { return svcs.Items[i].Name < svcs.Items[j].Name })
		for _, svc := range svcs.Items {
			if c.ownsStore(svc) {
				return carettaStoreInfo(svc, true)
			}
		}
		// A VictoriaMetrics that merely shares Caretta's namespace proves nothing —
		// `default` and `monitoring` host plenty of unrelated ones. Offer it, but make
		// it earn admission on content like any other third-party backend.
		return carettaStoreInfo(svcs.Items[0], false)
	}

	// The List can fail on get-but-not-list RBAC, and a store whose labels were
	// overridden won't match the selector — fall back to the pinned name.
	svc, err := c.k8sClient.CoreV1().Services(ns).Get(ctx, carettaStoreService, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return carettaStoreInfo(*svc, true)
}

// ownsStore reports whether a metrics service demonstrably belongs to the Caretta
// install that was detected: either the name the chart pins, or the same Helm
// release as Caretta's own pods. Only then is the store trusted without a content
// probe.
func (c *CarettaSource) ownsStore(svc corev1.Service) bool {
	if svc.Name == carettaStoreService {
		return true
	}
	return c.detectedInstance != "" && svc.Labels[carettaInstanceLabel] == c.detectedInstance
}

func carettaStoreInfo(svc corev1.Service, owned bool) *metricsServiceInfo {
	port := resolveServicePort(svc, 0)
	if owned {
		log.Printf("[caretta] Found Caretta metrics store: %s/%s:%d", svc.Namespace, svc.Name, port)
	} else {
		log.Printf("[caretta] Found unattributed metrics service %s/%s:%d in Caretta's namespace, will verify contents", svc.Namespace, svc.Name, port)
	}
	return &metricsServiceInfo{
		namespace:      svc.Namespace,
		name:           svc.Name,
		port:           port,
		targetPort:     resolveTargetPort(svc, port),
		clusterAddr:    buildClusterAddr(svc.Name, svc.Namespace, port),
		isCarettaStore: owned,
	}
}

// backendVerdict is why a candidate backend was accepted or turned down. The two
// rejections are different problems for the user — one is a broken connection,
// the other is a healthy connection to the wrong database — so they are reported
// separately rather than collapsed into "not available".
type backendVerdict int

const (
	backendAccepted backendVerdict = iota
	backendUnreachable
	backendNoCarettaData
)

// acceptBackendLocked decides whether an endpoint is the right backend for
// Caretta. Caretta's own store is accepted on identity — a freshly installed
// Caretta legitimately holds no series yet. Anything else has to prove it carries
// Caretta data, because the generic reachability probe cannot tell the cluster's
// general Prometheus apart from Caretta's store and binding to the former yields
// successful, permanently empty queries.
// Caller must hold lock.
func (c *CarettaSource) acceptBackendLocked(ctx context.Context, info *metricsServiceInfo, addr string) backendVerdict {
	if !c.tryMetricsEndpointLocked(ctx, addr) {
		return backendUnreachable
	}
	if info != nil && info.isCarettaStore {
		return backendAccepted
	}
	if c.carettaMetricsPresentLocked(ctx, addr) {
		return backendAccepted
	}
	log.Printf("[caretta] Backend %s answers PromQL but holds no Caretta metrics, skipping", addr)
	return backendNoCarettaData
}

// carettaMetricsPresentLocked reports whether the backend at addr holds Caretta
// data. Observed links are the direct signal; the scrape target being up covers
// both a fresh install that hasn't seen a connection yet and the deployment where
// the cluster's Prometheus scrapes Caretta itself and is the correct backend.
// Caller must hold lock.
func (c *CarettaSource) carettaMetricsPresentLocked(ctx context.Context, addr string) bool {
	for _, query := range []string{`count(caretta_links_observed)`, `count(up{job=~".*caretta.*"})`} {
		if c.hasSeriesLocked(ctx, addr, query) {
			return true
		}
	}
	return false
}

// hasSeriesLocked runs query against addr and reports whether it returned any
// sample. Caller must hold lock.
func (c *CarettaSource) hasSeriesLocked(ctx context.Context, addr, query string) bool {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(queryCtx, "GET", fmt.Sprintf("%s/api/v1/query?query=%s", addr, url.QueryEscape(query)), nil)
	if err != nil {
		return false
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var promResp prometheusResponse
	if err := json.NewDecoder(resp.Body).Decode(&promResp); err != nil {
		return false
	}
	return promResp.Status == "success" && len(promResp.Data.Result) > 0
}

// bindLocked records the backend the source will query from now on.
// Caller must hold lock.
func (c *CarettaSource) bindLocked(addr string, info *metricsServiceInfo) {
	c.prometheusAddr = addr
	c.backendWarning = ""
	if info != nil {
		c.metricsBasePath = info.basePath
		c.metricsNamespace = info.namespace
		c.metricsService = info.name
		c.metricsPort = info.port
		c.boundIsCarettaStore = info.isCarettaStore
		c.backendVerified = true
	}
}

// revalidateBoundLocked re-checks a cached address. Reachability alone is enough
// for Caretta's own store, but a third-party backend was admitted because it held
// Caretta data at bind time and can stop scraping Caretta later — leaving
// backendVerified stale would suppress the zero-flow warning and put the silence
// back. Caller must hold lock.
func (c *CarettaSource) revalidateBoundLocked(ctx context.Context, addr string) bool {
	if !c.tryMetricsEndpointLocked(ctx, addr) {
		return false
	}
	if !c.boundIsCarettaStore {
		c.backendVerified = c.carettaMetricsPresentLocked(ctx, addr)
	}
	return true
}

// ConnectionInfo implements ConnectionReporter. A binding that rides a managed
// forward defers to the live registry — a forward that has since died must not
// read as connected — while direct in-cluster and manual-URL bindings report
// the stored state their queries actually use.
func (c *CarettaSource) ConnectionInfo() *portforward.ConnectionInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	addr := c.prometheusAddr
	if addr == "" {
		return &portforward.ConnectionInfo{Connected: false}
	}
	if c.metricsURL == "" && strings.HasPrefix(addr, "http://localhost:") {
		// Bound through a managed forward (traffic's own, or a reused peer's) —
		// alive only while the registry still holds that exact address.
		if live := portforward.GetAddressForService(portforward.OwnerTraffic, c.currentContext, c.metricsNamespace, c.metricsService); live != addr {
			return &portforward.ConnectionInfo{Connected: false}
		}
	}
	return &portforward.ConnectionInfo{
		Connected:   true,
		Address:     addr,
		Namespace:   c.metricsNamespace,
		ServiceName: c.metricsService,
		ContextName: c.currentContext,
	}
}

// stopStaleTrafficForward drops the traffic-owned forward when it points at a
// service other than the one being bound. A candidate refused mid-walk can leave
// its forward running, which would make the reported connection name a different
// service than the one being queried.
func stopStaleTrafficForward(namespace, name string) {
	pf := portforward.GetConnectionInfo(portforward.OwnerTraffic)
	if pf.Connected && (pf.Namespace != namespace || pf.ServiceName != name) {
		log.Printf("[caretta] Dropping refused port-forward to %s/%s", pf.Namespace, pf.ServiceName)
		portforward.Stop(portforward.OwnerTraffic)
	}
}

// noBackendWarning explains why no backend was bound, so the UI can say why Live
// Traffic is empty instead of showing an indistinguishable "no traffic yet".
// Reached-but-wrong and never-reached are separate problems and read as such.
func noBackendWarning(noData, unreachable []string) string {
	switch {
	case len(noData) > 0:
		return fmt.Sprintf("Connected to %s, which holds no Caretta metrics. Caretta's own metrics store (caretta-vm) was not found — "+
			"reinstall Caretta with its bundled VictoriaMetrics, or point Radar at the backend holding Caretta data with --prometheus-url.",
			strings.Join(noData, ", "))
	case len(unreachable) > 0:
		return fmt.Sprintf("Found %s but could not reach it. Check that the service is running and its pods are ready.",
			strings.Join(unreachable, ", "))
	default:
		return "Prometheus/VictoriaMetrics service not found. Ensure Caretta's metrics backend is deployed."
	}
}

// queryPrometheusForFlows queries Prometheus for caretta_links_observed metrics
func (c *CarettaSource) queryPrometheusForFlows(ctx context.Context, promAddr string, basePath string, opts FlowOptions) ([]Flow, error) {
	// Build PromQL query for Caretta's link metric
	// caretta_links_observed{client_name, client_namespace, server_name, server_namespace, server_port, ...}
	query := "caretta_links_observed"
	if opts.Namespace != "" {
		// Filter by namespace (either client or server)
		safeNS := prom.SanitizeLabelValue(opts.Namespace)
		query = fmt.Sprintf(`caretta_links_observed{client_namespace="%s"} or caretta_links_observed{server_namespace="%s"}`,
			safeNS, safeNS)
	}

	queryURL := fmt.Sprintf("%s%s/api/v1/query?query=%s", promAddr, basePath, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.applyHeaders(req)

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
	c.detectedNamespace = ""
	c.detectedInstance = ""
	c.boundIsCarettaStore = false
	c.backendVerified = false
	c.backendWarning = ""
	c.closed = true
	return nil
}

// Connect establishes connection to metrics service, starting port-forward if needed
// contextName is the current K8s context name, used to validate port-forward belongs to right cluster
func (c *CarettaSource) Connect(ctx context.Context, contextName string) (*portforward.ConnectionInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// A Connect that raced Close (context switch) must not resurrect the
	// source — its forward would point at the previous cluster and outlive
	// Reset's cleanup.
	if c.closed {
		return &portforward.ConnectionInfo{
			Connected: false,
			Error:     "traffic source closed (context switched)",
		}, nil
	}

	// If already connected to the same context, check if still valid
	if c.prometheusAddr != "" && c.currentContext == contextName {
		testAddr := c.prometheusAddr + c.metricsBasePath
		if c.revalidateBoundLocked(ctx, testAddr) {
			return &portforward.ConnectionInfo{
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
			c.boundIsCarettaStore = false
			c.backendVerified = c.carettaMetricsPresentLocked(ctx, addr)
			c.backendWarning = ""
			return &portforward.ConnectionInfo{
				Connected:   true,
				Address:     addr,
				ContextName: contextName,
			}, nil
		}
		return &portforward.ConnectionInfo{
			Connected: false,
			Error:     fmt.Sprintf("Manual metrics URL %s is not reachable. Check the URL and ensure the service is running.", addr),
		}, nil
	}

	// Layer 2+3: Well-known locations, then dynamic discovery
	candidates := c.discoverServiceLocked(ctx)
	if len(candidates) == 0 {
		return &portforward.ConnectionInfo{
			Connected: false,
			Error:     "No Prometheus/VictoriaMetrics service found. Use --prometheus-url to specify manually.",
		}, nil
	}

	// Walk the candidates in Caretta priority order. Existence is not proof: the
	// cluster's general Prometheus answers PromQL but holds no Caretta series, so
	// each candidate has to be accepted by acceptBackendLocked before it is bound.
	var noData, unreachable []string
	var lastErr string
	for _, info := range candidates {
		target := fmt.Sprintf("%s/%s", info.namespace, info.name)

		if c.inCluster {
			// In-cluster: the Service address resolves and answers in single-digit ms,
			// and pods/portforward is normally denied — so probe the cluster address
			// and never fall back to a forward that can't be opened. A rejected
			// candidate here is terminal: record why and move to the next.
			switch c.acceptBackendLocked(ctx, info, info.clusterAddr+info.basePath) {
			case backendAccepted:
				log.Printf("[caretta] Connected to metrics service at %s (basePath=%q)", info.clusterAddr, info.basePath)
				// Queries go to the cluster address from here, so the traffic module needs
				// no forward of its own. An earlier candidate in this same walk may have
				// left one up after being refused — keeping it would make the reported
				// connection name a different service than the one being queried.
				portforward.Stop(portforward.OwnerTraffic)
				c.bindLocked(info.clusterAddr, info)
				return &portforward.ConnectionInfo{
					Connected:   true,
					Address:     info.clusterAddr,
					Namespace:   info.namespace,
					ServiceName: info.name,
					ContextName: contextName,
				}, nil
			case backendNoCarettaData:
				noData = append(noData, target)
			case backendUnreachable:
				unreachable = append(unreachable, target)
			}
			continue
		}

		// Local: a cluster address can't resolve from here, and probing it costs a
		// guaranteed multi-second dead-wait per candidate — so skip it and go
		// straight to a port-forward.

		// Check if there's already a valid managed port-forward for this context that
		// targets the SAME service we discovered. Matching on (namespace, service)
		// stops the traffic source from adopting the general-metrics forward (owner=
		// prometheus, e.g. prometheus-operated:9090): it answers the generic probe but
		// holds no caretta_links_observed, so flows would come back empty. On no match
		// we fall through and start the dedicated forward below.
		if pfAddr := portforward.GetAddressForService(portforward.OwnerTraffic, contextName, info.namespace, info.name); pfAddr != "" {
			switch c.acceptBackendLocked(ctx, info, pfAddr+info.basePath) {
			case backendAccepted:
				log.Printf("[caretta] Using existing port-forward at %s", pfAddr)
				stopStaleTrafficForward(info.namespace, info.name)
				c.bindLocked(pfAddr, info)
				return &portforward.ConnectionInfo{
					Connected:   true,
					Address:     pfAddr,
					Namespace:   info.namespace,
					ServiceName: info.name,
					ContextName: contextName,
				}, nil
			case backendNoCarettaData:
				noData = append(noData, target)
				continue
			}
		}

		// Start a new managed port-forward
		log.Printf("[caretta] Starting port-forward to %s/%s:%d (targetPort=%d)", info.namespace, info.name, info.port, info.targetPort)
		connInfo, err := portforward.Start(portforward.OwnerTraffic, ctx, info.namespace, info.name, info.targetPort, contextName)
		if err != nil {
			lastErr = fmt.Sprintf("Failed to start port-forward to %s/%s: %v", info.namespace, info.name, err)
			log.Printf("[caretta] %s", lastErr)
			continue
		}

		switch c.acceptBackendLocked(ctx, info, connInfo.Address+info.basePath) {
		case backendAccepted:
			c.bindLocked(connInfo.Address, info)
			log.Printf("[caretta] Connected via port-forward at %s (basePath=%q)", connInfo.Address, info.basePath)
			return connInfo, nil
		case backendNoCarettaData:
			noData = append(noData, target)
		case backendUnreachable:
			unreachable = append(unreachable, target)
		}
	}

	// Every candidate was rejected. Leaving the last forward up would point the
	// traffic module at a backend it just refused, so drop it and fail closed with
	// a message naming what was tried — silently returning zero flows is what made
	// this class of bug invisible.
	portforward.Stop(portforward.OwnerTraffic)
	errMsg := noBackendWarning(noData, unreachable)
	if len(noData) == 0 && len(unreachable) == 0 && lastErr != "" {
		// Keep the underlying error, but say what was being attempted. On its own
		// something like a port-forward RBAC denial reads as an unrelated
		// permissions problem rather than "Caretta's metrics store wasn't found".
		errMsg = fmt.Sprintf("No metrics backend holding Caretta data could be reached. %s", lastErr)
	}
	c.backendWarning = errMsg
	c.backendVerified = false
	return &portforward.ConnectionInfo{
		Connected: false,
		Error:     errMsg,
	}, nil
}

// metricsServiceInfo holds info about a discovered metrics service
type metricsServiceInfo struct {
	namespace      string
	name           string
	port           int // service port (for cluster-internal address)
	targetPort     int // container port (for port-forwarding to pod)
	clusterAddr    string
	basePath       string // sub-path for Prometheus API (e.g. "/select/0/prometheus" for vmselect)
	isCarettaStore bool   // Caretta's own metrics store, accepted without a content probe
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

// resolveTargetPort returns the container port for port-forwarding.
// When the service port differs from the container's targetPort (e.g., service:80 → container:9090),
// port-forwarding needs the container port since it bypasses the Service and connects directly to the pod.
func resolveTargetPort(svc corev1.Service, servicePort int) int {
	for _, p := range svc.Spec.Ports {
		if int(p.Port) == servicePort {
			if p.TargetPort.IntVal > 0 {
				return int(p.TargetPort.IntVal)
			}
			return servicePort
		}
	}
	return servicePort
}

// findMetricsServicesLocked returns every well-known location that exists, in
// declared order. All of them, not just the first: on a cluster running both
// VictoriaMetrics and kube-prometheus-stack, the earlier match may hold no
// Caretta data while a later one scrapes Caretta and is the right backend.
// Stopping at the first hit would fail closed there.
// Caller must hold lock.
func (c *CarettaSource) findMetricsServicesLocked(ctx context.Context) []*metricsServiceInfo {
	var found []*metricsServiceInfo
	for _, loc := range metricsServiceLocations {
		svc, err := c.k8sClient.CoreV1().Services(loc.namespace).Get(ctx, loc.name, metav1.GetOptions{})
		if err != nil {
			continue
		}

		port := resolveServicePort(*svc, loc.port)
		clusterAddr := buildClusterAddr(svc.Name, svc.Namespace, port)
		tp := resolveTargetPort(*svc, port)

		log.Printf("[caretta] Found metrics service: %s/%s:%d (targetPort=%d)", svc.Namespace, svc.Name, port, tp)
		found = append(found, &metricsServiceInfo{
			namespace:   svc.Namespace,
			name:        svc.Name,
			port:        port,
			targetPort:  tp,
			clusterAddr: clusterAddr,
			basePath:    loc.basePath,
			// The well-known list still carries caretta-vm, for the split install
			// whose store sits outside the namespace Caretta itself runs in. Mark it
			// so it is accepted on identity here too, or a store with no links yet
			// would be admitted by one discovery path and rejected by the other.
			isCarettaStore: svc.Name == carettaStoreService,
		})
	}

	return found
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
				targetPort:  resolveTargetPort(svc, port),
				clusterAddr: buildClusterAddr(svc.Name, svc.Namespace, port),
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

	// Validate top candidates via API probe — only in-cluster, where a cluster
	// address resolves. Locally the probe can't succeed and costs a dead-wait per
	// candidate, so skip straight to returning the best candidate for the caller
	// to port-forward.
	if c.inCluster {
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
	}

	// No candidate was reachable in-cluster (or running locally, where the probe
	// was skipped). Return the highest-scored candidate — the caller can establish
	// a port-forward.
	best := &candidates[0]
	log.Printf("[caretta] Dynamic discovery: no candidates reachable in-cluster, returning best candidate: %s/%s (score=%d)",
		best.info.namespace, best.info.name, best.score)
	return &best.info
}

// buildClusterAddr builds a cluster-internal address for a service
// A headless Service publishes A records for its ready pods under its own name,
// so the plain service address works for both kinds. Addressing a headless one as
// {service}-0.{service} assumes the StatefulSet is named after the Service: true
// for caretta-vm, false for kube-prometheus-stack's prometheus-operated, whose
// pod is prometheus-{release}-kube-prometheus-stack-prometheus-0. That guess made
// a reachable backend look unreachable in-cluster.
func buildClusterAddr(name, namespace string, port int) string {
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
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// See client.go probe — auth failures must not look like "not found".
		errorlog.Record("traffic", "error", "metrics endpoint %s returned HTTP %d (check --prometheus-header credentials)", addr, resp.StatusCode)
	}
	return resp.StatusCode == http.StatusOK
}

// GetMetricsServiceInfo returns info about the detected metrics service for display
func (c *CarettaSource) GetMetricsServiceInfo() (namespace, service string, port int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metricsNamespace, c.metricsService, c.metricsPort
}

// queryPrometheusRaw executes a PromQL query and returns the parsed response.
// Used by IstioSource to share Prometheus discovery infrastructure.
func (c *CarettaSource) queryPrometheusRaw(ctx context.Context, query string) (*prometheusResponse, error) {
	c.mu.RLock()
	promAddr := c.prometheusAddr
	basePath := c.metricsBasePath
	c.mu.RUnlock()

	if promAddr == "" {
		promAddr = c.discoverPrometheus(ctx)
		if promAddr == "" {
			return nil, fmt.Errorf("prometheus not found")
		}
		c.mu.RLock()
		basePath = c.metricsBasePath
		c.mu.RUnlock()
	}

	queryURL := fmt.Sprintf("%s%s/api/v1/query?query=%s", promAddr, basePath, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.applyHeaders(req)

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

	return &promResp, nil
}
