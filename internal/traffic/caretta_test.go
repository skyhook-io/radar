package traffic

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/internal/portforward/forwardtest"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCarettaRejectsCrossOriginRedirects(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(source.Close)
	c := NewCarettaSource(nil)
	c.prometheusAddr = source.URL
	c.headers = map[string]string{"Authorization": "Bearer secret", "X-API-Key": "api-secret", "X-Scope-OrgID": "tenant"}
	c.mu.Lock()
	connected := c.tryMetricsEndpointLocked(context.Background(), source.URL)
	hasSeries := c.hasSeriesLocked(context.Background(), source.URL, "up")
	c.mu.Unlock()
	if connected || hasSeries {
		t.Fatal("cross-origin redirect was accepted as a valid metrics source")
	}
	if _, err := c.queryPrometheusRaw(context.Background(), "up"); err == nil || !strings.Contains(err.Error(), "cross-origin redirect refused") {
		t.Fatalf("query error = %v, want refused redirect", err)
	}
	if received.Load() != 0 {
		t.Fatal("traffic consumer sent a request to the redirect destination")
	}
}

// Guards the parallel applyHeaders implementation in caretta.go against
// silent drift from internal/prometheus/client.go — a future contributor
// adding a 4th Prometheus call site here won't have a failing test to
// remind them if this one is missing.
func TestCarettaAppliesHeaders(t *testing.T) {
	var gotAuth, gotOrg atomic.Value
	gotAuth.Store("")
	gotOrg.Store("")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotOrg.Store(r.Header.Get("X-Scope-OrgID"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	c := &CarettaSource{
		httpClient:     &http.Client{Timeout: 5 * time.Second},
		prometheusAddr: srv.URL,
		headers: map[string]string{
			"Authorization": "Bearer caretta-token",
			"X-Scope-OrgID": "tenant-42",
		},
	}

	if _, err := c.queryPrometheusRaw(context.Background(), "up"); err != nil {
		t.Fatalf("queryPrometheusRaw failed: %v", err)
	}
	if got := gotAuth.Load().(string); got != "Bearer caretta-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer caretta-token")
	}
	if got := gotOrg.Load().(string); got != "tenant-42" {
		t.Errorf("X-Scope-OrgID = %q, want %q", got, "tenant-42")
	}

	// tryMetricsEndpointLocked must carry the same headers, otherwise
	// discovery would 401 against an auth-protected endpoint and the user
	// would see "no metrics source found". Acquire the write lock first —
	// that's what every production caller does, and the function name's
	// "Locked" suffix is the Go convention saying so. A previous version
	// of applyHeaders took c.mu.RLock and deadlocked here.
	gotAuth.Store("")
	c.mu.Lock()
	ok := c.tryMetricsEndpointLocked(context.Background(), srv.URL)
	c.mu.Unlock()
	if !ok {
		t.Fatal("tryMetricsEndpointLocked returned false for healthy server")
	}
	if got := gotAuth.Load().(string); got != "Bearer caretta-token" {
		t.Errorf("probe Authorization = %q, want %q", got, "Bearer caretta-token")
	}
}

func TestCarettaReleasedBorrowedForward(t *testing.T) {
	cfg := forwardtest.Server(t, forwardtest.ServeTunnel)
	manager := portforward.NewManager(kubernetes.NewForConfigOrDie(cfg), cfg)
	defer manager.Close()
	for _, path := range []string{"flows", "raw"} {
		t.Run(path, func(t *testing.T) {
			owner, err := manager.Start(portforward.OwnerPrometheus, t.Context(), "ns", "svc", 9090, "ctx")
			if err != nil {
				t.Fatal(err)
			}
			cluster := fake.NewSimpleClientset()
			c := NewCarettaSource(cluster)
			c.forwards, c.currentContext, c.isConnected = manager, "ctx", true
			c.forward = manager.GetAddressForService(portforward.OwnerTraffic, "ctx", "ns", "svc")
			c.prometheusAddr = c.forward.Address()
			address := fmt.Sprintf("127.0.0.1:%d", owner.Info().LocalPort)
			owner.Stop()

			var listener net.Listener
			for deadline := time.Now().Add(time.Second); ; {
				listener, err = net.Listen("tcp4", address)
				if err == nil {
					break
				}
				if !errors.Is(err, syscall.EADDRINUSE) || time.Now().After(deadline) {
					t.Fatal(err)
				}
				time.Sleep(time.Millisecond)
			}
			var hits atomic.Int32
			replacement := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
			})}
			defer replacement.Close()
			go replacement.Serve(listener)
			if path == "flows" {
				_, err = c.GetFlows(t.Context(), FlowOptions{})
			} else {
				_, err = c.queryPrometheusRaw(t.Context(), "up")
			}
			if len(cluster.Actions()) == 0 {
				t.Error("dead forward did not trigger backend discovery")
			}
			if hits.Load() != 0 || c.forward != nil || c.prometheusAddr != "" {
				t.Fatalf("replacement hits=%d, cached address=%q, query error=%v", hits.Load(), c.prometheusAddr, err)
			}
		})
	}
}

