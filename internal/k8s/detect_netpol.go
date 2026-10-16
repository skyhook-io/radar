package k8s

import (
	"context"
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

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/netpol"
	"github.com/skyhook-io/radar/pkg/timeutil"
	"github.com/skyhook-io/radar/pkg/topology"
)

const (
	NetworkPolicyDenyAllIngressReason      = "NetworkPolicy denies all ingress"
	NetworkPolicyDenyAllIngressFingerprint = "netpol:deny-all-ingress"
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
// APIs Radar doesn't evaluate can allow what core policies deny, so a Service
// whose backends one of them may select is skipped.
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
		nsObj := namespaceObject(cache, ns)
		env := denyAllEnv{
			otherMayApply: func(pods []*corev1.Pod, dir netpol.Direction) bool {
				return otherPolicies.mayApply(ns, pods, nsObj, dir)
			},
			ownerOf: func(pod *corev1.Pod) (string, string, string) { return podOwnerKindName(cache, pod) },
			isolationOnset: func(np *networkingv1.NetworkPolicy, impact *denyAllIngressImpact) (time.Time, bool) {
				return networkPolicyIsolationOnset(networkPolicyWatchedSince(cache), np, impact)
			},
		}
		out = append(out, denyAllIngressDetections(policiesByNamespace[ns], servicesByNamespace[ns], podsByNamespace[ns], env, now)...)
	}
	return out
}

// otherPolicyAPIs can allow traffic that the core NetworkPolicies alone would
// deny: Antrea and AWS VPC CNI policies add allow rules to the same pods, and
// admin-tier cluster policies are evaluated ahead of them. Radar doesn't
// evaluate their rules or selectors, so a namespace where any of them may
// apply gets no deny-all verdict. Cilium and Calico policies are matched per
// pod instead (ciliumMaySelect, calicoMaySelect), because a cluster-wide one
// usually targets only some workloads.
var otherPolicyAPIs = []struct {
	kind, group string
	namespaced  bool
}{
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
	globalCilium   []*unstructured.Unstructured

	// Per-scan memos: many Services share a namespace, and the dynamic cache
	// copies every object it lists.
	lists    map[string]listResult
	matchers map[calicoMatcherKey]*topology.CalicoPolicyMatcher
}

type listResult struct {
	items []*unstructured.Unstructured
	known bool
}

type calicoMatcherKey struct {
	policy          *unstructured.Unstructured
	namespaceLabels bool
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
	key := group + "/" + kind + "/" + namespace
	if r, ok := v.lists[key]; ok {
		return r.items, r.known
	}
	items, known = v.listUncached(kind, group, namespace)
	if v.lists == nil {
		v.lists = map[string]listResult{}
	}
	v.lists[key] = listResult{items: items, known: known}
	return items, known
}

