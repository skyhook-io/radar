package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/prom"
)

// metricsMatrixBody renders a one-series matrix with the given number of
// samples, all carrying value, so a test can control the serialized size.
func metricsMatrixBody(points int, value string) string {
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[`)
	for i := 0; i < points; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `[%d,"%s"]`, 1700000000+int64(i)*60, value)
	}
	b.WriteString(`]}]}}`)
	return b.String()
}

// metricsMatrixBodyAt is metricsMatrixBody with a per-sample value, so a test
// can choose whether its fixture is flat. A flat fixture now compresses to two
// points, which is the right shape for the compression tests and the wrong one
// for anything measuring point counts or serialized size.
func metricsMatrixBodyAt(points int, valueAt func(i int) string) string {
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[`)
	for i := 0; i < points; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `[%d,"%s"]`, 1700000000+int64(i)*60, valueAt(i))
	}
	b.WriteString(`]}]}}`)
	return b.String()
}

// metricsVaryingValue scales `base` by a per-sample factor so consecutive
// samples differ within four significant digits and therefore survive both the
// rounding and the constant-run compression. Walking a trailing digit is not
// enough: rounding flattens movement below the reported precision, which is
// the point of comparing after it.
func metricsVaryingValue(base string) func(i int) string {
	v, err := strconv.ParseFloat(base, 64)
	if err != nil {
		panic("metricsVaryingValue: " + base)
	}
	return func(i int) string {
		return strconv.FormatFloat(v*(1+float64(i%97)/100), 'g', 17, 64)
	}
}

// metricsMatrixForRequest answers a range query the way Prometheus does:
// floor((end-start)/step)+1 samples at the requested step.
func metricsMatrixForRequest(value string) func(params url.Values) string {
	return metricsMatrixForRequestAt(func(int) string { return value })
}

func metricsMatrixForRequestAt(valueAt func(i int) string) func(params url.Values) string {
	return func(params url.Values) string {
		start, _ := strconv.ParseFloat(params.Get("start"), 64)
		end, _ := strconv.ParseFloat(params.Get("end"), 64)
		step, _ := strconv.ParseFloat(params.Get("step"), 64)
		if step <= 0 || end <= start {
			return metricsMatrixBodyAt(1, valueAt)
		}
		return metricsMatrixBodyAt(int((end-start)/step)+1, valueAt)
	}
}

// fleetPodName is a 63-character pod name, the longest Kubernetes allows,
// so the pod-set cap and the byte backstop see the worst-case query length.
func fleetPodName(i int) string {
	return fmt.Sprintf("fleet-%s-%05d", strings.Repeat("a", 51), i)
}

// setupFakeCacheWithPodFleet installs a Deployment in namespace alpha with
// n Running pods carrying the longest names Kubernetes allows.
func setupFakeCacheWithPodFleet(t *testing.T, n int) {
	t.Helper()
	const ns = "alpha"
	selector := map[string]string{"app": "fleet"}
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "fleet", Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: selector},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: selector},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "fleet"}}},
				},
			},
		},
	}
	for i := 0; i < n; i++ {
		objs = append(objs, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fleetPodName(i), Namespace: ns, Labels: selector},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "fleet"}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		})
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(func() {
		k8s.ResetTestState()
		getPermCache().Invalidate()
	})
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
}

func diagnoseMetricsResult(t *testing.T, ctx context.Context, input diagnoseInput) (map[string]json.RawMessage, *diagnoseMetrics) {
	t.Helper()
	result, _, err := handleDiagnose(ctx, nil, input)
	if err != nil {
		t.Fatalf("handleDiagnose: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(extractText(t, result)), &got); err != nil {
		t.Fatal(err)
	}
	raw, ok := got["metrics"]
	if !ok {
		return got, nil
	}
	var m diagnoseMetrics
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode metrics: %v\n%s", err, raw)
	}
	return got, &m
}

func grantDiagnoseDeploymentRead(t *testing.T, username string) {
	t.Helper()
	perms := getPermCache().Get(username, nil)
	perms.SetCanI("get", "apps", "deployments", "alpha", true)
	perms.SetCanI("list", "apps", "deployments", "alpha", true)
	perms.SetCanI("list", "", "configmaps", "alpha", true)
}

