package traffic

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestBeylaSource_Detect_MetricProbe(t *testing.T) {
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{}, 42)), nil
		}
		if strings.Contains(query, "beyla_build_info") {
			return promResult("vector", promSeries(map[string]string{"version": "1.0.0"}, 1)), nil
		}
		return emptyResult(), nil
	}

	result, err := src.Detect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Available {
		t.Fatal("expected available=true")
	}
	if result.Native {
		t.Error("expected Native=false")
	}
	if result.Version != "1.0.0" {
		t.Errorf("version = %q, want %q", result.Version, "1.0.0")
	}
}

func TestBeylaSource_Detect_OBIMetricPrefix(t *testing.T) {
	// Grafana's Beyla renames the flow metric to beyla_*; upstream OBI emits
	// obi_*. Both distributions are current, so either name means available, and
	// the one that answered is what GetFlows must go on to query.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "obi_network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{}, 7)), nil
		}
		return emptyResult(), nil
	}

	result, err := src.Detect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Available {
		t.Fatal("expected available=true for the obi_ prefix")
	}
	assertEq(t, "resolved flow metric", src.flowMetricName(), obiFlowMetric)
}

func TestBeylaSource_Detect_AlloyPodsAloneAreNotAvailable(t *testing.T) {
	// app.kubernetes.io/name=alloy matches every Alloy install, and most carry no
	// Beyla. Claiming availability on that basis wins the source priority order
	// and then renders a permanently empty graph, so pods must not imply
	// availability — the message should point at the scrape instead.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alloy-abc",
			Namespace: "monitoring",
			Labels:    map[string]string{"app.kubernetes.io/name": "alloy"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset(pod)}
	src.queryFn = func(_ context.Context, _ string) (*prom.QueryResult, error) {
		return nil, fmt.Errorf("prometheus not available")
	}

	result, err := src.Detect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Available {
		t.Fatal("expected available=false: running Alloy pods are not evidence of Beyla metrics")
	}
	if !strings.Contains(result.Message, "Prometheus holds no Beyla metrics") {
		t.Errorf("message should name the actual problem, got: %q", result.Message)
	}
}

func TestBeylaSource_Detect_BuildInfoWithoutNetworkFeature(t *testing.T) {
	// The network feature is opt-in and off by default, so a stock Beyla install
	// exposes build_info but no flow metric at all. That has to read as "installed
	// but not watching the network", not as "no traffic yet".
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_build_info") {
			return promResult("vector", promSeries(map[string]string{"version": "v3.25.0"}, 1)), nil
		}
		return emptyResult(), nil
	}

	result, err := src.Detect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Available {
		t.Fatal("expected available=false: no network flow metric means no flows to draw")
	}
	assertEq(t, "version", result.Version, "v3.25.0")
	if !strings.Contains(result.Message, "OTEL_EBPF_METRICS_FEATURES") {
		t.Errorf("message should tell the operator how to enable network metrics, got: %q", result.Message)
	}
	// An idle cluster with the feature already on produces the same evidence, so
	// the message must offer that too rather than assert the cause it cannot see.
	if !strings.Contains(result.Message, "no traffic has been observed") {
		t.Errorf("message must not assert the feature is off when idleness looks identical, got: %q", result.Message)
	}
}

func TestBeylaSource_Detect_NotAvailable(t *testing.T) {
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, _ string) (*prom.QueryResult, error) {
		return nil, fmt.Errorf("prometheus not available")
	}

	result, err := src.Detect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Available {
		t.Fatal("expected available=false")
	}
}

func TestBeylaSource_GetFlows_OwnerLevel(t *testing.T) {
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "frontend", "k8s_src_namespace": "web",
				"k8s_src_owner_type": "Deployment",
				"k8s_dst_owner_name": "backend", "k8s_dst_namespace": "api",
				"k8s_dst_owner_type": "Deployment",
				"dst_port":           "8080", "transport": "TCP",
			}, 15.5)), nil
		}
		return emptyResult(), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 flow, got %d", len(resp.Flows))
	}
	f := resp.Flows[0]
	assertEq(t, "source name", f.Source.Name, "frontend")
	assertEq(t, "source namespace", f.Source.Namespace, "web")
	assertEq(t, "source kind", f.Source.Kind, "Workload")
	assertEq(t, "dest name", f.Destination.Name, "backend")
	assertEq(t, "dest kind", f.Destination.Kind, "Workload")
	assertEq(t, "port", fmt.Sprintf("%d", f.Port), "8080")
	assertEq(t, "protocol", f.Protocol, "tcp")
	assertEq(t, "verdict", f.Verdict, "forwarded")
	// No HTTP data on this edge, so there is no rate to report. Beyla exports no
	// connection count at all, and a placeholder here would drive edge thickness
	// and the node totals from a number nothing measured.
	if f.Connections != 0 {
		t.Errorf("connections = %d, want 0: nothing here counts connections", f.Connections)
	}
}

func TestBeylaSource_GetFlows_L7OnlyDroppedWithoutL4Match(t *testing.T) {
	// http_server_request_duration_seconds has no source labels, so an L7
	// series with no matching L4 destination can't be drawn as an edge and
	// must be dropped rather than emitted as a sourceless flow.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return emptyResult(), nil
		}
		return promResult("vector", promSeries(map[string]string{
			"k8s_namespace_name": "api", "k8s_owner_name": "backend",
			"http_request_method": "GET", "http_route": "/api/users", "http_response_status_code": "200",
		}, 8.0)), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 0 {
		t.Fatalf("expected 0 flows (no L4 match to attach HTTP metadata to), got %d", len(resp.Flows))
	}
}

func TestBeylaSource_GetFlows_L4PlusL7(t *testing.T) {
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "frontend", "k8s_src_namespace": "web",
				"k8s_dst_owner_name": "backend", "k8s_dst_namespace": "api",
				"dst_port": "8080", "transport": "TCP",
			}, 10.0)), nil
		}
		return promResult("vector", promSeries(map[string]string{
			"k8s_namespace_name": "api", "k8s_owner_name": "backend", "server_port": "8080",
			"http_request_method": "POST", "http_route": "/api/orders", "http_response_status_code": "201",
		}, 5.0)), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 merged flow, got %d", len(resp.Flows))
	}
	f := resp.Flows[0]
	assertEq(t, "httpMethod", f.HTTPMethod, "POST")
	assertEq(t, "httpPath", f.HTTPPath, "/api/orders")
	// No status is claimed. This series is one of however many status codes the
	// destination serves, so recording it would make the aggregated edge present a
	// single class as the entire distribution — an all-2xx bar over a destination
	// returning 5xx. Failures travel as the error rate instead.
	assertEq(t, "httpStatus", fmt.Sprintf("%d", f.HTTPStatus), "0")
	assertEq(t, "l7Protocol", f.L7Protocol, "HTTP")
	assertEq(t, "port", fmt.Sprintf("%d", f.Port), "8080")
	assertEq(t, "source name", f.Source.Name, "frontend")
}

