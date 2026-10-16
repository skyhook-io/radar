package k8s

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/netpol"
	"github.com/skyhook-io/radar/pkg/timeutil"
)

const (
	NetworkPolicyDenyAllIngressReason      = "NetworkPolicy denies all ingress"
	networkPolicyDenyAllIngressFingerprint = "netpol:deny-all-ingress"
	maxNamedServicesPerPolicy              = 3
)

// detectNetworkPolicyDenyAllIngress flags a NetworkPolicy that leaves every
// ready backend pod of a Service with no allowed inbound traffic at all: each
// pod is selected by at least one Ingress-isolating policy and none of the
// policies selecting it has an ingress rule. That is a namespace default-deny
// with no matching allow policy, or a targeted deny-all. Such a policy often
// produces no symptom at first, because the network plugin keeps established
// connections alive and only drops new ones, so the policy object is the only
// evidence.
//
// The subject is the policy, not the Service: it is the object to change, and
// its own change history then correlates with the issue.
//
// Deliberately not flagged, because each has common intended uses:
//   - pods no Service selects (deny-all ingress is normal for workers)
//   - egress-only isolation (normal hardening for leaf services)
//   - rules that allow some ports or sources but not others (diagnose's trace
//     evaluates those per port and caller)
//
// A policy is declared intent: whether it is enforced depends on the network
// plugin, so the copy says "would", and the severity stays warning. Policy
// APIs Radar doesn't evaluate can allow what core policies deny, so a
// namespace where one may apply is skipped.
func detectNetworkPolicyDenyAllIngress(cache *ResourceCache, namespace string, podsByNamespace map[string][]*corev1.Pod, now time.Time) []Detection {
	if cache == nil || cache.NetworkPolicies() == nil || cache.Services() == nil {
		return nil
	}
	var policies []*networkingv1.NetworkPolicy
	var services []*corev1.Service
	if namespace != "" {
		policies, _ = cache.NetworkPolicies().NetworkPolicies(namespace).List(labels.Everything())
		services, _ = cache.Services().Services(namespace).List(labels.Everything())
	} else {
		policies, _ = cache.NetworkPolicies().List(labels.Everything())
		services, _ = cache.Services().List(labels.Everything())
	}
	if len(policies) == 0 || len(services) == 0 {
		return nil
	}
	policiesByNamespace := map[string][]*networkingv1.NetworkPolicy{}
	for _, np := range policies {
		policiesByNamespace[np.Namespace] = append(policiesByNamespace[np.Namespace], np)
	}
	servicesByNamespace := map[string][]*corev1.Service{}
	for _, svc := range services {
		servicesByNamespace[svc.Namespace] = append(servicesByNamespace[svc.Namespace], svc)
	}

	namespaces := make([]string, 0, len(policiesByNamespace))
	for ns := range policiesByNamespace {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)

	otherPoliciesMayApply := otherPolicyAPIsLookup()
	var out []Detection
	for _, ns := range namespaces {
		if otherPoliciesMayApply(ns) {
			continue
		}
		out = append(out, denyAllIngressDetections(policiesByNamespace[ns], servicesByNamespace[ns], podsByNamespace[ns], now)...)
	}
	return out
}

// otherPolicyAPIs can allow traffic that the core NetworkPolicies alone would
// deny: Cilium and Antrea policies add allow rules to the same pods, and
// Calico policies and admin network policies are evaluated ahead of them.
// Radar evaluates none of them, so a namespace where any of them may apply
// gets no deny-all verdict. Calico's staged kinds are previews and enforce
// nothing.
var otherPolicyAPIs = []struct {
	kind, group string
	namespaced  bool
}{
	{"CiliumNetworkPolicy", "cilium.io", true},
	{"CiliumClusterwideNetworkPolicy", "cilium.io", false},
	{"NetworkPolicy", "projectcalico.org", true},
	{"GlobalNetworkPolicy", "projectcalico.org", false},
	{"NetworkPolicy", "crd.projectcalico.org", true},
	{"GlobalNetworkPolicy", "crd.projectcalico.org", false},
	{"NetworkPolicy", "crd.antrea.io", true},
	{"ClusterNetworkPolicy", "crd.antrea.io", false},
	{"AdminNetworkPolicy", "policy.networking.k8s.io", false},
	{"ClusterNetworkPolicy", "policy.networking.k8s.io", false},
}

type policyAPIDiscovery interface {
	GetGVRWithGroup(kind, group string) (schema.GroupVersionResource, bool)
}

type policyObjectLister interface {
	List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error)
	IsSynced(gvr schema.GroupVersionResource) bool
}