func TestHandleDiagnoseMetricsCapturesThreeSeriesOverExactPodSet(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	f.rangeBodyFunc = metricsMatrixForRequestAt(metricsVaryingValue("-0.0000345678912345678"))
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	got, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m == nil {
		t.Fatal("diagnose omitted metrics with Prometheus connected")
	}
	if m.Error != "" {
		t.Fatalf("metrics.error = %q, want none", m.Error)
	}
	if m.Pods != 1 || m.Partial || m.OmittedPods != 0 {
		t.Fatalf("pods=%d partial=%v omitted=%d, want the single resolved pod", m.Pods, m.Partial, m.OmittedPods)
	}
	wantCategories := []string{"cpu", "memory", "restarts"}
	if len(m.Series) != len(wantCategories) {
		t.Fatalf("series = %d entries, want %d", len(m.Series), len(wantCategories))
	}
	for i, s := range m.Series {
		if s.Category != wantCategories[i] {
			t.Errorf("series[%d].category = %q, want %q", i, s.Category, wantCategories[i])
		}
		if s.Unit == "" {
			t.Errorf("series[%d] has no unit", i)
		}
		if !strings.Contains(s.Query, `pod=~'^(cart-abc123)$'`) || !strings.Contains(s.Query, `namespace='alpha'`) {
			t.Errorf("series[%d].query = %q, want the exact pod set in namespace alpha", i, s.Query)
		}
		if strings.Contains(s.Query, "cart-.*") {
			t.Errorf("series[%d].query = %q uses the workload prefix pattern", i, s.Query)
		}
		if len(s.Series) != 1 {
			t.Fatalf("series[%d] returned %d series, want one", i, len(s.Series))
		}
		// One pod leaves the byte budget room for close to a point a minute.
		if points := len(s.Series[0].DataPoints); points > diagnoseMetricsMaxPoints || points < 50 {
			t.Errorf("series[%d] has %d points, want between 50 and %d", i, points, diagnoseMetricsMaxPoints)
		}
		if v := s.Series[0].DataPoints[0].Value; v != -0.00003457 {
			t.Errorf("series[%d] value = %v, want rounding to four significant digits", i, v)
		}
	}
	step, err := time.ParseDuration(m.Window.Step)
	if err != nil {
		t.Fatalf("window.step = %q: %v", m.Window.Step, err)
	}
	if points := int(time.Hour/step) + 1; points > diagnoseMetricsMaxPoints {
		t.Errorf("window.step = %s yields %d points over 1h, want at most %d", step, points, diagnoseMetricsMaxPoints)
	}
	start, err := time.Parse(time.RFC3339, m.Window.Start)
	if err != nil {
		t.Fatal(err)
	}
	end, err := time.Parse(time.RFC3339, m.Window.End)
	if err != nil {
		t.Fatal(err)
	}
	if d := end.Sub(start); d != time.Hour {
		t.Errorf("window = %s, want the 1h default", d)
	}
	if len(f.rangeParams) != 3 {
		t.Fatalf("Prometheus received %d range queries, want 3", len(f.rangeParams))
	}
	for _, params := range f.rangeParams {
		if params.Get("step") != strconv.Itoa(int(step.Seconds())) {
			t.Errorf("range step = %q, want the reported %s", params.Get("step"), step)
		}
	}
	if size := metricsBytesWithoutQueries(t, m); size > diagnoseMetricsMaxBytes {
		t.Errorf("samples and envelope = %d bytes, exceeds the %d byte budget with three full series", size, diagnoseMetricsMaxBytes)
	}
	if size := len(got["metrics"]); size > diagnoseMetricsHardMaxBytes {
		t.Errorf("serialized metrics = %d bytes, exceeds the %d byte backstop", size, diagnoseMetricsHardMaxBytes)
	}
}

// metricsBytesWithoutQueries measures what the sample budget governs: the
// field with its query strings blanked.
func metricsBytesWithoutQueries(t *testing.T, m *diagnoseMetrics) int {
	t.Helper()
	probe := *m
	probe.Series = make([]diagnoseMetricSeries, len(m.Series))
	copy(probe.Series, m.Series)
	for i := range probe.Series {
		probe.Series[i].Query = ""
	}
	encoded, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	return len(encoded)
}

