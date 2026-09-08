package prometheus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestAvailabilityAbsentWithoutClient(t *testing.T) {
	clientMu.Lock()
	prev := globalClient
	globalClient = nil
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		globalClient = prev
		clientMu.Unlock()
	})

	got := Availability(context.Background())
	if got.State != AvailabilityAbsent || got.Err != nil || got.Address != "" {
		t.Fatalf("Availability() = %+v, want absent with no error", got)
	}
}

func TestAvailabilityConnectedViaManualURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`))
	}))
	defer srv.Close()

	c := &Client{manualURL: srv.URL + "/", httpClient: &http.Client{Timeout: 5 * time.Second}}
	got := c.Availability(context.Background())
	if got.State != AvailabilityConnected || got.Address != srv.URL || got.Err != nil {
		t.Fatalf("Availability() = %+v, want connected to %s", got, srv.URL)
	}
}

func TestAvailabilityConfiguredFailedWhenManualURLUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := &Client{manualURL: url + "/", httpClient: &http.Client{Timeout: 5 * time.Second}}
	got := c.Availability(context.Background())
	if got.State != AvailabilityConfiguredFailed || got.Address != url || got.Err == nil {
		t.Fatalf("Availability() = %+v, want configured_failed at %s with an error", got, url)
	}
}

func TestAvailabilityAbsentWhenDiscoveryFoundNothing(t *testing.T) {
	c := &Client{
		httpClient:      &http.Client{Timeout: 5 * time.Second},
		lastDiscoverErr: ErrPrometheusNotFound,
		lastDiscoverAt:  time.Now(),
	}
	got := c.Availability(context.Background())
	if got.State != AvailabilityAbsent || !errors.Is(got.Err, ErrPrometheusNotFound) {
		t.Fatalf("Availability() = %+v, want absent wrapping ErrPrometheusNotFound", got)
	}
}

func TestAvailabilityAbsentWithoutKubernetesClient(t *testing.T) {
	c := &Client{httpClient: &http.Client{Timeout: 5 * time.Second}}
	got := c.Availability(context.Background())
	if got.State != AvailabilityAbsent || got.Err == nil {
		t.Fatalf("Availability() = %+v, want absent with the discovery error", got)
	}
}

func TestAvailabilityConfiguredFailedWhenCandidateUnreachable(t *testing.T) {
	c := &Client{
		k8sClient:       fake.NewSimpleClientset(),
		httpClient:      &http.Client{Timeout: 5 * time.Second},
		lastDiscoverErr: errors.New("port-forward to monitoring/prometheus failed: connection refused"),
		lastDiscoverAt:  time.Now(),
	}
	got := c.Availability(context.Background())
	if got.State != AvailabilityConfiguredFailed || got.Address != "" || got.Err == nil {
		t.Fatalf("Availability() = %+v, want configured_failed with the discovery error", got)
	}
}

func TestAvailabilityReturnsWhenCallerContextEnds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := &Client{manualURL: srv.URL, httpClient: &http.Client{Timeout: 5 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := c.Availability(ctx)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Availability blocked %s past a 150ms context", elapsed)
	}
	if got.State != AvailabilityConfiguredFailed || got.Address != srv.URL || !errors.Is(got.Err, context.DeadlineExceeded) {
		t.Fatalf("Availability() = %+v, want configured_failed with DeadlineExceeded", got)
	}
	if hits.Load() == 0 {
		t.Fatal("probe never reached the server")
	}
}

func TestAvailabilityConfiguredFailedWhenInClusterCandidateUnreachable(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "prometheus-server"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.50",
			Ports:     []corev1.ServicePort{{Name: "http", Port: 9090}},
		},
	})
	c := &Client{
		httpClient:  &http.Client{Timeout: time.Second},
		k8sClient:   cs,
		contextName: "in-cluster",
		inCluster:   true,
	}

	got := c.Availability(context.Background())
	if got.State != AvailabilityConfiguredFailed || !errors.Is(got.Err, ErrPrometheusNotFound) {
		t.Fatalf("Availability() = %+v, want configured_failed carrying ErrPrometheusNotFound", got)
	}

	// The five-second discovery-error cache must classify the same way.
	cached := c.Availability(context.Background())
	if cached.State != AvailabilityConfiguredFailed || !errors.Is(cached.Err, ErrPrometheusNotFound) {
		t.Fatalf("cached Availability() = %+v, want configured_failed", cached)
	}
}

func TestAvailabilityKeepsDiscoveredEndpointWhenCallerContextEnds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := &Client{
		baseURL:    srv.URL,
		k8sClient:  fake.NewSimpleClientset(),
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	got := c.Availability(ctx)
	if got.State != AvailabilityConfiguredFailed || got.Address != srv.URL || !errors.Is(got.Err, context.DeadlineExceeded) {
		t.Fatalf("Availability() = %+v, want configured_failed at the discovered endpoint with DeadlineExceeded", got)
	}
}

func TestAvailabilityKeepsKnownEndpointWhenCallerExpiresDuringRediscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The cached probe fails outright, so EnsureConnected clears the endpoint
	// and rediscovers; enumeration cancels the caller and then stalls so the
	// caller's own context wins the race against the detached flight.
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		cancel()
		time.Sleep(300 * time.Millisecond)
		return false, nil, nil
	})
	c := &Client{
		baseURL:    srv.URL,
		k8sClient:  cs,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}

	got := c.Availability(ctx)
	if got.State != AvailabilityConfiguredFailed || got.Address != srv.URL || !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("Availability() = %+v, want configured_failed at the endpoint the call started with", got)
	}
}
