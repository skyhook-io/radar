package traffic

import (
	"context"
	"fmt"
	"log"
	"maps"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// Manager handles traffic source detection and management
type Manager struct {
	k8sClient    kubernetes.Interface
	k8sConfig    *rest.Config
	sources      map[string]TrafficSource
	activeSource TrafficSource
	clusterInfo  *ClusterInfo
	contextName  string // current K8s context name
	mu           sync.RWMutex
}

var (
	manager  *Manager
	initOnce sync.Once
	initErr  error

	// metricsConfigMu guards the configured-metrics globals below. Originally
	// these were written only at startup (no concurrent readers), but the live
	// "apply Prometheus URL" path now writes them at runtime while a concurrent
	// context-switch rebuild reads them in initOnce.Do — so the access needs a
	// lock, matching the mutex-guarded prometheus client.
	metricsConfigMu sync.RWMutex
	// configuredMetricsURL is the user-provided --prometheus-url flag value.
	// Stored at package level so it persists across context-switch resets.
	configuredMetricsURL string
	// configuredMetricsHeaders are sent with every Prometheus query — required
	// for auth-protected backends. Also persists across context switches.
	configuredMetricsHeaders map[string]string
	// configuredBeylaJobSelector overrides the default `job` label matcher used
	// to scope Beyla's Prometheus queries; empty means use the built-in default.
	configuredBeylaJobSelector string
)

// SetMetricsURL sets a manual Prometheus/VictoriaMetrics URL, bypassing auto-discovery.
func SetMetricsURL(url string) {
	metricsConfigMu.Lock()
	defer metricsConfigMu.Unlock()
	configuredMetricsURL = url
}

// SetMetricsHeaders sets HTTP headers attached to every Prometheus query.
// Used for auth-protected backends (Bearer tokens, X-Scope-OrgID, etc.).
func SetMetricsHeaders(h map[string]string) {
	metricsConfigMu.Lock()
	defer metricsConfigMu.Unlock()
	if len(h) == 0 {
		configuredMetricsHeaders = nil
		return
	}
	out := make(map[string]string, len(h))
	maps.Copy(out, h)
	configuredMetricsHeaders = out
}

// SetBeylaJobSelector overrides the `job` label matcher fragment (e.g.
// `job=~".*beyla.*"`) Beyla queries use to scope which Prometheus series they
// read — for a cluster where Alloy or Beyla runs under a non-default job
// name. Pass "" to restore the default.
func SetBeylaJobSelector(selector string) {
	metricsConfigMu.Lock()
	defer metricsConfigMu.Unlock()
	configuredBeylaJobSelector = selector
}

// BeylaJobSelector returns the configured Beyla job-label matcher fragment,
// or "" if unset.
func BeylaJobSelector() string {
	metricsConfigMu.RLock()
	defer metricsConfigMu.RUnlock()
	return configuredBeylaJobSelector
}

// metricsConfig returns the configured URL + headers under the read lock.
func metricsConfig() (string, map[string]string) {
	metricsConfigMu.RLock()
	defer metricsConfigMu.RUnlock()
	return configuredMetricsURL, configuredMetricsHeaders
}

// Initialize sets up the traffic manager with the given K8s client
func Initialize(client kubernetes.Interface) error {
	return InitializeWithConfig(client, nil, "")
}

// InitializeWithConfig sets up the traffic manager with K8s client, config, and context name
func InitializeWithConfig(client kubernetes.Interface, config *rest.Config, contextName string) error {
	initOnce.Do(func() {
		manager = &Manager{
			k8sClient:   client,
			k8sConfig:   config,
			sources:     make(map[string]TrafficSource),
			contextName: contextName,
		}
		// Register available sources
		manager.sources["hubble"] = NewHubbleSource(client)
		caretta := NewCarettaSource(client)
		metricsURL, metricsHeaders := metricsConfig()
		if metricsURL != "" {
			caretta.metricsURL = metricsURL
		}
		caretta.headers = metricsHeaders
		manager.sources["caretta"] = caretta
		manager.sources["istio"] = NewIstioSource(client)
		manager.sources["beyla"] = NewBeylaSource(client)

		// Set K8s clients for port-forward functionality
		if config != nil {
			portforward.SetK8sClients(client, config)
		}
	})
	return initErr
}

// GetManager returns the global traffic manager
func GetManager() *Manager {
	return manager
}

// DetectSources checks all registered traffic sources and returns detection results
func (m *Manager) DetectSources(ctx context.Context) (*SourcesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Detect cluster info first
	clusterInfo, err := m.detectClusterInfo(ctx)
	if err != nil {
		log.Printf("[traffic] Warning: failed to detect cluster info: %v", err)
		clusterInfo = &ClusterInfo{Platform: "generic"}
	}
	m.clusterInfo = clusterInfo

	response := &SourcesResponse{
		Cluster:     *clusterInfo,
		Detected:    []SourceStatus{},
		NotDetected: []string{},
	}

	// Check each registered source in deterministic priority order
	// (hubble has deepest visibility, istio has L7 metrics, caretta is fallback, beyla is external eBPF)
	sourceOrder := []string{"hubble", "istio", "caretta", "beyla"}
	// Collected in priority order so the active source can be reconciled against
	// what is actually available once every source has reported.
	available := make([]TrafficSource, 0, len(sourceOrder))
	for _, name := range sourceOrder {
		source, ok := m.sources[name]
		if !ok {
			continue
		}
		result, err := source.Detect(ctx)
		if err != nil {
			log.Printf("[traffic] Error detecting %s: %v", name, err)
			errorlog.Record("traffic", "warning", "error detecting %s: %v", name, err)
			// Report as error status instead of just "not detected"
			response.Detected = append(response.Detected, SourceStatus{
				Name:    name,
				Status:  "error",
				Message: err.Error(),
			})
			continue
		}

		if result.Available {
			response.Detected = append(response.Detected, SourceStatus{
				Name:    name,
				Status:  "available",
				Version: result.Version,
				Native:  result.Native,
				Message: result.Message,
			})
			available = append(available, source)
		} else if result.Present && result.Message != "" {
			// Present but unusable — installed with the wrong feature enabled, or
			// running but not scraped. That is a status with an explanation, which is
			// what SourceStatus is for and what the error branch above already does;
			// a bare name in NotDetected would flatten it into "not installed".
			// Gated on Present so a source nobody installed stays out: the
			// recommendation covers absence, and a row per uninstalled source would
			// bury the one with a fixable problem.
			response.Detected = append(response.Detected, SourceStatus{
				Name:    name,
				Status:  "not_found",
				Version: result.Version,
				Native:  result.Native,
				Message: result.Message,
			})
		} else {
			response.NotDetected = append(response.NotDetected, name)
		}
	}

	m.reconcileActiveSource(available)

	// Set active source name in response
	if m.activeSource != nil {
		response.Active = m.activeSource.Name()
	}

	// Generate recommendation based on cluster type
	response.Recommended = m.generateRecommendation(clusterInfo, response.Detected)

	return response, nil
}

// detectClusterInfo determines cluster platform and CNI
func (m *Manager) detectClusterInfo(ctx context.Context) (*ClusterInfo, error) {
	info := &ClusterInfo{
		Platform: "generic",
	}

	// Get K8s version
	version, err := m.k8sClient.Discovery().ServerVersion()
	if err != nil {
		log.Printf("[traffic] Warning: failed to get server version: %v", err)
	} else {
		info.K8sVersion = version.GitVersion
		log.Printf("[traffic] K8s version: %s", info.K8sVersion)
		if platform := k8score.DetectPlatformFromVersion(info.K8sVersion); platform != "unknown" {
			info.Platform = platform
		}
	}

	// Detect platform from nodes
	nodes, err := m.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		log.Printf("[traffic] Warning: failed to list nodes: %v", err)
	} else if len(nodes.Items) == 0 {
		log.Printf("[traffic] Warning: no nodes found in cluster")
	} else {
		node := nodes.Items[0]
		providerID := node.Spec.ProviderID
		log.Printf("[traffic] Node providerID: %q", providerID)

		platform := k8score.DetectNodePlatform(node)
		if platform == "unknown" {
			log.Printf("[traffic] Unknown node platform, platform remains generic")
		} else if info.Platform == "generic" {
			info.Platform = platform
			if platform == "gke" {
				if cn, ok := node.Labels["cloud.google.com/gke-nodepool"]; ok {
					parts := strings.Split(cn, "-")
					if len(parts) > 0 {
						info.ClusterName = parts[0]
					}
				}
			}
		}
	}

	log.Printf("[traffic] Detected platform: %s", info.Platform)

	// Detect CNI from kube-system ConfigMaps/DaemonSets
	info.CNI, info.DataplaneV2 = m.detectCNI(ctx, info.Platform)
	log.Printf("[traffic] Detected CNI: %s, DataplaneV2: %v", info.CNI, info.DataplaneV2)

	return info, nil
}

