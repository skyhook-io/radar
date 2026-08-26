package opencost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type Source string

const (
	SourceAuto       Source = "auto"
	SourcePrometheus Source = "prometheus"
	SourceKubecost   Source = "kubecost"
	autoRetryDelay          = time.Minute
)

type ManagerConfig struct {
	Source    Source
	URL       string
	APIKey    string
	ClusterID string
}

type Connection struct {
	Source    Source
	Client    *pkgopencost.KubecostClient
	Address   string
	ClusterID string
}

type prometheusCostState int

const (
	prometheusCostAbsent prometheusCostState = iota
	prometheusCostAvailable
	prometheusCostUnknown
)

type Manager struct {
	mu           sync.RWMutex
	selectMu     sync.Mutex
	config       ManagerConfig
	selected     Source
	client       *pkgopencost.KubecostClient
	address      string
	clusterID    string
	retryAt      time.Time
	selectionErr error
	generation   uint64
	envManaged   bool
	envError     string
}

var defaultManager = &Manager{config: ManagerConfig{Source: SourceAuto}}

func ValidateSource(value string) (Source, error) {
	source := Source(strings.ToLower(strings.TrimSpace(value)))
	if source == "" {
		return SourceAuto, nil
	}
	switch source {
	case SourceAuto, SourcePrometheus, SourceKubecost:
		return source, nil
	default:
		return "", fmt.Errorf("cost source must be auto, prometheus, or kubecost")
	}
}

func ValidateKubecostURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("must be an absolute HTTP(S) URL")
	}
	if u.User != nil {
		return fmt.Errorf("must not contain embedded credentials")
	}
	if u.Fragment != "" || u.RawQuery != "" {
		return fmt.Errorf("must not contain a query or fragment")
	}
	return nil
}

func Configure(config ManagerConfig) error {
	return defaultManager.Configure(config)
}

func (m *Manager) Configure(config ManagerConfig) error {
	return m.configure(config, false, "")
}

func ConfigureStartup(config ManagerConfig) error {
	return defaultManager.configureStartup(config, os.Getenv)
}

func (m *Manager) configureStartup(config ManagerConfig, getenv func(string) string) error {
	resolved, managed, envErr := resolveEnvironmentConfig(config, getenv)
	if envErr != nil {
		if configureErr := m.configure(config, managed, envErr.Error()); configureErr != nil {
			return configureErr
		}
		return envErr
	}
	return m.configure(resolved, managed, "")
}

func (m *Manager) configure(config ManagerConfig, envManaged bool, envError string) error {
	source, err := ValidateSource(string(config.Source))
	if err != nil {
		return err
	}
	config.Source = source
	config.URL = strings.TrimRight(strings.TrimSpace(config.URL), "/")
	config.ClusterID = strings.TrimSpace(config.ClusterID)
	if err := ValidateKubecostURL(config.URL); err != nil {
		return err
	}
	m.mu.Lock()
	m.config = config
	m.selected = ""
	m.client = nil
	m.address = ""
	m.clusterID = ""
	m.retryAt = time.Time{}
	m.selectionErr = nil
	m.generation++
	m.envManaged = envManaged
	m.envError = envError
	m.mu.Unlock()
	portforward.Stop(portforward.OwnerCost)
	return nil
}

func resolveEnvironmentConfig(base ManagerConfig, getenv func(string) string) (ManagerConfig, bool, error) {
	const (
		envSource    = "RADAR_COST_SOURCE"
		envURL       = "RADAR_KUBECOST_URL"
		envAPIKey    = "RADAR_KUBECOST_API_KEY"
		envClusterID = "RADAR_KUBECOST_CLUSTER_ID"
	)
	values := map[string]string{
		envSource:    strings.TrimSpace(getenv(envSource)),
		envURL:       strings.TrimSpace(getenv(envURL)),
		envAPIKey:    strings.TrimSpace(getenv(envAPIKey)),
		envClusterID: strings.TrimSpace(getenv(envClusterID)),
	}
	managed := false
	for _, value := range values {
		managed = managed || value != ""
	}
	if !managed {
		return base, false, nil
	}
	if values[envSource] != "" {
		source, err := ValidateSource(values[envSource])
		if err != nil {
			return base, true, fmt.Errorf("invalid %s: %w", envSource, err)
		}
		base.Source = source
	}
	if values[envURL] != "" {
		if err := ValidateKubecostURL(values[envURL]); err != nil {
			return base, true, fmt.Errorf("invalid %s: %w", envURL, err)
		}
		base.URL = values[envURL]
	}
	if values[envAPIKey] != "" {
		base.APIKey = values[envAPIKey]
	}
	if values[envClusterID] != "" {
		base.ClusterID = values[envClusterID]
	}
	return base, true, nil
}

