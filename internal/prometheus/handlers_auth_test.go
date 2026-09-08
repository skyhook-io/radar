package prometheus

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// These tests drive the HTTP metrics routes end-to-end against a fake
// Prometheus, with the AuthGate seam standing in for the server's SAR-backed
// check. They use the global client singleton, so none run in parallel.

const (
	authProbeBody  = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"up"},"value":[1700000000,"1"]}]}}`
	authVectorBody = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"pod":"a"},"value":[1700000000,"1"]}]}}`
	authMatrixBody = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"pod":"a"},"values":[[1700000000,"1"],[1700000060,"2"]]}]}}`
)

type authFakeProm struct {
	mu          sync.Mutex
	rangeParams []url.Values
	queryCalls  int
	rangeBody   string
	queryBody   string // instant-query body; empty falls back to authVectorBody
}

func (f *authFakeProm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	switch r.URL.Path {
	case "/api/v1/query":
		if q.Get("query") == "up" {
			_, _ = w.Write([]byte(authProbeBody))
			return
		}
		f.queryCalls++
		body := f.queryBody
		if body == "" {
			body = authVectorBody
		}
		_, _ = w.Write([]byte(body))
	case "/api/v1/query_range":
		f.rangeParams = append(f.rangeParams, q)
		body := f.rangeBody
		if body == "" {
			body = authMatrixBody
		}
		_, _ = w.Write([]byte(body))
	default:
		http.NotFound(w, r)
	}
}

func (f *authFakeProm) queries(t *testing.T) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.rangeParams))
	for _, p := range f.rangeParams {
		out = append(out, p.Get("query"))
	}
	return out
}

func (f *authFakeProm) dataCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rangeParams) + f.queryCalls
}

func setupAuthFakeProm(t *testing.T) *authFakeProm {
	t.Helper()
	f := &authFakeProm{}
	srv := httptest.NewServer(f)
	Initialize(nil, nil, "auth-test")
	SetManualURL(srv.URL)
	seedScopeCache(t)
	t.Cleanup(func() {
		srv.Close()
		Reset()
		Initialize(nil, nil, "")
	})
	return f
}

// seedScopeCache gives the workload rows of the matrix pods to resolve:
// Deployment alpha/web owns one pod through a ReplicaSet, CronJob
// alpha/nightly owns one through a Job. Without a cluster cache the chart
// handler refuses rather than guessing pods from the name.
func seedScopeCache(t *testing.T) {
	t.Helper()
	isController := true
	owner := func(kind, name string) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &isController}
	}
	objects := []runtime.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "web"}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "web-7f6", OwnerReferences: []metav1.OwnerReference{owner("Deployment", "web")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "web-7f6-a", OwnerReferences: []metav1.OwnerReference{owner("ReplicaSet", "web-7f6")}}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "nightly"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "nightly-1", OwnerReferences: []metav1.OwnerReference{owner("CronJob", "nightly")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "nightly-1-k", OwnerReferences: []metav1.OwnerReference{owner("Job", "nightly-1")}}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	ResetPodScopeCache()
	t.Cleanup(func() {
		ResetPodScopeCache()
		k8s.ResetTestState()
	})
}

func metricsRouter() http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r)
	return r
}

func getMetrics(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

type gateCall struct {
	group, resource, namespace, verb string
}

// recordingGate installs an AuthGate driven by decide and records every
// tuple it was asked about.
func recordingGate(t *testing.T, decide func(gateCall) bool) *[]gateCall {
	t.Helper()
	calls := &[]gateCall{}
	var mu sync.Mutex
	SetAuthGate(func(_ *http.Request, group, resource, namespace, verb string) bool {
		c := gateCall{group, resource, namespace, verb}
		mu.Lock()
		*calls = append(*calls, c)
		mu.Unlock()
		return decide(c)
	})
	t.Cleanup(func() { SetAuthGate(nil) })
	return calls
}

// One row per surface in the authorization matrix: the path and the exact
// tuple the gate must be asked about.
var metricsMatrix = []struct {
	name string
	path string
	want gateCall
}{
	{"namespaced resource", "/prometheus/resources/Deployment/alpha/web?category=cpu", gateCall{"apps", "deployments", "alpha", "get"}},
	{"pod", "/prometheus/resources/Pod/alpha/web-1?category=memory", gateCall{"", "pods", "alpha", "get"}},
	{"cronjob", "/prometheus/resources/CronJob/alpha/nightly", gateCall{"batch", "cronjobs", "alpha", "get"}},
	{"node", "/prometheus/resources/Node/worker-1?category=cpu", gateCall{"", "nodes", "", "get"}},
	{"node via namespaced route", "/prometheus/resources/Node/alpha/worker-1?category=cpu", gateCall{"", "nodes", "", "get"}},
	{"namespace aggregate", "/prometheus/namespace/alpha?category=cpu", gateCall{"", "pods", "alpha", "list"}},
	{"cluster aggregate", "/prometheus/cluster?category=cpu", gateCall{"", "pods", "", "list"}},
	{"raw range query", "/prometheus/query?query=up", gateCall{"", "pods", "", "list"}},
	{"raw instant query", "/prometheus/query?query=up&type=instant", gateCall{"", "pods", "", "list"}},
	{"hpa", "/prometheus/hpa/alpha/web", gateCall{"autoscaling", "horizontalpodautoscalers", "alpha", "get"}},
}

func TestMetricsRoutes_NoGatePassesThrough(t *testing.T) {
	SetAuthGate(nil)
	f := setupAuthFakeProm(t)
	h := metricsRouter()

	for _, row := range metricsMatrix {
		t.Run(row.name, func(t *testing.T) {
			rec := getMetrics(t, h, row.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if f.dataCalls() < len(metricsMatrix) {
		t.Fatalf("fake prometheus saw %d data queries, want at least one per row (%d)", f.dataCalls(), len(metricsMatrix))
	}
}

func TestMetricsRoutes_AuthorizedUserIsAskedTheRightTuple(t *testing.T) {
	setupAuthFakeProm(t)
	h := metricsRouter()

	for _, row := range metricsMatrix {
		t.Run(row.name, func(t *testing.T) {
			calls := recordingGate(t, func(gateCall) bool { return true })
			rec := getMetrics(t, h, row.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if len(*calls) != 1 {
				t.Fatalf("gate called %d times, want exactly 1: %+v", len(*calls), *calls)
			}
			if (*calls)[0] != row.want {
				t.Fatalf("gate asked %+v, want %+v", (*calls)[0], row.want)
			}
		})
	}
}

// A user who can read everything in namespace alpha and nothing beyond it:
// their own namespace's charts work, every cross-namespace or cluster-wide
// surface is refused before Prometheus is touched.
func TestMetricsRoutes_NamespaceScopedUser(t *testing.T) {
	f := setupAuthFakeProm(t)
	h := metricsRouter()
	recordingGate(t, func(c gateCall) bool { return c.namespace == "alpha" })

	allowed := []string{
		"/prometheus/resources/Deployment/alpha/web",
		"/prometheus/resources/Pod/alpha/web-1",
		"/prometheus/namespace/alpha",
		"/prometheus/hpa/alpha/web",
	}
	for _, path := range allowed {
		if rec := getMetrics(t, h, path); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
		}
	}
	before := f.dataCalls()

	denied := []struct {
		path        string
		wantMessage string
	}{
		{"/prometheus/resources/Deployment/beta/web", "forbidden"},
		{"/prometheus/resources/Pod/beta/web-1", "forbidden"},
		{"/prometheus/resources/Node/worker-1", "forbidden"},
		{"/prometheus/resources/Node/alpha/worker-1", "forbidden"},
		{"/prometheus/namespace/beta", "forbidden"},
		{"/prometheus/hpa/beta/web", "forbidden"},
		{"/prometheus/cluster", ClusterWideMetricsDeniedMessage},
		{"/prometheus/query?query=up", ClusterWideMetricsDeniedMessage},
		{"/prometheus/query?query=up&type=instant", ClusterWideMetricsDeniedMessage},
	}
	for _, d := range denied {
		rec := getMetrics(t, h, d.path)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403; body=%s", d.path, rec.Code, rec.Body.String())
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: decode body: %v", d.path, err)
			continue
		}
		if body["error"] != d.wantMessage {
			t.Errorf("%s: error = %q, want %q", d.path, body["error"], d.wantMessage)
		}
	}
	if got := f.dataCalls(); got != before {
		t.Fatalf("denied requests reached prometheus: %d data queries after denials, want %d", got, before)
	}
}

func TestHandleHPAMetrics_QueriesBothReplicaSeries(t *testing.T) {
	SetAuthGate(nil)
	f := setupAuthFakeProm(t)
	h := metricsRouter()

	rec := getMetrics(t, h, `/prometheus/hpa/alpha/web"x?range=6h`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp HPAMetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Namespace != "alpha" || resp.Name != `web"x` || resp.Range != "6h" {
		t.Errorf("echoed identity = %q/%q range=%q", resp.Namespace, resp.Name, resp.Range)
	}
	if resp.Current == nil || len(resp.Current.Series) != 1 || resp.Desired == nil || len(resp.Desired.Series) != 1 {
		t.Fatalf("expected one series in each of current and desired, got %+v", resp)
	}

	queries := f.queries(t)
	if len(queries) != 2 {
		t.Fatalf("prometheus saw %d range queries, want 2: %v", len(queries), queries)
	}
	wantCurrent := `kube_horizontalpodautoscaler_status_current_replicas{namespace="alpha",horizontalpodautoscaler="web\"x"}`
	wantDesired := `kube_horizontalpodautoscaler_status_desired_replicas{namespace="alpha",horizontalpodautoscaler="web\"x"}`
	if queries[0] != wantCurrent {
		t.Errorf("current query = %q, want %q", queries[0], wantCurrent)
	}
	if queries[1] != wantDesired {
		t.Errorf("desired query = %q, want %q", queries[1], wantDesired)
	}
	if step := f.rangeParams[0].Get("step"); step != "300" {
		t.Errorf("6h range step = %q, want 300 (same range handling as resource metrics)", step)
	}
}

func TestHandleRawQuery_OversizedResultIsSummarized(t *testing.T) {
	SetAuthGate(nil)
	f := setupAuthFakeProm(t)
	h := metricsRouter()
	t.Setenv("RADAR_MCP_PROM_MAX_RESPONSE_BYTES", "200")

	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	for i := 0; i < 20; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"metric":{"pod":"web-` + strings.Repeat("x", i+1) + `"},"values":[[1700000000,"1"],[1700000060,"2"]]}`)
	}
	b.WriteString(`]}}`)
	f.rangeBody = b.String()

	rec := getMetrics(t, h, "/prometheus/query?query=container_memory_working_set_bytes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp RawQueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Truncated {
		t.Fatal("oversized result should be marked truncated")
	}
	if len(resp.Series) != 0 {
		t.Errorf("truncated response must not carry partial series, got %d", len(resp.Series))
	}
	var summary struct {
		SeriesCount int    `json:"seriesCount"`
		Suggestion  string `json:"suggestion"`
	}
	if err := json.Unmarshal(resp.Summary, &summary); err != nil {
		t.Fatalf("decode summary: %v (%s)", err, resp.Summary)
	}
	if summary.SeriesCount != 20 {
		t.Errorf("summary seriesCount = %d, want 20", summary.SeriesCount)
	}
	if !strings.Contains(summary.Suggestion, "topk(5, container_memory_working_set_bytes)") {
		t.Errorf("summary suggestion = %q, want a topk rewrite", summary.Suggestion)
	}
}

