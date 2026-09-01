package opencost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

func TestResolveEnvironmentConfig(t *testing.T) {
	values := map[string]string{
		"RADAR_COST_SOURCE":         "kubecost",
		"RADAR_KUBECOST_URL":        "https://cost.example.com/model/",
		"RADAR_KUBECOST_API_KEY":    "secret",
		"RADAR_KUBECOST_CLUSTER_ID": "prod-a",
	}
	config, managed, err := resolveEnvironmentConfig(ManagerConfig{Source: SourceAuto}, func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if !managed || config.Source != SourceKubecost || config.URL != "https://cost.example.com/model/" || config.APIKey != "secret" || config.ClusterID != "prod-a" {
		t.Fatalf("unexpected environment config: managed=%v config=%#v", managed, config)
	}
	if config.APIKeyContext != "" {
		t.Fatalf("explicit URL API key context = %q, want reusable credential", config.APIKeyContext)
	}
}

func TestResolveEnvironmentConfigBindsLocalAPIKeyToContext(t *testing.T) {
	previousContext := k8s.SetTestContextName("cluster-a")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })
	config, managed, err := resolveEnvironmentConfig(ManagerConfig{Source: SourceAuto}, func(key string) string {
		if key == "RADAR_KUBECOST_API_KEY" {
			return "secret"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if !managed || config.APIKeyContext != "cluster-a" {
		t.Fatalf("managed=%v API key context=%q, want cluster-a", managed, config.APIKeyContext)
	}
}

func TestResolveEnvironmentConfigDoesNotSendStoredKeyToEnvironmentURL(t *testing.T) {
	config, managed, err := resolveEnvironmentConfig(ManagerConfig{
		Source:        SourceAuto,
		APIKey:        "stored-secret",
		APIKeyContext: "cluster-a",
	}, func(key string) string {
		if key == "RADAR_KUBECOST_URL" {
			return "https://cost.example.com/model"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if !managed || config.APIKey != "" || config.APIKeyContext != "" {
		t.Fatalf("managed=%v API key=%q context=%q, want environment URL without inherited credentials", managed, config.APIKey, config.APIKeyContext)
	}
}

func TestConfigureStartupFailsClosedOnInvalidEnvironment(t *testing.T) {
	m := &Manager{}
	err := m.configureStartup(ManagerConfig{Source: SourceAuto}, func(key string) string {
		if key == "RADAR_KUBECOST_URL" {
			return "not-a-url"
		}
		return ""
	})
	if err == nil || !m.IsEnvManaged() || m.EnvManagedError() == "" {
		t.Fatalf("configureStartup error=%v managed=%v envError=%q", err, m.IsEnvManaged(), m.EnvManagedError())
	}
	if _, selectedErr := m.Selected(context.Background()); !errors.Is(selectedErr, ErrCostSourceEnvConfig) {
		t.Fatalf("Selected error = %v, want ErrCostSourceEnvConfig", selectedErr)
	}
}

func TestConfigureStartupRequiresDurableContextBindings(t *testing.T) {
	previousContext := k8s.SetTestContextName("cluster-a")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })
	tests := []struct {
		name   string
		config ManagerConfig
	}{
		{
			name:   "cluster ID",
			config: ManagerConfig{Source: SourceKubecost, URL: "https://cost.example.com/model", ClusterID: "prod-a"},
		},
		{
			name:   "local API key",
			config: ManagerConfig{Source: SourceKubecost, APIKey: "secret"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manager{}
			if err := m.configureStartup(tt.config, func(string) string { return "" }); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Selected(context.Background()); !errors.Is(err, ErrKubecostContextMismatch) {
				t.Fatalf("selection error = %v, want ErrKubecostContextMismatch", err)
			}
		})
	}
}

func TestProbeKubecostUsesModelPathAndAPIKey(t *testing.T) {
	var gotPath, gotAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("X-API-KEY")
		_, _ = w.Write([]byte(`{"code":200,"data":[{"prod-a":{"properties":{"cluster":"prod-a"}}}]}`))
	}))
	defer server.Close()

	connection, err := ProbeKubecost(context.Background(), ManagerConfig{
		Source: SourceKubecost, URL: server.URL + "/model", APIKey: "secret", ClusterID: "prod-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Source != SourceKubecost || connection.Address != server.URL+"/model" || connection.DisplayAddress != server.URL+"/model" || connection.ClusterID != "prod-a" {
		t.Fatalf("unexpected connection: %#v", connection)
	}
	if gotPath != "/model/allocation" || gotAPIKey != "secret" {
		t.Fatalf("request path=%q apiKey=%q", gotPath, gotAPIKey)
	}
}

func TestProbeKubecostTriesModelPathAfterRootAuthenticationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/allocation" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"data":[{"prod-a":{"properties":{"cluster":"prod-a"}}}]}`))
	}))
	defer server.Close()

	connection, err := ProbeKubecost(context.Background(), ManagerConfig{
		Source: SourceKubecost, URL: server.URL, ClusterID: "prod-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Address != server.URL+"/model" {
		t.Fatalf("address = %q, want /model fallback", connection.Address)
	}
}

