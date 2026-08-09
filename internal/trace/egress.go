package trace

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Egress destination summarization for the egress-note finding. An egress-
// isolating NetworkPolicy doesn't change the INBOUND answer (the page's
// question), but a pro DevOps wants to see WHAT the pods are allowed to call
// out to without running `kubectl describe`. This turns the policies' egress
// rules into renderable chips: plain jargon-free text on the chip, full
// selector/port jargon in the tooltip. Pure / cluster-free so it stays
// unit-testable; the DNS check evaluates namespaceSelectors against the
// synthetic kubernetes.io/metadata.name label (auto-applied to every namespace
// since k8s 1.22) instead of a live namespace.

const kubeDNSNamespaceLabel = "kubernetes.io/metadata.name"

// tri is a deliberate three-valued logic for the DNS check: we only cry "DNS
// may be blocked" when we can prove no rule plausibly allows it. Anything we
// can't disprove stays silent (fail toward silence).
type tri int

const (
	triNo tri = iota
	triMaybe
	triYes
)

type dnsCoverage int

const (
	dnsUncovered dnsCoverage = iota // no rule plausibly allows DNS → gap fires
	dnsUncertain                    // a rule MIGHT allow DNS → stay silent
	dnsCovered                      // a rule definitely allows DNS → silent
)

// egressDest is one allowed destination flattened to a chip.
type egressDest struct {
	text  string
	title string
	tone  string // "accent" (anywhere) | "" (muted)
}

// egressSummary is the caller-independent egress picture for the endpoint pods:
// the additive union of every egress-isolating policy that selects them.
type egressSummary struct {
	dests      []egressDest
	denyAllOut bool // egress isolated with zero rules → all outbound blocked
	dnsCov     dnsCoverage
	isolated   bool
	// partialReplicas is set when the egress policies don't select EVERY endpoint
	// pod - the dest chips and DNS gap then describe only some replicas, so the
	// copy must say "some of this service's pods", not claim it for the whole
	// Service.
	partialReplicas bool
	// hostNetwork is set when at least one endpoint pod runs on the host network,
	// where NetworkPolicy egress is CNI-specific and likely doesn't govern the
	// pod - the note must not claim outbound is controlled for it.
	hostNetwork bool
}

// summarizeEgress builds the union picture from the egress-isolating policies
// that select the pods. Separate policies/rules are OR'd, exactly like the
// ingress aggregation.
func summarizeEgress(pols []*networkingv1.NetworkPolicy, pods []*corev1.Pod) egressSummary {
	s := egressSummary{isolated: len(pols) > 0}
	if !s.isolated {
		return s
	}
	s.hostNetwork = anyHostNetworkPod(pods)
	s.partialReplicas = !everyPodEgressSelected(pols, pods)

	var rules []*networkingv1.NetworkPolicyEgressRule
	for _, np := range pols {
		for i := range np.Spec.Egress {
			rules = append(rules, &np.Spec.Egress[i])
		}
	}
	if len(rules) == 0 {
		// No egress-isolating policy here carries any rule. That is an absolute
		// deny-all only when EVERY endpoint pod is actually selected by one of
		// them - with heterogeneous replicas an unselected pod can still call
		// out, so the blanket "can't open any outbound" claim would be false.
		if everyPodDenyAllEgress(pols, pods) {
			s.denyAllOut = true
			return s
		}
		// Some pods aren't covered: can't assert deny-all and can't assert a DNS
		// gap either - stay uncertain (silent) with the generic outbound note.
		s.dnsCov = dnsUncertain
		return s
	}

	s.dnsCov = classifyDNS(rules, podsNamespace(pods))

	// A single rule with no peers AND no ports allows everything - it subsumes
	// the rest, so collapse to one chip instead of a misleading destination list.
	// But only collapse to the blanket "anywhere" claim when an allow-all rule
	// applies to EVERY endpoint pod; with heterogeneous replicas (pod A allow-
	// all, pod B restricted) the whole-Service "anywhere" would over-state the
	// reach of the restricted pods. When it doesn't hold for every pod, fall
	// through to the conservative per-rule union below.
	if everyPodAllowAllEgress(pols, pods) {
		for _, rule := range rules {
			if len(rule.To) == 0 && len(rule.Ports) == 0 {
				s.dests = []egressDest{{
					text:  "anywhere",
					title: "An egress rule with no peers and no ports allows all destinations on all ports - egress is isolated but unrestricted.",
					tone:  "accent",
				}}
				return s
			}
		}
	}

	for _, rule := range rules {
		phrase := portsPhrase(rule.Ports)
		if len(rule.To) == 0 {
			s.dests = append(s.dests, egressDest{
				text:  "anywhere",
				title: "Egress rule allows all destinations " + phrase + ".",
				tone:  "accent",
			})
			continue
		}
		for j := range rule.To {
			s.dests = append(s.dests, peerLabel(&rule.To[j], phrase))
		}
	}
	s.dests = dedupeSortDests(s.dests)
	return s
}

