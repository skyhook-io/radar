package traffic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func metricsSvc(ns, name string, port int32, clusterIP string, labels map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec: corev1.ServiceSpec{
			ClusterIP: clusterIP,
			Ports:     []corev1.ServicePort{{Name: "http", Port: port, TargetPort: intstr.FromString("http")}},
		},
	}
}

// carettaStoreSvc mirrors what the groundcover/caretta chart renders: the
// VictoriaMetrics subchart's name label, a headless service, port 8428.
func carettaStoreSvc(ns, name string) *corev1.Service {
	return metricsSvc(ns, name, 8428, "None", map[string]string{
		"app":                    "server",
		"app.kubernetes.io/name": "victoria-metrics-single",
	})
}

// kubePrometheusStackSvcs are the services a kube-prometheus-stack install puts
// in the well-known candidate list.
func kubePrometheusStackSvcs() []*corev1.Service {
	return []*corev1.Service{
		metricsSvc("monitoring", "prometheus-operated", 9090, "None", map[string]string{
			"operated-prometheus": "true",
		}),
		metricsSvc("monitoring", "kube-prometheus-stack-prometheus", 9090, "10.0.0.5", map[string]string{
			"app":                       "kube-prometheus-stack-prometheus",
			"app.kubernetes.io/part-of": "kube-prometheus-stack",
		}),
	}
}

func sourceWithServices(t *testing.T, detectedNS string, svcs ...*corev1.Service) *CarettaSource {
	t.Helper()
	cs := fake.NewSimpleClientset()
	for _, s := range svcs {
		if _, err := cs.CoreV1().Services(s.Namespace).Create(context.Background(), s, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding service %s/%s: %v", s.Namespace, s.Name, err)
		}
	}
	return &CarettaSource{
		k8sClient:         cs,
		httpClient:        &http.Client{Timeout: 500 * time.Millisecond},
		detectedNamespace: detectedNS,
	}
}

func discover(t *testing.T, c *CarettaSource) []*metricsServiceInfo {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.discoverServiceLocked(context.Background())
}

// The store Caretta ships must outrank the cluster's general Prometheus. This is
// the shape the wizard installs and the one reported in #1264.
func TestCarettaPrefersOwnStoreOverGeneralPrometheus(t *testing.T) {
	c := sourceWithServices(t, "caretta", append(kubePrometheusStackSvcs(), carettaStoreSvc("caretta", "caretta-vm"))...)

	got := discover(t, c)
	if len(got) == 0 {
		t.Fatal("no candidates discovered")
	}
	if got[0].namespace != "caretta" || got[0].name != "caretta-vm" {
		t.Errorf("top candidate = %s/%s, want caretta/caretta-vm", got[0].namespace, got[0].name)
	}
	if !got[0].isCarettaStore {
		t.Error("top candidate not marked as Caretta's own store")
	}
}

// The chart pins the service name but not the namespace: `helm install caretta
// groundcover/caretta` with no -n puts everything in `default`. A namespace-blind
// lookup walks past it and lands on kube-prometheus-stack, which holds no Caretta
// series and answers every query successfully.
func TestCarettaFindsStoreOutsideCarettaNamespace(t *testing.T) {
	for _, ns := range []string{"default", "kube-system", "monitoring", "observability"} {
		t.Run(ns, func(t *testing.T) {
			c := sourceWithServices(t, ns, append(kubePrometheusStackSvcs(), carettaStoreSvc(ns, "caretta-vm"))...)

			got := discover(t, c)
			if len(got) == 0 {
				t.Fatal("no candidates discovered")
			}
			if got[0].namespace != ns || got[0].name != "caretta-vm" {
				t.Errorf("top candidate = %s/%s, want %s/caretta-vm", got[0].namespace, got[0].name, ns)
			}
		})
	}
}

