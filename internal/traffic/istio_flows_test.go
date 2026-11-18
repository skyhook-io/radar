package traffic

import (
	"context"
	"errors"
	"fmt"
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

var (
	istioSumByRe  = regexp.MustCompile(`^sum by \(([^)]*)\) \(rate\(([a-z_]+)\{([^}]*)\}\[([0-9a-z]+)\]\)\)$`)
	istioCountRe  = regexp.MustCompile(`^count\(([a-z_]+)\{([^}]*)\}\)$`)
	promMatcherRe = regexp.MustCompile(`([a-z_]+)(=~|!=|=)"([^"]*)"`)
)

// fakeVector is an instant vector keyed by its full label set.
type fakeVector struct {
	order  []string
	series map[string]prom.Series
}

func (v *fakeVector) add(key string, s prom.Series) {
	if v.series == nil {
		v.series = map[string]prom.Series{}
	}
	if _, ok := v.series[key]; !ok {
		v.order = append(v.order, key)
	}
	v.series[key] = s
}

func (v fakeVector) or(o fakeVector) fakeVector {
	out := fakeVector{}
	for _, k := range v.order {
		out.add(k, v.series[k])
	}
	for _, k := range o.order {
		if _, ok := v.series[k]; !ok {
			out.add(k, o.series[k])
		}
	}
	return out
}

func (v fakeVector) and(o fakeVector) fakeVector {
	out := fakeVector{}
	for _, k := range v.order {
		if _, ok := o.series[k]; ok {
			out.add(k, v.series[k])
		}
	}
	return out
}

func (v fakeVector) positive() fakeVector {
	out := fakeVector{}
	for _, k := range v.order {
		if v.series[k].DataPoints[0].Value > 0 {
			out.add(k, v.series[k])
		}
	}
	return out
}

func (v fakeVector) unless(o fakeVector) fakeVector {
	out := fakeVector{}
	for _, k := range v.order {
		if _, ok := o.series[k]; !ok {
			out.add(k, v.series[k])
		}
	}
	return out
}

// lastTopLevel finds the last occurrence of op outside parentheses, braces
// and quotes, so binary operators split left-associatively.
func lastTopLevel(q, op string) int {
	depth, quoted, at := 0, false, -1
	for i := 0; i < len(q); i++ {
		switch c := q[i]; {
		case c == '"':
			quoted = !quoted
		case quoted:
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			depth--
		case depth == 0 && strings.HasPrefix(q[i:], op):
			at = i
		}
	}
	return at
}

// evalIstioExpr evaluates the PromQL subset the Istio source sends:
// sum-by-rate leaves with label matchers, combined by or / and / unless and
// `> 0`, with PromQL's precedence (comparison, then and/unless, then or).
func evalIstioExpr(raw []istioRawSeries, q string) fakeVector {
	q = strings.TrimSpace(q)
	if i := lastTopLevel(q, " or "); i >= 0 {
		return evalIstioExpr(raw, q[:i]).or(evalIstioExpr(raw, q[i+len(" or "):]))
	}
	and, unless := lastTopLevel(q, " and "), lastTopLevel(q, " unless ")
	if and > unless {
		return evalIstioExpr(raw, q[:and]).and(evalIstioExpr(raw, q[and+len(" and "):]))
	}
	if unless >= 0 {
		return evalIstioExpr(raw, q[:unless]).unless(evalIstioExpr(raw, q[unless+len(" unless "):]))
	}
	if strings.HasSuffix(q, " > 0") {
		return evalIstioExpr(raw, strings.TrimSuffix(q, " > 0")).positive()
	}
	if strings.HasPrefix(q, "(") && strings.HasSuffix(q, ")") {
		return evalIstioExpr(raw, q[1:len(q)-1])
	}
	if c := istioCountRe.FindStringSubmatch(q); c != nil {
		matchers := promMatcherRe.FindAllStringSubmatch(c[2], -1)
		n := 0
		for _, r := range raw {
			if r.metric == c[1] && matchesAll(r.labels, matchers) {
				n++
			}
		}
		if n == 0 {
			return fakeVector{}
		}
		out := fakeVector{}
		out.add("", prom.Series{Labels: map[string]string{}, DataPoints: []prom.DataPoint{{Value: float64(n)}}})
		return out
	}
	m := istioSumByRe.FindStringSubmatch(q)
	if m == nil && !strings.HasPrefix(q, "sum by") {
		return fakeVector{} // cluster identity probes: no series
	}
	if m == nil {
		panic(fmt.Sprintf("fake Prometheus cannot evaluate %q", q))
	}
	var by []string
	for _, l := range strings.Split(m[1], ",") {
		by = append(by, strings.TrimSpace(l))
	}
	matchers := promMatcherRe.FindAllStringSubmatch(m[3], -1)
	out := fakeVector{}
	for _, r := range raw {
		if r.metric != m[2] || !matchesAll(r.labels, matchers) {
			continue
		}
		labels := map[string]string{}
		var key []string
		for _, l := range by {
			labels[l] = r.labels[l]
			key = append(key, l+"="+r.labels[l])
		}
		k := strings.Join(key, "\x00")
		g, ok := out.series[k]
		if !ok {
			g = prom.Series{Labels: labels, DataPoints: []prom.DataPoint{{Value: 0}}}
		}
		g.DataPoints[0].Value += r.value
		out.add(k, g)
	}
	return out
}

func matchesAll(labels map[string]string, matchers [][]string) bool {
	for _, m := range matchers {
		v := labels[m[1]]
		switch m[2] {
		case "=":
			if v != m[3] {
				return false
			}
		case "!=":
			if v == m[3] {
				return false
			}
		case "=~":
			if !regexp.MustCompile(`^(?:` + m[3] + `)$`).MatchString(v) {
				return false
			}
		}
	}
	return true
}

// fakeIstioProm answers the queries the Istio source sends by actually
// evaluating them over raw series, so a query that groups by fewer labels than
// its caller keys on, or selects a different reporter, returns what real
// Prometheus would, instead of whatever a hand-written stub happened to say.
func fakeIstioProm(raw []istioRawSeries, fail map[string]error) promQueryFunc {
	return func(_ context.Context, query string) (*prom.QueryResult, error) {
		for metric, err := range fail {
			if strings.Contains(query, metric) {
				return nil, err
			}
		}
		v := evalIstioExpr(raw, query)
		out := &prom.QueryResult{ResultType: "vector"}
		for _, k := range v.order {
			out.Series = append(out.Series, v.series[k])
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
		{"istio_tcp_received_bytes_total", map[string]string{
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
	if got["tcp"] != 0 {
		t.Errorf("tcp connections for byte activity = %d, want 0", got["tcp"])
	}
}

func TestIstioGetFlows_FailedSubqueriesAreReportedNotZeroed(t *testing.T) {
	boom := errors.New("query timed out")
	for _, tc := range []struct {
		name   string
		metric string
		fail   string
	}{
		{"error rates", `istio_requests_total{reporter=~"destination|waypoint", response_code`, "5xx error rates"},
		{"tcp received", "istio_tcp_received_bytes_total", "TCP traffic"},
		{"tcp sent", "istio_tcp_sent_bytes_total", "TCP traffic"},
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
			// Missing figures are about the edges shown; missing TCP edges are
			// traffic that may not be shown at all.
			want := WarningPartial
			if strings.HasPrefix(tc.name, "tcp ") {
				want = WarningTransient
			}
			if resp.WarningKind != want {
				t.Errorf("warningKind = %q, want %q", resp.WarningKind, want)
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
	for _, metric := range []string{istioTCPMetric, "istio_tcp_sent_bytes_total"} {
		resp, err := newTestIstio(fakeIstioProm(nil, map[string]error{metric: errors.New("timeout")})).
			GetFlows(context.Background(), DefaultFlowOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp.Warning, "TCP traffic") || resp.WarningKind != WarningTransient {
			t.Errorf("%s: warning = %q (%s), want the failed TCP query named and retried", metric, resp.Warning, resp.WarningKind)
		}
	}

	// A failed 5xx query qualifies edges; with none there is nothing to qualify.
	resp, err := newTestIstio(fakeIstioProm(nil, map[string]error{`response_code=~"5.."`: errors.New("timeout")})).
		GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Warning != "" {
		t.Errorf("warning = %q on an empty result the missing 5xx rates cannot explain", resp.Warning)
	}
}
