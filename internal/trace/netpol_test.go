package trace

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func npPod(lbls map[string]string, ports ...corev1.ContainerPort) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Labels: lbls},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Ports: ports}}},
	}
}

func cport(name string, port int32) corev1.ContainerPort {
	return corev1.ContainerPort{Name: name, ContainerPort: port, Protocol: corev1.ProtocolTCP}
}

// np builds a NetworkPolicy selecting podSelector, with the given policyTypes
// (nil = default) and ingress rules.
func np(name string, sel map[string]string, types []networkingv1.PolicyType, ingress ...networkingv1.NetworkPolicyIngressRule) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: sel},
			PolicyTypes: types,
			Ingress:     ingress,
		},
	}
}

func tcpPort(p int) networkingv1.NetworkPolicyPort {
	port := intstr.FromInt(p)
	return networkingv1.NetworkPolicyPort{Port: &port}
}

func namedPort(name string) networkingv1.NetworkPolicyPort {
	port := intstr.FromString(name)
	return networkingv1.NetworkPolicyPort{Port: &port}
}

func rangePort(start, end int) networkingv1.NetworkPolicyPort {
	port := intstr.FromInt(start)
	e := int32(end)
	return networkingv1.NetworkPolicyPort{Port: &port, EndPort: &e}
}

func fromPod(sel map[string]string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: sel}}
}

func fromNS(sel map[string]string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: sel}}
}

func fromIPBlock(cidr string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}
}

var (
	ingressOnly = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
	egressOnly  = []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}
)