// A store whose name isn't the pinned caretta-vm is still found, because the
// lookup keys on the VictoriaMetrics subchart label rather than the name.
func TestCarettaFindsRenamedStoreByLabel(t *testing.T) {
	c := sourceWithServices(t, "caretta",
		append(kubePrometheusStackSvcs(), carettaStoreSvc("caretta", "caretta-victoria-metrics-single-server"))...)

	got := discover(t, c)
	if len(got) == 0 {
		t.Fatal("no candidates discovered")
	}
	if got[0].name != "caretta-victoria-metrics-single-server" {
		t.Errorf("top candidate = %s/%s, want the labelled VM store", got[0].namespace, got[0].name)
	}
}

// Without its own store, the general Prometheus is still offered — it may be
// scraping Caretta — but it comes second and has to prove it holds the data.
func TestCarettaOffersGeneralPrometheusWhenNoStoreExists(t *testing.T) {
	c := sourceWithServices(t, "caretta", kubePrometheusStackSvcs()...)

	got := discover(t, c)
	if len(got) == 0 {
		t.Fatal("no candidates discovered")
	}
	for _, info := range got {
		if info.isCarettaStore {
			t.Errorf("%s/%s wrongly marked as Caretta's own store", info.namespace, info.name)
		}
	}
}

// promBackend describes what a stub Prometheus holds. A real backend returns an
// empty vector for a metric it never scraped while still answering `up`, which is
// exactly why the generic reachability probe can't tell backends apart.
type promBackend struct {
	links      bool // has caretta_links_observed series
	carettaJob bool // scrapes a target whose job name contains "caretta"
}

