package traffic

import (
	"context"
	"testing"
)

// The records endpoint keys a selected edge as AggregateFlows keyed it and
// filters the source's flows with FlowMatch. Istio names workloads as pods,
// reports no port, falls back to the Service for a destination the mesh could
// not name, and reports HTTP and TCP traffic between one pair as separate
// flows. Every edge it produces has to find its own flows again.
func TestIstioEdgesFindTheirRecords(t *testing.T) {
	tcp := func(src, dst, dstNs, svc string, v float64) istioRawSeries {
		return istioRawSeries{"istio_tcp_connections_opened_total", map[string]string{
			"source_workload": src, "source_workload_namespace": "shop",
			"destination_workload": dst, "destination_workload_namespace": dstNs,
			"destination_service_name": svc, "reporter": "destination",
		}, v}
	}
	raw := append(twoServiceIstioSeries(),
		tcp("frontend", "reviews", "shop", "reviews", 2),
		tcp("frontend", "unknown", "", "payments.example.com", 1),
		tcp("checkout", "redis", "data", "redis", 3),
	)
	resp, err := newTestIstio(fakeIstioProm(raw, nil)).GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	ref := func(e Endpoint) EndpointRef {
		return EndpointRef{Namespace: e.Namespace, Name: e.Name, Kind: e.Kind}
	}
	aggregated := AggregateFlows(resp.Flows)
	if len(aggregated) < 3 {
		t.Fatalf("want at least 3 edges, got %d: %+v", len(aggregated), aggregated)
	}
	for _, a := range aggregated {
		match := FlowMatch{Pairs: []EndpointPair{{
			Source: ref(a.Source), Destination: ref(a.Destination),
			Port: a.Port, DirectionUnknown: a.DirectionUnknown,
		}}}
		var matched int64
		for _, f := range resp.Flows {
			if match.Matches(f) {
				matched += f.Connections
			}
		}
		if matched != a.Connections {
			t.Errorf("edge %s/%s -> %s/%s:%d: its records add up to %d connections, the edge shows %d",
				a.Source.Namespace, a.Source.Name, a.Destination.Namespace, a.Destination.Name, a.Port, matched, a.Connections)
		}
	}
}