func TestHandleDiagnoseMetricsHonorsSinceAndPodKind(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	ctx := withClusterAdmin(t, "admin")
	getPermCache().Get("admin", nil).SetCanI("get", "", "pods", "alpha", true)

	input := testDiagnoseInput("pod", "alpha", "cart-abc123")
	input.Since = "30m"
	_, m := diagnoseMetricsResult(t, ctx, input)
	if m == nil {
		t.Fatal("diagnose omitted metrics for kind=pod")
	}
	step, err := time.ParseDuration(m.Window.Step)
	if err != nil {
		t.Fatalf("window.step = %q: %v", m.Window.Step, err)
	}
	if points := int(30*time.Minute/step) + 1; points > diagnoseMetricsMaxPoints || points < 50 {
		t.Errorf("window.step = %s yields %d points over 30m, want between 50 and %d", step, points, diagnoseMetricsMaxPoints)
	}
	params := f.lastRangeParams(t)
	if !strings.Contains(params.Get("query"), `pod=~'^(cart-abc123)$'`) {
		t.Errorf("query = %q, want the pod itself", params.Get("query"))
	}
	startUnix, endUnix := params.Get("start"), params.Get("end")
	if startUnix == "" || endUnix == "" {
		t.Fatalf("range query lacks start/end: %v", params)
	}
}

func TestHandleDiagnoseMetricsCapsPodSetByName(t *testing.T) {
	const fleet = 55
	setupFakeCacheWithPodFleet(t, fleet)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	f.rangeBodyFunc = metricsMatrixForRequestAt(metricsVaryingValue("123456789.123"))
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	got, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "fleet"))
	if m == nil {
		t.Fatal("diagnose omitted metrics")
	}
	if !m.Partial || m.Pods != diagnoseMetricsMaxPods || m.OmittedPods != fleet-diagnoseMetricsMaxPods {
		t.Fatalf("partial=%v pods=%d omitted=%d, want partial over %d of %d", m.Partial, m.Pods, m.OmittedPods, diagnoseMetricsMaxPods, fleet)
	}
	if m.Error != "" {
		t.Fatalf("metrics.error = %q, want none for a capped set", m.Error)
	}
	if len(m.Series) != 3 {
		t.Fatalf("series = %d entries, want 3", len(m.Series))
	}
	size := len(got["metrics"])
	if size > diagnoseMetricsHardMaxBytes {
		t.Errorf("serialized metrics = %d bytes, exceeds the %d byte backstop", size, diagnoseMetricsHardMaxBytes)
	}
	// Fifty 63-character names make the queries alone outgrow the sample
	// budget; they sit outside it, so the chart keeps its resolution.
	if size <= diagnoseMetricsMaxBytes {
		t.Fatalf("serialized metrics = %d bytes, the fixture no longer exercises queries beyond the %d byte sample budget", size, diagnoseMetricsMaxBytes)
	}
	if points := len(m.Series[0].Series[0].DataPoints); points > diagnoseMetricsMaxPoints || points < 50 {
		t.Errorf("series[0] has %d points, want between 50 and %d regardless of pod-name length", points, diagnoseMetricsMaxPoints)
	}
	if size := metricsBytesWithoutQueries(t, m); size > diagnoseMetricsMaxBytes {
		t.Errorf("samples and envelope = %d bytes, exceeds the %d byte budget", size, diagnoseMetricsMaxBytes)
	}
	for _, s := range m.Series {
		if !strings.Contains(s.Query, "("+fleetPodName(0)+"|") || !strings.Contains(s.Query, "|"+fleetPodName(49)+")") {
			t.Errorf("%s query does not cover the first %d pods by name: %s", s.Category, diagnoseMetricsMaxPods, s.Query)
		}
		if strings.Contains(s.Query, fleetPodName(50)) {
			t.Errorf("%s query names a pod past the cap: %s", s.Category, s.Query)
		}
	}
}

func TestHandleDiagnoseMetricsOmittedWhenPrometheusAbsent(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	prometheus.Initialize(nil, nil, "test-ctx")
	t.Cleanup(func() {
		prometheus.Reset()
		prometheus.Initialize(nil, nil, "")
	})
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	got, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m != nil {
		t.Fatalf("metrics = %+v, want the field omitted when no Prometheus exists", m)
	}
	if _, ok := got["logCoverage"]; !ok {
		t.Fatal("the rest of the bundle went missing")
	}
}

