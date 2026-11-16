package traffic

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

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
	resolveTrafficScope = func(_ context.Context, metric, selector string, _ time.Duration) (string, promclient.SeriesIsolation, error) {
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
		fmt.Errorf("prom: query error from http://prom:9090: rejected query %s (bad_data)", query),
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
	for i := 0; len(istioHTTPQuery("istio_requests_total", istio5xxSelector, "", clusterScope{}, many, rateWindowFor(0))) <= istioQueryBudget; i++ {
		many = append(many, fmt.Sprintf("ns-%04d", i))
	}
	useConfiguredStore(t)
	var httpScopeSelectors []string
	prev := resolveTrafficScope
	resolveTrafficScope = func(_ context.Context, metric, selector string, _ time.Duration) (string, promclient.SeriesIsolation, error) {
		if metric == "istio_requests_total" {
			httpScopeSelectors = append(httpScopeSelectors, selector)
		}
		return "", promclient.SeriesIsolation{}, nil
	}
	t.Cleanup(func() { resolveTrafficScope = prev })
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
	if n := len(httpScopeSelectors); n == 0 || httpScopeSelectors[n-1] != istioReporterSelector {
		t.Errorf("HTTP scope last checked with %v, want the receiving-side selector once client reports are skipped", httpScopeSelectors)
	}
	if len(resp.Flows) != 1 || !strings.Contains(resp.Warning, "more ambient namespaces than Radar can include") || resp.WarningKind != WarningIncomplete {
		t.Errorf("want the waypoint flow and an incomplete warning, got flows %+v warning %q kind %q", resp.Flows, resp.Warning, resp.WarningKind)
	}
}

// Prometheus's own explanation is what an operator acts on; it is withheld only
// while the query names ambient namespaces.
func TestIstioQueryFailureKeepsPrometheusExplanation(t *testing.T) {
	tooMany := &prom.HTTPError{StatusCode: 422, URL: "http://prom/api/v1/query?query=x",
		Body: []byte(`{"status":"error","errorType":"execution","error":"query processing would load too many samples into memory"}`)}
	echoing := &prom.HTTPError{StatusCode: 422, URL: "http://prom/api/v1/query?query=x",
		Body: []byte(`{"status":"error","errorType":"422","error":"error when executing query=\"rate(m{destination_workload_namespace=~\\\"secret-ns\\\"})\""}`)}
	plain := &prom.HTTPError{StatusCode: 502, URL: "http://prom/api/v1/query?query=x", Body: []byte("bad gateway")}
	for _, tc := range []struct {
		err          error
		namesAmbient bool
		want         string
	}{
		{tooMany, false, "Prometheus answered HTTP 422: query processing would load too many samples into memory"},
		{tooMany, true, "Prometheus answered HTTP 422 (execution)"},
		{echoing, true, "Prometheus answered HTTP 422 (422)"},
		{plain, false, "Prometheus answered HTTP 502: bad gateway"},
		{plain, true, "Prometheus answered HTTP 502"},
		{&prom.HTTPError{StatusCode: 503}, false, "Prometheus answered HTTP 503"},
	} {
		if got := istioQueryFailure(tc.err, tc.namesAmbient); got != tc.want {
			t.Errorf("istioQueryFailure(%v, ambient=%v) = %q, want %q", tc.err, tc.namesAmbient, got, tc.want)
		}
	}
}