func TestCarettaFlowReadRetriesFailedReconnect(t *testing.T) {
	var restarting, abortRead atomic.Bool
	readCtx, cancelRead := context.WithCancel(t.Context())
	defer cancelRead()
	var refused atomic.Int32
	cfg := forwardtest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if restarting.Load() && r.Method == http.MethodPost {
			refused.Add(1)
			if abortRead.Load() {
				cancelRead()
			}
			http.Error(w, "pod restarting", http.StatusServiceUnavailable)
			return
		}
		forwardtest.ServeTunnel(w, r)
	})
	manager := portforward.NewManager(kubernetes.NewForConfigOrDie(cfg), cfg)
	defer manager.Close()

	metrics := promStub(t, promBackend{links: true})
	c := sourceWithServices(t, "ns", carettaStoreSvc("ns", "svc"))
	c.forwards, c.isConnected = manager, true
	defer c.Close()
	// The fake API upgrades the tunnel. Serve PromQL responses from the store fixture.
	c.httpClient.Transport = &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, metrics.Listener.Addr().String())
	}}
	defer c.httpClient.CloseIdleConnections()
	info, err := c.Connect(t.Context(), "ctx")
	if err != nil || !info.Connected {
		t.Fatalf("initial connection: %+v, %v", info, err)
	}
	lost := c.forward
	lost.Stop()
	restarting.Store(true)
	failedAt := time.Now()
	response, err := c.GetFlows(t.Context(), FlowOptions{})
	if err != nil || response.Warning == "" || c.ConnectionInfo().Connected || refused.Load() != 1 {
		t.Fatalf("expected a failed reconnect during restart: response=%+v err=%v refused=%d", response, err, refused.Load())
	}

	if c.reconnectAfter.Before(failedAt.Add(5*time.Second)) || c.reconnectDelay != 5*time.Second {
		t.Fatalf("first failed reconnect scheduled for %v with delay %v", c.reconnectAfter, c.reconnectDelay)
	}
	c.reconnectAfter = time.Now().Add(time.Hour)
	if _, err := c.GetFlows(t.Context(), FlowOptions{}); err != nil || refused.Load() != 1 {
		t.Fatalf("read during backoff retried: err=%v refused=%d", err, refused.Load())
	}

	for _, seconds := range []int{10, 20, 40, 60, 60} {
		wantDelay := time.Duration(seconds) * time.Second
		c.reconnectAfter = time.Now().Add(-time.Second)
		if _, err := c.GetFlows(t.Context(), FlowOptions{}); err != nil || c.reconnectDelay != wantDelay {
			t.Fatalf("retry delay=%v want=%v err=%v", c.reconnectDelay, wantDelay, err)
		}
	}
	c.reconnectAfter = time.Now().Add(-time.Second)
	abortRead.Store(true)
	_, _ = c.GetFlows(readCtx, FlowOptions{})
	if readCtx.Err() == nil {
		t.Fatal("reconnect never reached the cancelled read")
	}

	restarting.Store(false)
	response, err = c.GetFlows(t.Context(), FlowOptions{})
	if err != nil || len(response.Flows) != 1 || !c.forward.Alive() || c.forward.SameForward(lost) || !c.reconnectAfter.IsZero() || c.reconnectDelay != 0 {
		t.Fatalf("later flow read did not restore the tunnel: response=%+v err=%v", response, err)
	}
}

func TestCarettaCancelledRevalidationKeepsBinding(t *testing.T) {
	for _, query := range []string{"up", "count(caretta_links_observed)"} {
		t.Run(query, func(t *testing.T) {
			cfg := forwardtest.Server(t, forwardtest.ServeTunnel)
			manager := portforward.NewManager(kubernetes.NewForConfigOrDie(cfg), cfg)
			defer manager.Close()
			forward, err := manager.Start(portforward.OwnerTraffic, t.Context(), "ns", "svc", 9090, "ctx")
			if err != nil {
				t.Fatal(err)
			}

			metrics := promStub(t, promBackend{links: true})
			entered := make(chan struct{})
			var stall atomic.Bool
			stall.Store(true)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("query") == query && stall.CompareAndSwap(true, false) {
					close(entered)
					<-r.Context().Done()
					return
				}
				metrics.Config.Handler.ServeHTTP(w, r)
			}))
			defer backend.Close()

			c := NewCarettaSource(fake.NewSimpleClientset())
			defer c.Close()
			c.currentContext, c.isConnected = "ctx", true
			c.bindLocked(forward.Address(), &metricsServiceInfo{namespace: "ns", name: "svc"}, forward)
			localPort := forward.Info().LocalPort
			c.httpClient.Transport = &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr != fmt.Sprintf("localhost:%d", localPort) {
					return nil, fmt.Errorf("unexpected metrics address %s", addr)
				}
				return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
			}}
			defer c.httpClient.CloseIdleConnections()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, _ = c.Connect(ctx, "ctx")
			}()
			select {
			case <-entered:
			case <-finished:
				t.Fatal("Connect returned before revalidating the binding")
			case <-time.After(5 * time.Second):
				t.Fatal("revalidation did not reach the backend")
			}
			cancel()
			<-finished
			if !forward.Alive() || c.forward != forward || c.prometheusAddr != forward.Address() {
				t.Fatal("cancelled revalidation closed or cleared the live binding")
			}
			if !c.backendVerified || !c.reconnectAfter.IsZero() {
				t.Fatal("cancelled revalidation discarded verification or scheduled a reconnect")
			}
			response, err := c.GetFlows(t.Context(), FlowOptions{})
			if err != nil || len(response.Flows) != 1 || c.forward != forward {
				t.Fatalf("later flow read did not reuse the binding: response=%+v err=%v", response, err)
			}
		})
	}
}