// detectCNI determines which CNI is installed
func (m *Manager) detectCNI(ctx context.Context, platform string) (string, bool) {
	hubbleEnabled := false

	// Check for Cilium - multiple ways it can be detected
	// 1. cilium-config ConfigMap (standard Cilium install)
	cm, err := m.k8sClient.CoreV1().ConfigMaps("kube-system").Get(ctx, "cilium-config", metav1.GetOptions{})
	if err == nil {
		log.Printf("[traffic] Found cilium-config ConfigMap")
		if cm.Data["enable-hubble"] == "true" {
			hubbleEnabled = true
		}
		return "cilium", hubbleEnabled
	}

	// 2. Check for Cilium DaemonSet (GKE Dataplane V2 and others)
	ciliumDS, err := m.k8sClient.AppsV1().DaemonSets("kube-system").Get(ctx, "cilium", metav1.GetOptions{})
	if err == nil {
		log.Printf("[traffic] Found cilium DaemonSet")
		// Check for hubble in the DaemonSet env vars
		for _, container := range ciliumDS.Spec.Template.Spec.Containers {
			for _, env := range container.Env {
				if env.Name == "HUBBLE_ENABLED" && env.Value == "true" {
					hubbleEnabled = true
				}
			}
		}
		return "cilium", hubbleEnabled
	}

	// 3. Check for anetd DaemonSet (GKE Dataplane V2 component)
	_, err = m.k8sClient.AppsV1().DaemonSets("kube-system").Get(ctx, "anetd", metav1.GetOptions{})
	if err == nil {
		log.Printf("[traffic] Found anetd DaemonSet (GKE Dataplane V2)")
		// anetd is part of GKE Dataplane V2 which uses Cilium
		return "cilium", hubbleEnabled
	}

	for _, name := range []string{"rke2-canal", "canal"} {
		_, err = m.k8sClient.AppsV1().DaemonSets("kube-system").Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			log.Printf("[traffic] Found %s DaemonSet", name)
			return "canal", false
		}
	}

	// Check for Calico
	_, err = m.k8sClient.AppsV1().DaemonSets("kube-system").Get(ctx, "calico-node", metav1.GetOptions{})
	if err == nil {
		log.Printf("[traffic] Found calico-node DaemonSet")
		return "calico", false
	}

	// Check for Flannel
	_, err = m.k8sClient.AppsV1().DaemonSets("kube-system").Get(ctx, "kube-flannel-ds", metav1.GetOptions{})
	if err == nil {
		log.Printf("[traffic] Found kube-flannel-ds DaemonSet")
		return "flannel", false
	}

	// Check for AWS VPC CNI
	_, err = m.k8sClient.AppsV1().DaemonSets("kube-system").Get(ctx, "aws-node", metav1.GetOptions{})
	if err == nil {
		log.Printf("[traffic] Found aws-node DaemonSet")
		return "vpc-cni", false
	}

	// Check for Azure CNI
	_, err = m.k8sClient.AppsV1().DaemonSets("kube-system").Get(ctx, "azure-cni", metav1.GetOptions{})
	if err == nil {
		log.Printf("[traffic] Found azure-cni DaemonSet")
		return "azure-cni", false
	}

	// Check for GKE native networking (ip-masq-agent indicates GKE networking)
	if platform == "gke" {
		log.Printf("[traffic] Platform is GKE, checking for ip-masq-agent")
		_, err = m.k8sClient.AppsV1().DaemonSets("kube-system").Get(ctx, "ip-masq-agent", metav1.GetOptions{})
		if err == nil {
			log.Printf("[traffic] Found ip-masq-agent DaemonSet")
			return "gke-native", false
		}
		// Fallback - GKE always has some form of networking
		log.Printf("[traffic] No ip-masq-agent found, but platform is GKE, using gke-native")
		return "gke-native", false
	}

	log.Printf("[traffic] No known CNI detected, platform: %s", platform)
	return "unknown", false
}

