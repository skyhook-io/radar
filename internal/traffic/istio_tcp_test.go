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

func TestIstioAmbientTCPDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name, dstNs, tcpService, tcpSource, tcpSourceNs, tcpDestination string
		wantTCP                                                         bool
	}{
		{"ambient same Service", "amb", "httpbin", "curl", "side", "httpbin", false},
		{"sidecar same Service", "side", "httpbin", "curl", "side", "httpbin", true},
		{"ambient other Service", "amb", "db", "curl", "side", "httpbin", true},
		{"ambient other source", "amb", "httpbin", "other", "side", "httpbin", true},
		{"ambient other source namespace", "amb", "httpbin", "curl", "other", "httpbin", true},
		{"ambient other destination", "amb", "httpbin", "curl", "side", "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reporter := "destination"
			if tc.dstNs == "amb" {
				reporter = "source"
			}
			tcp := istioSeries(istioTCPMetric, "destination", tc.tcpSourceNs, tc.tcpSource, tc.dstNs, tc.tcpDestination, "", 100)
			tcp.labels["destination_service_name"] = tc.tcpService
			raw := []istioRawSeries{
				istioSeries("istio_requests_total", reporter, "side", "curl", tc.dstNs, "httpbin", "200", 2),
				istioSeries("istio_request_bytes_sum", reporter, "side", "curl", tc.dstNs, "httpbin", "200", 5),
				istioSeries("istio_response_bytes_sum", reporter, "side", "curl", tc.dstNs, "httpbin", "200", 9),
				tcp,
			}
			s := newTestIstio(fakeIstioProm(raw, nil))
			s.ambientNamespaceFn = func() []string { return []string{"amb"} }
			resp, err := s.GetFlows(t.Context(), DefaultFlowOptions())
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.wantTCP {
				want++
			}
			if len(resp.Flows) != want {
				t.Fatalf("got %+v, want %d flows", resp.Flows, want)
			}
			if !tc.wantTCP {
				a := AggregateFlows(resp.Flows)
				if len(a) != 1 || a[0].BytesSent != 5*300 || a[0].BytesRecv != 9*300 {
					t.Errorf("duplicate TCP bytes reached aggregation: %+v", a)
				}
				if strings.Contains(resp.Warning, istioZtunnelUnscrapedNote) {
					t.Error("de-duplication lost ztunnel scrape evidence")
				}
			}
		})
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
