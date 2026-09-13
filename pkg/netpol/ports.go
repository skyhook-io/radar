// Package netpol evaluates core networking.k8s.io/v1 NetworkPolicy objects
// statically. It is pure: callers hand it the pods, namespaces and policies
// they have already read, and it never talks to a cluster.
//
// A policy is declared intent. Whether packets actually flow is decided by the
// CNI, which may not enforce NetworkPolicy at all (kindnet) or may add rules of
// its own (Cilium, Calico CRDs); callers must present a verdict here as what the
// policies say, never as ground truth about the wire.
package netpol

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

// PolicyTypes is the set of directions a policy isolates for the pods it
// selects.
type PolicyTypes struct {
	Ingress bool
	Egress  bool
}

// EffectivePolicyTypes applies Kubernetes' policyTypes defaulting exactly: when
// spec.policyTypes is set it is authoritative; when omitted, a policy always
// isolates Ingress and additionally isolates Egress iff it declares egress
// rules. Getting this wrong turns an egress-only policy into a false inbound
// restriction (or vice versa).
func EffectivePolicyTypes(np *networkingv1.NetworkPolicy) PolicyTypes {
	if len(np.Spec.PolicyTypes) > 0 {
		var t PolicyTypes
		for _, pt := range np.Spec.PolicyTypes {
			switch pt {
			case networkingv1.PolicyTypeIngress:
				t.Ingress = true
			case networkingv1.PolicyTypeEgress:
				t.Egress = true
			}
		}
		return t
	}
	return PolicyTypes{Ingress: true, Egress: len(np.Spec.Egress) > 0}
}

// RuleMatchesPort reports whether a rule's port set covers the target pod port
// + protocol. Empty ports = all ports (match). A named rule port resolves
// against the pod's declared container ports - and per Kubernetes
// NetworkPolicy semantics a named port the pod does NOT declare simply does not
// match (the rule is ignored for that pod), so it's a clean no-match, not an
// uncertainty. Treating it as "unsure" would let a policy whose only allow rule
// references an undeclared named port read as advisory/clean when real traffic
// to that port is in fact denied.
func RuleMatchesPort(rulePorts []networkingv1.NetworkPolicyPort, podPort int32, proto corev1.Protocol, pod *corev1.Pod) bool {
	if len(rulePorts) == 0 {
		return true
	}
	for i := range rulePorts {
		rp := &rulePorts[i]
		if ProtocolOrTCP(protoString(rp.Protocol)) != proto {
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
		n, ok := ContainerPortByName(pod, rp.Port.StrVal, proto)
		if !ok {
			continue
		}
		if n == podPort {
			return true
		}
	}
	return false
}

// ContainerPortByName resolves a named container port on a pod for the given
// protocol.
func ContainerPortByName(pod *corev1.Pod, name string, proto corev1.Protocol) (int32, bool) {
	if pod == nil {
		return 0, false
	}
	for _, c := range pod.Spec.Containers {
		for _, cp := range c.Ports {
			if cp.Name == name && ProtocolOrTCP(string(cp.Protocol)) == proto {
				return cp.ContainerPort, true
			}
		}
	}
	return 0, false
}

// ProtocolOrTCP normalizes a protocol string, defaulting to TCP as Kubernetes
// does wherever a protocol is omitted.
func ProtocolOrTCP(p string) corev1.Protocol {
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
