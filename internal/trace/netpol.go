package trace

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// policyVerdict is the caller-INDEPENDENT conclusion of static NetworkPolicy
// evaluation for one route's backend port against the endpoint pods. It is a
// PRIOR, never ground truth: the CNI is the only authority on whether a packet
// actually flows (kindnet creates NetworkPolicy objects but enforces nothing),
// so a wouldDeny is a strong warning the live in-cluster probe must confirm
// before any verdict goes red. Everything caller-dependent stays advisory by
// construction - Radar tests from its own vantage, not the real client pod, so
// it can never assert allow/deny for a rule scoped to specific sources.
type policyVerdict int

const (
	policyNotEvaluated policyVerdict = iota
	// policyNotRestricted: no Ingress-isolating core NetworkPolicy selects the
	// pods, OR one allows this port from anywhere (empty from). Caller-independent.
	// Scoped to CORE NetworkPolicy only - never an absolute "nothing blocks this".
	policyNotRestricted
	// policyWouldDeny: every eligible endpoint pod is caller-independently denied
	// this port (classic default-deny, or the port is in no ingress rule).
	policyWouldDeny
	// policyAdvisory: a rule allows the port only from specific sources, or pods
	// disagree, or a named port couldn't be resolved, or a hostNetwork pod is in
	// play. Radar can't assert - stays advisory.
	policyAdvisory
	// policyEgressNote: the only policies selecting the pods isolate Egress, not
	// Ingress - the inbound route is unrestricted, but the pod's OUTBOUND calls
	// may be governed.
	policyEgressNote
)

// policyResult is what evaluateNetpol returns for one Pods hop: the worst
// caller-independent verdict across the route's ports, plus the data the
// finding layer needs to write plain-language copy and a kubectl reproducer.
type policyResult struct {
	verdict     policyVerdict
	policyNames []string // core NPs that select at least one endpoint pod (sorted)
	port        string   // the Service port the verdict is about, ":80" form (operator-facing)
	// denyPort is the resolved POD/container port the deny applies to - what the
	// in-cluster data-path probe reports as its Port. The reconcile pass matches
	// on this so a multi-port backend can't downgrade a real block with another
	// port's success. 0 when it couldn't be resolved to a single number.
	denyPort int32
	// wrongPort is set when the deny is because the port is in no ingress rule
	// while OTHER ports are allowed - drives the "allows other ports but not X"
	// copy instead of a blanket default-deny message.
	wrongPort bool
	// proto is the port's protocol. The in-cluster probe can only confirm a
	// would-deny on TCP - UDP/SCTP probes are skipped - so the finding copy must
	// not promise a verification the tool can't perform.
	proto corev1.Protocol
	// egress carries the allowed-outbound picture for the egress-note verdict
	// (the destinations these pods may call out to). Nil for every other verdict.
	egress *egressSummary
	// advisory is the sub-reason a policyAdvisory verdict fired. An advisory can
	// mean four very different things; the finding copy branches on this instead
	// of asserting a single (often wrong) caller-dependent cause.
	advisory advisoryReason
}

// advisoryReason distinguishes why a static NetworkPolicy verdict is advisory
// rather than caller-independent, so the finding can give the right diagnosis.
type advisoryReason int

const (
	advisoryNone advisoryReason = iota
	advisoryCallerDependent
	advisoryHostNetwork
	advisoryUnresolvedPort
	advisoryMixed
)

