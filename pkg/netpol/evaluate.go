package netpol

import (
	"net"
	"sort"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Peer is one end of a connection: the pod and, when the caller could read
// it, its Namespace object. namespaceSelector rules match the Namespace's
// labels, so a nil Namespace makes every such rule undecidable rather than
// silently unmatched.
type Peer struct {
	Pod       *corev1.Pod
	Namespace *corev1.Namespace
}

// Backend is a destination pod together with the port and protocol the
// connection targets on it. A Service's targetPort can resolve to different
// numbers on different pods, so each backend carries its own.
type Backend struct {
	Peer
	Port     int32
	Protocol corev1.Protocol
}

// Kind is how certain a verdict is. Only Denied is evidence: it means every
// policy the caller supplied was examined and none admits the connection.
// Unknown covers everything the static model cannot decide, and must never be
// read as "not blocked".
type Kind int

const (
	Unknown Kind = iota
	Allowed
	Denied
)

func (k Kind) String() string {
	switch k {
	case Allowed:
		return "allowed"
	case Denied:
		return "denied"
	default:
		return "unknown"
	}
}

// Direction names which end's policies produced a denial.
type Direction string

const (
	DirectionIngress Direction = "ingress"
	DirectionEgress  Direction = "egress"
)

// Verdict is the outcome of Evaluate. Policies lists, as namespace/name in
// sorted order, the policies that together isolate the denied end — under
// additive semantics no single one of them is "the" blocker.
type Verdict struct {
	Kind      Kind
	Direction Direction
	Policies  []string
}

type tri int

const (
	triUnknown tri = iota
	triYes
	triNo
)

// Evaluate decides whether the supplied policies deny a connection from src to
// the given backends. policies should be every NetworkPolicy the caller can
// see in the source and destination namespaces; the caller is responsible for
// only calling this when that list is complete, since a policy missing from it
// could be the one that admits the traffic.
//
// Denied requires positive evidence at one end: the source is egress-isolated
// and no rule admits any backend, or every backend is ingress-isolated and no
// rule admits the source. Anything the model cannot decide — hostNetwork pods,
// selectors that need a Namespace the caller didn't supply, ipBlock rules on
// the egress side (cluster-IP DNAT makes the matched address CNI-defined),
// malformed selectors — yields Unknown.
func Evaluate(src Peer, dst []Backend, policies []*networkingv1.NetworkPolicy) Verdict {
	if src.Pod == nil || src.Pod.Spec.HostNetwork || len(dst) == 0 {
		return Verdict{Kind: Unknown}
	}
	for _, b := range dst {
		if b.Pod == nil || b.Pod.Spec.HostNetwork {
			return Verdict{Kind: Unknown}
		}
	}

	egress := evaluateEgress(src, dst, policies)
	if egress.Kind == Denied {
		return egress
	}
	ingress := evaluateIngress(src, dst, policies)
	if ingress.Kind == Denied {
		return ingress
	}
	if egress.Kind == Unknown || ingress.Kind == Unknown {
		return Verdict{Kind: Unknown}
	}
	return Verdict{Kind: Allowed}
}

func evaluateEgress(src Peer, dst []Backend, policies []*networkingv1.NetworkPolicy) Verdict {
	selecting, ok := selectingPolicies(src.Pod, policies, func(t PolicyTypes) bool { return t.Egress })
	if !ok {
		return Verdict{Kind: Unknown}
	}
	if len(selecting) == 0 {
		return Verdict{Kind: Allowed}
	}
	overall := triNo
	for _, b := range dst {
		if samePod(src.Pod, b.Pod) {
			overall = fold(overall, triYes)
			continue
		}
		verdict := triNo
		for _, np := range selecting {
			for i := range np.Spec.Egress {
				rule := &np.Spec.Egress[i]
				// Named ports in an egress rule refer to the destination pod.
				if !RuleMatchesPort(rule.Ports, b.Port, b.Protocol, b.Pod) {
					continue
				}
				verdict = fold(verdict, peersAdmit(rule.To, b.Peer, np.Namespace, DirectionEgress))
			}
		}
		overall = fold(overall, verdict)
	}
	return verdictFor(overall, DirectionEgress, selecting)
}

func evaluateIngress(src Peer, dst []Backend, policies []*networkingv1.NetworkPolicy) Verdict {
	overall := triNo
	var isolating []*networkingv1.NetworkPolicy
	for _, b := range dst {
		// A pod can never block traffic to itself.
		if samePod(src.Pod, b.Pod) {
			overall = fold(overall, triYes)
			continue
		}
		selecting, ok := selectingPolicies(b.Pod, policies, func(t PolicyTypes) bool { return t.Ingress })
		if !ok {
			return Verdict{Kind: Unknown}
		}
		if len(selecting) == 0 {
			// NetworkPolicy is additive: an unselected pod accepts everything.
			overall = fold(overall, triYes)
			continue
		}
		isolating = append(isolating, selecting...)
		verdict := triNo
		for _, np := range selecting {
			for i := range np.Spec.Ingress {
				rule := &np.Spec.Ingress[i]
				if !RuleMatchesPort(rule.Ports, b.Port, b.Protocol, b.Pod) {
					continue
				}
				verdict = fold(verdict, peersAdmit(rule.From, src, np.Namespace, DirectionIngress))
			}
		}
		overall = fold(overall, verdict)
	}
	return verdictFor(overall, DirectionIngress, isolating)
}

// peersAdmit is a rule's from/to list as one answer: empty admits everyone,
// otherwise one admitting entry is enough.
func peersAdmit(peers []networkingv1.NetworkPolicyPeer, target Peer, policyNs string, dir Direction) tri {
	if len(peers) == 0 {
		return triYes
	}
	out := triNo
	for i := range peers {
		out = fold(out, peerAdmits(&peers[i], target, policyNs, dir))
	}
	return out
}

// fold merges answers that are alternatives to each other (rules of one
// policy, entries of one rule, backends of one Service): admitted as soon as
// one says yes, denied only when all say no, undecidable otherwise.
func fold(acc, next tri) tri {
	switch {
	case acc == triYes || next == triYes:
		return triYes
	case acc == triUnknown || next == triUnknown:
		return triUnknown
	default:
		return triNo
	}
}

func verdictFor(t tri, dir Direction, policies []*networkingv1.NetworkPolicy) Verdict {
	switch t {
	case triYes:
		return Verdict{Kind: Allowed}
	case triNo:
		return Verdict{Kind: Denied, Direction: dir, Policies: policyNames(policies)}
	default:
		return Verdict{Kind: Unknown}
	}
}

// selectingPolicies returns the policies in the pod's namespace whose
// podSelector matches it and whose effective types include the requested
// direction. ok is false when a selector could not be parsed: the policy's
// effect is then undecidable and no verdict may be built on the rest.
func selectingPolicies(pod *corev1.Pod, policies []*networkingv1.NetworkPolicy, wants func(PolicyTypes) bool) ([]*networkingv1.NetworkPolicy, bool) {
	var out []*networkingv1.NetworkPolicy
	for _, np := range policies {
		if np == nil || np.Namespace != pod.Namespace {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
		if err != nil {
			return nil, false
		}
		if !sel.Matches(labels.Set(pod.Labels)) {
			continue
		}
		if wants(EffectivePolicyTypes(np)) {
			out = append(out, np)
		}
	}
	return out, true
}

// peerAdmits decides whether one from/to entry admits target. policyNs is the
// namespace the rule lives in — a podSelector without a namespaceSelector
// only ever matches pods there.
func peerAdmits(peer *networkingv1.NetworkPolicyPeer, target Peer, policyNs string, dir Direction) tri {
	if peer.IPBlock != nil {
		if dir == DirectionEgress {
			return triUnknown
		}
		// A match admits. A miss proves nothing: whether the policy sees the
		// pod's address or a rewritten one (kube-proxy masquerade, a mesh
		// sidecar) is up to the network plugin.
		if ipBlockAdmits(peer.IPBlock, podIPs(target.Pod)) == triYes {
			return triYes
		}
		return triUnknown
	}
	if peer.NamespaceSelector == nil && peer.PodSelector == nil {
		return triUnknown
	}

	ns := triYes
	if peer.NamespaceSelector == nil {
		if target.Pod.Namespace != policyNs {
			ns = triNo
		}
	} else {
		if target.Namespace == nil {
			ns = triUnknown
		} else {
			sel, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
			if err != nil {
				ns = triUnknown
			} else if !sel.Matches(labels.Set(target.Namespace.Labels)) {
				ns = triNo
			}
		}
	}
	if ns == triNo {
		return triNo
	}

	pod := triYes
	if peer.PodSelector != nil {
		sel, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
		if err != nil {
			pod = triUnknown
		} else if !sel.Matches(labels.Set(target.Pod.Labels)) {
			pod = triNo
		}
	}
	if pod == triNo {
		return triNo
	}
	if ns == triUnknown || pod == triUnknown {
		return triUnknown
	}
	return triYes
}

// ipBlockAdmits reports whether one of the pod's addresses of the block's own
// family is inside it and outside every exception — a dual-stack pod reaches
// an IPv6 backend from its IPv6 address, which an IPv4 block says nothing
// about.
func ipBlockAdmits(block *networkingv1.IPBlock, ips []net.IP) tri {
	_, cidr, err := net.ParseCIDR(block.CIDR)
	if err != nil {
		return triUnknown
	}
	v4 := cidr.IP.To4() != nil
	for _, ip := range ips {
		if (ip.To4() != nil) != v4 || !cidr.Contains(ip) {
			continue
		}
		excepted := false
		for _, ex := range block.Except {
			_, exCIDR, err := net.ParseCIDR(ex)
			if err != nil {
				return triUnknown
			}
			if exCIDR.Contains(ip) {
				excepted = true
				break
			}
		}
		if excepted {
			continue
		}
		return triYes
	}
	return triUnknown
}

func podIPs(pod *corev1.Pod) []net.IP {
	var out []net.IP
	seen := map[string]bool{}
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		if ip := net.ParseIP(s); ip != nil {
			out = append(out, ip)
		}
	}
	add(pod.Status.PodIP)
	for _, p := range pod.Status.PodIPs {
		add(p.IP)
	}
	return out
}

func samePod(a, b *corev1.Pod) bool {
	if a.UID != "" && b.UID != "" {
		return a.UID == b.UID
	}
	return a.Namespace == b.Namespace && a.Name == b.Name
}

func policyNames(policies []*networkingv1.NetworkPolicy) []string {
	seen := map[string]bool{}
	var out []string
	for _, np := range policies {
		key := np.Namespace + "/" + np.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
