package netpol

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestExplain(t *testing.T) {
	src := radar()
	dst := prom(9090)
	external := Peer{External: true, IP: "203.0.113.7"}
	unresolved := Peer{}
	fromRadarNs := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "radar"}}}
	fromAnyPodInAnyNs := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}}

	tests := []struct {
		name   string
		np     *networkingv1.NetworkPolicy
		dir    Direction
		sel    *corev1.Pod
		peer   Peer
		port   int32
		effect Effect
		reason string // substring
	}{
		{
			name: "policy that does not select the pod is not applicable",
			np:   policy("monitoring", "other", map[string]string{"app": "other"}, ingressT, nil, nil),
			dir:  DirectionIngress, sel: dst.Pod, peer: src, port: 9090,
			effect: NotApplicable, reason: "does not select monitoring/prom-0",
		},
		{
			name: "egress-only policy is not applicable to ingress",
			np:   policy("monitoring", "egress-only", promLabels, egressT, nil, nil),
			dir:  DirectionIngress, sel: dst.Pod, peer: src, port: 9090,
			effect: NotApplicable, reason: "does not isolate ingress",
		},
		{
			name: "isolating policy with no rules admits nothing",
			np:   policy("monitoring", "deny-all", nil, ingressT, nil, nil),
			dir:  DirectionIngress, sel: dst.Pod, peer: src, port: 9090,
			effect: DoesNotAdmit, reason: "isolates ingress with no rules",
		},
		{
			name: "empty from admits any source",
			np: policy("monitoring", "open", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{tcpPort(9090)}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: src, port: 9090,
			effect: Admits, reason: "rule 1 admits any source on TCP/9090",
		},
		{
			name: "namespaceSelector match admits and names the selector",
			np: policy("monitoring", "from-radar", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: src, port: 9090,
			effect: Admits, reason: "admits any pod in namespaces matching kubernetes.io/metadata.name=radar",
		},
		{
			name: "rules for other ports only",
			np: policy("monitoring", "other-port", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{tcpPort(8080)}, From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: src, port: 9090,
			effect: DoesNotAdmit, reason: "admit other ports, none covers TCP/9090",
		},
		{
			name: "no rule admits this source",
			np: policy("monitoring", "from-other", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "payments"}}}}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: src, port: 9090,
			effect: DoesNotAdmit, reason: "no rule admits the source radar/radar-1 on TCP/9090",
		},
		{
			name: "unknown port against a port-restricted rule is undecidable",
			np: policy("monitoring", "ported", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{tcpPort(9090)}, From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: src, port: 0,
			effect: Undecidable, reason: "port is not known",
		},
		{
			name: "selector rules never admit an external source",
			np: policy("monitoring", "from-radar", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromAnyPodInAnyNs}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: external, port: 9090,
			effect: DoesNotAdmit, reason: "no rule admits the source 203.0.113.7 (not a pod)",
		},
		{
			name: "ipBlock containing an external source admits it",
			np: policy("monitoring", "cidr", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "203.0.113.0/24"}}}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: external, port: 9090,
			effect: Admits, reason: "admits ipBlock 203.0.113.0/24",
		},
		{
			name: "ipBlock missing the source is undecidable on ingress",
			np: policy("monitoring", "cidr", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "192.0.2.0/24"}}}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: external, port: 9090,
			effect: Undecidable, reason: "may have been rewritten",
		},
		{
			name: "egress ipBlock to an external destination is decisive",
			np: policy("radar", "egress-cidr", radarLabels, egressT, nil,
				[]networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "192.0.2.0/24"}}}}}),
			dir: DirectionEgress, sel: src.Pod, peer: external, port: 443,
			effect: DoesNotAdmit, reason: "no rule admits the destination 203.0.113.7 (not a pod)",
		},
		{
			name: "egress ipBlock to a pod destination is undecidable even on a hit",
			np: policy("radar", "egress-cidr", radarLabels, egressT, nil,
				[]networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.2.0/24"}}}}}),
			dir: DirectionEgress, sel: src.Pod, peer: dst.Peer, port: 9090,
			effect: Undecidable, reason: "cannot be established",
		},
		{
			name: "an unresolved pod peer is undecidable",
			np: policy("monitoring", "from-radar", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: unresolved, port: 9090,
			effect: Undecidable, reason: "could not be resolved",
		},
		{
			name: "named egress port against an unresolved destination is undecidable",
			np: policy("radar", "egress-named", radarLabels, egressT, nil,
				[]networkingv1.NetworkPolicyEgressRule{{Ports: []networkingv1.NetworkPolicyPort{namedPort("web")}, To: []networkingv1.NetworkPolicyPeer{fromAnyPodInAnyNs}}}),
			dir: DirectionEgress, sel: src.Pod, peer: unresolved, port: 9090,
			effect: Undecidable, reason: "names a port on a destination pod that could not be resolved",
		},
		{
			name: "external destination with no known address is undecidable, not refused",
			np: policy("radar", "egress-cidr", radarLabels, egressT, nil,
				[]networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "192.0.2.0/24"}}}}}),
			dir: DirectionEgress, sel: src.Pod, peer: Peer{External: true}, port: 443,
			effect: Undecidable, reason: "no address of that family is known",
		},
		{
			name: "hostNetwork peer is undecidable",
			np: policy("monitoring", "from-radar", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: func() Peer { p := radar(); p.Pod.Spec.HostNetwork = true; return p }(), port: 9090,
			effect: Undecidable, reason: "host network",
		},
		{
			name: "an observed address wins over the pod's other addresses",
			np: policy("monitoring", "v4-cidr", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/16"}}}}}, nil),
			dir: DirectionIngress, sel: dst.Pod,
			peer: func() Peer {
				p := radar()
				p.Pod.Status.PodIPs = []corev1.PodIP{{IP: "10.0.1.5"}, {IP: "fd00::5"}}
				p.IP = "fd00::5" // the flow arrived over IPv6
				return p
			}(), port: 9090,
			effect: Undecidable, reason: "no address of that family",
		},
		{
			name: "a numeric port entry admits even when a named sibling cannot be resolved",
			np: policy("radar", "egress-mixed-ports", radarLabels, egressT, nil,
				[]networkingv1.NetworkPolicyEgressRule{{Ports: []networkingv1.NetworkPolicyPort{tcpPort(443), namedPort("https")}}}),
			dir: DirectionEgress, sel: src.Pod, peer: unresolved, port: 443,
			effect: Admits, reason: "rule 1 admits any destination on TCP/443",
		},
		{
			name: "unknown port: a protocol-only entry admits every port of that protocol",
			np: policy("monitoring", "any-tcp", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{{Protocol: func() *corev1.Protocol { p := corev1.ProtocolTCP; return &p }()}}, From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: src, port: 0,
			effect: Admits, reason: "on TCP/?",
		},
		{
			name: "unknown port: an entry for another protocol never matches",
			np: policy("monitoring", "udp-only", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{{Protocol: func() *corev1.Protocol { p := corev1.ProtocolUDP; return &p }(), Port: func() *intstr.IntOrString { p := intstr.FromInt32(53); return &p }()}}, From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: src, port: 0,
			effect: DoesNotAdmit, reason: "admit other ports",
		},
		{
			name: "an empty namespaceSelector needs no Namespace object",
			np: policy("monitoring", "from-anywhere", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromAnyPodInAnyNs}}}, nil),
			dir: DirectionIngress, sel: dst.Pod, peer: Peer{Pod: radar().Pod}, port: 9090,
			effect: Admits, reason: "admits any pod in namespaces matching anything",
		},
		{
			name: "egress ipBlock of the other family does not admit a known external destination",
			np: policy("radar", "egress-v4", radarLabels, egressT, nil,
				[]networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "203.0.113.0/24"}}}}}),
			dir: DirectionEgress, sel: src.Pod, peer: Peer{External: true, IP: "2001:db8::7"}, port: 443,
			effect: DoesNotAdmit, reason: "no rule admits the destination 2001:db8::7 (not a pod)",
		},
		{
			name: "a malformed ipBlock is undecidable even for a known external destination",
			np: policy("radar", "egress-bad", radarLabels, egressT, nil,
				[]networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "203.0.113.0/24", Except: []string{"not-a-cidr"}}}}}}),
			dir: DirectionEgress, sel: src.Pod, peer: external, port: 443,
			effect: Undecidable, reason: "not a valid CIDR",
		},
		{
			name: "hostNetwork selected pod is undecidable",
			np:   policy("monitoring", "deny-all", nil, ingressT, nil, nil),
			dir:  DirectionIngress, sel: func() *corev1.Pod { p := prom(9090).Pod; p.Spec.HostNetwork = true; return p }(), peer: src, port: 9090,
			effect: Undecidable, reason: "host network",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Explain(tt.np, tt.dir, tt.sel, tt.peer, tt.port, corev1.ProtocolTCP)
			if got.Effect != tt.effect {
				t.Fatalf("effect = %v (%q), want %v", got.Effect, got.Reason, tt.effect)
			}
			if !strings.Contains(got.Reason, tt.reason) {
				t.Fatalf("reason = %q, want it to contain %q", got.Reason, tt.reason)
			}
		})
	}
}