func ConfigSnapshot() ManagerConfig { return defaultManager.ConfigSnapshot() }

func (m *Manager) ConfigSnapshot() ManagerConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config
}

func IsEnvManaged() bool { return defaultManager.IsEnvManaged() }

func (m *Manager) IsEnvManaged() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.envManaged
}

func EnvManagedError() string { return defaultManager.EnvManagedError() }

func (m *Manager) EnvManagedError() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.envError
}

func SelectedSourceSnapshot() Source { return defaultManager.SelectedSourceSnapshot() }

func (m *Manager) SelectedSourceSnapshot() Source {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.selected
}

func Reset() { defaultManager.Reset() }

func (m *Manager) Reset() {
	m.mu.Lock()
	m.selected = ""
	m.client = nil
	m.address = ""
	m.clusterID = ""
	m.retryAt = time.Time{}
	m.selectionErr = nil
	m.generation++
	m.mu.Unlock()
	portforward.Stop(portforward.OwnerCost)
}

func Selected(ctx context.Context) (Connection, error) { return defaultManager.Selected(ctx) }

func (m *Manager) Selected(ctx context.Context) (Connection, error) {
	m.mu.RLock()
	if m.envError != "" {
		err := m.envError
		m.mu.RUnlock()
		return Connection{}, fmt.Errorf("cost source environment configuration is invalid: %s", err)
	}
	if connection, err, ok := m.cachedSelectionLocked(time.Now()); ok {
		m.mu.RUnlock()
		return connection, err
	}
	m.mu.RUnlock()

	m.selectMu.Lock()
	defer m.selectMu.Unlock()
	m.mu.RLock()
	if m.envError != "" {
		err := m.envError
		m.mu.RUnlock()
		return Connection{}, fmt.Errorf("cost source environment configuration is invalid: %s", err)
	}
	if connection, err, ok := m.cachedSelectionLocked(time.Now()); ok {
		m.mu.RUnlock()
		return connection, err
	}
	config := m.config
	generation := m.generation
	m.mu.RUnlock()

	if config.Source == SourcePrometheus {
		return m.commitSelection(generation, Connection{Source: SourcePrometheus})
	}
	if config.Source == SourceAuto {
		prometheusState := detectPrometheusCostState(ctx)
		if err := ctx.Err(); err != nil {
			return Connection{}, err
		}
		switch prometheusState {
		case prometheusCostAvailable:
			return m.commitSelection(generation, Connection{Source: SourcePrometheus})
		case prometheusCostUnknown:
			return m.commitAutoFallback(generation)
		}
	}

	connection, err := m.connectKubecost(ctx, config)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return Connection{}, contextErr
		}
		if config.Source == SourceAuto {
			return m.commitAutoFallback(generation)
		}
		return m.commitSelectionFailure(generation, err)
	}
	return m.commitSelection(generation, connection)
}

func ProbeKubecost(ctx context.Context, config ManagerConfig) (Connection, error) {
	source, err := ValidateSource(string(config.Source))
	if err != nil {
		return Connection{}, err
	}
	config.Source = source
	config.URL = strings.TrimRight(strings.TrimSpace(config.URL), "/")
	config.ClusterID = strings.TrimSpace(config.ClusterID)
	if err := ValidateKubecostURL(config.URL); err != nil {
		return Connection{}, err
	}
	return defaultManager.connectKubecost(ctx, config)
}

func (m *Manager) commitSelection(generation uint64, connection Connection) (Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation != generation {
		return Connection{}, fmt.Errorf("cost source selection was superseded")
	}
	m.selected = connection.Source
	m.client = connection.Client
	m.address = connection.Address
	m.clusterID = connection.ClusterID
	m.retryAt = time.Time{}
	m.selectionErr = nil
	return connection, nil
}

func (m *Manager) commitAutoFallback(generation uint64) (Connection, error) {
	connection := Connection{Source: SourcePrometheus}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation != generation {
		return Connection{}, fmt.Errorf("cost source selection was superseded")
	}
	m.selected = connection.Source
	m.client = nil
	m.address = ""
	m.clusterID = ""
	m.retryAt = time.Now().Add(autoRetryDelay)
	m.selectionErr = nil
	return connection, nil
}

func (m *Manager) commitSelectionFailure(generation uint64, selectionErr error) (Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation != generation {
		return Connection{}, fmt.Errorf("cost source selection was superseded")
	}
	m.selected = ""
	m.client = nil
	m.address = ""
	m.clusterID = ""
	m.retryAt = time.Now().Add(autoRetryDelay)
	m.selectionErr = selectionErr
	return Connection{}, selectionErr
}