// generateRecommendation creates a recommendation based on cluster info
func (m *Manager) generateRecommendation(info *ClusterInfo, detected []SourceStatus) *Recommendation {
	// If any source is already available, no recommendation needed
	for _, s := range detected {
		if s.Status == "available" {
			return nil
		}
	}

	// If Istio is detected but not yet "available" (e.g., Prometheus not found),
	// recommend connecting Prometheus for Istio metrics
	for _, s := range detected {
		if s.Name == "istio" && s.Status == "error" {
			return &Recommendation{
				Name:    "istio",
				Reason:  "Istio service mesh detected but Prometheus not reachable. Use --prometheus-url to point Radar to your Prometheus instance for Istio traffic visibility.",
				DocsURL: "https://istio.io/latest/docs/ops/integrations/prometheus/",
			}
		}
	}

	switch info.CNI {
	case "cilium":
		if info.Platform == "gke" {
			return &Recommendation{
				Name:   "hubble",
				Reason: "Your GKE cluster has Cilium (Dataplane V2). Enable Hubble observability for traffic visibility.",
				InstallCommand: `gcloud container clusters update CLUSTER_NAME \
  --location=LOCATION \
  --enable-dataplane-v2-observability`,
				DocsURL: "https://cloud.google.com/kubernetes-engine/docs/how-to/dataplane-v2-observability",
			}
		}
		return &Recommendation{
			Name:           "hubble",
			Reason:         "Your cluster uses Cilium CNI. Enable Hubble for network observability.",
			InstallCommand: `cilium hubble enable --ui`,
			DocsURL:        "https://docs.cilium.io/en/stable/gettingstarted/hubble/",
		}

	case "gke-native":
		// GKE without Dataplane V2 - recommend Caretta for existing clusters
		return &Recommendation{
			Name:               "caretta",
			Reason:             "Your GKE cluster uses standard networking. Caretta provides lightweight eBPF-based traffic visibility that works immediately.",
			HelmChart:          carettaHelmChart(),
			DocsURL:            "https://github.com/groundcover-com/caretta",
			AlternativeName:    "Dataplane V2",
			AlternativeReason:  "For new GKE clusters, Dataplane V2 provides native Cilium/Hubble integration with better performance and deeper visibility.",
			AlternativeDocsURL: "https://cloud.google.com/kubernetes-engine/docs/how-to/dataplane-v2",
		}

	case "calico":
		return &Recommendation{
			Name:      "caretta",
			Reason:    "Caretta provides lightweight eBPF-based traffic visibility for Calico clusters.",
			HelmChart: carettaHelmChart(),
			DocsURL:   "https://github.com/groundcover-com/caretta",
		}

	case "canal":
		return &Recommendation{
			Name:      "caretta",
			Reason:    "Caretta provides lightweight eBPF-based traffic visibility for Canal clusters.",
			HelmChart: carettaHelmChart(),
			DocsURL:   "https://github.com/groundcover-com/caretta",
		}

	case "flannel":
		return &Recommendation{
			Name:      "caretta",
			Reason:    "Caretta provides lightweight eBPF-based traffic visibility that works with Flannel.",
			HelmChart: carettaHelmChart(),
			DocsURL:   "https://github.com/groundcover-com/caretta",
		}

	case "vpc-cni":
		// AWS EKS with VPC CNI
		return &Recommendation{
			Name:      "caretta",
			Reason:    "Caretta provides lightweight eBPF-based traffic visibility for EKS clusters with VPC CNI.",
			HelmChart: carettaHelmChart(),
			DocsURL:   "https://github.com/groundcover-com/caretta",
		}

	case "azure-cni":
		// Azure AKS
		return &Recommendation{
			Name:      "caretta",
			Reason:    "Caretta provides lightweight eBPF-based traffic visibility for AKS clusters.",
			HelmChart: carettaHelmChart(),
			DocsURL:   "https://github.com/groundcover-com/caretta",
		}

	case "unknown":
		// Unknown CNI - recommend Caretta as universal fallback
		return &Recommendation{
			Name:      "caretta",
			Reason:    "Caretta provides lightweight eBPF-based traffic visibility that works with any CNI.",
			HelmChart: carettaHelmChart(),
			DocsURL:   "https://github.com/groundcover-com/caretta",
		}

	default:
		// Fallback to Caretta for any unrecognized CNI
		return &Recommendation{
			Name:      "caretta",
			Reason:    "Caretta provides lightweight eBPF-based traffic visibility that works with any CNI.",
			HelmChart: carettaHelmChart(),
			DocsURL:   "https://github.com/groundcover-com/caretta",
		}
	}
}