func TestDiscoveredKubecostProbeDoesNotTryFrontendModelPath(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/model/allocation" {
			_, _ = w.Write([]byte(`{"code":200,"data":[{"prod-a":{"properties":{"cluster":"prod-a"}}}]}`))
			return
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, _, err := probeDiscoveredKubecostURL(context.Background(), server.URL, "", "prod-a")
	if !errors.Is(err, ErrKubecostAuthentication) {
		t.Fatalf("error = %v, want ErrKubecostAuthentication", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the direct Aggregator API probe", requests)
	}
}

func TestKubecostHTTPTransportSeparatesProbeAndQueryTimeouts(t *testing.T) {
	probe := newKubecostHTTPTransport("https://cost.example.com", "/model", "secret", kubecostProbeHTTPTimeout)
	query := newKubecostHTTPTransport("https://cost.example.com", "/model", "secret", kubecostQueryHTTPTimeout)

	if probe.HTTPClient.Timeout != kubecostProbeHTTPTimeout {
		t.Fatalf("probe timeout = %s, want %s", probe.HTTPClient.Timeout, kubecostProbeHTTPTimeout)
	}
	if query.HTTPClient.Timeout != kubecostQueryHTTPTimeout {
		t.Fatalf("query timeout = %s, want %s", query.HTTPClient.Timeout, kubecostQueryHTTPTimeout)
	}
	if probe.MaxResponseBytes != kubecostMaxResponseBytes || query.MaxResponseBytes != kubecostMaxResponseBytes {
		t.Fatalf("response limits = %d, %d, want %d", probe.MaxResponseBytes, query.MaxResponseBytes, kubecostMaxResponseBytes)
	}
	if probe.Headers["X-API-KEY"] != "secret" || query.Headers["X-API-KEY"] != "secret" {
		t.Fatal("API key header missing from probe or query transport")
	}
}

func TestKubecostConnectBudgetFitsEveryProbePath(t *testing.T) {
	if minimum := portforward.EstablishTimeout + kubecostProbeHTTPTimeout; kubecostEndpointTimeout < minimum {
		t.Fatalf("endpoint budget %s cannot fit port-forward and probe budget %s", kubecostEndpointTimeout, minimum)
	}
	if minimum := 2 * kubecostEndpointTimeout; kubecostConnectTimeout < minimum {
		t.Fatalf("connect budget %s cannot fit two endpoint attempts of %s", kubecostConnectTimeout, kubecostEndpointTimeout)
	}
	if maximum := prometheusCostDetectionTimeout + kubecostConnectTimeout; maximum >= time.Minute {
		t.Fatalf("automatic selection budget %s leaves no room below the server timeout", maximum)
	}
}

func TestPrometheusDetectionDeadlineAttemptsKubecost(t *testing.T) {
	tests := []struct {
		name         string
		state        prometheusCostState
		detectionErr error
		want         bool
	}{
		{name: "Prometheus absent", state: prometheusCostAbsent, want: true},
		{name: "detection deadline", state: prometheusCostUnknown, detectionErr: context.DeadlineExceeded, want: true},
		{name: "upstream failure", state: prometheusCostUnknown, want: false},
		{name: "Prometheus available at deadline", state: prometheusCostAvailable, detectionErr: context.DeadlineExceeded, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAttemptKubecost(tt.state, tt.detectionErr); got != tt.want {
				t.Fatalf("shouldAttemptKubecost() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestProbeKubecostReturnsTypedAuthenticationFailureAfterAllPaths(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := ProbeKubecost(context.Background(), ManagerConfig{
		Source: SourceKubecost, URL: server.URL, ClusterID: "prod-a",
	})
	if !errors.Is(err, ErrKubecostAuthentication) {
		t.Fatalf("error = %v, want ErrKubecostAuthentication", err)
	}
}

func TestProbeKubecostAuthenticationOutranksAlternatePathFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/allocation" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	_, err := ProbeKubecost(context.Background(), ManagerConfig{
		Source: SourceKubecost, URL: server.URL, ClusterID: "prod-a",
	})
	if !errors.Is(err, ErrKubecostAuthentication) {
		t.Fatalf("error = %v, want ErrKubecostAuthentication", err)
	}
}

