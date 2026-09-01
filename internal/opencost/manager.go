package opencost

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/k8score"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type Source string

const (
	SourceAuto                     Source = "auto"
	SourcePrometheus               Source = "prometheus"
	SourceKubecost                 Source = "kubecost"
	autoRetryDelay                        = time.Minute
	noCostSourceRetryDelay                = 5 * time.Second
	prometheusCostDetectionTimeout        = 8 * time.Second
	kubecostConnectTimeout                = 48 * time.Second
	kubecostEndpointTimeout               = 23 * time.Second
	kubecostProbeHTTPTimeout              = 12 * time.Second
	kubecostQueryHTTPTimeout              = 30 * time.Second
	kubecostMaxResponseBytes              = 64 << 20
)

var (
	ErrKubecostAuthentication  = errors.New("Kubecost authentication failed")
	ErrKubecostClusterID       = errors.New("Kubecost cluster ID unavailable")
	ErrKubecostContextMismatch = errors.New("Kubecost configuration context mismatch")
	ErrKubecostNoData          = errors.New("Kubecost returned no allocation data")
	ErrKubecostUnavailable     = errors.New("Kubecost Aggregator unavailable")
	ErrKubecostNotFound        = errors.New("Kubecost Aggregator not found")
	ErrKubecostDiscovery       = errors.New("Kubecost auto-discovery unavailable")
	ErrNoCostSource            = errors.New("no usable cost source found")
	ErrCostSourceEnvConfig     = errors.New("cost source environment configuration is invalid")
)

type ManagerConfig struct {
	Source           Source
	URL              string
	APIKey           string
	APIKeyContext    string
	ClusterID        string
	ClusterIDContext string
}

type ServiceReference struct {
	Name      string
	Namespace string
	Port      int
}

type kubecostAggregatorEndpoint struct {
	servicePort            int
	targetPort             int
	bypassesAuthentication bool
}

type kubecostAggregator struct {
	service   *corev1.Service
	endpoints []kubecostAggregatorEndpoint
}

type Connection struct {
	Source         Source
	Client         *pkgopencost.KubecostClient
	Address        string
	DisplayAddress string
	ClusterID      string
	Service        ServiceReference
	lease          *connectionLease
}

type connectionLease struct {
	alive   func() bool
	release func()
}

type prometheusCostState int

const (
	prometheusCostAbsent prometheusCostState = iota
	prometheusCostAvailable
	prometheusCostUnknown
)

type Manager struct {
	mu             sync.RWMutex
	selectMu       sync.Mutex
	config         ManagerConfig
	selected       Source
	client         *pkgopencost.KubecostClient
	address        string
	displayAddress string
	clusterID      string
	service        ServiceReference
	retryAt        time.Time
	selectionErr   error
	lease          *connectionLease
	generation     uint64
	envManaged     bool
	envError       string
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
	config.APIKeyContext = strings.TrimSpace(config.APIKeyContext)
	config.ClusterID = strings.TrimSpace(config.ClusterID)
	config.ClusterIDContext = strings.TrimSpace(config.ClusterIDContext)
	if err := ValidateKubecostURL(config.URL); err != nil {
		return err
	}
	m.mu.Lock()
	m.config = config
	m.selected = ""
	m.client = nil
	m.address = ""
	m.displayAddress = ""
	m.clusterID = ""
	m.service = ServiceReference{}
	m.retryAt = time.Time{}
	m.selectionErr = nil
	m.lease = nil
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
		if values[envAPIKey] == "" {
			base.APIKey = ""
			base.APIKeyContext = ""
		}
	}
	if values[envAPIKey] != "" {
		base.APIKey = values[envAPIKey]
		if base.URL == "" {
			base.APIKeyContext = k8s.GetContextName()
		} else {
			base.APIKeyContext = ""
		}
	}
	if values[envClusterID] != "" {
		base.ClusterID = values[envClusterID]
		base.ClusterIDContext = k8s.GetContextName()
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
	m.displayAddress = ""
	m.clusterID = ""
	m.service = ServiceReference{}
	m.retryAt = time.Time{}
	m.selectionErr = nil
	m.lease = nil
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
		return Connection{}, fmt.Errorf("%w: %s", ErrCostSourceEnvConfig, err)
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
		return Connection{}, fmt.Errorf("%w: %s", ErrCostSourceEnvConfig, err)
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
	provisionalKubecost := false
	if config.Source == SourceAuto {
		detectCtx, cancel := context.WithTimeout(ctx, prometheusCostDetectionTimeout)
		prometheusState := detectPrometheusCostState(detectCtx)
		detectionErr := detectCtx.Err()
		cancel()
		if err := ctx.Err(); err != nil {
			return Connection{}, err
		}
		if prometheusState == prometheusCostAvailable {
			return m.commitSelection(generation, Connection{Source: SourcePrometheus})
		}
		if prometheusState == prometheusCostUnknown {
			if connection, ok := m.renewProvisionalKubecostSelection(generation); ok {
				return connection, nil
			}
		}
		if !shouldAttemptKubecost(prometheusState, detectionErr) {
			return m.commitAutoFallback(generation)
		}
		provisionalKubecost = prometheusState == prometheusCostUnknown
	}

	connection, err := m.connectKubecost(ctx, config)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return Connection{}, contextErr
		}
		if config.Source == SourceAuto && !errors.Is(err, ErrKubecostContextMismatch) && !hasExplicitKubecostConfig(config) {
			if errors.Is(err, ErrKubecostNotFound) || errors.Is(err, ErrKubecostDiscovery) {
				log.Printf("[opencost] Auto mode found no usable cost source: %s", k8s.SanitizeForLog(err.Error()))
				return m.commitSelectionFailure(generation, fmt.Errorf("%w: %v", ErrNoCostSource, err))
			}
			return m.commitSelectionFailure(generation, err)
		}
		return m.commitSelectionFailure(generation, err)
	}
	if provisionalKubecost {
		return m.commitProvisionalSelection(generation, connection)
	}
	return m.commitSelection(generation, connection)
}