// GetFlows retrieves flows from the active source
func (m *Manager) GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error) {
	m.mu.RLock()
	source := m.activeSource
	m.mu.RUnlock()

	if source == nil {
		return nil, fmt.Errorf("no traffic source available")
	}

	return source.GetFlows(ctx, opts)
}

// StreamFlows returns a channel of flows from the active source
func (m *Manager) StreamFlows(ctx context.Context, opts FlowOptions) (<-chan Flow, error) {
	m.mu.RLock()
	source := m.activeSource
	m.mu.RUnlock()

	if source == nil {
		return nil, fmt.Errorf("no traffic source available")
	}

	return source.StreamFlows(ctx, opts)
}

// reconcileActiveSource keeps the active source in step with what detection just
// found. Detection used to only ever promote, so a source that stopped being
// available stayed active: the sources response then reported it as active and
// not_found at once, and the flows endpoint answered 200 with an empty graph and
// no warning to explain it — the same permanently-empty view that requiring data
// for availability exists to prevent. Clearing it makes the flows endpoint report
// no source, which is the truth and what a freshly started Radar already says.
func (m *Manager) reconcileActiveSource(available []TrafficSource) {
	if m.activeSource != nil {
		for _, s := range available {
			if s.Name() == m.activeSource.Name() {
				return
			}
		}
		log.Printf("[traffic] Active source %s is no longer available", m.activeSource.Name())
		m.activeSource = nil
	}
	if len(available) > 0 {
		m.activeSource = available[0]
	}
}

