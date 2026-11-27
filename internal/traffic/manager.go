package traffic

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// Manager handles traffic source detection and management
type Manager struct {
	lifetime       context.Context
	cancel         context.CancelFunc
	handles        *portforward.Handles
	closeOnce      sync.Once
	k8sClient      kubernetes.Interface
	sources        map[string]TrafficSource
	activeSource   TrafficSource
	sourceSnapshot atomic.Value
	clusterInfo    *ClusterInfo
	contextName    string // current K8s context name
	mu             sync.RWMutex

	preferredSource string

	// The last detection, for callers that only ask whether traffic is
	// available. A context switch builds a new Manager, so it never outlives
	// the cluster it describes.
	lastSources      *SourcesResponse
	lastDetectedAt   time.Time
	metrics          *prometheus.Client
	lastMetricsEpoch uint64
}

func NewManager(client kubernetes.Interface, forwards *portforward.Manager, contextName, metricsURL string, metricsHeaders map[string]string) *Manager {
	lifetime, cancel := context.WithCancel(context.Background())
	handles := &portforward.Handles{}
	hubble := NewHubbleSource(client)
	hubble.forwards, hubble.handles, hubble.lifetime = forwards, handles, lifetime
	caretta := NewCarettaSource(client)
	caretta.forwards, caretta.handles = forwards, handles
	caretta.metricsURL = metricsURL
	caretta.headers = metricsHeaders
	return &Manager{
		lifetime:    lifetime,
		cancel:      cancel,
		handles:     handles,
		k8sClient:   client,
		contextName: contextName,
		sources: map[string]TrafficSource{
			"hubble": hubble, "caretta": caretta,
			"istio": NewIstioSource(client), "beyla": NewBeylaSource(client),
		},
	}
}

func NewConnectionManager(conn *k8s.Connection, client *prometheus.Client, selector string) *Manager {
	url, headers := client.CurrentConfig()
	m := NewManager(conn.ClientInterface(), conn.PortForwards(), conn.ContextName(), url, headers)
	m.metrics = client
	inputs := &metricsInputs{client: client, cache: conn.ResourceCache(), url: url, job: selector}
	if client != nil {
		inputs.resolve = func(ctx context.Context, metric, selector string, window time.Duration) (string, prometheus.SeriesIsolation, error) {
			return client.ResolveTrafficScope(ctx, metric, selector, window, inputs.cache)
		}
		inputs.spansClusters = client.TrafficSpansClusters
		inputs.operatorScope = client.OperatorScopeDuringRelay
		inputs.relayActive = client.RelayActive
	}
	caretta := m.sources["caretta"].(*CarettaSource)
	caretta.metrics = inputs
	beyla := m.sources["beyla"].(*BeylaSource)
	beyla.metrics = inputs
	istio := m.sources["istio"].(*IstioSource)
	istio.metrics = inputs
	istio.waypointsFn = func() map[istioWorkload]bool { return istioWaypoints(inputs.cache) }
	istio.ambientNamespaceFn = func() []string { return ambientNamespaces(inputs.cache) }
	return m
}

// DetectSources checks all registered traffic sources and returns detection results
func (m *Manager) DetectSources(ctx context.Context) (*SourcesResponse, error) {
	ctx, release := portforward.WithLifetime(ctx, m.lifetime)
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	return m.detectSourcesLocked(ctx)
}

// RecentSources returns the last detection when it is younger than maxAge,
// and detects otherwise. It is for pages that only ask whether a source is
// available: detection probes every source in turn while holding the manager
// lock, which flow reads wait on. Concurrent callers share one detection, the
// later ones finding the first one's result.
func (m *Manager) RecentSources(ctx context.Context, maxAge time.Duration) (*SourcesResponse, error) {
	ctx, release := portforward.WithLifetime(ctx, m.lifetime)
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastSources != nil && time.Since(m.lastDetectedAt) < maxAge && m.lastMetricsEpoch == m.metrics.BackendEpoch() {
		response := *m.lastSources
		response.Detected = slices.Clone(m.lastSources.Detected)
		response.NotDetected = slices.Clone(m.lastSources.NotDetected)
		response.Active = ""
		if m.activeSource != nil {
			response.Active = m.activeSource.Name()
		}
		return &response, nil
	}
	return m.detectSourcesLocked(ctx)
}

