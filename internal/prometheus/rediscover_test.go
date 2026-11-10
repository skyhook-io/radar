package prometheus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	toolscache "k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
)

// stubRediscovery shortens the worker's delays and replaces each run with a
// recorder. result decides each run's outcome.
func stubRediscovery(t *testing.T, result func(label string) error) <-chan string {
	t.Helper()
	savedDebounce, savedFollowUps := rediscoveryDebounce, rediscoveryFollowUps
	runs := make(chan string, 32)
	rediscoveryDebounce = 20 * time.Millisecond
	rediscoveryFollowUps = []time.Duration{20 * time.Millisecond, 20 * time.Millisecond}
	backgroundConnectForTest = func(_ *Client, label string) error {
		runs <- label
		return result(label)
	}
	t.Cleanup(func() {
		rediscoveryDebounce, rediscoveryFollowUps = savedDebounce, savedFollowUps
		backgroundConnectForTest = nil
	})
	return runs
}

func newRediscoveryClient(t *testing.T) (*Client, kubernetes.Interface) {
	t.Helper()
	cs := fake.NewSimpleClientset()
	return &Client{k8sClient: cs, httpClient: &http.Client{Timeout: 2 * time.Second}}, cs
}

// waitIdle waits for the worker to drain so a test never leaks it into the next.
func waitIdle(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.rediscovery.mu.Lock()
		running := c.rediscovery.running
		c.rediscovery.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("rediscovery worker did not go idle")
}

func collectRuns(runs <-chan string, within time.Duration) []string {
	var out []string
	timeout := time.After(within)
	for {
		select {
		case r := <-runs:
			out = append(out, r)
		case <-timeout:
			return out
		}
	}
}

func promService(ns, name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app.kubernetes.io/name": "prometheus"}},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 9090}},
		},
	}
}

func TestRediscovery_CoalescesBurstIntoOneRun(t *testing.T) {
	runs := stubRediscovery(t, func(string) error { return nil })
	c, _ := newRediscoveryClient(t)

	for range 5 {
		c.requestRediscovery("", "burst")
	}
	got := collectRuns(runs, 300*time.Millisecond)
	waitIdle(t, c)
	if len(got) != 1 {
		t.Fatalf("burst of 5 signals ran %d discoveries, want 1: %v", len(got), got)
	}
}

func TestRediscovery_WaitsForInFlightRun(t *testing.T) {
	runs := stubRediscovery(t, func(string) error { return nil })
	c, _ := newRediscoveryClient(t)
	inFlight := make(chan struct{})
	c.flightDone = inFlight

	c.requestRediscovery("", "service created")
	if got := collectRuns(runs, 150*time.Millisecond); len(got) != 0 {
		t.Fatalf("ran while a discovery was in flight: %v", got)
	}
	c.mu.Lock()
	c.flightDone = nil
	c.mu.Unlock()
	close(inFlight)
	if got := collectRuns(runs, 300*time.Millisecond); len(got) != 1 {
		t.Fatalf("want one run after the in-flight discovery ended, got %v", got)
	}
	waitIdle(t, c)
}