func TestProbeKubecostAuthenticationOutranksEmptyAlternatePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/allocation" {
			_, _ = w.Write([]byte(`{"code":200,"data":[{}]}`))
			return
		}
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := ProbeKubecost(context.Background(), ManagerConfig{
		Source: SourceKubecost, URL: server.URL, ClusterID: "prod-a",
	})
	if !errors.Is(err, ErrKubecostAuthentication) {
		t.Fatalf("error = %v, want ErrKubecostAuthentication", err)
	}
}

func TestProbeKubecostRejectsClusterWithoutAllocationData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":[{"__idle__":{"properties":{"cluster":"__idle__"}}}]}`))
	}))
	defer server.Close()

	_, err := ProbeKubecost(context.Background(), ManagerConfig{
		Source: SourceKubecost, URL: server.URL + "/model", ClusterID: "missing-cluster",
	})
	if err == nil || !strings.Contains(err.Error(), "no allocation data") {
		t.Fatalf("error = %v, want no allocation data", err)
	}
}

func TestProbeKubecostPreservesUpstreamFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "aggregator warming up", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := ProbeKubecost(context.Background(), ManagerConfig{
		Source: SourceKubecost, URL: server.URL + "/model", ClusterID: "prod-a",
	})
	if err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "aggregator warming up") {
		t.Fatalf("error = %v, want preserved upstream diagnostic", err)
	}
}

func TestProbeKubecostRequiresExplicitURL(t *testing.T) {
	_, err := ProbeKubecost(context.Background(), ManagerConfig{Source: SourceKubecost})
	if err == nil || !strings.Contains(err.Error(), "requires an Aggregator URL") {
		t.Fatalf("error = %v, want explicit URL requirement", err)
	}
}

func TestKubecostClusterIDContextMismatchFailsClosed(t *testing.T) {
	previousContext := k8s.SetTestContextName("cluster-a")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })
	m := &Manager{}
	if err := m.Configure(ManagerConfig{Source: SourceKubecost, URL: "https://cost.example.com/model", ClusterID: "prod-a", ClusterIDContext: "cluster-a"}); err != nil {
		t.Fatal(err)
	}
	if got := m.ConfigSnapshot().ClusterIDContext; got != "cluster-a" {
		t.Fatalf("cluster ID context = %q, want cluster-a", got)
	}
	k8s.SetTestContextName("cluster-b")
	if _, err := m.Selected(context.Background()); !errors.Is(err, ErrKubecostContextMismatch) {
		t.Fatalf("selection error = %v, want ErrKubecostContextMismatch", err)
	}
}

func TestKubecostLocalAPIKeyContextMismatchFailsClosed(t *testing.T) {
	previousContext := k8s.SetTestContextName("cluster-a")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })
	m := &Manager{}
	if err := m.Configure(ManagerConfig{Source: SourceKubecost, APIKey: "secret", APIKeyContext: "cluster-a"}); err != nil {
		t.Fatal(err)
	}
	if got := m.ConfigSnapshot().APIKeyContext; got != "cluster-a" {
		t.Fatalf("API key context = %q, want cluster-a", got)
	}
	k8s.SetTestContextName("cluster-b")
	if _, err := m.Selected(context.Background()); !errors.Is(err, ErrKubecostContextMismatch) {
		t.Fatalf("selection error = %v, want ErrKubecostContextMismatch", err)
	}
}