func TestBeylaSource_GetFlows_L7LandsOnTheServedPortOnly(t *testing.T) {
	// http_server_request_duration_seconds carries server_port, so a destination
	// serving HTTP on 8080 alongside raw TCP on 5432 gets its HTTP metadata on the
	// 8080 edge and nothing on the 5432 edge. No inference, no all-or-nothing
	// guard.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector",
				promSeries(map[string]string{
					"k8s_src_owner_name": "frontend", "k8s_src_namespace": "web",
					"k8s_dst_owner_name": "backend", "k8s_dst_namespace": "api",
					"dst_port": "8080", "transport": "TCP",
				}, 10.0),
				promSeries(map[string]string{
					"k8s_src_owner_name": "frontend", "k8s_src_namespace": "web",
					"k8s_dst_owner_name": "backend", "k8s_dst_namespace": "api",
					"dst_port": "5432", "transport": "TCP",
				}, 3.0),
			), nil
		}
		return promResult("vector", promSeries(map[string]string{
			"k8s_namespace_name": "api", "k8s_owner_name": "backend", "server_port": "8080",
			"http_request_method": "POST", "http_route": "/api/orders", "http_response_status_code": "201",
		}, 5.0)), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 2 {
		t.Fatalf("expected 2 flows, got %d", len(resp.Flows))
	}
	for _, f := range resp.Flows {
		switch f.Port {
		case 8080:
			assertEq(t, "8080 l7Protocol", f.L7Protocol, "HTTP")
			assertEq(t, "8080 httpPath", f.HTTPPath, "/api/orders")
			if f.RequestRate != 5.0 {
				t.Errorf("8080 requestRate = %v, want 5 (sole caller on this port takes the whole rate)", f.RequestRate)
			}
		case 5432:
			if f.L7Protocol != "" {
				t.Errorf("5432 should carry no HTTP metadata, got L7Protocol=%q path=%q", f.L7Protocol, f.HTTPPath)
			}
		default:
			t.Errorf("unexpected port %d", f.Port)
		}
	}
}

func TestBeylaSource_ParseL4Flows_TransportSeparatesOtherwiseIdenticalSeries(t *testing.T) {
	// Same src/dst/port, different transport (DNS on 53) must not collide: the raw
	// transport label is part of l4Key.
	//
	// Deliberately a parser-level test rather than a GetFlows one. The L4 query
	// filters direction="request", and Beyla labels UDP "unknown", so real UDP
	// never reaches the parser through that path — asserting it end to end would
	// only prove the stub ignores the query it was handed.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	result := promResult("vector",
		promSeries(map[string]string{
			"k8s_src_owner_name": "app", "k8s_src_namespace": "web",
			"k8s_dst_owner_name": "coredns", "k8s_dst_namespace": "kube-system",
			"dst_port": "53", "transport": "TCP",
		}, 4.0),
		promSeries(map[string]string{
			"k8s_src_owner_name": "app", "k8s_src_namespace": "web",
			"k8s_dst_owner_name": "coredns", "k8s_dst_namespace": "kube-system",
			"dst_port": "53", "transport": "UDP",
		}, 20.0),
	)

	flows, presence := src.parseL4Flows(result, beylaWindow(0))
	if len(flows) != 2 {
		t.Fatalf("expected 2 flows (TCP and UDP kept apart), got %d", len(flows))
	}
	protocols := map[string]bool{}
	for _, f := range flows {
		protocols[f.Protocol] = true
	}
	if !protocols["tcp"] || !protocols["udp"] {
		t.Errorf("expected both tcp and udp to survive, got %v", protocols)
	}
	if !presence.port || !presence.transport {
		t.Errorf("both attributes were present in the fixture, got port=%v transport=%v", presence.port, presence.transport)
	}
}

func TestBeylaSource_GetFlows_WarningKindSeparatesRetryableFromPermanent(t *testing.T) {
	// The client retries a transient warning and must not retry a permanent one:
	// a source that cannot export a port will not start exporting it on a refetch,
	// and a 2s retry loop against Prometheus is the cost of getting this wrong.
	t.Run("partial data is permanent", func(t *testing.T) {
		src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
		src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
			if strings.Contains(query, `direction="unknown"`) {
				return emptyResult(), nil
			}
			if strings.Contains(query, "beyla_network_flow_bytes_total") {
				return promResult("vector", promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
				}, 9.0)), nil
			}
			return emptyResult(), nil
		}

		resp, err := src.GetFlows(context.Background(), FlowOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Warning == "" {
			t.Fatal("expected a warning about the missing attributes")
		}
		assertEq(t, "warningKind", resp.WarningKind, WarningPartial)
	})

	t.Run("query failure is transient", func(t *testing.T) {
		src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
		src.queryFn = func(_ context.Context, _ string) (*prom.QueryResult, error) {
			return nil, fmt.Errorf("connection refused")
		}

		resp, err := src.GetFlows(context.Background(), FlowOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Warning == "" {
			t.Fatal("expected a warning about the failed query")
		}
		assertEq(t, "warningKind", resp.WarningKind, WarningTransient)
	})
}

func TestBeylaSource_GetFlows_L7NotAttachedWhenItsPortHasNoNamedCaller(t *testing.T) {
	// The destination serves HTTP on 8080, but its only caller there is an
	// external one Beyla can't name, so that edge is dropped. Unrelated TCP
	// traffic on 5432 survives. The HTTP metadata belongs to 8080 and must not
	// land on 5432 just because 5432 is what's left.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector",
				promSeries(map[string]string{
					// no k8s_src_owner_name/k8s_src_name: unresolved external caller
					"k8s_dst_owner_name": "backend", "k8s_dst_namespace": "api",
					"dst_port": "8080", "transport": "TCP",
				}, 10.0),
				promSeries(map[string]string{
					"k8s_src_owner_name": "worker", "k8s_src_namespace": "jobs",
					"k8s_dst_owner_name": "backend", "k8s_dst_namespace": "api",
					"dst_port": "5432", "transport": "TCP",
				}, 3.0),
			), nil
		}
		return promResult("vector", promSeries(map[string]string{
			"k8s_namespace_name": "api", "k8s_owner_name": "backend", "server_port": "8080",
			"http_request_method": "GET", "http_route": "/health", "http_response_status_code": "200",
		}, 5.0)), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 flow (only port 5432 survives naming), got %d", len(resp.Flows))
	}
	if resp.Flows[0].L7Protocol != "" {
		t.Errorf("port %d: expected no HTTP metadata attached to the non-HTTP port, got L7Protocol=%q",
			resp.Flows[0].Port, resp.Flows[0].L7Protocol)
	}
}

func TestBeylaSource_QueryL4_KeepsOnlyTheRequestDirection(t *testing.T) {
	// Beyla emits both directions of every conversation with src and dst swapped,
	// so an unfiltered query gives every edge a mirror twin pointing the wrong
	// way. UDP is worse: it is labelled "unknown" on both sides, and once dst.port
	// is selected the reverse half carries the client's ephemeral port, so a
	// single DNS conversation becomes hundreds of edges. Only "request" is
	// orientable.
	var queries []string
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "network_flow_bytes_total") && strings.Contains(query, "rate(") {
			queries = append(queries, query)
		}
		return emptyResult(), nil
	}

	if _, err := src.GetFlows(context.Background(), FlowOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The edge query is the one grouped by the full label set. A second query reads
	// the response direction for received bytes and must not be mistaken for it.
	var edgeQuery string
	for _, q := range queries {
		if strings.Contains(q, "dst_port") {
			edgeQuery = q
		}
	}
	if edgeQuery == "" {
		t.Fatalf("no edge query was issued (%d flow queries seen)", len(queries))
	}
	if !strings.Contains(edgeQuery, `direction="request"`) {
		t.Errorf("the edge query must keep only the request direction, got: %s", edgeQuery)
	}
	if strings.Contains(beylaL4GroupBy, "direction") {
		t.Error("direction must stay out of the group-by so it cannot split one conversation across two keys")
	}
}