// With ambient namespaces in the query, a transport error is described without
// any text that may quote the URL.
func TestIstioQueryFailureTransportErrorsWithAmbientNamespaces(t *testing.T) {
	full := "http://prom:9090/api/v1/query?query=" + url.QueryEscape(`m{destination_workload_namespace=~"secret-ns"}`)
	redirect := &url.Error{Op: "Get", URL: full, Err: fmt.Errorf("failed to parse Location header %q: invalid", full)}
	dial := &url.Error{Op: "Get", URL: full, Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	timeout := &url.Error{Op: "Get", URL: full, Err: context.DeadlineExceeded}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{redirect, "Prometheus returned an error; Radar's server log has the details"},
		{dial, "dial tcp: connection refused"},
		{timeout, "the request to Prometheus timed out"},
	} {
		if got := istioQueryFailure(fmt.Errorf("prom.HTTPTransport: %w", tc.err), true); got != tc.want {
			t.Errorf("istioQueryFailure(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	if got := istioQueryFailure(redirect, false); !strings.Contains(got, "Location") {
		t.Errorf("without ambient namespaces the transport detail is kept, got %q", got)
	}
}

func TestIstioGetFlows_SaysWhenZtunnelIsNotScraped(t *testing.T) {
	waypointFlow := istioSeries("istio_requests_total", "waypoint", "amb", "curl", "amb", "httpbin", "200", 1)
	sidecarIntoAmbient := istioSeries("istio_requests_total", "source", "side", "curl", "amb", "httpbin", "200", 1)
	sidecarOnly := istioSeries("istio_requests_total", "destination", "side", "curl", "side", "httpbin", "200", 1)
	ztunnelTCP := istioSeries(istioTCPMetric, "destination", "side", "curl", "amb", "db", "", 1)
	ztunnelHop := istioSeries(istioTCPMetric, "destination", "amb", "waypoint", "amb", "httpbin", "", 1)
	ztunnelIdle := istioSeries(istioTCPMetric, "destination", "side", "curl", "amb", "db", "", 0)
	ztunnelBuild := istioRawSeries{"istio_build", map[string]string{"component": "ztunnel", "tag": "1.30.3"}, 1}
	proxyBuild := istioRawSeries{"istio_build", map[string]string{"component": "proxy", "tag": "1.30.3"}, 1}
	for _, tc := range []struct {
		name       string
		raw        []istioRawSeries
		ambient    []string
		namespaces []string
		probe      func(*prom.QueryResult) (*prom.QueryResult, error)
		tcpFails   bool
		want       bool
	}{
		{name: "ambient namespaces, no ztunnel metrics", raw: []istioRawSeries{waypointFlow, proxyBuild}, ambient: []string{"amb"}, want: true},
		{name: "viewing an ambient namespace", raw: []istioRawSeries{waypointFlow}, ambient: []string{"amb"}, namespaces: []string{"amb"}, want: true},
		{name: "viewing a client namespace that calls into one", raw: []istioRawSeries{sidecarIntoAmbient, sidecarOnly}, ambient: []string{"amb"}, namespaces: []string{"side"}, want: true},
		{name: "ztunnel build info present", raw: []istioRawSeries{waypointFlow, ztunnelBuild}, ambient: []string{"amb"}},
		{name: "ztunnel TCP drawn without build info", raw: []istioRawSeries{waypointFlow, ztunnelTCP}, ambient: []string{"amb"}},
		{name: "an idle ztunnel TCP series without build info", raw: []istioRawSeries{waypointFlow, ztunnelIdle}, ambient: []string{"amb"}},
		{name: "only a waypoint hop reported by ztunnel", raw: []istioRawSeries{waypointFlow, ztunnelHop}, ambient: []string{"amb"}},
		{name: "no ambient namespaces", raw: []istioRawSeries{waypointFlow}},
		{name: "a view with no ambient traffic", raw: []istioRawSeries{waypointFlow, sidecarOnly}, ambient: []string{"amb"}, namespaces: []string{"side", "other"}},
		{name: "the TCP read failed", raw: []istioRawSeries{waypointFlow}, ambient: []string{"amb"}, tcpFails: true},
		{name: "the check failed", raw: []istioRawSeries{waypointFlow}, ambient: []string{"amb"},
			probe: func(*prom.QueryResult) (*prom.QueryResult, error) { return nil, errors.New("boom") }},
		{name: "a partial answer", raw: []istioRawSeries{waypointFlow}, ambient: []string{"amb"},
			probe: func(r *prom.QueryResult) (*prom.QueryResult, error) { r.Annotations.Partial = true; return r, nil }},
		{name: "an answer with warnings", raw: []istioRawSeries{waypointFlow}, ambient: []string{"amb"},
			probe: func(r *prom.QueryResult) (*prom.QueryResult, error) {
				r.Annotations.Warnings = []string{"store down"}
				return r, nil
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fail map[string]error
			if tc.tcpFails {
				fail = map[string]error{istioTCPMetric: errors.New("boom")}
			}
			fake := fakeIstioProm(tc.raw, fail)
			s := newTestIstio(func(ctx context.Context, q string) (*prom.QueryResult, error) {
				r, err := fake(ctx, q)
				if q == istioZtunnelProbe && tc.probe != nil {
					return tc.probe(r)
				}
				return r, err
			})
			s.ambientNamespaceFn = func() []string { return tc.ambient }
			s.waypointsFn = func() map[istioWorkload]bool { return map[istioWorkload]bool{{"amb", "waypoint"}: true} }
			opts := DefaultFlowOptions()
			opts.Namespaces = tc.namespaces
			if len(tc.namespaces) == 1 {
				opts.Namespace = tc.namespaces[0]
			}
			resp, err := s.GetFlows(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(resp.Warning, istioZtunnelUnscrapedNote); got != tc.want {
				t.Fatalf("note shown = %v, want %v (warning %q, flows %+v)", got, tc.want, resp.Warning, resp.Flows)
			}
			if tc.want && resp.WarningKind != WarningIncomplete {
				t.Errorf("kind = %q, want incomplete", resp.WarningKind)
			}
			if len(resp.Flows) == 0 {
				t.Errorf("the note must not cost the flows")
			}
		})
	}
}

// The view's time range sets the window every rate is taken over, and byte
// totals are that window's.
func TestIstioGetFlows_UsesTheRequestedWindow(t *testing.T) {
	raw := []istioRawSeries{
		istioSeries("istio_requests_total", "waypoint", "amb", "curl", "amb", "httpbin", "200", 2),
		istioSeries("istio_request_bytes_sum", "waypoint", "amb", "curl", "amb", "httpbin", "200", 100),
		istioSeries("istio_response_bytes_sum", "waypoint", "amb", "curl", "amb", "httpbin", "200", 400),
	}
	inner := fakeIstioProm(raw, nil)
	var queries []string
	src := newTestIstio(func(ctx context.Context, q string) (*prom.QueryResult, error) {
		queries = append(queries, q)
		return inner(ctx, q)
	})
	opts := DefaultFlowOptions()
	opts.Since = time.Hour
	resp, err := src.GetFlows(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queries {
		if strings.Contains(q, "rate(") && (!strings.Contains(q, "[3600s]") || strings.Contains(q, "[5m]")) {
			t.Errorf("rate query not over the requested hour: %s", q)
		}
	}
	if len(resp.Flows) != 1 || resp.Flows[0].BytesSent != 100*3600 || resp.Flows[0].BytesRecv != 400*3600 {
		t.Errorf("want one flow with an hour of bytes, got %+v", resp.Flows)
	}
}

// The view's range also reaches the cluster-scope checks and the empty-map
// explanation, so traffic another cluster sent within the range is found.
func TestIstioGetFlows_ScopeChecksUseTheRequestedWindow(t *testing.T) {
	useConfiguredStore(t)
	var mu sync.Mutex
	var windows []time.Duration
	prev := resolveTrafficScope
	resolveTrafficScope = func(_ context.Context, _, _ string, w time.Duration) (string, promclient.SeriesIsolation, error) {
		mu.Lock()
		windows = append(windows, w)
		mu.Unlock()
		return `cluster="a"`, promclient.SeriesIsolation{Labels: map[string]string{"cluster": "a"}}, nil
	}
	t.Cleanup(func() { resolveTrafficScope = prev })
	var queries []string
	src := newTestIstio(func(_ context.Context, q string) (*prom.QueryResult, error) {
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		return &prom.QueryResult{}, nil
	})
	opts := DefaultFlowOptions()
	opts.Since = time.Hour
	if _, err := src.GetFlows(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(windows) == 0 {
		t.Fatal("no scope check ran")
	}
	for _, w := range windows {
		if w != time.Hour {
			t.Errorf("scope check over %v, want the requested hour", w)
		}
	}
	for _, q := range queries {
		if strings.Contains(q, "last_over_time(") && !strings.Contains(q, "[3600s]") {
			t.Errorf("empty-map check not over the requested hour: %s", q)
		}
	}
}

// A range shorter than two scrape intervals leaves rate nothing to work with:
// the map comes back empty although the metrics are there, and says why.
func TestIstioGetFlows_SaysWhenTheRangeHasTooFewSamples(t *testing.T) {
	for _, tc := range []struct {
		name    string
		since   time.Duration
		samples float64
		want    bool
	}{
		{"one sample per series in a minute", time.Minute, 1, true},
		{"two samples: genuinely idle", time.Minute, 2, false},
		{"no metrics at all", time.Minute, 0, false},
		{"default range is not checked", 5 * time.Minute, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := newTestIstio(func(_ context.Context, q string) (*prom.QueryResult, error) {
				if strings.HasPrefix(q, "max(count_over_time(") && tc.samples > 0 {
					return &prom.QueryResult{Series: []prom.Series{{DataPoints: []prom.DataPoint{{Value: tc.samples}}}}}, nil
				}
				return &prom.QueryResult{}, nil
			})
			opts := DefaultFlowOptions()
			opts.Since = tc.since
			resp, err := src.GetFlows(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := resp.Warning == istioTooFewSamplesNote; got != tc.want {
				t.Errorf("warning %q, want the too-few-samples note: %v", resp.Warning, tc.want)
			}
			if tc.want && resp.WarningKind != WarningIncomplete {
				t.Errorf("kind %q, want incomplete", resp.WarningKind)
			}
		})
	}
}

// A fractional range is rounded to whole seconds once, so the cluster check's
// window is valid PromQL and matches the rates' window.
func TestRateWindowForRoundsToWholeSeconds(t *testing.T) {
	w := rateWindowFor(90*time.Second + 500*time.Millisecond)
	if w.promQL != "90s" || w.seconds != 90 || w.span() != 90*time.Second {
		t.Errorf("got %q / %v / %v, want 90s throughout", w.promQL, w.seconds, w.span())
	}
}