func (v *otherPolicyView) listUncached(kind, group, namespace string) ([]*unstructured.Unstructured, bool) {
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

// mayApply reports whether a policy outside core NetworkPolicy may allow
// traffic in dir for any of pods (a Service's backends), which all live in
// namespace. ns is nil when the Namespace object couldn't be read.
func (v *otherPolicyView) mayApply(namespace string, pods []*corev1.Pod, ns *corev1.Namespace, dir netpol.Direction) bool {
	if v == nil {
		return true
	}
	namespaceLabels := calicoNamespaceLabels(ns)
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
				if v.calicoMaySelect(policy, pods, namespaceLabels, dir) {
					return true
				}
			}
		}
	}
	for _, policy := range v.globalCalico {
		if v.calicoMaySelect(policy, pods, namespaceLabels, dir) {
			return true
		}
	}
	items, known := v.list("CiliumNetworkPolicy", "cilium.io", namespace)
	if !known {
		return true
	}
	for _, policy := range append(append([]*unstructured.Unstructured(nil), items...), v.globalCilium...) {
		if ciliumMaySelect(policy, pods, ns, dir) {
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
	items, known := v.list("CiliumClusterwideNetworkPolicy", "cilium.io", "")
	if !known {
		return true
	}
	v.globalCilium = items
	return false
}

// ciliumMaySelect reports whether a Cilium policy has allow rules in dir for
// any of the pods, or can't be ruled out (a selector on a label Radar can't
// reproduce). Cilium adds its allow rules to those of core policies, so such
// a policy may allow what they deny.
func ciliumMaySelect(policy *unstructured.Unstructured, pods []*corev1.Pod, ns *corev1.Namespace, dir netpol.Direction) bool {
	for _, pod := range pods {
		selects, decided, _ := netpol.CiliumMayAllow(policy, netpol.CiliumIdentityFor(pod, ns), dir)
		if selects || !decided {
			return true
		}
	}
	return false
}

// calicoMaySelect reports whether a Calico policy that covers dir selects any
// of the pods, or can't be ruled out. Radar doesn't read ServiceAccount labels
// here, and namespace labels may be unknown, so those selectors are dropped
// before matching: what remains can still exclude the pods, but a match only
// means the policy may apply.
func (v *otherPolicyView) calicoMaySelect(policy *unstructured.Unstructured, pods []*corev1.Pod, namespaceLabels map[string]string, dir netpol.Direction) bool {
	if !calicoCovers(policy, dir) {
		return false
	}
	key := calicoMatcherKey{policy: policy, namespaceLabels: namespaceLabels != nil}
	matcher, ok := v.matchers[key]
	if !ok {
		matcher = compileCalicoMayMatcher(policy, namespaceLabels != nil)
		if v.matchers == nil {
			v.matchers = map[calicoMatcherKey]*topology.CalicoPolicyMatcher{}
		}
		v.matchers[key] = matcher
	}
	for _, pod := range pods {
		matched, valid := matcher.Matches(topology.CalicoEndpointLabels(pod.Namespace, pod.Labels, pod.Spec.ServiceAccountName), namespaceLabels, pod.Spec.ServiceAccountName, nil)
		if matched || !valid {
			return true
		}
	}
	return false
}

func compileCalicoMayMatcher(policy *unstructured.Unstructured, namespaceLabelsKnown bool) *topology.CalicoPolicyMatcher {
	if sel, _, _ := unstructured.NestedString(policy.Object, "spec", "serviceAccountSelector"); !calicoSelectsAll(sel) {
		policy = policy.DeepCopy()
		unstructured.RemoveNestedField(policy.Object, "spec", "serviceAccountSelector")
	}
	if sel, _, _ := unstructured.NestedString(policy.Object, "spec", "namespaceSelector"); !namespaceLabelsKnown && !calicoSelectsAll(sel) {
		policy = policy.DeepCopy()
		unstructured.RemoveNestedField(policy.Object, "spec", "namespaceSelector")
	}
	return topology.CompileCalicoPolicyMatcher(policy)
}

// calicoCovers applies Calico's types defaulting: without explicit types a
// policy covers Ingress unless it has egress rules and no ingress rules, and
// covers Egress when it has egress rules.
func calicoCovers(policy *unstructured.Unstructured, dir netpol.Direction) bool {
	want := "Ingress"
	if dir == netpol.DirectionEgress {
		want = "Egress"
	}
	types, found, err := unstructured.NestedStringSlice(policy.Object, "spec", "types")
	if err != nil {
		return true
	}
	if found && len(types) > 0 {
		for _, t := range types {
			if strings.EqualFold(t, want) {
				return true
			}
		}
		return false
	}
	ingress, _, _ := unstructured.NestedSlice(policy.Object, "spec", "ingress")
	egress, _, _ := unstructured.NestedSlice(policy.Object, "spec", "egress")
	if dir == netpol.DirectionEgress {
		return len(egress) > 0
	}
	return len(ingress) > 0 || len(egress) == 0
}

func calicoSelectsAll(selector string) bool {
	trimmed := strings.ReplaceAll(strings.TrimSpace(selector), " ", "")
	return trimmed == "" || trimmed == "all()"
}

type denyAllIngressImpact struct {
	services []string
	// lastCreated is the newest creation time among the affected Services and
	// pods: isolation can't be dated earlier than objects it applies to.
	lastCreated time.Time
	// selectors are the affected Services' pod selectors.
	selectors []labels.Selector
	pods      map[string]bool
	owners    map[issuesapi.Ref]bool
	// alsoDenying names the other policies leaving the same pods without
	// inbound traffic; each gets its own issue, and each issue names the rest.
	alsoDenying map[string]bool
	// egressDenied is true only while every affected Service's evaluated pods
	// are also denied all outbound traffic.
	egressDenied bool
}

// denyAllIngressDetections evaluates one namespace. A Service whose backends
// another policy engine may allow inbound traffic to is skipped, and the
// outbound note is dropped when one may allow outbound traffic.
// denyAllEnv carries what denyAllIngressDetections reads beyond the
// namespace's policies, Services and pods. Zero fields mean "nothing": no
// other policy engine applies, and no owners are resolved.
type denyAllEnv struct {
	// otherMayApply reports whether a policy Radar doesn't evaluate may allow
	// traffic in a direction for any of a Service's backends.
	otherMayApply func([]*corev1.Pod, netpol.Direction) bool
	ownerOf       func(*corev1.Pod) (group, kind, name string)
	// isolationOnset returns when the policy began isolating the impact's
	// pods, or false when that isn't established.
	isolationOnset func(*networkingv1.NetworkPolicy, *denyAllIngressImpact) (time.Time, bool)
}

func denyAllIngressDetections(policies []*networkingv1.NetworkPolicy, services []*corev1.Service, pods []*corev1.Pod, env denyAllEnv, now time.Time) []Detection {
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
		if !allDenied || (env.otherMayApply != nil && env.otherMayApply(backends, netpol.DirectionIngress)) {
			continue
		}
		if egressDenied && env.otherMayApply != nil && env.otherMayApply(backends, netpol.DirectionEgress) {
			egressDenied = false
		}
		for _, pod := range backends {
			for _, np := range isolation.of(pod).ingressDenying {
				impact := impacts[np]
				if impact == nil {
					impact = &denyAllIngressImpact{pods: map[string]bool{}, owners: map[issuesapi.Ref]bool{}, alsoDenying: map[string]bool{}, egressDenied: true}
					impacts[np] = impact
				}
				if n := len(impact.services); n == 0 || impact.services[n-1] != svc.Name {
					impact.services = append(impact.services, svc.Name)
					impact.selectors = append(impact.selectors, labels.SelectorFromSet(svc.Spec.Selector))
					if created := svc.CreationTimestamp.Time; created.After(impact.lastCreated) {
						impact.lastCreated = created
					}
				}
				impact.egressDenied = impact.egressDenied && egressDenied
				impact.pods[pod.Name] = true
				if created := pod.CreationTimestamp.Time; created.After(impact.lastCreated) {
					impact.lastCreated = created
				}
				if env.ownerOf != nil {
					if group, kind, name := env.ownerOf(pod); kind != "" {
						impact.owners[issuesapi.Ref{Group: group, Kind: kind, Namespace: pod.Namespace, Name: name}] = true
					}
				}
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
		d := denyAllIngressDetection(np, impact, now)
		if env.isolationOnset != nil {
			if since, ok := env.isolationOnset(np, impact); ok {
				setDetectionOnset(&d, now, since)
				if !d.OnsetUnknown {
					d.IssueTiming, d.IssueTimingBasis = "started_at_resource_creation", "spec"
				}
			}
		}
		out = append(out, d)
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
	outbound := ""
	if impact.egressDenied {
		outbound = " Their outbound traffic, including DNS, would be blocked too."
	}
	cause := fmt.Sprintf("NetworkPolicy %s leaves the %d %s behind %s with no allowed inbound traffic: it isolates them for ingress and has no ingress rules, and no NetworkPolicy selecting them allows ingress.%s%s "+
		"New connections to them would be dropped, while connections opened before the isolation existed can keep working until they close, so traffic can look healthy for a while. "+
		"Whether the policy is enforced depends on the cluster's network plugin.", np.Name, len(impact.pods), podWord, target, outbound, others)
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
		MessageSource:     issuesapi.MessageSourceRadar,
		Cause:             cause,
		Action:            action,
		Fingerprint:       NetworkPolicyDenyAllIngressFingerprint,
		Age:               timeutil.FormatAgeShort(age),
		AgeSeconds:        int64(age.Seconds()),
		ResourceCreatedAt: np.CreationTimestamp.Time,
		OnsetUnknown:      true,
		RequiredReads:     networkPolicyEvidenceReads(np.Namespace),
		AffectedRefs:      affectedRefs(np.Namespace, impact),
	}
}

// onsetObservationMargin absorbs clock skew between the API server, which
// stamps creation times, and this process, which stamps observations.
const onsetObservationMargin = time.Minute

// networkPolicyIsolationOnset dates the isolation from the policy's creation,
// and only when it's established that creating it started the isolation:
//   - its spec was never changed (generation 1), so creation set the rules;
//   - every affected Service and pod already existed then;
//   - this process watched from before then and kept every event since
//     (watchedSince is set), and none show another NetworkPolicy in the
//     namespace, an affected Service, or a pod its selector matches, created,
//     changed or deleted at or after it (deleting an allow policy, changing a
//     selector, or losing a backend the policy didn't isolate would start the
//     isolation later).
//
// A pod relabeled into a Service's selector afterwards is matched by its new
// labels only, so a relabel isn't caught; that is the remaining way to
// backdate.
// Anything less leaves the onset unknown: a backdated onset would mislead.
func networkPolicyIsolationOnset(watchedSince time.Time, np *networkingv1.NetworkPolicy, impact *denyAllIngressImpact) (time.Time, bool) {
	created := np.CreationTimestamp.Time
	if created.IsZero() || np.Generation > 1 || impact.lastCreated.After(created) ||
		watchedSince.IsZero() || watchedSince.After(created.Add(-onsetObservationMargin)) {
		return time.Time{}, false
	}
	events, ok := isolationInputEventsSince(np.Namespace, impact.services, created)
	if !ok {
		return time.Time{}, false
	}
	for _, e := range events {
		if e.Kind == "NetworkPolicy" && e.Name == np.Name {
			continue
		}
		if e.Kind == "Pod" && !matchesAny(impact.selectors, e.Labels) {
			continue
		}
		// An equal timestamp can't establish order, so it counts as after.
		if !e.Timestamp.Before(created) {
			return time.Time{}, false
		}
	}
	return created, true
}

// networkPolicyWatchedSince returns the start of the window in which every
// NetworkPolicy change was recorded and is still retained: the later of when
// this process began recording and when its NetworkPolicy informer synced.
// Zero when no such window is known, including once the store has evicted
// anything (ring overflow or retention cleanup).
func networkPolicyWatchedSince(cache *ResourceCache) time.Time {
	start := timeline.ObservationStart()
	if start.IsZero() || cache == nil {
		return time.Time{}
	}
	synced := time.Time{}
	for _, s := range cache.GetSyncStatus().Informers {
		if s.Key == "networkpolicies" && s.Synced {
			synced, _ = time.Parse(time.RFC3339, s.SyncedAt)
		}
	}
	if synced.IsZero() {
		return time.Time{}
	}
	if synced.After(start) {
		start = synced
	}
	store := timeline.GetStore()
	if store == nil {
		return time.Time{}
	}
	// A store that evicted anything may have dropped part of the window: the
	// ring drops by arrival, so the oldest timestamp it keeps doesn't bound it.
	if store.Stats().EventsEvicted {
		return time.Time{}
	}
	return start
}

// isolationInputEventsSince returns the namespace's NetworkPolicy and Pod
// events and the named Services' events since t, and false when a query
// fails or hits its cap.
func isolationInputEventsSince(namespace string, services []string, t time.Time) ([]timeline.TimelineEvent, bool) {
	const limit = 500
	query := func(opts timeline.QueryOptions) ([]timeline.TimelineEvent, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		opts.Namespaces, opts.Since, opts.Limit = []string{namespace}, t, limit
		opts.Sources, opts.ClusterContext = []timeline.EventSource{timeline.SourceInformer}, ActiveClusterContext()
		events, err := timeline.QueryEvents(ctx, opts)
		return events, err == nil && len(events) < limit
	}
	policies, ok := query(timeline.QueryOptions{Kinds: []string{"NetworkPolicy"}, APIGroups: []string{"networking.k8s.io"}})
	if !ok {
		return nil, false
	}
	svcs, ok := query(timeline.QueryOptions{Kinds: []string{"Service"}, APIGroups: []string{""}, Names: services})
	if !ok {
		return nil, false
	}
	pods, ok := query(timeline.QueryOptions{Kinds: []string{"Pod"}, APIGroups: []string{""}, IncludeManaged: true})
	if !ok {
		return nil, false
	}
	return append(append(policies, svcs...), pods...), true
}

func matchesAny(selectors []labels.Selector, podLabels map[string]string) bool {
	for _, sel := range selectors {
		if sel.Matches(labels.Set(podLabels)) {
			return true
		}
	}
	return false
}

// affectedRefs lists the isolated Services, then their pods' owning workloads,
// then the pods, so a cap keeps the most useful links.
func affectedRefs(namespace string, impact *denyAllIngressImpact) []issuesapi.Ref {
	refs := make([]issuesapi.Ref, 0, len(impact.services)+len(impact.owners)+len(impact.pods))
	for _, name := range impact.services {
		refs = append(refs, issuesapi.Ref{Kind: "Service", Namespace: namespace, Name: name})
	}
	owners := make([]issuesapi.Ref, 0, len(impact.owners))
	for ref := range impact.owners {
		owners = append(owners, ref)
	}
	sort.Slice(owners, func(i, j int) bool {
		if owners[i].Kind != owners[j].Kind {
			return owners[i].Kind < owners[j].Kind
		}
		return owners[i].Name < owners[j].Name
	})
	refs = append(refs, owners...)
	pods := make([]string, 0, len(impact.pods))
	for name := range impact.pods {
		pods = append(pods, name)
	}
	sort.Strings(pods)
	for _, name := range pods {
		refs = append(refs, issuesapi.Ref{Kind: "Pod", Namespace: namespace, Name: name})
	}
	return refs
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

func namespaceObject(cache *ResourceCache, namespace string) *corev1.Namespace {
	lister := cache.Namespaces()
	if lister == nil {
		return nil
	}
	ns, err := lister.Get(namespace)
	if err != nil {
		return nil
	}
	return ns
}

// calicoNamespaceLabels returns the labels a Calico namespaceSelector matches,
// or nil when the Namespace couldn't be read.
func calicoNamespaceLabels(ns *corev1.Namespace) map[string]string {
	if ns == nil {
		return nil
	}
	out := make(map[string]string, len(ns.Labels)+2)
	for k, v := range ns.Labels {
		out[k] = v
	}
	out["kubernetes.io/metadata.name"] = ns.Name
	out["projectcalico.org/name"] = ns.Name
	return out
}