func TestBeylaSource_GetFlows_ServiceAndWorkloadDuplicateCollapsesToWorkload(t *testing.T) {
	// A Service-routed conversation is reported twice with byte-identical values,
	// once attributed to the destination workload and once to the Service in front
	// of it. Emitting both would double the traffic on most edges; letting result
	// order decide would make the rendered Kind arbitrary. The workload wins.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector",
				promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_src_owner_type": "Deployment",
					"k8s_dst_owner_name": "db", "k8s_dst_namespace": "demo",
					"k8s_dst_owner_type": "Service",
					"dst_port":           "6379", "transport": "TCP",
				}, 7.0),
				promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_src_owner_type": "Deployment",
					"k8s_dst_owner_name": "db", "k8s_dst_namespace": "demo",
					"k8s_dst_owner_type": "Deployment",
					"dst_port":           "6379", "transport": "TCP",
				}, 7.0),
			), nil
		}
		return emptyResult(), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected the duplicate pair to collapse to 1 flow, got %d (traffic would be double counted)", len(resp.Flows))
	}
	assertEq(t, "dest kind", resp.Flows[0].Destination.Kind, "Workload")
}

func TestBeylaSource_GetFlows_WarnsWhenPortAndTransportAreNotExported(t *testing.T) {
	// dst.port and transport are Default:false in Beyla's attribute registry, so a
	// stock install exports neither and every edge arrives with no port and no
	// protocol. Rendering port 0 over TCP without saying so is the dishonest part.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
				"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
				// no dst_port, no transport: the default install
			}, 12.0)), nil
		}
		return emptyResult(), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 flow, got %d", len(resp.Flows))
	}
	if resp.Warning == "" {
		t.Fatal("expected a warning naming the missing attributes")
	}
	for _, want := range []string{"dst.port", "transport", "attributes.select"} {
		if !strings.Contains(resp.Warning, want) {
			t.Errorf("warning should mention %q so the operator can act on it, got: %q", want, resp.Warning)
		}
	}
}

func TestBeylaSource_GetFlows_NoWarningWhenAttributesArePresent(t *testing.T) {
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
				"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
				"dst_port": "80", "transport": "TCP",
			}, 12.0)), nil
		}
		return emptyResult(), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Warning != "" {
		t.Errorf("expected no warning when both attributes are exported, got: %q", resp.Warning)
	}
}

func TestBeylaSource_ParseL7Flows_OwnerFallback(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		wantName string
		wantKind string
	}{
		{"owner name", map[string]string{"k8s_owner_name": "backend"}, "backend", "Workload"},
		{"pod fallback", map[string]string{"k8s_pod_name": "backend-abc123"}, "backend-abc123", "Pod"},
	}
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := map[string]string{
				"k8s_namespace_name": "api", "http_request_method": "GET",
				"http_route": "/x", "http_response_status_code": "200",
			}
			for k, v := range tt.labels {
				labels[k] = v
			}
			flows := src.parseL7Flows(promResult("vector", promSeries(labels, 1.0)))
			if len(flows) != 1 {
				t.Fatalf("expected 1 flow, got %d", len(flows))
			}
			assertEq(t, "dest name", flows[0].Destination.Name, tt.wantName)
			assertEq(t, "dest kind", flows[0].Destination.Kind, tt.wantKind)
		})
	}
}

func TestBeylaSource_GetFlows_NamespaceFilter(t *testing.T) {
	var capturedQuery string
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		capturedQuery = query
		return emptyResult(), nil
	}

	_, err := src.GetFlows(context.Background(), FlowOptions{Namespace: "test-ns"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(capturedQuery, "test-ns") {
		t.Errorf("expected namespace filter in query, got: %s", capturedQuery)
	}
}

func TestBeylaSource_GetFlows_FallbackToOwner(t *testing.T) {
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// The unorientable query shares the metric name with the edge query, so a
		// stub matching on the name alone would answer it with oriented flow data
		// and invent edges this test never asked for.
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "api", "k8s_src_namespace": "backend",
				"k8s_dst_owner_name": "db", "k8s_dst_namespace": "data",
				"k8s_src_owner_type": "Deployment", "k8s_dst_owner_type": "StatefulSet",
				"dst_port": "5432", "transport": "TCP",
			}, 3.0)), nil
		}
		return emptyResult(), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 flow, got %d", len(resp.Flows))
	}
	f := resp.Flows[0]
	assertEq(t, "source kind", f.Source.Kind, "Workload")
	assertEq(t, "dest kind", f.Destination.Kind, "Workload")
	assertEq(t, "port", fmt.Sprintf("%d", f.Port), "5432")
}