func shouldAttemptKubecost(prometheusState prometheusCostState, detectionErr error) bool {
	return prometheusState == prometheusCostAbsent ||
		(prometheusState == prometheusCostUnknown && errors.Is(detectionErr, context.DeadlineExceeded))
}

func hasExplicitKubecostConfig(config ManagerConfig) bool {
	return config.URL != "" || config.APIKey != "" || config.ClusterID != ""
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
	if config.URL == "" {
		return Connection{}, fmt.Errorf("Kubecost probe requires an Aggregator URL")
	}
	return defaultManager.connectKubecost(ctx, config)
}

func (m *Manager) commitSelection(generation uint64, connection Connection) (Connection, error) {
	return m.commitSelectionWithRetry(generation, connection, time.Time{})
}

func (m *Manager) commitProvisionalSelection(generation uint64, connection Connection) (Connection, error) {
	return m.commitSelectionWithRetry(generation, connection, time.Now().Add(autoRetryDelay))
}

func (m *Manager) renewProvisionalKubecostSelection(generation uint64) (Connection, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation != generation || m.selected != SourceKubecost || m.retryAt.IsZero() {
		return Connection{}, false
	}
	now := time.Now()
	m.retryAt = now.Add(autoRetryDelay)
	connection, _, ok := m.cachedSelectionLocked(now)
	return connection, ok
}

func (m *Manager) commitSelectionWithRetry(generation uint64, connection Connection, retryAt time.Time) (Connection, error) {
	m.mu.Lock()
	if m.generation != generation {
		m.mu.Unlock()
		if connection.lease != nil && connection.lease.release != nil {
			connection.lease.release()
		}
		return Connection{}, fmt.Errorf("cost source selection was superseded")
	}
	previousLease := m.lease
	m.selected = connection.Source
	m.client = connection.Client
	m.address = connection.Address
	m.displayAddress = connection.DisplayAddress
	m.clusterID = connection.ClusterID
	m.service = connection.Service
	m.retryAt = retryAt
	m.selectionErr = nil
	m.lease = connection.lease
	m.mu.Unlock()
	if connection.Source != SourceKubecost && previousLease != nil && previousLease.release != nil {
		previousLease.release()
	}
	return connection, nil
}

func (m *Manager) commitAutoFallback(generation uint64) (Connection, error) {
	connection := Connection{Source: SourcePrometheus}
	m.mu.Lock()
	if m.generation != generation {
		m.mu.Unlock()
		return Connection{}, fmt.Errorf("cost source selection was superseded")
	}
	previousLease := m.lease
	m.selected = connection.Source
	m.client = nil
	m.address = ""
	m.displayAddress = ""
	m.clusterID = ""
	m.service = ServiceReference{}
	m.retryAt = time.Now().Add(autoRetryDelay)
	m.selectionErr = nil
	m.lease = nil
	m.mu.Unlock()
	if previousLease != nil && previousLease.release != nil {
		previousLease.release()
	}
	return connection, nil
}

