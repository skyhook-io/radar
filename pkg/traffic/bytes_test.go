package traffic

import (
	"reflect"
	"testing"
)

func TestAggregateFlowsBytesByProtocol(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			flows := []Flow{
				{Protocol: "http", BytesSent: 5969, BytesRecv: 260965},
				{Protocol: "tcp", BytesSent: 32699, BytesRecv: 391439},
				{Protocol: "http", BytesSent: 10, BytesRecv: 20},
				{Protocol: "grpc", BytesSent: 30, BytesRecv: 40},
				{Protocol: "udp"},
			}
			for i := range flows {
				flows[i].Source = Endpoint{Name: "client-1", Namespace: "demo", Kind: "Pod", Workload: "client"}
				flows[i].Destination = Endpoint{Name: "server-1", Namespace: "demo", Kind: "Pod", Workload: "server"}
				flows[i].DirectionUnknown = unknown
			}
			if reverse {
				for i, j := 0, len(flows)-1; i < j; i, j = i+1, j-1 {
					flows[i], flows[j] = flows[j], flows[i]
				}
			}
			for _, graph := range []bool{false, true} {
				input := flows
				if graph {
					input = GraphFlows(flows)
				}
				got := AggregateFlows(input)
				want := map[string]ProtocolBytes{
					"http": {BytesSent: 5979, BytesRecv: 260985},
					"tcp":  {BytesSent: 32699, BytesRecv: 391439},
					"grpc": {BytesSent: 30, BytesRecv: 40},
					"udp":  {},
				}
				if len(got) != 1 {
					t.Fatalf("got %d edges, want 1", len(got))
				}
				a := got[0]
				if !reflect.DeepEqual(a.BytesByProtocol, want) {
					t.Errorf("reverse=%t unknown=%t graph=%t: %+v, want %+v", reverse, unknown, graph, a.BytesByProtocol, want)
				}
				if a.BytesSent != 38708 || a.BytesRecv != 652464 || a.DirectionUnknown != unknown || a.FlowCount != 5 {
					t.Errorf("compatibility or identity changed: %+v", a)
				}
				if graph && (a.Source.Name != "client" || a.Destination.Name != "server") {
					t.Errorf("workload grouping lost: %+v", a)
				}
			}
		}
	}
}

func TestGraphFlowsWorkloadProtocolBuckets(t *testing.T) {
	flows := []Flow{
		{Source: Endpoint{Name: "client-1", Namespace: "demo", Kind: "Pod", Workload: "client"}, Destination: Endpoint{Name: "server-1", Namespace: "demo", Kind: "Pod", Workload: "server"}, Protocol: "http", BytesSent: 100, BytesRecv: 200},
		{Source: Endpoint{Name: "client-2", Namespace: "demo", Kind: "Pod", Workload: "client"}, Destination: Endpoint{Name: "server-2", Namespace: "demo", Kind: "Pod", Workload: "server"}, Protocol: "tcp", BytesSent: 300, BytesRecv: 400},
	}
	if got := AggregateFlows(flows); len(got) != 2 {
		t.Fatalf("by-pod aggregation has %d edges", len(got))
	}
	got := AggregateFlows(GraphFlows(flows))
	want := map[string]ProtocolBytes{"http": {BytesSent: 100, BytesRecv: 200}, "tcp": {BytesSent: 300, BytesRecv: 400}}
	if len(got) != 1 || !reflect.DeepEqual(got[0].BytesByProtocol, want) {
		t.Fatalf("workload aggregation: %+v", got)
	}
}
