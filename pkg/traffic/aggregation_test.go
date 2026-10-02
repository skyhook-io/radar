package traffic

import (
	"math"
	"testing"
	"time"
)

func TestAggregateFlows_Basic(t *testing.T) {
	now := time.Now()
	flows := []Flow{
		{
			Source:      Endpoint{Name: "web", Namespace: "default"},
			Destination: Endpoint{Name: "api", Namespace: "default"},
			Protocol:    "tcp",
			Port:        8080,
			Connections: 10,
			BytesSent:   1000,
			BytesRecv:   2000,
			LastSeen:    now,
		},
		{
			Source:      Endpoint{Name: "web", Namespace: "default"},
			Destination: Endpoint{Name: "api", Namespace: "default"},
			Protocol:    "tcp",
			Port:        8080,
			Connections: 5,
			BytesSent:   500,
			BytesRecv:   1000,
			LastSeen:    now.Add(-time.Minute),
		},
	}

	result := AggregateFlows(flows)
	if len(result) != 1 {
		t.Fatalf("expected 1 aggregated flow, got %d", len(result))
	}

	agg := result[0]
	if agg.FlowCount != 2 {
		t.Errorf("FlowCount = %d, want 2", agg.FlowCount)
	}
	if agg.Connections != 15 {
		t.Errorf("Connections = %d, want 15", agg.Connections)
	}
	if agg.BytesSent != 1500 {
		t.Errorf("BytesSent = %d, want 1500", agg.BytesSent)
	}
	if agg.BytesRecv != 3000 {
		t.Errorf("BytesRecv = %d, want 3000", agg.BytesRecv)
	}
	if !agg.LastSeen.Equal(now) {
		t.Errorf("LastSeen should be the most recent timestamp")
	}
}

func TestAggregateFlows_DifferentPorts(t *testing.T) {
	flows := []Flow{
		{
			Source:      Endpoint{Name: "web", Namespace: "default"},
			Destination: Endpoint{Name: "api", Namespace: "default"},
			Protocol:    "tcp",
			Port:        8080,
			Connections: 10,
		},
		{
			Source:      Endpoint{Name: "web", Namespace: "default"},
			Destination: Endpoint{Name: "api", Namespace: "default"},
			Protocol:    "tcp",
			Port:        9090,
			Connections: 5,
		},
	}

	result := AggregateFlows(flows)
	if len(result) != 2 {
		t.Fatalf("expected 2 aggregated flows (different ports), got %d", len(result))
	}
}

func TestAggregateFlows_L7ProtocolMajorityVote(t *testing.T) {
	flows := []Flow{
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "api", Namespace: "default"},
			Port: 443, Connections: 100, L7Protocol: "HTTP",
		},
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "api", Namespace: "default"},
			Port: 443, Connections: 5, L7Protocol: "DNS",
		},
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "api", Namespace: "default"},
			Port: 443, Connections: 50, L7Protocol: "HTTP",
		},
	}

	result := AggregateFlows(flows)
	if len(result) != 1 {
		t.Fatalf("expected 1 aggregated flow, got %d", len(result))
	}
	if result[0].L7Protocol != "HTTP" {
		t.Errorf("L7Protocol = %q, want HTTP (majority)", result[0].L7Protocol)
	}
}

func TestAggregateFlows_LatencyPercentiles(t *testing.T) {
	// Build 100 RESPONSE flows with latencies 1ms, 2ms, ..., 100ms
	flows := make([]Flow, 100)
	for i := 0; i < 100; i++ {
		flows[i] = Flow{
			Source:      Endpoint{Name: "web", Namespace: "default"},
			Destination: Endpoint{Name: "api", Namespace: "default"},
			Port:        8080,
			Connections: 1,
			L7Type:      "RESPONSE",
			LatencyNs:   uint64((i + 1)) * 1_000_000, // (i+1) ms in ns
		}
	}

	result := AggregateFlows(flows)
	if len(result) != 1 {
		t.Fatalf("expected 1 aggregated flow, got %d", len(result))
	}

	agg := result[0]

	// P50 of [1..100] at index 49 = 50ms
	if math.Abs(agg.LatencyP50Ms-50.0) > 1.0 {
		t.Errorf("LatencyP50Ms = %.2f, want ~50.0", agg.LatencyP50Ms)
	}
	// P95 at index 94 = 95ms
	if math.Abs(agg.LatencyP95Ms-95.0) > 1.0 {
		t.Errorf("LatencyP95Ms = %.2f, want ~95.0", agg.LatencyP95Ms)
	}
	// P99 at index 98 = 99ms
	if math.Abs(agg.LatencyP99Ms-99.0) > 1.0 {
		t.Errorf("LatencyP99Ms = %.2f, want ~99.0", agg.LatencyP99Ms)
	}
	// AvgLatencyMs = average of 1..100 = 50.5
	if math.Abs(agg.AvgLatencyMs-50.5) > 0.1 {
		t.Errorf("AvgLatencyMs = %.2f, want ~50.5", agg.AvgLatencyMs)
	}
}