func TestBeylaSource_MapBeylaKind(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Pod", "Pod"}, {"Deployment", "Workload"}, {"ReplicaSet", "Workload"},
		{"StatefulSet", "Workload"}, {"DaemonSet", "Workload"}, {"Service", "Service"},
		{"Unknown", "Pod"}, {"pod", "Pod"}, {"deployment", "Workload"},
	}
	for _, tt := range tests {
		if got := mapBeylaKind(tt.input); got != tt.want {
			t.Errorf("mapBeylaKind(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBeylaSource_MapBeylaTransport(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"TCP", "tcp"}, {"UDP", "udp"}, {"tcp", "tcp"}, {"Tcp", "tcp"}, {"", "tcp"},
	}
	for _, tt := range tests {
		if got := mapBeylaTransport(tt.input); got != tt.want {
			t.Errorf("mapBeylaTransport(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestManager_DetectSources_IncludesBeyla(t *testing.T) {
	m := &Manager{sources: make(map[string]TrafficSource)}
	m.sources["beyla"] = NewBeylaSource(fake.NewSimpleClientset())
	if _, ok := m.sources["beyla"]; !ok {
		t.Fatal("expected 'beyla' in sources map")
	}
}

func TestBeylaSource_QueryL4_NamespaceFilterIsValidPromQL(t *testing.T) {
	q := beylaRateQuery(beylaL4GroupBy, beylaFlowMetric, "test-ns", beylaL4DirectionFilter, beylaWindow(0))
	if !strings.Contains(q, `k8s_src_namespace="test-ns"}`) || !strings.Contains(q, `k8s_dst_namespace="test-ns"}`) {
		t.Errorf("namespace matchers must live inside the label selector, got: %s", q)
	}
	if strings.Contains(q, " and (") {
		t.Errorf("bare label matchers after `and` are not valid PromQL, got: %s", q)
	}
}

func TestBeylaSource_QueryL7_UsesCorrectMetricAndLabels(t *testing.T) {
	q := beylaL7RateQuery("test-ns", beylaWindow(0))
	if !strings.Contains(q, "http_server_request_duration_seconds_count") {
		t.Errorf("expected the OTel-aligned Beyla HTTP server metric, got: %s", q)
	}
	if !strings.Contains(q, `k8s_namespace_name="test-ns"}`) {
		t.Errorf("expected a single k8s_namespace_name matcher inside the label selector, got: %s", q)
	}
	if strings.Contains(q, "k8s_src_owner_name") || strings.Contains(q, "k8s_dst_owner_name") {
		t.Errorf("L7 query must not reference network-flow-only owner labels, got: %s", q)
	}
}

// --- test helpers ---

func promResult(resultType string, series ...prom.Series) *prom.QueryResult {
	return &prom.QueryResult{ResultType: resultType, Series: series}
}

func promSeries(labels map[string]string, value float64) prom.Series {
	return prom.Series{
		Labels:     labels,
		DataPoints: []prom.DataPoint{{Value: value}},
	}
}

func emptyResult() *prom.QueryResult {
	return &prom.QueryResult{ResultType: "vector", Series: []prom.Series{}}
}

func assertEq(t *testing.T, label, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", label, got, want)
	}
}

func TestBeylaSource_DetectAndPollConcurrently(t *testing.T) {
	// Manager releases its own lock before calling into a source, so a
	// re-detection can land while a StreamFlows goroutine is mid-poll. Detect
	// resolves the metric name and the pollers read it. Meaningful under -race.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if strings.Contains(query, "network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{}, 1)), nil
		}
		return emptyResult(), nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := src.Detect(context.Background()); err != nil {
				t.Errorf("Detect: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := src.GetFlows(context.Background(), FlowOptions{}); err != nil {
				t.Errorf("GetFlows: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestBeylaSource_Detect_JobSelectorMismatchIsNotReportedAsFeatureOff(t *testing.T) {
	// Beyla is installed, scraped, and emitting network metrics — under a job name
	// the selector does not match. Both that and "network feature off" look like
	// "no flow metric", and they need opposite fixes, so the two must not collapse
	// into the same advice.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		// Anything scoped to the job selector finds nothing.
		if strings.Contains(query, "job=~") {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_build_info") {
			return promResult("vector", promSeries(map[string]string{"version": "v3.25.0"}, 1)), nil
		}
		return emptyResult(), nil
	}

	result, err := src.Detect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Available {
		t.Fatal("expected available=false: nothing the flow queries can read")
	}
	if !result.Present {
		t.Error("Beyla is demonstrably running, so Present must be set for the reason to surface")
	}
	if !strings.Contains(result.Message, "beyla-job-selector") {
		t.Errorf("message should point at the job selector, got: %q", result.Message)
	}
	if strings.Contains(result.Message, "OTEL_EBPF_METRICS_FEATURES") {
		t.Errorf("this is not a feature-flag problem and must not be reported as one, got: %q", result.Message)
	}
}

func TestBeylaSource_UnorientableQuery_ScopesNamespaceOnEitherEnd(t *testing.T) {
	// beylaRateQuery treats a namespace filter as "either end of the conversation",
	// so the unorientable query has to match. Filtering on the source alone would
	// miss inbound traffic and would surface conversations from namespaces the user
	// is not looking at.
	var unorientableQuery string
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `direction="unknown"`) {
			unorientableQuery = query
		}
		return emptyResult(), nil
	}

	if _, err := src.GetFlows(context.Background(), FlowOptions{Namespace: "demo"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(unorientableQuery, `k8s_src_namespace="demo"`) || !strings.Contains(unorientableQuery, `k8s_dst_namespace="demo"`) {
		t.Errorf("query must scope on either end, got: %s", unorientableQuery)
	}
}

func TestBeylaSource_GetFlows_MultiPortHTTPSumsWhenEdgesHaveNoPort(t *testing.T) {
	// dst_port is opt-in, so by default every L4 edge carries port 0 while the HTTP
	// metric still reports a distinct server_port per port served. All of that HTTP
	// traffic belongs to the same port-0 edge, so the rates have to be summed.
	// Attaching each port's record in turn overwrites instead, leaving the rate
	// short and the displayed route decided by map iteration order.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			// No dst_port label: the default install.
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
				"k8s_dst_owner_name": "api", "k8s_dst_namespace": "demo",
			}, 40.0)), nil
		}
		return promResult("vector",
			promSeries(map[string]string{
				"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "80",
				"http_request_method": "GET", "http_route": "/health", "http_response_status_code": "200",
			}, 3.0),
			promSeries(map[string]string{
				"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "8080",
				"http_request_method": "POST", "http_route": "/orders", "http_response_status_code": "201",
			}, 7.0),
		), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 flow, got %d", len(resp.Flows))
	}
	f := resp.Flows[0]
	if f.RequestRate != 10.0 {
		t.Errorf("requestRate = %v, want 10 (3 on :80 plus 7 on :8080); a lower value means one port overwrote the other", f.RequestRate)
	}
	// The busiest single series decides the label, so it must be deterministic
	// rather than whichever the map happened to visit last.
	assertEq(t, "httpMethod", f.HTTPMethod, "POST")
	assertEq(t, "httpPath", f.HTTPPath, "/orders")
}

func TestBeylaSource_GetFlows_WarningNamesTheClustersOwnMetric(t *testing.T) {
	// On an OBI install the attributes.select key is obi_network_flow_bytes.
	// Telling the operator to configure the Beyla spelling would not work.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "obi_network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
				"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
			}, 5.0)), nil
		}
		return emptyResult(), nil
	}

	if _, err := src.Detect(context.Background()); err != nil {
		t.Fatalf("detect: %v", err)
	}
	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Warning, "obi_network_flow_bytes") {
		t.Errorf("warning must name this cluster's metric, got: %q", resp.Warning)
	}
	if strings.Contains(resp.Warning, "beyla_network_flow_bytes") {
		t.Errorf("warning must not send an OBI user to the Beyla spelling, got: %q", resp.Warning)
	}
}

func TestBeylaSource_GetFlows_PortedEdgesWinOverThePortZeroLeftover(t *testing.T) {
	// For five minutes after dst.port is added or removed, the rate window holds
	// series from both configurations, so a destination has a port-80 edge and a
	// port-0 edge at once. The port-bearing edge is authoritative; giving the
	// port-0 leftover the destination aggregate as well puts the same HTTP rate on
	// two edges and doubles it in any total.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector",
				promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
					"dst_port": "80", "transport": "TCP",
				}, 20.0),
				promSeries(map[string]string{
					// same conversation, from before dst.port was selected
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
				}, 18.0),
			), nil
		}
		return promResult("vector", promSeries(map[string]string{
			"k8s_namespace_name": "demo", "k8s_owner_name": "web", "server_port": "80",
			"http_request_method": "GET", "http_route": "/", "http_response_status_code": "200",
		}, 6.0)), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var total float64
	byPort := map[int]float64{}
	for _, f := range resp.Flows {
		total += f.RequestRate
		byPort[f.Port] = f.RequestRate
	}
	if total != 6.0 {
		t.Errorf("request rates sum to %v, want 6 — the destination served 6/s and it must not be counted twice", total)
	}
	if byPort[80] != 6.0 {
		t.Errorf("port 80 rate = %v, want 6: the port-bearing edge is the authoritative one", byPort[80])
	}
	if byPort[0] != 0 {
		t.Errorf("the port-0 leftover must not also carry the rate, got %v", byPort[0])
	}
}

func TestBeylaSource_GetFlows_PortedDestinationCountsEvenWhenItsCallerIsNameless(t *testing.T) {
	// A destination's port-bearing traffic can come entirely from a caller Beyla
	// cannot name — an external client — so that series is dropped and no surviving
	// edge carries the port. The destination still has a real port, and the port-0
	// leftover must not be handed the destination's HTTP data: that traffic belongs
	// to the caller who was dropped, not to the named one.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector",
				promSeries(map[string]string{
					// external caller Beyla cannot name: dropped, but :8080 is real
					"k8s_dst_owner_name": "api", "k8s_dst_namespace": "demo",
					"dst_port": "8080", "transport": "TCP",
				}, 30.0),
				promSeries(map[string]string{
					// same destination, leftover series from before dst.port was selected
					"k8s_src_owner_name": "worker", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "api", "k8s_dst_namespace": "demo",
				}, 5.0),
			), nil
		}
		return promResult("vector", promSeries(map[string]string{
			"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "8080",
			"http_request_method": "GET", "http_route": "/v1/items", "http_response_status_code": "200",
		}, 9.0)), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 flow (the nameless caller is dropped), got %d", len(resp.Flows))
	}
	f := resp.Flows[0]
	assertEq(t, "source", f.Source.Name, "worker")
	if f.L7Protocol != "" || f.RequestRate != 0 {
		t.Errorf("the :8080 HTTP traffic belongs to the dropped external caller, not to worker; got l7=%q rate=%v",
			f.L7Protocol, f.RequestRate)
	}
}