func TestEvaluateNetpol(t *testing.T) {
	echoPod := npPod(map[string]string{"app": "echo"}, cport("http", 80))
	port80 := []PortMap{{Port: 80, TargetPort: "80"}}

	tests := []struct {
		name      string
		pods      []*corev1.Pod
		ports     []PortMap
		policies  []*networkingv1.NetworkPolicy
		want      policyVerdict
		wantWrong bool
	}{
		{
			name:     "no policies",
			pods:     []*corev1.Pod{echoPod},
			ports:    port80,
			policies: nil,
			want:     policyNotEvaluated,
		},
		{
			name:     "policy selects other pods only",
			pods:     []*corev1.Pod{echoPod},
			ports:    port80,
			policies: []*networkingv1.NetworkPolicy{np("deny", map[string]string{"app": "other"}, ingressOnly)},
			want:     policyNotEvaluated,
		},
		{
			name:     "default-deny ingress",
			pods:     []*corev1.Pod{echoPod},
			ports:    port80,
			policies: []*networkingv1.NetworkPolicy{np("deny", map[string]string{"app": "echo"}, ingressOnly)},
			want:     policyWouldDeny,
		},
		{
			name:  "allow port from anywhere",
			pods:  []*corev1.Pod{echoPod},
			ports: port80,
			policies: []*networkingv1.NetworkPolicy{np("allow", map[string]string{"app": "echo"}, ingressOnly,
				networkingv1.NetworkPolicyIngressRule{Ports: []networkingv1.NetworkPolicyPort{tcpPort(80)}})},
			want: policyNotRestricted,
		},
		{
			name:  "allow from podSelector is caller-dependent",
			pods:  []*corev1.Pod{echoPod},
			ports: port80,
			policies: []*networkingv1.NetworkPolicy{np("allow", map[string]string{"app": "echo"}, ingressOnly,
				networkingv1.NetworkPolicyIngressRule{
					Ports: []networkingv1.NetworkPolicyPort{tcpPort(80)},
					From:  []networkingv1.NetworkPolicyPeer{fromPod(map[string]string{"app": "client"})}})},
			want: policyAdvisory,
		},
		{
			name:  "wrong port - allows other port not the served one",
			pods:  []*corev1.Pod{npPod(map[string]string{"app": "cache"}, cport("redis", 6379))},
			ports: []PortMap{{Port: 6379, TargetPort: "6379"}},
			policies: []*networkingv1.NetworkPolicy{np("wrong", map[string]string{"app": "cache"}, ingressOnly,
				networkingv1.NetworkPolicyIngressRule{Ports: []networkingv1.NetworkPolicyPort{tcpPort(9999)}})},
			want:      policyWouldDeny,
			wantWrong: true,
		},
		{
			name:     "egress-only policy",
			pods:     []*corev1.Pod{echoPod},
			ports:    port80,
			policies: []*networkingv1.NetworkPolicy{np("egress", map[string]string{"app": "echo"}, egressOnly)},
			want:     policyEgressNote,
		},
		{
			name:  "overlap - default-deny plus allow-anywhere is additive allow",
			pods:  []*corev1.Pod{echoPod},
			ports: port80,
			policies: []*networkingv1.NetworkPolicy{
				np("deny", map[string]string{"app": "echo"}, ingressOnly),
				np("allow", map[string]string{"app": "echo"}, ingressOnly,
					networkingv1.NetworkPolicyIngressRule{Ports: []networkingv1.NetworkPolicyPort{tcpPort(80)}}),
			},
			want: policyNotRestricted,
		},
		{
			name:  "ipBlock is caller-dependent",
			pods:  []*corev1.Pod{echoPod},
			ports: port80,
			policies: []*networkingv1.NetworkPolicy{np("ipb", map[string]string{"app": "echo"}, ingressOnly,
				networkingv1.NetworkPolicyIngressRule{
					Ports: []networkingv1.NetworkPolicyPort{tcpPort(80)},
					From:  []networkingv1.NetworkPolicyPeer{fromIPBlock("10.0.0.0/8")}})},
			want: policyAdvisory,
		},
		{
			name:  "namespaceSelector is caller-dependent",
			pods:  []*corev1.Pod{echoPod},
			ports: port80,
			policies: []*networkingv1.NetworkPolicy{np("xns", map[string]string{"app": "echo"}, ingressOnly,
				networkingv1.NetworkPolicyIngressRule{
					Ports: []networkingv1.NetworkPolicyPort{tcpPort(80)},
					From:  []networkingv1.NetworkPolicyPeer{fromNS(map[string]string{"team": "a"})}})},
			want: policyAdvisory,
		},
		{
			name:  "empty rule {} allows all",
			pods:  []*corev1.Pod{echoPod},
			ports: port80,
			policies: []*networkingv1.NetworkPolicy{np("allowall", map[string]string{"app": "echo"}, ingressOnly,
				networkingv1.NetworkPolicyIngressRule{})},
			want: policyNotRestricted,
		},
		{
			name:  "policyTypes omitted with egress rules still isolates ingress",
			pods:  []*corev1.Pod{echoPod},
			ports: port80,
			policies: []*networkingv1.NetworkPolicy{{
				ObjectMeta: metav1.ObjectMeta{Name: "egwithdefault", Namespace: "ns"},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "echo"}},
					Egress:      []networkingv1.NetworkPolicyEgressRule{{}},
				},
			}},
			want: policyWouldDeny,
		},
		{
			name:  "named targetPort resolves to container port",
			pods:  []*corev1.Pod{npPod(map[string]string{"app": "echo"}, cport("http", 8080))},
			ports: []PortMap{{Port: 80, TargetPort: "http"}},
			policies: []*networkingv1.NetworkPolicy{np("allow", map[string]string{"app": "echo"}, ingressOnly,
				networkingv1.NetworkPolicyIngressRule{Ports: []networkingv1.NetworkPolicyPort{tcpPort(8080)}})},
			want: policyNotRestricted,
		},
		{
			name:  "endPort range covers target",
			pods:  []*corev1.Pod{npPod(map[string]string{"app": "echo"}, cport("http", 8080))},
			ports: []PortMap{{Port: 8080, TargetPort: "8080"}},
			policies: []*networkingv1.NetworkPolicy{np("range", map[string]string{"app": "echo"}, ingressOnly,
				networkingv1.NetworkPolicyIngressRule{Ports: []networkingv1.NetworkPolicyPort{rangePort(8000, 9000)}})},
			want: policyNotRestricted,
		},
		{
			name:  "rule's only allow is an undeclared named port - k8s treats it as no-match → would-deny",
			pods:  []*corev1.Pod{npPod(map[string]string{"app": "echo"}, cport("http", 80))},
			ports: port80,
			policies: []*networkingv1.NetworkPolicy{np("named", map[string]string{"app": "echo"}, ingressOnly,
				networkingv1.NetworkPolicyIngressRule{Ports: []networkingv1.NetworkPolicyPort{namedPort("metrics")}})},
			want:      policyWouldDeny,
			wantWrong: true,
		},
		{
			name: "hostNetwork pod is advisory",
			pods: []*corev1.Pod{func() *corev1.Pod {
				p := npPod(map[string]string{"app": "echo"}, cport("http", 80))
				p.Spec.HostNetwork = true
				return p
			}()},
			ports:    port80,
			policies: []*networkingv1.NetworkPolicy{np("deny", map[string]string{"app": "echo"}, ingressOnly)},
			want:     policyAdvisory,
		},
		{
			name: "mixed endpoint pods - one denied one allowed is advisory",
			pods: []*corev1.Pod{
				npPod(map[string]string{"app": "fleet"}, cport("http", 80)),
				npPod(map[string]string{"app": "fleet", "tier": "web"}, cport("http", 80)),
			},
			ports: port80,
			policies: []*networkingv1.NetworkPolicy{
				np("deny", map[string]string{"app": "fleet"}, ingressOnly),
				np("allowweb", map[string]string{"tier": "web"}, ingressOnly,
					networkingv1.NetworkPolicyIngressRule{Ports: []networkingv1.NetworkPolicyPort{tcpPort(80)}}),
			},
			want: policyAdvisory,
		},
		{
			name: "mixed - one pod default-denied, one pod selected by no policy is unrestricted",
			pods: []*corev1.Pod{
				npPod(map[string]string{"app": "fleet"}, cport("http", 80)),
				npPod(map[string]string{"app": "loner"}, cport("http", 80)),
			},
			ports: port80,
			// default-deny selects app=fleet only; app=loner is selected by nothing.
			policies: []*networkingv1.NetworkPolicy{np("deny", map[string]string{"app": "fleet"}, ingressOnly)},
			want:     policyAdvisory,
		},
		{
			name:  "matchExpressions selector matches",
			pods:  []*corev1.Pod{echoPod},
			ports: port80,
			policies: []*networkingv1.NetworkPolicy{{
				ObjectMeta: metav1.ObjectMeta{Name: "expr", Namespace: "ns"},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"echo", "fleet"}},
					}},
					PolicyTypes: ingressOnly,
				},
			}},
			want: policyWouldDeny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateNetpol(tt.pods, tt.ports, tt.policies)
			if got.verdict != tt.want {
				t.Errorf("verdict = %v, want %v", got.verdict, tt.want)
			}
			if tt.want == policyWouldDeny && got.wrongPort != tt.wantWrong {
				t.Errorf("wrongPort = %v, want %v", got.wrongPort, tt.wantWrong)
			}
		})
	}
}

