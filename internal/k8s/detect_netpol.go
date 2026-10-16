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
	"github.com/skyhook-io/radar/pkg/topology"
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
	// A partly synced policy list can hold a deny-all without the policy that
	// allows the same pods.
	if cache == nil || cache.NetworkPolicies() == nil || cache.Services() == nil ||
		!cache.IsKindReady("networkpolicies") || !cache.IsKindReady("services") || !cache.IsKindReady("pods") {
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

	otherPolicies := newOtherPolicyView()
	var out []Detection
	for _, ns := range namespaces {
		if otherPolicies.mayApply(ns, podsByNamespace[ns], namespaceLabels(cache, ns)) {
			continue
		}
		out = append(out, denyAllIngressDetections(policiesByNamespace[ns], servicesByNamespace[ns], podsByNamespace[ns], now)...)
	}
	return out
}

// otherPolicyAPIs can allow traffic that the core NetworkPolicies alone would
// deny: Cilium, Antrea and AWS VPC CNI policies add allow rules to the same
// pods, and admin-tier cluster policies are evaluated ahead of them. Radar
// doesn't evaluate their rules, so a namespace where any of them may apply
// gets no deny-all verdict. Calico policies are matched per pod instead, in
// otherPolicyView.calicoMaySelect, because a cluster-wide Calico policy
// usually targets only some workloads.
var otherPolicyAPIs = []struct {
	kind, group string
	namespaced  bool
}{
	{"CiliumNetworkPolicy", "cilium.io", true},
	{"CiliumClusterwideNetworkPolicy", "cilium.io", false},
	{"NetworkPolicy", "crd.antrea.io", true},
	{"ClusterNetworkPolicy", "crd.antrea.io", false},
	{"ApplicationNetworkPolicy", "networking.k8s.aws", true},
	{"ClusterNetworkPolicy", "networking.k8s.aws", false},
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

// otherPolicyView answers, per namespace, whether a policy Radar doesn't
// evaluate may apply there. Anything it can't rule out counts as applying: an
// API that is served but can't be listed or isn't synced, a missing dynamic
// cache, or a selector Radar can't judge.
type otherPolicyView struct {
	discovery policyAPIDiscovery
	lister    policyObjectLister // nil when no dynamic cache exists

	clusterChecked bool
	clusterApplies bool
	globalCalico   []*unstructured.Unstructured
}

func newOtherPolicyView() *otherPolicyView {
	discovery := GetResourceDiscovery()
	if discovery == nil {
		return nil
	}
	view := &otherPolicyView{discovery: discovery}
	if dynamicCache := GetDynamicResourceCache(); dynamicCache != nil {
		view.lister = dynamicCache
	}
	return view
}

// list returns the objects, or known=false when the API is served but its
// contents can't be read completely.
func (v *otherPolicyView) list(kind, group, namespace string) (items []*unstructured.Unstructured, known bool) {
	gvr, served := v.discovery.GetGVRWithGroup(kind, group)
	if !served {
		return nil, true
	}
	if v.lister == nil {
		return nil, false
	}
	items, err := v.lister.List(gvr, namespace)
	if err != nil || !v.lister.IsSynced(gvr) {
		return nil, false
	}
	return items, true
}

// mayApply reports whether a policy outside core NetworkPolicy may select any
// of pods, which all live in namespace. namespaceLabels is nil when the
// Namespace object couldn't be read.
func (v *otherPolicyView) mayApply(namespace string, pods []*corev1.Pod, namespaceLabels map[string]string) bool {
	if v == nil {
		return true
	}
	if !v.clusterChecked {
		v.clusterChecked = true
		v.clusterApplies = v.clusterWidePolicyApplies()
	}
	if v.clusterApplies {
		return true
	}
	for _, api := range otherPolicyAPIs {
		if !api.namespaced {
			continue
		}
		items, known := v.list(api.kind, api.group, namespace)
		if !known || len(items) > 0 {
			return true
		}
	}
	for _, group := range topology.CalicoAPIGroups() {
		for _, def := range topology.CalicoPolicyKinds() {
			if !def.Namespaced || def.Staged {
				continue
			}
			items, known := v.list(def.Kind, group, namespace)
			if !known {
				return true
			}
			for _, policy := range items {
				if calicoMaySelect(policy, pods, namespaceLabels) {
					return true
				}
			}
		}
	}
	for _, policy := range v.globalCalico {
		if calicoMaySelect(policy, pods, namespaceLabels) {
			return true
		}
	}
	return false
}

// clusterWidePolicyApplies reports whether a cluster-scoped policy from
// otherPolicyAPIs may apply anywhere, and loads the cluster-wide Calico
// policies for per-namespace matching.
func (v *otherPolicyView) clusterWidePolicyApplies() bool {
	for _, api := range otherPolicyAPIs {
		if api.namespaced {
			continue
		}
		items, known := v.list(api.kind, api.group, "")
		if !known {
			return true
		}
		for _, item := range items {
			// A Baseline-tier cluster policy is evaluated after
			// NetworkPolicies and only for traffic they don't select, so it
			// can't allow what one denies.
			if tier, _, _ := unstructured.NestedString(item.Object, "spec", "tier"); tier != "Baseline" {
				return true
			}
		}
	}
	for _, group := range topology.CalicoAPIGroups() {
		for _, def := range topology.CalicoPolicyKinds() {
			if def.Namespaced || def.Staged {
				continue
			}
			items, known := v.list(def.Kind, group, "")
			if !known {
				return true
			}
			v.globalCalico = append(v.globalCalico, items...)
		}
	}
	return false
}

// calicoMaySelect reports whether a Calico policy that can allow inbound
// traffic selects any of the pods, or can't be ruled out. Radar doesn't read
// ServiceAccount labels here, and namespace labels may be unknown, so those
// selectors are dropped before matching: what remains can still exclude the
// pods, but a match only means the policy may apply.
func calicoMaySelect(policy *unstructured.Unstructured, pods []*corev1.Pod, namespaceLabels map[string]string) bool {
	if !calicoGovernsIngress(policy) {
		return false
	}
	if sel, _, _ := unstructured.NestedString(policy.Object, "spec", "serviceAccountSelector"); !calicoSelectsAll(sel) {
		policy = policy.DeepCopy()
		unstructured.RemoveNestedField(policy.Object, "spec", "serviceAccountSelector")
	}
	if sel, _, _ := unstructured.NestedString(policy.Object, "spec", "namespaceSelector"); namespaceLabels == nil && !calicoSelectsAll(sel) {
		policy = policy.DeepCopy()
		unstructured.RemoveNestedField(policy.Object, "spec", "namespaceSelector")
	}
	matcher := topology.CompileCalicoPolicyMatcher(policy)
	for _, pod := range pods {
		matched, valid := matcher.Matches(calicoPodLabels(pod), namespaceLabels, pod.Spec.ServiceAccountName, nil)
		if matched || !valid {
			return true
		}
	}
	return false
}

// calicoGovernsIngress applies Calico's types defaulting: without explicit
// types a policy covers Ingress unless it has egress rules and no ingress
// rules.
func calicoGovernsIngress(policy *unstructured.Unstructured) bool {
	types, found, err := unstructured.NestedStringSlice(policy.Object, "spec", "types")
	if err != nil {
		return true
	}
	if found && len(types) > 0 {
		for _, t := range types {
			if strings.EqualFold(t, "Ingress") {
				return true
			}
		}
		return false
	}
	ingress, _, _ := unstructured.NestedSlice(policy.Object, "spec", "ingress")
	egress, _, _ := unstructured.NestedSlice(policy.Object, "spec", "egress")
	return len(ingress) > 0 || len(egress) == 0
}

// calicoPodLabels adds the service-account label Calico sets on a pod's
// endpoint (for names short enough to be a label value) to the shared
// endpoint labels.
func calicoPodLabels(pod *corev1.Pod) map[string]string {
	out := topology.CalicoEndpointLabels(pod.Namespace, pod.Labels)
	sa := pod.Spec.ServiceAccountName
	if sa == "" {
		sa = "default"
	}
	if len(sa) < 63 {
		out["projectcalico.org/serviceaccount"] = sa
	}
	return out
}

func calicoSelectsAll(selector string) bool {
	trimmed := strings.ReplaceAll(strings.TrimSpace(selector), " ", "")
	return trimmed == "" || trimmed == "all()"
}

type denyAllIngressImpact struct {
	services []string
	pods     map[string]bool
	// alsoDenying names the other policies leaving the same pods without
	// inbound traffic; each gets its own issue, and each issue names the rest.
	alsoDenying map[string]bool
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
					impact = &denyAllIngressImpact{pods: map[string]bool{}, alsoDenying: map[string]bool{}, egressDenied: true}
					impacts[np] = impact
				}
				if n := len(impact.services); n == 0 || impact.services[n-1] != svc.Name {
					impact.services = append(impact.services, svc.Name)
				}
				impact.egressDenied = impact.egressDenied && egressDenied
				impact.pods[pod.Name] = true
				for _, other := range isolation.of(pod).ingressDenying {
					if other != np {
						impact.alsoDenying[other.Name] = true
					}
				}
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

	others := ""
	if len(impact.alsoDenying) > 0 {
		names := make([]string, 0, len(impact.alsoDenying))
		for name := range impact.alsoDenying {
			names = append(names, name)
		}
		sort.Strings(names)
		others = fmt.Sprintf(" NetworkPolicies %s also isolate some of these pods.", strings.Join(names, ", "))
		if len(names) == 1 {
			others = fmt.Sprintf(" NetworkPolicy %s also isolates some of these pods.", names[0])
		}
	}
	cause := fmt.Sprintf("NetworkPolicy %s isolates these pods for ingress and has no ingress rules, and no NetworkPolicy selecting them allows ingress, so new connections to them would be dropped.%s "+
		"Connections opened before the isolation existed can keep working until they close, so traffic can look healthy for a while. "+
		"Whether the policy is enforced depends on the cluster's network plugin.", np.Name, others)
	// A namespace-wide default-deny is usually intended; what's missing is an
	// allow policy for these pods, so deleting it is the wrong first advice.
	action := fmt.Sprintf("If this isolation is not intended, delete the policy or add an ingress rule for the expected clients. Inspect it with: kubectl describe networkpolicy %s -n %s", np.Name, np.Namespace)
	switch {
	case podSelectorDisplay(&np.Spec.PodSelector) == "all pods":
		action = fmt.Sprintf("This is a namespace-wide default-deny, so these pods need a NetworkPolicy that allows their expected clients. Inspect the policies with: kubectl get networkpolicy -n %s", np.Namespace)
	case len(impact.alsoDenying) > 0:
		action = fmt.Sprintf("Other policies isolate these pods too, so removing this one alone may not restore traffic; give the pods a NetworkPolicy that allows their expected clients, or remove every isolating policy that isn't intended. Inspect the policies with: kubectl get networkpolicy -n %s", np.Namespace)
	}

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

func namespaceLabels(cache *ResourceCache, namespace string) map[string]string {
	lister := cache.Namespaces()
	if lister == nil {
		return nil
	}
	ns, err := lister.Get(namespace)
	if err != nil {
		return nil
	}
	// Calico adds these to every namespace's selectable labels.
	out := make(map[string]string, len(ns.Labels)+2)
	for k, v := range ns.Labels {
		out[k] = v
	}
	out["kubernetes.io/metadata.name"] = ns.Name
	out["projectcalico.org/name"] = ns.Name
	return out
}
