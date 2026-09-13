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
	// External marks an endpoint that is not a pod at all — a node, the host
	// network, the world outside the cluster. Selectors can never match it;
	// an ipBlock is matched against IP. A Peer with a nil Pod that is not
	// External is a pod the caller could not resolve, which is a different
	// thing: nothing about it is decidable.
	External bool
	// IP is the address the caller actually observed for this endpoint, when
	// it has one (a flow record). It takes precedence over the pod's own
	// address set for ipBlock matching.
	IP string
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
	// DirectionBoth: no single direction accounts for the denial — some
	// backends are cut off by the source's egress policies, the rest by their
	// own ingress policies — so both policy sets are named.
	DirectionBoth Direction = "ingress+egress"
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

	egressPolicies, ok := selectingPolicies(src.Pod, policies, func(t PolicyTypes) bool { return t.Egress })
	if !ok {
		return Verdict{Kind: Unknown}
	}

	// Each backend is judged on the complete path — the source's egress AND
	// the backend's ingress — and the connection succeeds if any backend's
	// path does. Folding the two directions separately would call a Service
	// reachable when egress admits only one backend and ingress only another.
	overall := triNo
	egressAll, ingressAll := triNo, triNo
	var ingressPolicies []*networkingv1.NetworkPolicy
	for _, b := range dst {
		// A pod can never block traffic to itself.
		if samePod(src.Pod, b.Pod) {
			overall = triYes
			egressAll, ingressAll = triYes, triYes
			continue
		}
		egress := egressAdmits(egressPolicies, src, b)
		selecting, ok := selectingPolicies(b.Pod, policies, func(t PolicyTypes) bool { return t.Ingress })
		if !ok {
			return Verdict{Kind: Unknown}
		}
		ingressPolicies = append(ingressPolicies, selecting...)
		ingress := ingressAdmits(selecting, src, b)
		overall = fold(overall, both(egress, ingress))
		egressAll = fold(egressAll, egress)
		ingressAll = fold(ingressAll, ingress)
	}

	switch overall {
	case triYes:
		return Verdict{Kind: Allowed}
	case triUnknown:
		return Verdict{Kind: Unknown}
	}
	switch {
	case egressAll == triNo:
		return Verdict{Kind: Denied, Direction: DirectionEgress, Policies: policyNames(egressPolicies)}
	case ingressAll == triNo:
		return Verdict{Kind: Denied, Direction: DirectionIngress, Policies: policyNames(ingressPolicies)}
	default:
		return Verdict{Kind: Denied, Direction: DirectionBoth, Policies: policyNames(append(append([]*networkingv1.NetworkPolicy{}, egressPolicies...), ingressPolicies...))}
	}
}

// egressAdmits answers for one backend against the policies that isolate the
// source's egress; none means the source is unrestricted.
func egressAdmits(selecting []*networkingv1.NetworkPolicy, src Peer, b Backend) tri {
	if len(selecting) == 0 {
		return triYes
	}
	verdict := triNo
	for _, np := range selecting {
		verdict = fold(verdict, effectTri(Explain(np, DirectionEgress, src.Pod, b.Peer, b.Port, b.Protocol).Effect))
	}
	return verdict
}

// ingressAdmits answers for one backend against the policies that isolate
// its ingress; none means the pod accepts everything (NetworkPolicy is
// additive — an unselected pod is open).
func ingressAdmits(selecting []*networkingv1.NetworkPolicy, src Peer, b Backend) tri {
	if len(selecting) == 0 {
		return triYes
	}
	verdict := triNo
	for _, np := range selecting {
		verdict = fold(verdict, effectTri(Explain(np, DirectionIngress, b.Pod, src, b.Port, b.Protocol).Effect))
	}
	return verdict
}

// effectTri maps a policy's Effect onto the fold. A policy that does not
// apply must not count as a refusal, so it folds as neither.
func effectTri(e Effect) tri {
	switch e {
	case Admits:
		return triYes
	case Undecidable:
		return triUnknown
	default:
		return triNo
	}
}

// both merges the two legs of one path: the path works only if both do, is
// denied as soon as one is, and is undecidable otherwise.
func both(a, b tri) tri {
	switch {
	case a == triNo || b == triNo:
		return triNo
	case a == triUnknown || b == triUnknown:
		return triUnknown
	default:
		return triYes
	}
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

// Selects reports whether a policy applies to a pod: same namespace and a
// podSelector match (an empty selector matches every pod there). The error is
// a malformed selector, which callers decide how to treat — an evaluator that
// must be certain treats it as undecidable, a display may just skip it.
func Selects(np *networkingv1.NetworkPolicy, pod *corev1.Pod) (bool, error) {
	if np == nil || pod == nil || np.Namespace != pod.Namespace {
		return false, nil
	}
	sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
	if err != nil {
		return false, err
	}
	return sel.Matches(labels.Set(pod.Labels)), nil
}

// selectingPolicies returns the policies that select the pod and whose
// effective types include the requested direction. ok is false when a
// selector could not be parsed: the policy's effect is then undecidable and
// no verdict may be built on the rest.
func selectingPolicies(pod *corev1.Pod, policies []*networkingv1.NetworkPolicy, wants func(PolicyTypes) bool) ([]*networkingv1.NetworkPolicy, bool) {
	var out []*networkingv1.NetworkPolicy
	for _, np := range policies {
		selects, err := Selects(np, pod)
		if err != nil {
			return nil, false
		}
		if selects && wants(EffectivePolicyTypes(np)) {
			out = append(out, np)
		}
	}
	return out, true
}

// ipBlockMatch compares the block against the addresses of its own family —
// a dual-stack pod reaches an IPv6 backend from its IPv6 address, which an
// IPv4 block says nothing about. Yes: an address is inside the block and
// outside every exception. No: an address of the family exists and none
// qualifies. Unknown: no address of the family, or the block is malformed.
// Whether a "no" is decisive is the caller's question, not this one's.
func ipBlockMatch(block *networkingv1.IPBlock, ips []net.IP) tri {
	_, cidr, err := net.ParseCIDR(block.CIDR)
	if err != nil {
		return triUnknown
	}
	v4 := cidr.IP.To4() != nil
	out := triUnknown
	for _, ip := range ips {
		if (ip.To4() != nil) != v4 {
			continue
		}
		if out == triUnknown {
			out = triNo
		}
		if !cidr.Contains(ip) {
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
	return out
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