func promStub(t *testing.T, backend promBackend) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := url.QueryUnescape(r.URL.Query().Get("query"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var hasSeries bool
		switch {
		case strings.Contains(q, "caretta_links_observed"):
			hasSeries = backend.links
		case strings.Contains(q, "caretta"):
			hasSeries = backend.carettaJob
		default:
			hasSeries = true // plain `up` — every Prometheus answers this
		}

		result := []any{}
		if hasSeries {
			result = append(result, map[string]any{
				"metric": map[string]string{"client_name": "frontend", "server_name": "backend", "server_port": "80"},
				"value":  []any{1.0, "3"},
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "vector", "result": result},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func accept(t *testing.T, c *CarettaSource, info *metricsServiceInfo, addr string) bool {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.acceptBackendLocked(context.Background(), info, addr) == backendAccepted
}

// The generic reachability probe cannot tell the two apart — that is what made
// the wrong-backend failure silent. Acceptance has to look at the content.
func TestAcceptBackendRejectsPrometheusWithoutCarettaData(t *testing.T) {
	general := promStub(t, promBackend{})
	c := &CarettaSource{httpClient: &http.Client{Timeout: 2 * time.Second}}

	c.mu.Lock()
	reachable := c.tryMetricsEndpointLocked(context.Background(), general.URL)
	c.mu.Unlock()
	if !reachable {
		t.Fatal("stub should pass the generic reachability probe")
	}

	if accept(t, c, &metricsServiceInfo{namespace: "monitoring", name: "prometheus-operated"}, general.URL) {
		t.Error("accepted a Prometheus that holds no Caretta metrics")
	}
}

// A general Prometheus that does scrape Caretta (ServiceMonitor deployments) is
// the correct backend and must not be rejected by an identity-based gate.
func TestAcceptBackendAcceptsPrometheusScrapingCaretta(t *testing.T) {
	scraping := promStub(t, promBackend{links: true})
	c := &CarettaSource{httpClient: &http.Client{Timeout: 2 * time.Second}}

	if !accept(t, c, &metricsServiceInfo{namespace: "monitoring", name: "kube-prometheus-stack-prometheus"}, scraping.URL) {
		t.Error("rejected a Prometheus that does hold Caretta metrics")
	}
}

// A Caretta install that hasn't observed a connection yet holds no
// caretta_links_observed. Its own store is accepted on identity so an idle
// cluster doesn't read as a misconfiguration.
func TestAcceptBackendAcceptsOwnStoreWithoutSeriesYet(t *testing.T) {
	fresh := promStub(t, promBackend{})
	c := &CarettaSource{httpClient: &http.Client{Timeout: 2 * time.Second}}

	if !accept(t, c, &metricsServiceInfo{namespace: "caretta", name: "caretta-vm", isCarettaStore: true}, fresh.URL) {
		t.Error("rejected Caretta's own store because it has no links yet")
	}
}

// The scrape-target signal covers a store that has targets but no links.
func TestCarettaMetricsPresentAcceptsScrapeTargetSignal(t *testing.T) {
	srv := promStub(t, promBackend{carettaJob: true})
	c := &CarettaSource{httpClient: &http.Client{Timeout: 2 * time.Second}}

	c.mu.Lock()
	got := c.carettaMetricsPresentLocked(context.Background(), srv.URL)
	c.mu.Unlock()
	if !got {
		t.Error("scrape-target signal not recognised")
	}
}

// The acceptance probe must carry the configured auth headers, or an
// auth-protected store reads as "holds no Caretta metrics".
func TestCarettaProbeCarriesHeaders(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	c := &CarettaSource{
		httpClient: &http.Client{Timeout: 2 * time.Second},
		headers:    map[string]string{"Authorization": "Bearer caretta-token"},
	}
	c.mu.Lock()
	c.carettaMetricsPresentLocked(context.Background(), srv.URL)
	c.mu.Unlock()

	if gotAuth != "Bearer caretta-token" {
		t.Errorf("probe Authorization = %q, want %q", gotAuth, "Bearer caretta-token")
	}
}

// Zero flows from a backend that never proved it holds Caretta data is
// indistinguishable from a quiet cluster unless the response says so.
func TestGetFlowsWarnsWhenBackendHasNoCarettaMetrics(t *testing.T) {
	general := promStub(t, promBackend{})

	c := &CarettaSource{
		httpClient:       &http.Client{Timeout: 2 * time.Second},
		prometheusAddr:   general.URL,
		metricsNamespace: "monitoring",
		metricsService:   "prometheus-operated",
		isConnected:      true,
	}

	resp, err := c.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("GetFlows: %v", err)
	}
	if len(resp.Flows) != 0 {
		t.Fatalf("expected 0 flows, got %d", len(resp.Flows))
	}
	if resp.Warning == "" {
		t.Fatal("zero flows from an unverified backend reported without a warning")
	}
	if !strings.Contains(resp.Warning, "monitoring/prometheus-operated") {
		t.Errorf("warning does not name the backend: %q", resp.Warning)
	}
}

// A verified backend that is simply quiet must not raise a false alarm.
func TestGetFlowsStaysSilentOnVerifiedBackend(t *testing.T) {
	store := promStub(t, promBackend{})

	c := &CarettaSource{
		httpClient:       &http.Client{Timeout: 2 * time.Second},
		prometheusAddr:   store.URL,
		metricsNamespace: "caretta",
		metricsService:   "caretta-vm",
		isConnected:      true,
		backendVerified:  true,
	}

	resp, err := c.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("GetFlows: %v", err)
	}
	if resp.Warning != "" {
		t.Errorf("verified quiet backend warned anyway: %q", resp.Warning)
	}
}

// Reached-but-wrong and never-reached are different problems: one means Radar is
// reading the wrong database, the other means it read nothing at all. Collapsing
// them sends the user to fix the wrong thing.
func TestNoBackendWarningDistinguishesRejectionReasons(t *testing.T) {
	tests := []struct {
		name        string
		noData      []string
		unreachable []string
		want        string
	}{
		{"holds no caretta data", []string{"monitoring/prometheus-operated"}, nil, "holds no Caretta metrics"},
		{"never reached", nil, []string{"caretta/caretta-vm"}, "could not reach it"},
		{"data problem wins", []string{"monitoring/prometheus-operated"}, []string{"caretta/caretta-vm"}, "holds no Caretta metrics"},
		{"nothing found", nil, nil, "service not found"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := noBackendWarning(tc.noData, tc.unreachable)
			if !strings.Contains(got, tc.want) {
				t.Errorf("noBackendWarning(%v, %v) = %q, want it to contain %q", tc.noData, tc.unreachable, got, tc.want)
			}
		})
	}
}

// The well-known list still carries caretta-vm for split installs. A store found
// that way must be treated the same as one found by label, or a Caretta with no
// links yet is admitted or rejected depending on which path happened to find it.
func TestWellKnownCarettaStoreIsMarkedAsOwnStore(t *testing.T) {
	// Detected in `default`, but the store lives in `caretta` — only the well-known
	// list can find it.
	c := sourceWithServices(t, "default", append(kubePrometheusStackSvcs(), carettaStoreSvc("caretta", "caretta-vm"))...)

	got := discover(t, c)
	if len(got) == 0 {
		t.Fatal("no candidates discovered")
	}
	if got[0].namespace != "caretta" || got[0].name != "caretta-vm" {
		t.Fatalf("top candidate = %s/%s, want caretta/caretta-vm", got[0].namespace, got[0].name)
	}
	if !got[0].isCarettaStore {
		t.Error("well-known caretta-vm not marked as Caretta's own store")
	}
}

// A cluster can run more than one Prometheus-family backend. If the well-known
// walk stops at the first hit, a cluster whose earlier backend holds no Caretta
// data never reaches the later one that scrapes Caretta, and Live Traffic fails
// closed on a cluster that has the data.
func TestWellKnownLayerOffersEveryMatch(t *testing.T) {
	vm := metricsSvc("monitoring", "victoria-metrics-single-server", 8428, "10.0.0.9", map[string]string{
		"app.kubernetes.io/name": "victoria-metrics-single",
	})
	c := sourceWithServices(t, "caretta", append(kubePrometheusStackSvcs(), vm)...)

	got := discover(t, c)
	if len(got) < 2 {
		t.Fatalf("got %d candidate(s), want every well-known match", len(got))
	}

	names := make([]string, 0, len(got))
	for _, info := range got {
		names = append(names, info.namespace+"/"+info.name)
	}
	want := []string{"monitoring/victoria-metrics-single-server", "monitoring/prometheus-operated"}
	for _, w := range want {
		if !slices.Contains(names, w) {
			t.Errorf("candidate %s missing from %v", w, names)
		}
	}

	// Declared order decides: the VictoriaMetrics entry precedes prometheus-operated.
	if slices.Index(names, want[0]) > slices.Index(names, want[1]) {
		t.Errorf("candidates out of declared order: %v", names)
	}
}

// Local, the candidate list is walked with a port-forward per attempt, so it
// stays bounded by the cap.
func TestCandidateListIsCapped(t *testing.T) {
	svcs := kubePrometheusStackSvcs()
	svcs = append(svcs,
		metricsSvc("monitoring", "victoria-metrics-single-server", 8428, "10.0.0.9", nil),
		metricsSvc("monitoring", "vmsingle", 8428, "10.0.0.10", nil),
		metricsSvc("monitoring", "prometheus-server", 9090, "10.0.0.11", nil),
		metricsSvc("prometheus", "prometheus-server", 9090, "10.0.0.12", nil),
	)
	c := sourceWithServices(t, "caretta", svcs...) // inCluster defaults to false (local)

	if got := discover(t, c); len(got) > maxMetricsCandidates {
		t.Errorf("got %d candidates, want at most %d", len(got), maxMetricsCandidates)
	}
}

// recordingTransport captures every URL a probe dials and fails the request, so
// a test can assert which addresses discovery attempted without a live backend.
type recordingTransport struct {
	mu   sync.Mutex
	urls []string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.urls = append(rt.urls, req.URL.String())
	rt.mu.Unlock()
	return nil, fmt.Errorf("recorded, not dialed")
}

func (rt *recordingTransport) probedClusterAddress() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, u := range rt.urls {
		if strings.Contains(u, "svc.cluster.local") {
			return true
		}
	}
	return false
}

