package traffic

import (
	"testing"

	flowpb "github.com/cilium/cilium/api/v1/flow"
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
