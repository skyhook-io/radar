package traffic

import (
	"testing"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestConvertEndpointKinds(t *testing.T) {
	tests := []struct {
		name     string
		ep       *flowpb.Endpoint
		ip       string
		wantKind string
		wantName string
	}{
		{"no endpoint at all", nil, "10.1.2.3", EndpointKindUnknown, "10.1.2.3"},
		{"a pod", &flowpb.Endpoint{Namespace: "shop", PodName: "web-0", Labels: []string{"k8s:app=web"}}, "10.0.0.10", EndpointKindPod, "web-0"},
		{"the world", &flowpb.Endpoint{Labels: []string{"reserved:world"}}, "203.0.113.7", EndpointKindExternal, "world"},
		{"a CIDR identity", &flowpb.Endpoint{Labels: []string{"cidr:203.0.113.0/24", "reserved:world"}}, "203.0.113.7", EndpointKindExternal, "world"},
		{"the local host", &flowpb.Endpoint{Labels: []string{"reserved:host"}}, "172.18.0.2", EndpointKindHost, "host"},
		{"a remote node", &flowpb.Endpoint{Labels: []string{"reserved:remote-node"}}, "172.18.0.3", EndpointKindHost, "remote-node"},
		{"the API server", &flowpb.Endpoint{Labels: []string{"reserved:kube-apiserver", "reserved:remote-node"}}, "172.18.0.4", EndpointKindHost, "kube-apiserver"},
		{"a node with CIDR matching enabled, cidr first", &flowpb.Endpoint{Labels: []string{"cidr:172.18.0.3/32", "reserved:remote-node"}}, "172.18.0.3", EndpointKindHost, "remote-node"},
		{"a node with CIDR matching enabled, node first", &flowpb.Endpoint{Labels: []string{"reserved:kube-apiserver", "cidr:172.18.0.4/32"}}, "172.18.0.4", EndpointKindHost, "kube-apiserver"},
		{"the IPv4 world", &flowpb.Endpoint{Labels: []string{"reserved:world-ipv4"}}, "203.0.113.8", EndpointKindExternal, "world"},
		{"a health endpoint carries no policy identity", &flowpb.Endpoint{Labels: []string{"reserved:health"}}, "10.0.0.99", EndpointKindUnknown, "health"},
		{"no labels and no pod is unidentified", &flowpb.Endpoint{}, "10.0.0.98", EndpointKindUnknown, "10.0.0.98"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertEndpoint(tt.ep, tt.ip)
			if got.Kind != tt.wantKind || got.Name != tt.wantName {
				t.Fatalf("kind/name = %s/%s, want %s/%s", got.Kind, got.Name, tt.wantKind, tt.wantName)
			}
			if got.IP != tt.ip {
				t.Fatalf("ip = %s, want %s", got.IP, tt.ip)
			}
		})
	}
}

func TestConvertHubbleFlowPolicyVerdict(t *testing.T) {
	t.Run("absent when Hubble names no policy", func(t *testing.T) {
		flow := convertHubbleFlow(&flowpb.Flow{Verdict: flowpb.Verdict_DROPPED})
		if flow.PolicyVerdict != nil {
			t.Fatalf("PolicyVerdict = %+v, want nil", flow.PolicyVerdict)
		}
	})
	t.Run("ingress and egress attributions are merged and kinds kept", func(t *testing.T) {
		flow := convertHubbleFlow(&flowpb.Flow{
			Verdict: flowpb.Verdict_DROPPED,
			IngressDeniedBy: []*flowpb.Policy{
				{Kind: "NetworkPolicy", Namespace: "shop", Name: "deny-all-ingress"},
				nil,
				{Kind: "CiliumNetworkPolicy", Name: ""},
			},
			EgressAllowedBy: []*flowpb.Policy{{Kind: "CiliumClusterwideNetworkPolicy", Name: "allow-dns"}},
		})
		pv := flow.PolicyVerdict
		if pv == nil {
			t.Fatal("PolicyVerdict = nil")
		}
		if len(pv.DeniedBy) != 1 || pv.DeniedBy[0] != (PolicyRef{Kind: "NetworkPolicy", Namespace: "shop", Name: "deny-all-ingress"}) {
			t.Fatalf("DeniedBy = %+v", pv.DeniedBy)
		}
		if len(pv.AllowedBy) != 1 || pv.AllowedBy[0] != (PolicyRef{Kind: "CiliumClusterwideNetworkPolicy", Name: "allow-dns"}) {
			t.Fatalf("AllowedBy = %+v", pv.AllowedBy)
		}
	})
}