func TestEffectivePolicyTypes(t *testing.T) {
	// Omitted policyTypes with no egress rules → ingress-only isolation.
	ingressDefault := &networkingv1.NetworkPolicy{}
	if got := effectivePolicyTypes(ingressDefault); !got.ingress || got.egress {
		t.Errorf("empty spec: got %+v, want ingress-only", got)
	}
	// Omitted policyTypes WITH egress rules → both.
	withEgress := &networkingv1.NetworkPolicy{Spec: networkingv1.NetworkPolicySpec{
		Egress: []networkingv1.NetworkPolicyEgressRule{{}},
	}}
	if got := effectivePolicyTypes(withEgress); !got.ingress || !got.egress {
		t.Errorf("egress rules present: got %+v, want both", got)
	}
	// Explicit egress-only → egress, not ingress.
	explicit := &networkingv1.NetworkPolicy{Spec: networkingv1.NetworkPolicySpec{PolicyTypes: egressOnly}}
	if got := effectivePolicyTypes(explicit); got.ingress || !got.egress {
		t.Errorf("explicit egress-only: got %+v, want egress-only", got)
	}
}

// A kube-system-resident workload whose egress rule allows port 53 to a
// same-namespace podSelector matching CoreDNS DOES reach DNS - the gap must not
// false-fire. A non-kube-system source keeps the prior "gap fires" behavior.
func TestRuleDestCoversDNS_KubeSystemSource(t *testing.T) {
	coreDNS := rule(nil, peerPod(sel("k8s-app", "kube-dns")))
	if got := ruleDestCoversDNS(&coreDNS, metav1.NamespaceSystem); got != triYes {
		t.Errorf("kube-system source + CoreDNS podSelector: dest cover = %v, want triYes", got)
	}
	// A kube-system source whose peer is NOT CoreDNS stays uncovered (gap may fire).
	other := rule(nil, peerPod(sel("app", "other")))
	if got := ruleDestCoversDNS(&other, metav1.NamespaceSystem); got == triYes {
		t.Errorf("kube-system source + non-CoreDNS podSelector: dest cover = %v, want not-triYes", got)
	}
	// A normal namespace source: same-namespace podSelector does not reach CoreDNS.
	if got := ruleDestCoversDNS(&coreDNS, "prod"); got == triYes {
		t.Errorf("non-kube-system source: same-ns podSelector must not cover DNS, got %v", got)
	}
}