func TestRediscovery_WaitsOutFailedDiscoveryTTL(t *testing.T) {
	runs := stubRediscovery(t, func(string) error { return nil })
	c, _ := newRediscoveryClient(t)
	const remaining = 300 * time.Millisecond
	c.lastDiscoverErr = ErrPrometheusNotFound
	c.lastDiscoverAt = time.Now().Add(-failedDiscoveryCacheTTL + remaining)

	start := time.Now()
	c.requestRediscovery("", "service created")
	select {
	case <-runs:
		if waited := time.Since(start); waited < remaining {
			t.Fatalf("ran after %s, inside the failed-discovery TTL (%s left)", waited, remaining)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no run after the TTL expired")
	}
	waitIdle(t, c)
}

func TestRediscovery_SkipsWhenNoLongerApplicable(t *testing.T) {
	runs := stubRediscovery(t, func(string) error { return nil })

	t.Run("connected meanwhile", func(t *testing.T) {
		c, _ := newRediscoveryClient(t)
		c.requestRediscovery("", "service created")
		c.mu.Lock()
		c.baseURL = "http://localhost:1"
		c.mu.Unlock()
		if got := collectRuns(runs, 150*time.Millisecond); len(got) != 0 {
			t.Fatalf("ran although connected: %v", got)
		}
		waitIdle(t, c)
	})
	t.Run("manual URL", func(t *testing.T) {
		c, _ := newRediscoveryClient(t)
		c.manualURL = "http://prom.example"
		c.requestRediscovery("", "service created")
		if got := collectRuns(runs, 150*time.Millisecond); len(got) != 0 {
			t.Fatalf("cluster signal ran discovery over a manual URL: %v", got)
		}
		waitIdle(t, c)
	})
	t.Run("generation moved", func(t *testing.T) {
		c, _ := newRediscoveryClient(t)
		c.requestRediscovery("", "service created")
		c.mu.Lock()
		c.discoveryGen++
		c.mu.Unlock()
		if got := collectRuns(runs, 150*time.Millisecond); len(got) != 0 {
			t.Fatalf("signal from a superseded configuration ran: %v", got)
		}
		waitIdle(t, c)
	})
	t.Run("verified address already replaced", func(t *testing.T) {
		c, _ := newRediscoveryClient(t)
		c.baseURL = "http://localhost:2"
		c.requestRediscovery("http://localhost:1", "query failed")
		if got := collectRuns(runs, 150*time.Millisecond); len(got) != 0 {
			t.Fatalf("verified a connection that was already replaced: %v", got)
		}
		waitIdle(t, c)
	})
	t.Run("retired", func(t *testing.T) {
		c, _ := newRediscoveryClient(t)
		c.retired = true
		c.requestRediscovery("", "service created")
		if got := collectRuns(runs, 150*time.Millisecond); len(got) != 0 {
			t.Fatalf("retired client ran discovery: %v", got)
		}
	})
}

func TestRediscovery_FollowUpsAfterUnreachableCandidate(t *testing.T) {
	runs := stubRediscovery(t, func(string) error { return errPrometheusUnreachable })
	c, _ := newRediscoveryClient(t)

	c.requestRediscovery("", "pod monitoring/prometheus-0 became ready")
	got := collectRuns(runs, 500*time.Millisecond)
	waitIdle(t, c)
	if want := 1 + len(rediscoveryFollowUps); len(got) != want {
		t.Fatalf("got %d runs, want the signal's run plus %d follow-ups: %v", len(got), len(rediscoveryFollowUps), got)
	}
	if !strings.Contains(got[len(got)-1], "follow-up 2/2 after pod monitoring/prometheus-0 became ready") {
		t.Fatalf("follow-up should name the signal it follows: %q", got[len(got)-1])
	}
}

func TestRediscovery_NoFollowUpWithoutCandidates(t *testing.T) {
	runs := stubRediscovery(t, func(string) error { return ErrPrometheusNotFound })
	c, _ := newRediscoveryClient(t)

	c.requestRediscovery("", "service created")
	got := collectRuns(runs, 300*time.Millisecond)
	waitIdle(t, c)
	if len(got) != 1 {
		t.Fatalf("no candidate exists, so nothing should be retried; got %v", got)
	}
}

func TestObserveResourceChange_ServiceSignals(t *testing.T) {
	runs := stubRediscovery(t, func(string) error { return nil })
	candidate := promService("monitoring", "prometheus")
	redis := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "redis"}}

	for _, tc := range []struct {
		name    string
		setup   func(c *Client)
		source  func(own kubernetes.Interface) kubernetes.Interface
		op      string
		obj     any
		old     any
		wantRun bool
	}{
		{name: "candidate created while disconnected", op: "add", obj: candidate, wantRun: true},
		{name: "non-candidate created", op: "add", obj: redis},
		{name: "candidate created while connected", setup: func(c *Client) { c.baseURL = "http://localhost:1" }, op: "add", obj: candidate},
		{name: "candidate created with manual URL", setup: func(c *Client) { c.manualURL = "http://prom.example" }, op: "add", obj: candidate},
		{name: "event from another cluster's cache", source: func(kubernetes.Interface) kubernetes.Interface { return fake.NewSimpleClientset() }, op: "add", obj: candidate},
		{name: "metadata-only update of a candidate", op: "update", obj: candidate, old: candidate.DeepCopy()},
		{name: "service becomes a candidate", op: "update", obj: candidate, old: &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "prometheus"}}, wantRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, own := newRediscoveryClient(t)
			if tc.setup != nil {
				tc.setup(c)
			}
			source := own
			if tc.source != nil {
				source = tc.source(own)
			}
			c.observeResourceChange(source, tc.op, tc.obj, tc.old)
			got := collectRuns(runs, 150*time.Millisecond)
			waitIdle(t, c)
			if ran := len(got) > 0; ran != tc.wantRun {
				t.Fatalf("ran=%v want %v (%v)", ran, tc.wantRun, got)
			}
		})
	}
}

