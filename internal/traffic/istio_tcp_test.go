package traffic

import (
	"context"
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

func TestIstioAmbientHTTPByteReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name, dstNs, tcpService, tcpSource, tcpSourceNs, tcpDestination string
		httpReporter, tcpReporter                                       string
		zeroHTTPBytes                                                   bool
	}{
		{"ambient same Service", "amb", "httpbin", "curl", "side", "httpbin", "source", "destination", true},
		{"waypoint same Service", "amb", "httpbin", "curl", "side", "httpbin", "waypoint", "waypoint", false},
		{"sidecar same Service", "side", "httpbin", "curl", "side", "httpbin", "destination", "destination", false},
		// A namespace labelled ambient whose pods still run sidecars: the sidecar
		// reports HTTP and an opaque TCP port separately, so neither covers the other.
		{"sidecar in an ambient namespace, same Service", "amb", "httpbin", "curl", "side", "httpbin", "destination", "destination", false},
		{"ambient other Service", "amb", "db", "curl", "side", "httpbin", "source", "destination", false},
		{"ambient other source", "amb", "httpbin", "other", "side", "httpbin", "source", "destination", false},
		{"ambient other source namespace", "amb", "httpbin", "curl", "other", "httpbin", "source", "destination", false},
		{"ambient other destination", "amb", "httpbin", "curl", "side", "other", "source", "destination", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tcp := istioSeries(istioTCPMetric, tc.tcpReporter, tc.tcpSourceNs, tc.tcpSource, tc.dstNs, tc.tcpDestination, "", 100)
			tcp.labels["destination_service_name"] = tc.tcpService
			sent := istioSeries("istio_tcp_sent_bytes_total", tc.tcpReporter, tc.tcpSourceNs, tc.tcpSource, tc.dstNs, tc.tcpDestination, "", 200)
			sent.labels["destination_service_name"] = tc.tcpService
			raw := []istioRawSeries{
				istioSeries("istio_requests_total", tc.httpReporter, "side", "curl", tc.dstNs, "httpbin", "200", 2),
				istioSeries("istio_requests_total", tc.httpReporter, "side", "curl", tc.dstNs, "httpbin", "503", 1),
				istioSeries("istio_request_bytes_sum", tc.httpReporter, "side", "curl", tc.dstNs, "httpbin", "200", 5),
				istioSeries("istio_response_bytes_sum", tc.httpReporter, "side", "curl", tc.dstNs, "httpbin", "200", 9),
				tcp, sent,
			}
			s := newTestIstio(fakeIstioProm(raw, nil))
			s.ambientNamespaceFn = func() []string { return []string{"amb"} }
			resp, err := s.GetFlows(t.Context(), DefaultFlowOptions())
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Flows) != 2 {
				t.Fatalf("want HTTP and TCP flows, got %+v", resp.Flows)
			}
			wantHTTPSent, wantHTTPRecv := int64(5*300), int64(9*300)
			if tc.zeroHTTPBytes {
				wantHTTPSent, wantHTTPRecv = 0, 0
			}
			for _, f := range resp.Flows {
				if f.Protocol == "tcp" {
					if f.BytesSent != 100*300 || f.BytesRecv != 200*300 {
						t.Errorf("TCP wire bytes changed: %+v", f)
					}
				} else if f.BytesSent != wantHTTPSent || f.BytesRecv != wantHTTPRecv || f.RequestRate != 3 || f.ErrorRate != 1 {
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
			if aggregateSent != 100*300+wantHTTPSent || aggregateRecv != 200*300+wantHTTPRecv || recordsSent != aggregateSent || recordsRecv != aggregateRecv {
				t.Errorf("aggregate bytes %d/%d, records %d/%d; want %d/%d", aggregateSent, aggregateRecv, recordsSent, recordsRecv, 100*300+wantHTTPSent, 200*300+wantHTTPRecv)
			}
			if tc.zeroHTTPBytes && strings.Contains(resp.Warning, istioZtunnelUnscrapedNote) {
				t.Error("reconciliation lost ztunnel scrape evidence")
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
