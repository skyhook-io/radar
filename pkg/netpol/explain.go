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
		return Explanation{Undecidable, "Radar could not interpret this policy's pod selector (" + err.Error() + ")"}
	}
	if !selects {
		return Explanation{NotApplicable, "does not select pod " + podRef(selected)}
	}
	types := EffectivePolicyTypes(np)
	switch dir {
	case DirectionIngress:
		if !types.Ingress {
			return Explanation{NotApplicable, "does not apply to incoming traffic"}
		}
	case DirectionEgress:
		if !types.Egress {
			return Explanation{NotApplicable, "does not apply to outgoing traffic"}
		}
	default:
		return Explanation{NotApplicable, "no direction"}
	}
	if selected.Spec.HostNetwork {
		return Explanation{Undecidable, "pod " + podRef(selected) + " runs on the host network, where NetworkPolicy does not apply the same way"}
	}
	// Whether a selector can match a host-network pod at the other end is up
	// to the network plugin, so nothing about such a peer is decidable.
	if peer.Pod != nil && peer.Pod.Spec.HostNetwork {
		return Explanation{Undecidable, "pod " + podRef(peer.Pod) + " runs on the host network; whether policies see it as a pod depends on the network plugin"}
	}
	if peer.Host {
		return Explanation{Undecidable, peerDesc(peer, dir) + " is a node or the host network; whether policies apply to it depends on the network plugin"}
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
		return Explanation{DoesNotAdmit, fmt.Sprintf("requires %s traffic to be explicitly allowed, but has no allow rules", flowWord(dir))}
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
			switch rulePortsMatch(rule.ports, port, proto, namedPortPod, peer.External) {
			case triNo:
				continue
			case triUnknown:
				overall = fold(overall, triUnknown)
				if port <= 0 {
					reasons = append(reasons, fmt.Sprintf("rule %d allows specific ports and the port of this connection is not known", i+1))
				} else {
					reasons = append(reasons, fmt.Sprintf("rule %d uses a named port and the destination pod can't be looked up", i+1))
				}
				continue
			}
		}
		sawPortMatch = true
		if len(rule.peers) == 0 {
			what := "from any source"
			if dir == DirectionEgress {
				what = "to any destination"
			}
			return Explanation{Admits, fmt.Sprintf("rule %d allows %s %s %s", i+1, flowWord(dir), portText, what)}
		}
		ruleTri := triNo
		for j := range rule.peers {
			t, desc := peerAdmits(&rule.peers[j], peer, np.Namespace, dir)
			switch t {
			case triYes:
				return Explanation{Admits, fmt.Sprintf("rule %d allows %s %s %s %s", i+1, flowWord(dir), portText, fromTo(dir), desc)}
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
		return Explanation{DoesNotAdmit, fmt.Sprintf("no rule allows %s; its rules cover other ports", portText)}
	}
	return Explanation{DoesNotAdmit, fmt.Sprintf("no rule allows %s %s %s %s", flowWord(dir), portText, fromTo(dir), peerText)}
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
//
// The protocol is decided before any port uncertainty: an entry for another
// protocol never matches, and an entry with a protocol but no port admits
// every port of it — both hold even when the caller's port is unknown.
func rulePortsMatch(ports []networkingv1.NetworkPolicyPort, port int32, proto corev1.Protocol, namedPortPod *corev1.Pod, external bool) tri {
	proto = ProtocolOrTCP(string(proto))
	out := triNo
	for i := range ports {
		entry := &ports[i]
		if ProtocolOrTCP(protoString(entry.Protocol)) != proto {
			continue
		}
		if entry.Port == nil {
			return triYes
		}
		named := entry.Port.Type == 1 // intstr.String
		switch {
		case port <= 0:
			out = fold(out, triUnknown)
		case named && namedPortPod == nil && !external:
			out = fold(out, triUnknown)
		case RuleMatchesPort(ports[i:i+1], port, proto, namedPortPod):
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
		desc := "addresses in " + entry.IPBlock.CIDR
		if len(entry.IPBlock.Except) > 0 {
			desc += " (except " + strings.Join(entry.IPBlock.Except, ", ") + ")"
		}
		if dir == DirectionEgress && !target.External {
			// Whether the policy sees the pod's address or, for traffic that
			// went through a Service, the cluster IP is up to the network
			// plugin; neither a hit nor a miss on the pod's address proves
			// anything.
			return triUnknown, desc + ": for a pod destination the network may check the Service address rather than the pod's, so Radar can't tell whether this range matches"
		}
		if !ipBlockWellFormed(entry.IPBlock) {
			return triUnknown, desc + ": the range or one of its exceptions is not a valid CIDR"
		}
		ips := peerIPs(target)
		switch ipBlockMatch(entry.IPBlock, ips) {
		case triYes:
			return triYes, desc
		case triNo:
			if dir == DirectionEgress {
				// An external destination's address is not rewritten by
				// anything in the cluster: a miss is a miss.
				return triNo, desc
			}
			return triUnknown, desc + ": the network may change the source address before this rule sees it, so Radar can't tell whether " + addrOrPeer(target, dir) + " falls in this range"
		default:
			if dir == DirectionEgress && len(ips) > 0 {
				// The destination is known and reached over the other address
				// family; a block of this family cannot admit it.
				return triNo, desc
			}
			return triUnknown, desc + ": no " + familyWord(entry.IPBlock.CIDR) + " address is known for " + peerDesc(target, dir)
		}
	}
	if entry.NamespaceSelector == nil && entry.PodSelector == nil {
		return triUnknown, "an empty from/to entry, which Radar can't interpret"
	}
	desc := selectorPeerDesc(entry, policyNs)
	if target.External {
		return triNo, desc
	}
	if target.Pod == nil {
		return triUnknown, desc + ": the pod at the other end can't be looked up (it may no longer exist)"
	}

	ns := triYes
	if entry.NamespaceSelector == nil {
		if target.Pod.Namespace != policyNs {
			ns = triNo
		}
	} else if selectorIsEmpty(entry.NamespaceSelector) {
		// Matches every namespace; no labels needed.
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
		return triUnknown, desc + ": can't check the labels of namespace " + target.Pod.Namespace + " — they are unavailable"
	}
	if pod == triUnknown {
		return triUnknown, desc + ": Radar could not interpret this selector"
	}
	return triYes, desc
}

func selectorPeerDesc(entry *networkingv1.NetworkPolicyPeer, policyNs string) string {
	pods := "any pod"
	if entry.PodSelector != nil && !selectorIsEmpty(entry.PodSelector) {
		pods = "pods " + selectorDesc(entry.PodSelector)
	}
	switch {
	case entry.NamespaceSelector == nil:
		return pods + " in namespace " + policyNs
	case selectorIsEmpty(entry.NamespaceSelector):
		return pods + " in any namespace"
	default:
		return pods + " in namespaces " + selectorDesc(entry.NamespaceSelector)
	}
}

func selectorDesc(sel *metav1.LabelSelector) string {
	if sel == nil {
		return "with any labels"
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
		return "with any labels"
	}
	return "labeled " + strings.Join(parts, ", ")
}

// flowWord and fromTo phrase a direction the way an operator reads a flow.
func flowWord(dir Direction) string {
	if dir == DirectionEgress {
		return "outgoing"
	}
	return "incoming"
}

func fromTo(dir Direction) string {
	if dir == DirectionEgress {
		return "to"
	}
	return "from"
}

// addrOrPeer is the address a range would be checked against, or the peer
// when none is known.
func addrOrPeer(p Peer, dir Direction) string {
	if p.IP != "" {
		return p.IP
	}
	if p.Pod != nil && p.Pod.Status.PodIP != "" {
		return p.Pod.Status.PodIP
	}
	return peerDesc(p, dir)
}

func familyWord(cidr string) string {
	if ip, _, err := net.ParseCIDR(cidr); err == nil && ip.To4() == nil {
		return "IPv6"
	}
	return "IPv4"
}

func peerDesc(p Peer, dir Direction) string {
	role := "the source"
	if dir == DirectionEgress {
		role = "the destination"
	}
	switch {
	case p.Pod != nil:
		return role + " pod " + podRef(p.Pod)
	case p.External && p.IP != "":
		return role + " " + p.IP + " (outside the cluster)"
	case p.External:
		return role + " (outside the cluster)"
	case p.Host && p.IP != "":
		return role + " " + p.IP + " (host network)"
	case p.Host:
		return role + " (host network)"
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
		return p + " (port unknown)"
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

func selectorIsEmpty(sel *metav1.LabelSelector) bool {
	return sel != nil && len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 0
}

func ipBlockWellFormed(block *networkingv1.IPBlock) bool {
	if _, _, err := net.ParseCIDR(block.CIDR); err != nil {
		return false
	}
	for _, ex := range block.Except {
		if _, _, err := net.ParseCIDR(ex); err != nil {
			return false
		}
	}
	return true
}