func readyPod(ns, name string, lbls map[string]string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: lbls},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}},
	}
}

func TestPodBecameReady(t *testing.T) {
	notReady := readyPod("monitoring", "p", nil, false)
	ready := readyPod("monitoring", "p", nil, true)
	deleting := ready.DeepCopy()
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	for _, tc := range []struct {
		name string
		op   string
		pod  *corev1.Pod
		old  any
		want bool
	}{
		{"turns ready", "update", ready, notReady, true},
		{"stays ready", "update", ready, ready, false},
		{"turns unready", "update", notReady, ready, false},
		{"created ready", "add", ready, nil, true},
		{"created unready", "add", notReady, nil, false},
		{"terminating", "update", deleting, notReady, false},
	} {
		if got := podBecameReady(tc.op, tc.pod, tc.old); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestCandidateServiceSelecting(t *testing.T) {
	indexer := toolscache.NewIndexer(toolscache.MetaNamespaceKeyFunc, toolscache.Indexers{toolscache.NamespaceIndex: toolscache.MetaNamespaceIndexFunc})
	prometheusSvc := promService("monitoring", "prometheus")
	grafana := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "grafana"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "grafana"}},
	}
	headless := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "prometheus-operated"}}
	for _, s := range []*corev1.Service{prometheusSvc, grafana, headless} {
		if err := indexer.Add(s); err != nil {
			t.Fatal(err)
		}
	}
	lister := listerscorev1.NewServiceLister(indexer)
	promPod := readyPod("monitoring", "prometheus-0", map[string]string{"app": "prometheus"}, true)
	grafanaPod := readyPod("monitoring", "grafana-0", map[string]string{"app": "grafana"}, true)

	if got := candidateServiceSelecting(lister, promPod, nil); got == nil || got.Name != "prometheus" {
		t.Fatalf("prometheus pod: got %v, want monitoring/prometheus", got)
	}
	if got := candidateServiceSelecting(lister, grafanaPod, nil); got != nil {
		t.Fatalf("grafana pod is not behind a candidate, got %s", got.Name)
	}
	connected := &prom.ServiceInfo{Namespace: "monitoring", Name: "grafana"}
	if got := candidateServiceSelecting(lister, grafanaPod, connected); got == nil || got.Name != "grafana" {
		t.Fatalf("pod behind the connected service should match it, got %v", got)
	}
	if got := candidateServiceSelecting(lister, promPod, connected); got != nil {
		t.Fatalf("while connected only the connected service matters, got %s", got.Name)
	}
	if got := candidateServiceSelecting(nil, promPod, nil); got != nil {
		t.Fatal("no lister (cache of another cluster) must match nothing")
	}
}