func (m *Manager) detectSourcesLocked(ctx context.Context) (*SourcesResponse, error) {
	epoch := m.metrics.BackendEpoch()
	// Detect cluster info first
	clusterInfo, err := m.detectClusterInfo(ctx)
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		if portforward.IsMemberClosed(ctx, err) {
			return nil, err
		}
		log.Printf("[traffic] Warning: failed to detect cluster info: %v", err)
		clusterInfo = &ClusterInfo{Platform: "generic"}
	}

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
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		if portforward.IsMemberClosed(ctx, err) {
			return nil, err
		}
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

	// A request cancelled midway reads as every remaining source failing. The
	// active source and the remembered detection are shared by every user, so
	// that must not switch traffic off for them.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.clusterInfo = clusterInfo
	m.reconcileActiveSource(available)

	// Set active source name in response
	if m.activeSource != nil {
		response.Active = m.activeSource.Name()
	}

	// Generate recommendation based on cluster type
	response.Recommended = m.generateRecommendation(clusterInfo, response.Detected)

	remembered := *response
	remembered.Detected = slices.Clone(response.Detected)
	remembered.NotDetected = slices.Clone(response.NotDetected)
	m.lastSources = &remembered
	m.lastDetectedAt = time.Now()
	m.lastMetricsEpoch = epoch

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

	if info.Platform == "generic" && k8score.IsNKP(ctx, m.k8sClient) {
		info.Platform = "nkp"
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

	// The Tigera operator, the install Calico's docs recommend, runs calico-node
	// in calico-system; the plain manifest keeps it in kube-system.
	if m.daemonSetExists(ctx, "calico-node", "kube-system", "calico-system") {
		return "calico", false
	}

	// The current upstream kube-flannel.yml installs into its own kube-flannel
	// namespace; older manifests used kube-system.
	if m.daemonSetExists(ctx, "kube-flannel-ds", "kube-system", "kube-flannel") {
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

func (m *Manager) daemonSetExists(ctx context.Context, name string, namespaces ...string) bool {
	for _, ns := range namespaces {
		if _, err := m.k8sClient.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			log.Printf("[traffic] Found %s/%s DaemonSet", ns, name)
			return true
		}
	}
	return false
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
		// Hubble is installed but unusable, and the detected entry already says
		// why. Advice to enable it would contradict that and send the user to
		// install what they already have.
		for _, s := range detected {
			if s.Name == "hubble" && s.Status == "not_found" {
				return nil
			}
		}
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
	ctx, release := portforward.WithLifetime(ctx, m.lifetime)
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	source := m.activeSource
	m.mu.RUnlock()

	if source == nil {
		return nil, fmt.Errorf("no traffic source available")
	}

	response, err := source.GetFlows(ctx, opts)
	if m.lifetime != nil && m.lifetime.Err() != nil {
		return nil, m.lifetime.Err()
	}
	return response, err
}

// StreamFlows returns a channel of flows from the active source
func (m *Manager) StreamFlows(ctx context.Context, opts FlowOptions) (<-chan Flow, error) {
	m.mu.RLock()
	source := m.activeSource
	m.mu.RUnlock()

	if source == nil {
		return nil, fmt.Errorf("no traffic source available")
	}

	ctx, release := portforward.WithLifetime(ctx, m.lifetime)
	flows, err := source.StreamFlows(ctx, opts)
	if err != nil {
		release()
		return nil, err
	}
	stream := make(chan Flow)
	go func() {
		defer close(stream)
		defer release()
		for {
			select {
			case <-ctx.Done():
				return
			case flow, ok := <-flows:
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					return
				case stream <- flow:
				}
			}
		}
	}()
	return stream, nil
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
		m.sourceSnapshot.Store("")
	}
	preferred := m.preferredSource
	m.preferredSource = ""
	if len(available) == 0 {
		return
	}
	m.activeSource = available[0]
	for _, source := range available {
		if source.Name() == preferred {
			m.activeSource = source
			break
		}
	}
	m.sourceSnapshot.Store(m.activeSource.Name())
}

func (m *Manager) PreferSource(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.preferredSource = name
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
	m.preferredSource = ""
	m.sourceSnapshot.Store(source.Name())
	return nil
}

func (m *Manager) SourceSnapshot() string {
	if m == nil {
		return ""
	}
	name, _ := m.sourceSnapshot.Load().(string)
	return name
}

// ActiveSourceNameIfIdle is GetActiveSourceName without waiting. Detection
// holds the manager lock while it probes every source in turn; ok is false
// while it does.
func (m *Manager) ActiveSourceNameIfIdle() (name string, ok bool) {
	if !m.mu.TryRLock() {
		return "", false
	}
	defer m.mu.RUnlock()
	if m.activeSource == nil {
		return "", true
	}
	return m.activeSource.Name(), true
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
	m.closeOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
		if m.handles != nil {
			m.handles.Close()
		}
		for name, source := range m.sources {
			go func() {
				if err := source.Close(); err != nil {
					log.Printf("[traffic] Closing %s: %v", name, err)
				}
			}()
		}
	})
	return nil
}

// Connect establishes connection to the active traffic source
// This may start a port-forward if running locally and needed
func (m *Manager) Connect(ctx context.Context) (*portforward.ConnectionInfo, error) {
	ctx, release := portforward.WithLifetime(ctx, m.lifetime)
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

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

	connector, ok := source.(interface {
		Connect(context.Context, string) (*portforward.ConnectionInfo, error)
	})
	if !ok {
		return &portforward.ConnectionInfo{Connected: true}, nil
	}
	info, err := connector.Connect(ctx, contextName)
	if m.lifetime != nil && m.lifetime.Err() != nil {
		return nil, portforward.ErrClosed
	}
	return info, err
}

// ConnectionReporter is implemented by sources that can report their own live
// connection state. Necessary because "traffic has an active port-forward" is
// not the same thing as "traffic is connected": every source prefers a direct
// in-cluster connection with no forward behind it, and the Prometheus-backed
// sources (Istio, Beyla) ride the prometheus owner's forward, not traffic's.
type ConnectionReporter interface {
	ConnectionInfo() *portforward.ConnectionInfo
}

// GetConnectionInfo reports the active source's data path, including direct
// connections and forwards borrowed from the Prometheus client.
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
	return &portforward.ConnectionInfo{}
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