// dnsGapFires is the gate for the "DNS may be blocked" observation: only when
// egress is isolated, isn't already a deny-all, and no rule plausibly allows
// cluster DNS. Corrected from the original spec: the silencer is an all-
// DESTINATION rule (empty `to`), not an all-PORT rule - CoreDNS lives in
// kube-system, so "all ports to a same-namespace pod" does NOT reach it.
func (s egressSummary) dnsGapFires() bool {
	// Don't generalize a DNS gap to pods the egress policies don't actually
	// restrict, or to host-network pods NetworkPolicy may not govern.
	if s.partialReplicas || s.hostNetwork {
		return false
	}
	return s.isolated && !s.denyAllOut && s.dnsCov == dnsUncovered
}

// ── destination labels ────────────────────────────────────────────────────

// peerLabel renders one egress peer in PLAIN language for a non-expert: the
// on-chip text never shows a raw k=v selector. We humanize only where it's
// provably safe - a namespace by its real name, or a workload by its app label
// ("the cache app"). Anything else collapses to a generic phrase; the exact
// selector + ports always live in the tooltip and the kubectl command.
func peerLabel(peer *networkingv1.NetworkPolicyPeer, phrase string) egressDest {
	if peer.IPBlock != nil {
		return ipBlockLabel(peer.IPBlock, phrase)
	}
	pod, ns := peer.PodSelector, peer.NamespaceSelector
	switch {
	case ns == nil && pod != nil:
		_, full, empty, _ := selectorParts(pod)
		if empty {
			return egressDest{text: "any pod in this namespace",
				title: "podSelector {} (same namespace), all pods, " + phrase + "."}
		}
		if app, ok := appName(pod); ok {
			return egressDest{text: "the " + app + " app",
				title: "podSelector " + full + ", same namespace, " + phrase + "."}
		}
		return egressDest{text: "specific pods here",
			title: "podSelector " + full + ", same namespace, " + phrase + "."}
	case ns != nil && pod == nil:
		_, full, empty, _ := selectorParts(ns)
		if empty {
			return egressDest{text: "any namespace",
				title: "namespaceSelector {} = every namespace (all pods), " + phrase + "."}
		}
		if name, ok := namespaceName(ns); ok {
			// The kubernetes.io/metadata.name label IS the namespace name.
			return egressDest{text: "pods in " + name,
				title: "namespaceSelector " + full + " = the " + name + " namespace, all pods, " + phrase + "."}
		}
		return egressDest{text: "pods in certain namespaces",
			title: "namespaceSelector " + full + ", all pods in matching namespaces, " + phrase + "."}
	case ns != nil && pod != nil:
		return intersectLabel(pod, ns, phrase)
	default:
		// Peer with neither selector nor ipBlock is invalid; treat as anywhere.
		return egressDest{text: "anywhere",
			title: "Egress rule peer with no selector " + phrase + ".", tone: "accent"}
	}
}

func intersectLabel(pod, ns *metav1.LabelSelector, phrase string) egressDest {
	_, pf, pe, _ := selectorParts(pod)
	_, nf, ne, _ := selectorParts(ns)
	podTok := "specific pods"
	if app, ok := appName(pod); ok {
		podTok = "the " + app + " app"
	} else if pe {
		podTok = "all pods"
	}
	nsTok := "certain namespaces"
	if name, ok := namespaceName(ns); ok {
		nsTok = "the " + name + " namespace"
	} else if ne {
		nsTok = "every namespace"
	}
	podJargon := pf
	if pe {
		podJargon = "{} (all)"
	}
	nsJargon := nf
	if ne {
		nsJargon = "{} (all)"
	}
	return egressDest{
		text:  podTok + " in " + nsTok,
		title: "INTERSECTION: pods matching " + podJargon + " within namespaces matching " + nsJargon + ", " + phrase + ". Both selectors in one peer = AND (not every namespace pod).",
	}
}