func TestBeylaSource_GetFlows_ConnectionsCarryTheRequestRate(t *testing.T) {
	// Beyla measures rates, not connections — the same position Istio is in, which
	// puts its rate in Connections and lets the graph label it req/s. An edge with
	// HTTP traffic reports that rate; an edge without reports nothing, because a
	// placeholder would make every edge the same thickness.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `direction="unknown"`) || strings.Contains(query, `direction="response"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector",
				promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
					"dst_port": "80", "transport": "TCP",
				}, 100.0),
				promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "db", "k8s_dst_namespace": "demo",
					"dst_port": "6379", "transport": "TCP",
				}, 40.0),
			), nil
		}
		return promResult("vector", promSeries(map[string]string{
			"k8s_namespace_name": "demo", "k8s_owner_name": "web", "server_port": "80",
			"http_request_method": "GET", "http_route": "/", "http_response_status_code": "200",
		}, 4.75)), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := map[string]int64{}
	for _, f := range resp.Flows {
		got[f.Destination.Name] = f.Connections
	}
	// RoundRate is what the aggregation uses to turn the same rate into
	// RequestCount, so using it here keeps the graph's edge label and the details
	// panel from reporting two different numbers for one measurement.
	if got["web"] != RoundRate(4.75) || got["web"] != 5 {
		t.Errorf("web connections = %d, want %d — the package's own rate conversion", got["web"], RoundRate(4.75))
	}
	if got["db"] != 0 {
		t.Errorf("db connections = %d, want 0: no HTTP data means no rate to report", got["db"])
	}
}

func TestBeylaSource_GetFlows_FractionalRateStillCountsAsTraffic(t *testing.T) {
	// Istio floors a sub-1 rate to 1 rather than 0, because traffic below one
	// request per second is still traffic and a zero would read as none.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `direction="unknown"`) || strings.Contains(query, `direction="response"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
				"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
				"dst_port": "80", "transport": "TCP",
			}, 10.0)), nil
		}
		return promResult("vector", promSeries(map[string]string{
			"k8s_namespace_name": "demo", "k8s_owner_name": "web", "server_port": "80",
			"http_request_method": "GET", "http_route": "/", "http_response_status_code": "200",
		}, 0.2)), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 || resp.Flows[0].Connections != 1 {
		t.Fatalf("want a single flow reporting 1, got %+v", resp.Flows)
	}
}

func TestBeylaSource_GetFlows_FillsLatencyAndErrorRateLikeTheOtherSources(t *testing.T) {
	// Hubble fills LatencyNs and Istio fills ErrorRate, and Beyla exports the data
	// for both. Without them the flow list's Latency column reads "—" on every row
	// and the graph's "Errors (5xx)" legend can never light up.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		switch {
		case strings.Contains(query, `direction="unknown"`), strings.Contains(query, `direction="response"`):
			return emptyResult(), nil
		case strings.Contains(query, "beyla_network_flow_bytes_total"):
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
				"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
				"dst_port": "80", "transport": "TCP",
			}, 100.0)), nil
		case strings.Contains(query, `http_response_status_code=~"5.."`):
			return promResult("vector", promSeries(map[string]string{
				"k8s_namespace_name": "demo", "k8s_owner_name": "web", "server_port": "80",
			}, 0.308)), nil
		case strings.Contains(query, "http_server_request_duration_seconds_sum"):
			// The query divides sum by count; the stub returns the quotient.
			return promResult("vector", promSeries(map[string]string{
				"k8s_namespace_name": "demo", "k8s_owner_name": "web", "server_port": "80",
			}, 0.000101133)), nil
		}
		return promResult("vector", promSeries(map[string]string{
			"k8s_namespace_name": "demo", "k8s_owner_name": "web", "server_port": "80",
			"http_request_method": "GET", "http_route": "/", "http_response_status_code": "200",
		}, 5.0)), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 flow, got %d", len(resp.Flows))
	}
	f := resp.Flows[0]
	if f.LatencyNs != 101133 {
		t.Errorf("latencyNs = %d, want 101133 (0.000101133s expressed in nanoseconds)", f.LatencyNs)
	}
	if f.ErrorRate != 0.308 {
		t.Errorf("errorRate = %v, want 0.308", f.ErrorRate)
	}
	// Istio marks the flow errored once any 5xx is present, and the graph colours
	// the edge from the verdict.
	assertEq(t, "verdict", f.Verdict, "error")
}

func TestBeylaSource_GetFlows_ReceivedBytesComeFromTheResponseDirection(t *testing.T) {
	// The response half of a conversation cannot be drawn as an edge — that is what
	// produced the mirror edges — but it is the true count of bytes coming back, so
	// it fills BytesRecv instead of being discarded. Istio fills the same field from
	// istio_response_bytes_sum.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		switch {
		case strings.Contains(query, `direction="unknown"`):
			return emptyResult(), nil
		case strings.Contains(query, `direction="response"`):
			// Runs destination-to-source: web answering client.
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "web", "k8s_src_namespace": "demo",
				"k8s_dst_owner_name": "client", "k8s_dst_namespace": "demo",
			}, 10.0)), nil
		case strings.Contains(query, "beyla_network_flow_bytes_total"):
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
				"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
				"dst_port": "80", "transport": "TCP",
			}, 4.0)), nil
		}
		return emptyResult(), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 flow, got %d", len(resp.Flows))
	}
	f := resp.Flows[0]
	// Rates are converted to absolute counts over the window, as Istio does.
	if f.BytesSent != 4*beylaRateWindowSeconds {
		t.Errorf("bytesSent = %d, want %d", f.BytesSent, 4*beylaRateWindowSeconds)
	}
	if f.BytesRecv != 10*beylaRateWindowSeconds {
		t.Errorf("bytesRecv = %d, want %d — the response direction carries it", f.BytesRecv, 10*beylaRateWindowSeconds)
	}
}

func TestBeylaSource_GetFlows_UnorientableConversationBecomesOneUndirectedEdge(t *testing.T) {
	// Beyla labels both halves of a UDP conversation direction="unknown", so
	// neither can be called the request. Dropping them removed DNS from the map and
	// the workload on the other end with it; drawing both produced a mirrored pair
	// of arrows. One edge, no direction claimed, bytes both ways.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		switch {
		case strings.Contains(query, `direction="unknown"`):
			return promResult("vector",
				promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_src_owner_type": "Deployment",
					"k8s_dst_owner_name": "coredns", "k8s_dst_namespace": "kube-system",
					"k8s_dst_owner_type": "Deployment", "transport": "UDP",
				}, 10.0),
				promSeries(map[string]string{
					"k8s_src_owner_name": "coredns", "k8s_src_namespace": "kube-system",
					"k8s_src_owner_type": "Deployment",
					"k8s_dst_owner_name": "client", "k8s_dst_namespace": "demo",
					"k8s_dst_owner_type": "Deployment", "transport": "UDP",
				}, 25.0),
			), nil
		case strings.Contains(query, `direction="response"`):
			return emptyResult(), nil
		}
		return emptyResult(), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected the pair to collapse to 1 edge, got %d", len(resp.Flows))
	}
	f := resp.Flows[0]
	if !f.DirectionUnknown {
		t.Error("the edge must declare that its direction is unknown, or the graph will draw an arrow")
	}
	assertEq(t, "protocol", f.Protocol, "udp")
	// Endpoints are ordered deterministically so the edge does not flip between
	// polls; demo/client sorts before kube-system/coredns.
	assertEq(t, "source", f.Source.Name, "client")
	assertEq(t, "destination", f.Destination.Name, "coredns")
	if f.BytesSent != 10*beylaRateWindowSeconds || f.BytesRecv != 25*beylaRateWindowSeconds {
		t.Errorf("bytes = %d/%d, want %d/%d — both halves belong to the one edge",
			f.BytesSent, f.BytesRecv, 10*beylaRateWindowSeconds, 25*beylaRateWindowSeconds)
	}
	// Beyla reports no request count for an unorientable conversation, and the
	// graph omits the label rather than printing a zero.
	if f.Connections != 0 || f.RequestRate != 0 {
		t.Errorf("connections/rate = %d/%v, want 0/0: nothing here counts requests", f.Connections, f.RequestRate)
	}
}