func (m *Manager) commitSelectionFailure(generation uint64, selectionErr error) (Connection, error) {
	m.mu.Lock()
	if m.generation != generation {
		m.mu.Unlock()
		return Connection{}, fmt.Errorf("cost source selection was superseded")
	}
	previousLease := m.lease
	m.selected = ""
	m.client = nil
	m.address = ""
	m.displayAddress = ""
	m.clusterID = ""
	m.service = ServiceReference{}
	retryDelay := autoRetryDelay
	if errors.Is(selectionErr, ErrNoCostSource) {
		retryDelay = noCostSourceRetryDelay
	}
	m.retryAt = time.Now().Add(retryDelay)
	m.selectionErr = selectionErr
	m.lease = nil
	m.mu.Unlock()
	if previousLease != nil && previousLease.release != nil {
		previousLease.release()
	}
	return Connection{}, selectionErr
}

func (m *Manager) cachedSelectionLocked(now time.Time) (Connection, error, bool) {
	if m.selectionErr != nil && now.Before(m.retryAt) {
		return Connection{}, m.selectionErr, true
	}
	if m.selected == "" || m.autoRetryDueLocked(now) {
		return Connection{}, nil, false
	}
	if m.lease != nil && m.lease.alive != nil && !m.lease.alive() {
		return Connection{}, nil, false
	}
	return Connection{
		Source:         m.selected,
		Client:         m.client,
		Address:        m.address,
		DisplayAddress: m.displayAddress,
		ClusterID:      m.clusterID,
		Service:        m.service,
		lease:          m.lease,
	}, nil, true
}

func (m *Manager) autoRetryDueLocked(now time.Time) bool {
	return m.config.Source == SourceAuto && m.selected != "" && !m.retryAt.IsZero() && !now.Before(m.retryAt)
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
	ctx, cancel := context.WithTimeout(ctx, kubecostConnectTimeout)
	defer cancel()
	if err := validateKubecostConfigContext(config, k8s.GetContextName()); err != nil {
		return Connection{}, err
	}
	if config.URL != "" {
		clusterID, err := resolveKubecostClusterID(config.ClusterID)
		if err != nil {
			return Connection{}, err
		}
		client, address, err := probeKubecostURL(ctx, config.URL, config.APIKey, clusterID)
		if err != nil {
			return Connection{}, err
		}
		return Connection{
			Source: SourceKubecost, Client: client, Address: address,
			DisplayAddress: address, ClusterID: clusterID,
		}, nil
	}

	aggregator, err := discoverKubecostAggregator()
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrKubecostUnavailable, err)
	}
	clusterID, err := resolveKubecostClusterID(config.ClusterID)
	if err != nil {
		return Connection{}, err
	}
	return connectDiscoveredKubecost(ctx, config, clusterID, aggregator)
}

func connectDiscoveredKubecost(ctx context.Context, config ManagerConfig, clusterID string, aggregator *kubecostAggregator) (Connection, error) {
	usedAuthenticationBypass := false
	connection, err := connectKubecostAggregatorEndpoints(config.APIKey, aggregator.endpoints, func(endpoint kubecostAggregatorEndpoint) (Connection, error) {
		endpointCtx, cancel := context.WithTimeout(ctx, kubecostEndpointTimeout)
		defer cancel()
		connection, err := connectDiscoveredKubecostEndpoint(endpointCtx, config.APIKey, clusterID, aggregator.service, endpoint)
		if err == nil {
			usedAuthenticationBypass = endpoint.bypassesAuthentication
		}
		return connection, err
	})
	if err == nil && usedAuthenticationBypass {
		log.Printf("[opencost] Kubecost port 9004 requires authentication; using %s/%s port 9008, which Kubecost exposes without SAML/OIDC for internal clients", aggregator.service.Namespace, aggregator.service.Name)
	}
	return connection, err
}

func connectKubecostAggregatorEndpoints(apiKey string, endpoints []kubecostAggregatorEndpoint, connect func(kubecostAggregatorEndpoint) (Connection, error)) (Connection, error) {
	var primaryErr error
	for _, endpoint := range endpoints {
		// A supplied credential is explicit auth intent; never turn its rejection into unauthenticated access.
		if endpoint.bypassesAuthentication && (apiKey != "" || !errors.Is(primaryErr, ErrKubecostAuthentication)) {
			continue
		}
		connection, err := connect(endpoint)
		if err == nil {
			return connection, nil
		}
		if endpoint.bypassesAuthentication {
			return Connection{}, fmt.Errorf("primary endpoint failed: %v; port %d fallback failed: %w", primaryErr, endpoint.servicePort, err)
		}
		primaryErr = err
	}
	if primaryErr == nil {
		return Connection{}, fmt.Errorf("%w: no supported API port", ErrKubecostNotFound)
	}
	return Connection{}, primaryErr
}

