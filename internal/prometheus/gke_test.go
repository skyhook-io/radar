package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

const fixtureIssuer = "https://container.googleapis.com/v1/projects/demo-project/locations/us-east1-b/clusters/demo"

func gkeFixture(t *testing.T, issuer string, metricCount int) *Client {
	t.Helper()
	return gkeFixtureServices(t, issuer, metricCount, nil)
}

func gkeFixtureServices(t *testing.T, issuer string, metricCount int, services func(http.ResponseWriter, *http.Request, corev1.Service) bool) *Client {
	t.Helper()
	svc := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "gmp-public", Labels: map[string]string{"app.kubernetes.io/name": "prometheus"}},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "frontend"}, Ports: []corev1.ServicePort{{Name: "web", Port: 9090, TargetPort: intstr.FromString("http")}}},
	}
	pod := corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: gmpFrontendImage + ":v0.14", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 9090}}}}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet && !(r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/proxy/api/v1/")) {
			t.Errorf("unexpected Kubernetes or proxy method %s", r.Method)
		}
		if services != nil && services(w, r, svc) {
			return
		}
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]string{"issuer": issuer})
		case "/api/v1/services":
			json.NewEncoder(w).Encode(corev1.ServiceList{Items: []corev1.Service{svc}})
		case "/api/v1/namespaces/gmp-public/pods":
			if r.URL.Query().Get("labelSelector") != "app=frontend" {
				t.Errorf("unexpected selector %s", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode(corev1.PodList{Items: []corev1.Pod{pod}})
		default:
			if !strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/gmp-public/services/frontend:web/proxy/api/v1/") {
				http.NotFound(w, r)
				return
			}
			query := r.FormValue("query")
			if query == "up" {
				fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
				return
			}
			if !strings.Contains(query, "kubernetes_io:") {
				http.Error(w, `{"status":"error","error":"conventional metric identity unavailable"}`, http.StatusBadRequest)
				return
			}
			for _, m := range []string{`project_id="demo-project"`, `location="us-east1-b"`, `cluster_name="demo"`, `monitored_resource="k8s_`} {
				if !strings.Contains(query, m) {
					t.Errorf("native query missing %s: %s", m, query)
				}
			}
			if strings.HasPrefix(query, "count(") {
				if metricCount < 0 {
					fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
					return
				}
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"%d"]}]}}`, metricCount)
				return
			}
			if strings.Contains(query, "pod_volume_") {
				if !strings.Contains(query, `persistentvolumeclaim_name="data"`) || !strings.Contains(query, `persistentvolumeclaim_namespace="default"`) || !strings.HasPrefix(query, "max(") {
					t.Errorf("PVC query: %s", query)
				}
				n := 256
				if strings.Contains(query, "total_bytes") {
					n = 1024
				}
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"%d"]}]}}`, n)
				return
			}
			start, _ := strconv.ParseInt(r.FormValue("start"), 10, 64)
			end, _ := strconv.ParseInt(r.FormValue("end"), 10, 64)
			step, _ := strconv.ParseInt(r.FormValue("step"), 10, 64)
			if step <= 0 || end-start > int64((15*24*time.Hour).Seconds()) {
				http.Error(w, "bad range", 400)
				return
			}
			var rows []any
			for i, name := range []string{"nginx-abc-111", "nginx-abc-222"} {
				var values [][]any
				for ts := start; ts <= end; ts += step {
					v := 0.08 + 0.04*float64(i) + 0.025*math.Sin(float64(ts-start)/510)
					if strings.Contains(query, "memory_used_bytes") {
						v = (110 + float64(i)*15 + 5*math.Sin(float64(ts-start)/520)) * 1048576
					}
					if strings.Contains(query, "pod_network_") {
						v *= 2400000
					}
					if strings.Contains(query, "container_restart_count") {
						v = 0
						if strings.Contains(query, "last_over_time") && ts%3600 >= 15*60 {
							v = 3
						}
					}
					values = append(values, []any{ts, strconv.FormatFloat(v, 'f', 6, 64)})
				}
				rows = append(rows, map[string]any{"metric": map[string]string{"pod": name, "namespace": "default"}, "values": values})
			}
			json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": rows}})
		}
	}))
	t.Cleanup(srv.Close)
	config := &rest.Config{Host: srv.URL, QPS: 1000, Burst: 100, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	k, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{k8sClient: k, k8sConfig: config, httpClient: srv.Client()}
}

func TestGKEFrontendChartDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, issuer string
		count        int
		labels       map[string]string
		regular      bool
		want         bool
	}{
		{"verified", fixtureIssuer, 1, nil, false, true},
		{"cluster alias", fixtureIssuer, 1, map[string]string{"cluster": "demo"}, false, true},
		{"cluster disagreement", fixtureIssuer, 1, map[string]string{"cluster": "other"}, false, false},
		{"conflicting aliases", fixtureIssuer, 1, map[string]string{"cluster": "demo", "cluster_name": "other"}, false, false},
		{"untrusted issuer", "https://example.org", 1, nil, false, false},
		{"complete configured scope", "https://example.org", 1, map[string]string{"project_id": "demo-project", "location": "us-east1-b", "cluster": "demo"}, false, true},
		{"partial assertion", "https://example.org", 1, map[string]string{"cluster": "demo"}, false, false},
		{"empty probe", fixtureIssuer, -1, nil, false, false},
		{"zero probe", fixtureIssuer, 0, nil, false, false},
		{"supplement regular store", fixtureIssuer, 1, nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := gkeFixture(t, tc.issuer, tc.count)
			if tc.labels != nil {
				c.workloadScope = &prom.WorkloadMetricsScope{ClusterLabels: tc.labels}
			}
			if tc.regular {
				c.baseURL = "http://regular.invalid"
			}
			c.findGKEChartStore(t.Context(), 0, ErrPrometheusNotFound)
			g := c.gkeChartStore()
			if (g != nil) != tc.want {
				t.Fatalf("discovered = %v, want %v", g != nil, tc.want)
			}
			if g != nil && strings.Contains(g.matchers, `cluster="`) {
				t.Fatalf("GMP selector also requires unsupported conventional cluster alias: %s", g.matchers)
			}
			if !tc.regular {
				if p, _ := c.PromSnapshot(); p != nil || c.GetStatus().Connected {
					t.Fatal("discovery exposed project store for raw queries")
				}
			}
		})
	}
}

func TestGKEFrontendTarget(t *testing.T) {
	s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "tools", Name: "metrics", Labels: map[string]string{"app": "frontend"}}, Spec: corev1.ServiceSpec{Selector: map[string]string{"tier": "metrics"}, Ports: []corev1.ServicePort{{Name: "admin", Port: 8080}, {Name: "web", Port: 80, TargetPort: intstr.FromString("http")}}}}
	p := corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: gmpFrontendImage + "@sha256:123", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 9090}}}, {Image: "example/sidecar", Ports: []corev1.ContainerPort{{Name: "admin", ContainerPort: 8080}}}}}}
	if gkeFrontendCandidate(s) {
		t.Fatal("trusted a tenant namespace on the strength of labels")
	}
	s.Namespace, s.Name = "gke-gmp-system", "gmp-frontend"
	if !gkeFrontendCandidate(s) {
		t.Fatal("missed the managed frontend")
	}
	if port, ok := gkeFrontendPort(s, []corev1.Pod{p}); !ok || port.Name != "web" {
		t.Fatalf("selected %v %v", port, ok)
	}
	s.Spec.Ports[1].TargetPort = intstr.FromString("admin")
	if _, ok := gkeFrontendPort(s, []corev1.Pod{p}); ok {
		t.Fatal("accepted sidecar port")
	}
	s.Spec.Ports[1].TargetPort = intstr.FromString("http")
	wrong := p.DeepCopy()
	wrong.Spec.Containers[0].Image = gmpFrontendImage + "-unrelated"
	if _, ok := gkeFrontendPort(s, []corev1.Pod{p, *wrong}); ok {
		t.Fatal("accepted mixed backends")
	}
	s.Spec.Type = corev1.ServiceTypeExternalName
	if gkeFrontendCandidate(s) {
		t.Fatal("accepted external service")
	}
}