func TestAutoSourceSurfacesKubecostContextMismatch(t *testing.T) {
	previousContext := k8s.SetTestContextName("cluster-a")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })
	promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(promServer.URL)
	t.Cleanup(func() {
		promServer.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	m := &Manager{}
	if err := m.Configure(ManagerConfig{Source: SourceAuto, URL: "https://cost.example.com/model", ClusterID: "prod-a", ClusterIDContext: "cluster-a"}); err != nil {
		t.Fatal(err)
	}
	k8s.SetTestContextName("cluster-b")
	if _, err := m.Selected(context.Background()); !errors.Is(err, ErrKubecostContextMismatch) {
		t.Fatalf("selection error = %v, want ErrKubecostContextMismatch instead of Prometheus fallback", err)
	}
}

func TestAutoSourceSurfacesExplicitKubecostFailureAfterPrometheusIsAbsent(t *testing.T) {
	previousContext := k8s.SetTestContextName("cluster-a")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })
	promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	kubecostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(promServer.URL)
	t.Cleanup(func() {
		promServer.Close()
		kubecostServer.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	m := &Manager{}
	if err := m.Configure(ManagerConfig{
		Source: SourceAuto, URL: kubecostServer.URL, ClusterID: "prod-a", ClusterIDContext: "cluster-a",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Selected(context.Background()); !errors.Is(err, ErrKubecostAuthentication) {
		t.Fatalf("selection error = %v, want ErrKubecostAuthentication", err)
	}
}

func TestAutoPrometheusFallbackExpires(t *testing.T) {
	m := &Manager{config: ManagerConfig{Source: SourceAuto}}
	connection, err := m.commitAutoFallback(0)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Source != SourcePrometheus || m.retryAt.IsZero() {
		t.Fatalf("connection=%#v retryAt=%v, want temporary Prometheus fallback", connection, m.retryAt)
	}
	if m.autoRetryDueLocked(m.retryAt.Add(-time.Nanosecond)) {
		t.Fatal("fallback expired before retry deadline")
	}
	if !m.autoRetryDueLocked(m.retryAt) {
		t.Fatal("fallback did not expire at retry deadline")
	}
}

func TestProvisionalKubecostSelectionExpiresAndReleasesOnSourceChange(t *testing.T) {
	releases := 0
	m := &Manager{config: ManagerConfig{Source: SourceAuto}}
	connection, err := m.commitProvisionalSelection(0, Connection{
		Source: SourceKubecost,
		lease:  &connectionLease{release: func() { releases++ }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Source != SourceKubecost || m.retryAt.IsZero() {
		t.Fatalf("connection=%#v retryAt=%v, want provisional Kubecost", connection, m.retryAt)
	}
	if m.autoRetryDueLocked(m.retryAt.Add(-time.Nanosecond)) {
		t.Fatal("provisional Kubecost expired before retry deadline")
	}
	if !m.autoRetryDueLocked(m.retryAt) {
		t.Fatal("provisional Kubecost did not expire at retry deadline")
	}
	if _, err := m.commitSelection(0, Connection{Source: SourcePrometheus}); err != nil {
		t.Fatal(err)
	}
	if releases != 1 {
		t.Fatalf("Kubecost lease releases = %d, want 1 after Prometheus wins", releases)
	}
}

func TestInconclusivePrometheusRecheckKeepsProvisionalKubecost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(server.URL)
	t.Cleanup(func() {
		server.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	releases := 0
	m := &Manager{
		config:         ManagerConfig{Source: SourceAuto},
		selected:       SourceKubecost,
		address:        "http://localhost:12345",
		displayAddress: "kubecost-aggregator.kubecost:9008",
		service:        ServiceReference{Name: "kubecost-aggregator", Namespace: "kubecost", Port: 9008},
		retryAt:        time.Now().Add(-time.Second),
		lease: &connectionLease{
			alive:   func() bool { return true },
			release: func() { releases++ },
		},
	}
	connection, err := m.Selected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connection.Source != SourceKubecost || connection.Service.Port != 9008 {
		t.Fatalf("connection = %#v, want existing provisional Kubecost port 9008", connection)
	}
	if releases != 0 {
		t.Fatalf("Kubecost lease releases = %d, want 0 after inconclusive Prometheus recheck", releases)
	}
	if !m.retryAt.After(time.Now()) {
		t.Fatalf("retryAt = %v, want renewed future deadline", m.retryAt)
	}
}

func TestAutoPrometheusErrorUsesTemporaryFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(server.URL)
	t.Cleanup(func() {
		server.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	m := &Manager{config: ManagerConfig{Source: SourceAuto}}
	connection, err := m.Selected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connection.Source != SourcePrometheus || m.retryAt.IsZero() {
		t.Fatalf("connection=%#v retryAt=%v, want temporary Prometheus fallback", connection, m.retryAt)
	}
}

func TestAutoSourceFailsWhenPrometheusAndKubecostAreAbsent(t *testing.T) {
	if err := k8s.InitTestResourceCache(fakeclientset.NewSimpleClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(promServer.URL)
	t.Cleanup(func() {
		promServer.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	m := &Manager{config: ManagerConfig{Source: SourceAuto}}
	if _, err := m.Selected(context.Background()); !errors.Is(err, ErrNoCostSource) {
		t.Fatalf("selection error = %v, want ErrNoCostSource", err)
	}
	if m.selected != "" || m.selectionErr == nil || m.retryAt.IsZero() {
		t.Fatalf("selection state = selected %q, error %v, retry %v; want cached failure", m.selected, m.selectionErr, m.retryAt)
	}
	if delay := time.Until(m.retryAt); delay <= 0 || delay > noCostSourceRetryDelay {
		t.Fatalf("no-source retry delay = %v, want at most %v", delay, noCostSourceRetryDelay)
	}
}

func TestAutoSourceRetriesQuicklyBeforeKubecostDiscoveryIsReady(t *testing.T) {
	k8s.ResetResourceCache()
	t.Cleanup(k8s.ResetTestState)
	promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(promServer.URL)
	t.Cleanup(func() {
		promServer.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	m := &Manager{config: ManagerConfig{Source: SourceAuto}}
	if _, err := m.Selected(context.Background()); !errors.Is(err, ErrNoCostSource) {
		t.Fatalf("selection error = %v, want ErrNoCostSource", err)
	}
	if delay := time.Until(m.retryAt); delay <= 0 || delay > noCostSourceRetryDelay {
		t.Fatalf("discovery retry delay = %v, want at most %v", delay, noCostSourceRetryDelay)
	}
}

func TestExplicitPrometheusRemainsASelectablePreference(t *testing.T) {
	m := &Manager{config: ManagerConfig{Source: SourcePrometheus}}
	connection, err := m.Selected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connection.Source != SourcePrometheus {
		t.Fatalf("source = %q, want prometheus", connection.Source)
	}
}

func TestExplicitKubecostFailureIsTemporarilyCached(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	m := &Manager{config: ManagerConfig{Source: SourceKubecost, URL: server.URL, ClusterID: "cluster-a"}}
	if _, err := m.Selected(context.Background()); err == nil {
		t.Fatal("first selection unexpectedly succeeded")
	}
	firstRequests := requests
	if _, err := m.Selected(context.Background()); err == nil {
		t.Fatal("cached selection unexpectedly succeeded")
	}
	if requests != firstRequests {
		t.Fatalf("requests = %d after cached failure, want %d", requests, firstRequests)
	}
}

func TestCanceledKubecostSelectionIsNotCached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":[{"cluster-a":{"properties":{"cluster":"cluster-a"}}}]}`))
	}))
	defer server.Close()

	m := &Manager{config: ManagerConfig{Source: SourceKubecost, URL: server.URL, ClusterID: "cluster-a"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Selected(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("selection error = %v, want context canceled", err)
	}
	if !m.retryAt.IsZero() || m.selectionErr != nil || m.selected != "" {
		t.Fatalf("canceled selection was cached: selected=%q retryAt=%v err=%v", m.selected, m.retryAt, m.selectionErr)
	}
	connection, err := m.Selected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connection.Source != SourceKubecost {
		t.Fatalf("source = %q, want kubecost", connection.Source)
	}
}

func TestSupersededSelectionReleasesOnlyItsOwnedConnection(t *testing.T) {
	stops := 0
	m := &Manager{generation: 2}
	owned := Connection{
		Source: SourceKubecost,
		lease:  &connectionLease{release: func() { stops++ }},
	}
	if _, err := m.commitSelection(1, owned); err == nil {
		t.Fatal("superseded selection unexpectedly committed")
	}
	if stops != 1 {
		t.Fatalf("connection releases = %d, want 1", stops)
	}

	if _, err := m.commitSelection(1, Connection{Source: SourceKubecost}); err == nil {
		t.Fatal("superseded direct Kubecost selection unexpectedly committed")
	}
	if stops != 1 {
		t.Fatalf("direct Kubecost supersession released another connection: releases=%d", stops)
	}
}

func TestDeadConnectionLeaseInvalidatesCachedSelection(t *testing.T) {
	alive := true
	m := &Manager{
		config:         ManagerConfig{Source: SourceKubecost},
		selected:       SourceKubecost,
		address:        "http://localhost:12345/model",
		displayAddress: "kubecost-aggregator.kubecost:9004",
		service:        ServiceReference{Name: "kubecost-aggregator", Namespace: "kubecost", Port: 9004},
		lease:          &connectionLease{alive: func() bool { return alive }},
	}
	connection, _, ok := m.cachedSelectionLocked(time.Now())
	if !ok {
		t.Fatal("live port-forward-backed selection was not cached")
	}
	if connection.DisplayAddress != "kubecost-aggregator.kubecost:9004" {
		t.Fatalf("display address = %q, want stable Service address", connection.DisplayAddress)
	}
	if connection.Service != (ServiceReference{Name: "kubecost-aggregator", Namespace: "kubecost", Port: 9004}) {
		t.Fatalf("cached Service reference = %#v", connection.Service)
	}
	alive = false
	if _, _, ok := m.cachedSelectionLocked(time.Now()); ok {
		t.Fatal("dead port-forward-backed selection remained cached")
	}
}

func TestResetClearsDisplayAddress(t *testing.T) {
	m := &Manager{
		selected:       SourceKubecost,
		address:        "http://localhost:12345/model",
		displayAddress: "kubecost-aggregator.kubecost:9004",
		service:        ServiceReference{Name: "kubecost-aggregator", Namespace: "kubecost", Port: 9004},
	}

	m.Reset()

	if m.address != "" || m.displayAddress != "" {
		t.Fatalf("addresses after reset = transport %q, display %q", m.address, m.displayAddress)
	}
	if m.service != (ServiceReference{}) {
		t.Fatalf("Service reference after reset = %#v", m.service)
	}
}

func TestKubecostAggregatorDiscoverySignals(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "aggregator"}},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Name: "tcp-api", Port: 9004, TargetPort: intstr.FromString("api")},
			{Name: "tcp-api-rbac", Port: 9008, TargetPort: intstr.FromInt32(9008)},
		}},
	}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "aggregator"}},
		Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Ports: []corev1.ContainerPort{{Name: "api", ContainerPort: 19004}},
		}}}}},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	endpoints := aggregatorServiceEndpoints(service, statefulSet.Spec.Template.Spec.Containers)
	if len(endpoints) != 2 || !activeKubecostAggregator(statefulSet) {
		t.Fatal("official Kubecost 3 Aggregator signals were not recognized")
	}
	if endpoints[0] != (kubecostAggregatorEndpoint{servicePort: 9004, targetPort: 19004}) {
		t.Fatalf("primary endpoint = %#v", endpoints[0])
	}
	if endpoints[1] != (kubecostAggregatorEndpoint{servicePort: 9008, targetPort: 9008, bypassesAuthentication: true}) {
		t.Fatalf("auth-bypass endpoint = %#v", endpoints[1])
	}

	service.Spec.Ports = service.Spec.Ports[1:]
	if endpoints := aggregatorServiceEndpoints(service, statefulSet.Spec.Template.Spec.Containers); len(endpoints) != 0 {
		t.Fatalf("9008 without the primary Aggregator API was auto-selected: %#v", endpoints)
	}
}