func ipBlockLabel(b *networkingv1.IPBlock, phrase string) egressDest {
	nExcept := len(b.Except)
	if isZeroCIDR(b.CIDR) {
		if nExcept == 0 {
			return egressDest{text: "any IP address",
				title: "ipBlock " + b.CIDR + " " + phrase + " - any destination IP.", tone: "accent"}
		}
		return egressDest{text: fmt.Sprintf("any IP except %d range%s", nExcept, plural(nExcept)),
			title: "ipBlock " + b.CIDR + " except [" + strings.Join(b.Except, ", ") + "] " + phrase + ".", tone: "accent"}
	}
	if nExcept == 0 {
		return egressDest{text: b.CIDR, title: "ipBlock " + b.CIDR + " " + phrase + "."}
	}
	return egressDest{text: fmt.Sprintf("%s (minus %d range%s)", b.CIDR, nExcept, plural(nExcept)),
		title: "ipBlock " + b.CIDR + " except [" + strings.Join(b.Except, ", ") + "] " + phrase + "."}
}

// ── ports (tooltip phrase only - kept off the chip face to stay calm) ───────

func portsPhrase(ports []networkingv1.NetworkPolicyPort) string {
	if len(ports) == 0 {
		return "on all ports"
	}
	toks := make([]string, 0, len(ports))
	for i := range ports {
		toks = append(toks, portToken(&ports[i]))
	}
	return "on " + strings.Join(toks, ", ")
}

func portToken(p *networkingv1.NetworkPolicyPort) string {
	proto := "TCP"
	if p.Protocol != nil && *p.Protocol != "" {
		proto = strings.ToUpper(string(*p.Protocol))
	}
	if p.Port == nil {
		return proto // protocol only = all ports of that protocol
	}
	if p.Port.Type == intstr.Int {
		if p.EndPort != nil && *p.EndPort > p.Port.IntVal {
			return fmt.Sprintf("%s %d-%d", proto, p.Port.IntVal, *p.EndPort)
		}
		return fmt.Sprintf("%s %d", proto, p.Port.IntVal)
	}
	return proto + " " + p.Port.StrVal
}

// ── DNS coverage ──────────────────────────────────────────────────────────

func classifyDNS(rules []*networkingv1.NetworkPolicyEgressRule, srcNamespace string) dnsCoverage {
	best := dnsUncovered
	for _, rule := range rules {
		pt := rulePort53(rule)
		if pt == triNo {
			continue
		}
		dt := ruleDestCoversDNS(rule, srcNamespace)
		if dt == triNo {
			continue
		}
		if pt == triYes && dt == triYes {
			return dnsCovered
		}
		best = dnsUncertain // plausible but unprovable → silent
	}
	return best
}

func rulePort53(rule *networkingv1.NetworkPolicyEgressRule) tri {
	if len(rule.Ports) == 0 {
		return triYes // all ports of ALL protocols include UDP 53
	}
	maybe := false
	for i := range rule.Ports {
		p := &rule.Ports[i]
		// Cluster DNS resolution is primarily UDP. A :53 port match only proves DNS
		// coverage when the rule permits UDP - a nil Protocol defaults to TCP per
		// NetworkPolicy semantics (NOT UDP), so a TCP-only (or unspecified) :53 rule
		// permits at most a TCP fallback, never primary UDP resolution. Skip non-UDP
		// port entries so a UDP-less egress policy can't suppress the DNS-gap advisory
		// (returning triMaybe here would route through classifyDNS to dnsUncertain,
		// which is SILENT - the honest result is to leave this rule as no-coverage so
		// the gap still fires).
		if !portProtocolIsUDP(p) {
			continue
		}
		if p.Port == nil {
			return triYes // all UDP ports of this rule include 53
		}
		if p.Port.Type == intstr.Int {
			if p.Port.IntVal == 53 || (p.EndPort != nil && p.Port.IntVal <= 53 && *p.EndPort >= 53) {
				return triYes // a numeric UDP port or range covering 53
			}
		}
		if p.Port.Type == intstr.String {
			maybe = true // named UDP port could resolve to 53
		}
	}
	if maybe {
		return triMaybe
	}
	return triNo
}