func TestGKEMetricsCharts(t *testing.T) {
	c := gkeFixture(t, fixtureIssuer, 1)
	c.findGKEChartStore(t.Context(), 0, ErrPrometheusNotFound)
	g := c.gkeChartStore()
	if g == nil {
		t.Fatal("fixture frontend not discovered")
	}
	end := time.Unix(1789376400, 0)
	start := end.Add(-time.Hour)
	scope := PodScope{Kind: "Deployment", Namespace: "default", Name: "nginx", CurrentTotal: 2, CurrentPods: []string{"nginx-abc-111", "nginx-abc-222"}, Created: map[string]time.Time{"nginx-abc-111": start.Add(-time.Hour), "nginx-abc-222": start.Add(10 * time.Minute)}}
	resp := g.workload(context.Background(), scope, start, end, time.Minute)
	for _, k := range []string{"cpu", "memory"} {
		panel := resp.Panels[k]
		if panel.State != "available" || len(panel.Series) != 2 || panel.Series[1].DataPoints[0].Timestamp <= start.Add(15*time.Minute).Unix() {
			t.Fatalf("%s didn't respect reused Pod lifetime: %+v", k, panel)
		}
	}
	restarts, err := g.chart(t.Context(), scope.Kind, scope.Namespace, scope.Name, prom.CategoryRestarts, &scope, start, end, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	observedJump := false
	for _, series := range restarts.Result.Series {
		if series.Labels["pod"] != "nginx-abc-222" {
			continue
		}
		for i, point := range series.DataPoints {
			if point.Timestamp <= scope.Created["nginx-abc-222"].Add(time.Minute).Unix() {
				t.Fatal("previous Pod restart data escaped")
			}
			if i > 0 && point.Value-series.DataPoints[i-1].Value == 3 {
				observedJump = true
			}
		}
	}
	if !observedJump {
		t.Fatal("lost three early restarts in a replacement Pod's first hour")
	}
	empty := workloadMetricsResponse{State: "unavailable", Panels: map[string]workloadMetricPanel{}, History: map[string]workloadMetricScope{}, Comparison: map[string]workloadMetricPanel{}}
	mergeGKEResources(t.Context(), g, &empty, scope, start, end, time.Minute)
	if empty.Panels["cpu"].Source != "gke" || empty.Panels["memory"].Source != "gke" || empty.Start != start.Unix() || empty.End != end.Unix() || empty.StepSeconds != 60 || empty.PodsTotal != 2 {
		t.Fatalf("native panels lost scope/window on an empty configured store: %+v", empty)
	}
	mixed := workloadMetricsResponse{
		State: "partial", Panels: map[string]workloadMetricPanel{"cpu": {State: "error", Reason: "Historical query failed."}, "memory": {State: "unavailable"}},
		History: map[string]workloadMetricScope{}, Comparison: map[string]workloadMetricPanel{},
	}
	mergeGKEResources(t.Context(), g, &mixed, scope, start, end, time.Minute)
	if mixed.Panels["cpu"].State != "error" || mixed.Panels["cpu"].Reason != "Historical query failed." || mixed.Panels["memory"].Source != "gke" {
		t.Fatalf("mixed panel diagnostics or native fallback lost: %+v", mixed.Panels)
	}
	knownCPU := resp.Panels["cpu"]
	knownCPU.Source = "cadvisor"
	mixed.Panels["cpu"] = knownCPU
	mixed.Comparison["cpu"] = workloadMetricPanel{State: "unavailable"}
	mergeGKEResources(t.Context(), g, &mixed, scope, start, end, time.Minute)
	if mixed.Panels["cpu"].Source != "cadvisor" || mixed.Comparison["cpu"].Source != "gke" {
		t.Fatal("native comparison overwrote conventional totals or remained missing")
	}
	missingLifetime := scope
	missingLifetime.Created = nil
	if got := g.workload(t.Context(), missingLifetime, start, end, time.Minute); got.State != "unavailable" {
		t.Fatalf("native-only workload with no usable samples: %+v", got)
	}
	for _, state := range []string{"detecting", "error"} {
		original := workloadMetricsResponse{State: state, Reason: "Waiting for conventional identity.", Panels: map[string]workloadMetricPanel{}}
		unknown := scope
		unknown.Created = nil
		mergeGKEResources(t.Context(), g, &original, unknown, start, end, time.Minute)
		if original.State != state || original.Reason != "Waiting for conventional identity." || len(original.Panels) != 0 {
			t.Fatalf("empty native data hid conventional %s: %+v", state, original)
		}
	}
	if q := g.chartQuery("Deployment", "default", "nginx", prom.CategoryCPU, nil); q != "" {
		t.Fatalf("unresolved workload widened to namespace: %s", q)
	}
	if q := g.chartQuery("Pod", "default", "", prom.CategoryMemory, nil); q != "" {
		t.Fatalf("empty Pod widened: %s", q)
	}
	if len(resp.Panels) != 2 || len(resp.Sources) != 0 {
		t.Fatal("unexpected native request or throttling series")
	}
	for _, tc := range []struct {
		kind, ns, name string
		category       prom.MetricCategory
		metric         string
	}{
		{"Node", "", "demo-node", prom.CategoryFilesystem, "node_ephemeral_storage_used_bytes"},
		{"Node", "", "demo-node", prom.CategoryCPU, "node_cpu_core_usage_time"},
		{"Cluster", "", "", prom.CategoryCPU, "container_cpu_core_usage_time"},
		{"Namespace", "default", "", prom.CategoryMemory, "container_memory_used_bytes"},
		{"Pod", "default", "demo.pod", prom.CategoryNetworkTX, "pod_network_sent_bytes_count"},
	} {
		var pods *PodScope
		if tc.kind == "Pod" {
			pods = &PodScope{CurrentPods: []string{tc.name}}
		}
		q := g.chartQuery(tc.kind, tc.ns, tc.name, tc.category, pods)
		if !strings.Contains(q, tc.metric) || !strings.Contains(q, g.matchers) || (tc.kind == "Node" && !strings.Contains(q, `node_name="demo-node"`)) || (tc.ns != "" && !strings.Contains(q, `namespace_name="default"`)) {
			t.Fatalf("unexpected query %s", q)
		}
	}
	if q := g.chartQuery("Deployment", "default", "nginx", prom.CategoryFilesystem, &scope); q != "" {
		t.Fatalf("disk occupancy presented as I/O: %s", q)
	}
	if v := g.pvc(t.Context(), "default", "data"); !v.HasData || v.Ratio != 0.25 {
		t.Fatalf("PVC %+v", v)
	}
	g.configured, g.fallback = true, true
	c.manualURL, c.baseURL, c.prom = "http://configured.invalid", "http://configured.invalid", g.store
	d := chartDeps{gke: g, q: &twoClusterStore{}, resolve: func(context.Context, ViewScopeRequest) (ViewScope, error) { return ViewScope{}, nil }}
	regular, err := clusterChart(t.Context(), d, prom.CategoryCPU, "10m")
	if err != nil || regular.Source != "" || len(regular.Result.Series) != 1 {
		t.Fatalf("regular priority %+v %v", regular, err)
	}
	d.q = &twoClusterStore{answer: func(string) (*prom.QueryResult, error) { return emptyChartResult(), nil }}
	native, err := clusterChart(t.Context(), d, prom.CategoryCPU, "10m")
	if err != nil || native.Source != "gke" || len(native.Result.Series) == 0 {
		t.Fatalf("native fallback %+v %v", native, err)
	}
}

func TestGKECuratedRoutesKeepRawStoreSeparate(t *testing.T) {
	c := gkeFixture(t, fixtureIssuer, 1)
	Initialize(c.k8sClient, c.k8sConfig, "fixture")
	GetClient().inCluster = false
	t.Cleanup(func() { SetAuthGate(nil); Reset(); Initialize(nil, nil, "") })
	created := time.Now().Add(-20 * time.Minute).Truncate(time.Minute)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nginx-abc-111", Namespace: "default", UID: "current-pod", CreationTimestamp: metav1.NewTime(created)}}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(pod), map[string]k8score.ResourceScope{string(k8score.Pods): {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	router := metricsRouter()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/prometheus/connect?optional=true", nil))
	var status prom.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || !status.GKESystemMetrics || status.Connected {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{"/prometheus/cluster?category=cpu", "/prometheus/namespace/default?category=memory", "/prometheus/resources/Node/demo-node?category=filesystem", "/prometheus/resources/Pod/default/nginx-abc-111?category=network_tx", "/prometheus/pvc/default/data"} {
		r := getMetrics(t, router, path)
		if r.Code != http.StatusOK || !(strings.Contains(r.Body.String(), `"source":"gke"`) || strings.Contains(r.Body.String(), `"hasData":true`)) {
			t.Errorf("%s: %d %s", path, r.Code, r.Body)
		}
	}
	podChart := getMetrics(t, router, "/prometheus/resources/Pod/default/nginx-abc-111?category=memory")
	var measured ResourceMetricsResponse
	if err := json.Unmarshal(podChart.Body.Bytes(), &measured); err != nil || measured.Result == nil || len(measured.Result.Series) != 1 || len(measured.Result.Series[0].DataPoints) == 0 || measured.Result.Series[0].DataPoints[0].Timestamp <= created.Add(5*time.Minute).Unix() {
		t.Fatalf("current Pod lifetime guard: %d %s", podChart.Code, podChart.Body)
	}
	node := getMetrics(t, router, "/prometheus/resources/Node/demo-node?category=filesystem")
	if !strings.Contains(node.Body.String(), `"unit":"bytes"`) {
		t.Fatalf("node occupancy units: %s", node.Body)
	}
	if r := getMetrics(t, router, "/prometheus/query?query=up"); r.Code == http.StatusOK {
		t.Errorf("raw query unexpectedly admitted: %s", r.Body)
	}
	if GetClient().PromForMCP() != nil {
		t.Error("MCP raw query store unexpectedly available")
	}
	SetAuthGate(func(*http.Request, string, string, string, string) bool { return false })
	for _, path := range []string{"/prometheus/cluster", "/prometheus/resources/Node/demo-node", "/prometheus/pvc/default/data", "/prometheus/namespace/default"} {
		if r := getMetrics(t, router, path); r.Code != http.StatusForbidden {
			t.Errorf("%s expected 403, got %d", path, r.Code)
		}
	}
}