func TestHandleRawQuery_SmallResultIsReturnedWhole(t *testing.T) {
	SetAuthGate(nil)
	setupAuthFakeProm(t)
	h := metricsRouter()

	rec := getMetrics(t, h, "/prometheus/query?query=up&type=instant")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp RawQueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Truncated || resp.Summary != nil {
		t.Errorf("small result must not be truncated: %+v", resp)
	}
	if resp.ResultType != "vector" || len(resp.Series) != 1 {
		t.Errorf("resultType=%q series=%d, want vector/1", resp.ResultType, len(resp.Series))
	}
}

// Every curated kind must resolve to the resource its read check runs
// against; an unmapped kind fails closed in canReadMetricsResource, so a
// new SupportedKinds entry cannot ship ungated.
func TestMetricsKindResourceCoversSupportedKinds(t *testing.T) {
	for _, kind := range prom.SupportedKinds() {
		if _, resource, _, ok := metricsKindResource(kind); !ok || resource == "" {
			t.Errorf("kind %q has no read-check mapping", kind)
		}
	}
	t.Cleanup(func() { SetAuthGate(nil) })
	SetAuthGate(func(*http.Request, string, string, string, string) bool { return true })
	if canReadMetricsResource(httptest.NewRequest(http.MethodGet, "/", nil), "Widget", "alpha") {
		t.Error("an unmapped kind must be denied even when the gate allows everything")
	}
	// A builtin Radar does not chart resolves in the shared GVR catalogues but
	// is not chartable, so the supported-kind allowlist must still refuse it.
	if _, _, _, ok := metricsKindResource("Secret"); ok {
		t.Error("a builtin kind outside prom.SupportedKinds must not map")
	}
}