func otherPolicyAPIsLookup() func(namespace string) bool {
	discovery := GetResourceDiscovery()
	if discovery == nil {
		return func(string) bool { return true }
	}
	var lister policyObjectLister
	if dynamicCache := GetDynamicResourceCache(); dynamicCache != nil {
		lister = dynamicCache
	}
	return otherPoliciesMayApply(discovery, lister)
}

// otherPoliciesMayApply reports, per namespace, whether a policy from
// otherPolicyAPIs exists there (or cluster-wide) or can't be ruled out: an API
// that is served but can't be listed, or isn't synced yet, counts as present.
// lister may be nil when no dynamic cache exists.
func otherPoliciesMayApply(discovery policyAPIDiscovery, lister policyObjectLister) func(namespace string) bool {
	present := func(gvr schema.GroupVersionResource, namespace string) bool {
		if lister == nil {
			return true
		}
		items, err := lister.List(gvr, namespace)
		return err != nil || !lister.IsSynced(gvr) || len(items) > 0
	}
	var clusterWide *bool
	return func(namespace string) bool {
		if clusterWide == nil {
			found := false
			for _, api := range otherPolicyAPIs {
				if api.namespaced {
					continue
				}
				if gvr, ok := discovery.GetGVRWithGroup(api.kind, api.group); ok && present(gvr, "") {
					found = true
					break
				}
			}
			clusterWide = &found
		}
		if *clusterWide {
			return true
		}
		for _, api := range otherPolicyAPIs {
			if !api.namespaced {
				continue
			}
			if gvr, ok := discovery.GetGVRWithGroup(api.kind, api.group); ok && present(gvr, namespace) {
				return true
			}
		}
		return false
	}
}

type denyAllIngressImpact struct {
	services []string
	pods     map[string]bool
	// egressDenied is true only while every affected Service's evaluated pods
	// are also denied all outbound traffic.
	egressDenied bool
}