func TestHandleDiagnoseMetricsReportsUnreachablePrometheus(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	prometheus.Initialize(nil, nil, "test-ctx")
	prometheus.SetManualURL("http://127.0.0.1:1")
	t.Cleanup(func() {
		prometheus.Reset()
		prometheus.Initialize(nil, nil, "")
	})
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	_, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m == nil {
		t.Fatal("diagnose omitted metrics for a configured but unreachable Prometheus")
	}
	if !strings.HasPrefix(m.Error, "prometheus unreachable: ") {
		t.Fatalf("metrics.error = %q, want a prometheus unreachable explanation", m.Error)
	}
	if len(m.Series) != 0 || m.Pods != 1 {
		t.Fatalf("series=%d pods=%d, want no series and the pod count", len(m.Series), m.Pods)
	}
}

func TestHandleDiagnoseMetricsOmittedWhenWorkloadReadDenied(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	ctx := withClusterAdmin(t, "scoped")
	grantDiagnoseDeploymentRead(t, "scoped")
	getPermCache().Get("scoped", nil).SetCanI("get", "apps", "deployments", "alpha", false)

	result, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("deployment", "alpha", "cart"))
	if err != nil {
		t.Fatalf("handleDiagnose: %v", err)
	}
	text := extractText(t, result)
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["metrics"]; ok {
		t.Fatal("metrics present although the caller cannot get the Deployment")
	}
	if strings.Contains(strings.ToLower(text), "prometheus") {
		t.Fatal("a denied read leaked Prometheus state into the response")
	}
	if f.probeCalls != 0 || len(f.rangeParams) != 0 {
		t.Fatalf("Prometheus was contacted (%d probes, %d range queries) for a denied read", f.probeCalls, len(f.rangeParams))
	}
}

func TestHandleDiagnoseMetricsDropsSeriesOnTimeBudget(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	f.rangeDelay = 500 * time.Millisecond
	prev := diagnoseMetricsBudget
	diagnoseMetricsBudget = 200 * time.Millisecond
	t.Cleanup(func() { diagnoseMetricsBudget = prev })
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	started := time.Now()
	got, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if elapsed := time.Since(started); elapsed >= f.rangeDelay {
		t.Errorf("diagnose took %s, want it back before the %s Prometheus answer", elapsed, f.rangeDelay)
	}
	if m == nil {
		t.Fatal("diagnose omitted metrics on a budget breach")
	}
	if len(m.Series) != 0 || !strings.Contains(m.Error, "budget exceeded") {
		t.Fatalf("series=%d error=%q, want no series and a budget explanation", len(m.Series), m.Error)
	}
	if _, ok := got["logCoverage"]; !ok {
		t.Fatal("the rest of the bundle went missing")
	}
}

func TestHandleDiagnoseMetricsDropsSeriesOnSizeBudget(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	f.rangeBody = metricsMatrixBodyAt(600, metricsVaryingValue("123456789.123"))
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	got, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m == nil {
		t.Fatal("diagnose omitted metrics on a size breach")
	}
	if len(m.Series) != 0 || !strings.Contains(m.Error, "byte backstop") {
		t.Fatalf("series=%d error=%q, want no series and a backstop explanation", len(m.Series), m.Error)
	}
	if size := len(got["metrics"]); size > diagnoseMetricsMaxBytes {
		t.Errorf("serialized metrics = %d bytes after dropping series, still over budget", size)
	}
}

func TestHandleDiagnoseMetricsKeepsOtherSeriesWhenOneQueryFails(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	f.rangeStatus = 422
	f.rangeBody = `{"status":"error","errorType":"execution","error":"boom"}`
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	_, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m == nil {
		t.Fatal("diagnose omitted metrics when queries failed")
	}
	if len(m.Series) != 0 {
		t.Fatalf("series=%d, want none when every query failed", len(m.Series))
	}
	for _, cat := range []string{"cpu:", "memory:", "restarts:"} {
		if !strings.Contains(m.Error, cat) {
			t.Errorf("metrics.error = %q, want it to name %s", m.Error, cat)
		}
	}
}

