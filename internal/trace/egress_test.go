package trace

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ── builders ──────────────────────────────────────────────────────────────

// egressTestPods is a single label-less pod that every empty-podSelector egress
// policy in these tests selects, so summarizeEgress's per-pod deny-all gate has
// a pod to evaluate against.
var egressTestPods = []*corev1.Pod{{}}

func egressPolicy(rules ...networkingv1.NetworkPolicyEgressRule) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      rules,
		},
	}
}

func sel(kv ...string) *metav1.LabelSelector {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return &metav1.LabelSelector{MatchLabels: m}
}

func tcp(port int32) networkingv1.NetworkPolicyPort {
	p := intstr.FromInt32(port)
	proto := corev1.ProtocolTCP
	return networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &p}
}

func udp(port int32) networkingv1.NetworkPolicyPort {
	p := intstr.FromInt32(port)
	proto := corev1.ProtocolUDP
	return networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &p}
}

func rule(ports []networkingv1.NetworkPolicyPort, peers ...networkingv1.NetworkPolicyPeer) networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{To: peers, Ports: ports}
}

func peerPod(s *metav1.LabelSelector) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{PodSelector: s}
}
func peerNS(s *metav1.LabelSelector) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{NamespaceSelector: s}
}
func peerPodNS(p, n *metav1.LabelSelector) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{PodSelector: p, NamespaceSelector: n}
}
func peerIP(cidr string, except ...string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr, Except: except}}
}

// ── chip text + DNS gate ──────────────────────────────────────────────────

func TestSummarizeEgress_Chips(t *testing.T) {
	tests := []struct {
		name     string
		pols     []*networkingv1.NetworkPolicy
		wantText []string // ordered chip texts (accent first then muted, then +N)
		wantDNS  bool     // dnsGapFires
	}{
		{
			name:     "podSelector same namespace (case 1)",
			pols:     []*networkingv1.NetworkPolicy{egressPolicy(rule([]networkingv1.NetworkPolicyPort{tcp(8080)}, peerPod(sel("app", "echo"))))},
			wantText: []string{"the echo app"},
			wantDNS:  true,
		},
		{
			name:     "namespaceSelector only (case 2)",
			pols:     []*networkingv1.NetworkPolicy{egressPolicy(rule([]networkingv1.NetworkPolicyPort{tcp(5432)}, peerNS(sel("team", "data"))))},
			wantText: []string{"pods in certain namespaces"},
			wantDNS:  true,
		},
		{
			name:     "intersection one peer (case 3)",
			pols:     []*networkingv1.NetworkPolicy{egressPolicy(rule([]networkingv1.NetworkPolicyPort{tcp(443)}, peerPodNS(sel("app", "api"), sel("env", "prod"))))},
			wantText: []string{"the api app in certain namespaces"},
			wantDNS:  true,
		},
		{
			name: "union with kube-system + UDP 53 → DNS covered (case 4)",
			pols: []*networkingv1.NetworkPolicy{egressPolicy(rule(
				[]networkingv1.NetworkPolicyPort{tcp(6379), udp(53)},
				peerPod(sel("app", "cache")),
				peerNS(sel("kubernetes.io/metadata.name", "kube-system")),
			))},
			wantText: []string{
				"pods in kube-system",
				"the cache app",
			},
			wantDNS: false,
		},
		{
			name:     "ipBlock cidr+except on 53 → uncertain → silent (case 5, corrected)",
			pols:     []*networkingv1.NetworkPolicy{egressPolicy(rule([]networkingv1.NetworkPolicyPort{udp(53), tcp(53)}, peerIP("10.0.0.0/8", "10.10.0.0/16")))},
			wantText: []string{"10.0.0.0/8 (minus 1 range)"},
			wantDNS:  false,
		},
		{
			name:     "ipBlock 0.0.0.0/0 on 443 → accent + DNS fires (case 6)",
			pols:     []*networkingv1.NetworkPolicy{egressPolicy(rule([]networkingv1.NetworkPolicyPort{tcp(443)}, peerIP("0.0.0.0/0")))},
			wantText: []string{"any IP address"},
			wantDNS:  true,
		},
		{
			name:     "empty to + empty ports → allow all (case 7)",
			pols:     []*networkingv1.NetworkPolicy{egressPolicy(rule(nil))},
			wantText: []string{"anywhere"},
			wantDNS:  false,
		},
		{
			name:     "all ports to restricted same-ns dest → DNS still fires (case 12)",
			pols:     []*networkingv1.NetworkPolicy{egressPolicy(rule(nil, peerPod(sel("app", "anything"))))},
			wantText: []string{"the anything app"},
			wantDNS:  true,
		},
		{
			name:     "match-all podSelector {} (case 15)",
			pols:     []*networkingv1.NetworkPolicy{egressPolicy(rule([]networkingv1.NetworkPolicyPort{tcp(80)}, peerPod(&metav1.LabelSelector{})))},
			wantText: []string{"any pod in this namespace"},
			wantDNS:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := summarizeEgress(tt.pols, egressTestPods)
			chips := s.chips()
			var got []string
			for _, c := range chips {
				got = append(got, c.Text)
			}
			if strings.Join(got, " | ") != strings.Join(tt.wantText, " | ") {
				t.Errorf("chips\n got=%q\nwant=%q", got, tt.wantText)
			}
			if s.dnsGapFires() != tt.wantDNS {
				t.Errorf("dnsGapFires=%v want %v (coverage=%d)", s.dnsGapFires(), tt.wantDNS, s.dnsCov)
			}
		})
	}
}

