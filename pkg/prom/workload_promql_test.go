package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in suite evaluates the production expressions in real Prometheus,
// rather than teaching a mock server to return the answers the test expects.
func TestWorkloadPromQL(t *testing.T) {
	image := os.Getenv("RADAR_TEST_PROMTOOL_IMAGE")
	if image == "" {
		t.Skip("set RADAR_TEST_PROMTOOL_IMAGE=prom/prometheus:v3.5.0 to evaluate queries with Docker")
	}
	input := []map[string]string{}
	add := func(name, labels, values string) {
		input = append(input, map[string]string{"series": name + "{" + labels + "}", "values": values})
	}
	scope := WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "west"}}
	sel := SelectPods("shop", []string{"api-0"})
	tests := []map[string]any{}
	check := func(query, labels string, value float64) {
		tests = append(tests, map[string]any{"expr": "round((" + query + "), 0.000001)", "eval_time": "10m", "exp_samples": []map[string]any{{"labels": labels, "value": value}}})
	}
	identityPods := []WorkloadPodIdentity{{Name: "identity-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}, {Name: "identity-1", UID: "0d709b51-f624-4d0a-b7dd-79e17402acb9"}}
	for i, pod := range identityPods {
		base := fmt.Sprintf(`job="custom-http",k8s_namespace_name="shop",k8s_pod_name=%q,k8s_pod_uid=%q,http_response_status_code="200"`, pod.Name, pod.UID)
		add("http_server_request_duration_seconds_count", base, "0+60x10")
		foreign := strings.Replace(base, pod.UID, "ffffffff-ffff-ffff-ffff-ffffffffffff", 1)
		add("http_server_request_duration_seconds_count", foreign, "0+6000x10")
		mismatched := strings.Replace(base, pod.Name, identityPods[1-i].Name, 1) + `,__radar_keep="1",__radar_pair="forged"`
		add("http_server_request_duration_seconds_count", mismatched, "0+6000x10")
		id := "/kubepods/burstable/pod" + pod.UID + "/container"
		if i == 1 {
			id = "/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod" + strings.ReplaceAll(pod.UID, "-", "_") + ".slice/container.scope"
		}
		resource := fmt.Sprintf(`namespace="shop",pod=%q,container="app",id=%q`, pod.Name, id)
		add("container_cpu_usage_seconds_total", resource, "0+60x10")
		add("container_cpu_usage_seconds_total", strings.ReplaceAll(resource, pod.Name, identityPods[1-i].Name), "0+6000x10")
		add("container_cpu_usage_seconds_total", fmt.Sprintf(`namespace="shop",pod=%q,container="app",id="/not-a-pod",__radar_uid=%q,__radar_keep="1"`, pod.Name, pod.UID), "0+6000x10")
	}
	identitySelection := SelectPods("shop", []string{"identity-0", "identity-1"})
	identityRequests, err := BuildIdentityRequestQueries(time.Minute, identitySelection, identityPods, RequestSourceBeyla, `job="custom-http"`)
	if err != nil {
		t.Fatal(err)
	}
	check(identityRequests.Rate, "{}", 2)
	check(identityRequests.ObservedPods, "{}", 2)
	identityCPU, err := BuildIdentityWorkloadResourceQuery(time.Minute, identitySelection, identityPods, CategoryCPU)
	if err != nil {
		t.Fatal(err)
	}
	check("sum("+identityCPU+")", "{}", 2)
	kubeletPod := WorkloadPodIdentity{Name: "kubelet-identity", UID: "bf48504c-d21a-4b82-a598-a19868cf3d29"}
	kubeletID := "/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-burstable.slice/kubelet-kubepods-burstable-podbf48504c_d21a_4b82_a598_a19868cf3d29.slice/cri-containerd-container.scope"
	kubeletLabels := fmt.Sprintf(`namespace="shop",pod=%q,container="app",id=%q`, kubeletPod.Name, kubeletID)
	for _, fixture := range []struct{ metric, values string }{
		{"container_cpu_usage_seconds_total", "0+60x10"},
		{"container_memory_working_set_bytes", "1024+0x10"},
		{"container_cpu_cfs_throttled_periods_total", "0+15x10"},
		{"container_cpu_cfs_periods_total", "0+60x10"},
	} {
		add(fixture.metric, kubeletLabels, fixture.values)
		add(fixture.metric, strings.ReplaceAll(kubeletLabels, "bf48504c_d21a_4b82_a598_a19868cf3d29", "ffffffff_ffff_ffff_ffff_ffffffffffff"), "0+6000x10")
		add(fixture.metric, strings.ReplaceAll(kubeletLabels, kubeletPod.Name, "wrong-pod-name"), "0+6000x10")
	}
	for _, category := range []MetricCategory{CategoryCPU, CategoryMemory} {
		query, err := BuildIdentityWorkloadResourceQuery(time.Minute, SelectPods("shop", []string{kubeletPod.Name, "wrong-pod-name"}), []WorkloadPodIdentity{kubeletPod}, category)
		if err != nil {
			t.Fatal(err)
		}
		want := float64(1)
		if category == CategoryMemory {
			want = 1024
		}
		check(query, `{pod="kubelet-identity"}`, want)
	}
	kubeletThrottle, err := BuildIdentityThrottleQuery(time.Minute, SelectPods("shop", []string{kubeletPod.Name, "wrong-pod-name"}), []WorkloadPodIdentity{kubeletPod})
	if err != nil {
		t.Fatal(err)
	}
	check(kubeletThrottle, `{pod="kubelet-identity"}`, 25)
	for _, source := range []RequestSource{RequestSourceBeyla, RequestSourceIstio} {
		base := `cluster="west",job="beyla",k8s_namespace_name="shop",k8s_pod_name="api-0"`
		metric, status := "http_server_request_duration_seconds", "http_response_status_code"
		buckets := []string{"0.1", "0.5", "1", "+Inf"}
		if source == RequestSourceIstio {
			base = `cluster="west",reporter="destination",request_protocol="http",namespace="shop",pod="api-0",destination_workload_namespace="shop"`
			metric, status = "istio_request_duration_milliseconds", "response_code"
			buckets = []string{"100", "500", "1000", "+Inf"}
		}
		count := metric + "_count"
		if source == RequestSourceIstio {
			count = "istio_requests_total"
		}
		for i, code := range []string{"200", "500"} {
			increments := []int{108, 12}
			add(count, base+fmt.Sprintf(`,%s="%s"`, status, code), fmt.Sprintf("0+%dx10", increments[i]))
		}
		duplicateBase := base + `,instance="duplicate"`
		for i, code := range []string{"200", "500"} {
			increments := []int{108, 12}
			add(count, duplicateBase+fmt.Sprintf(`,%s="%s"`, status, code), fmt.Sprintf("0+%dx10", increments[i]))
		}
		for i, le := range buckets {
			increments := []int{60, 108, 120, 120}
			for _, labels := range []string{base, duplicateBase} {
				if source == RequestSourceIstio {
					for j, code := range []string{"200", "500"} {
						fraction := []float64{0.9, 0.1}[j]
						add(metric+"_bucket", labels+fmt.Sprintf(`,le=%q,response_code=%q`, le, code), fmt.Sprintf("0+%gx10", float64(increments[i])*fraction))
						if le == "+Inf" {
							add(metric+"_count", labels+fmt.Sprintf(`,response_code=%q`, code), fmt.Sprintf("0+%gx10", 120*fraction))
						}
					}
				} else {
					add(metric+"_bucket", labels+fmt.Sprintf(`,le=%q`, le), fmt.Sprintf("0+%dx10", increments[i]))
				}
			}
		}
		queries, err := BuildRequestQueries(time.Minute, sel, scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(queries.Rate, "{}", 2)
		check(queries.Errors, "{}", 0.2)
		check(queries.P50, "{}", 0.1)
		check(queries.P95, "{}", 0.75)
		if source == RequestSourceIstio {
			check(queries.HistogramCoverage, "{}", 1)
		} else {
			check(queries.HistogramCoverage, "{}", 2)
		}
		check(queries.HistogramUniformity, "{}", 1)
		check(queries.StatusCoverage, "{}", 2)
		check(queries.Population, "{}", 1)
		check(queries.ObservedPods, "{}", 1)
		counterResetBase := strings.ReplaceAll(base, `"api-0"`, `"api-counter-reset"`)
		add(count, counterResetBase+fmt.Sprintf(`,%s="200"`, status), "0+60x6 60+60x123")
		counterReset, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-counter-reset"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(counterReset.Rate, "{}", 1)
		check("count("+counterReset.Errors+") or vector(0)", "{}", 0)
		longWindow, err := BuildRequestQueries(30*time.Minute, SelectPods("shop", []string{"api-counter-reset"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		tests = append(tests, map[string]any{"expr": longWindow.Rate, "eval_time": "120m", "exp_samples": []map[string]any{{"labels": "{}", "value": 1}}})
		partlyObserved, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-0", "api-uninstrumented"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(partlyObserved.ObservedPods, "{}", 1)
		idleBase := strings.ReplaceAll(base, `"api-0"`, `"api-idle"`)
		add(count, idleBase+fmt.Sprintf(`,%s="200"`, status), "0+0x10")
		for _, le := range buckets {
			add(metric+"_bucket", idleBase+fmt.Sprintf(`,le="%s"`, le), "0+0x10")
		}
		idle, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-idle"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(idle.Rate, "{}", 0)
		check(idle.ObservedPods, "{}", 1)
		for _, undefined := range []string{idle.P50, idle.P95} {
			check("("+undefined+") == bool ("+undefined+")", "{}", 0)
		}
		healthyBase := strings.ReplaceAll(base, `"api-0"`, `"api-healthy"`)
		add(count, healthyBase+fmt.Sprintf(`,%s="200"`, status), "0+60x10")
		healthy, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-healthy"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(healthy.Rate, "{}", 1)
		check("count("+healthy.Errors+") or vector(0)", "{}", 0)
		check("count("+healthy.P95+") or vector(0)", "{}", 0)
		check("count("+healthy.HistogramCoverage+") or vector(0)", "{}", 0)
		resetBase := strings.ReplaceAll(base, `"api-0"`, `"api-no-response"`)
		add(count, resetBase+fmt.Sprintf(`,%s="0"`, status), "0+30x10")
		add(count, resetBase+fmt.Sprintf(`,%s="200"`, status), "0+30x10")
		reset, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-no-response"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(reset.Rate, "{}", 1)
		check("count("+reset.Errors+") or vector(0)", "{}", 0)
		check(reset.StatusCoverage, "{}", 1)
		for i, pod := range []string{"api-mixed-1", "api-mixed-2"} {
			mixedBase := strings.ReplaceAll(base, `"api-0"`, `"`+pod+`"`)
			add(count, mixedBase+fmt.Sprintf(`,%s="200"`, status), "0+60x10")
			for _, le := range []string{buckets[i], buckets[2], "+Inf"} {
				add(metric+"_bucket", mixedBase+fmt.Sprintf(`,le="%s"`, le), "0+60x10")
			}
		}
		mixed, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-mixed-1", "api-mixed-2"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(mixed.HistogramUniformity, "{}", 0.5)
		for _, replica := range []string{"a", "b"} {
			haBase := strings.ReplaceAll(base, `"api-0"`, `"api-ha"`) + `,prometheus_replica="` + replica + `"`
			add(count, haBase+fmt.Sprintf(`,%s="200"`, status), "0+60x10")
		}
		ha, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-ha"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(ha.Population, "{}", 2)
	}
	for _, tc := range []struct {
		name                                                         string
		missingStatus, missingBucket, mismatchedCount, foreignBucket bool
	}{
		{name: "async"}, {name: "missing-status", missingStatus: true}, {name: "missing-bucket", missingBucket: true},
		{name: "inconsistent-count", mismatchedCount: true}, {name: "foreign-bucket", foreignBucket: true},
	} {
		pod := "istio-" + tc.name
		base := fmt.Sprintf(`cluster="west",job="istio",reporter="destination",request_protocol="http",namespace="shop",pod=%q,destination_workload_namespace="shop"`, pod)
		for _, code := range []string{"200", "503"} {
			labels := base + fmt.Sprintf(`,response_code=%q`, code)
			add("istio_requests_total", labels, "0+60x10")
			if tc.missingStatus && code == "503" {
				continue
			}
			// The histogram is internally complete but lags its separate counter.
			add("istio_request_duration_milliseconds_count", labels, "0+54x10")
			for _, le := range []string{"100", "500", "+Inf"} {
				if tc.missingBucket && code == "503" && le == "100" {
					continue
				}
				values := "0+54x10"
				if tc.mismatchedCount && code == "503" && le == "+Inf" {
					values = "0+48x10"
				}
				add("istio_request_duration_milliseconds_bucket", labels+fmt.Sprintf(`,le=%q`, le), values)
			}
		}
		if tc.foreignBucket {
			add("istio_request_duration_milliseconds_bucket", base+`,response_code="404",le="100"`, "0+54x10")
		}
		q, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{pod}), scope, RequestSourceIstio, "")
		if err != nil {
			t.Fatal(err)
		}
		want := float64(0)
		if tc.name == "async" {
			want = 1
			check(q.Rate, "{}", 2)
			check(q.P95, "{}", 0.095)
		}
		check("("+q.HistogramCoverage+") * ("+q.HistogramUniformity+" == bool 1) or vector(0)", "{}", want)
	}
	// Identical workload labels in another cluster must not affect any answer.
	add("http_server_request_duration_seconds_count", `cluster="east",job="beyla",k8s_namespace_name="shop",k8s_pod_name="api-0",http_response_status_code="500"`, "0+9999x10")
	for _, job := range []string{"cadvisor", "duplicate-scrape"} {
		base := fmt.Sprintf(`cluster="west",namespace="shop",pod="api-0",container="app",job="%s"`, job)
		throttled, periods := "0+15x10", "0+60x10"
		if job == "duplicate-scrape" {
			throttled, periods = "0+3x10", "0+30x10"
		}
		add("container_cpu_cfs_throttled_periods_total", base, throttled)
		add("container_cpu_cfs_periods_total", base, periods)
		add("container_cpu_usage_seconds_total", base, "0+60x10")
		add("container_memory_working_set_bytes", base, "1024+0x10")
	}
	query, err := BuildThrottleQuery(time.Minute, sel, scope)
	if err != nil {
		t.Fatal(err)
	}
	check(query, `{pod="api-0"}`, 25)
	for category, want := range map[MetricCategory]float64{CategoryCPU: 1, CategoryMemory: 1024} {
		query, err := BuildWorkloadResourceQuery(time.Minute, sel, scope, category)
		if err != nil {
			t.Fatal(err)
		}
		check(query, `{pod="api-0"}`, want)
	}
	localScope := WorkloadMetricsScope{SingleCluster: true}
	localPods := SelectPods("demo", []string{"web-0"})
	add("http_server_request_duration_seconds_count", `job="app-http-metrics",k8s_namespace_name="demo",k8s_pod_name="web-0",http_response_status_code="200"`, "0+60x10")
	for _, tc := range []struct {
		scope WorkloadMetricsScope
		job   string
		count float64
	}{
		{localScope, "", 0},
		{localScope, `job="app-http-metrics"`, 1},
		{WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "skh-nonprod"}}, `job="app-http-metrics"`, 0},
	} {
		queries, err := BuildRequestQueries(time.Minute, localPods, tc.scope, RequestSourceBeyla, tc.job)
		if err != nil {
			t.Fatal(err)
		}
		check("count("+queries.Rate+") or vector(0)", "{}", tc.count)
	}
	add("http_server_request_duration_seconds_count", `job="demo/web",instance="demo.web-0.nginx",http_response_status_code="200"`, "0+60x10")
	add("target_info", `job="demo/web",instance="demo.web-0.nginx",k8s_namespace_name="demo",k8s_pod_name="web-0"`, "1+0x10")
	otlp, err := BuildRequestQueries(time.Minute, localPods, localScope, RequestSourceBeyla, `job="demo/web"`)
	if err != nil {
		t.Fatal(err)
	}
	check("count("+otlp.Rate+") or vector(0)", "{}", 0)
	check(`sum(rate(http_server_request_duration_seconds_count{job="demo/web"}[5m]))`, "{}", 1)
	runWorkloadPromQL(t, image, input, tests)
}

func runWorkloadPromQL(t *testing.T, image string, input []map[string]string, tests []map[string]any) {
	t.Helper()
	fixture := map[string]any{"evaluation_interval": "1m", "tests": []map[string]any{{"interval": "1m", "input_series": input, "promql_expr_test": tests}}}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rules.yml"), data, 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--read-only", "--tmpfs", "/tmp", "--entrypoint", "/bin/promtool", "-v", dir+":/tests:ro", image, "test", "rules", "/tests/rules.yml")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PromQL evaluation: %v\n%s", err, output)
	}
}