func TestAggregateFlows_LatencyOnlyFromResponses(t *testing.T) {
	flows := []Flow{
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "api", Namespace: "default"},
			Port: 8080, Connections: 1, L7Type: "REQUEST", LatencyNs: 999_000_000, // should be ignored
		},
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "api", Namespace: "default"},
			Port: 8080, Connections: 1, L7Type: "RESPONSE", LatencyNs: 5_000_000, // 5ms
		},
	}

	result := AggregateFlows(flows)
	agg := result[0]
	if agg.LatencyP50Ms != 5.0 {
		t.Errorf("LatencyP50Ms = %.2f, want 5.0 (only RESPONSE latency counted)", agg.LatencyP50Ms)
	}
	// P50 of [5, 999] is also 5, so assert the mean too — otherwise this test
	// passes whether or not the request record was excluded.
	if agg.AvgLatencyMs != 5.0 {
		t.Errorf("AvgLatencyMs = %.2f, want 5.0: a request record carries no measurement", agg.AvgLatencyMs)
	}
}

func TestAggregateFlows_HTTPStatusDistribution(t *testing.T) {
	flows := []Flow{
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "api", Namespace: "default"},
			Port: 8080, Connections: 1, HTTPStatus: 200,
		},
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "api", Namespace: "default"},
			Port: 8080, Connections: 1, HTTPStatus: 201,
		},
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "api", Namespace: "default"},
			Port: 8080, Connections: 1, HTTPStatus: 404,
		},
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "api", Namespace: "default"},
			Port: 8080, Connections: 1, HTTPStatus: 500,
		},
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "api", Namespace: "default"},
			Port: 8080, Connections: 1, HTTPStatus: 503,
		},
	}

	result := AggregateFlows(flows)
	agg := result[0]

	if agg.HTTPStatusCounts == nil {
		t.Fatal("HTTPStatusCounts is nil")
	}
	if agg.HTTPStatusCounts["2xx"] != 2 {
		t.Errorf("2xx count = %d, want 2", agg.HTTPStatusCounts["2xx"])
	}
	if agg.HTTPStatusCounts["4xx"] != 1 {
		t.Errorf("4xx count = %d, want 1", agg.HTTPStatusCounts["4xx"])
	}
	if agg.HTTPStatusCounts["5xx"] != 2 {
		t.Errorf("5xx count = %d, want 2", agg.HTTPStatusCounts["5xx"])
	}
}

func TestAggregateFlows_TopHTTPPaths(t *testing.T) {
	flows := make([]Flow, 0)
	src := Endpoint{Name: "web", Namespace: "default"}
	dst := Endpoint{Name: "api", Namespace: "default"}

	// 5 requests to GET /users, 3 to POST /users, 1 to DELETE /users
	for i := 0; i < 5; i++ {
		flows = append(flows, Flow{Source: src, Destination: dst, Port: 8080, Connections: 1, HTTPMethod: "GET", HTTPPath: "/users", HTTPStatus: 200})
	}
	for i := 0; i < 3; i++ {
		flows = append(flows, Flow{Source: src, Destination: dst, Port: 8080, Connections: 1, HTTPMethod: "POST", HTTPPath: "/users", HTTPStatus: 201})
	}
	flows = append(flows, Flow{Source: src, Destination: dst, Port: 8080, Connections: 1, HTTPMethod: "DELETE", HTTPPath: "/users", HTTPStatus: 500})

	result := AggregateFlows(flows)
	agg := result[0]

	if len(agg.TopHTTPPaths) != 3 {
		t.Fatalf("TopHTTPPaths len = %d, want 3", len(agg.TopHTTPPaths))
	}
	// Sorted by count descending
	if agg.TopHTTPPaths[0].Method != "GET" || agg.TopHTTPPaths[0].Count != 5 {
		t.Errorf("Top path = %s %d, want GET 5", agg.TopHTTPPaths[0].Method, agg.TopHTTPPaths[0].Count)
	}
	if agg.TopHTTPPaths[1].Method != "POST" || agg.TopHTTPPaths[1].Count != 3 {
		t.Errorf("Second path = %s %d, want POST 3", agg.TopHTTPPaths[1].Method, agg.TopHTTPPaths[1].Count)
	}
	// DELETE /users has 100% error rate (500)
	if agg.TopHTTPPaths[2].ErrorPct != 100.0 {
		t.Errorf("DELETE ErrorPct = %.1f, want 100.0", agg.TopHTTPPaths[2].ErrorPct)
	}
}