func TestGKEConfiguredFrontendWithoutScrapeTargets(t *testing.T) {
	c := gkeFixture(t, fixtureIssuer, 1)
	Initialize(c.k8sClient, c.k8sConfig, "fixture")
	t.Cleanup(func() { Reset(); Initialize(nil, nil, "") })
	url := c.k8sConfig.Host + "/api/v1/namespaces/gmp-public/services/frontend:web/proxy"
	SetManualURL(url)
	client := GetClient()
	addr, _, err := client.connectWithGKE(t.Context())
	if err != nil || addr != url || !client.GetStatus().GKESystemMetrics || !client.GetStatus().Connected || !client.gkeChartStore().configured {
		t.Fatalf("manual endpoint was replaced or not recognized: addr=%s status=%+v err=%v", addr, client.GetStatus(), err)
	}
	if err := SetWorkloadMetricsScope(prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "wrong"}}, ""); err != nil {
		t.Fatal(err)
	}
	if client.gkeChartStore() != nil {
		t.Fatal("new scope retained previous native reader")
	}
	if _, _, err = client.connectWithGKE(t.Context()); err != nil || client.GetStatus().GKESystemMetrics || !client.GetStatus().Connected {
		t.Fatalf("scope conflict affected wrong capability: %+v err=%v", client.GetStatus(), err)
	}
}