func TestConvertHubbleFlowDropReason(t *testing.T) {
	t.Run("the description wins when the relay fills it", func(t *testing.T) {
		flow := convertHubbleFlow(&flowpb.Flow{Verdict: flowpb.Verdict_DROPPED, DropReasonDesc: flowpb.DropReason_POLICY_DENIED, DropReason: 133})
		if flow.DropReasonDesc != "POLICY_DENIED" {
			t.Fatalf("DropReasonDesc = %q", flow.DropReasonDesc)
		}
	})
	t.Run("an older relay's numeric code is read by its enum name", func(t *testing.T) {
		flow := convertHubbleFlow(&flowpb.Flow{Verdict: flowpb.Verdict_DROPPED, DropReason: uint32(flowpb.DropReason_POLICY_DENY)})
		if flow.DropReasonDesc != "POLICY_DENY" {
			t.Fatalf("DropReasonDesc = %q", flow.DropReasonDesc)
		}
	})
	t.Run("no reason at all stays empty", func(t *testing.T) {
		flow := convertHubbleFlow(&flowpb.Flow{Verdict: flowpb.Verdict_DROPPED})
		if flow.DropReasonDesc != "" {
			t.Fatalf("DropReasonDesc = %q", flow.DropReasonDesc)
		}
	})
}

func TestCallerOrientedFlow(t *testing.T) {
	client := &flowpb.Endpoint{Namespace: "demo", PodName: "client-0"}
	server := &flowpb.Endpoint{Namespace: "demo", PodName: "echo-0"}
	tcp := func(src, dst uint32) *flowpb.Layer4 {
		return &flowpb.Layer4{Protocol: &flowpb.Layer4_TCP{TCP: &flowpb.TCP{SourcePort: src, DestinationPort: dst}}}
	}
	reply := func(v bool) *wrapperspb.BoolValue { return wrapperspb.Bool(v) }

	t.Run("request direction is kept as is", func(t *testing.T) {
		f, ok := callerOrientedFlow(&flowpb.Flow{Source: client, Destination: server, L4: tcp(41732, 80), IsReply: reply(false)})
		if !ok || f.Source.Name != "client-0" || f.Destination.Name != "echo-0" || f.Port != 80 || f.Connections != 1 {
			t.Fatalf("got ok=%v %+v", ok, f)
		}
	})
	t.Run("an L4 reply draws no edge", func(t *testing.T) {
		if f, ok := callerOrientedFlow(&flowpb.Flow{Source: server, Destination: client, L4: tcp(80, 41732), IsReply: reply(true)}); ok {
			t.Fatalf("reply packet became an edge: %+v", f)
		}
	})
	t.Run("an L7 response lands on the request's edge", func(t *testing.T) {
		f, ok := callerOrientedFlow(&flowpb.Flow{
			Source: server, Destination: client, L4: tcp(80, 41732), IsReply: reply(true),
			DestinationService: &flowpb.Service{Name: "client-svc"}, SourceService: &flowpb.Service{Name: "echo"},
			L7: &flowpb.Layer7{Type: flowpb.L7FlowType_RESPONSE, LatencyNs: 5e6, Record: &flowpb.Layer7_Http{Http: &flowpb.HTTP{Code: 503}}},
		})
		if !ok {
			t.Fatal("the response carries the status and latency and must be kept")
		}
		if f.Source.Name != "client-0" || f.Destination.Name != "echo-0" || f.Port != 80 {
			t.Errorf("response oriented %s -> %s :%d, want client-0 -> echo-0 :80", f.Source.Name, f.Destination.Name, f.Port)
		}
		if f.DestService != "echo" {
			t.Errorf("DestService = %q, want the server's Service", f.DestService)
		}
		if f.Connections != 0 {
			t.Errorf("Connections = %d, want 0: the request already counted this connection", f.Connections)
		}
		if f.HTTPStatus != 503 || f.LatencyNs != 5e6 {
			t.Errorf("lost L7 detail: status %d latency %d", f.HTTPStatus, f.LatencyNs)
		}
	})
	t.Run("unknown direction keeps its orientation", func(t *testing.T) {
		f, ok := callerOrientedFlow(&flowpb.Flow{Source: server, Destination: client, L4: tcp(80, 41732), Verdict: flowpb.Verdict_DROPPED})
		if !ok || f.Source.Name != "echo-0" {
			t.Fatalf("a flow Hubble could not orient must pass through untouched, got ok=%v %+v", ok, f)
		}
	})
}