func TestAggregateFlows_TopHTTPPathsCapped(t *testing.T) {
	src := Endpoint{Name: "web", Namespace: "default"}
	dst := Endpoint{Name: "api", Namespace: "default"}

	// 15 unique paths — should be capped to 10
	flows := make([]Flow, 15)
	for i := 0; i < 15; i++ {
		flows[i] = Flow{
			Source: src, Destination: dst, Port: 8080, Connections: 1,
			HTTPMethod: "GET", HTTPPath: "/path" + string(rune('A'+i)),
		}
	}

	result := AggregateFlows(flows)
	if len(result[0].TopHTTPPaths) != 10 {
		t.Errorf("TopHTTPPaths len = %d, want 10 (capped)", len(result[0].TopHTTPPaths))
	}
}

func TestAggregateFlows_DNSQueries(t *testing.T) {
	src := Endpoint{Name: "web", Namespace: "default"}
	dst := Endpoint{Name: "dns", Namespace: "kube-system"}

	flows := []Flow{
		{Source: src, Destination: dst, Port: 53, Connections: 1, L7Protocol: "DNS", DNSQuery: "api.example.com.", DNSRCode: 0, DNSTTL: 300},
		{Source: src, Destination: dst, Port: 53, Connections: 1, L7Protocol: "DNS", DNSQuery: "api.example.com.", DNSRCode: 0, DNSTTL: 300},
		{Source: src, Destination: dst, Port: 53, Connections: 1, L7Protocol: "DNS", DNSQuery: "bad.example.com.", DNSRCode: 3, DNSTTL: 0}, // NXDOMAIN
	}

	result := AggregateFlows(flows)
	agg := result[0]

	if len(agg.TopDNSQueries) != 2 {
		t.Fatalf("TopDNSQueries len = %d, want 2", len(agg.TopDNSQueries))
	}

	// api.example.com should be first (count=2)
	if agg.TopDNSQueries[0].Query != "api.example.com." || agg.TopDNSQueries[0].Count != 2 {
		t.Errorf("Top DNS query = %s (%d), want api.example.com. (2)", agg.TopDNSQueries[0].Query, agg.TopDNSQueries[0].Count)
	}
	if agg.TopDNSQueries[0].AvgTTL != 300 {
		t.Errorf("AvgTTL = %d, want 300", agg.TopDNSQueries[0].AvgTTL)
	}

	// bad.example.com should have NXCount=1
	if agg.TopDNSQueries[1].NXCount != 1 {
		t.Errorf("NXCount = %d, want 1", agg.TopDNSQueries[1].NXCount)
	}
}

func TestAggregateFlows_VerdictCounts(t *testing.T) {
	src := Endpoint{Name: "web", Namespace: "default"}
	dst := Endpoint{Name: "api", Namespace: "default"}

	flows := []Flow{
		{Source: src, Destination: dst, Port: 8080, Connections: 1, Verdict: "forwarded"},
		{Source: src, Destination: dst, Port: 8080, Connections: 1, Verdict: "forwarded"},
		{Source: src, Destination: dst, Port: 8080, Connections: 1, Verdict: "dropped", DropReasonDesc: "POLICY_DENIED"},
		{Source: src, Destination: dst, Port: 8080, Connections: 1, Verdict: "error"},
	}

	result := AggregateFlows(flows)
	agg := result[0]

	if agg.VerdictCounts["forwarded"] != 2 {
		t.Errorf("forwarded = %d, want 2", agg.VerdictCounts["forwarded"])
	}
	if agg.VerdictCounts["dropped"] != 1 {
		t.Errorf("dropped = %d, want 1", agg.VerdictCounts["dropped"])
	}
	if agg.VerdictCounts["error"] != 1 {
		t.Errorf("error = %d, want 1", agg.VerdictCounts["error"])
	}
	if agg.DropReasons["POLICY_DENIED"] != 1 {
		t.Errorf("POLICY_DENIED = %d, want 1", agg.DropReasons["POLICY_DENIED"])
	}
}