// In-cluster each candidate costs only a cheap cluster-address probe and is never
// port-forwarded, so the local port-forward cap must not truncate the list — a cap
// there silently drops reachable backends from consideration.
func TestInClusterProbesEveryCandidateNoCap(t *testing.T) {
	svcs := kubePrometheusStackSvcs()
	svcs = append(svcs,
		metricsSvc("monitoring", "victoria-metrics-single-server", 8428, "10.0.0.9", nil),
		metricsSvc("monitoring", "vmsingle", 8428, "10.0.0.10", nil),
		metricsSvc("monitoring", "prometheus-server", 9090, "10.0.0.11", nil),
		metricsSvc("prometheus", "prometheus-server", 9090, "10.0.0.12", nil),
	)
	c := sourceWithServices(t, "caretta", svcs...)
	c.inCluster = true

	if got := discover(t, c); len(got) <= maxMetricsCandidates {
		t.Errorf("in-cluster returned %d candidates, want more than the local cap of %d", len(got), maxMetricsCandidates)
	}
}

// Local, a cluster address (*.svc.cluster.local) cannot resolve and each probe
// costs a guaranteed multi-second dead-wait. Discovery must skip it and rely on
// port-forwards (started by Connect), never probe the cluster address.
func TestLocalDiscoveryDoesNotProbeClusterAddresses(t *testing.T) {
	rec := &recordingTransport{}
	c := sourceWithServices(t, "caretta", append(kubePrometheusStackSvcs(), carettaStoreSvc("caretta", "caretta-vm"))...)
	c.httpClient = &http.Client{Transport: rec, Timeout: time.Second}
	c.inCluster = false

	_ = c.discoverPrometheus(context.Background())

	if rec.probedClusterAddress() {
		t.Errorf("local discovery probed a cluster address; probed=%v", rec.urls)
	}
}