func TestEvaluate_MatchesExplainFold(t *testing.T) {
	// Evaluate is defined as a fold over Explain; pin one denial and one
	// admission so a divergence between the two cannot go unnoticed.
	src, backends := radar(), []Backend{prom(9090)}
	deny := policy("monitoring", "deny-all", nil, ingressT, nil, nil)
	if got := Evaluate(src, backends, []*networkingv1.NetworkPolicy{deny}); got.Kind != Denied {
		t.Fatalf("Evaluate = %+v, want Denied", got)
	}
	if got := Explain(deny, DirectionIngress, backends[0].Pod, src, 9090, corev1.ProtocolTCP); got.Effect != DoesNotAdmit {
		t.Fatalf("Explain = %+v, want DoesNotAdmit", got)
	}
	allow := policy("monitoring", "open", promLabels, ingressT, []networkingv1.NetworkPolicyIngressRule{{}}, nil)
	if got := Evaluate(src, backends, []*networkingv1.NetworkPolicy{deny, allow}); got.Kind != Allowed {
		t.Fatalf("Evaluate = %+v, want Allowed", got)
	}
	if got := Explain(allow, DirectionIngress, backends[0].Pod, src, 9090, corev1.ProtocolTCP); got.Effect != Admits {
		t.Fatalf("Explain = %+v, want Admits", got)
	}
}
