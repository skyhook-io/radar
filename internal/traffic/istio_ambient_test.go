package traffic

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	promclient "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

func istioSeries(metric, reporter, srcNs, src, dstNs, dst, code string, value float64) istioRawSeries {
	return istioRawSeries{metric, map[string]string{
		"source_workload": src, "source_workload_namespace": srcNs,
		"destination_workload": dst, "destination_workload_namespace": dstNs,
		"destination_service_name": dst, "request_protocol": "http",
		"reporter": reporter, "response_code": code,
	}, value}
}

func flowsByPair(flows []Flow) map[string]Flow {
	out := map[string]Flow{}
	for _, f := range flows {
		out[f.Protocol+" "+f.Source.Namespace+"/"+f.Source.Workload+" -> "+f.Destination.Namespace+"/"+f.Destination.Workload] = f
	}
	return out
}

// In ambient mode the waypoint is the only L7 observer: istio_requests_total
// carries reporter="waypoint" and nothing reports "destination".
func TestIstioGetFlows_ReadsWaypointReports(t *testing.T) {
	raw := []istioRawSeries{
		istioSeries("istio_requests_total", "waypoint", "amb", "curl", "amb", "httpbin", "200", 9),
		istioSeries("istio_requests_total", "waypoint", "amb", "curl", "amb", "httpbin", "503", 1),
		istioSeries("istio_request_bytes_sum", "waypoint", "amb", "curl", "amb", "httpbin", "200", 100),
		istioSeries("istio_response_bytes_sum", "waypoint", "amb", "curl", "amb", "httpbin", "200", 400),
	}
	resp, err := newTestIstio(fakeIstioProm(raw, nil)).GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Flows) != 1 {
		t.Fatalf("want the waypoint-reported request as one flow, got %+v", resp.Flows)
	}
	f := resp.Flows[0]
	if f.Source.Workload != "curl" || f.Destination.Workload != "httpbin" {
		t.Errorf("flow %s -> %s, want curl -> httpbin", f.Source.Workload, f.Destination.Workload)
	}
	if !approx(f.RequestRate, 10) || !approx(f.ErrorRate, 1) {
		t.Errorf("req %v err %v, want 10 / 1", f.RequestRate, f.ErrorRate)
	}
	if f.BytesSent != 100*300 || f.BytesRecv != 400*300 {
		t.Errorf("bytes %d/%d, want %d/%d", f.BytesSent, f.BytesRecv, 100*300, 400*300)
	}
}

// A mesh mixing sidecars and ambient: each request is read from its receiving
// side only, so a client sidecar's reporter="source" copy is never added in.
func TestIstioGetFlows_MixedMeshCountsEachRequestOnce(t *testing.T) {
	raw := []istioRawSeries{
		// sidecar client -> sidecar server
		istioSeries("istio_requests_total", "source", "side", "curl", "side", "httpbin", "200", 4),
		istioSeries("istio_requests_total", "destination", "side", "curl", "side", "httpbin", "200", 4),
		// sidecar client -> ambient server behind a waypoint
		istioSeries("istio_requests_total", "source", "side", "curl", "amb", "httpbin", "200", 3),
		istioSeries("istio_requests_total", "waypoint", "side", "curl", "amb", "httpbin", "200", 3),
	}
	resp, err := newTestIstio(fakeIstioProm(raw, nil)).GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := flowsByPair(resp.Flows)
	if len(got) != 2 {
		t.Fatalf("want two flows, got %+v", got)
	}
	if f := got["http side/curl -> side/httpbin"]; !approx(f.RequestRate, 4) {
		t.Errorf("sidecar pair rate = %v, want 4", f.RequestRate)
	}
	if f := got["http side/curl -> amb/httpbin"]; !approx(f.RequestRate, 3) {
		t.Errorf("waypoint pair rate = %v, want 3", f.RequestRate)
	}
}