// The workload chart reports how its pods were established, and never falls
// back to a name prefix that would also chart a sibling workload's pods.
func TestResourceMetricsReportsPodCoverage(t *testing.T) {
	SetAuthGate(nil)
	f := setupAuthFakeProm(t)
	h := metricsRouter()

	rec := getMetrics(t, h, "/prometheus/resources/Deployment/alpha/web?category=cpu")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp ResourceMetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// This fake answers the ownership probe, so the chart covers the pods
	// kube-state-metrics attributed across the window and says so.
	if resp.Coverage != OwnerCoverageKSMHistory || resp.Pods != 1 || resp.PodsTotal != 1 {
		t.Fatalf("coverage = %q pods = %d/%d, want ksm_history with the one current pod counted", resp.Coverage, resp.Pods, resp.PodsTotal)
	}
	queries := f.queries(t)
	if len(queries) == 0 {
		t.Fatal("no range query reached prometheus")
	}
	last := queries[len(queries)-1]
	if !strings.Contains(last, "kube_replicaset_owner{namespace='alpha',owner_kind='Deployment',owner_name='web'") {
		t.Fatalf("query should join through the workload's ReplicaSets: %s", last)
	}
	if strings.Contains(last, "web-.*") {
		t.Fatalf("query still infers pods from the workload name: %s", last)
	}
	if !strings.Contains(last, "by (pod,namespace)") {
		t.Fatalf("the workload page needs one series per pod: %s", last)
	}
}

