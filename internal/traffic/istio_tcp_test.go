package traffic

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestIstioTCPByteActivity(t *testing.T) {
	for _, reporter := range []string{"destination", "waypoint"} {
		for _, tc := range []struct {
			name           string
			received, sent float64
		}{
			{"long-lived", 13, 7},
			{"client-only", 13, 0},
			{"server-only", 0, 7},
			{"idle", 0, 0},
		} {
			t.Run(reporter+"/"+tc.name, func(t *testing.T) {
				var raw []istioRawSeries
				if tc.received > 0 || tc.name == "idle" {
					raw = append(raw, istioSeries(istioTCPMetric, reporter, "shop", "frontend", "data", "db", "", tc.received))
				}
				if tc.sent > 0 || tc.name == "idle" {
					raw = append(raw, istioSeries("istio_tcp_sent_bytes_total", reporter, "shop", "frontend", "data", "db", "", tc.sent))
				}
				inner := fakeIstioProm(raw, nil)
				s := newTestIstio(func(ctx context.Context, q string) (*prom.QueryResult, error) {
					if strings.Contains(q, "connections_opened") {
						t.Errorf("connection-open query sent: %s", q)
					}
					if strings.Contains(q, "rate(istio_tcp_") && !strings.Contains(q, "[3600s]") {
						t.Errorf("TCP query uses wrong window: %s", q)
					}
					return inner(ctx, q)
				})
				opts := DefaultFlowOptions()
				opts.Since = time.Hour
				opts.Namespace = "data"
				resp, err := s.GetFlows(t.Context(), opts)
				if err != nil {
					t.Fatal(err)
				}
				if tc.name == "idle" {
					if len(resp.Flows) != 0 {
						t.Fatalf("idle bytes drew flows: %+v", resp.Flows)
					}
					return
				}
				if len(resp.Flows) != 1 {
					t.Fatalf("want one TCP flow without any opens series, got %+v", resp.Flows)
				}
				f := resp.Flows[0]
				if f.Protocol != "tcp" || f.Connections != 0 || f.RequestRate != 0 || f.BytesSent != int64(tc.received*3600) || f.BytesRecv != int64(tc.sent*3600) {
					t.Errorf("wrong TCP figures: %+v", f)
				}
			})
		}
	}
}