// ztunnel reports the hops into and out of a waypoint beside the
// client->server traffic. The hops are dropped; a workload that merely shares
// the waypoint's name in another namespace is not.
func TestIstioGetFlows_DropsWaypointHops(t *testing.T) {
	tcp := func(reporter, srcNs, src, dstNs, dst string, v float64) istioRawSeries {
		return istioRawSeries{istioTCPMetric, map[string]string{
			"source_workload": src, "source_workload_namespace": srcNs,
			"destination_workload": dst, "destination_workload_namespace": dstNs,
			"destination_service_name": dst, "reporter": reporter,
		}, v}
	}
	raw := []istioRawSeries{
		istioSeries("istio_requests_total", "waypoint", "amb", "curl", "amb", "httpbin", "200", 5),
		tcp("destination", "amb", "curl", "amb", "waypoint", 1),
		tcp("destination", "amb", "waypoint", "amb", "httpbin", 1),
		tcp("destination", "other", "curl", "other", "waypoint", 2),
	}
	s := newTestIstio(fakeIstioProm(raw, nil))
	s.waypointsFn = func() map[istioWorkload]bool { return map[istioWorkload]bool{{"amb", "waypoint"}: true} }
	resp, err := s.GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := flowsByPair(resp.Flows)
	if len(got) != 2 {
		t.Fatalf("want the client->server request and the unrelated workload named waypoint, got %+v", got)
	}
	if _, ok := got["http amb/curl -> amb/httpbin"]; !ok {
		t.Errorf("client->server request missing: %+v", got)
	}
	if _, ok := got["tcp other/curl -> other/waypoint"]; !ok {
		t.Errorf("a non-waypoint workload named waypoint was dropped: %+v", got)
	}
}

func TestIstioGetFlows_KeepsHopsWhenWaypointsAreUnknown(t *testing.T) {
	raw := []istioRawSeries{{istioTCPMetric, map[string]string{
		"source_workload": "waypoint", "source_workload_namespace": "amb",
		"destination_workload": "httpbin", "destination_workload_namespace": "amb",
		"reporter": "destination",
	}, 1}}
	s := newTestIstio(fakeIstioProm(raw, nil))
	s.waypointsFn = func() map[istioWorkload]bool { return nil }
	resp, err := s.GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Flows) != 1 {
		t.Errorf("without a Deployment cache nothing can be recognized as a waypoint, so nothing is dropped; got %+v", resp.Flows)
	}
}