func TestSummarizeEgress_DenyAll(t *testing.T) {
	s := summarizeEgress([]*networkingv1.NetworkPolicy{egressPolicy()}, egressTestPods) // policyTypes:[Egress], no rules
	if !s.denyAllOut {
		t.Fatal("want denyAllOut")
	}
	if s.dnsGapFires() {
		t.Error("deny-all must suppress the DNS gap (subsumed)")
	}
	// Deny-all is stated in the message, not a chip - nothing to "reach".
	if chips := s.chips(); len(chips) != 0 {
		t.Errorf("deny-all wants no chips, got %+v", chips)
	}
	if s.chipsLabel() != "" {
		t.Errorf("deny-all wants no label, got %q", s.chipsLabel())
	}
	if !strings.Contains(s.message(), "can't open any outbound connection") {
		t.Errorf("deny-all message = %q", s.message())
	}
}

func TestSummarizeEgress_MatchExpressions(t *testing.T) {
	pod := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"db", "cache"}},
	}}
	s := summarizeEgress([]*networkingv1.NetworkPolicy{egressPolicy(rule([]networkingv1.NetworkPolicyPort{tcp(5432)}, peerPod(pod)))}, egressTestPods)
	chips := s.chips()
	if len(chips) != 1 {
		t.Fatalf("want 1 chip, got %d", len(chips))
	}
	// matchExpressions aren't humanizable → generic plain phrase (selector in tooltip).
	if chips[0].Text != "specific pods here" {
		t.Errorf("matchExpressions chip text = %q", chips[0].Text)
	}
}

func TestSummarizeEgress_UnionOverflowCap(t *testing.T) {
	// 5 muted dests + cap 3 → 3 shown + "+2 more".
	peers := []networkingv1.NetworkPolicyPeer{
		peerPod(sel("app", "a")), peerPod(sel("app", "b")), peerPod(sel("app", "c")),
		peerPod(sel("app", "d")), peerPod(sel("app", "e")),
	}
	s := summarizeEgress([]*networkingv1.NetworkPolicy{egressPolicy(rule([]networkingv1.NetworkPolicyPort{tcp(80)}, peers...))}, egressTestPods)
	chips := s.chips()
	if len(chips) != egressChipCap+1 {
		t.Fatalf("want %d chips (cap+overflow), got %d: %+v", egressChipCap+1, len(chips), chips)
	}
	last := chips[len(chips)-1]
	if last.Text != "+2 more" {
		t.Errorf("overflow chip = %q want %q", last.Text, "+2 more")
	}
}

func TestSummarizeEgress_NotIsolated(t *testing.T) {
	s := summarizeEgress(nil, egressTestPods)
	if s.isolated || s.dnsGapFires() || len(s.chips()) != 0 {
		t.Errorf("no egress policy → empty summary, got %+v", s)
	}
}

// A port RANGE covering 53 (e.g. UDP 1-65535) allows DNS, so the DNS gap must
// NOT fire - rulePort53 must honor EndPort, not only the exact port.
func TestEgress_PortRangeCovering53AllowsDNS(t *testing.T) {
	start := intstr.FromInt32(1)
	end := int32(65535)
	proto := corev1.ProtocolUDP
	p := egressPolicy(rule(
		[]networkingv1.NetworkPolicyPort{{Protocol: &proto, Port: &start, EndPort: &end}},
		peerNS(sel("kubernetes.io/metadata.name", "kube-system")),
	))
	s := summarizeEgress([]*networkingv1.NetworkPolicy{p}, egressTestPods)
	if s.dnsGapFires() {
		t.Error("a UDP 1-65535 range covers port 53 - the DNS gap must not fire (no false 'no rule allows DNS')")
	}
	if s.dnsCov != dnsCovered {
		t.Errorf("dnsCov = %d, want dnsCovered (range covers 53 to kube-system)", s.dnsCov)
	}
}