// portProtocolIsUDP reports whether a NetworkPolicyPort permits UDP. Cluster DNS
// is primarily UDP, so only a UDP port match counts as DNS coverage. A nil
// Protocol defaults to TCP per NetworkPolicy semantics (NOT UDP), so a rule that
// leaves Protocol unset permits only TCP on that port and does not cover UDP DNS.
func portProtocolIsUDP(p *networkingv1.NetworkPolicyPort) bool {
	return p.Protocol != nil && *p.Protocol == corev1.ProtocolUDP
}

func ruleDestCoversDNS(rule *networkingv1.NetworkPolicyEgressRule, srcNamespace string) tri {
	if len(rule.To) == 0 {
		return triYes // all destinations
	}
	maybe := false
	for i := range rule.To {
		peer := &rule.To[i]
		if peer.IPBlock != nil {
			if isZeroCIDR(peer.IPBlock.CIDR) {
				return triYes
			}
			maybe = true // a non-/0 CIDR may contain the CoreDNS pod IP
			continue
		}
		if peer.NamespaceSelector != nil {
			switch nsSelectorVsKubeSystem(peer.NamespaceSelector) {
			case triYes:
				if podSelectorAllowsCoreDNS(peer.PodSelector) {
					return triYes
				}
				// kube-system matches, but the podSelector isn't the well-known
				// k8s-app=kube-dns/coredns. The real CoreDNS labels aren't visible
				// from here, so an unrecognized selector is unprovable, not a
				// definite non-cover - stay uncertain (silent) rather than fire the
				// DNS gap.
				maybe = true
			case triMaybe:
				maybe = true
			}
			continue
		}
		// podSelector-only (no namespaceSelector) = same namespace as the source
		// pod. CoreDNS lives in kube-system, so a same-namespace pod normally does
		// NOT reach DNS - EXCEPT when the source workload is itself kube-system-
		// resident, where a peer matching the well-known CoreDNS labels
		// (k8s-app=kube-dns/coredns) DOES cover DNS. Endpoint pods carry their
		// namespace, so this is determinable, not uncertain.
		if peer.PodSelector != nil && srcNamespace == metav1.NamespaceSystem {
			if podSelectorAllowsCoreDNS(peer.PodSelector) {
				return triYes
			}
			// Same as the namespaceSelector branch above: the real CoreDNS labels
			// aren't visible here, so an unrecognized same-namespace podSelector is
			// unprovable, not a definite non-cover - stay uncertain (silent) rather
			// than fire the DNS gap.
			maybe = true
		}
	}
	if maybe {
		return triMaybe
	}
	return triNo
}

// podsNamespace returns the shared namespace of the endpoint pods, or "" when
// they span namespaces (or none are known) - the only cases where the source
// namespace is provable for DNS classification.
func podsNamespace(pods []*corev1.Pod) string {
	ns := ""
	for _, p := range nonNilPods(pods) {
		switch {
		case ns == "":
			ns = p.Namespace
		case p.Namespace != ns:
			return ""
		}
	}
	return ns
}

// nsSelectorVsKubeSystem evaluates a namespaceSelector against the synthetic
// kube-system label set. Definitive only when the selector keys solely on
// kubernetes.io/metadata.name; selectors using other labels we can't see stay
// uncertain.
func nsSelectorVsKubeSystem(sel *metav1.LabelSelector) tri {
	if sel == nil {
		return triNo
	}
	if len(sel.MatchLabels)+len(sel.MatchExpressions) == 0 {
		return triYes // {} matches every namespace
	}
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return triMaybe
	}
	if s.Matches(labels.Set{kubeDNSNamespaceLabel: "kube-system"}) {
		return triYes
	}
	if selectorOnlyKeys(sel, kubeDNSNamespaceLabel) {
		return triNo // keyed only on the name, and it isn't kube-system
	}
	return triMaybe
}

func podSelectorAllowsCoreDNS(sel *metav1.LabelSelector) bool {
	if sel == nil || len(sel.MatchLabels)+len(sel.MatchExpressions) == 0 {
		return true // all pods in the namespace include CoreDNS
	}
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return false
	}
	return s.Matches(labels.Set{"k8s-app": "kube-dns"}) || s.Matches(labels.Set{"k8s-app": "coredns"})
}

