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
			add(metric+"_bucket", base+fmt.Sprintf(`,le="%s"`, le), fmt.Sprintf("0+%dx10", increments[i]))
			add(metric+"_bucket", duplicateBase+fmt.Sprintf(`,le="%s"`, le), fmt.Sprintf("0+%dx10", increments[i]))
		}
		queries, err := BuildRequestQueries(time.Minute, sel, scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(queries.Rate, "{}", 2)
		check(queries.Errors, "{}", 10)
		check(queries.P50, "{}", 0.1)
		check(queries.P95, "{}", 0.75)
		check(queries.HistogramCoverage, "{}", 1)
		check(queries.StatusCoverage, "{}", 1)
		check(queries.ObservedPods, "{}", 1)
		counterResetBase := strings.ReplaceAll(base, `"api-0"`, `"api-counter-reset"`)
		add(count, counterResetBase+fmt.Sprintf(`,%s="200"`, status), "0+60x6 60+60x123")
		counterReset, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-counter-reset"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(counterReset.Rate, "{}", 1)
		check(counterReset.Errors, "{}", 0)
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
		for _, undefined := range []string{idle.Errors, idle.P50, idle.P95} {
			check("("+undefined+") == bool ("+undefined+")", "{}", 0)
		}
		healthyBase := strings.ReplaceAll(base, `"api-0"`, `"api-healthy"`)
		add(count, healthyBase+fmt.Sprintf(`,%s="200"`, status), "0+60x10")
		healthy, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-healthy"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(healthy.Rate, "{}", 1)
		check(healthy.Errors, "{}", 0)
		check("count("+healthy.P95+") or vector(0)", "{}", 0)
		resetBase := strings.ReplaceAll(base, `"api-0"`, `"api-no-response"`)
		add(count, resetBase+fmt.Sprintf(`,%s="0"`, status), "0+30x10")
		add(count, resetBase+fmt.Sprintf(`,%s="200"`, status), "0+30x10")
		reset, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-no-response"}), scope, source, "")
		if err != nil {
			t.Fatal(err)
		}
		check(reset.Rate, "{}", 1)
		check(reset.Errors, "{}", 0)
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
		check("count("+mixed.HistogramCoverage+") or vector(0)", "{}", 0)
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