func TestIsIstioWaypoint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"istio waypoint", map[string]string{istioManagedLabel: istioWaypointController, gatewayNameLabel: "waypoint"}, true},
		{"istio waypoint with any name", map[string]string{istioManagedLabel: istioWaypointController, gatewayNameLabel: "reviews-l7"}, true},
		{"istio ingress gateway named like a waypoint", map[string]string{istioManagedLabel: "istio.io-gateway-controller", gatewayNameLabel: "waypoint-ingress"}, false},
		{"unmanaged gateway named waypoint", map[string]string{gatewayNameLabel: "my-Waypoint"}, true},
		{"unmanaged gateway", map[string]string{gatewayNameLabel: "public"}, false},
		{"plain workload", map[string]string{"app": "waypoint"}, false},
	} {
		if got := isIstioWaypoint(tc.labels); got != tc.want {
			t.Errorf("%s: isIstioWaypoint = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A sidecar or ingress gateway calling an ambient workload that no waypoint
// fronts (or that the call skipped) is reported only by the client proxy.
func TestIstioGetFlows_ReadsClientReportsIntoAmbientNamespaces(t *testing.T) {
	raw := []istioRawSeries{
		// sidecar client -> ambient workload, no waypoint in the path
		istioSeries("istio_requests_total", "source", "side", "curl", "amb", "httpbin", "200", 2),
		istioSeries("istio_requests_total", "source", "side", "curl", "amb", "httpbin", "503", 1),
		istioSeries("istio_request_bytes_sum", "source", "side", "curl", "amb", "httpbin", "200", 50),
		// sidecar client -> ambient workload through its waypoint: both report
		istioSeries("istio_requests_total", "source", "side", "curl", "amb", "reviews", "200", 3),
		istioSeries("istio_requests_total", "source", "side", "curl", "amb", "reviews", "503", 1),
		istioSeries("istio_requests_total", "waypoint", "side", "curl", "amb", "reviews", "200", 4),
		// sidecar client -> sidecar server: the source copy stays unread
		istioSeries("istio_requests_total", "source", "side", "curl", "side", "httpbin", "200", 6),
		istioSeries("istio_requests_total", "destination", "side", "curl", "side", "httpbin", "200", 6),
		// a client report into a namespace that is not ambient is not read
		istioSeries("istio_requests_total", "source", "side", "curl", "legacy", "app", "200", 7),
	}
	s := newTestIstio(fakeIstioProm(raw, nil))
	s.ambientNamespaceFn = func() []string { return []string{"amb"} }
	resp, err := s.GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := flowsByPair(resp.Flows)
	if len(got) != 3 {
		t.Fatalf("want 3 flows, got %+v", got)
	}
	if f := got["http side/curl -> amb/httpbin"]; !approx(f.RequestRate, 3) || !approx(f.ErrorRate, 1) || f.BytesSent != 50*300 {
		t.Errorf("client-reported flow: req %v err %v sent %d, want 3 / 1 / %d", f.RequestRate, f.ErrorRate, f.BytesSent, 50*300)
	}
	// The waypoint reported this series, so all its figures come from the
	// waypoint: the client's 5xx are not mixed in.
	if f := got["http side/curl -> amb/reviews"]; !approx(f.RequestRate, 4) || f.ErrorRate != 0 {
		t.Errorf("waypoint-reported flow: req %v err %v, want 4 / 0", f.RequestRate, f.ErrorRate)
	}
	if f := got["http side/curl -> side/httpbin"]; !approx(f.RequestRate, 6) {
		t.Errorf("sidecar flow rate = %v, want 6", f.RequestRate)
	}

	s.ambientNamespaceFn = func() []string { return nil }
	resp, err = s.GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := flowsByPair(resp.Flows)["http side/curl -> amb/httpbin"]; ok {
		t.Error("with no ambient namespace known, client reports must not be read")
	}
}

func TestIstioGetFlows_AmbientClientReportsHonorTheNamespaceFilter(t *testing.T) {
	raw := []istioRawSeries{
		istioSeries("istio_requests_total", "source", "side", "curl", "amb", "httpbin", "200", 2),
		istioSeries("istio_requests_total", "destination", "side", "curl", "side", "httpbin", "200", 6),
		istioSeries("istio_requests_total", "waypoint", "other", "curl", "other", "httpbin", "200", 1),
	}
	s := newTestIstio(fakeIstioProm(raw, nil))
	s.ambientNamespaceFn = func() []string { return []string{"amb", "other"} }
	opts := DefaultFlowOptions()
	opts.Namespace = "amb"
	resp, err := s.GetFlows(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	got := flowsByPair(resp.Flows)
	if _, ok := got["http side/curl -> amb/httpbin"]; len(got) != 1 || !ok {
		t.Errorf("namespace amb: want only side/curl -> amb/httpbin, got %+v", got)
	}
}

// A waypoint's counter for a series stays present after the series' requests
// stop reaching it; with no requests counted in the window, the client's
// report is the one read.
func TestIstioGetFlows_IdleReceiverDoesNotHideClientReports(t *testing.T) {
	raw := []istioRawSeries{
		istioSeries("istio_requests_total", "waypoint", "side", "curl", "amb", "httpbin", "200", 0),
		istioSeries("istio_requests_total", "source", "side", "curl", "amb", "httpbin", "200", 2),
		istioSeries("istio_requests_total", "source", "side", "curl", "amb", "httpbin", "503", 0.5),
	}
	s := newTestIstio(fakeIstioProm(raw, nil))
	s.ambientNamespaceFn = func() []string { return []string{"amb"} }
	resp, err := s.GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	f, ok := flowsByPair(resp.Flows)["http side/curl -> amb/httpbin"]
	if !ok || !approx(f.RequestRate, 2.5) || !approx(f.ErrorRate, 0.5) {
		t.Errorf("want the client's 2.5 req/s with 0.5 errors, got %+v", resp.Flows)
	}
}

// While client reports into ambient namespaces are read, the HTTP cluster
// check covers them too, so they cannot bring another cluster's traffic in.
func TestIstioScopeCheckCoversClientReportsWhenAmbient(t *testing.T) {
	useConfiguredStore(t)
	var mu sync.Mutex
	selectors := map[string]string{}
	prev := resolveTrafficScope
	resolveTrafficScope = func(_ context.Context, metric, selector string) (string, promclient.SeriesIsolation, error) {
		mu.Lock()
		selectors[metric] = selector
		mu.Unlock()
		return `cluster="a"`, promclient.SeriesIsolation{Labels: map[string]string{"cluster": "a"}}, nil
	}
	t.Cleanup(func() { resolveTrafficScope = prev })

	for _, tc := range []struct {
		ambient  []string
		wantHTTP string
	}{
		{nil, istioReporterSelector},
		{[]string{"amb"}, istioAllReportsSelector},
	} {
		s := newTestIstio(fakeIstioProm(nil, nil))
		s.ambientNamespaceFn = func() []string { return tc.ambient }
		if _, err := s.GetFlows(context.Background(), DefaultFlowOptions()); err != nil {
			t.Fatal(err)
		}
		if selectors["istio_requests_total"] != tc.wantHTTP {
			t.Errorf("ambient %v: HTTP scope checked with %q, want %q", tc.ambient, selectors["istio_requests_total"], tc.wantHTTP)
		}
		if selectors[istioTCPMetric] != istioReporterSelector {
			t.Errorf("ambient %v: TCP scope checked with %q, want %q", tc.ambient, selectors[istioTCPMetric], istioReporterSelector)
		}
	}
}

// The query names every ambient namespace, so a failure shown to a viewer must
// not carry the request URL.
func TestIstioQueryFailureOmitsTheQuery(t *testing.T) {
	query := `sum by (x) (rate(istio_requests_total{destination_workload_namespace=~"secret-ns"}[5m]))`
	full := "http://prom:9090/api/v1/query?query=" + url.QueryEscape(query)
	for _, err := range []error{
		fmt.Errorf("prom.HTTPTransport: %w", &url.Error{Op: "Get", URL: full, Err: errors.New("dial tcp: connection refused")}),
		&prom.HTTPError{StatusCode: 503, URL: full, Body: []byte(`{"error":"query timed out"}`)},
	} {
		s := newTestIstio(func(context.Context, string) (*prom.QueryResult, error) { return nil, err })
		s.ambientNamespaceFn = func() []string { return []string{"secret-ns"} }
		resp, gerr := s.GetFlows(context.Background(), DefaultFlowOptions())
		if gerr != nil {
			t.Fatal(gerr)
		}
		if resp.Warning == "" || strings.Contains(resp.Warning, "secret-ns") || strings.Contains(resp.Warning, "query=") {
			t.Errorf("warning %q must say the query failed without the query itself", resp.Warning)
		}
	}
}

func TestIstioGetFlows_TooManyAmbientNamespacesSaysWhatIsMissing(t *testing.T) {
	raw := []istioRawSeries{
		istioSeries("istio_requests_total", "source", "side", "curl", "ns-0001", "httpbin", "200", 2),
		istioSeries("istio_requests_total", "waypoint", "ns-0002", "curl", "ns-0002", "httpbin", "200", 1),
	}
	var many []string
	for i := 0; len(strings.Join(many, "|")) <= istioAmbientListBudget; i++ {
		many = append(many, fmt.Sprintf("ns-%04d", i))
	}
	var queries []string
	fake := fakeIstioProm(raw, nil)
	s := newTestIstio(func(ctx context.Context, q string) (*prom.QueryResult, error) {
		queries = append(queries, q)
		return fake(ctx, q)
	})
	s.ambientNamespaceFn = func() []string { return many }
	resp, err := s.GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queries {
		if strings.Contains(q, `reporter="source"`) {
			t.Fatalf("over budget, no query may name the ambient namespaces: %s", q)
		}
	}
	if len(resp.Flows) != 1 || !strings.Contains(resp.Warning, "too many ambient namespaces") || resp.WarningKind != WarningIncomplete {
		t.Errorf("want the waypoint flow and an incomplete warning, got flows %+v warning %q kind %q", resp.Flows, resp.Warning, resp.WarningKind)
	}
}