func TestHandleDiagnoseMetricsKeepsOtherSeriesWhenOneCategoryFails(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	f.rangeBodyFunc = func(params url.Values) string {
		if strings.Contains(params.Get("query"), "container_cpu_usage_seconds_total") {
			return `{"status":"error","errorType":"execution","error":"cpu exploded"}`
		}
		return metricsMatrixForRequest("1")(params)
	}
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	_, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m == nil {
		t.Fatal("diagnose omitted metrics when one query failed")
	}
	var categories []string
	for _, s := range m.Series {
		categories = append(categories, s.Category)
	}
	if strings.Join(categories, ",") != "memory,restarts" {
		t.Fatalf("series categories = %v, want the two that answered", categories)
	}
	if !strings.HasPrefix(m.Error, "cpu: ") || !strings.Contains(m.Error, "cpu exploded") {
		t.Fatalf("metrics.error = %q, want the failed category named with its cause", m.Error)
	}
}

func TestHandleDiagnoseMetricsReportsFailedContainerFilterFallback(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	const emptyMatrix = `{"status":"success","data":{"resultType":"matrix","result":[]}}`
	f.rangeBodyFunc = func(params url.Values) string {
		q := params.Get("query")
		switch {
		case !strings.Contains(q, "container_cpu_usage_seconds_total"):
			return metricsMatrixForRequest("1")(params)
		case strings.Contains(q, "container!=''"):
			return emptyMatrix
		default:
			return `{"status":"error","errorType":"execution","error":"fallback exploded"}`
		}
	}
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	_, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m == nil {
		t.Fatal("diagnose omitted metrics")
	}
	for _, s := range m.Series {
		if s.Category == "cpu" {
			t.Fatalf("cpu reported as an empty window although its fallback query failed: %+v", s)
		}
	}
	if len(m.Series) != 2 || !strings.HasPrefix(m.Error, "cpu: ") || !strings.Contains(m.Error, "fallback exploded") {
		t.Fatalf("series=%d error=%q, want cpu dropped and named with the fallback failure", len(m.Series), m.Error)
	}
}

func TestHandleDiagnoseMetricsGateFailsClosedWithoutDecision(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	ctx := withClusterAdmin(t, "undecided")
	getPermCache().Get("undecided", nil).SetCanI("list", "apps", "deployments", "alpha", true)
	getPermCache().Get("undecided", nil).SetCanI("list", "", "configmaps", "alpha", true)
	// No cached decision for get deployments: the gate must ask an apiserver
	// the test does not have, so it denies, and Prometheus is never touched.
	_, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m != nil {
		t.Fatalf("metrics = %+v, want the field omitted when the gate cannot answer", m)
	}
	if f.probeCalls != 0 || len(f.rangeParams) != 0 {
		t.Fatalf("Prometheus was contacted (%d probes, %d range queries) before the gate answered", f.probeCalls, len(f.rangeParams))
	}
}

func TestBoundDiagnoseMetricsError(t *testing.T) {
	long := strings.Repeat("x", diagnoseMetricsMaxErrorBytes*2)
	got := boundDiagnoseMetricsError(long)
	if len(got) != diagnoseMetricsMaxErrorBytes || !strings.HasSuffix(got, "…") {
		t.Fatalf("bounded error is %d bytes ending %q, want at most %d with an ellipsis", len(got), got[len(got)-3:], diagnoseMetricsMaxErrorBytes)
	}
	if short := boundDiagnoseMetricsError("fine"); short != "fine" {
		t.Errorf("short error changed to %q", short)
	}
}

