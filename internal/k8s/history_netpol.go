package k8s

import (
	"fmt"
	"sort"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/netpol"
)

// diffNetworkPolicy reports changes to which pods a policy selects, which
// directions it isolates, and its ingress/egress rules. Rules render as sorted
// one-line strings because Kubernetes ignores rule order. The summary describes
// this policy only: whether traffic is actually allowed depends on every
// policy selecting the same pods, so "ingress rules 2→0" never claims that
// inbound traffic is now denied.
func diffNetworkPolicy(oldObj, newObj any) ([]FieldChange, []string) {
	oldNP, ok1 := oldObj.(*networkingv1.NetworkPolicy)
	newNP, ok2 := newObj.(*networkingv1.NetworkPolicy)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	oldSel := podSelectorDisplay(&oldNP.Spec.PodSelector)
	newSel := podSelectorDisplay(&newNP.Spec.PodSelector)
	if oldSel != newSel {
		changes = append(changes, FieldChange{Path: "spec.podSelector", OldValue: oldSel, NewValue: newSel})
		summary = append(summary, fmt.Sprintf("podSelector: %s→%s", oldSel, newSel))
	}

	oldTypes := policyTypesDisplay(oldNP)
	newTypes := policyTypesDisplay(newNP)
	if !equalStringSlices(oldTypes, newTypes) {
		changes = append(changes, FieldChange{Path: "spec.policyTypes", OldValue: oldTypes, NewValue: newTypes})
		summary = append(summary, fmt.Sprintf("isolates: %s→%s", joinOrNone(oldTypes), joinOrNone(newTypes)))
	}

	oldIngress := ingressRuleRefs(oldNP.Spec.Ingress)
	newIngress := ingressRuleRefs(newNP.Spec.Ingress)
	if !equalStringSlices(oldIngress, newIngress) {
		changes = append(changes, FieldChange{Path: "spec.ingress", OldValue: oldIngress, NewValue: newIngress})
		summary = append(summary, ruleCountSummary("ingress", len(oldIngress), len(newIngress)))
	}

	oldEgress := egressRuleRefs(oldNP.Spec.Egress)
	newEgress := egressRuleRefs(newNP.Spec.Egress)
	if !equalStringSlices(oldEgress, newEgress) {
		changes = append(changes, FieldChange{Path: "spec.egress", OldValue: oldEgress, NewValue: newEgress})
		summary = append(summary, ruleCountSummary("egress", len(oldEgress), len(newEgress)))
	}

	return changes, summary
}

func ruleCountSummary(direction string, oldCount, newCount int) string {
	if oldCount == newCount {
		return direction + " rules changed"
	}
	return fmt.Sprintf("%s rules %d→%d", direction, oldCount, newCount)
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ",")
}

// policyTypesDisplay applies Kubernetes' policyTypes defaulting, so adding the
// first egress rule to a policy without explicit policyTypes shows up as the
// policy starting to isolate Egress.
func policyTypesDisplay(np *networkingv1.NetworkPolicy) []string {
	types := netpol.EffectivePolicyTypes(np)
	var out []string
	if types.Ingress {
		out = append(out, "Ingress")
	}
	if types.Egress {
		out = append(out, "Egress")
	}
	return out
}

func podSelectorDisplay(sel *metav1.LabelSelector) string {
	if sel == nil || len(sel.MatchLabels)+len(sel.MatchExpressions) == 0 {
		return "all pods"
	}
	return metav1.FormatLabelSelector(sel)
}

func namespaceSelectorDisplay(sel *metav1.LabelSelector) string {
	if len(sel.MatchLabels)+len(sel.MatchExpressions) == 0 {
		return "all namespaces"
	}
	return "namespaces " + metav1.FormatLabelSelector(sel)
}

func ingressRuleRefs(rules []networkingv1.NetworkPolicyIngressRule) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, "from "+peersDisplay(rule.From)+" ports "+policyPortsDisplay(rule.Ports))
	}
	sort.Strings(out)
	return out
}

func egressRuleRefs(rules []networkingv1.NetworkPolicyEgressRule) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, "to "+peersDisplay(rule.To)+" ports "+policyPortsDisplay(rule.Ports))
	}
	sort.Strings(out)
	return out
}

func peersDisplay(peers []networkingv1.NetworkPolicyPeer) string {
	if len(peers) == 0 {
		return "anywhere"
	}
	parts := make([]string, 0, len(peers))
	for _, peer := range peers {
		switch {
		case peer.IPBlock != nil:
			block := peer.IPBlock.CIDR
			if len(peer.IPBlock.Except) > 0 {
				except := append([]string(nil), peer.IPBlock.Except...)
				sort.Strings(except)
				block += " except " + strings.Join(except, ",")
			}
			parts = append(parts, block)
		case peer.PodSelector != nil && peer.NamespaceSelector != nil:
			parts = append(parts, "pods "+podSelectorDisplay(peer.PodSelector)+" in "+namespaceSelectorDisplay(peer.NamespaceSelector))
		case peer.NamespaceSelector != nil:
			parts = append(parts, namespaceSelectorDisplay(peer.NamespaceSelector))
		case peer.PodSelector != nil:
			parts = append(parts, "pods "+podSelectorDisplay(peer.PodSelector))
		}
	}
	sort.Strings(parts)
	return "[" + strings.Join(parts, "; ") + "]"
}

func policyPortsDisplay(ports []networkingv1.NetworkPolicyPort) string {
	if len(ports) == 0 {
		return "all"
	}
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		proto := "TCP"
		if p.Protocol != nil {
			proto = string(*p.Protocol)
		}
		port := "all"
		if p.Port != nil {
			port = p.Port.String()
			if p.EndPort != nil {
				port = fmt.Sprintf("%s-%d", port, *p.EndPort)
			}
		}
		parts = append(parts, proto+"/"+port)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
