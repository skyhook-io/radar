package server

import (
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/internal/traffic"
)

func TestTopDashboardFlowsSumsPairsAndKeepsTopN(t *testing.T) {
	ep := func(ns, name, workload string) traffic.Endpoint {
		return traffic.Endpoint{Namespace: ns, Name: name, Workload: workload}
	}
	aggregated := []traffic.AggregatedFlow{
		// Two pods of one workload on different ports: one pair.
		{Source: ep("shop", "web-1", "web"), Destination: ep("shop", "api-1", "api"), Port: 80, Connections: 4},
		{Source: ep("shop", "web-2", "web"), Destination: ep("shop", "api-1", "api"), Port: 9090, Connections: 3, DirectionUnknown: true},
		// The same names in another namespace stay a separate pair.
		{Source: ep("other", "web-1", "web"), Destination: ep("other", "api-1", "api"), Connections: 6},
		{Source: ep("", "world", ""), Destination: ep("shop", "web-1", "web"), Connections: 2},
		{Source: ep("shop", "api-1", "api"), Destination: ep("shop", "db-0", "db"), Connections: 1},
	}

	got := topDashboardFlows(aggregated, 3)
	want := []DashboardTopFlow{
		{Src: "web", SrcNamespace: "shop", Dst: "api", DstNamespace: "shop", Connections: 7, DirectionUnknown: true},
		{Src: "web", SrcNamespace: "other", Dst: "api", DstNamespace: "other", Connections: 6},
		{Src: "world", Dst: "web", DstNamespace: "shop", Connections: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("topDashboardFlows:\n got %+v\nwant %+v", got, want)
	}

	if got := topDashboardFlows(nil, 3); len(got) != 0 {
		t.Fatalf("no flows: got %+v", got)
	}
}