func TestIstioAmbientHTTPByteOverlap(t *testing.T) {
	for _, tc := range []struct {
		name, dstNs, tcpService, tcpSource, tcpSourceNs, tcpDestination string
		httpReporter, tcpReporter                                       string
		zeroHTTPBytes, wantOverlap, missingErrors, failTCP              bool
	}{
		{name: "ambient same Service", wantOverlap: true},
		{name: "waypoint same Service", httpReporter: "waypoint", tcpReporter: "waypoint"},
		{name: "sidecar same Service", dstNs: "side", httpReporter: "destination"},
		{name: "sidecar in an ambient namespace, same Service", httpReporter: "destination", wantOverlap: true},
		{name: "ambient other Service", tcpService: "db"},
		{name: "ambient other source", tcpSource: "other"},
		{name: "ambient other source namespace", tcpSourceNs: "other"},
		{name: "ambient other destination", tcpDestination: "other"},
		{name: "ambient zero HTTP bytes", zeroHTTPBytes: true},
		{name: "ambient missing errors", missingErrors: true, wantOverlap: true},
		{name: "ambient failed TCP read", failTCP: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.dstNs == "" {
				tc.dstNs = "amb"
			}
			if tc.tcpService == "" {
				tc.tcpService = "httpbin"
			}
			if tc.tcpSource == "" {
				tc.tcpSource = "curl"
			}
			if tc.tcpSourceNs == "" {
				tc.tcpSourceNs = "side"
			}
			if tc.tcpDestination == "" {
				tc.tcpDestination = "httpbin"
			}
			if tc.httpReporter == "" {
				tc.httpReporter = "source"
			}
			if tc.tcpReporter == "" {
				tc.tcpReporter = "destination"
			}
			httpSent, httpRecv := float64(5), float64(9)
			if tc.zeroHTTPBytes {
				httpSent, httpRecv = 0, 0
			}
			tcp := istioSeries(istioTCPMetric, tc.tcpReporter, tc.tcpSourceNs, tc.tcpSource, tc.dstNs, tc.tcpDestination, "", 100)
			tcp.labels["destination_service_name"] = tc.tcpService
			sent := istioSeries("istio_tcp_sent_bytes_total", tc.tcpReporter, tc.tcpSourceNs, tc.tcpSource, tc.dstNs, tc.tcpDestination, "", 200)
			sent.labels["destination_service_name"] = tc.tcpService
			raw := []istioRawSeries{
				istioSeries("istio_requests_total", tc.httpReporter, "side", "curl", tc.dstNs, "httpbin", "200", 2),
				istioSeries("istio_requests_total", tc.httpReporter, "side", "curl", tc.dstNs, "httpbin", "503", 1),
				istioSeries("istio_request_bytes_sum", tc.httpReporter, "side", "curl", tc.dstNs, "httpbin", "200", httpSent),
				istioSeries("istio_response_bytes_sum", tc.httpReporter, "side", "curl", tc.dstNs, "httpbin", "200", httpRecv),
				tcp, sent,
			}
			fail := map[string]error{}
			if tc.missingErrors {
				fail[istio5xxSelector] = errors.New("unavailable")
			}
			if tc.failTCP {
				fail["istio_tcp_sent_bytes_total"] = errors.New("unavailable")
			}
			s := newTestIstio(fakeIstioProm(raw, fail))
			s.ambientNamespaceFn = func() []string { return []string{"amb"} }
			resp, err := s.GetFlows(t.Context(), DefaultFlowOptions())
			if err != nil {
				t.Fatal(err)
			}
			wantFlows := 2
			wantTCPSent, wantTCPRecv := int64(100*300), int64(200*300)
			if tc.failTCP {
				wantFlows, wantTCPSent, wantTCPRecv = 1, 0, 0
			}
			if len(resp.Flows) != wantFlows {
				t.Fatalf("want %d flows, got %+v", wantFlows, resp.Flows)
			}
			wantHTTPSent, wantHTTPRecv := int64(httpSent*300), int64(httpRecv*300)
			wantErrors := float64(1)
			if tc.missingErrors {
				wantErrors = 0
			}
			for _, f := range resp.Flows {
				if f.Protocol == "tcp" {
					if f.BytesSent != 100*300 || f.BytesRecv != 200*300 {
						t.Errorf("TCP wire bytes changed: %+v", f)
					}
				} else if f.BytesSent != wantHTTPSent || f.BytesRecv != wantHTTPRecv || f.RequestRate != 3 || f.ErrorRate != wantErrors {
					t.Errorf("wrong HTTP bytes or rates: %+v", f)
				}
			}
			var aggregateSent, aggregateRecv, recordsSent, recordsRecv int64
			for _, a := range AggregateFlows(resp.Flows) {
				aggregateSent += a.BytesSent
				aggregateRecv += a.BytesRecv
				match := FlowMatch{Pairs: []EndpointPair{{
					Source:      EndpointRef{Namespace: a.Source.Namespace, Name: a.Source.Name, Kind: a.Source.Kind},
					Destination: EndpointRef{Namespace: a.Destination.Namespace, Name: a.Destination.Name, Kind: a.Destination.Kind},
					Port:        a.Port, DirectionUnknown: a.DirectionUnknown,
				}}}
				for _, f := range resp.Flows {
					if match.Matches(f) {
						recordsSent += f.BytesSent
						recordsRecv += f.BytesRecv
					}
				}
			}
			if aggregateSent != wantTCPSent+wantHTTPSent || aggregateRecv != wantTCPRecv+wantHTTPRecv || recordsSent != aggregateSent || recordsRecv != aggregateRecv {
				t.Errorf("aggregate bytes %d/%d, records %d/%d; want %d/%d", aggregateSent, aggregateRecv, recordsSent, recordsRecv, wantTCPSent+wantHTTPSent, wantTCPRecv+wantHTTPRecv)
			}
			if got := strings.Contains(resp.Warning, istioByteOverlapNote); got != tc.wantOverlap {
				t.Errorf("overlap note present = %v, want %v: %q", got, tc.wantOverlap, resp.Warning)
			}
			wantKind := ""
			if tc.wantOverlap {
				wantKind = WarningPartial
			}
			if tc.failTCP {
				wantKind = WarningTransient
			}
			if tc.dstNs != "amb" {
				wantKind = WarningIncomplete
			}
			if resp.WarningKind != wantKind {
				t.Errorf("warning kind = %q, want %q: %q", resp.WarningKind, wantKind, resp.Warning)
			}
			if tc.missingErrors && !strings.Contains(resp.Warning, "5xx error rates") {
				t.Errorf("missing figures warning lost: %q", resp.Warning)
			}
			if !tc.failTCP && tc.dstNs == "amb" && strings.Contains(resp.Warning, istioZtunnelUnscrapedNote) {
				t.Error("lost ztunnel scrape evidence")
			}
		})
	}
}

