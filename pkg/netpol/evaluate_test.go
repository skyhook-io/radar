package netpol

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func pod(ns, name, ip string, lbls map[string]string, ports ...corev1.ContainerPort) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: lbls},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: ports}}},
		Status:     corev1.PodStatus{PodIP: ip},
	}
}

func namespace(name string, lbls map[string]string) *corev1.Namespace {
	if lbls == nil {
		lbls = map[string]string{}
	}
	lbls["kubernetes.io/metadata.name"] = name
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls}}
}

func policy(ns, name string, podSel map[string]string, types []networkingv1.PolicyType, ingress []networkingv1.NetworkPolicyIngressRule, egress []networkingv1.NetworkPolicyEgressRule) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podSel},
			PolicyTypes: types,
			Ingress:     ingress,
			Egress:      egress,
		},
	}
}

func tcpPort(n int32) networkingv1.NetworkPolicyPort {
	p := intstr.FromInt32(n)
	return networkingv1.NetworkPolicyPort{Port: &p}
}

func namedPort(n string) networkingv1.NetworkPolicyPort {
	p := intstr.FromString(n)
	return networkingv1.NetworkPolicyPort{Port: &p}
}

var (
	promLabels  = map[string]string{"app": "prometheus"}
	radarLabels = map[string]string{"app": "radar"}
	ingressT    = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
	egressT     = []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}
)

func radar() Peer {
	return Peer{Pod: pod("radar", "radar-1", "10.0.1.5", radarLabels), Namespace: namespace("radar", map[string]string{"team": "platform"})}
}

func prom(port int32) Backend {
	return Backend{
		Peer:     Peer{Pod: pod("monitoring", "prom-0", "10.0.2.9", promLabels, corev1.ContainerPort{Name: "web", ContainerPort: 9090}), Namespace: namespace("monitoring", nil)},
		Port:     port,
		Protocol: corev1.ProtocolTCP,
	}
}