// SetActiveSource sets the active traffic source by name
func (m *Manager) SetActiveSource(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	source, ok := m.sources[name]
	if !ok {
		return fmt.Errorf("unknown traffic source: %s", name)
	}

	m.activeSource = source
	return nil
}

// GetActiveSourceName returns the name of the active source
func (m *Manager) GetActiveSourceName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.activeSource == nil {
		return ""
	}
	return m.activeSource.Name()
}

// Close cleans up all traffic sources
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for name, source := range m.sources {
		if err := source.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing sources: %v", errs)
	}
	return nil
}

// Reset cleans up for context switching
func Reset() {
	// Stop any active metrics port-forward first
	portforward.Stop(portforward.OwnerTraffic)

	if manager != nil {
		manager.Close()
	}
	// Stop again after Close: an in-flight Connect (Close blocks on the source
	// mutex until it finishes) may have published a forward for the old cluster
	// after the first Stop.
	portforward.Stop(portforward.OwnerTraffic)
	manager = nil
	initOnce = sync.Once{}
}

// Reinitialize reinitializes after context switch
func Reinitialize(client kubernetes.Interface) error {
	Reset()
	return Initialize(client)
}

// ReinitializeWithConfig reinitializes with full config after context switch
func ReinitializeWithConfig(client kubernetes.Interface, config *rest.Config, contextName string) error {
	Reset()
	return InitializeWithConfig(client, config, contextName)
}