// evaluateNetpol runs the static NetworkPolicy analysis for a Service's ready
// endpoint pods against the namespace's core NetworkPolicies. It resolves the
// route's Service ports to the POD port each policy actually matches (Service
// port != targetPort != containerPort), evaluates every eligible pod, and
// aggregates additively (a pod is allowed if ANY selecting policy allows it;
// the route "would deny" only when EVERY pod is caller-independently denied).
//
// Returns policyNotEvaluated when there's nothing to say (no policies, no pods,
// or every port resolves clean). The caller turns a non-trivial result into a
// Finding - this function stays pure and cluster-free so the full taxonomy is
// unit-testable.
func evaluateNetpol(pods []*corev1.Pod, ports []PortMap, policies []*networkingv1.NetworkPolicy) policyResult {
	pods = nonNilPods(pods)
	if len(pods) == 0 || len(ports) == 0 {
		return policyResult{verdict: policyNotEvaluated}
	}

	// Only Ingress-isolating policies that select at least one endpoint pod can
	// restrict the inbound route. Track egress-isolating selectors separately so
	// an egress-only policy becomes a note, not a false "not restricted".
	var ingressPolicies []*networkingv1.NetworkPolicy
	var egressPolicies []*networkingv1.NetworkPolicy
	// Track ingress- and egress-selecting names separately. An egress-only
	// policy cannot affect inbound traffic, so it must never appear in the
	// would-deny/advisory Cause as an inbound blocker.
	ingressNameSet := map[string]struct{}{}
	egressNameSet := map[string]struct{}{}
	for _, np := range policies {
		if np == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
		if err != nil {
			continue
		}
		if !selectsAnyPod(sel, pods) {
			continue
		}
		types := effectivePolicyTypes(np)
		if types.ingress {
			ingressPolicies = append(ingressPolicies, np)
			ingressNameSet[np.Name] = struct{}{}
		}
		if types.egress {
			egressPolicies = append(egressPolicies, np)
			egressNameSet[np.Name] = struct{}{}
		}
	}

	if len(ingressNameSet) == 0 && len(egressNameSet) == 0 {
		return policyResult{verdict: policyNotEvaluated}
	}
	ingressNames := sortedKeys(ingressNameSet)
	egressNames := sortedKeys(egressNameSet)

	// No ingress-isolating policy → inbound is unrestricted by core NP. If an
	// egress-only policy selects the pods, surface that as a faint note;
	// otherwise notRestricted.
	if len(ingressPolicies) == 0 {
		if len(egressPolicies) > 0 {
			summary := summarizeEgress(egressPolicies, pods)
			return policyResult{verdict: policyEgressNote, policyNames: egressNames, egress: &summary}
		}
		return policyResult{verdict: policyNotRestricted, policyNames: egressNames}
	}

	// Evaluate each route port; report the worst caller-independent verdict.
	// Severity order: wouldDeny > advisory > notRestricted. Inbound verdicts name
	// only ingress-isolating policies.
	worst := policyResult{verdict: policyNotRestricted, policyNames: ingressNames}
	for _, pm := range ports {
		pv := evaluatePort(pods, pm, ingressPolicies)
		pv.policyNames = ingressNames
		if policyRank(pv.verdict) > policyRank(worst.verdict) {
			worst = pv
		}
	}
	return worst
}

// evaluatePort aggregates the per-pod verdict for one Service port across all
// endpoint pods. The pod port the policies match is the resolved targetPort,
// which differs per pod when the Service uses a named targetPort.
func evaluatePort(pods []*corev1.Pod, pm PortMap, ingressPolicies []*networkingv1.NetworkPolicy) policyResult {
	proto := protocolOrTCP(pm.Protocol)
	label := fmt.Sprintf(":%d", pm.Port)

	anyDeny := false
	anyAllow := false           // allow-from-anywhere on at least one pod
	anyHostNetwork := false     // a hostNetwork pod (CNI-specific NP semantics)
	anyUnresolved := false      // a named targetPort that didn't resolve
	anyCallerDependent := false // a rule allows the port only from specific sources
	anyDeniedPort := false      // a deny that's a full default-deny (no other port allowed)
	denyPort := int32(0)        // the resolved pod port a deny applies to (for probe matching)
	denyPortVaries := false

	for _, pod := range pods {
		// hostNetwork pods bypass the pod-network NetworkPolicy semantics in
		// ways that are CNI-specific - never assert deny/allow for them.
		if pod.Spec.HostNetwork {
			anyHostNetwork = true
			continue
		}
		podPort, resolved := resolveTargetPort(pm, pod)
		if !resolved {
			// Named targetPort with no matching container port: can't say.
			anyUnresolved = true
			continue
		}
		pa := evaluatePodPort(pod, podPort, proto, ingressPolicies)
		switch pa.kind {
		case podAllowAnywhere:
			anyAllow = true
		case podCallerDependent:
			anyCallerDependent = true
		case podDenied:
			anyDeny = true
			if !pa.otherPortsAllowed {
				anyDeniedPort = true
			}
			if denyPort == 0 {
				denyPort = podPort
			} else if denyPort != podPort {
				denyPortVaries = true
			}
		}
	}
	if denyPortVaries {
		denyPort = 0 // can't pin a single port to filter the live probe on
	}

	switch {
	case anyHostNetwork || anyUnresolved || anyCallerDependent:
		// A caller-dependent, hostNetwork, or unresolvable pod taints the whole
		// route. Report the most specific reason so the copy can branch
		// (hostNetwork > unresolved-port > caller-dependent).
		reason := advisoryCallerDependent
		switch {
		case anyHostNetwork:
			reason = advisoryHostNetwork
		case anyUnresolved:
			reason = advisoryUnresolvedPort
		}
		return policyResult{verdict: policyAdvisory, port: label, advisory: reason}
	case anyDeny && anyAllow:
		// Mixed endpoint pods: some would flow, some wouldn't - can't condemn
		// the route, but it's not clean either.
		return policyResult{verdict: policyAdvisory, port: label, advisory: advisoryMixed}
	case anyDeny:
		// Every eligible pod denies this port. wrongPort when each deny is a
		// "port not in any rule" (other ports allowed), not a blanket deny.
		return policyResult{verdict: policyWouldDeny, port: label, denyPort: denyPort, wrongPort: !anyDeniedPort, proto: proto}
	default:
		return policyResult{verdict: policyNotRestricted, port: label}
	}
}