func (m *Manager) cachedSelectionLocked(now time.Time) (Connection, error, bool) {
	if m.selectionErr != nil && now.Before(m.retryAt) {
		return Connection{}, m.selectionErr, true
	}
	if m.selected == "" || m.autoRetryDueLocked(now) {
		return Connection{}, nil, false
	}
	return Connection{Source: m.selected, Client: m.client, Address: m.address, ClusterID: m.clusterID}, nil, true
}

func (m *Manager) autoRetryDueLocked(now time.Time) bool {
	return m.config.Source == SourceAuto && m.selected == SourcePrometheus && !m.retryAt.IsZero() && !now.Before(m.retryAt)
}

func detectPrometheusCostState(ctx context.Context) prometheusCostState {
	client := prometheuspkg.GetClient()
	if client == nil {
		return prometheusCostAbsent
	}
	if _, _, err := client.EnsureConnected(ctx); err != nil {
		if errors.Is(err, prometheuspkg.ErrPrometheusNotFound) {
			return prometheusCostAbsent
		}
		return prometheusCostUnknown
	}
	resp := pkgopencost.ComputeCostSummaryFromProm(ctx, client.Prom(), pkgopencost.SummaryOptions{Currency: pkgopencost.DefaultCurrency})
	if resp.Available {
		return prometheusCostAvailable
	}
	if resp.Reason == pkgopencost.ReasonNoPrometheus || resp.Reason == pkgopencost.ReasonNoMetrics {
		return prometheusCostAbsent
	}
	return prometheusCostUnknown
}

func (m *Manager) connectKubecost(ctx context.Context, config ManagerConfig) (Connection, error) {
	if config.URL != "" {
		clusterID, err := resolveKubecostClusterID(config.ClusterID)
		if err != nil {
			return Connection{}, err
		}
		client, address, err := probeKubecostURL(ctx, config.URL, config.APIKey, clusterID)
		if err != nil {
			return Connection{}, err
		}
		return Connection{Source: SourceKubecost, Client: client, Address: address, ClusterID: clusterID}, nil
	}

	service, port, err := discoverKubecostAggregator()
	if err != nil {
		return Connection{}, err
	}
	clusterID, err := resolveKubecostClusterID(config.ClusterID)
	if err != nil {
		return Connection{}, err
	}
	directURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", service.Name, service.Namespace, port)
	if client, address, directErr := probeKubecostURL(ctx, directURL, config.APIKey, clusterID); directErr == nil {
		return Connection{Source: SourceKubecost, Client: client, Address: address, ClusterID: clusterID}, nil
	}
	forward, err := portforward.Start(portforward.OwnerCost, ctx, service.Namespace, service.Name, port, k8s.GetContextName())
	if err != nil {
		return Connection{}, fmt.Errorf("Kubecost Aggregator port-forward failed: %w", err)
	}
	client, address, err := probeKubecostURL(ctx, forward.Address, config.APIKey, clusterID)
	if err != nil {
		portforward.Stop(portforward.OwnerCost)
		return Connection{}, err
	}
	return Connection{Source: SourceKubecost, Client: client, Address: address, ClusterID: clusterID}, nil
}

func resolveKubecostClusterID(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	return detectKubecostClusterID()
}

func probeKubecostURL(ctx context.Context, rawURL, apiKey, clusterID string) (*pkgopencost.KubecostClient, string, error) {
	if err := ValidateKubecostURL(rawURL); err != nil {
		return nil, "", err
	}
	parsed, _ := url.Parse(rawURL)
	paths := []string{strings.TrimRight(parsed.EscapedPath(), "/")}
	if paths[0] == "" {
		paths = append(paths, "/model")
	}
	origin := parsed.Scheme + "://" + parsed.Host
	noData := false
	var lastErr error
	for _, basePath := range paths {
		httpClient := &http.Client{
			Timeout: 12 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 0 && !sameOrigin(via[0].URL, req.URL) {
					return fmt.Errorf("cross-origin redirect refused")
				}
				return nil
			},
		}
		transport := prom.NewHTTPTransport(origin, basePath, httpClient)
		if apiKey != "" {
			transport.Headers = map[string]string{"X-API-KEY": apiKey}
		}
		client := pkgopencost.NewKubecostClient(transport)
		resp, err := client.GetAllocation(ctx, pkgopencost.KubecostAllocationOptions{
			Window:     "1d",
			Aggregate:  "cluster",
			Accumulate: "true",
			Filter:     kubecostClusterFilter(clusterID),
		})
		if err == nil {
			if kubecostProbeHasClusterData(resp, clusterID) {
				return client, transport.Address(), nil
			}
			noData = true
			continue
		}
		var httpErr *prom.HTTPError
		if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) {
			return nil, "", fmt.Errorf("Kubecost authentication failed")
		}
		lastErr = err
	}
	if noData {
		return nil, "", fmt.Errorf("Kubecost returned no allocation data for cluster %q", clusterID)
	}
	if lastErr != nil {
		return nil, "", fmt.Errorf("Kubecost Aggregator is unreachable or did not return its allocation API: %w", lastErr)
	}
	return nil, "", fmt.Errorf("Kubecost Aggregator is unreachable or did not return its allocation API")
}