func TestEvaluate(t *testing.T) {
	fromRadarNs := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "radar"}}}
	fromOtherNs := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "other"}}}
	fromRadarPodsAnyNs := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: &metav1.LabelSelector{MatchLabels: radarLabels}}
	fromLocalPromPods := networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: promLabels}}
	toMonitoring := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "monitoring"}}}

	tests := []struct {
		name     string
		src      Peer
		dst      []Backend
		policies []*networkingv1.NetworkPolicy
		want     Verdict
	}{
		{
			name: "no policies is allowed",
			src:  radar(), dst: []Backend{prom(9090)},
			want: Verdict{Kind: Allowed},
		},
		{
			name: "default deny ingress on the backend",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "deny-all", nil, ingressT, nil, nil)},
			want:     Verdict{Kind: Denied, Direction: DirectionIngress, Policies: []string{"monitoring/deny-all"}},
		},
		{
			name: "allow-all rule admits",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "open", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{}}, nil)},
			want: Verdict{Kind: Allowed},
		},
		{
			name: "namespaceSelector matching the source namespace admits",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "from-radar", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil)},
			want: Verdict{Kind: Allowed},
		},
		{
			name: "namespaceSelector for another namespace denies",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "from-other", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromOtherNs}}}, nil)},
			want: Verdict{Kind: Denied, Direction: DirectionIngress, Policies: []string{"monitoring/from-other"}},
		},
		{
			name: "podSelector without namespaceSelector only matches the policy's namespace",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "local-only", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromLocalPromPods}}}, nil)},
			want: Verdict{Kind: Denied, Direction: DirectionIngress, Policies: []string{"monitoring/local-only"}},
		},
		{
			name: "combined namespace and pod selectors admit a matching pod anywhere",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "radar-pods", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromRadarPodsAnyNs}}}, nil)},
			want: Verdict{Kind: Allowed},
		},
		{
			name: "ipBlock containing the source IP admits",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "cidr", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/16"}}}}}, nil)},
			want: Verdict{Kind: Allowed},
		},
		{
			name: "ipBlock except carving out the source IP denies",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "cidr-except", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/16", Except: []string{"10.0.1.0/24"}}}}}}, nil)},
			want: Verdict{Kind: Denied, Direction: DirectionIngress, Policies: []string{"monitoring/cidr-except"}},
		},
		{
			name: "ipBlock without a known source IP is undecidable",
			src:  Peer{Pod: pod("radar", "radar-1", "", radarLabels), Namespace: namespace("radar", nil)}, dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "cidr", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/16"}}}}}, nil)},
			want: Verdict{Kind: Unknown},
		},
		{
			name: "rule for a different port denies this one",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "other-port", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{tcpPort(8080)}, From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil)},
			want: Verdict{Kind: Denied, Direction: DirectionIngress, Policies: []string{"monitoring/other-port"}},
		},
		{
			name: "named port resolved on the backend pod admits",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "named", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{namedPort("web")}, From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil)},
			want: Verdict{Kind: Allowed},
		},
		{
			name: "endPort range covering the port admits",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: func() []*networkingv1.NetworkPolicy {
				p := tcpPort(9000)
				end := int32(9100)
				p.EndPort = &end
				return []*networkingv1.NetworkPolicy{policy("monitoring", "range", promLabels, ingressT,
					[]networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{p}, From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil)}
			}(),
			want: Verdict{Kind: Allowed},
		},
		{
			name: "egress-isolated source with no rule to the backend denies on the egress side",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{
				policy("radar", "egress-dns-only", radarLabels, egressT, nil,
					[]networkingv1.NetworkPolicyEgressRule{{Ports: []networkingv1.NetworkPolicyPort{tcpPort(53)}}}),
			},
			want: Verdict{Kind: Denied, Direction: DirectionEgress, Policies: []string{"radar/egress-dns-only"}},
		},
		{
			name: "egress rule to the monitoring namespace admits, ingress then decides",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{
				policy("radar", "egress", radarLabels, egressT, nil,
					[]networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{toMonitoring}}}),
				policy("monitoring", "deny-all", nil, ingressT, nil, nil),
			},
			want: Verdict{Kind: Denied, Direction: DirectionIngress, Policies: []string{"monitoring/deny-all"}},
		},
		{
			name: "egress ipBlock rules are undecidable",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{
				policy("radar", "egress-cidr", radarLabels, egressT, nil,
					[]networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.2.0/24"}}}}}),
			},
			want: Verdict{Kind: Unknown},
		},
		{
			name: "policyTypes omitted isolates ingress only",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "implicit", promLabels, nil, nil, nil)},
			want:     Verdict{Kind: Denied, Direction: DirectionIngress, Policies: []string{"monitoring/implicit"}},
		},
		{
			name: "policyTypes omitted with egress rules on the source isolates egress too",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("radar", "implicit-egress", radarLabels, nil, nil,
				[]networkingv1.NetworkPolicyEgressRule{{Ports: []networkingv1.NetworkPolicyPort{tcpPort(53)}}})},
			want: Verdict{Kind: Denied, Direction: DirectionEgress, Policies: []string{"radar/implicit-egress"}},
		},
		{
			name: "explicit egress-only policy does not isolate ingress",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "egress-only", promLabels, egressT, nil, nil)},
			want:     Verdict{Kind: Allowed},
		},
		{
			name: "policy in an unrelated namespace is ignored",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("other", "deny-all", nil, ingressT, nil, nil)},
			want:     Verdict{Kind: Allowed},
		},
		{
			name: "additive: a second policy admitting the source wins over a deny-all",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{
				policy("monitoring", "deny-all", nil, ingressT, nil, nil),
				policy("monitoring", "from-radar", promLabels, ingressT,
					[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil),
			},
			want: Verdict{Kind: Allowed},
		},
		{
			name: "mixed backends: one unselected pod keeps the service reachable",
			src:  radar(),
			dst: []Backend{prom(9090), {
				Peer:     Peer{Pod: pod("monitoring", "prom-canary", "10.0.2.10", map[string]string{"app": "prometheus-canary"}), Namespace: namespace("monitoring", nil)},
				Port:     9090,
				Protocol: corev1.ProtocolTCP,
			}},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "deny-prom", promLabels, ingressT, nil, nil)},
			want:     Verdict{Kind: Allowed},
		},
		{
			name: "heterogeneous target ports: a rule for one backend's port admits",
			src:  radar(),
			dst: []Backend{prom(9090), {
				Peer:     Peer{Pod: pod("monitoring", "prom-alt", "10.0.2.11", promLabels), Namespace: namespace("monitoring", nil)},
				Port:     9091,
				Protocol: corev1.ProtocolTCP,
			}},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "alt-port", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{tcpPort(9091)}, From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil)},
			want: Verdict{Kind: Allowed},
		},
		{
			name: "namespaceSelector without the source Namespace object is undecidable",
			src:  Peer{Pod: pod("radar", "radar-1", "10.0.1.5", radarLabels)}, dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "from-radar", promLabels, ingressT,
				[]networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{fromRadarNs}}}, nil)},
			want: Verdict{Kind: Unknown},
		},
		{
			name: "hostNetwork backend is undecidable",
			src:  radar(),
			dst: func() []Backend {
				b := prom(9090)
				b.Pod.Spec.HostNetwork = true
				return []Backend{b}
			}(),
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "deny-all", nil, ingressT, nil, nil)},
			want:     Verdict{Kind: Unknown},
		},
		{
			name: "malformed selector is undecidable",
			src:  radar(), dst: []Backend{prom(9090)},
			policies: []*networkingv1.NetworkPolicy{{
				ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "bad"},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}},
					PolicyTypes: ingressT,
				},
			}},
			want: Verdict{Kind: Unknown},
		},
		{
			name: "no backends is undecidable",
			src:  radar(), dst: nil,
			policies: []*networkingv1.NetworkPolicy{policy("monitoring", "deny-all", nil, ingressT, nil, nil)},
			want:     Verdict{Kind: Unknown},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(tt.src, tt.dst, tt.policies)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Evaluate() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