func TestAggregateFlows_NoL7Data(t *testing.T) {
	// Pure TCP flows — no L7 fields should be populated
	flows := []Flow{
		{
			Source: Endpoint{Name: "web", Namespace: "default"}, Destination: Endpoint{Name: "db", Namespace: "default"},
			Protocol: "tcp", Port: 5432, Connections: 100, BytesSent: 5000, Verdict: "forwarded",
		},
	}

	result := AggregateFlows(flows)
	agg := result[0]

	if agg.L7Protocol != "" {
		t.Errorf("L7Protocol = %q, want empty for pure TCP", agg.L7Protocol)
	}
	if agg.LatencyP50Ms != 0 {
		t.Errorf("LatencyP50Ms = %.2f, want 0", agg.LatencyP50Ms)
	}
	if agg.HTTPStatusCounts != nil {
		t.Errorf("HTTPStatusCounts should be nil for pure TCP")
	}
	if agg.TopHTTPPaths != nil {
		t.Errorf("TopHTTPPaths should be nil for pure TCP")
	}
	if agg.TopDNSQueries != nil {
		t.Errorf("TopDNSQueries should be nil for pure TCP")
	}
}

func TestAggregateFlows_EmptyInput(t *testing.T) {
	result := AggregateFlows(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil input, got %d", len(result))
	}

	result = AggregateFlows([]Flow{})
	if len(result) != 0 {
		t.Errorf("expected empty result for empty input, got %d", len(result))
	}
}

func TestAggregateFlows_MixedProtocols(t *testing.T) {
	src := Endpoint{Name: "web", Namespace: "default"}
	dst := Endpoint{Name: "api", Namespace: "default"}

	// Same edge: mix of HTTP and plain TCP flows
	flows := []Flow{
		{Source: src, Destination: dst, Port: 8080, Connections: 50, L7Protocol: "HTTP", HTTPMethod: "GET", HTTPPath: "/health", HTTPStatus: 200, L7Type: "RESPONSE", LatencyNs: 2_000_000},
		{Source: src, Destination: dst, Port: 8080, Connections: 10, Protocol: "tcp"},
		{Source: src, Destination: dst, Port: 8080, Connections: 30, L7Protocol: "HTTP", HTTPMethod: "POST", HTTPPath: "/data", HTTPStatus: 500, L7Type: "RESPONSE", LatencyNs: 100_000_000},
	}

	result := AggregateFlows(flows)
	agg := result[0]

	if agg.L7Protocol != "HTTP" {
		t.Errorf("L7Protocol = %q, want HTTP", agg.L7Protocol)
	}
	if agg.Connections != 90 {
		t.Errorf("Connections = %d, want 90", agg.Connections)
	}
	// Latency: 2ms and 100ms → P50 = 2ms (index 0 of 2), P95 = 100ms (index 1)
	if agg.LatencyP50Ms != 2.0 {
		t.Errorf("LatencyP50Ms = %.2f, want 2.0", agg.LatencyP50Ms)
	}
	if agg.HTTPStatusCounts["2xx"] != 1 {
		t.Errorf("2xx = %d, want 1", agg.HTTPStatusCounts["2xx"])
	}
	if agg.HTTPStatusCounts["5xx"] != 1 {
		t.Errorf("5xx = %d, want 1", agg.HTTPStatusCounts["5xx"])
	}
}

func TestAggregateFlows_PathLatencyAverage(t *testing.T) {
	src := Endpoint{Name: "web", Namespace: "default"}
	dst := Endpoint{Name: "api", Namespace: "default"}

	flows := []Flow{
		{Source: src, Destination: dst, Port: 8080, Connections: 1, HTTPMethod: "GET", HTTPPath: "/slow", L7Type: "RESPONSE", LatencyNs: 10_000_000}, // 10ms
		{Source: src, Destination: dst, Port: 8080, Connections: 1, HTTPMethod: "GET", HTTPPath: "/slow", L7Type: "RESPONSE", LatencyNs: 20_000_000}, // 20ms
	}

	result := AggregateFlows(flows)
	agg := result[0]

	if len(agg.TopHTTPPaths) != 1 {
		t.Fatalf("TopHTTPPaths len = %d, want 1", len(agg.TopHTTPPaths))
	}
	// Avg of 10ms and 20ms = 15ms
	if math.Abs(agg.TopHTTPPaths[0].AvgMs-15.0) > 0.1 {
		t.Errorf("Path AvgMs = %.2f, want 15.0", agg.TopHTTPPaths[0].AvgMs)
	}
}