func TestGKEConfiguredCapabilityRefresh(t *testing.T) {
	var present atomic.Bool
	var probes atomic.Int32
	c := gkeFixtureServices(t, fixtureIssuer, 1, func(w http.ResponseWriter, r *http.Request, _ corev1.Service) bool {
		if strings.HasSuffix(r.URL.Path, "/api/v1/query") && strings.Contains(r.FormValue("query"), "node_cpu_total_cores") {
			probes.Add(1)
			if !present.Load() {
				fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
				return true
			}
		}
		return false
	})
	c.manualURL = c.k8sConfig.Host + "/api/v1/namespaces/gmp-public/services/frontend:web/proxy"
	lookup := func(want bool, count int32) {
		t.Helper()
		stores := c.chartStores(t.Context())
		g, err := stores.supplement(t.Context())
		if err != nil || (g != nil) != want || probes.Load() != count {
			t.Fatalf("native present=%t probes=%d err=%v, want %t/%d", g != nil, probes.Load(), err, want, count)
		}
	}
	expire := func() {
		c.mu.Lock()
		c.gkeChecked = time.Now().Add(-2 * time.Minute)
		c.mu.Unlock()
	}
	lookup(false, 1)
	present.Store(true)
	lookup(false, 1)
	expire()
	lookup(true, 2)
	present.Store(false)
	lookup(true, 2)
	expire()
	lookup(false, 3)
	if !c.GetStatus().Connected {
		t.Fatal("native capability expiry cleared explicit raw connection")
	}
}

func TestGKEConfiguredWorkloadWithUnverifiedConventionalIdentity(t *testing.T) {
	c := gkeFixture(t, fixtureIssuer, 1)
	Initialize(c.k8sClient, c.k8sConfig, "fixture")
	t.Cleanup(func() { SetAuthGate(nil); Reset(); Initialize(nil, nil, "") })
	previous := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.SetConnectionStatus(previous) })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "nginx", UID: "nginx-uid"}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "nginx-abc", UID: "nginx-abc-uid", OwnerReferences: []metav1.OwnerReference{scopeOwner("Deployment", "nginx")}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "nginx-abc-111", UID: "pod-uid", CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)), OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "nginx-abc")}}}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(deployment, rs, pod), map[string]k8score.ResourceScope{k8score.Deployments: {Enabled: true}, k8score.ReplicaSets: {Enabled: true}, k8score.Pods: {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	SetManualURL(c.k8sConfig.Host + "/api/v1/namespaces/gmp-public/services/frontend:web/proxy")
	client := GetClient()
	first := getMetrics(t, metricsRouter(), "/prometheus/workload/Deployment/default/nginx?range=1h")
	var cold workloadMetricsResponse
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &cold) != nil || cold.State != "detecting" || len(cold.Panels) != 0 {
		t.Fatalf("cold conventional attribution must retain its quick poll: %d %s", first.Code, first.Body)
	}
	scope, err := ResolvePodScope(t.Context(), k8s.GetResourceCache(), "Deployment", "default", "nginx", 100)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, state := client.automaticWorkloadAttributions(scope)
		if state == "error" {
			break
		}
		if state != "detecting" || time.Now().After(deadline) {
			t.Fatalf("expected failed conventional detection, got %s", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
	r := getMetrics(t, metricsRouter(), "/prometheus/workload/Deployment/default/nginx?range=1h")
	var got workloadMetricsResponse
	if json.Unmarshal(r.Body.Bytes(), &got) != nil || r.Code != http.StatusOK || got.State != "partial" || got.ResourceSource != "gke" || !strings.Contains(got.Reason, "Metrics identity could not be checked. Retrying automatically.") || got.Pods != 1 {
		t.Fatalf("workload response %d: %s", r.Code, r.Body)
	}
	for _, key := range []string{"cpu", "memory"} {
		if p := got.Panels[key]; p.Source != "gke" || len(p.Series) != 1 || len(p.Series[0].DataPoints) == 0 {
			t.Fatalf("%s missing its current Pod samples", key)
		}
	}
}

func TestGKEInvalidatesDroppedOrChangedService(t *testing.T) {
	for _, op := range []string{"delete", "update"} {
		t.Run(op, func(t *testing.T) {
			c := gkeFixture(t, fixtureIssuer, 1)
			c.findGKEChartStore(t.Context(), 0, ErrPrometheusNotFound)
			g := c.cachedNativeCharts()
			if g == nil {
				t.Fatal("reader missing")
			}
			old := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: g.service.Namespace, Name: g.service.Name}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9090}}, Selector: map[string]string{"app": "frontend"}}}
			next := old.DeepCopy()
			if op == "update" {
				next.Spec.Selector = nil
			}
			if !c.invalidateGKEService(c.k8sClient, op, next, old) || c.cachedNativeCharts() != nil || c.GetStatus().GKESystemMetrics {
				t.Fatal("removed frontend still active")
			}
		})
	}
}

