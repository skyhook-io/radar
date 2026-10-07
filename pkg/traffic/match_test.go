package traffic

import "testing"

func TestFlowMatch(t *testing.T) {
	ep := func(ns, name string) Endpoint { return Endpoint{Namespace: ns, Name: name, Kind: EndpointKindPod} }
	ref := func(ns, name string) EndpointRef { return EndpointRef{Namespace: ns, Name: name, Kind: EndpointKindPod} }
	flow := Flow{Source: ep("a", "web-1"), Destination: ep("b", "db-0")}

	var none *FlowMatch
	if !none.Matches(flow) {
		t.Error("no selection must match everything")
	}
	if !(&FlowMatch{Endpoints: []EndpointRef{ref("b", "db-0")}}).Matches(flow) {
		t.Error("an endpoint selection matches a flow touching it on either side")
	}
	if (&FlowMatch{Endpoints: []EndpointRef{ref("a", "web-12")}}).Matches(Flow{Source: ep("a", "web-1"), Destination: ep("b", "x")}) {
		t.Error("names match exactly, not by prefix as Hubble's pod filter does")
	}
	if (&FlowMatch{Endpoints: []EndpointRef{ref("other", "db-0")}}).Matches(flow) {
		t.Error("the namespace is part of the identity")
	}
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