func TestBeylaSource_GetFlows_UnorientablePairsCollapseAcrossEphemeralPorts(t *testing.T) {
	// With dst.port selected the reverse half carries the client's ephemeral port,
	// which is what turned one conversation into hundreds of edges. Grouping the
	// pair without the port is what keeps it to one.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if !strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		series := []prom.Series{promSeries(map[string]string{
			"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
			"k8s_dst_owner_name": "coredns", "k8s_dst_namespace": "kube-system",
			"transport": "UDP", "dst_port": "53",
		}, 5.0)}
		for _, port := range []string{"41866", "41864", "37221", "52001"} {
			series = append(series, promSeries(map[string]string{
				"k8s_src_owner_name": "coredns", "k8s_src_namespace": "kube-system",
				"k8s_dst_owner_name": "client", "k8s_dst_namespace": "demo",
				"transport": "UDP", "dst_port": port,
			}, 2.0))
		}
		return promResult("vector", series...), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("five series for one conversation must collapse to 1 edge, got %d", len(resp.Flows))
	}
	// The four reverse series sum into the return direction.
	if want := int64(4 * 2.0 * beylaRateWindowSeconds); resp.Flows[0].BytesRecv != want {
		t.Errorf("bytesRecv = %d, want %d (all four ephemeral-port series)", resp.Flows[0].BytesRecv, want)
	}
}

func TestBeylaSource_GetFlows_ReceivedBytesSplitAcrossAPairsEdges(t *testing.T) {
	// Received bytes are known per conversation, not per port — the response series
	// carry ephemeral ports. When a pair has more than one edge, copying the total
	// onto each counts the same return traffic once per port; observed live as the
	// identical recv figure on a destination's port-80 and port-0 edges.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		switch {
		case strings.Contains(query, `direction="unknown"`):
			return emptyResult(), nil
		case strings.Contains(query, `direction="response"`):
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "web", "k8s_src_namespace": "demo",
				"k8s_dst_owner_name": "client", "k8s_dst_namespace": "demo",
			}, 30.0)), nil
		case strings.Contains(query, "beyla_network_flow_bytes_total"):
			return promResult("vector",
				promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
					"dst_port": "80", "transport": "TCP",
				}, 30.0),
				promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
					"dst_port": "8080", "transport": "TCP",
				}, 10.0),
			), nil
		}
		return emptyResult(), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	total := int64(0)
	for _, f := range resp.Flows {
		total += f.BytesRecv
	}
	want := int64(30 * beylaRateWindowSeconds)
	if total != want {
		t.Errorf("received bytes total %d across the pair's edges, want %d — it must be divided, not copied", total, want)
	}
	// Divided by share of bytes sent: 30 and 10 means three quarters and one quarter.
	byPort := map[int]int64{}
	for _, f := range resp.Flows {
		byPort[f.Port] = f.BytesRecv
	}
	if byPort[80] <= byPort[8080] {
		t.Errorf("the busier port should take the larger share, got %d on :80 and %d on :8080", byPort[80], byPort[8080])
	}
}