func TestPercentileFloat64(t *testing.T) {
	tests := []struct {
		name   string
		data   []float64
		p      float64
		expect float64
	}{
		{"empty", nil, 0.5, 0},
		{"single", []float64{42}, 0.5, 42},
		{"single p99", []float64{42}, 0.99, 42},
		{"two values p50", []float64{10, 20}, 0.5, 10},
		{"two values p99", []float64{10, 20}, 0.99, 10}, // int(1*0.99) = 0 → index 0
		{"ten values p50", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.50, 5},
		{"ten values p90", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.90, 9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PercentileFloat64(tt.data, tt.p)
			if got != tt.expect {
				t.Errorf("PercentileFloat64(%v, %.2f) = %.2f, want %.2f", tt.data, tt.p, got, tt.expect)
			}
		})
	}
}

func TestRoundRate(t *testing.T) {
	tests := []struct {
		name   string
		rate   float64
		expect int64
	}{
		{"zero", 0, 0},
		{"negative", -1.5, 0},
		{"tiny positive → at least 1", 0.001, 1},
		{"rounds to zero → clamped to 1", 0.4, 1},
		{"rounds to 1", 0.6, 1},
		{"normal rounding", 5.7, 6},
		{"exact integer", 100.0, 100},
		{"NaN", math.NaN(), 0},
		{"positive infinity", math.Inf(1), 0},
		{"negative infinity", math.Inf(-1), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RoundRate(tt.rate)
			if got != tt.expect {
				t.Errorf("RoundRate(%v) = %d, want %d", tt.rate, got, tt.expect)
			}
		})
	}
}

func TestAggregateFlows_KeepsUnroundedRates(t *testing.T) {
	src := Endpoint{Name: "frontend", Namespace: "shop"}
	dst := Endpoint{Name: "reviews", Namespace: "shop"}
	agg := AggregateFlows([]Flow{
		{Source: src, Destination: dst, RequestRate: 0.2, ErrorRate: 0.004},
		{Source: src, Destination: dst, RequestRate: 0.1, ErrorRate: 0.006},
		{Source: src, Destination: dst, RequestRate: math.NaN(), ErrorRate: math.Inf(1)},
	})
	if len(agg) != 1 {
		t.Fatalf("want one edge, got %d", len(agg))
	}
	a := agg[0]
	if math.Abs(a.RequestRate-0.3) > 1e-9 || math.Abs(a.ErrorRate-0.01) > 1e-9 {
		t.Errorf("rates = %v req/s, %v err/s; want 0.3 and 0.01", a.RequestRate, a.ErrorRate)
	}
}

// A request and its response arrive as two records on one edge. They are one
// request: a GET answered 503 is one failed request, not two with half failing.
func TestAggregateFlows_RequestAndResponseRecordsAreOneRequest(t *testing.T) {
	src := Endpoint{Name: "client", Namespace: "demo"}
	dst := Endpoint{Name: "web", Namespace: "demo"}
	agg := AggregateFlows([]Flow{
		{Source: src, Destination: dst, Port: 80, L7Type: "REQUEST", HTTPMethod: "GET", HTTPPath: "/orders"},
		{Source: src, Destination: dst, Port: 80, L7Type: "RESPONSE", HTTPMethod: "GET", HTTPPath: "/orders", HTTPStatus: 503, LatencyNs: 4e6},
		{Source: src, Destination: dst, Port: 53, L7Type: "REQUEST", DNSQuery: "db.example.com."},
		{Source: src, Destination: dst, Port: 53, L7Type: "RESPONSE", DNSQuery: "db.example.com.", DNSRCode: 3},
		{Source: src, Destination: dst, Port: 53, L7Type: "REQUEST", DNSQuery: "api.example.com."},
		{Source: src, Destination: dst, Port: 53, L7Type: "RESPONSE", DNSQuery: "api.example.com.", DNSTTL: 30},
	})
	for _, a := range agg {
		for _, p := range a.TopHTTPPaths {
			if p.Count != 1 || p.ErrorPct != 100 {
				t.Errorf("path %s %s: count %d, errorPct %v; want 1 request, 100%% failed", p.Method, p.Path, p.Count, p.ErrorPct)
			}
		}
		for _, q := range a.TopDNSQueries {
			if q.Count != 1 {
				t.Errorf("query %s counted %d times, want 1", q.Query, q.Count)
			}
			if q.Query == "api.example.com." && q.AvgTTL != 30 {
				t.Errorf("avg TTL = %d, want 30: the query record carries no TTL to average in", q.AvgTTL)
			}
		}
	}
}
