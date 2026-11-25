package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const testRelayPath = "/api/agent/metrics/openobserve"

type fakeSourceHub struct {
	mu       sync.Mutex
	source   any
	status   int
	polls    atomic.Int32
	requests []*http.Request
	forms    []url.Values
	relay    func(w http.ResponseWriter, r *http.Request)
}

func newFakeSourceHub(t *testing.T) (*fakeSourceHub, *httptest.Server) {
	t.Helper()
	h := &fakeSourceHub{source: map[string]any{"source": "prometheus"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rhc_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == metricsSourcePath:
			h.polls.Add(1)
			h.mu.Lock()
			source, status := h.source, h.status
			h.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				return
			}
			_ = json.NewEncoder(w).Encode(source)
		case strings.HasPrefix(r.URL.Path, testRelayPath+"/"):
			body, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(body))
			h.mu.Lock()
			h.requests = append(h.requests, r)
			h.forms = append(h.forms, form)
			relay := h.relay
			h.mu.Unlock()
			if relay != nil {
				relay(w, r)
				return
			}
			w.Header().Set("X-Radar-Metrics-Source", "openobserve")
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return h, srv
}

func (h *fakeSourceHub) setRelay(relay func(w http.ResponseWriter, r *http.Request)) {
	h.mu.Lock()
	h.relay = relay
	h.mu.Unlock()
}

func (h *fakeSourceHub) set(source any) {
	h.mu.Lock()
	h.source = source
	h.mu.Unlock()
}

func openObserveSource() map[string]any {
	return map[string]any{
		"source":    "openobserve",
		"relayPath": testRelayPath,
		"matchers":  []map[string]string{{"name": "radar_cluster_uid", "value": "uid-1"}},
		"matchedBy": "id",
		"dialect":   "standard",
		"revision":  "r1",
	}
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent"
}