func connectDiscoveredKubecostEndpoint(ctx context.Context, apiKey, clusterID string, service *corev1.Service, endpoint kubecostAggregatorEndpoint) (Connection, error) {
	if k8s.IsInCluster() {
		directURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", service.Name, service.Namespace, endpoint.servicePort)
		client, address, err := probeDiscoveredKubecostURL(ctx, directURL, apiKey, clusterID)
		if err != nil {
			return Connection{}, err
		}
		return discoveredKubecostConnection(client, address, clusterID, service, endpoint.servicePort), nil
	}
	forward, err := portforward.Start(portforward.OwnerCost, ctx, service.Namespace, service.Name, endpoint.targetPort, k8s.GetContextName())
	if err != nil {
		return Connection{}, fmt.Errorf("%w: port-forward failed: %w", ErrKubecostUnavailable, err)
	}
	client, address, err := probeDiscoveredKubecostURL(ctx, forward.Address, apiKey, clusterID)
	if err != nil {
		portforward.Stop(portforward.OwnerCost)
		return Connection{}, err
	}
	connection := discoveredKubecostConnection(client, address, clusterID, service, endpoint.servicePort)
	connection.lease = &connectionLease{
		alive: func() bool {
			info := portforward.GetConnectionInfo(portforward.OwnerCost)
			return info.Connected && info.Address == forward.Address
		},
		release: func() { portforward.StopIfAddress(portforward.OwnerCost, forward.Address) },
	}
	return connection, nil
}

func discoveredKubecostConnection(client *pkgopencost.KubecostClient, address, clusterID string, service *corev1.Service, servicePort int) Connection {
	return Connection{
		Source: SourceKubecost, Client: client, Address: address,
		DisplayAddress: fmt.Sprintf("%s.%s:%d", service.Name, service.Namespace, servicePort),
		ClusterID:      clusterID,
		Service: ServiceReference{
			Name:      service.Name,
			Namespace: service.Namespace,
			Port:      servicePort,
		},
	}
}

func validateKubecostConfigContext(config ManagerConfig, currentContext string) error {
	if currentContext == "" {
		return nil
	}
	if config.ClusterID != "" {
		if config.ClusterIDContext == "" {
			return fmt.Errorf("%w: cluster ID has no durable kubeconfig context binding", ErrKubecostContextMismatch)
		}
		if config.ClusterIDContext != currentContext {
			return fmt.Errorf("%w: cluster ID configured for kubeconfig context %q, current context is %q", ErrKubecostContextMismatch, config.ClusterIDContext, currentContext)
		}
	}
	if config.URL == "" && config.APIKey != "" {
		if config.APIKeyContext == "" {
			return fmt.Errorf("%w: local API key has no durable kubeconfig context binding", ErrKubecostContextMismatch)
		}
		if config.APIKeyContext != currentContext {
			return fmt.Errorf("%w: local API key configured for kubeconfig context %q, current context is %q", ErrKubecostContextMismatch, config.APIKeyContext, currentContext)
		}
	}
	return nil
}

func resolveKubecostClusterID(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	clusterID, err := detectKubecostClusterID()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrKubecostClusterID, err)
	}
	return clusterID, nil
}

func probeKubecostURL(ctx context.Context, rawURL, apiKey, clusterID string) (*pkgopencost.KubecostClient, string, error) {
	return probeKubecostURLWithModelFallback(ctx, rawURL, apiKey, clusterID, true)
}

func probeDiscoveredKubecostURL(ctx context.Context, rawURL, apiKey, clusterID string) (*pkgopencost.KubecostClient, string, error) {
	return probeKubecostURLWithModelFallback(ctx, rawURL, apiKey, clusterID, false)
}