type podPortKind int

const (
	podDenied podPortKind = iota
	podAllowAnywhere
	podCallerDependent
)

type podPortVerdict struct {
	kind podPortKind
	// otherPortsAllowed: this pod denies the target port, but some rule allows a
	// DIFFERENT port - distinguishes a "wrong port" misconfig from a full
	// default-deny so the copy can be specific.
	otherPortsAllowed bool
}

// evaluatePodPort applies the additive NetworkPolicy semantics for one pod and
// one resolved pod port. A pod is allowed if ANY ingress-isolating policy that
// selects it allows the port; allow-from-anywhere (empty from) beats a
// caller-dependent allow because it's the only thing Radar can assert.
func evaluatePodPort(pod *corev1.Pod, podPort int32, proto corev1.Protocol, policies []*networkingv1.NetworkPolicy) podPortVerdict {
	callerDependent := false
	otherPortsAllowed := false
	selectedByAny := false

	for _, np := range policies {
		sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
		if err != nil || !sel.Matches(labels.Set(pod.Labels)) {
			continue
		}
		selectedByAny = true
		for i := range np.Spec.Ingress {
			rule := &np.Spec.Ingress[i]
			if !ruleMatchesPort(rule.Ports, podPort, proto, pod) {
				// Rule allows OTHER ports (it has explicit ports, none matched).
				if len(rule.Ports) > 0 {
					otherPortsAllowed = true
				}
				continue
			}
			// Rule matches the port. Empty from = allow from anywhere = the one
			// thing we can assert caller-independently.
			if len(rule.From) == 0 {
				return podPortVerdict{kind: podAllowAnywhere}
			}
			callerDependent = true
		}
	}

	switch {
	case !selectedByAny:
		// No ingress-isolating policy selects this pod → it is unrestricted.
		// NetworkPolicy is additive: a pod with no policy targeting it allows
		// all ingress. Returning denied here would falsely condemn a mixed
		// route where one pod is isolated and another is wide open.
		return podPortVerdict{kind: podAllowAnywhere}
	case callerDependent:
		return podPortVerdict{kind: podCallerDependent}
	default:
		return podPortVerdict{kind: podDenied, otherPortsAllowed: otherPortsAllowed}
	}
}

// ruleMatchesPort reports whether an ingress rule's port set covers the target
// pod port + protocol. Empty ports = all ports (match). A named rule port
// resolves against the pod's declared container ports - and per Kubernetes
// NetworkPolicy semantics a named port the pod does NOT declare simply does not
// match (the rule is ignored for that pod), so it's a clean no-match, not an
// uncertainty. Treating it as "unsure" would let a policy whose only allow rule
// references an undeclared named port read as advisory/clean when real traffic
// to that port is in fact denied.
func ruleMatchesPort(rulePorts []networkingv1.NetworkPolicyPort, podPort int32, proto corev1.Protocol, pod *corev1.Pod) bool {
	if len(rulePorts) == 0 {
		return true
	}
	for i := range rulePorts {
		rp := &rulePorts[i]
		if protocolOrTCP(protoString(rp.Protocol)) != proto {
			continue
		}
		if rp.Port == nil {
			// Protocol given, no port → all ports of that protocol.
			return true
		}
		if rp.Port.Type == 0 { // intstr.Int
			start := rp.Port.IntVal
			end := start
			if rp.EndPort != nil && *rp.EndPort >= start {
				end = *rp.EndPort
			}
			if podPort >= start && podPort <= end {
				return true
			}
			continue
		}
		// Named port - resolve against the pod's declared container ports. An
		// undeclared name is a no-match per k8s (the rule doesn't apply here).
		n, ok := containerPortByName(pod, rp.Port.StrVal, proto)
		if !ok {
			continue
		}
		if n == podPort {
			return true
		}
	}
	return false
}

