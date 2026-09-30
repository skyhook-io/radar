package traffic

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
	"k8s.io/client-go/kubernetes/fake"
)

// istioRawSeries is one per-series rate as Prometheus holds it before any
// aggregation: the full label set Istio's telemetry attaches.
type istioRawSeries struct {
	metric string
	labels map[string]string
	value  float64
}

var istioSumByRe = regexp.MustCompile(`sum by \(([^)]*)\) \(rate\(([a-z_]+)\{([^}]*)\}`)

// fakeIstioProm answers the queries the Istio source sends by actually
// evaluating `sum by (...)` over raw series, so a query that groups by fewer
// labels than its caller keys on returns the coarser totals real Prometheus
// would return, instead of whatever a hand-written stub happened to say.
func fakeIstioProm(raw []istioRawSeries, fail map[string]error) promQueryFunc {
	return func(_ context.Context, query string) (*prom.QueryResult, error) {
		for metric, err := range fail {
			if strings.Contains(query, metric) {
				return nil, err
			}
		}
		m := istioSumByRe.FindStringSubmatch(query)
		if m == nil {
			return emptyResult(), nil
		}
		var by []string
		for _, l := range strings.Split(m[1], ",") {
			by = append(by, strings.TrimSpace(l))
		}
		metric, selector := m[2], m[3]
		only5xx := strings.Contains(selector, `response_code=~"5.."`)

		groups := map[string]*prom.Series{}
		var order []string
		for _, r := range raw {
			if r.metric != metric {
				continue
			}
			if only5xx && !strings.HasPrefix(r.labels["response_code"], "5") {
				continue
			}
			labels := map[string]string{}
			var key []string
			for _, l := range by {
				labels[l] = r.labels[l]
				key = append(key, r.labels[l])
			}
			k := strings.Join(key, "\x00")
			g, ok := groups[k]
			if !ok {
				g = &prom.Series{Labels: labels, DataPoints: []prom.DataPoint{{Value: 0}}}
				groups[k] = g
				order = append(order, k)
			}
			g.DataPoints[0].Value += r.value
		}
		out := &prom.QueryResult{ResultType: "vector"}
		for _, k := range order {
			out.Series = append(out.Series, *groups[k])
		}
		return out, nil
	}
}

func istioLabels(dstService, protocol, code string) map[string]string {
	return map[string]string{
		"source_workload": "frontend", "source_workload_namespace": "shop",
		"destination_workload": "reviews", "destination_workload_namespace": "shop",
		"destination_service_name": dstService, "request_protocol": protocol,
		"reporter": "destination", "response_code": code,
	}
}

// One workload pair reached through two Services (HTTP on one, gRPC on the
// other) — the shape where pair-level figures get attached to each series.
func twoServiceIstioSeries() []istioRawSeries {
	return []istioRawSeries{
		{"istio_requests_total", istioLabels("reviews", "http", "200"), 9},
		{"istio_requests_total", istioLabels("reviews", "http", "503"), 1},
		{"istio_requests_total", istioLabels("reviews-grpc", "grpc", "200"), 4.5},
		{"istio_requests_total", istioLabels("reviews-grpc", "grpc", "500"), 0.5},
		{"istio_request_bytes_sum", istioLabels("reviews", "http", "200"), 2000},
		{"istio_request_bytes_sum", istioLabels("reviews-grpc", "grpc", "200"), 1000},
		{"istio_response_bytes_sum", istioLabels("reviews", "http", "200"), 8000},
		{"istio_response_bytes_sum", istioLabels("reviews-grpc", "grpc", "200"), 4000},
	}
}