// everyPodDenyAllEgress reports whether every endpoint pod is selected by at
// least one egress-isolating policy AND none of the policies selecting a given
// pod carry an egress rule - the only condition under which "these pods can't
// open any outbound connection" is true for all of them. A pod selected by
// nothing, or by a policy with rules, breaks the blanket claim.
func everyPodDenyAllEgress(pols []*networkingv1.NetworkPolicy, pods []*corev1.Pod) bool {
	pods = nonNilPods(pods)
	if len(pods) == 0 {
		return false
	}
	for _, pod := range pods {
		if pod.Spec.HostNetwork {
			return false // hostNetwork bypasses pod-network egress semantics
		}
		selected := false
		for _, np := range pols {
			sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
			if err != nil || !sel.Matches(labels.Set(pod.Labels)) {
				continue
			}
			selected = true
			if len(np.Spec.Egress) > 0 {
				return false // a selecting policy allows some outbound for this pod
			}
		}
		if !selected {
			return false
		}
	}
	return true
}

// everyPodAllowAllEgress reports whether every endpoint pod is selected by at
// least one egress-isolating policy carrying a rule that allows ALL outbound
// (no peers and no ports). Only then is the blanket "anywhere" chip true for the
// whole Service; with heterogeneous replicas a pod that doesn't get such a rule
// breaks the claim, so the caller falls back to the conservative per-rule union.
func everyPodAllowAllEgress(pols []*networkingv1.NetworkPolicy, pods []*corev1.Pod) bool {
	pods = nonNilPods(pods)
	if len(pods) == 0 {
		return false
	}
	for _, pod := range pods {
		if pod.Spec.HostNetwork {
			return false // hostNetwork bypasses pod-network egress semantics
		}
		allowAll := false
		for _, np := range pols {
			sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
			if err != nil || !sel.Matches(labels.Set(pod.Labels)) {
				continue
			}
			for i := range np.Spec.Egress {
				rule := &np.Spec.Egress[i]
				if len(rule.To) == 0 && len(rule.Ports) == 0 {
					allowAll = true
					break
				}
			}
			if allowAll {
				break
			}
		}
		if !allowAll {
			return false
		}
	}
	return true
}

// everyPodEgressSelected reports whether every endpoint pod is selected by at
// least one of the egress-isolating policies. When false, the policies govern
// only some replicas, so the dest chips / DNS gap can't be claimed for the whole
// Service.
func everyPodEgressSelected(pols []*networkingv1.NetworkPolicy, pods []*corev1.Pod) bool {
	pods = nonNilPods(pods)
	if len(pods) == 0 {
		return false
	}
	for _, pod := range pods {
		selected := false
		for _, np := range pols {
			sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
			if err != nil || !sel.Matches(labels.Set(pod.Labels)) {
				continue
			}
			selected = true
			break
		}
		if !selected {
			return false
		}
	}
	return true
}

func anyHostNetworkPod(pods []*corev1.Pod) bool {
	for _, p := range nonNilPods(pods) {
		if p.Spec.HostNetwork {
			return true
		}
	}
	return false
}

// ── rendering helpers (chips + notes for the Finding) ──────────────────────

const egressChipCap = 3

// message is the one plain reassurance-led sentence shown as the finding's
// headline. It names the subject and leads with "doesn't change reachability"
// - the page's actual question - before the outbound mechanism.
func (s egressSummary) message() string {
	subject := "This service's pods"
	if s.partialReplicas {
		// The egress policies don't select every replica, so don't claim the rule
		// governs the whole Service.
		subject = "Some of this service's pods"
	}
	if s.denyAllOut {
		return subject + " can't open any outbound connection - including DNS name lookups. It doesn't change whether the service can be reached."
	}
	if s.hostNetwork {
		// NetworkPolicy egress is CNI-specific for host-network pods, so it likely
		// doesn't govern them - don't claim outbound is controlled.
		return subject + " also have a rule meant to control their outbound connections, but some run on the host network where NetworkPolicy may not apply. It doesn't change whether the service can be reached."
	}
	return subject + " also have a rule that controls their outbound connections. It doesn't change whether the service can be reached."
}