func newTestPoller(t *testing.T, srv *httptest.Server, apply func(MetricsSourceDecision)) *metricsSourcePoller {
	t.Helper()
	p, err := newMetricsSourcePoller(Config{URL: wsURL(srv), Token: "rhc_test", OnMetricsSource: apply})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMetricsSourcePollerSwitchesToRelayAndBack(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	var decisions []MetricsSourceDecision
	p := newTestPoller(t, srv, func(d MetricsSourceDecision) { decisions = append(decisions, d) })
	ctx := t.Context()

	p.poll(ctx)
	p.poll(ctx) // unchanged: not applied again
	if len(decisions) != 1 || decisions[0].Response.Source != MetricsSourcePrometheus || decisions[0].NewTransport != nil {
		t.Fatalf("initial decisions = %+v", decisions)
	}

	hub.set(openObserveSource())
	p.poll(ctx)
	if len(decisions) != 2 {
		t.Fatalf("decisions = %d, want relay applied", len(decisions))
	}
	relay := decisions[1]
	if relay.Response.Source != MetricsSourceOpenObserve || relay.NewTransport == nil || relay.Endpoint != srv.URL+testRelayPath {
		t.Fatalf("relay decision = %+v", relay)
	}
	if strings.Contains(relay.Key, "rhc_test") {
		t.Fatal("relay key carries the token")
	}

	// Metadata change keeps the key; a matcher change replaces it.
	src := openObserveSource()
	src["warning"] = "matched by name"
	hub.set(src)
	p.poll(ctx)
	if len(decisions) != 3 || decisions[2].Key != relay.Key || decisions[2].Response.Warning != "matched by name" {
		t.Fatalf("metadata update = %+v", decisions[len(decisions)-1])
	}
	src["matchers"] = []map[string]string{{"name": "cluster", "value": "oo-b"}}
	hub.set(src)
	p.poll(ctx)
	if len(decisions) != 4 || decisions[3].Key == relay.Key {
		t.Fatal("matcher change did not replace the relay key")
	}
	// Hub moved to another OpenObserve and kept the matchers: a new revision
	// replaces the key, so work against the old store is invalidated.
	src["revision"] = "r2"
	hub.set(src)
	p.poll(ctx)
	if len(decisions) != 5 || decisions[4].Key == decisions[3].Key {
		t.Fatal("a new connection revision did not replace the relay key")
	}

	// A failed check keeps the relay rather than falling back.
	hub.mu.Lock()
	hub.status = http.StatusServiceUnavailable
	hub.mu.Unlock()
	p.poll(ctx)
	if len(decisions) != 5 {
		t.Fatalf("failed source check applied %+v", decisions[len(decisions)-1])
	}

	hub.mu.Lock()
	hub.status = 0
	hub.mu.Unlock()
	hub.set(map[string]any{"source": "prometheus"})
	p.poll(ctx)
	if len(decisions) != 6 || decisions[5].Response.Source != MetricsSourcePrometheus {
		t.Fatalf("revert decision = %+v", decisions[len(decisions)-1])
	}
}

func TestMetricsSourceRejectsUnsafeAnswers(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	var applied int
	p := newTestPoller(t, srv, func(MetricsSourceDecision) { applied++ })
	for name, mutate := range map[string]func(map[string]any){
		"foreign relay path":  func(s map[string]any) { s["relayPath"] = "/api/orgs/1/secrets" },
		"dot segments":        func(s map[string]any) { s["relayPath"] = "/api/agent/metrics/../../orgs" },
		"no matcher":          func(s map[string]any) { delete(s, "matchers") },
		"empty matcher value": func(s map[string]any) { s["matchers"] = []map[string]string{{"name": "cluster", "value": ""}} },
		"unknown source":      func(s map[string]any) { s["source"] = "datadog" },
	} {
		src := openObserveSource()
		mutate(src)
		hub.set(src)
		p.poll(t.Context())
		if applied != 0 {
			t.Fatalf("%s: unsafe source applied", name)
		}
	}
}

func TestMetricsSourceOlderHubMeansPrometheus(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	hub.mu.Lock()
	hub.status = http.StatusNotFound
	hub.mu.Unlock()
	var got []MetricsSourceDecision
	newTestPoller(t, srv, func(d MetricsSourceDecision) { got = append(got, d) }).poll(t.Context())
	if len(got) != 1 || got[0].Response.Source != MetricsSourcePrometheus {
		t.Fatalf("decisions = %+v", got)
	}
}

// Hub says on every answer whether it offers OpenObserve; a Hub that leaves
// it out, or predates the endpoint, does not. A failed check keeps the last
// answer's.
func TestHubOffersOpenObserve(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	p := newTestPoller(t, srv, func(MetricsSourceDecision) {})
	metricsSourcePollers.Lock()
	metricsSourcePollers.set[p] = struct{}{}
	metricsSourcePollers.Unlock()
	t.Cleanup(func() {
		metricsSourcePollers.Lock()
		delete(metricsSourcePollers.set, p)
		metricsSourcePollers.Unlock()
	})
	setStatus := func(status int) {
		hub.mu.Lock()
		hub.status = status
		hub.mu.Unlock()
	}
	ctx := t.Context()

	p.poll(ctx)
	if HubOffersOpenObserve() {
		t.Fatal("answer without the flag: offers OpenObserve")
	}
	hub.set(map[string]any{"source": "prometheus", "openobserve": true})
	p.poll(ctx)
	if !HubOffersOpenObserve() {
		t.Fatal("prometheus answer with the flag: does not offer OpenObserve")
	}
	setStatus(http.StatusServiceUnavailable)
	p.poll(ctx)
	if !HubOffersOpenObserve() {
		t.Fatal("failed check dropped the last answer's flag")
	}
	setStatus(http.StatusNotFound)
	p.poll(ctx)
	if HubOffersOpenObserve() {
		t.Fatal("older Hub: offers OpenObserve")
	}
	setStatus(0)
	src := openObserveSource()
	src["openobserve"] = true
	hub.set(src)
	p.poll(ctx)
	if !HubOffersOpenObserve() {
		t.Fatal("OpenObserve selected: does not offer OpenObserve")
	}
}

func relayTransportFor(t *testing.T, hub *fakeSourceHub, srv *httptest.Server) (prom.Transport, *metricsSourcePoller, MetricsSourceDecision) {
	t.Helper()
	hub.set(openObserveSource())
	var decision MetricsSourceDecision
	p := newTestPoller(t, srv, func(d MetricsSourceDecision) { decision = d })
	p.poll(t.Context())
	if decision.NewTransport == nil {
		t.Fatal("no relay decision")
	}
	return decision.NewTransport(5*time.Second, 0), p, decision
}

func TestHubRelayTransportSendsBearerAndPath(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	tr, _, _ := relayTransportFor(t, hub, srv)
	if tr.Address() != MetricsRelayAddress {
		t.Fatalf("address = %q", tr.Address())
	}
	client := prom.NewClient(tr)
	if _, err := client.Query(t.Context(), `up{job="a"}`); err != nil {
		t.Fatalf("GET query: %v", err)
	}
	if _, err := client.QueryPost(t.Context(), `rate(x[5m])`); err != nil {
		t.Fatalf("POST query: %v", err)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.requests) != 2 {
		t.Fatalf("relay requests = %d", len(hub.requests))
	}
	get, post := hub.requests[0], hub.requests[1]
	if get.Method != http.MethodGet || get.URL.Path != testRelayPath+"/api/v1/query" || get.URL.Query().Get("query") != `up{job="a"}` {
		t.Fatalf("GET request = %s %s", get.Method, get.URL)
	}
	if post.Method != http.MethodPost || post.URL.Path != testRelayPath+"/api/v1/query" || post.URL.RawQuery != "" ||
		post.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || hub.forms[1].Get("query") != `rate(x[5m])` {
		t.Fatalf("POST request = %s %s form=%v", post.Method, post.URL, hub.forms[1])
	}
	for _, r := range hub.requests {
		if r.Header.Get("Authorization") != "Bearer rhc_test" {
			t.Fatal("relay request without the cluster token")
		}
	}
}

func TestHubRelayTransportUnavailable(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	tr, _, decision := relayTransportFor(t, hub, srv)
	hub.setRelay(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"openobserve_unavailable","error":"OpenObserve unavailable: connection refused"}`))
	})
	_, err := tr.Do(t.Context(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}})
	var relayErr *MetricsRelayError
	if !errors.As(err, &relayErr) || !relayErr.Unavailable || err.Error() != "OpenObserve unavailable: connection refused" {
		t.Fatalf("err = %v", err)
	}
	if unavailable, msg := decision.Health(); !unavailable || msg != err.Error() {
		t.Fatalf("health = %v %q", unavailable, msg)
	}

	// An unreachable Hub reads the same way.
	hub.setRelay(nil)
	srv.Close()
	_, err = tr.Do(t.Context(), http.MethodGet, "/api/v1/query", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "OpenObserve unavailable") {
		t.Fatalf("unreachable err = %v", err)
	}
}

func TestHubRelayTransportRecoversHealthOnSuccess(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	tr, _, decision := relayTransportFor(t, hub, srv)
	hub.setRelay(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	if _, err := tr.Do(t.Context(), http.MethodGet, "/api/v1/query", nil); err == nil {
		t.Fatal("expected failure")
	}
	hub.setRelay(nil)
	if _, err := tr.Do(t.Context(), http.MethodGet, "/api/v1/query", nil); err != nil {
		t.Fatal(err)
	}
	if unavailable, msg := decision.Health(); unavailable || msg != "" {
		t.Fatalf("health after success = %v %q", unavailable, msg)
	}
}

func TestHubRelayTransportNotSelectedRequestsPoll(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	tr, p, _ := relayTransportFor(t, hub, srv)
	hub.setRelay(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"not_selected","error":"not selected"}`))
	})
	if _, err := tr.Do(t.Context(), http.MethodGet, "/api/v1/query", nil); err == nil || !strings.HasPrefix(err.Error(), "OpenObserve unavailable") {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-p.nudge:
	default:
		t.Fatal("not_selected did not ask the poller to re-read the source")
	}
}