func denyAllIngressDetections(policies []*networkingv1.NetworkPolicy, services []*corev1.Service, pods []*corev1.Pod, now time.Time) []Detection {
	if len(policies) == 0 || len(services) == 0 || len(pods) == 0 {
		return nil
	}
	isolation, ok := newPodIsolation(policies)
	if !ok {
		return nil
	}
	impacts := map[*networkingv1.NetworkPolicy]*denyAllIngressImpact{}
	for _, svc := range services {
		if svc.Spec.Type == corev1.ServiceTypeExternalName || len(svc.Spec.Selector) == 0 {
			continue
		}
		var backends []*corev1.Pod
		for _, pod := range podsMatchingService(svc, pods) {
			if svc.Spec.PublishNotReadyAddresses || isPodReadyForProblem(pod) {
				backends = append(backends, pod)
			}
		}
		if len(backends) == 0 {
			continue
		}
		allDenied, egressDenied := true, true
		for _, pod := range backends {
			iso := isolation.of(pod)
			allDenied = allDenied && len(iso.ingressDenying) > 0
			egressDenied = egressDenied && iso.egressDenied
		}
		if !allDenied {
			continue
		}
		for _, pod := range backends {
			for _, np := range isolation.of(pod).ingressDenying {
				impact := impacts[np]
				if impact == nil {
					impact = &denyAllIngressImpact{pods: map[string]bool{}, egressDenied: true}
					impacts[np] = impact
				}
				if n := len(impact.services); n == 0 || impact.services[n-1] != svc.Name {
					impact.services = append(impact.services, svc.Name)
				}
				impact.egressDenied = impact.egressDenied && egressDenied
				impact.pods[pod.Name] = true
			}
		}
	}

	out := make([]Detection, 0, len(impacts))
	for np, impact := range impacts {
		out = append(out, denyAllIngressDetection(np, impact, now))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

type compiledPolicy struct {
	policy   *networkingv1.NetworkPolicy
	selector labels.Selector
	types    netpol.PolicyTypes
}

type podIsolationResult struct {
	// ingressDenying lists the Ingress-isolating policies selecting the pod
	// when none of them has an ingress rule; empty when any inbound traffic
	// may be allowed.
	ingressDenying []*networkingv1.NetworkPolicy
	egressDenied   bool
}

// podIsolation evaluates one namespace's policies per pod, compiling each
// selector once and memoizing per pod, since a pod can back several Services.
type podIsolation struct {
	policies []compiledPolicy
	// egressUndecidable is set when an Egress-only policy's selector can't be
	// parsed: the egress claim is then never made.
	egressUndecidable bool
	memo              map[*corev1.Pod]podIsolationResult
}

// newPodIsolation returns ok=false when an Ingress-isolating policy's selector
// can't be parsed: that policy might be the one allowing traffic, so no
// deny-all verdict can be built for the namespace.
func newPodIsolation(policies []*networkingv1.NetworkPolicy) (*podIsolation, bool) {
	iso := &podIsolation{memo: map[*corev1.Pod]podIsolationResult{}}
	for _, np := range policies {
		types := netpol.EffectivePolicyTypes(np)
		selector, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
		if err != nil {
			if types.Ingress {
				return nil, false
			}
			iso.egressUndecidable = true
			continue
		}
		iso.policies = append(iso.policies, compiledPolicy{policy: np, selector: selector, types: types})
	}
	return iso, true
}

func (iso *podIsolation) of(pod *corev1.Pod) podIsolationResult {
	if result, ok := iso.memo[pod]; ok {
		return result
	}
	var result podIsolationResult
	// hostNetwork pods sit outside pod-network policy semantics, which vary
	// by network plugin.
	if !pod.Spec.HostNetwork {
		podLabels := labels.Set(pod.Labels)
		ingressAllowed, egressSelected, egressAllowed := false, false, iso.egressUndecidable
		var denying []*networkingv1.NetworkPolicy
		for _, cp := range iso.policies {
			if cp.policy.Namespace != pod.Namespace || !cp.selector.Matches(podLabels) {
				continue
			}
			if cp.types.Ingress {
				if len(cp.policy.Spec.Ingress) > 0 {
					ingressAllowed = true
				}
				denying = append(denying, cp.policy)
			}
			if cp.types.Egress {
				egressSelected = true
				if len(cp.policy.Spec.Egress) > 0 {
					egressAllowed = true
				}
			}
		}
		if !ingressAllowed {
			result.ingressDenying = denying
		}
		result.egressDenied = egressSelected && !egressAllowed
	}
	iso.memo[pod] = result
	return result
}

func denyAllIngressDetection(np *networkingv1.NetworkPolicy, impact *denyAllIngressImpact, now time.Time) Detection {
	sort.Strings(impact.services)
	named := impact.services
	more := 0
	if len(named) > maxNamedServicesPerPolicy {
		more = len(named) - maxNamedServicesPerPolicy
		named = named[:maxNamedServicesPerPolicy]
	}
	target := "Service " + named[0]
	if len(named) > 1 {
		target = "Services " + strings.Join(named, ", ")
	}
	if more > 0 {
		target += fmt.Sprintf(" and %d more", more)
	}
	podWord := "pods"
	if len(impact.pods) == 1 {
		podWord = "pod"
	}
	message := fmt.Sprintf("Would block all inbound traffic to the %d %s behind %s: no NetworkPolicy selecting them allows any ingress", len(impact.pods), podWord, target)
	if impact.egressDenied {
		message += "; their outbound traffic, including DNS, would be blocked too"
	}

	cause := fmt.Sprintf("NetworkPolicy %s isolates these pods for ingress and has no ingress rules, and no other NetworkPolicy selecting them allows ingress, so new connections to them would be dropped. "+
		"Connections opened before the policy existed can keep working until they close, so traffic can look healthy for a while. "+
		"Whether the policy is enforced depends on the cluster's network plugin.", np.Name)
	action := fmt.Sprintf("If this isolation is not intended, delete the policy or add an ingress rule for the expected clients. Inspect it with: kubectl describe networkpolicy %s -n %s", np.Name, np.Namespace)

	age := now.Sub(np.CreationTimestamp.Time)
	return Detection{
		Kind:              "NetworkPolicy",
		Group:             "networking.k8s.io",
		Namespace:         np.Namespace,
		Name:              np.Name,
		Severity:          "warning",
		Reason:            NetworkPolicyDenyAllIngressReason,
		Message:           message,
		Cause:             cause,
		Action:            action,
		Fingerprint:       networkPolicyDenyAllIngressFingerprint,
		Age:               timeutil.FormatAgeShort(age),
		AgeSeconds:        int64(age.Seconds()),
		ResourceCreatedAt: np.CreationTimestamp.Time,
		// A rule removed after creation would make creation time a wrong
		// onset, and edits carry no timestamp of their own.
		OnsetUnknown:  true,
		RequiredReads: networkPolicyEvidenceReads(np.Namespace),
	}
}

// The finding rests on the namespace's policies, Services and pods; a caller
// who can't list any of them doesn't get it.
func networkPolicyEvidenceReads(namespace string) []issuesapi.EvidenceRead {
	return []issuesapi.EvidenceRead{
		{Group: "networking.k8s.io", Resource: "networkpolicies", Namespace: namespace, Verb: "list"},
		{Resource: "services", Namespace: namespace, Verb: "list"},
		{Resource: "pods", Namespace: namespace, Verb: "list"},
	}
}