func (s egressSummary) chips() []FindingChip {
	// Deny-all is stated plainly in the message; no chip needed (there's
	// nothing to "reach").
	if s.denyAllOut {
		return nil
	}
	var accent, muted []FindingChip
	for _, d := range s.dests {
		c := FindingChip{Text: d.text, Title: d.title, Tone: d.tone}
		if d.tone == "accent" {
			accent = append(accent, c)
		} else {
			muted = append(muted, c)
		}
	}
	out := accent
	extra := 0
	if len(muted) > egressChipCap {
		extra = len(muted) - egressChipCap
		muted = muted[:egressChipCap]
	}
	out = append(out, muted...)
	if extra > 0 {
		out = append(out, FindingChip{
			Text:  fmt.Sprintf("+%d more", extra),
			Title: fmt.Sprintf("%d more allowed destination%s - see the full rules below.", extra, plural(extra)),
		})
	}
	return out
}

// chipsLabel is the tiny field label before the strip. Empty when the chips
// read on their own (deny-all, pure anywhere).
func (s egressSummary) chipsLabel() string {
	if s.denyAllOut {
		return ""
	}
	for _, d := range s.dests {
		if d.tone != "accent" {
			return "Can reach"
		}
	}
	return ""
}

func (s egressSummary) chipNotes() []string {
	var notes []string
	hasMuted := false
	for _, d := range s.dests {
		if d.tone != "accent" {
			hasMuted = true
			break
		}
	}
	if s.denyAllOut || hasMuted {
		notes = append(notes, "Radar reads this from the rule - it hasn't confirmed your cluster actually enforces it.")
	}
	if s.dnsGapFires() {
		notes = append(notes, "These pods may not be able to look up other services by name - no rule allows DNS (port 53). Could break their outbound calls if the rule is enforced.")
	}
	return notes
}

// ── small utilities ───────────────────────────────────────────────────────

// selectorParts returns the compact on-chip form (single-term selectors only),
// the full kubectl-style form for the tooltip, and whether it's empty/single.
func selectorParts(sel *metav1.LabelSelector) (compact, full string, empty, single bool) {
	if sel == nil {
		return "", "", true, false
	}
	terms := len(sel.MatchLabels) + len(sel.MatchExpressions)
	if terms == 0 {
		return "", "", true, false
	}
	full = metav1.FormatLabelSelector(sel)
	single = terms == 1
	if single {
		compact = full // FormatLabelSelector of one term is already compact (k=v / "key in (a,b)")
	}
	return compact, full, false, single
}

// namespaceName returns the literal namespace name when a namespaceSelector is
// a single match on kubernetes.io/metadata.name (the label that mirrors the
// namespace name) - so it can render as plain "kube-system namespace".
func namespaceName(sel *metav1.LabelSelector) (string, bool) {
	if sel == nil || len(sel.MatchExpressions) != 0 || len(sel.MatchLabels) != 1 {
		return "", false
	}
	if v, ok := sel.MatchLabels[kubeDNSNamespaceLabel]; ok {
		return v, true
	}
	return "", false
}

// appCommonKeys are the conventional "what app is this" labels. A single match
// on one of them lets us name a destination plainly ("the cache app") instead
// of printing a raw k=v selector a non-expert can't read.
var appCommonKeys = []string{"app", "app.kubernetes.io/name", "k8s-app"}

func appName(sel *metav1.LabelSelector) (string, bool) {
	if sel == nil || len(sel.MatchExpressions) != 0 || len(sel.MatchLabels) != 1 {
		return "", false
	}
	for _, k := range appCommonKeys {
		if v, ok := sel.MatchLabels[k]; ok {
			return v, true
		}
	}
	return "", false
}

func selectorOnlyKeys(sel *metav1.LabelSelector, key string) bool {
	for k := range sel.MatchLabels {
		if k != key {
			return false
		}
	}
	for _, e := range sel.MatchExpressions {
		if e.Key != key {
			return false
		}
	}
	return true
}

func isZeroCIDR(cidr string) bool {
	return strings.HasSuffix(strings.TrimSpace(cidr), "/0")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func dedupeSortDests(dests []egressDest) []egressDest {
	seen := make(map[string]struct{}, len(dests))
	out := dests[:0:0]
	for _, d := range dests {
		k := d.text + "\x00" + d.title
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].text < out[j].text })
	return out
}