// Without ownership history the chart falls back to the pods the cluster
// shows now, named exactly.
func TestResourceMetricsFallsBackToCurrentPods(t *testing.T) {
	SetAuthGate(nil)
	f := setupAuthFakeProm(t)
	f.queryBody = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	h := metricsRouter()

	rec := getMetrics(t, h, "/prometheus/resources/Deployment/alpha/web?category=cpu")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp ResourceMetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Coverage != OwnerCoverageCurrentPods || resp.Pods != 1 {
		t.Fatalf("coverage = %q pods = %d, want current_pods/1", resp.Coverage, resp.Pods)
	}
	queries := f.queries(t)
	last := queries[len(queries)-1]
	if !strings.Contains(last, `pod=~'^(web-7f6-a)$'`) || strings.Contains(last, "web-.*") {
		t.Fatalf("query should name the owned pod exactly: %s", last)
	}
}

// A workload whose pods the cache cannot list must not be charted from a
// name pattern; the handler says the cache could not answer.
func TestResourceMetricsRefusesWhenOwnershipCannotBeResolved(t *testing.T) {
	SetAuthGate(nil)
	f := setupAuthFakeProm(t)
	h := metricsRouter()

	f.queryBody = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	rec := getMetrics(t, h, "/prometheus/resources/Deployment/alpha/ghost?category=cpu")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with an empty result; body=%s", rec.Code, rec.Body.String())
	}
	var resp ResourceMetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Coverage != OwnerCoverageNone {
		t.Fatalf("coverage = %q, want none for a workload with no pods", resp.Coverage)
	}
}