func newTestIstio(q promQueryFunc) *IstioSource {
	s := NewIstioSource(fake.NewSimpleClientset())
	s.queryFn = q
	return s
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestIstioGetFlows_PairFiguresAreNotCopiedOntoEachSeries(t *testing.T) {
	resp, err := newTestIstio(fakeIstioProm(twoServiceIstioSeries(), nil)).GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Flows) != 2 {
		t.Fatalf("want one flow per Service, got %d: %+v", len(resp.Flows), resp.Flows)
	}

	var reqs, errs float64
	var sent, recv int64
	for _, f := range resp.Flows {
		reqs += f.RequestRate
		errs += f.ErrorRate
		sent += f.BytesSent
		recv += f.BytesRecv
	}
	if !approx(reqs, 15) {
		t.Errorf("total request rate = %v, want 15", reqs)
	}
	if !approx(errs, 1.5) {
		t.Errorf("total 5xx rate = %v, want 1.5 — the pair's errors were attributed more than once", errs)
	}
	// 5m of bytes/sec: 3000 B/s sent, 12000 B/s received across the pair.
	if sent != 3000*300 || recv != 12000*300 {
		t.Errorf("bytes sent/recv = %d/%d, want %d/%d — the pair's bytes were attributed more than once", sent, recv, 3000*300, 12000*300)
	}

	for _, f := range resp.Flows {
		switch f.DestService {
		case "reviews":
			if !approx(f.ErrorRate, 1) || !approx(f.RequestRate, 10) {
				t.Errorf("reviews: req %v err %v, want 10 / 1", f.RequestRate, f.ErrorRate)
			}
		case "reviews-grpc":
			if !approx(f.ErrorRate, 0.5) || !approx(f.RequestRate, 5) {
				t.Errorf("reviews-grpc: req %v err %v, want 5 / 0.5", f.RequestRate, f.ErrorRate)
			}
		default:
			t.Errorf("flow has DestService %q, want the Service it was addressed to", f.DestService)
		}
	}
}

func TestIstioGetFlows_RatesRoundRatherThanTruncate(t *testing.T) {
	raw := []istioRawSeries{
		{"istio_requests_total", istioLabels("reviews", "http", "200"), 1.9},
		{"istio_tcp_connections_opened_total", map[string]string{
			"source_workload": "frontend", "source_workload_namespace": "shop",
			"destination_workload": "db", "destination_workload_namespace": "shop",
			"destination_service_name": "db", "reporter": "destination",
		}, 2.7},
	}
	resp, err := newTestIstio(fakeIstioProm(raw, nil)).GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, f := range resp.Flows {
		got[f.Protocol] = f.Connections
	}
	if got["http"] != 2 {
		t.Errorf("http connections for 1.9 req/s = %d, want 2", got["http"])
	}
	if got["tcp"] != 3 {
		t.Errorf("tcp connections for 2.7 conn/s = %d, want 3", got["tcp"])
	}
}

func TestIstioGetFlows_FailedSubqueriesAreReportedNotZeroed(t *testing.T) {
	boom := errors.New("query timed out")
	for _, tc := range []struct {
		name   string
		metric string
		fail   string
	}{
		{"error rates", "istio_requests_total{reporter=\"destination\", response_code", "5xx error rates"},
		{"tcp", "istio_tcp_connections_opened_total", "TCP connections"},
		{"request bytes", "istio_request_bytes_sum", "bytes"},
		{"response bytes", "istio_response_bytes_sum", "bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := newTestIstio(fakeIstioProm(twoServiceIstioSeries(), map[string]error{tc.metric: boom})).
				GetFlows(context.Background(), DefaultFlowOptions())
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Flows) == 0 {
				t.Fatal("the request-rate query succeeded, so its flows must still be returned")
			}
			if !strings.Contains(resp.Warning, tc.fail) {
				t.Errorf("warning = %q, want it to name the missing %s", resp.Warning, tc.fail)
			}
			if resp.WarningKind != WarningTransient {
				t.Errorf("warningKind = %q, want transient: a failed query can succeed on the next poll", resp.WarningKind)
			}
		})
	}
}

func TestIstioGetFlows_NoPrometheus(t *testing.T) {
	q := func(context.Context, string) (*prom.QueryResult, error) {
		return nil, errIstioPrometheusUnavailable
	}
	resp, err := newTestIstio(q).GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Warning != "Prometheus not available for Istio metrics" || len(resp.Flows) != 0 {
		t.Errorf("got %+v", resp)
	}
}

func TestIstioGetFlows_EmptyResultWarnsOnlyWhenTheGapCouldExplainIt(t *testing.T) {
	// A TCP-only mesh whose TCP query failed has no HTTP flows either; without
	// the warning it reads as idle.
	resp, err := newTestIstio(fakeIstioProm(nil, map[string]error{"istio_tcp_connections_opened_total": errors.New("timeout")})).
		GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Warning, "TCP connections") {
		t.Errorf("warning = %q, want the failed TCP query named", resp.Warning)
	}

	// A failed 5xx query qualifies edges; with none there is nothing to qualify.
	resp, err = newTestIstio(fakeIstioProm(nil, map[string]error{`response_code=~"5.."`: errors.New("timeout")})).
		GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Warning != "" {
		t.Errorf("warning = %q on an empty result the missing 5xx rates cannot explain", resp.Warning)
	}
}