func TestBeylaSource_GetFlows_MultiPortLatencyAndErrorsFollowTheSamePathAsTheRate(t *testing.T) {
	// Without dst_port every edge carries port 0, so a destination's HTTP traffic
	// from all its ports lands on one edge and the rate is summed across them.
	// Latency and 5xx rate were still being looked up for a single port, so a
	// multi-port destination under-reported its errors.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		switch {
		case strings.Contains(query, `direction="unknown"`), strings.Contains(query, `direction="response"`):
			return emptyResult(), nil
		case strings.Contains(query, "beyla_network_flow_bytes_total"):
			// No dst_port: the default install.
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
				"k8s_dst_owner_name": "api", "k8s_dst_namespace": "demo",
			}, 50.0)), nil
		case strings.Contains(query, `http_response_status_code=~"5.."`):
			return promResult("vector",
				promSeries(map[string]string{"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "80"}, 0.2),
				promSeries(map[string]string{"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "8080"}, 0.5),
			), nil
		case strings.Contains(query, "http_server_request_duration_seconds_sum"):
			return promResult("vector",
				promSeries(map[string]string{"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "80"}, 0.001),
				promSeries(map[string]string{"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "8080"}, 0.004),
			), nil
		}
		return promResult("vector",
			promSeries(map[string]string{
				"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "80",
				"http_request_method": "GET", "http_route": "/a", "http_response_status_code": "200",
			}, 3.0),
			promSeries(map[string]string{
				"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "8080",
				"http_request_method": "GET", "http_route": "/b", "http_response_status_code": "200",
			}, 7.0),
		), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 flow, got %d", len(resp.Flows))
	}
	f := resp.Flows[0]
	if f.RequestRate != 10 {
		t.Errorf("requestRate = %v, want 10 (3 + 7 across both ports)", f.RequestRate)
	}
	// Both ports' errors belong to this edge, since both ports' traffic does.
	if f.ErrorRate != 0.7 {
		t.Errorf("errorRate = %v, want 0.7 (0.2 + 0.5); a single port's figure under-reports", f.ErrorRate)
	}
	// Latencies are means, so the edge takes the worst rather than summing them.
	if f.LatencyNs != uint64(0.004*float64(time.Second)) {
		t.Errorf("latencyNs = %d, want %d (the slower of the two ports)", f.LatencyNs, uint64(0.004*float64(time.Second)))
	}
}

func TestBeylaSource_GetFlows_PortedEdgesTakeTheirOwnPortsFigures(t *testing.T) {
	// With dst.port selected each edge has a real port, so latency and errors must
	// come from that port rather than from the destination-wide figure. Every other
	// ported fixture has a single port, where the two are equal and indistinguishable.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		switch {
		case strings.Contains(query, `direction="unknown"`), strings.Contains(query, `direction="response"`):
			return emptyResult(), nil
		case strings.Contains(query, "beyla_network_flow_bytes_total"):
			return promResult("vector",
				promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "api", "k8s_dst_namespace": "demo",
					"dst_port": "80", "transport": "TCP",
				}, 10.0),
				promSeries(map[string]string{
					"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "api", "k8s_dst_namespace": "demo",
					"dst_port": "8080", "transport": "TCP",
				}, 10.0),
			), nil
		case strings.Contains(query, `http_response_status_code=~"5.."`):
			return promResult("vector",
				promSeries(map[string]string{"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "80"}, 0.1),
				promSeries(map[string]string{"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "8080"}, 0.9),
			), nil
		case strings.Contains(query, "http_server_request_duration_seconds_sum"):
			return promResult("vector",
				promSeries(map[string]string{"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "80"}, 0.001),
				promSeries(map[string]string{"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "8080"}, 0.009),
			), nil
		}
		return promResult("vector",
			promSeries(map[string]string{
				"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "80",
				"http_request_method": "GET", "http_route": "/a", "http_response_status_code": "200",
			}, 4.0),
			promSeries(map[string]string{
				"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "8080",
				"http_request_method": "GET", "http_route": "/b", "http_response_status_code": "200",
			}, 4.0),
		), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byPort := map[int]Flow{}
	for _, f := range resp.Flows {
		byPort[f.Port] = f
	}
	if len(byPort) != 2 {
		t.Fatalf("expected an edge per port, got %d", len(byPort))
	}
	if got := byPort[80].LatencyNs; got != uint64(0.001*float64(time.Second)) {
		t.Errorf(":80 latency = %d, want %d — its own port's figure, not the destination's worst",
			got, uint64(0.001*float64(time.Second)))
	}
	if got := byPort[8080].LatencyNs; got != uint64(0.009*float64(time.Second)) {
		t.Errorf(":8080 latency = %d, want %d", got, uint64(0.009*float64(time.Second)))
	}
	if byPort[80].ErrorRate != 0.1 || byPort[8080].ErrorRate != 0.9 {
		t.Errorf("error rates = %v/%v, want 0.1/0.9 per port", byPort[80].ErrorRate, byPort[8080].ErrorRate)
	}
}

func TestBeylaSource_GetFlows_DestinationFiguresSplitBetweenCallers(t *testing.T) {
	// The HTTP metric has no caller labels, so everything derived from it is the
	// destination's and must be divided the same way between its callers. Splitting
	// the request rate while copying the error rate whole lets an edge report more
	// errors than requests, which the graph renders as a percentage above 100.
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		switch {
		case strings.Contains(query, `direction="unknown"`), strings.Contains(query, `direction="response"`):
			return emptyResult(), nil
		case strings.Contains(query, "beyla_network_flow_bytes_total"):
			// Two callers, three-to-one by volume.
			return promResult("vector",
				promSeries(map[string]string{
					"k8s_src_owner_name": "busy", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "api", "k8s_dst_namespace": "demo",
				}, 30.0),
				promSeries(map[string]string{
					"k8s_src_owner_name": "quiet", "k8s_src_namespace": "demo",
					"k8s_dst_owner_name": "api", "k8s_dst_namespace": "demo",
				}, 10.0),
			), nil
		case strings.Contains(query, `http_response_status_code=~"5.."`):
			return promResult("vector", promSeries(map[string]string{
				"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "80",
			}, 4.0)), nil
		case strings.Contains(query, "http_server_request_duration_seconds_sum"):
			return emptyResult(), nil
		}
		return promResult("vector", promSeries(map[string]string{
			"k8s_namespace_name": "demo", "k8s_owner_name": "api", "server_port": "80",
			"http_request_method": "GET", "http_route": "/", "http_response_status_code": "500",
		}, 8.0)), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var rate, errRate float64
	for _, f := range resp.Flows {
		rate += f.RequestRate
		errRate += f.ErrorRate
		if f.ErrorRate > f.RequestRate {
			t.Errorf("%s: errorRate %v exceeds requestRate %v — that renders above 100%%",
				f.Source.Name, f.ErrorRate, f.RequestRate)
		}
	}
	if rate != 8.0 {
		t.Errorf("request rates sum to %v, want 8 — the destination's rate, divided not duplicated", rate)
	}
	if errRate != 4.0 {
		t.Errorf("error rates sum to %v, want 4 — divided on the same basis as the requests", errRate)
	}
}

// Two series colliding on an l4Key are attributions of one conversation, not two
// conversations. Beyla reports a Service-routed conversation twice with identical
// values — once for the workload, once for the Service — and the owner types are
// deliberately absent from the key, so both land together. Adding them would
// double every Service-routed edge, which is most of them.
func TestPreferL4TreatsCollidingSeriesAsOneConversation(t *testing.T) {
	flow := func(dstKind string, sent, recv int64) *Flow {
		return &Flow{
			Source:      Endpoint{Namespace: "demo", Name: "client", Kind: "Workload"},
			Destination: Endpoint{Namespace: "demo", Name: "web", Kind: dstKind},
			BytesSent:   sent,
			BytesRecv:   recv,
		}
	}

	workload := flow("Workload", 400, 900)
	service := flow("Service", 400, 900)

	won := preferL4(service, workload)
	if won.Destination.Kind == "Service" {
		t.Error("the workload attribution must win: the graph navigates to workloads")
	}
	if won.BytesSent != 400 {
		t.Errorf("the conversation's bytes must not double: got %d, want 400", won.BytesSent)
	}
	if won.BytesRecv != 900 {
		t.Errorf("received bytes must survive the swap: got %d, want 900", won.BytesRecv)
	}

	// Order must not change the answer — Prometheus returns series in no
	// particular order, and an order-dependent rule renders differently per poll.
	reversed := preferL4(flow("Workload", 400, 900), flow("Service", 400, 900))
	if reversed.Destination.Kind == "Service" || reversed.BytesSent != 400 {
		t.Errorf("result must not depend on arrival order: got kind=%q sent=%d",
			reversed.Destination.Kind, reversed.BytesSent)
	}

	// Neither is a Service copy: still one conversation under two owner types,
	// reported with the same values. Taking the larger keeps its real size.
	tied := preferL4(flow("Workload", 400, 900), flow("Workload", 400, 900))
	if tied.BytesSent != 400 {
		t.Errorf("two attributions of one conversation must not sum: got %d, want 400", tied.BytesSent)
	}
	if tied.BytesRecv != 900 {
		t.Errorf("received bytes must not sum either: got %d, want 900", tied.BytesRecv)
	}
}

// The L7 detail queries group by pod, so a destination with several replicas
// reports the same owner and port once per pod. Those have to combine: overwriting
// reports one replica's figure as the whole destination's, and which replica wins
// is whatever order Prometheus returned.
func TestQueryL7DetailCombinesReplicasOnOnePort(t *testing.T) {
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		replica := func(pod string, val float64) prom.Series {
			return promSeries(map[string]string{
				"k8s_namespace_name": "demo",
				"k8s_owner_name":     "web",
				"k8s_pod_name":       pod,
				"server_port":        "80",
			}, val)
		}
		if strings.Contains(query, `http_response_status_code=~"5.."`) {
			return promResult("vector", replica("web-a", 0.5), replica("web-b", 0.25)), nil
		}
		if strings.Contains(query, "http_server_request_duration_seconds_sum") {
			return promResult("vector", replica("web-a", 0.010), replica("web-b", 0.040)), nil
		}
		return emptyResult(), nil
	}

	latency, errors, _ := src.queryL7Detail(context.Background(), FlowOptions{})
	key := dstPortKey{"demo", "web", 80}

	got, ok := errors.perPort[key]
	if !ok {
		t.Fatal("no per-port error rate recorded")
	}
	if math.Abs(got-0.75) > 1e-9 {
		t.Errorf("both replicas' 5xx must count: got %v, want 0.75", got)
	}

	// Latencies are means, so they take the worst rather than adding.
	lat, ok := latency.perPort[key]
	if !ok {
		t.Fatal("no per-port latency recorded")
	}
	if math.Abs(lat-0.040) > 1e-9 {
		t.Errorf("combined latency is the slowest replica, not a sum: got %v, want 0.040", lat)
	}
}

// Exporting dst.port buys per-port edges and costs accurate received bytes: each
// reply is labelled with the client's short-lived port, so most reply counters are
// never observed twice and no rate can be derived from them. The figure still
// renders, so the user has to be told it is understated.
func TestGetFlowsWarnsWhenMostRepliesCannotBeMeasured(t *testing.T) {
	// Nothing measured as lost, nothing to say.
	silent := l4LabelPresence{metric: beylaFlowMetric, port: true, transport: true}
	if w := silent.warning(3); strings.Contains(w, "far lower than the real traffic") {
		t.Errorf("no measured loss must produce no warning, got: %s", w)
	}

	// Most replies unmeasurable: say so, say what is still trustworthy, and say
	// what to change.
	p := l4LabelPresence{metric: beylaFlowMetric, port: true, transport: true, replyLossFraction: 0.91}
	w := p.warning(3)
	for _, want := range []string{
		"Received-byte figures",
		"Bytes sent, request rate, errors and latency are not affected",
		"Remove dst.port",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("warning must contain %q, got: %s", want, w)
		}
	}

	// A handful of unmeasurable replies among thousands does not move the figure.
	quiet := l4LabelPresence{metric: beylaFlowMetric, port: true, transport: true, replyLossFraction: 0.02}
	if strings.Contains(quiet.warning(3), "far lower than the real traffic") {
		t.Error("a negligible loss must not raise a warning")
	}

	// And the advice to enable dst.port must state what it costs, so nobody is
	// walked into this trade without being told.
	missing := l4LabelPresence{metric: beylaFlowMetric}
	if !strings.Contains(missing.warning(3), "received-byte figures would become unreliable") {
		t.Errorf("recommending dst.port must state its cost, got: %s", missing.warning(3))
	}

	// Each attribute has its own consequence and either can be missing alone. Naming
	// both regardless tells an operator who already fixed one that nothing changed.
	portOnly := l4LabelPresence{metric: beylaFlowMetric, transport: true}
	if w := portOnly.warning(3); strings.Contains(w, "UDP is shown as TCP") {
		t.Errorf("transport is exported, so UDP is not shown as TCP: %s", w)
	}
	transportOnly := l4LabelPresence{metric: beylaFlowMetric, port: true}
	if w := transportOnly.warning(3); strings.Contains(w, "port 0") {
		t.Errorf("dst.port is exported, so edges are not on port 0: %s", w)
	}

	// The sentence explaining the loss only renders once dst.port is exported, and
	// this advice only renders while it is not, so it cannot refer back to it.
	if w := missing.warning(3); strings.Contains(w, "for the same reason") {
		t.Errorf("advice must not point at an explanation the reader has not seen: %s", w)
	}
}

// The probe reads a fraction; anything outside (0,1] means the query was not
// answered and must not drive a warning.
func TestReplyLossFractionRejectsAnImpossibleFraction(t *testing.T) {
	for _, tc := range []struct {
		name      string
		val, want float64
	}{
		{"a byte rate, not a fraction", 15.5, 0},
		{"exactly none lost", 0, 0},
		{"negative", -0.5, 0},
		{"most lost", 0.91, 0.91},
		{"everything lost", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
			src.queryFn = func(_ context.Context, _ string) (*prom.QueryResult, error) {
				return promResult("vector", promSeries(map[string]string{}, tc.val)), nil
			}
			if got := src.replyLossFraction(context.Background(), beylaWindow(0)); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// The traffic view's time range reaches the queries. It is a control the user sets
// on every visit, and Beyla used to rate over a fixed five minutes whatever they
// chose, so picking an hour changed nothing on screen.
func TestBeylaWindowFollowsTheRequestedRange(t *testing.T) {
	for _, tc := range []struct {
		name    string
		since   time.Duration
		promQL  string
		seconds float64
	}{
		{"unset falls back to the view's own default", 0, "5m", 300},
		// Prometheus needs two samples inside the window; at a 15s scrape a
		// sub-minute span is a coin toss, so it is not offered as a real answer.
		{"below a scrapeable span falls back", 30 * time.Second, "5m", 300},
		{"a minute is the shortest honoured", time.Minute, "60s", 60},
		{"quarter hour", 15 * time.Minute, "900s", 900},
		{"an hour", time.Hour, "3600s", 3600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := beylaWindow(tc.since)
			if w.promQL != tc.promQL || w.seconds != tc.seconds {
				t.Errorf("got %q/%v, want %q/%v", w.promQL, w.seconds, tc.promQL, tc.seconds)
			}
		})
	}
}

// The window has to reach both halves: the rate query that measures, and the
// multiplication that turns that rate back into a total for the window. Using one
// window to measure and another to scale reports an hour of traffic as five
// minutes of it.
func TestGetFlowsRatesAndScalesOverTheRequestedWindow(t *testing.T) {
	var queries []string
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		queries = append(queries, query)
		if strings.Contains(query, `direction="unknown"`) {
			return emptyResult(), nil
		}
		if strings.Contains(query, "beyla_network_flow_bytes_total") {
			return promResult("vector", promSeries(map[string]string{
				"k8s_src_owner_name": "client", "k8s_src_namespace": "demo",
				"k8s_src_owner_type": "Deployment",
				"k8s_dst_owner_name": "web", "k8s_dst_namespace": "demo",
				"k8s_dst_owner_type": "Deployment",
			}, 10.0)), nil
		}
		return emptyResult(), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{Since: time.Hour})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rated bool
	for _, q := range queries {
		if strings.Contains(q, "[3600s]") {
			rated = true
		}
		if strings.Contains(q, "[5m]") {
			t.Errorf("a query still rates over a fixed five minutes: %s", q)
		}
	}
	if !rated {
		t.Error("no query rated over the requested hour")
	}

	if len(resp.Flows) != 1 {
		t.Fatalf("expected 1 flow, got %d", len(resp.Flows))
	}
	// 10 B/s across an hour, not across the old fixed 300 seconds.
	if got := resp.Flows[0].BytesSent; got != 36000 {
		t.Errorf("bytes must be scaled by the requested window: got %d, want 36000", got)
	}
}

func TestBeylaSource_GetFlows_FailedEnrichmentIsReportedNotZeroed(t *testing.T) {
	isL7Rate := func(q string) bool {
		return strings.Contains(q, beylaL7Metric) && !strings.Contains(q, "_sum") && !strings.Contains(q, `=~"5.."`)
	}
	for _, tc := range []struct {
		name  string
		fails func(query string) bool
		want  string
	}{
		{"request rates", isL7Rate, "HTTP request rates"},
		{"latency", func(q string) bool { return strings.Contains(q, "http_server_request_duration_seconds_sum") }, "HTTP latency"},
		{"5xx", func(q string) bool { return strings.Contains(q, `http_response_status_code=~"5.."`) }, "HTTP 5xx error rates"},
		{"received bytes", func(q string) bool { return strings.Contains(q, `direction="response"`) }, "received bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
			src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
				if tc.fails(query) {
					return nil, fmt.Errorf("query timed out")
				}
				if strings.Contains(query, `direction="unknown"`) {
					return emptyResult(), nil
				}
				if strings.Contains(query, "beyla_network_flow_bytes_total") && !strings.Contains(query, `direction="response"`) {
					return promResult("vector", promSeries(map[string]string{
						"k8s_src_owner_name": "frontend", "k8s_src_namespace": "web",
						"k8s_dst_owner_name": "backend", "k8s_dst_namespace": "api",
						"dst_port": "8080", "transport": "TCP",
					}, 10.0)), nil
				}
				return emptyResult(), nil
			}

			resp, err := src.GetFlows(context.Background(), FlowOptions{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.Flows) != 1 {
				t.Fatalf("the L4 edge must still be returned, got %d flows", len(resp.Flows))
			}
			if !strings.Contains(resp.Warning, tc.want) {
				t.Errorf("warning = %q, want it to name %q", resp.Warning, tc.want)
			}
			assertEq(t, "warningKind", resp.WarningKind, WarningPartial)
		})
	}
}
