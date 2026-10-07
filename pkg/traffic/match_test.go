package traffic

import (
	"reflect"
	"testing"
)

func TestFlowMatch(t *testing.T) {
	ep := func(ns, name string) Endpoint { return Endpoint{Namespace: ns, Name: name, Kind: EndpointKindPod} }
	ref := func(ns, name string) EndpointRef {
		return EndpointRef{Namespace: ns, Name: name, Kind: EndpointKindPod}
	}
	flow := Flow{Source: ep("a", "web-1"), Destination: ep("b", "db-0")}

	var none *FlowMatch
	if !none.Matches(flow) {
		t.Error("no selection must match everything")
	}
	if (&FlowMatch{Pairs: []EndpointPair{{Source: ref("a", "web-12"), Destination: ref("b", "db-0")}}}).Matches(flow) {
		t.Error("names match exactly, not by prefix as Hubble's pod filter does")
	}
	if (&FlowMatch{Pairs: []EndpointPair{{Source: ref("a", "web-1"), Destination: ref("other", "db-0")}}}).Matches(flow) {
		t.Error("the namespace is part of the identity")
	}
	onPort := &FlowMatch{Pairs: []EndpointPair{{Source: ref("a", "web-1"), Destination: ref("b", "db-0"), Port: 5432}}}
	if onPort.Matches(flow) {
		t.Error("a pair matches only its own port")
	}
	if (&FlowMatch{Pairs: []EndpointPair{{Source: ref("a", "web-1"), Destination: ref("b", "db-0")}}}).Matches(Flow{Source: flow.Source, Destination: flow.Destination, Port: 443}) {
		t.Error("port zero is a port like any other, not a wildcard")
	}
	unoriented := Flow{Source: flow.Source, Destination: flow.Destination, DirectionUnknown: true}
	if (&FlowMatch{Pairs: []EndpointPair{{Source: ref("a", "web-1"), Destination: ref("b", "db-0")}}}).Matches(unoriented) {
		t.Error("traffic of unknown direction is a separate edge, as the aggregation keys it")
	}
	flow.Port = 5432
	if !onPort.Matches(flow) {
		t.Error("a pair on a port matches its own port")
	}
	flow.Port = 0
	pair := &FlowMatch{Pairs: []EndpointPair{{Source: ref("a", "web-1"), Destination: ref("b", "db-0")}}}
	if !pair.Matches(flow) {
		t.Error("a pair matches its own direction")
	}
	if pair.Matches(Flow{Source: ep("b", "db-0"), Destination: ep("a", "web-1")}) {
		t.Error("a pair is directional: flows are caller-oriented, like the edges they were aggregated into")
	}
}

func TestFlowOptionsExcludes(t *testing.T) {
	opts := FlowOptions{ExcludeNamespaces: []string{"kube-system"}, ExcludeHost: true}
	for _, tc := range []struct {
		name string
		flow Flow
		want bool
	}{
		{"source in an excluded namespace", Flow{Source: Endpoint{Namespace: "kube-system"}, Destination: Endpoint{Namespace: "app"}}, true},
		{"destination in an excluded namespace", Flow{Source: Endpoint{Namespace: "app"}, Destination: Endpoint{Namespace: "kube-system"}}, true},
		{"host endpoint", Flow{Source: Endpoint{Namespace: "app"}, Destination: Endpoint{Kind: EndpointKindHost, Name: "host"}}, true},
		{"ordinary traffic", Flow{Source: Endpoint{Namespace: "app"}, Destination: Endpoint{Kind: EndpointKindExternal, Name: "world"}}, false},
	} {
		if got := opts.Excludes(tc.flow); got != tc.want {
			t.Errorf("%s: Excludes = %v, want %v", tc.name, got, tc.want)
		}
	}
	if (FlowOptions{}).Excludes(Flow{Source: Endpoint{Namespace: "kube-system", Kind: EndpointKindHost}}) {
		t.Error("nothing is excluded unless asked")
	}
}

func TestGraphEndpoint(t *testing.T) {
	pod := Endpoint{Namespace: "shop", Name: "web-7d9f-x2k4q", Kind: EndpointKindPod, IP: "10.0.0.1", Workload: "web", WorkloadKind: "Deployment"}
	g := GraphEndpoint(pod)
	if g.Name != "web" || g.Kind != EndpointKindWorkload || g.WorkloadKind != "Deployment" || g.IP != "" {
		t.Errorf("GraphEndpoint = %+v, want the Deployment, with nothing pod-specific", g)
	}
	for _, e := range []Endpoint{
		{Namespace: "shop", Name: "standalone", Kind: EndpointKindPod},                          // nothing owns it
		{Name: "world", Kind: EndpointKindExternal, Workload: "x"},                              // not a pod
		{Namespace: "shop", Name: "checkout", Kind: EndpointKindWorkload, Workload: "checkout"}, // already a workload
	} {
		if got := GraphEndpoint(e); !reflect.DeepEqual(got, e) {
			t.Errorf("GraphEndpoint(%+v) = %+v, want it unchanged", e, got)
		}
	}

	flows := []Flow{
		{Source: pod, Destination: Endpoint{Namespace: "shop", Name: "db-0", Kind: EndpointKindPod, Workload: "db", WorkloadKind: "StatefulSet"}, Connections: 1},
		{Source: Endpoint{Namespace: "shop", Name: "web-7d9f-zz9", Kind: EndpointKindPod, Workload: "web", WorkloadKind: "Deployment"}, Destination: Endpoint{Namespace: "shop", Name: "db-1", Kind: EndpointKindPod, Workload: "db", WorkloadKind: "StatefulSet"}, Connections: 1},
	}
	agg := AggregateFlows(GraphFlows(flows))
	if len(agg) != 1 || agg[0].Connections != 2 || agg[0].Source.Name != "web" || agg[0].Destination.Name != "db" {
		t.Errorf("aggregation = %+v, want one web→db edge carrying both pod pairs", agg)
	}
	if flows[0].Source.Name != "web-7d9f-x2k4q" {
		t.Error("GraphFlows must not rewrite the records it was given")
	}
}

func TestFlowMatchWorkloadReference(t *testing.T) {
	web := EndpointRef{Namespace: "shop", Name: "web", Kind: EndpointKindWorkload, WorkloadKind: "Deployment"}
	db := EndpointRef{Namespace: "shop", Name: "db-0", Kind: EndpointKindPod}
	podFlow := Flow{
		Source:      Endpoint{Namespace: "shop", Name: "web-7d9f-x2k4q", Kind: EndpointKindPod, Workload: "web"},
		Destination: Endpoint{Namespace: "shop", Name: "db-0", Kind: EndpointKindPod},
	}
	match := &FlowMatch{Pairs: []EndpointPair{{Source: web, Destination: db}}}
	if !match.Matches(podFlow) {
		t.Error("a workload→pod pair matches the records of the workload's pods")
	}
	other := podFlow
	other.Source.Workload = "web-canary"
	if match.Matches(other) {
		t.Error("a pod of another workload must not match, however its name starts")
	}
}