func TestFailureReportingTransport(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)

	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	t.Cleanup(hung.Close)

	for _, tc := range []struct {
		name       string
		addr       string
		httpClient *http.Client
		ctx        func() context.Context
		wantReport bool
	}{
		{name: "no response", addr: deadURL(t), ctx: context.Background, wantReport: true},
		{name: "HTTP client timeout", addr: hung.URL, httpClient: &http.Client{Timeout: 50 * time.Millisecond}, ctx: context.Background, wantReport: true},
		{name: "HTTP error status", addr: failing.URL, ctx: context.Background},
		{name: "caller handles failure itself", addr: deadURL(t), ctx: func() context.Context {
			return withoutFailureReport(context.Background())
		}},
		{name: "caller gave up", addr: deadURL(t), ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reported []string
			tr := failureReportingTransport{
				Transport: prom.NewHTTPTransport(tc.addr, "", tc.httpClient),
				addr:      tc.addr,
				report:    func(addr string, _ error) { reported = append(reported, addr) },
			}
			_, err := prom.NewClient(tr).Query(tc.ctx(), "up")
			if err == nil {
				t.Fatal("query should fail")
			}
			if got := len(reported) > 0; got != tc.wantReport {
				t.Fatalf("reported=%v want %v", got, tc.wantReport)
			}
		})
	}
}

func TestQueryFailureOnConnectedEndpointRequestsVerify(t *testing.T) {
	runs := stubRediscovery(t, func(string) error { return nil })
	c, _ := newRediscoveryClient(t)
	dead := deadURL(t)
	c.baseURL = dead

	if _, err := c.Prom().Query(context.Background(), "up"); err == nil {
		t.Fatal("query against a dead endpoint should fail")
	}
	got := collectRuns(runs, 300*time.Millisecond)
	waitIdle(t, c)
	if len(got) != 1 || !strings.Contains(got[0], "query to") {
		t.Fatalf("want one verify run for the failed query, got %v", got)
	}
}

func TestOnForwardLost(t *testing.T) {
	runs := stubRediscovery(t, func(string) error { return errors.New("pod not ready") })
	rediscoveryFollowUps = nil

	c, _ := newRediscoveryClient(t)
	c.baseURL = "http://localhost:4000"
	c.onForwardLost("traffic", "http://localhost:5000")
	if c.baseURL == "" {
		t.Fatal("loss of an unrelated forward dropped the connection")
	}
	if got := collectRuns(runs, 100*time.Millisecond); len(got) != 0 {
		t.Fatalf("unrelated forward loss ran discovery: %v", got)
	}

	c.onForwardLost("prometheus", "http://localhost:4000")
	if c.baseURL != "" {
		t.Fatal("connection served by the lost forward was kept")
	}
	got := collectRuns(runs, 300*time.Millisecond)
	waitIdle(t, c)
	if len(got) != 1 || !strings.Contains(got[0], "port-forward http://localhost:4000 lost") {
		t.Fatalf("want one rediscovery naming the lost forward, got %v", got)
	}
}

var registerSignalsOnce sync.Once

// TestRediscoverySignalsThroughResourceCache drives the signals end to end:
// objects written to the cluster reach the informer cache, its observer and
// the worker, and discovery enumerates from that cache. The Service is older
// than any age cutoff, as after a relist that follows a watch outage, and must
// still count once the initial sync is over.
func TestRediscoverySignalsThroughResourceCache(t *testing.T) {
	runs := stubRediscovery(t, func(string) error { return nil })
	cs := fake.NewClientset()
	if err := k8s.InitScopedTestResourceCache(cs, map[string]k8score.ResourceScope{
		string(k8score.Services): {Enabled: true},
		string(k8score.Pods):     {Enabled: true},
	}); err != nil {
		t.Fatalf("InitScopedTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
	registerSignalsOnce.Do(RegisterRediscoverySignals)
	c := &Client{k8sClient: cs, httpClient: &http.Client{Timeout: 2 * time.Second}}
	clientMu.Lock()
	saved := globalClient
	globalClient = c
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		globalClient = saved
		clientMu.Unlock()
	})

	ctx := context.Background()
	svc := promService("monitoring", "prometheus")
	svc.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	if _, err := cs.CoreV1().Services("monitoring").Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	got := collectRuns(runs, 500*time.Millisecond)
	waitIdle(t, c)
	if len(got) != 1 || !strings.Contains(got[0], "service monitoring/prometheus created") {
		t.Fatalf("want one run for the new candidate Service, got %v", got)
	}

	cached, ok := c.cachedServices()
	if !ok || len(cached) != 1 || cached[0].Name != "prometheus" {
		t.Fatalf("discovery should enumerate from the cluster-wide cache, got ok=%v %v", ok, cached)
	}

	pod := readyPod("monitoring", "prometheus-0", map[string]string{"app": "prometheus"}, false)
	if _, err := cs.CoreV1().Pods("monitoring").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := collectRuns(runs, 200*time.Millisecond); len(got) != 0 {
		t.Fatalf("a Pod that is not Ready yet should not signal, got %v", got)
	}
	pod.Status.Conditions[0].Status = corev1.ConditionTrue
	if _, err := cs.CoreV1().Pods("monitoring").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	got = collectRuns(runs, 500*time.Millisecond)
	waitIdle(t, c)
	if len(got) != 1 || !strings.Contains(got[0], "pod monitoring/prometheus-0 of service monitoring/prometheus became ready") {
		t.Fatalf("want one run for the Pod turning Ready, got %v", got)
	}
}

