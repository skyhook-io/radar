package prom

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWorkloadHistoryScope(t *testing.T) {
	for _, h := range []WorkloadHistory{
		{Kind: "Deployment", Namespace: "shop", Name: "api"},
		{Kind: "Service", Namespace: "shop", Name: "api", Scope: WorkloadMetricsScope{SingleCluster: true}},
		{Kind: "Deployment", Name: "api", Scope: WorkloadMetricsScope{SingleCluster: true}},
	} {
		if _, err := h.OwnerQuery(); err == nil {
			t.Fatalf("unsafe history accepted: %+v", h)
		}
	}
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		for _, recorded := range []bool{false, true} {
			h := WorkloadHistory{Kind: kind, Namespace: "shop", Name: "api", Scope: WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "west"}}, Recorded: recorded}
			for _, source := range []RequestSource{RequestSourceBeyla, RequestSourceIstio} {
				q, err := BuildHistoryRequestQueries(time.Minute, h, source, "")
				if err != nil {
					t.Fatal(err)
				}
				for _, query := range []string{q.Rate, q.P95, q.HistogramCoverage, q.Population} {
					if len(query) > MaxWorkloadQueryBytes || strings.Contains(query, "pod=~") || !strings.Contains(query, `cluster="west"`) {
						t.Fatalf("unbounded or unscoped %s %s query (%d bytes): %s", kind, source, len(query), query)
					}
				}
			}
		}
	}
}