// Connect establishes connection to the active traffic source
// This may start a port-forward if running locally and needed
func (m *Manager) Connect(ctx context.Context) (*portforward.ConnectionInfo, error) {
	m.mu.Lock()
	source := m.activeSource
	contextName := m.contextName
	m.mu.Unlock()

	if source == nil {
		return &portforward.ConnectionInfo{
			Connected: false,
			Error:     "No traffic source available",
		}, nil
	}

	switch s := source.(type) {
	case *CarettaSource:
		return s.Connect(ctx, contextName)
	case *HubbleSource:
		return s.Connect(ctx, contextName)
	case *IstioSource:
		return s.Connect(ctx, contextName)
	case *BeylaSource:
		return s.Connect(ctx, contextName)
	default:
		// For sources without Connect support, just report connected.
		return &portforward.ConnectionInfo{Connected: true}, nil
	}
}

// ConnectionReporter is implemented by sources that can report their own live
// connection state. Necessary because "traffic has an active port-forward" is
// not the same thing as "traffic is connected": every source prefers a direct
// in-cluster connection with no forward behind it, and the Prometheus-backed
// sources (Istio, Beyla) ride the prometheus owner's forward, not traffic's.
type ConnectionReporter interface {
	ConnectionInfo() *portforward.ConnectionInfo
}

// GetConnectionInfo returns live traffic connection status, as reported by the
// active source when it can (see ConnectionReporter). The registry fallback
// deliberately does NOT report another owner's forward: a Prometheus forward
// for the same context means Prometheus is connected, not traffic.
func (m *Manager) GetConnectionInfo() *portforward.ConnectionInfo {
	m.mu.RLock()
	source := m.activeSource
	m.mu.RUnlock()

	if source == nil {
		// A leftover forward with nothing querying it is not a connection.
		return &portforward.ConnectionInfo{Connected: false}
	}
	if reporter, ok := source.(ConnectionReporter); ok {
		return reporter.ConnectionInfo()
	}
	return portforward.GetConnectionInfo(portforward.OwnerTraffic)
}

// SetContextName updates the current context name
func (m *Manager) SetContextName(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.contextName = name

	// Update caretta source context
	if caretta, ok := m.sources["caretta"].(*CarettaSource); ok {
		caretta.mu.Lock()
		caretta.currentContext = name
		caretta.mu.Unlock()
	}

	// Update hubble source context
	if hubble, ok := m.sources["hubble"].(*HubbleSource); ok {
		hubble.mu.Lock()
		hubble.currentContext = name
		hubble.mu.Unlock()
	}

	// Istio shares Prometheus via caretta, no additional context update needed
}

// carettaHelmChart returns the Helm chart info for Caretta
func carettaHelmChart() *HelmChartInfo {
	return &HelmChartInfo{
		Repo:      "groundcover",
		RepoURL:   "https://helm.groundcover.com/",
		ChartName: "caretta",
		DefaultValues: map[string]any{
			"resources": map[string]any{
				"limits": map[string]any{
					"memory": "512Mi",
				},
			},
		},
	}
}
