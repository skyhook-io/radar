package prometheus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

// releasingServer blocks every probe until release is closed, so a flight can
// be observed mid-run and then allowed to finish with a healthy answer.
func releasingServer(t *testing.T) (*httptest.Server, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(healthyProbeBody))
	}))
	t.Cleanup(srv.Close)
	return srv, release
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestGetStatus_IdleBeforeAnyDiscovery(t *testing.T) {
	c := &Client{httpClient: &http.Client{Timeout: time.Second}}
	st := c.GetStatus()
	if st.Connected || st.Discovering || st.Error != "" {
		t.Fatalf("fresh client status = %+v, want idle with no error", st)
	}
}

func TestGetStatusDoesNotExposeURLCredentials(t *testing.T) {
	const address = "https://operator:private-password@metrics.example/prom?token=private-token#private-fragment"
	c := &Client{baseURL: address}
	if got := c.GetStatus().Address; got != "https://metrics.example/prom" {
		t.Fatalf("unsafe display address: %s", got)
	}
	if c.baseURL != address {
		t.Fatal("display redaction changed the connection URL")
	}
	c.baseURL = ""
	c.lastOutcome = "GET " + address + ": unavailable"
	if got := c.GetStatus().Error; strings.Contains(got, "private-") || !strings.Contains(got, "unavailable") {
		t.Fatalf("unsafe or unhelpful status error: %s", got)
	}
}

func TestGetStatus_DiscoveringFollowsTheFlightNotItsWaiters(t *testing.T) {
	srv, release := releasingServer(t)
	c := &Client{
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		manualURL:   srv.URL,
		contextName: "ctx-status",
	}

	ctx, cancel := context.WithCancel(withSuppressedDiscoveryDiagnostics(context.Background()))
	done := make(chan error, 1)
	go func() {
		_, _, err := c.EnsureConnected(ctx)
		done <- err
	}()

	waitFor(t, "discovering to be reported while the flight runs", func() bool {
		return c.GetStatus().Discovering
	})

	// The only waiter gives up; the detached flight must keep counting as
	// discovering until it actually finishes.
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter err = %v, want context.Canceled", err)
	}
	if st := c.GetStatus(); !st.Discovering || st.Error != "" {
		t.Fatalf("status after waiter left = %+v, want still discovering with no error", st)
	}

	close(release)
	waitFor(t, "flight to connect", func() bool { return c.GetStatus().Connected })
	if st := c.GetStatus(); st.Discovering || st.Error != "" || st.Address != srv.URL {
		t.Fatalf("connected status = %+v, want address %s and nothing else", st, srv.URL)
	}
}

func TestGetStatus_ReportsFailureOutcomeWhenIdle(t *testing.T) {
	c := &Client{
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		k8sClient:   fake.NewClientset(),
		contextName: "ctx-outcome",
	}
	_, _, err := c.EnsureConnected(withSuppressedDiscoveryDiagnostics(context.Background()))
	if !errors.Is(err, ErrPrometheusNotFound) {
		t.Fatalf("err = %v, want ErrPrometheusNotFound", err)
	}
	st := c.GetStatus()
	if st.Discovering || st.Connected {
		t.Fatalf("status = %+v, want idle", st)
	}
	if st.Error != ErrPrometheusNotFound.Error() {
		t.Fatalf("status.Error = %q, want %q", st.Error, ErrPrometheusNotFound.Error())
	}
}

func TestRecordOutcome_TimeoutAndGenerationRules(t *testing.T) {
	c := &Client{discoveryGen: 3}

	c.recordOutcomeLocked(3, context.DeadlineExceeded)
	if c.lastOutcome == "" || c.lastOutcomeGen != 3 {
		t.Fatalf("timeout not recorded: outcome=%q gen=%d", c.lastOutcome, c.lastOutcomeGen)
	}
	if st := c.GetStatus(); st.Error != c.lastOutcome {
		t.Fatalf("status.Error = %q, want the timeout outcome", st.Error)
	}

	// An older flight ending after a configuration change must not describe
	// the new configuration.
	c.recordOutcomeLocked(2, errors.New("stale failure"))
	if c.lastOutcome == "stale failure" {
		t.Fatal("stale generation overwrote the current outcome")
	}

	// Supersession is not an outcome of the configuration.
	c.recordOutcomeLocked(3, context.Canceled)
	if c.lastOutcome == "" {
		t.Fatal("cancellation cleared a recorded outcome")
	}

	c.recordOutcomeLocked(3, nil)
	if c.lastOutcome != "" {
		t.Fatalf("success left outcome %q", c.lastOutcome)
	}
}

func TestEnsureConnected_HeadersWithoutURLNeverDiscovers(t *testing.T) {
	var listed atomic.Bool
	k8sClient := fake.NewClientset()
	k8sClient.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listed.Store(true)
		return false, nil, nil
	})
	c := &Client{
		httpClient:  &http.Client{Timeout: time.Second},
		k8sClient:   k8sClient,
		headers:     map[string]string{"Authorization": "Bearer secret"},
		contextName: "ctx-gate",
	}

	_, _, err := c.EnsureConnected(context.Background())
	if !errors.Is(err, prom.ErrHeadersRequireURL) {
		t.Fatalf("err = %v, want prom.ErrHeadersRequireURL", err)
	}
	if listed.Load() {
		t.Fatal("discovery touched the cluster despite the headers gate")
	}
	st := c.GetStatus()
	if st.Discovering || st.Error != prom.ErrHeadersRequireURL.Error() {
		t.Fatalf("status = %+v, want the headers-require-URL error", st)
	}
}

func TestConfigureLocked_NewCredentialsNeverReachThePreviousEndpoint(t *testing.T) {
	var leaked atomic.Bool
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tenant") != "" {
			leaked.Store(true)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(healthyProbeBody))
	}))
	defer old.Close()
	fresh := healthyServer(t, 0)

	c := &Client{
		httpClient:  &http.Client{Timeout: time.Second},
		baseURL:     old.URL,
		manualURL:   old.URL,
		contextName: "ctx-configure",
	}
	c.mu.Lock()
	c.configureLocked(fresh.URL, map[string]string{"X-Tenant": "acme"})
	c.mu.Unlock()

	addr, _, err := c.EnsureConnected(context.Background())
	if err != nil || addr != fresh.URL {
		t.Fatalf("EnsureConnected = %q, %v; want %q", addr, err, fresh.URL)
	}
	if leaked.Load() {
		t.Fatal("new headers were sent to the previous endpoint")
	}
}

func TestEnsureConnected_StaleCachedProbeCannotTearDownNewerConnection(t *testing.T) {
	release := make(chan struct{})
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer stale.Close()
	fresh := healthyServer(t, 0)

	c := &Client{
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		baseURL:     stale.URL,
		contextName: "ctx-stale-probe",
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = c.EnsureConnected(withSuppressedDiscoveryDiagnostics(context.Background()))
	}()

	// While the stale probe is still blocked, a configuration change lands and
	// discovery under the new generation connects somewhere else. The manual
	// URL is deliberately one the stale caller cannot reconnect through, so a
	// wrongful teardown cannot be masked by an immediate rediscovery.
	waitFor(t, "stale probe to be in flight", func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.prom != nil
	})
	c.mu.Lock()
	c.configureLocked(deadURL(t), nil)
	c.baseURL = fresh.URL
	c.mu.Unlock()

	close(release)
	<-done

	c.mu.RLock()
	got := c.baseURL
	c.mu.RUnlock()
	if got != fresh.URL {
		t.Fatalf("baseURL = %q after the stale probe failed, want the newer connection %q kept", got, fresh.URL)
	}
}