func kubecostProbeHasClusterData(resp *pkgopencost.KubecostAllocationResponse, clusterID string) bool {
	if resp == nil {
		return false
	}
	for _, window := range resp.Data {
		for _, allocation := range window {
			if allocation == nil {
				continue
			}
			cluster, _ := allocation.Properties["cluster"].(string)
			if cluster == clusterID {
				return true
			}
		}
	}
	return false
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func kubecostClusterFilter(clusterID string) string {
	if clusterID == "" {
		return ""
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(clusterID)
	return `cluster:"` + escaped + `"`
}

func discoverKubecostAggregator() (*corev1.Service, int, error) {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Services() == nil || cache.StatefulSets() == nil {
		return nil, 0, fmt.Errorf("Kubecost auto-discovery requires access to Services and StatefulSets; configure the Aggregator URL manually")
	}
	services, err := cache.Services().List(labels.Everything())
	if err != nil {
		return nil, 0, fmt.Errorf("list Services for Kubecost discovery: %w", err)
	}
	statefulSets, err := cache.StatefulSets().List(labels.Everything())
	if err != nil {
		return nil, 0, fmt.Errorf("list StatefulSets for Kubecost discovery: %w", err)
	}
	for _, service := range services {
		port := aggregatorServicePort(service)
		if port == 0 {
			continue
		}
		for _, statefulSet := range statefulSets {
			if statefulSet.Namespace != service.Namespace || !activeKubecostAggregator(statefulSet) {
				continue
			}
			selector := labels.SelectorFromSet(service.Spec.Selector)
			if len(service.Spec.Selector) > 0 && selector.Matches(labels.Set(statefulSet.Spec.Template.Labels)) {
				return service, port, nil
			}
		}
	}
	return nil, 0, fmt.Errorf("no active Kubecost 3 Aggregator Service found; configure the central Aggregator URL manually")
}

func aggregatorServicePort(service *corev1.Service) int {
	if service == nil || (service.Labels["app.kubernetes.io/name"] != "aggregator" && service.Labels["app"] != "aggregator") {
		return 0
	}
	for _, port := range service.Spec.Ports {
		if port.Name == "tcp-api" && port.Port == 9004 {
			return int(port.Port)
		}
	}
	return 0
}

func activeKubecostAggregator(statefulSet *appsv1.StatefulSet) bool {
	return statefulSet != nil && statefulSet.Status.ReadyReplicas > 0 && (statefulSet.Labels["app.kubernetes.io/name"] == "aggregator" || statefulSet.Labels["app"] == "aggregator")
}

func detectKubecostClusterID() (string, error) {
	cache := k8s.GetResourceCache()
	if cache == nil || (cache.Deployments() == nil && cache.StatefulSets() == nil) {
		return "", fmt.Errorf("Kubecost cluster ID could not be detected; configure it manually")
	}
	ids := map[string]struct{}{}
	if cache.Deployments() != nil {
		deployments, err := cache.Deployments().List(labels.Everything())
		if err != nil {
			return "", fmt.Errorf("list Deployments for Kubecost cluster ID: %w", err)
		}
		for _, deployment := range deployments {
			if deployment.Status.ReadyReplicas > 0 && deployment.Labels["app.kubernetes.io/name"] == "finopsagent" {
				collectLiteralEnv(ids, deployment.Spec.Template.Spec.Containers, "CLUSTER_ID")
			}
		}
	}
	if cache.StatefulSets() != nil {
		statefulSets, err := cache.StatefulSets().List(labels.Everything())
		if err != nil {
			return "", fmt.Errorf("list StatefulSets for Kubecost cluster ID: %w", err)
		}
		for _, statefulSet := range statefulSets {
			if activeKubecostAggregator(statefulSet) {
				collectLiteralEnv(ids, statefulSet.Spec.Template.Spec.Containers, "CLUSTER_ID")
			}
		}
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("Kubecost cluster ID detection found %d distinct literal values; configure it manually", len(ids))
	}
	for id := range ids {
		return id, nil
	}
	return "", fmt.Errorf("Kubecost cluster ID could not be detected; configure it manually")
}

func collectLiteralEnv(values map[string]struct{}, containers []corev1.Container, name string) {
	for _, container := range containers {
		for _, env := range container.Env {
			if env.Name == name && env.Value != "" && env.ValueFrom == nil {
				values[env.Value] = struct{}{}
			}
		}
	}
}