// A numeric range that does NOT cover 53 leaves DNS uncovered → the gap fires.
func TestEgress_PortRangeBelow53StillGaps(t *testing.T) {
	start := intstr.FromInt32(8000)
	end := int32(9000)
	proto := corev1.ProtocolTCP
	p := egressPolicy(rule(
		[]networkingv1.NetworkPolicyPort{{Protocol: &proto, Port: &start, EndPort: &end}},
		peerIP("0.0.0.0/0"),
	))
	s := summarizeEgress([]*networkingv1.NetworkPolicy{p}, egressTestPods)
	if !s.dnsGapFires() {
		t.Error("an 8000-9000 range does not cover 53 - the DNS gap should fire")
	}
}

func TestEgress_KubeSystemUnknownPodSelector53StaysSilent(t *testing.T) {
	// Source pods live in kube-system and the only peer is a same-namespace
	// podSelector on port 53 whose labels aren't the well-known CoreDNS set. The
	// real CoreDNS labels aren't visible here, so this is unprovable - uncertain,
	// not a definite non-cover. The DNS gap must NOT fire (fail toward silence),
	// mirroring the namespaceSelector branch.
	pods := []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Namespace: metav1.NamespaceSystem}}}
	p := egressPolicy(rule(
		[]networkingv1.NetworkPolicyPort{udp(53)},
		peerPod(sel("app", "custom-dns")),
	))
	s := summarizeEgress([]*networkingv1.NetworkPolicy{p}, pods)
	if s.dnsGapFires() {
		t.Error("unknown same-namespace podSelector in kube-system on port 53 must not fire the DNS gap (uncertain)")
	}
}

func TestEgress_NamedPort53StaysSilent(t *testing.T) {
	// Named port could be 53 → uncertain → fail toward silence (no DNS alarm).
	named := intstr.FromString("dns")
	proto := corev1.ProtocolUDP
	p := egressPolicy(rule(
		[]networkingv1.NetworkPolicyPort{{Protocol: &proto, Port: &named}},
		peerNS(sel("kubernetes.io/metadata.name", "kube-system")),
	))
	s := summarizeEgress([]*networkingv1.NetworkPolicy{p}, egressTestPods)
	if s.dnsGapFires() {
		t.Error("named port to kube-system must not fire the DNS gap (uncertain)")
	}
}

// S5: cluster DNS is primarily UDP, so a :53 port match only proves coverage when
// the rule permits UDP. A TCP-only :53 rule permits at most a TCP fallback, not
// primary UDP resolution, so it must NOT suppress the DNS-gap advisory.
func TestEgress_Port53ProtocolAwareness(t *testing.T) {
	kubeSystem := peerNS(sel("kubernetes.io/metadata.name", "kube-system"))
	tests := []struct {
		name    string
		ports   []networkingv1.NetworkPolicyPort
		wantCov dnsCoverage
		wantGap bool
	}{
		{
			name:    "UDP :53 -> covered (advisory suppressed)",
			ports:   []networkingv1.NetworkPolicyPort{udp(53)},
			wantCov: dnsCovered,
			wantGap: false,
		},
		{
			name:    "TCP-only :53 -> NOT covered (advisory fires)",
			ports:   []networkingv1.NetworkPolicyPort{tcp(53)},
			wantCov: dnsUncovered,
			wantGap: true,
		},
		{
			name:    "no ports (all protocols) -> covered",
			ports:   nil,
			wantCov: dnsCovered,
			wantGap: false,
		},
		{
			name:    "UDP + TCP :53 -> covered (UDP present)",
			ports:   []networkingv1.NetworkPolicyPort{udp(53), tcp(53)},
			wantCov: dnsCovered,
			wantGap: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := egressPolicy(rule(tt.ports, kubeSystem))
			s := summarizeEgress([]*networkingv1.NetworkPolicy{p}, egressTestPods)
			if s.dnsCov != tt.wantCov {
				t.Errorf("dnsCov = %d, want %d", s.dnsCov, tt.wantCov)
			}
			if s.dnsGapFires() != tt.wantGap {
				t.Errorf("dnsGapFires = %v, want %v", s.dnsGapFires(), tt.wantGap)
			}
		})
	}
}