// TestCachedServicesDeclinesPartialView pins the API fallback: a cache that
// watches Services in one namespace cannot vouch that a cluster has no
// Prometheus elsewhere.
func TestCachedServicesDeclinesPartialView(t *testing.T) {
	cs := fake.NewClientset()
	if err := k8s.InitScopedTestResourceCache(cs, map[string]k8score.ResourceScope{
		string(k8score.Services): {Enabled: true, Namespace: "team-a"},
	}); err != nil {
		t.Fatalf("InitScopedTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)

	if _, ok := (&Client{k8sClient: cs}).cachedServices(); ok {
		t.Fatal("namespace-scoped Service cache must not stand in for the cluster")
	}
	if _, ok := (&Client{k8sClient: fake.NewClientset()}).cachedServices(); ok {
		t.Fatal("a cache built from another cluster connection must not be used")
	}
}

// TestRediscovery_ConfigurationChangeCancelsFollowUps pins that a Reset,
// manual URL or header change (each moves the discovery generation) ends a
// pending follow-up budget instead of letting it rediscover under the new
// configuration.
func TestRediscovery_ConfigurationChangeCancelsFollowUps(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(c *Client)
	}{
		{"reset", func(c *Client) { c.discoveryGen++ }},
		{"manual URL", func(c *Client) { c.configureLocked("http://prom.example", nil) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			var c *Client
			runs := stubRediscovery(t, func(string) error {
				c.mu.Lock()
				change.apply(c)
				c.mu.Unlock()
				return errPrometheusUnreachable
			})
			c, _ = newRediscoveryClient(t)
			c.requestRediscovery("", "service created")
			got := collectRuns(runs, 300*time.Millisecond)
			waitIdle(t, c)
			if len(got) != 1 {
				t.Fatalf("follow-ups ran after the configuration changed: %v", got)
			}
		})
	}
}

// TestRediscovery_NewSignalInterruptsFollowUpWait pins that a signal arriving
// while the worker waits out a follow-up delay runs after its own debounce,
// with a fresh budget, instead of waiting for the older signal's delay.
func TestRediscovery_NewSignalInterruptsFollowUpWait(t *testing.T) {
	runs := stubRediscovery(t, func(label string) error {
		if strings.Contains(label, "service created") {
			return errPrometheusUnreachable
		}
		return nil
	})
	rediscoveryFollowUps = []time.Duration{5 * time.Second}
	c, _ := newRediscoveryClient(t)

	c.requestRediscovery("", "service created")
	if got := collectRuns(runs, 200*time.Millisecond); len(got) != 1 {
		t.Fatalf("want the signal's run, got %v", got)
	}
	c.requestRediscovery("", "pod became ready")
	select {
	case got := <-runs:
		if !strings.Contains(got, "pod became ready") {
			t.Fatalf("the new signal should name the run, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("new signal waited behind the follow-up delay")
	}
	waitIdle(t, c)
}