func TestHubRelayTransportHonorsRetryAfter(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	tr, _, _ := relayTransportFor(t, hub, srv)
	hub.setRelay(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"rate_limited","error":"slow down"}`))
	})
	_, err := tr.Do(t.Context(), http.MethodGet, "/api/v1/query", nil)
	var relayErr *MetricsRelayError
	if !errors.As(err, &relayErr) || relayErr.RetryAfter != time.Minute {
		t.Fatalf("err = %#v", err)
	}
	for range 5 {
		if _, err := tr.Do(t.Context(), http.MethodGet, "/api/v1/query", nil); err == nil || !strings.Contains(err.Error(), "rate limited") {
			t.Fatalf("err during back-off = %v", err)
		}
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.requests) != 1 {
		t.Fatalf("relay received %d requests during back-off, want 1", len(hub.requests))
	}
}

func TestHubRelayTransportBadQueryIsPerQuery(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	tr, _, decision := relayTransportFor(t, hub, srv)
	hub.setRelay(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error"}`))
	})
	_, err := tr.Do(t.Context(), http.MethodGet, "/api/v1/query", nil)
	var httpErr *prom.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, prom.ErrQueryRefused) || err.Error() != "parse error" {
		t.Fatalf("refusal = %v, want ErrQueryRefused with the relay's message", err)
	}
	if unavailable, msg := decision.Health(); unavailable || msg != "" {
		t.Fatalf("a refused query marked the source: %v %q", unavailable, msg)
	}
}

func TestHubRelayTransportRejectsRedirectAndForeignPath(t *testing.T) {
	hub, srv := newFakeSourceHub(t)
	tr, _, _ := relayTransportFor(t, hub, srv)
	if _, err := tr.Do(t.Context(), http.MethodGet, "/../../api/orgs", nil); err == nil {
		t.Fatal("foreign path accepted")
	}
	var leaked atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
	}))
	defer elsewhere.Close()
	hub.setRelay(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, elsewhere.URL, http.StatusFound) })
	if _, err := tr.Do(t.Context(), http.MethodGet, "/api/v1/query", nil); err == nil {
		t.Fatal("redirect followed")
	}
	if leaked.Load() {
		t.Fatal("cluster token sent to a redirect target")
	}
}

