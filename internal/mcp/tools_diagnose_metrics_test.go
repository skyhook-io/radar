package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
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

// metricsMatrixForRequest answers a range query the way Prometheus does:
// floor((end-start)/step)+1 samples at the requested step.
func metricsMatrixForRequest(value string) func(params url.Values) string {
	return func(params url.Values) string {
		start, _ := strconv.ParseFloat(params.Get("start"), 64)
		end, _ := strconv.ParseFloat(params.Get("end"), 64)
		step, _ := strconv.ParseFloat(params.Get("step"), 64)
		if step <= 0 || end <= start {
			return metricsMatrixBody(1, value)
		}
		return metricsMatrixBody(int((end-start)/step)+1, value)
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
	isController := true
	rsOwner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "fleet", Controller: &isController}
	podOwner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "fleet-rs", Controller: &isController}
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: "fleet-rs", Namespace: ns, Labels: selector, OwnerReferences: []metav1.OwnerReference{rsOwner}},
			Spec:       appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: selector}},
		},
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
			ObjectMeta: metav1.ObjectMeta{Name: fleetPodName(i), Namespace: ns, Labels: selector, OwnerReferences: []metav1.OwnerReference{podOwner}},
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
	f.rangeBodyFunc = metricsMatrixForRequest("-0.0000345678912345678")
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
	f.rangeBodyFunc = metricsMatrixForRequest("123456789.123")
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
	f.rangeBody = metricsMatrixBody(600, "123456789.123")
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

// setupFakeCacheWithOwnershipHistory installs the Deployment → ReplicaSet →
// Pod chain the vitals resolve membership through.
func TestHandleDiagnoseMetricsReportsPodCoverage(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	f.rangeBodyFunc = metricsMatrixForRequest("1")
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	// No kube-state-metrics ownership series: the charts cover the pods
	// running now and say so.
	got, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m == nil {
		t.Fatal("diagnose omitted metrics")
	}
	if m.Coverage != prometheus.OwnerCoverageCurrentPods || m.ObservedPods != 0 {
		t.Fatalf("coverage = %q observed = %d, want current_pods/0", m.Coverage, m.ObservedPods)
	}
	if m.Pods != 1 {
		t.Fatalf("pods = %d, want the single owned pod", m.Pods)
	}
	for _, s := range m.Series {
		if !strings.Contains(s.Query, `pod=~'^(cart-abc123)$'`) {
			t.Errorf("%s query should name the owned pod set: %s", s.Category, s.Query)
		}
		if strings.Contains(s.Query, "cart-.*") {
			t.Errorf("%s query uses a name prefix: %s", s.Category, s.Query)
		}
	}
	var bundle struct {
		PodNames          []string `json:"podNames"`
		PodNamesTruncated bool     `json:"podNamesTruncated"`
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.PodNames) != 1 || bundle.PodNames[0] != "cart-abc123" || bundle.PodNamesTruncated {
		t.Fatalf("bundle podNames = %v truncated=%v, want the owned pod", bundle.PodNames, bundle.PodNamesTruncated)
	}
}

func TestHandleDiagnoseMetricsJoinsOwnershipHistoryWhenKubeStateMetricsHasIt(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	f := setupFakeProm(t)
	f.rangeBodyFunc = metricsMatrixForRequest("1")
	// kube-state-metrics attributed three pods to the Deployment across the
	// window, two of them already replaced.
	f.queryBodyFunc = func(params url.Values) string {
		if strings.Contains(params.Get("query"), "kube_pod_owner") {
			return `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"3"]}]}}`
		}
		return ""
	}
	ctx := withClusterAdmin(t, "admin")
	grantDiagnoseDeploymentRead(t, "admin")

	_, m := diagnoseMetricsResult(t, ctx, testDiagnoseInput("deployment", "alpha", "cart"))
	if m == nil {
		t.Fatal("diagnose omitted metrics")
	}
	if m.Coverage != prometheus.OwnerCoverageKSMHistory || m.ObservedPods != 3 {
		t.Fatalf("coverage = %q observed = %d, want ksm_history/3", m.Coverage, m.ObservedPods)
	}
	if m.Pods != 1 {
		t.Fatalf("pods = %d, want the one pod running now", m.Pods)
	}
	for _, s := range m.Series {
		if !strings.Contains(s.Query, "kube_replicaset_owner{namespace='alpha',owner_kind='Deployment',owner_name='cart'") ||
			!strings.Contains(s.Query, "* on (namespace,pod) group_left()") {
			t.Errorf("%s query should join through the ReplicaSet edge: %s", s.Category, s.Query)
		}
		if strings.Contains(s.Query, "cart-abc123") {
			t.Errorf("%s query pinned the current pod instead of the owner: %s", s.Category, s.Query)
		}
	}
}