func TestGKEChartStoreTransitionToTrafficBackend(t *testing.T) {
	c := gkeFixture(t, fixtureIssuer, 1)
	c.findGKEChartStore(t.Context(), 0, ErrPrometheusNotFound)
	if c.cachedNativeCharts() == nil {
		t.Fatal("missing native reader")
	}
	c.baseURL = "http://traffic.invalid"
	c.discoveryService = &prom.ServiceInfo{Namespace: "caretta", Name: "caretta-vm"}
	if c.cachedNativeCharts() != nil {
		t.Fatal("native-only shortcut survived a newly available conventional store")
	}
	c.findGKEChartStore(t.Context(), 0, nil)
	if g := c.gkeChartStore(); g == nil || !g.fallback || g.configured {
		t.Fatalf("expected an independent native fallback alongside Caretta: %+v", g)
	}
}

func TestGKECoexistsWithDiscoveredPrometheus(t *testing.T) {
	for _, listAllowed := range []bool{true, false} {
		t.Run(fmt.Sprintf("listAllowed=%t", listAllowed), func(t *testing.T) {
			regular := corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "prometheus-server", Namespace: "monitoring"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9090}}}}
			var lists, gets, nativeReads, standardRanges atomic.Int32
			blocked, release := make(chan struct{}, 1), make(chan struct{})
			var hold, failRange atomic.Bool
			hold.Store(true)
			releaseFrontend := sync.OnceFunc(func() { hold.Store(false); close(release) })
			defer releaseFrontend()
			c := gkeFixtureServices(t, fixtureIssuer, 1, func(w http.ResponseWriter, r *http.Request, native corev1.Service) bool {
				switch r.URL.Path {
				case "/api/v1/services":
					lists.Add(1)
					if !listAllowed {
						http.Error(w, "list denied", http.StatusForbidden)
					} else {
						json.NewEncoder(w).Encode(corev1.ServiceList{Items: []corev1.Service{native, regular}})
					}
					return true
				case "/api/v1/namespaces/gmp-public/services/frontend", "/api/v1/namespaces/monitoring/services/prometheus-server":
					gets.Add(1)
					if listAllowed {
						http.Error(w, "get denied", http.StatusForbidden)
					} else if strings.HasSuffix(r.URL.Path, "/frontend") {
						json.NewEncoder(w).Encode(native)
					} else {
						json.NewEncoder(w).Encode(regular)
					}
					return true
				}
				if strings.Contains(r.URL.Path, "/proxy/api/v1/") {
					nativeReads.Add(1)
					if hold.Load() {
						select {
						case blocked <- struct{}{}:
						default:
						}
						select {
						case <-release:
						case <-r.Context().Done():
							return true
						}
					}
				}
				return false
			})
			ordinary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "prometheus-server.monitoring.svc.cluster.local:9090" {
					t.Errorf("non-conventional endpoint used for generic discovery: %s", r.Host)
					http.Error(w, "wrong service", 400)
					return
				}
				if strings.HasSuffix(r.URL.Path, "query_range") {
					standardRanges.Add(1)
					if failRange.Load() {
						http.Error(w, "conventional query failed", http.StatusInternalServerError)
						return
					}
					if strings.Contains(r.FormValue("query"), "memory") {
						fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
					} else {
						fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1700000000,"2"]]}]}}`)
					}
					return
				}
				fmt.Fprint(w, healthyProbeBody)
			}))
			t.Cleanup(ordinary.Close)
			tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var dial net.Dialer
				return dial.DialContext(ctx, network, ordinary.Listener.Addr().String())
			}}
			t.Cleanup(tr.CloseIdleConnections)
			c.httpClient = &http.Client{Transport: tr, Timeout: time.Second}
			chart := func(category prom.MetricCategory) (ClusterMetricsResponse, error) {
				d := productionChartDeps(t.Context(), c)
				d.resolve = func(context.Context, ViewScopeRequest) (ViewScope, error) { return ViewScope{}, nil }
				return clusterChart(t.Context(), d, category, "1h")
			}
			done := make(chan error, 1)
			go func() {
				got, err := chart(prom.CategoryCPU)
				if err == nil && (got.Source != "" || !hasSample(got.Result.Series)) {
					err = fmt.Errorf("conventional CPU missing: %+v", got)
				}
				done <- err
			}()
			select {
			case <-blocked:
				releaseFrontend()
				<-done
				t.Fatal("a chart with conventional data waited for the supplemental frontend")
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			}
			releaseFrontend()
			if nativeReads.Load() != 0 || c.GetStatus().GKESystemMetrics {
				t.Fatal("healthy conventional chart probed the supplemental store")
			}
			raw, snapshot := c.PromSnapshot()
			if raw == nil {
				t.Fatal("no conventional raw store")
			}
			if _, err := raw.Query(t.Context(), "up"); err != nil {
				t.Fatal(err)
			}
			mem, err := chart(prom.CategoryMemory)
			if err != nil || mem.Source != "gke" || !hasSample(mem.Result.Series) || standardRanges.Load() < 2 || !c.ConnectionCurrent(snapshot) || !c.GetStatus().GKESystemMetrics {
				t.Fatalf("missing family did not fall back without moving raw store: result=%+v err=%v", mem, err)
			}
			if lists.Load() != 1 || (gets.Load() > 0) == listAllowed {
				t.Fatalf("wrong Service enumeration: lists=%d targeted gets=%d", lists.Load(), gets.Load())
			}
			failRange.Store(true)
			beforeError := nativeReads.Load()
			_, err = chart(prom.CategoryMemory)
			var queryErr errChartQuery
			if !errors.As(err, &queryErr) || nativeReads.Load() != beforeError {
				t.Fatalf("available separate frontend masked conventional error: %v", err)
			}
			failRange.Store(false)
			c.mu.Lock()
			c.gkeChecked = time.Now().Add(-2 * time.Minute)
			c.mu.Unlock()
			before := nativeReads.Load()
			if _, err := chart(prom.CategoryCPU); err != nil || nativeReads.Load() != before {
				t.Fatalf("usable warm chart refreshed optional store: %v", err)
			}
			if _, _, err := c.EnsureConnected(t.Context()); err != nil || nativeReads.Load() != before || lists.Load() != 1 {
				t.Fatalf("raw connection initiated native refresh")
			}
		})
	}
}

func TestGKEFrontendPodReadyInvalidatesSupplementWithConventionalConnection(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "gmp-public", Name: "frontend"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "frontend"}, Ports: []corev1.ServicePort{{Port: 9090}}}}
	cs := fake.NewClientset(svc)
	if err := k8s.InitScopedTestResourceCache(cs, map[string]k8score.ResourceScope{k8score.Services: {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	c := &Client{k8sClient: cs, baseURL: "http://ordinary", gkeChecked: time.Now()}
	pod := readyPod(svc.Namespace, "frontend-0", map[string]string{"app": "unrelated"}, true)
	c.observeResourceChange(cs, "add", pod, nil)
	if c.gkeChecked.IsZero() {
		t.Fatal("unrelated ready Pod invalidated the native cache")
	}
	pod.Labels = svc.Spec.Selector
	c.observeResourceChange(cs, "add", pod, nil)
	if !c.gkeChecked.IsZero() || c.gkeRevision != 1 || c.baseURL != "http://ordinary" {
		t.Fatal("frontend readiness failed to invalidate only native discovery")
	}
}

func TestGKEExplicitOrdinaryEndpointRemainsAuthoritative(t *testing.T) {
	var nativeProbes atomic.Int32
	var failConventional atomic.Bool
	c := gkeFixtureServices(t, fixtureIssuer, 1, func(_ http.ResponseWriter, r *http.Request, _ corev1.Service) bool {
		if r.Header.Get("X-Test-Tenant") != "" || strings.Contains(r.URL.Path, "/services") {
			t.Errorf("configured endpoint's read or header reached Kubernetes Service discovery: %s", r.URL.Path)
		}
		return false
	})
	ordinary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Tenant") != "ordinary" {
			t.Error("configured header missing from selected store")
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/query_range") {
			if failConventional.Load() {
				http.Error(w, "standard range unavailable", http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
			return
		}
		if strings.Contains(r.FormValue("query"), "kubernetes_io:") {
			nativeProbes.Add(1)
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer ordinary.Close()
	c.manualURL = ordinary.URL
	c.headers = map[string]string{"X-Test-Tenant": "ordinary"}
	chart := func() (ClusterMetricsResponse, error) {
		d := productionChartDeps(t.Context(), c)
		d.resolve = func(context.Context, ViewScopeRequest) (ViewScope, error) { return ViewScope{}, nil }
		return clusterChart(t.Context(), d, prom.CategoryMemory, "1h")
	}
	got, err := chart()
	if err != nil || hasSample(got.Result.Series) || got.Source != "" || nativeProbes.Load() != 1 || c.GetStatus().GKESystemMetrics {
		t.Fatalf("explicit ordinary URL escaped: %+v %v", got, err)
	}
	failConventional.Store(true)
	c.mu.Lock()
	c.gkeChecked = time.Time{}
	c.mu.Unlock()
	_, err = chart()
	var queryError errChartQuery
	if !errors.As(err, &queryError) || nativeProbes.Load() != 1 {
		t.Fatalf("conventional error triggered native fallback: err=%v probes=%d", err, nativeProbes.Load())
	}
}

func TestGKEStaleNativeReaderDoesNotWaitForOrdinaryDiscovery(t *testing.T) {
	c := gkeFixture(t, fixtureIssuer, 1)
	c.findGKEChartStore(t.Context(), 0, ErrPrometheusNotFound)
	if c.cachedNativeCharts() == nil {
		t.Fatal("native reader not established")
	}
	// A background conventional attempt can wait here while a native chart completes.
	release := make(chan struct{})
	runs := stubRediscovery(t, func(string) error { <-release; return nil })
	defer func() { close(release); waitIdle(t, c) }()
	c.mu.Lock()
	c.gkeChecked = time.Now().Add(-2 * time.Minute)
	c.mu.Unlock()
	if stores := c.chartStores(t.Context()); stores.native == nil || stores.standard.store != nil {
		t.Fatal("expired native capability failed to refresh directly")
	}
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("ordinary discovery was not scheduled")
	}
}

func TestGKEExplicitConnectRetriesNegativeAutomaticCapability(t *testing.T) {
	var present atomic.Bool
	c := gkeFixtureServices(t, fixtureIssuer, 1, func(w http.ResponseWriter, r *http.Request, _ corev1.Service) bool {
		if !present.Load() && strings.HasSuffix(r.URL.Path, "/api/v1/query") && strings.Contains(r.FormValue("query"), "node_cpu_total_cores") {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
			return true
		}
		return false
	})
	Initialize(c.k8sClient, c.k8sConfig, "fixture")
	GetClient().inCluster = false
	t.Cleanup(func() { Reset(); Initialize(nil, nil, "") })
	router := metricsRouter()
	connect := func(path string) prom.Status {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		var st prom.Status
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return st
	}
	if st := connect("/prometheus/connect?optional=true"); st.GKESystemMetrics {
		t.Fatal("negative node probe treated as positive")
	}
	present.Store(true)
	if st := connect("/prometheus/connect"); !st.GKESystemMetrics || st.Connected {
		t.Fatal("explicit Discover did not retry native capability independently of raw discovery")
	}
}

func TestGKENativeChartsNeverServeWhileRelayActive(t *testing.T) {
	c := gkeFixture(t, fixtureIssuer, 1)
	c.findGKEChartStore(t.Context(), 0, ErrPrometheusNotFound)
	if c.cachedNativeCharts() == nil {
		t.Fatal("native reader not established")
	}
	f := &fakeRelayTransport{}
	cfg := testRelayConfig(f, nil)
	c.mu.Lock()
	c.applyRelayLocked(&relayState{cfg: cfg, source: cfg.Source})
	c.mu.Unlock()

	if stores := c.chartStores(t.Context()); stores.native != nil || stores.supplement != nil {
		t.Fatal("chart stores offered GKE's in-cluster store while the relay is active")
	}
	c.findGKEChartStore(t.Context(), c.DiscoveryGeneration(), ErrPrometheusNotFound)
	if c.gkeChartStore() != nil || c.cachedNativeCharts() != nil {
		t.Fatal("GKE native reader established while the relay is active")
	}
	c.mu.RLock()
	probed := c.gkeGeneration == c.discoveryGen
	c.mu.RUnlock()
	if probed {
		t.Fatal("GKE discovery ran while the relay is active")
	}
}
