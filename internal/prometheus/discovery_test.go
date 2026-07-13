package prometheus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const healthyProbeBody = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`

func healthyServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(healthyProbeBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// deadURL returns the URL of a server that has already been closed, so probes
// against it fail fast with connection-refused.
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

func TestProbeCandidatesConcurrently(t *testing.T) {
	c := &Client{httpClient: &http.Client{Timeout: 5 * time.Second}}

	t.Run("no candidates respond returns -1", func(t *testing.T) {
		cands := []prom.Candidate{
			{ClusterAddr: deadURL(t)},
			{ClusterAddr: deadURL(t)},
		}
		if got := c.probeCandidatesConcurrently(context.Background(), cands); got != -1 {
			t.Fatalf("got %d, want -1", got)
		}
	})

	t.Run("only later candidate responds returns its index", func(t *testing.T) {
		cands := []prom.Candidate{
			{ClusterAddr: deadURL(t)},
			{ClusterAddr: healthyServer(t, 0).URL},
		}
		if got := c.probeCandidatesConcurrently(context.Background(), cands); got != 1 {
			t.Fatalf("got %d, want 1", got)
		}
	})

	t.Run("highest-priority candidate wins even when slower", func(t *testing.T) {
		// Index 0 is a healthy but slow responder; index 1 is healthy and fast.
		// Selection must be by priority order, not response latency.
		cands := []prom.Candidate{
			{ClusterAddr: healthyServer(t, 100*time.Millisecond).URL},
			{ClusterAddr: healthyServer(t, 0).URL},
		}
		if got := c.probeCandidatesConcurrently(context.Background(), cands); got != 0 {
			t.Fatalf("got %d, want 0 (priority order must beat latency)", got)
		}
	})
}

// TestEnsureConnected_CoalescesConcurrentDiscovery verifies that a burst of
// concurrent EnsureConnected callers triggers exactly one discovery. Without
// singleflight each caller would run its own discovery (and, in the real
// system, clobber the shared port-forward). We observe the discovery count via
// probe hits against the manual-URL endpoint, which discovery hits exactly once
// per run.
func TestEnsureConnected_CoalescesConcurrentDiscovery(t *testing.T) {
	var probeHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeHits.Add(1)
		// Widen the in-flight window so all callers pile onto the singleflight
		// leader rather than arriving after it has already connected.
		time.Sleep(40 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(healthyProbeBody))
	}))
	defer srv.Close()

	c := &Client{
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		manualURL:   srv.URL,
		contextName: "ctx-A",
	}

	const n = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	addrs := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all callers together
			addrs[i], _, errs[i] = c.EnsureConnected(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Fatalf("caller %d: unexpected error %v", i, errs[i])
		}
		if want := strings.TrimRight(srv.URL, "/"); addrs[i] != want {
			t.Errorf("caller %d addr=%q, want %q", i, addrs[i], want)
		}
	}
	if got := probeHits.Load(); got != 1 {
		t.Fatalf("discovery ran %d times under %d concurrent callers, want 1 (singleflight should coalesce)", got, n)
	}
}

// TestProbeCandidatesConcurrently_RespectsContextDeadline verifies the pass
// returns at its context deadline (the caller's directProbeBudget) rather than
// blocking on a probe that hangs — the case that matters off-cluster, where a
// stuck cluster-DNS lookup can outlive its own timeout.
func TestProbeCandidatesConcurrently_RespectsContextDeadline(t *testing.T) {
	c := &Client{httpClient: &http.Client{Timeout: 30 * time.Second}}

	blackhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer blackhole.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := c.probeCandidatesConcurrently(ctx, []prom.Candidate{{ClusterAddr: blackhole.URL}})
	elapsed := time.Since(start)

	if got != -1 {
		t.Fatalf("got %d, want -1 (nothing reachable within the budget)", got)
	}
	if elapsed > time.Second {
		t.Fatalf("took %v — should return at the ~300ms deadline, not wait for the blackhole", elapsed)
	}
}

// TestProbeCandidatesConcurrently_ReachableWinsDespiteStuckHigherPriority
// verifies that when the budget expires while a higher-priority probe is stuck,
// a lower-priority candidate that already succeeded is returned rather than
// dropped — the case that matters over a VPN that routes only some Services.
func TestProbeCandidatesConcurrently_ReachableWinsDespiteStuckHigherPriority(t *testing.T) {
	c := &Client{httpClient: &http.Client{Timeout: 30 * time.Second}}

	blackhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer blackhole.Close()

	cands := []prom.Candidate{
		{ClusterAddr: blackhole.URL},           // index 0: higher priority, wedged
		{ClusterAddr: healthyServer(t, 0).URL}, // index 1: reachable
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if got := c.probeCandidatesConcurrently(ctx, cands); got != 1 {
		t.Fatalf("got %d, want 1 (reachable candidate must win when a higher-priority probe is stuck past the budget)", got)
	}
}

// TestProbeCandidatesConcurrently_HighPrioritySuccessReturnsImmediately checks
// that a fast top-priority hit returns without waiting for lower-priority probes
// that have wedged the semaphore — collection runs concurrently with launching.
func TestProbeCandidatesConcurrently_HighPrioritySuccessReturnsImmediately(t *testing.T) {
	c := &Client{httpClient: &http.Client{Timeout: 30 * time.Second}}

	blackhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer blackhole.Close()

	cands := []prom.Candidate{{ClusterAddr: healthyServer(t, 0).URL}}
	for range 7 {
		cands = append(cands, prom.Candidate{ClusterAddr: blackhole.URL})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // long budget
	defer cancel()

	start := time.Now()
	got := c.probeCandidatesConcurrently(ctx, cands)
	elapsed := time.Since(start)

	if got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
	if elapsed > time.Second {
		t.Fatalf("took %v — a fast index-0 hit must not wait on wedged lower-priority probes", elapsed)
	}
}

// TestProbeCandidatesConcurrently_KeepsBufferedSuccessOnBudgetExpiry checks that
// a probe which succeeded just before the deadline — but whose completion is
// still buffered when the budget expires mid-launch — is not discarded into a
// needless port-forward fallback. Looped because the collector's select between
// the buffered completion and ctx.Done is a race; the drain must win either way.
func TestProbeCandidatesConcurrently_KeepsBufferedSuccessOnBudgetExpiry(t *testing.T) {
	c := &Client{httpClient: &http.Client{Timeout: 30 * time.Second}}

	blackhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer blackhole.Close()
	healthy := healthyServer(t, 0)

	// More candidates than maxConcurrentProbes, so the launcher is still filling
	// the semaphore (blocked behind wedged probes) when the budget expires and
	// index 0's success sits buffered on the completion channel.
	cands := []prom.Candidate{{ClusterAddr: healthy.URL}}
	for range 7 {
		cands = append(cands, prom.Candidate{ClusterAddr: blackhole.URL})
	}

	for iter := range 25 {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		got := c.probeCandidatesConcurrently(ctx, cands)
		cancel()
		if got != 0 {
			t.Fatalf("iter %d: got %d, want 0 (a success buffered at budget expiry must not be dropped)", iter, got)
		}
	}
}

// TestDiscover_StaleFlightGenerationDoesNotCommit verifies discover commits
// against the generation its flight was keyed with, not a freshly-read one: a
// flight keyed before a Reset must not adopt the post-Reset generation and
// publish an endpoint, undoing the reset.
func TestDiscover_StaleFlightGenerationDoesNotCommit(t *testing.T) {
	srv := healthyServer(t, 0) // would connect if the generation matched

	c := &Client{
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		manualURL:    srv.URL,
		contextName:  "ctx",
		discoveryGen: 5, // current generation (a Reset already bumped it)
	}

	// Flight was keyed at generation 4 (before the Reset).
	_, _, err := c.discover(context.Background(), 4)
	if !errors.Is(err, errDiscoverySuperseded) {
		t.Fatalf("err = %v, want errDiscoverySuperseded", err)
	}
	if c.baseURL != "" {
		t.Fatalf("stale-generation discovery published baseURL=%q", c.baseURL)
	}
}

// TestEnsureConnected_RetiredClientAborts verifies that a Client retired by
// Reinitialize aborts discovery even when its singleflight goroutine starts
// after the swap — it must not connect (and drive the shared port-forward) for
// a context that has already been replaced.
func TestEnsureConnected_RetiredClientAborts(t *testing.T) {
	srv := healthyServer(t, 0) // would connect if the client weren't retired

	c := &Client{
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		manualURL:   srv.URL,
		contextName: "ctx-retired",
		retired:     true,
	}

	if _, _, err := c.EnsureConnected(context.Background()); err == nil {
		t.Fatal("retired client connected instead of aborting discovery")
	}
}

// TestMarkConnected_DropsStaleGeneration verifies the generation gate: a
// discovery whose configuration was invalidated (Reset / SetManualURL /
// SetHeaders bump discoveryGen) mid-flight must not publish its now-stale
// endpoint over the newer configuration.
func TestMarkConnected_DropsStaleGeneration(t *testing.T) {
	c := &Client{discoveryGen: 5}

	if c.markConnected("http://stale", "", 4) { // started under an older generation
		t.Fatal("markConnected committed a stale-generation result")
	}
	if c.baseURL != "" {
		t.Fatalf("stale-generation result was published: baseURL=%q", c.baseURL)
	}

	if !c.markConnected("http://fresh", "/bp", 5) { // current generation
		t.Fatal("markConnected rejected a current-generation result")
	}
	if c.baseURL != "http://fresh" || c.basePath != "/bp" {
		t.Fatalf("current-generation result not published: baseURL=%q basePath=%q", c.baseURL, c.basePath)
	}

	// A client retired by Reinitialize must not commit, even at the current gen.
	retiredC := &Client{discoveryGen: 5, retired: true}
	if retiredC.markConnected("http://x", "", 5) {
		t.Fatal("retired client committed a result")
	}
	if retiredC.baseURL != "" {
		t.Fatalf("retired client published baseURL=%q", retiredC.baseURL)
	}
}

// TestConnectionLive rejects a stale or retired connection so discoverShared
// can't report a hollow success for a superseded context.
func TestConnectionLive(t *testing.T) {
	live := &Client{baseURL: "http://p"}
	if !live.connectionLive("http://p") {
		t.Fatal("current connection reported not live")
	}
	if live.connectionLive("http://other") {
		t.Fatal("mismatched address reported live (Reset cleared/changed baseURL)")
	}

	retired := &Client{baseURL: "http://p", retired: true}
	if retired.connectionLive("http://p") {
		t.Fatal("retired client reported live (would query the previous cluster)")
	}
}

// TestReset_CancelsInFlightDiscovery verifies that Reset aborts a detached
// in-flight discovery, so a superseded run (e.g. an old context's pre-warm after
// a context switch) stops instead of holding the process discovery gate.
func TestReset_CancelsInFlightDiscovery(t *testing.T) {
	clientMu.Lock()
	saved := globalClient
	clientMu.Unlock()
	t.Cleanup(func() { clientMu.Lock(); globalClient = saved; clientMu.Unlock() })

	canceled := make(chan struct{})
	clientMu.Lock()
	globalClient = &Client{discoveryCancel: func() { close(canceled) }}
	clientMu.Unlock()

	Reset()

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Reset did not cancel the in-flight discovery")
	}
}

// TestEnsureConnected_CanceledCallerReturnsPromptly verifies that a caller whose
// request context is canceled returns immediately, rather than blocking on the
// detached shared discovery until its 60s backstop.
func TestEnsureConnected_CanceledCallerReturnsPromptly(t *testing.T) {
	// A server that blocks the probe until its own (probe) context is canceled,
	// keeping the shared discovery in-flight for the duration of the test.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := &Client{
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		manualURL:   srv.URL,
		contextName: "ctx-cancel",
	}

	// The caller abandons this run, but it continues detached; suppress
	// diagnostics so its eventual (asynchronous) probe failure doesn't record a
	// global errorlog entry that would pollute a later test.
	ctx, cancel := context.WithCancel(withSuppressedDiscoveryDiagnostics(context.Background()))
	done := make(chan error, 1)
	go func() {
		_, _, err := c.EnsureConnected(ctx)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond) // let the flight start and block on the server
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("caller did not return promptly after its context was canceled")
	}

	// Abort the detached run so it doesn't hold the discovery gate into later tests.
	c.mu.Lock()
	dcancel := c.discoveryCancel
	c.mu.Unlock()
	if dcancel != nil {
		dcancel()
	}
}