func TestRoundSignificant(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{0, 0},
		{0.00345678912345678, 0.003457},
		{123456789.123, 123500000},
		{1000, 1000},
		{-2.71828, -2.718},
		{7, 7},
	}
	for _, c := range cases {
		if got := roundSignificant(c.in, 4); got != c.want {
			t.Errorf("roundSignificant(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	if got := roundSignificant(math.NaN(), 4); !math.IsNaN(got) {
		t.Errorf("NaN should pass through, got %v", got)
	}
	if b, _ := json.Marshal(roundSignificant(123456789.123, 4)); string(b) != "123500000" {
		t.Errorf("rounded value serializes as %s, want 123500000", b)
	}
}

// A flat stretch is worth two points and a count, not sixty. What the field
// costs should follow what it says, not how long the window is.
func TestCollapseConstantRuns(t *testing.T) {
	at := func(ts int64, v float64) prom.DataPoint { return prom.DataPoint{Timestamp: ts, Value: v} }
	flat := func(n int, start int64, step int64, v float64) []prom.DataPoint {
		out := make([]prom.DataPoint, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, at(start+int64(i)*step, v))
		}
		return out
	}

	t.Run("a constant run keeps only its ends", func(t *testing.T) {
		got := collapseConstantRuns(flat(56, 1000, 60, 512000))
		want := []prom.DataPoint{at(1000, 512000), at(1000+55*60, 512000)}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("a step change keeps both boundaries", func(t *testing.T) {
		in := append(flat(4, 0, 60, 1), flat(4, 240, 60, 2)...)
		got := collapseConstantRuns(in)
		want := []prom.DataPoint{at(0, 1), at(180, 1), at(240, 2), at(420, 2)}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("equal values across a gap are not one run", func(t *testing.T) {
		// Prometheus returns nothing for an evaluation with no data, so these
		// two stretches are not adjacent. Merging them would erase the only
		// evidence the gap happened.
		in := append(flat(5, 0, 60, 7), flat(5, 900, 60, 7)...)
		got := collapseConstantRuns(in)
		want := []prom.DataPoint{at(0, 7), at(240, 7), at(900, 7), at(1140, 7)}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("gap boundaries lost: got %v, want %v", got, want)
		}
	})

	t.Run("a varying series is untouched", func(t *testing.T) {
		in := []prom.DataPoint{at(0, 1), at(60, 2), at(120, 3), at(180, 4)}
		got := collapseConstantRuns(in)
		if !reflect.DeepEqual(got, in) {
			t.Fatalf("got %v, want %v", got, in)
		}
	})

	t.Run("non-finite values never collapse", func(t *testing.T) {
		// NaN is unequal to itself, but equal infinities would otherwise look
		// like a flat stretch. Both serialize as null and mark a gap.
		for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
			in := flat(5, 0, 60, v)
			if got := collapseConstantRuns(in); len(got) != len(in) {
				t.Fatalf("value %v: collapsed %d points to %d", v, len(in), len(got))
			}
		}
	})

	t.Run("short inputs pass through", func(t *testing.T) {
		for _, n := range []int{0, 1, 2} {
			in := flat(n, 0, 60, 3)
			if got := collapseConstantRuns(in); len(got) != n {
				t.Fatalf("%d points became %d", n, len(got))
			}
		}
	})
}

// The compression has to pay for itself on the case that motivates it — a
// workload whose pods report a flat line — and it must not cost the reader the
// knowledge that the flat line was actually observed.
func TestHandleDiagnoseMetricsCompressesFlatSeriesAndKeepsTheObservedCount(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	f.rangeBodyFunc = metricsMatrixForRequest("512000")
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	_, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m == nil {
		t.Fatal("diagnose omitted metrics with Prometheus connected")
	}
	for _, s := range m.Series {
		if len(s.Series) != 1 {
			t.Fatalf("%s: %d series, want one", s.Category, len(s.Series))
		}
		if points := len(s.Series[0].DataPoints); points != 2 {
			t.Fatalf("%s: a flat series kept %d points, want 2", s.Category, points)
		}
		if s.ObservedSamples < 50 {
			t.Fatalf("%s: observedSamples = %d, want the count actually returned", s.Category, s.ObservedSamples)
		}
		for _, dp := range s.Series[0].DataPoints {
			if dp.Value != 512000 {
				t.Fatalf("%s: endpoint value %v, want the observed 512000", s.Category, dp.Value)
			}
		}
	}
	flat, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(flat) > 2*1024 {
		t.Fatalf("a flat three-category field serialized to %d bytes, want it well under the %d byte sample budget", len(flat), diagnoseMetricsMaxBytes)
	}
}

// Whether diagnose collects vitals at all is a deployment decision, so it has
// to be switchable without the rest of the bundle noticing.
func TestDiagnoseMetricsCanBeSwitchedOff(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	f.rangeBodyFunc = metricsMatrixForRequest("512000")
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	DiagnoseMetricsEnabled = false
	t.Cleanup(func() { DiagnoseMetricsEnabled = true })

	got, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m != nil {
		t.Fatalf("vitals are off but diagnose returned a metrics field: %+v", m)
	}
	// Everything else the bundle carries is unaffected.
	for _, key := range []string{"resource", "pods", "resourceContext"} {
		if _, ok := got[key]; !ok {
			t.Errorf("bundle lost %q when vitals were switched off", key)
		}
	}
}