func TestIstioByteOverlapWarningPrecedence(t *testing.T) {
	for _, kind := range []string{"", WarningPartial, WarningTransient, WarningIncomplete, WarningCluster} {
		t.Run(kind, func(t *testing.T) {
			response := &FlowsResponse{Warning: "Existing warning.", WarningKind: kind}
			noteIstioByteOverlap(response)
			wantKind := kind
			if wantKind == "" {
				wantKind = WarningPartial
			}
			if response.Warning != "Existing warning. "+istioByteOverlapNote || response.WarningKind != wantKind {
				t.Errorf("warning = %q (%s), want appended overlap note (%s)", response.Warning, response.WarningKind, wantKind)
			}
		})
	}
}

func TestIstioTCPJoinsDirectionalSeriesByReporter(t *testing.T) {
	raw := []istioRawSeries{
		istioSeries(istioTCPMetric, "destination", "side", "curl", "amb", "httpbin", "", 13),
		istioSeries("istio_tcp_sent_bytes_total", "destination", "side", "curl", "amb", "httpbin", "", 7),
		istioSeries(istioTCPMetric, "waypoint", "side", "curl", "amb", "httpbin", "", 5),
		istioSeries("istio_tcp_sent_bytes_total", "waypoint", "side", "curl", "amb", "httpbin", "", 3),
	}
	s := newTestIstio(fakeIstioProm(raw, nil))
	flows, _, destinationTCP, err := s.queryTCPFlows(t.Context(), DefaultFlowOptions(), clusterScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 2 || len(destinationTCP) != 1 {
		t.Fatalf("want two reporter flows and one destination conversation, got %+v, %+v", flows, destinationTCP)
	}
	volumes := map[[2]int64]bool{}
	for _, f := range flows {
		volumes[[2]int64{f.BytesSent, f.BytesRecv}] = true
	}
	if !volumes[[2]int64{13 * 300, 7 * 300}] || !volumes[[2]int64{5 * 300, 3 * 300}] {
		t.Errorf("directional byte joins mixed reporters: %+v", flows)
	}
}

func TestIstioTCPJoinsDirectionalSeriesByService(t *testing.T) {
	received := istioSeries(istioTCPMetric, "destination", "shop", "frontend", "data", "db", "", 13)
	sentOtherService := istioSeries("istio_tcp_sent_bytes_total", "destination", "shop", "frontend", "data", "db", "", 7)
	sentOtherService.labels["destination_service_name"] = "db-admin"
	sent := istioSeries("istio_tcp_sent_bytes_total", "destination", "shop", "frontend", "data", "db", "", 5)
	resp, err := newTestIstio(fakeIstioProm([]istioRawSeries{received, sentOtherService, sent}, nil)).GetFlows(t.Context(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Flows) != 2 {
		t.Fatalf("want two Services, got %+v", resp.Flows)
	}
	for _, f := range resp.Flows {
		switch f.DestService {
		case "db":
			if f.BytesSent != 13*300 || f.BytesRecv != 5*300 {
				t.Errorf("wrong db bytes: %+v", f)
			}
		case "db-admin":
			if f.BytesSent != 0 || f.BytesRecv != 7*300 {
				t.Errorf("wrong db-admin bytes: %+v", f)
			}
		default:
			t.Errorf("unexpected Service: %+v", f)
		}
	}
}