func TestRequestMetricsSourceRefreshPollsNow(t *testing.T) {
	prevGap := metricsSourceMinPollGap
	metricsSourceMinPollGap = 0
	t.Cleanup(func() { metricsSourceMinPollGap = prevGap })

	hub, srv := newFakeSourceHub(t)
	p := newTestPoller(t, srv, func(MetricsSourceDecision) {})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitUntil(t, func() bool { return hub.polls.Load() == 1 })
	waitUntil(t, func() bool {
		metricsSourcePollers.Lock()
		defer metricsSourcePollers.Unlock()
		_, ok := metricsSourcePollers.set[p]
		return ok
	})
	RequestMetricsSourceRefresh()
	waitUntil(t, func() bool { return hub.polls.Load() == 2 })
}

func TestCheckMetricsSourceNowWaitsForTheAnswer(t *testing.T) {
	if CheckMetricsSourceNow(t.Context()) {
		t.Fatal("reported a check with no poller running")
	}
	prevGap := metricsSourceMinPollGap
	metricsSourceMinPollGap = 50 * time.Millisecond
	t.Cleanup(func() { metricsSourceMinPollGap = prevGap })

	hub, srv := newFakeSourceHub(t)
	var source atomic.Value
	p := newTestPoller(t, srv, func(d MetricsSourceDecision) { source.Store(d.Response.Source) })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitUntil(t, func() bool { return hub.polls.Load() == 1 })

	hub.set(openObserveSource())
	if !CheckMetricsSourceNow(t.Context()) {
		t.Fatal("check did not finish")
	}
	if got := source.Load(); got != MetricsSourceOpenObserve {
		t.Fatalf("source after check = %v, want openobserve applied before returning", got)
	}
	if n := hub.polls.Load(); n != 2 {
		t.Fatalf("polls = %d, want 2", n)
	}

	expired, stop := context.WithTimeout(t.Context(), time.Millisecond)
	defer stop()
	hub.mu.Lock()
	hub.status = http.StatusServiceUnavailable
	hub.mu.Unlock()
	metricsSourceMinPollGap = time.Second
	if CheckMetricsSourceNow(expired) {
		t.Fatal("reported a finished check after the wait expired")
	}
}

// Nothing but a start, a reconnect, a nudge or a not-selected relay answer
// makes the agent ask again: a quiet cluster costs Hub nothing.
func TestMetricsSourceNotPolledWhileNothingChanges(t *testing.T) {
	prevGap, prevRetry := metricsSourceMinPollGap, metricsSourceRetryMin
	metricsSourceMinPollGap, metricsSourceRetryMin = 0, 10*time.Millisecond
	t.Cleanup(func() { metricsSourceMinPollGap, metricsSourceRetryMin = prevGap, prevRetry })

	hub, srv := newFakeSourceHub(t)
	p := newTestPoller(t, srv, func(MetricsSourceDecision) {})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitUntil(t, func() bool { return hub.polls.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := hub.polls.Load(); n != 1 {
		t.Fatalf("polled %d times with nothing changed, want 1", n)
	}
	p.requestPoll()
	waitUntil(t, func() bool { return hub.polls.Load() == 2 })
}

// A failed check (Hub unreachable at start, say) is retried until one
// succeeds, then the agent goes quiet again.
func TestMetricsSourceRetriedAfterFailedCheck(t *testing.T) {
	prevGap, prevRetry, prevMax := metricsSourceMinPollGap, metricsSourceRetryMin, metricsSourceRetryMax
	metricsSourceMinPollGap, metricsSourceRetryMin, metricsSourceRetryMax = 0, 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		metricsSourceMinPollGap, metricsSourceRetryMin, metricsSourceRetryMax = prevGap, prevRetry, prevMax
	})

	hub, srv := newFakeSourceHub(t)
	hub.mu.Lock()
	hub.status = http.StatusServiceUnavailable
	hub.mu.Unlock()
	var applied atomic.Int32
	p := newTestPoller(t, srv, func(MetricsSourceDecision) { applied.Add(1) })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitUntil(t, func() bool { return hub.polls.Load() >= 3 })
	hub.mu.Lock()
	hub.status = 0
	hub.mu.Unlock()
	waitUntil(t, func() bool { return applied.Load() == 1 })
	settled := hub.polls.Load()
	time.Sleep(100 * time.Millisecond)
	if n := hub.polls.Load(); n != settled {
		t.Fatalf("kept polling after a successful check: %d -> %d", settled, n)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