func TestWorkloadHistoryPromQL(t *testing.T) {
	image := os.Getenv("RADAR_TEST_PROMTOOL_IMAGE")
	if image == "" {
		t.Skip("set RADAR_TEST_PROMTOOL_IMAGE to evaluate historical queries with Docker")
	}
	input := []map[string]string{}
	add := func(metric, labels, values string) {
		input = append(input, map[string]string{"series": metric + "{" + labels + "}", "values": values})
	}
	tests := []map[string]any{}
	check := func(query, at string, samples ...map[string]any) {
		tests = append(tests, map[string]any{"expr": "round((" + query + "),0.000001)", "eval_time": at, "exp_samples": samples})
	}
	lines := func(total, maximum float64) []map[string]any {
		return []map[string]any{{"labels": `{aggregation="Workload"}`, "value": total}, {"labels": `{aggregation="Maximum Pod"}`, "value": maximum}}
	}
	h := WorkloadHistory{Kind: "StatefulSet", Namespace: "large", Name: "app", Scope: WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "west"}}}
	for i := range 128 {
		base := fmt.Sprintf(`cluster="west",namespace="large",pod="app-%d"`, i)
		add("kube_pod_owner", base+`,owner_kind="StatefulSet",owner_name="app",owner_is_controller="true"`, "1+0x10")
		add("namespace_workload_pod:kube_pod_owner:relabel", base+`,workload="app",workload_type="statefulset"`, "1+0x10")
		for metric, values := range map[string]string{
			"container_cpu_usage_seconds_total":         "0+60x10",
			"container_memory_working_set_bytes":        "1024+0x10",
			"container_cpu_cfs_throttled_periods_total": "0+15x10",
			"container_cpu_cfs_periods_total":           "0+60x10",
		} {
			add(metric, base+`,container="app",job="kubelet"`, values)
			add(metric, strings.Replace(base, `cluster="west"`, `cluster="east"`, 1)+`,container="app",job="kubelet"`, "100000+6000x10")
		}
	}
	for _, recorded := range []bool{false, true} {
		h.Recorded = recorded
		for category, total := range map[MetricCategory]float64{CategoryCPU: 128, CategoryMemory: 128 * 1024} {
			query, err := BuildHistoryResourceQuery(time.Minute, h, category)
			if err != nil {
				t.Fatal(err)
			}
			check(query, "10m", lines(total, total/128)...)
		}
		query, err := BuildHistoryThrottleQuery(time.Minute, h)
		if err != nil {
			t.Fatal(err)
		}
		check(query, "10m", lines(25, 25)...)
	}
	h = WorkloadHistory{Kind: "Deployment", Namespace: "rollout", Name: "api", Scope: h.Scope}
	for _, revision := range []struct{ pod, rs, owners, memory, counter string }{
		{"old", "api-old", "1+0x5 stale _ _ _ _", "100+0x5 stale _ _ _ _", "0+60x5 stale _ _ _ _"},
		{"new", "api-new", "_ _ _ _ _ _ 1+0x4", "_ _ _ _ _ _ 200+0x4", "_ _ _ _ _ _ 0+60x4"},
	} {
		base := fmt.Sprintf(`cluster="west",namespace="rollout",pod=%q`, revision.pod)
		add("kube_pod_owner", base+fmt.Sprintf(`,owner_kind="ReplicaSet",owner_name=%q,owner_is_controller="true"`, revision.rs), revision.owners)
		add("kube_replicaset_owner", fmt.Sprintf(`cluster="west",namespace="rollout",replicaset=%q,owner_kind="Deployment",owner_name="api",owner_is_controller="true"`, revision.rs), revision.owners)
		add("container_memory_working_set_bytes", base+`,container="app"`, revision.memory)
		add("istio_requests_total", base+`,destination_workload="api",destination_workload_namespace="rollout",reporter="destination",request_protocol="http",response_code="200",job="istio"`, revision.counter)
		add("istio_requests_total", base+`,destination_workload="api",destination_workload_namespace="rollout",reporter="waypoint",request_protocol="http",response_code="200",job="istio"`, "0+6000x10")
		add("http_server_request_duration_seconds_count", fmt.Sprintf(`cluster="west",k8s_namespace_name="rollout",k8s_pod_name=%q,http_response_status_code="200",job="custom-collector"`, revision.pod), revision.counter)
	}
	query, err := BuildHistoryResourceQuery(time.Minute, h, CategoryMemory)
	if err != nil {
		t.Fatal(err)
	}
	check(query, "4m", lines(100, 100)...)
	check(query, "10m", lines(200, 200)...)
	for _, source := range []RequestSource{RequestSourceIstio, RequestSourceBeyla} {
		q, err := BuildHistoryRequestQueries(time.Minute, h, source, "")
		if err != nil {
			t.Fatal(err)
		}
		// A new counter starting at zero contributes four observed minutes to the five-minute window.
		check(q.Rate, "4m", map[string]any{"labels": "{}", "value": 0.8})
		check(q.Rate, "10m", map[string]any{"labels": "{}", "value": 0.8})
		check(q.Population, "10m", map[string]any{"labels": "{}", "value": 1.0})
	}
	add("kube_pod_owner", `cluster="west",namespace="rollout",pod="wrong-kind",owner_kind="StatefulSet",owner_name="api",owner_is_controller="true"`, "1+0x10")
	add("istio_requests_total", `cluster="west",namespace="rollout",pod="wrong-kind",destination_workload="api",destination_workload_namespace="rollout",reporter="destination",request_protocol="http",response_code="200",job="istio"`, "0+6000x10")
	weighted := WorkloadHistory{Kind: "DaemonSet", Namespace: "weights", Name: "agent", Scope: h.Scope}
	for i, counters := range [][2]string{{"0+6x10", "0+60x10"}, {"0+300x10", "0+600x10"}} {
		base := fmt.Sprintf(`cluster="west",namespace="weights",pod="agent-%d"`, i)
		add("kube_pod_owner", base+`,owner_kind="DaemonSet",owner_name="agent",owner_is_controller="true"`, "1+0x10")
		add("container_cpu_cfs_throttled_periods_total", base+`,container="app",job="kubelet"`, counters[0])
		add("container_cpu_cfs_periods_total", base+`,container="app",job="kubelet"`, counters[1])
	}
	weightedQuery, err := BuildHistoryThrottleQuery(time.Minute, weighted)
	if err != nil {
		t.Fatal(err)
	}
	check(weightedQuery, "10m", lines(46.363636, 50)...)
	population, err := BuildHistoryResourcePopulationQuery(time.Minute, weighted, "throttling")
	if err != nil {
		t.Fatal(err)
	}
	check(population, "7m", map[string]any{"labels": "{}", "value": 1.0})
	add("container_cpu_cfs_periods_total", `cluster="west",namespace="weights",pod="agent-0",container="app",job="kubelet",replica="duplicate"`, "_ _ _ _ _ _ _ _ 0+60x2")
	check(population, "10m", map[string]any{"labels": "{}", "value": 2.0})
	runWorkloadPromQL(t, image, input, tests)
}