func TestKubecostAggregatorRejectsAuthPortNearMisses(t *testing.T) {
	containers := []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "api", ContainerPort: 9004}}}}
	primary := corev1.ServicePort{Name: "tcp-api", Port: 9004, TargetPort: intstr.FromString("api")}
	tests := []struct {
		name  string
		ports []corev1.ServicePort
	}{
		{name: "wrong auth port name", ports: []corev1.ServicePort{primary, {Name: "tcp-api", Port: 9008, TargetPort: intstr.FromInt32(9008)}}},
		{name: "wrong auth port number", ports: []corev1.ServicePort{primary, {Name: "tcp-api-rbac", Port: 9009, TargetPort: intstr.FromInt32(9008)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "aggregator"}},
				Spec:       corev1.ServiceSpec{Ports: tt.ports},
			}
			endpoints := aggregatorServiceEndpoints(service, containers)
			if len(endpoints) != 1 || endpoints[0].servicePort != 9004 {
				t.Fatalf("near-miss auth port was accepted: %#v", endpoints)
			}
		})
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "aggregator"}},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Name: "tcp-api-rbac", Port: 9008, TargetPort: intstr.FromInt32(9008)},
			primary,
		}},
	}
	endpoints := aggregatorServiceEndpoints(service, containers)
	if len(endpoints) != 2 || endpoints[0].servicePort != 9004 || endpoints[1].servicePort != 9008 {
		t.Fatalf("Service port ordering changed endpoint priority: %#v", endpoints)
	}
}