// In-cluster the cluster address resolves in single-digit ms, so discovery must
// probe it directly (the fast path this whole branch exists to take).
func TestInClusterDiscoveryProbesClusterAddress(t *testing.T) {
	rec := &recordingTransport{}
	c := sourceWithServices(t, "caretta", carettaStoreSvc("caretta", "caretta-vm"))
	c.httpClient = &http.Client{Transport: rec, Timeout: time.Second}
	c.inCluster = true

	_ = c.discoverPrometheus(context.Background())

	if !rec.probedClusterAddress() {
		t.Errorf("in-cluster discovery did not probe the cluster address; probed=%v", rec.urls)
	}
}

func carettaPod(ns, name, instance string, running bool) *corev1.Pod {
	phase := corev1.PodPending
	if running {
		phase = corev1.PodRunning
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			Labels: map[string]string{
				"app.kubernetes.io/name":     "caretta",
				"app.kubernetes.io/instance": instance,
			},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func sourceWithObjects(t *testing.T, pods []*corev1.Pod, svcs ...*corev1.Service) *CarettaSource {
	t.Helper()
	cs := fake.NewSimpleClientset()
	for _, p := range pods {
		if _, err := cs.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding pod %s/%s: %v", p.Namespace, p.Name, err)
		}
	}
	for _, s := range svcs {
		if _, err := cs.CoreV1().Services(s.Namespace).Create(context.Background(), s, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding service %s/%s: %v", s.Namespace, s.Name, err)
		}
	}
	return &CarettaSource{k8sClient: cs, httpClient: &http.Client{Timeout: 500 * time.Millisecond}}
}

// Caretta's namespace follows its Helm release, so an install outside the three
// hardcoded names is still real. Without this it isn't detected at all, and the
// namespace-aware store lookup never runs.
func TestDetectFindsCarettaInAnyNamespace(t *testing.T) {
	c := sourceWithObjects(t, []*corev1.Pod{carettaPod("observability", "caretta-abc", "caretta", true)})

	result, err := c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !result.Available {
		t.Fatalf("Caretta not detected: %s", result.Message)
	}

	c.mu.RLock()
	ns, instance := c.detectedNamespace, c.detectedInstance
	c.mu.RUnlock()
	if ns != "observability" {
		t.Errorf("detectedNamespace = %q, want observability", ns)
	}
	if instance != "caretta" {
		t.Errorf("detectedInstance = %q, want caretta", instance)
	}
}

// Namespace and release must be read off the same pod: they jointly decide where
// the store is looked up and which store is trusted without a content probe.
func TestDetectPrefersRunningPodForIdentity(t *testing.T) {
	c := sourceWithObjects(t, []*corev1.Pod{
		carettaPod("aaa-dead", "caretta-old", "stale-release", false),
		carettaPod("zzz-live", "caretta-new", "live-release", true),
	})

	if _, err := c.Detect(context.Background()); err != nil {
		t.Fatalf("Detect: %v", err)
	}

	c.mu.RLock()
	ns, instance := c.detectedNamespace, c.detectedInstance
	c.mu.RUnlock()
	if ns != "zzz-live" || instance != "live-release" {
		t.Errorf("identity = %s/%s, want zzz-live/live-release", ns, instance)
	}
}

// Sharing a namespace with Caretta is not proof of belonging to it. `default` and
// `monitoring` host unrelated VictoriaMetrics; trusting one on identity would skip
// the content check and put the silent zero-flow failure right back.
func TestUnrelatedVictoriaMetricsMustEarnAdmission(t *testing.T) {
	unrelated := metricsSvc("default", "billing-metrics", 8428, "10.0.0.7", map[string]string{
		"app.kubernetes.io/name":     "victoria-metrics-single",
		"app.kubernetes.io/instance": "billing",
	})
	c := sourceWithServices(t, "default", unrelated)
	c.detectedInstance = "caretta"

	got := discover(t, c)
	if len(got) == 0 {
		t.Fatal("no candidates discovered")
	}
	if got[0].isCarettaStore {
		t.Error("an unrelated VictoriaMetrics was trusted as Caretta's own store")
	}
}

// A store belonging to the same Helm release as Caretta is its own, whatever the
// release was named.
func TestStoreOfSameReleaseIsTrusted(t *testing.T) {
	store := metricsSvc("default", "traffic-vm", 8428, "None", map[string]string{
		"app.kubernetes.io/name":     "victoria-metrics-single",
		"app.kubernetes.io/instance": "traffic",
	})
	c := sourceWithServices(t, "default", store)
	c.detectedInstance = "traffic"

	got := discover(t, c)
	if len(got) == 0 {
		t.Fatal("no candidates discovered")
	}
	if !got[0].isCarettaStore {
		t.Error("store from Caretta's own release not trusted")
	}
}

// A third-party backend was admitted because it held Caretta data at bind time.
// It can stop scraping Caretta later; if the cached-address check only asks `up`,
// backendVerified stays true and the zero-flow warning is suppressed again.
func TestCachedGenericBackendIsRevalidated(t *testing.T) {
	scraping := promStub(t, promBackend{links: true})

	c := &CarettaSource{
		httpClient:          &http.Client{Timeout: 2 * time.Second},
		prometheusAddr:      scraping.URL,
		backendVerified:     true,
		boundIsCarettaStore: false,
	}

	c.mu.Lock()
	stillUp := c.revalidateBoundLocked(context.Background(), scraping.URL)
	verified := c.backendVerified
	c.mu.Unlock()
	if !stillUp || !verified {
		t.Fatalf("backend still holds Caretta data: reachable=%v verified=%v", stillUp, verified)
	}

	// Same backend, no longer scraping Caretta.
	stopped := promStub(t, promBackend{})
	c.mu.Lock()
	c.prometheusAddr = stopped.URL
	stillUp = c.revalidateBoundLocked(context.Background(), stopped.URL)
	verified = c.backendVerified
	c.mu.Unlock()
	if !stillUp {
		t.Fatal("backend is reachable, revalidation said otherwise")
	}
	if verified {
		t.Error("backend that stopped holding Caretta data is still marked verified")
	}
}

// Caretta's own store is trusted on identity, so an idle one must not be
// downgraded on revalidation and start warning about a healthy install.
func TestCachedOwnStoreStaysVerifiedWhenIdle(t *testing.T) {
	idle := promStub(t, promBackend{})

	c := &CarettaSource{
		httpClient:          &http.Client{Timeout: 2 * time.Second},
		prometheusAddr:      idle.URL,
		backendVerified:     true,
		boundIsCarettaStore: true,
	}

	c.mu.Lock()
	ok := c.revalidateBoundLocked(context.Background(), idle.URL)
	verified := c.backendVerified
	c.mu.Unlock()
	if !ok || !verified {
		t.Errorf("idle own store downgraded: reachable=%v verified=%v", ok, verified)
	}
}

// A dead install leaves pods behind. Returning on them hides a healthy Caretta in
// another namespace and pins store discovery to the wrong release.
func TestDetectSkipsPastStoppedPodsToRunningInstall(t *testing.T) {
	c := sourceWithObjects(t, []*corev1.Pod{
		carettaPod("default", "caretta-dead", "old-release", false),
		carettaPod("observability", "caretta-live", "new-release", true),
	})

	result, err := c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !result.Available {
		t.Fatalf("healthy Caretta not detected: %s", result.Message)
	}

	c.mu.RLock()
	ns, instance := c.detectedNamespace, c.detectedInstance
	c.mu.RUnlock()
	if ns != "observability" || instance != "new-release" {
		t.Errorf("identity = %s/%s, want observability/new-release", ns, instance)
	}
}

// With nothing running anywhere, the stopped pods are still reported — that is
// the diagnosis the user needs, and it must not become "not detected".
func TestDetectStillReportsStoppedPodsWhenNothingRuns(t *testing.T) {
	c := sourceWithObjects(t, []*corev1.Pod{carettaPod("default", "caretta-dead", "old-release", false)})

	result, err := c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if result.Available {
		t.Error("no pod is running, Caretta reported available")
	}
	if !strings.Contains(result.Message, "none are running") {
		t.Errorf("message = %q, want it to say no pods are running", result.Message)
	}
}

// Connect does not run detection, so the store lookup cannot assume Detect has
// already recorded a namespace. When it hasn't, Caretta's own store must still be
// found — otherwise connecting before listing sources binds the wrong backend.
func TestStoreFoundWithoutPriorDetection(t *testing.T) {
	c := sourceWithServices(t, "", append(kubePrometheusStackSvcs(), carettaStoreSvc("default", "caretta-vm"))...)

	got := discover(t, c)
	if len(got) == 0 {
		t.Fatal("no candidates discovered")
	}
	if got[0].namespace != "default" || got[0].name != "caretta-vm" {
		t.Errorf("top candidate = %s/%s, want default/caretta-vm", got[0].namespace, got[0].name)
	}
	if !got[0].isCarettaStore {
		t.Error("store found without prior detection not trusted as Caretta's own")
	}
}

// Same, for an install in a namespace none of the fixed names cover.
func TestStoreFoundWithoutPriorDetectionInAnyNamespace(t *testing.T) {
	c := sourceWithServices(t, "", append(kubePrometheusStackSvcs(), carettaStoreSvc("observability", "caretta-vm"))...)

	got := discover(t, c)
	if len(got) == 0 {
		t.Fatal("no candidates discovered")
	}
	if got[0].namespace != "observability" || got[0].name != "caretta-vm" {
		t.Errorf("top candidate = %s/%s, want observability/caretta-vm", got[0].namespace, got[0].name)
	}
}

// A headless Service publishes A records for its ready pods under its own name.
// Addressing it as {service}-0.{service} assumes the StatefulSet is named after
// the Service — true for caretta-vm, false for kube-prometheus-stack's
// prometheus-operated — and made a reachable backend look unreachable in-cluster.
func TestClusterAddrUsesPlainServiceDNS(t *testing.T) {
	tests := []struct {
		name, namespace string
		port            int
		want            string
	}{
		{"caretta-vm", "default", 8428, "http://caretta-vm.default.svc.cluster.local:8428"},
		{"prometheus-operated", "monitoring", 9090, "http://prometheus-operated.monitoring.svc.cluster.local:9090"},
	}
	for _, tc := range tests {
		if got := buildClusterAddr(tc.name, tc.namespace, tc.port); got != tc.want {
			t.Errorf("buildClusterAddr(%s/%s) = %q, want %q", tc.namespace, tc.name, got, tc.want)
		}
	}
}
