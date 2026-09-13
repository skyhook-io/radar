package netpol

import (
	"fmt"
	"net"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Effect is one policy's answer for one connection. It is deliberately not a
// verdict: NetworkPolicy is additive, so a policy that does not admit a
// connection blocks nothing on its own — only the absence of any admitting
// policy does. Callers fold Effects across the policies that select a pod;
// Evaluate does exactly that.
type Effect int

const (
	// NotApplicable: the policy does not select the pod, or does not isolate
	// this direction. It contributes nothing either way.
	NotApplicable Effect = iota
	// Admits: a rule of this policy admits the connection.
	Admits
	// DoesNotAdmit: the policy isolates the pod in this direction and none of
	// its rules admit the connection.
	DoesNotAdmit
	// Undecidable: the answer depends on something the static model cannot
	// see — a rewritten address, a pod the caller could not resolve, a
	// hostNetwork pod, a port the caller does not know.
	Undecidable
)

func (e Effect) String() string {
	switch e {
	case Admits:
		return "admits"
	case DoesNotAdmit:
		return "does not admit"
	case Undecidable:
		return "undecidable"
	default:
		return "not applicable"
	}
}

// Explanation is an Effect with the sentence that justifies it, written for
// an operator reading a policy list.
type Explanation struct {
	Effect Effect
	Reason string
}

// Explain answers, for one policy, whether it admits a connection in dir.
// selected is the pod the policy would have to select — the destination for
// ingress, the source for egress — and peer is the other end. port and proto
// are the destination port; port 0 means the caller does not know it.
//
// Evaluate is built on this: the two must never disagree about what a rule
// means, so there is one rule walk and Evaluate folds its answers.
func Explain(np *networkingv1.NetworkPolicy, dir Direction, selected *corev1.Pod, peer Peer, port int32, proto corev1.Protocol) Explanation {
	if np == nil {
		return Explanation{NotApplicable, "no policy"}
	}
	if selected == nil {
		return Explanation{Undecidable, "the pod this policy would apply to is not known"}
	}
	selects, err := Selects(np, selected)
	if err != nil {
		return Explanation{Undecidable, "podSelector could not be parsed: " + err.Error()}
	}
	if !selects {
		return Explanation{NotApplicable, "does not select " + podRef(selected)}
	}
	types := EffectivePolicyTypes(np)
	switch dir {
	case DirectionIngress:
		if !types.Ingress {
			return Explanation{NotApplicable, "does not isolate ingress"}
		}
	case DirectionEgress:
		if !types.Egress {
			return Explanation{NotApplicable, "does not isolate egress"}
		}
	default:
		return Explanation{NotApplicable, "no direction"}
	}
	if selected.Spec.HostNetwork {
		return Explanation{Undecidable, podRef(selected) + " runs on the host network, where NetworkPolicy does not apply the same way"}
	}
	// Whether a selector can match a host-network pod at the other end is up
	// to the network plugin, so nothing about such a peer is decidable.
	if peer.Pod != nil && peer.Pod.Spec.HostNetwork {
		return Explanation{Undecidable, podRef(peer.Pod) + " runs on the host network; whether policies see it as a pod depends on the network plugin"}
	}

	var rules []ruleView
	if dir == DirectionIngress {
		for i := range np.Spec.Ingress {
			rules = append(rules, ruleView{ports: np.Spec.Ingress[i].Ports, peers: np.Spec.Ingress[i].From})
		}
	} else {
		for i := range np.Spec.Egress {
			rules = append(rules, ruleView{ports: np.Spec.Egress[i].Ports, peers: np.Spec.Egress[i].To})
		}
	}
	if len(rules) == 0 {
		return Explanation{DoesNotAdmit, fmt.Sprintf("isolates %s with no rules — admits nothing", dir)}
	}

	// A named rule port resolves against the destination pod: the selected pod
	// for ingress, the peer for egress. A non-pod destination has no named
	// ports, so such a rule simply does not match it; a destination pod the
	// caller could not resolve leaves the question open.
	namedPortPod := selected
	if dir == DirectionEgress {
		namedPortPod = peer.Pod
	}
	portText := portDesc(port, proto)
	peerText := peerDesc(peer, dir)

	overall := triNo
	sawPortMatch := false
	var reasons []string
	for i, rule := range rules {
		if len(rule.ports) > 0 {
			if port <= 0 {
				overall = fold(overall, triUnknown)
				reasons = append(reasons, fmt.Sprintf("rule %d restricts ports but the port is not known", i+1))
				continue
			}
			switch rulePortsMatch(rule.ports, port, proto, namedPortPod, peer.External) {
			case triNo:
				continue
			case triUnknown:
				overall = fold(overall, triUnknown)
				reasons = append(reasons, fmt.Sprintf("rule %d names a port on a destination pod that could not be resolved", i+1))
				continue
			}
		}
		sawPortMatch = true
		if len(rule.peers) == 0 {
			what := "any source"
			if dir == DirectionEgress {
				what = "any destination"
			}
			return Explanation{Admits, fmt.Sprintf("rule %d admits %s on %s", i+1, what, portText)}
		}
		ruleTri := triNo
		for j := range rule.peers {
			t, desc := peerAdmits(&rule.peers[j], peer, np.Namespace, dir)
			switch t {
			case triYes:
				return Explanation{Admits, fmt.Sprintf("rule %d admits %s on %s", i+1, desc, portText)}
			case triUnknown:
				ruleTri = fold(ruleTri, triUnknown)
				reasons = append(reasons, desc)
			}
		}
		overall = fold(overall, ruleTri)
	}

	if overall == triUnknown {
		return Explanation{Undecidable, strings.Join(dedupe(reasons), "; ")}
	}
	if !sawPortMatch {
		return Explanation{DoesNotAdmit, fmt.Sprintf("its rules admit other ports, none covers %s", portText)}
	}
	return Explanation{DoesNotAdmit, fmt.Sprintf("no rule admits %s on %s", peerText, portText)}
}

type ruleView struct {
	ports []networkingv1.NetworkPolicyPort
	peers []networkingv1.NetworkPolicyPeer
}

// rulePortsMatch treats a rule's port entries as the alternatives they are:
// a numeric entry that covers the port is a definite match whatever the
// named entries beside it say, and only a named entry that cannot be
// resolved — the destination pod is unknown — leaves the question open.
// A non-pod destination has no named ports, so a named entry never matches it.
func rulePortsMatch(ports []networkingv1.NetworkPolicyPort, port int32, proto corev1.Protocol, namedPortPod *corev1.Pod, external bool) tri {
	out := triNo
	for i := range ports {
		one := ports[i : i+1]
		named := ports[i].Port != nil && ports[i].Port.Type == 1 // intstr.String
		if named && namedPortPod == nil && !external {
			out = fold(out, triUnknown)
			continue
		}
		if RuleMatchesPort(one, port, proto, namedPortPod) {
			return triYes
		}
	}
	return out
}

// peerAdmits decides whether one from/to entry admits target and describes
// the entry. policyNs is the namespace the rule lives in — a podSelector
// without a namespaceSelector only ever matches pods there.
//
// A selector can only ever match a pod, so it never admits an external
// endpoint. An ipBlock is compared against the address the policy would see:
// a match admits; a miss is undecidable on ingress (the source may have been
// rewritten before the policy saw it) and on egress to a pod (a cluster IP is
// rewritten by DNAT), but decisive on egress to an external destination,
// whose address nothing in the cluster rewrites.
func peerAdmits(entry *networkingv1.NetworkPolicyPeer, target Peer, policyNs string, dir Direction) (tri, string) {
	if entry.IPBlock != nil {
		desc := "ipBlock " + entry.IPBlock.CIDR
		if len(entry.IPBlock.Except) > 0 {
			desc += " except " + strings.Join(entry.IPBlock.Except, ", ")
		}
		if dir == DirectionEgress && !target.External {
			// Whether the policy sees the pod's address or, for traffic that
			// went through a Service, the cluster IP is up to the network
			// plugin; neither a hit nor a miss on the pod's address proves
			// anything.
			return triUnknown, desc + ": the destination address the policy sees for a pod cannot be established"
		}
		switch ipBlockMatch(entry.IPBlock, peerIPs(target)) {
		case triYes:
			return triYes, desc
		case triNo:
			if dir == DirectionEgress {
				// An external destination's address is not rewritten by
				// anything in the cluster: a miss is a miss.
				return triNo, desc
			}
			return triUnknown, desc + ": the source address the policy sees may have been rewritten"
		default:
			return triUnknown, desc + ": no address of that family is known for " + peerDesc(target, dir)
		}
	}
	if entry.NamespaceSelector == nil && entry.PodSelector == nil {
		return triUnknown, "an empty peer entry"
	}
	desc := selectorPeerDesc(entry)
	if target.External {
		return triNo, desc
	}
	if target.Pod == nil {
		return triUnknown, desc + ": the pod at the other end could not be resolved"
	}

	ns := triYes
	if entry.NamespaceSelector == nil {
		if target.Pod.Namespace != policyNs {
			ns = triNo
		}
	} else if target.Namespace == nil {
		ns = triUnknown
	} else {
		sel, err := metav1.LabelSelectorAsSelector(entry.NamespaceSelector)
		if err != nil {
			ns = triUnknown
		} else if !sel.Matches(labels.Set(target.Namespace.Labels)) {
			ns = triNo
		}
	}
	if ns == triNo {
		return triNo, desc
	}

	pod := triYes
	if entry.PodSelector != nil {
		sel, err := metav1.LabelSelectorAsSelector(entry.PodSelector)
		if err != nil {
			pod = triUnknown
		} else if !sel.Matches(labels.Set(target.Pod.Labels)) {
			pod = triNo
		}
	}
	if pod == triNo {
		return triNo, desc
	}
	if ns == triUnknown {
		return triUnknown, desc + ": the Namespace of " + podRef(target.Pod) + " could not be read, so its labels are unknown"
	}
	if pod == triUnknown {
		return triUnknown, desc + ": selector could not be parsed"
	}
	return triYes, desc
}

func selectorPeerDesc(entry *networkingv1.NetworkPolicyPeer) string {
	var parts []string
	if entry.PodSelector != nil {
		parts = append(parts, "pods "+selectorDesc(entry.PodSelector))
	}
	switch {
	case entry.NamespaceSelector != nil && entry.PodSelector != nil:
		parts = append(parts, "in namespaces "+selectorDesc(entry.NamespaceSelector))
	case entry.NamespaceSelector != nil:
		parts = append(parts, "any pod in namespaces "+selectorDesc(entry.NamespaceSelector))
	default:
		parts = append(parts, "in this namespace")
	}
	return strings.Join(parts, " ")
}

func selectorDesc(sel *metav1.LabelSelector) string {
	if sel == nil {
		return "matching anything"
	}
	var parts []string
	keys := make([]string, 0, len(sel.MatchLabels))
	for k := range sel.MatchLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+sel.MatchLabels[k])
	}
	for _, e := range sel.MatchExpressions {
		if len(e.Values) > 0 {
			parts = append(parts, fmt.Sprintf("%s %s (%s)", e.Key, e.Operator, strings.Join(e.Values, ",")))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s", e.Key, e.Operator))
		}
	}
	if len(parts) == 0 {
		return "matching anything"
	}
	return "matching " + strings.Join(parts, ", ")
}

func peerDesc(p Peer, dir Direction) string {
	role := "the source"
	if dir == DirectionEgress {
		role = "the destination"
	}
	switch {
	case p.Pod != nil:
		return role + " " + podRef(p.Pod)
	case p.External && p.IP != "":
		return role + " " + p.IP + " (not a pod)"
	case p.External:
		return role + " (not a pod)"
	default:
		return role
	}
}

func podRef(pod *corev1.Pod) string {
	if pod == nil {
		return "an unknown pod"
	}
	if pod.Namespace == "" {
		return pod.Name
	}
	return pod.Namespace + "/" + pod.Name
}

func portDesc(port int32, proto corev1.Protocol) string {
	p := string(ProtocolOrTCP(string(proto)))
	if port <= 0 {
		return p + "/?"
	}
	return fmt.Sprintf("%s/%d", p, port)
}

// peerIPs prefers the address the caller observed: a flow seen on one of a
// dual-stack pod's addresses must not be admitted on the strength of the
// other. Without an observed address, a pod's whole address set stands in.
func peerIPs(p Peer) []net.IP {
	if ip := net.ParseIP(p.IP); ip != nil {
		return []net.IP{ip}
	}
	if p.Pod != nil {
		return podIPs(p.Pod)
	}
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