type effectiveTypes struct {
	ingress bool
	egress  bool
}

// effectivePolicyTypes applies Kubernetes' policyTypes defaulting exactly: when
// spec.policyTypes is set it is authoritative; when omitted, a policy always
// isolates Ingress and additionally isolates Egress iff it declares egress
// rules. Getting this wrong turns an egress-only policy into a false inbound
// restriction (or vice versa).
func effectivePolicyTypes(np *networkingv1.NetworkPolicy) effectiveTypes {
	if len(np.Spec.PolicyTypes) > 0 {
		var t effectiveTypes
		for _, pt := range np.Spec.PolicyTypes {
			switch pt {
			case networkingv1.PolicyTypeIngress:
				t.ingress = true
			case networkingv1.PolicyTypeEgress:
				t.egress = true
			}
		}
		return t
	}
	return effectiveTypes{ingress: true, egress: len(np.Spec.Egress) > 0}
}

// resolvePortMap applies Kubernetes' targetPort rule - numeric used as-is, empty
// defaults to the Service port, named resolved by lookup - leaving the caller to
// say WHERE named ports are read from (a live Pod spec, or a hop's config
// snapshot). The rule lives here once so the two sources can't drift.
func resolvePortMap(pm PortMap, byName func(string) (int32, bool)) (int32, bool) {
	tp := strings.TrimSpace(pm.TargetPort)
	if tp == "" {
		return pm.Port, true
	}
	if n, err := strconv.ParseInt(tp, 10, 32); err == nil {
		return int32(n), true
	}
	return byName(tp)
}

// resolveTargetPort maps a Service port's targetPort to the numeric container
// port a NetworkPolicy actually matches on this pod.
func resolveTargetPort(pm PortMap, pod *corev1.Pod) (int32, bool) {
	return resolvePortMap(pm, func(name string) (int32, bool) {
		return containerPortByName(pod, name, protocolOrTCP(pm.Protocol))
	})
}

func containerPortByName(pod *corev1.Pod, name string, proto corev1.Protocol) (int32, bool) {
	for _, c := range pod.Spec.Containers {
		for _, cp := range c.Ports {
			if cp.Name == name && protocolOrTCP(string(cp.Protocol)) == proto {
				return cp.ContainerPort, true
			}
		}
	}
	return 0, false
}

func selectsAnyPod(sel labels.Selector, pods []*corev1.Pod) bool {
	for _, pod := range pods {
		if sel.Matches(labels.Set(pod.Labels)) {
			return true
		}
	}
	return false
}

// filterPortMapsByRef restricts a Service's port maps to those an upstream
// backendRef references (by numeric port or by Service-port name). With no refs
// (a bare Service entrypoint) it returns all ports. When refs WERE given but
// match nothing - an upstream pointing at a port the Service doesn't declare -
// it returns empty so the verdict is policyNotEvaluated (silence): falling back
// to all ports would let a deny on an unrelated, unreferenced port surface a
// would-deny on a path that never uses it (fail toward noise).
func filterPortMapsByRef(all []PortMap, refs []string) []PortMap {
	want := map[string]bool{}
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r != "" {
			want[r] = true
		}
	}
	if len(want) == 0 {
		return all
	}
	out := make([]PortMap, 0, len(all))
	for _, pm := range all {
		if want[pm.Name] || want[strconv.Itoa(int(pm.Port))] {
			out = append(out, pm)
		}
	}
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonNilPods(pods []*corev1.Pod) []*corev1.Pod {
	out := pods[:0:0]
	for _, p := range pods {
		if p != nil {
			out = append(out, p)
		}
	}
	return out
}

func protocolOrTCP(p string) corev1.Protocol {
	if p == "" {
		return corev1.ProtocolTCP
	}
	return corev1.Protocol(strings.ToUpper(p))
}

func protoString(p *corev1.Protocol) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func policyRank(v policyVerdict) int {
	switch v {
	case policyWouldDeny:
		return 4
	case policyAdvisory:
		return 3
	case policyEgressNote:
		return 2
	case policyNotRestricted:
		return 1
	default:
		return 0
	}
}