func TestKubecostAggregatorAuthenticationBypassPolicy(t *testing.T) {
	endpoints := []kubecostAggregatorEndpoint{
		{servicePort: 9004, targetPort: 9004},
		{servicePort: 9008, targetPort: 9008, bypassesAuthentication: true},
	}
	tests := []struct {
		name       string
		apiKey     string
		primaryErr error
		wantPorts  []int
		wantPort   int
		wantErr    error
		wantAuth   bool
		bypassErr  error
	}{
		{name: "primary succeeds", wantPorts: []int{9004}, wantPort: 9004},
		{name: "authentication falls back without a key", primaryErr: ErrKubecostAuthentication, wantPorts: []int{9004, 9008}, wantPort: 9008},
		{name: "bypass failure keeps fallback authoritative", primaryErr: ErrKubecostAuthentication, bypassErr: ErrKubecostUnavailable, wantPorts: []int{9004, 9008}, wantErr: ErrKubecostUnavailable},
		{name: "bypass no-data remains authoritative", primaryErr: ErrKubecostAuthentication, bypassErr: ErrKubecostNoData, wantPorts: []int{9004, 9008}, wantErr: ErrKubecostNoData},
		{name: "explicit key is never bypassed", apiKey: "secret", primaryErr: ErrKubecostAuthentication, wantPorts: []int{9004}, wantErr: ErrKubecostAuthentication, wantAuth: true},
		{name: "non-authentication failure is not bypassed", primaryErr: ErrKubecostUnavailable, wantPorts: []int{9004}, wantErr: ErrKubecostUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPorts []int
			connection, err := connectKubecostAggregatorEndpoints(tt.apiKey, endpoints, func(endpoint kubecostAggregatorEndpoint) (Connection, error) {
				gotPorts = append(gotPorts, endpoint.servicePort)
				if endpoint.servicePort == 9004 && tt.primaryErr != nil {
					return Connection{}, tt.primaryErr
				}
				if endpoint.servicePort == 9008 && tt.bypassErr != nil {
					return Connection{}, tt.bypassErr
				}
				return Connection{Service: ServiceReference{Port: endpoint.servicePort}}, nil
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if errors.Is(err, ErrKubecostAuthentication) != tt.wantAuth {
				t.Fatalf("authentication error presence = %t, want %t: %v", errors.Is(err, ErrKubecostAuthentication), tt.wantAuth, err)
			}
			if tt.bypassErr != nil && !strings.Contains(err.Error(), ErrKubecostAuthentication.Error()) {
				t.Fatalf("fallback error lost the primary authentication diagnostic: %v", err)
			}
			if !slices.Equal(gotPorts, tt.wantPorts) {
				t.Fatalf("attempted ports = %v, want %v", gotPorts, tt.wantPorts)
			}
			if err == nil && connection.Service.Port != tt.wantPort {
				t.Fatalf("selected port = %d, want %d", connection.Service.Port, tt.wantPort)
			}
		})
	}
}

func TestDiscoveredKubecostConnectionSeparatesTransportFromService(t *testing.T) {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kubecost-aggregator", Namespace: "kubecost"}}
	connection := discoveredKubecostConnection(nil, "http://localhost:12345", "prod-a", service, 9008)

	if connection.Address != "http://localhost:12345" {
		t.Fatalf("transport address = %q, want loopback port-forward", connection.Address)
	}
	if connection.DisplayAddress != "kubecost-aggregator.kubecost:9008" {
		t.Fatalf("display address = %q, want stable Service address", connection.DisplayAddress)
	}
	if connection.Service != (ServiceReference{Name: "kubecost-aggregator", Namespace: "kubecost", Port: 9008}) {
		t.Fatalf("Service reference = %#v", connection.Service)
	}
}