func probeKubecostURLWithModelFallback(ctx context.Context, rawURL, apiKey, clusterID string, tryModel bool) (*pkgopencost.KubecostClient, string, error) {
	if err := ValidateKubecostURL(rawURL); err != nil {
		return nil, "", err
	}
	parsed, _ := url.Parse(rawURL)
	paths := []string{strings.TrimRight(parsed.EscapedPath(), "/")}
	if paths[0] == "" && tryModel {
		paths = append(paths, "/model")
	}
	origin := parsed.Scheme + "://" + parsed.Host
	noData := false
	authenticationFailed := false
	var lastErr error
	for _, basePath := range paths {
		transport := newKubecostHTTPTransport(origin, basePath, apiKey, kubecostProbeHTTPTimeout)
		client := pkgopencost.NewKubecostClient(transport)
		resp, err := client.GetAllocation(ctx, pkgopencost.KubecostAllocationOptions{
			Window:     "24h",
			Aggregate:  "cluster",
			Accumulate: "true",
			Filter:     kubecostClusterFilter(clusterID),
		})
		if err == nil {
			if kubecostProbeHasClusterData(resp, clusterID) {
				transport = newKubecostHTTPTransport(origin, basePath, apiKey, kubecostQueryHTTPTimeout)
				return pkgopencost.NewKubecostClient(transport), transport.Address(), nil
			}
			noData = true
			continue
		}
		var httpErr *prom.HTTPError
		if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) {
			authenticationFailed = true
			lastErr = ErrKubecostAuthentication
			continue
		}
		lastErr = err
	}
	if lastErr != nil {
		if authenticationFailed {
			return nil, "", ErrKubecostAuthentication
		}
		if !noData {
			return nil, "", fmt.Errorf("%w or did not return its allocation API: %w", ErrKubecostUnavailable, lastErr)
		}
	}
	if noData {
		return nil, "", fmt.Errorf("%w for cluster %q", ErrKubecostNoData, clusterID)
	}
	return nil, "", fmt.Errorf("%w or did not return its allocation API", ErrKubecostUnavailable)
}

func newKubecostHTTPTransport(origin, basePath, apiKey string, timeout time.Duration) *prom.HTTPTransport {
	httpClient := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && !sameOrigin(via[0].URL, req.URL) {
				return fmt.Errorf("cross-origin redirect refused")
			}
			return nil
		},
	}
	transport := prom.NewHTTPTransport(origin, basePath, httpClient)
	transport.MaxResponseBytes = kubecostMaxResponseBytes
	if apiKey != "" {
		transport.Headers = map[string]string{"X-API-KEY": apiKey}
	}
	return transport
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

func discoverKubecostAggregator() (*kubecostAggregator, error) {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Services() == nil || cache.StatefulSets() == nil {
		return nil, fmt.Errorf("%w: requires access to Services and StatefulSets; configure the Aggregator URL manually", ErrKubecostDiscovery)
	}
	services, err := cache.Services().List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list Services for Kubecost discovery: %w", err)
	}
	statefulSets, err := cache.StatefulSets().List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list StatefulSets for Kubecost discovery: %w", err)
	}
	for _, service := range services {
		for _, statefulSet := range statefulSets {
			if statefulSet.Namespace != service.Namespace || !activeKubecostAggregator(statefulSet) {
				continue
			}
			selector := labels.SelectorFromSet(service.Spec.Selector)
			if len(service.Spec.Selector) > 0 && selector.Matches(labels.Set(statefulSet.Spec.Template.Labels)) {
				endpoints := aggregatorServiceEndpoints(service, statefulSet.Spec.Template.Spec.Containers)
				if len(endpoints) == 0 {
					continue
				}
				return &kubecostAggregator{service: service, endpoints: endpoints}, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: no active Kubecost 3 Aggregator Service found; configure the central Aggregator URL manually", ErrKubecostNotFound)
}

func aggregatorServiceEndpoints(service *corev1.Service, containers []corev1.Container) []kubecostAggregatorEndpoint {
	if service == nil || (service.Labels["app.kubernetes.io/name"] != "aggregator" && service.Labels["app"] != "aggregator") {
		return nil
	}
	var primary *kubecostAggregatorEndpoint
	var authBypass *kubecostAggregatorEndpoint
	for _, port := range service.Spec.Ports {
		targetPort, ok := k8score.ResolveServiceTargetPort(port, containers)
		if !ok {
			continue
		}
		endpoint := &kubecostAggregatorEndpoint{servicePort: int(port.Port), targetPort: targetPort}
		switch {
		case port.Name == "tcp-api" && port.Port == 9004:
			primary = endpoint
		case port.Name == "tcp-api-rbac" && port.Port == 9008:
			endpoint.bypassesAuthentication = true
			authBypass = endpoint
		}
	}
	if primary == nil {
		return nil
	}
	endpoints := []kubecostAggregatorEndpoint{*primary}
	if authBypass != nil {
		endpoints = append(endpoints, *authBypass)
	}
	return endpoints
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
