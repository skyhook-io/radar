package opencost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	if _, selectedErr := m.Selected(context.Background()); selectedErr == nil {
		t.Fatal("Selected must fail while the environment configuration is invalid")
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
	if connection.Source != SourceKubecost || connection.Address != server.URL+"/model" || connection.ClusterID != "prod-a" {
		t.Fatalf("unexpected connection: %#v", connection)
	}
	if gotPath != "/model/allocation" || gotAPIKey != "secret" {
		t.Fatalf("request path=%q apiKey=%q", gotPath, gotAPIKey)
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

func TestSupersededKubecostSelectionStopsItsPortForward(t *testing.T) {
	stops := 0
	m := &Manager{
		generation:  2,
		stopForward: func() { stops++ },
	}
	if _, err := m.commitSelection(1, Connection{Source: SourceKubecost}); err == nil {
		t.Fatal("superseded selection unexpectedly committed")
	}
	if stops != 1 {
		t.Fatalf("port-forward stops = %d, want 1", stops)
	}

	if _, err := m.commitSelection(1, Connection{Source: SourcePrometheus}); err == nil {
		t.Fatal("superseded Prometheus selection unexpectedly committed")
	}
	if stops != 1 {
		t.Fatalf("Prometheus supersession stopped cost port-forward: stops=%d", stops)
	}
}

func TestKubecostAggregatorDiscoverySignals(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "aggregator"}},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "tcp-api", Port: 9004}}},
	}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "aggregator"}},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	if aggregatorServicePort(service) != 9004 || !activeKubecostAggregator(statefulSet) {
		t.Fatal("official Kubecost 3 Aggregator signals were not recognized")
	}
	service.Spec.Ports[0].Port = 9008
	if aggregatorServicePort(service) != 0 {
		t.Fatal("port 9008 must not be auto-selected")
	}
}
