package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const fixtureIssuer = "https://container.googleapis.com/v1/projects/demo-project/locations/us-east1-b/clusters/demo"

func gkeFixture(t *testing.T, issuer string, metricCount int) *Client {
	t.Helper()
	svc := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "gmp-public"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "frontend"}, Ports: []corev1.ServicePort{{Name: "web", Port: 9090, TargetPort: intstr.FromString("http")}}},
	}
	pod := corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: gmpFrontendImage + ":v0.14", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 9090}}}}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("unexpected Kubernetes or proxy method %s", r.Method)
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
		{"regular discovered first", fixtureIssuer, 1, nil, true, false},
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
	if !gkeFrontendCandidate(s) {
		t.Fatal("lost custom labelled Service")
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
		q := g.chartQuery(tc.kind, tc.ns, tc.name, tc.category, nil)
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
	g.configured = true
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
	addr, _, err := client.EnsureConnected(t.Context())
	if err != nil || addr != url || !client.GetStatus().GKESystemMetrics || !client.GetStatus().Connected || !client.gkeChartStore().configured {
		t.Fatalf("manual endpoint was replaced or not recognized: addr=%s status=%+v err=%v", addr, client.GetStatus(), err)
	}
	if err := SetWorkloadMetricsScope(prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "wrong"}}, ""); err != nil {
		t.Fatal(err)
	}
	if client.gkeChartStore() != nil {
		t.Fatal("new scope retained previous native reader")
	}
	if _, _, err = client.EnsureConnected(t.Context()); err != nil || client.GetStatus().GKESystemMetrics || !client.GetStatus().Connected {
		t.Fatalf("scope conflict affected wrong capability: %+v err=%v", client.GetStatus(), err)
	}
}
