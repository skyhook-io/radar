package traffic

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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
	manager   *Manager
	initOnce  sync.Once
	initErr   error
)

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
		manager.sources["caretta"] = NewCarettaSource(client)

		// Set K8s clients for port-forward functionality
		if config != nil {
			SetK8sClients(client, config)
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

	// Check each registered source
	for name, source := range m.sources {
		result, err := source.Detect(ctx)
		if err != nil {
			log.Printf("[traffic] Error detecting %s: %v", name, err)
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
			// Set first available as active
			if m.activeSource == nil {
				m.activeSource = source
			}
		} else {
			response.NotDetected = append(response.NotDetected, name)
		}
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

		// Detect platform
		switch {
		case strings.HasPrefix(providerID, "gce://"):
			info.Platform = "gke"
			// Extract cluster name from labels
			if cn, ok := node.Labels["cloud.google.com/gke-nodepool"]; ok {
				// Parse cluster name from nodepool
				parts := strings.Split(cn, "-")
				if len(parts) > 0 {
					info.ClusterName = parts[0]
				}
			}
		case strings.HasPrefix(providerID, "aws://"):
			info.Platform = "eks"
		case strings.HasPrefix(providerID, "azure://"):
			info.Platform = "aks"
		case strings.HasPrefix(providerID, "kind://"):
			info.Platform = "kind"
		default:
			log.Printf("[traffic] Unknown providerID format, platform remains generic")
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
			Name:   "hubble",
			Reason: "Your cluster uses Cilium CNI. Enable Hubble for network observability.",
			InstallCommand: `cilium hubble enable --ui`,
			DocsURL: "https://docs.cilium.io/en/stable/gettingstarted/hubble/",
		}

	case "gke-native":
		// GKE without Dataplane V2 - recommend Caretta for existing clusters
		return &Recommendation{
			Name:      "caretta",
			Reason:    "Your GKE cluster uses standard networking. Caretta provides lightweight eBPF-based traffic visibility that works immediately.",
			HelmChart: carettaHelmChart(),
			DocsURL:   "https://github.com/groundcover-com/caretta",
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

// AggregateFlows aggregates flows by service pair
func AggregateFlows(flows []Flow) []AggregatedFlow {
	// Key: source-ns/source-name|dest-ns/dest-name|port
	aggregated := make(map[string]*AggregatedFlow)

	for _, f := range flows {
		key := fmt.Sprintf("%s/%s|%s/%s|%d",
			f.Source.Namespace, f.Source.Name,
			f.Destination.Namespace, f.Destination.Name,
			f.Port)

		if agg, ok := aggregated[key]; ok {
			agg.FlowCount++
			agg.BytesSent += f.BytesSent
			agg.BytesRecv += f.BytesRecv
			agg.Connections += f.Connections
			if f.LastSeen.After(agg.LastSeen) {
				agg.LastSeen = f.LastSeen
			}
		} else {
			aggregated[key] = &AggregatedFlow{
				Source:      f.Source,
				Destination: f.Destination,
				Protocol:    f.Protocol,
				Port:        f.Port,
				FlowCount:   1,
				BytesSent:   f.BytesSent,
				BytesRecv:   f.BytesRecv,
				Connections: f.Connections,
				LastSeen:    f.LastSeen,
			}
		}
	}

	result := make([]AggregatedFlow, 0, len(aggregated))
	for _, agg := range aggregated {
		result = append(result, *agg)
	}
	return result
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
	StopMetricsPortForward()

	if manager != nil {
		manager.Close()
	}
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
func (m *Manager) Connect(ctx context.Context) (*MetricsConnectionInfo, error) {
	m.mu.Lock()
	source := m.activeSource
	contextName := m.contextName
	m.mu.Unlock()

	if source == nil {
		return &MetricsConnectionInfo{
			Connected: false,
			Error:     "No traffic source available",
		}, nil
	}

	// Check if source supports Connect (only Caretta currently)
	if caretta, ok := source.(*CarettaSource); ok {
		return caretta.Connect(ctx, contextName)
	}

	// For sources without Connect support (like Hubble), just return connected
	return &MetricsConnectionInfo{
		Connected: true,
	}, nil
}

// GetConnectionInfo returns current connection status
func (m *Manager) GetConnectionInfo() *MetricsConnectionInfo {
	return GetConnectionInfo()
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
}

// DefaultFlowOptions returns sensible defaults
func DefaultFlowOptions() FlowOptions {
	return FlowOptions{
		Since: 5 * time.Minute,
		Limit: 1000,
	}
}

// carettaHelmChart returns the Helm chart info for Caretta
func carettaHelmChart() *HelmChartInfo {
	return &HelmChartInfo{
		Repo:      "groundcover",
		RepoURL:   "https://helm.groundcover.com/",
		ChartName: "caretta",
	}
}
