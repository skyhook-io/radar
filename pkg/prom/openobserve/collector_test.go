package openobserve

import (
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/prometheus/promql/parser"
)

const uidA = "80fe77fa-9593-40e9-ad9c-9c3689666f11"

var collectorOptsA = RewriteOptions{
	Matchers:     []Matcher{{Name: "k8s_cluster", Value: "cluster1"}},
	Dialect:      DialectCollector,
	CollectorUID: uidA,
}

const renamesContainer = `, "namespace", "$1", "k8s_namespace_name", "(.+)"), "pod", "$1", "k8s_pod_name", "(.+)"), "container", "$1", "k8s_container_name", "(.+)"), "node", "$1", "k8s_node_name", "(.+)")`
const renamesPod = `, "namespace", "$1", "k8s_namespace_name", "(.+)"), "pod", "$1", "k8s_pod_name", "(.+)"), "node", "$1", "k8s_node_name", "(.+)")`

// The queries are the chart builders' output (pkg/prom/testdata/charts_golden.txt).
func TestCollectorTranslatesRadarQueries(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{
			"pod cpu",
			`sum(rate(container_cpu_usage_seconds_total{container!='',namespace='pay-ments',pod='api.x-1'}[5m])) by (pod,namespace)`,
			`sum by (pod, namespace, k8s_cluster) (label_replace(label_replace(label_replace(label_replace(rate(container_cpu_time{k8s_cluster="cluster1",k8s_cluster_uid="` + uidA + `",k8s_container_name!="",k8s_namespace_name="pay-ments",k8s_pod_name="api.x-1"}[5m])` + renamesContainer + `)`,
		},
		{
			"pod memory",
			`sum by (pod,namespace) (max by (pod,namespace,container) (container_memory_working_set_bytes{container!='',namespace='pay-ments',pod='api.x-1'}))`,
			`sum by (pod, namespace, k8s_cluster) (max by (pod, namespace, container, k8s_cluster) (label_replace(label_replace(label_replace(label_replace(container_memory_working_set{k8s_cluster="cluster1",k8s_cluster_uid="` + uidA + `",k8s_container_name!="",k8s_namespace_name="pay-ments",k8s_pod_name="api.x-1"}` + renamesContainer + `))`,
		},
		{
			"pod network receive",
			`sum(rate(container_network_receive_bytes_total{namespace='pay-ments',pod='api.x-1'}[5m])) by (pod,namespace)`,
			`sum by (pod, namespace, k8s_cluster) (label_replace(label_replace(label_replace(rate(k8s_pod_network_io{direction="receive",k8s_cluster="cluster1",k8s_cluster_uid="` + uidA + `",k8s_namespace_name="pay-ments",k8s_pod_name="api.x-1"}[5m])` + renamesPod + `)`,
		},
		{
			"cluster network transmit, container and interface matchers",
			`sum(rate(container_network_transmit_bytes_total{container="POD",interface="eth0"}[5m]))`,
			`sum by (k8s_cluster) (label_replace(label_replace(label_replace(rate(k8s_pod_network_io{direction="transmit",interface="eth0",k8s_cluster="cluster1",k8s_cluster_uid="` + uidA + `"}[5m])` + renamesPod + `)`,
		},
		{
			"pod-level cadvisor series and cadvisor-only matchers",
			`sum by (pod) (rate(container_cpu_usage_seconds_total{container="",image!="",cpu="total",node="n1"}[5m]))`,
			`sum by (pod, k8s_cluster) (label_replace(label_replace(label_replace(rate(k8s_pod_cpu_time{k8s_cluster="cluster1",k8s_cluster_uid="` + uidA + `",k8s_node_name="n1"}[5m])` + renamesPod + `)`,
		},
		{
			"absent renames after absent",
			`absent(container_memory_working_set_bytes{namespace="a"})`,
			`label_replace(label_replace(label_replace(label_replace(absent(container_memory_working_set{k8s_cluster="cluster1",k8s_cluster_uid="` + uidA + `",k8s_namespace_name="a"})` + renamesContainer,
		},
		{
			"agent metric named directly",
			`sum(rate(k8s_pod_cpu_time{k8s_namespace_name="demo"}[5m]))`,
			`sum by (k8s_cluster) (rate(k8s_pod_cpu_time{k8s_cluster="cluster1",k8s_cluster_uid="` + uidA + `",k8s_namespace_name="demo"}[5m]))`,
		},
		{
			"cluster override attempts",
			`container_cpu_time{k8s_cluster="other",k8s_cluster_uid=~".*"}`,
			`container_cpu_time{k8s_cluster="cluster1",k8s_cluster="other",k8s_cluster_uid="` + uidA + `",k8s_cluster_uid=~".*"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Rewrite(tc.in, collectorOptsA)
			if err != nil {
				t.Fatalf("Rewrite: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Rewrite(%q)\n got  %s\n want %s", tc.in, got, tc.want)
			}
			if _, err := parser.ParseExpr(got); err != nil {
				t.Fatalf("output does not parse: %v", err)
			}
			assertEverySelectorScoped(t, got, "k8s_cluster", "cluster1")
		})
	}
}

func TestCollectorSubqueryAndRangeFunctions(t *testing.T) {
	for _, in := range []string{
		`max_over_time(rate(container_cpu_usage_seconds_total{container!=""}[1m])[10m:1m])`,
		`quantile_over_time(0.95, container_memory_working_set_bytes{container!=""}[1h])`,
		`avg_over_time(container_memory_working_set_bytes[10m:1m])`,
		`increase(container_network_receive_errors_total[1h])`,
	} {
		got, err := Rewrite(in, collectorOptsA)
		if err != nil {
			t.Fatalf("Rewrite(%q): %v", in, err)
		}
		if !strings.Contains(got, `"namespace", "$1", "k8s_namespace_name"`) {
			t.Errorf("Rewrite(%q) does not rename labels: %s", in, got)
		}
		assertEverySelectorScoped(t, got, "k8s_cluster_uid", uidA)
	}
	if _, err := Rewrite(`container_memory_working_set_bytes[5m]`, collectorOptsA); err == nil {
		t.Fatal("a bare range selector on a translated metric was accepted")
	}
}

func TestCollectorNotCollected(t *testing.T) {
	allowed := collectorOptsA
	allowed.GatewayMetricsAllowed = true
	for _, in := range []string{
		`sum(rate(container_cpu_cfs_throttled_periods_total[5m])) / sum(rate(container_cpu_cfs_periods_total[5m]))`,
		`rate(node_cpu_seconds_total{mode!="idle"}[5m])`,
		`kubelet_volume_stats_used_bytes`,
		`rate(container_network_receive_packets_dropped_total[5m])`,
		`container_spec_cpu_quota`,
	} {
		for _, opts := range []RewriteOptions{collectorOptsA, allowed} {
			if _, err := Rewrite(in, opts); !errors.Is(err, ErrNotCollected) {
				t.Errorf("Rewrite(%q, gateway=%v) err = %v, want ErrNotCollected", in, opts.GatewayMetricsAllowed, err)
			}
		}
	}
}

func TestCollectorGatewayMetrics(t *testing.T) {
	for _, in := range []string{
		`kube_pod_info{namespace="demo"}`,
		`sum(rate(container_fs_writes_bytes_total[5m]))`,
		`container_cpu_time * on (pod) group_left kube_pod_owner`,
		`{__name__=~"container_.*"}`,
	} {
		if _, err := Rewrite(in, collectorOptsA); !errors.Is(err, ErrNotCollected) {
			t.Errorf("Rewrite(%q) with gateway refused: err = %v, want ErrNotCollected", in, err)
		}
	}

	allowed := RewriteOptions{Matchers: collectorOptsA.Matchers, Dialect: DialectCollector, CollectorUID: uidA, GatewayMetricsAllowed: true}
	got, err := Rewrite(`kube_pod_info{namespace="demo",k8s_cluster_uid="x"}`, allowed)
	if err != nil {
		t.Fatal(err)
	}
	// kube_* keep their own label names and get the cluster name only.
	if got != `kube_pod_info{k8s_cluster="cluster1",k8s_cluster_uid="x",namespace="demo"}` {
		t.Fatalf("got %s", got)
	}

	noUID := RewriteOptions{Matchers: collectorOptsA.Matchers, Dialect: DialectCollector, GatewayMetricsAllowed: true}
	got, err = Rewrite(`kube_pod_info`, noUID)
	if err != nil || got != `kube_pod_info{k8s_cluster="cluster1"}` {
		t.Fatalf("no-uid scope: %s, %v", got, err)
	}
	if _, err := Rewrite(`up`, RewriteOptions{Matchers: collectorOptsA.Matchers, Dialect: DialectCollector}); err == nil {
		t.Fatal("collector scope with neither a UID nor a unique name accepted")
	}
	if _, err := Rewrite(`up`, RewriteOptions{Matchers: uidOpts.Matchers, CollectorUID: uidA}); err == nil {
		t.Fatal("collector UID on a standard scope accepted")
	}
}

func TestCollectorSeriesSelector(t *testing.T) {
	got, err := RewriteSeriesSelector(`container_cpu_usage_seconds_total{namespace="demo"}`, collectorOptsA)
	if err != nil {
		t.Fatal(err)
	}
	if got != `container_cpu_time{k8s_cluster="cluster1",k8s_cluster_uid="`+uidA+`",k8s_namespace_name="demo"}` {
		t.Fatalf("got %s", got)
	}
	if _, err := RewriteSeriesSelector(`kube_pod_info`, collectorOptsA); !errors.Is(err, ErrNotCollected) {
		t.Fatalf("gateway series selector err = %v", err)
	}
}

// FilterResponse with the collector scope checks only the canonical name
// matcher: a translated answer carries k8s_cluster, not necessarily the UID.
func TestCollectorFilterChecksCanonicalMatchers(t *testing.T) {
	body := []byte(`{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"k8s_cluster":"cluster1","namespace":"demo","pod":"web"},"value":[1,"1"]},` +
		`{"metric":{"cluster":"cluster1","namespace":"demo","pod":"web"},"value":[1,"1"]}]}}`)
	out, dropped, err := FilterResponse(body, collectorOptsA.Matchers)
	if err != nil || dropped != 1 || !strings.Contains(string(out), `"k8s_cluster":"cluster1"`) {
		t.Fatalf("out=%s dropped=%d err=%v", out, dropped, err)
	}
}
