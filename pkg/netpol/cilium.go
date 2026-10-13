package netpol

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

const ciliumNamespaceLabels = "io.cilium.k8s.namespace.labels."

// CiliumIdentity contains the observable Kubernetes part of an endpoint's
// identity. A missing Namespace is different from a known, unlabelled one.
// Cluster identity and agent-specific identity-label filters are not inferred.
type CiliumIdentity struct {
	set            labels.Set
	namespaceKnown bool
	hostNetwork    bool
}

func CiliumIdentityFor(pod *corev1.Pod, namespace *corev1.Namespace) CiliumIdentity {
	set := labels.Set{}
	for key, value := range pod.Labels {
		// Cilium sanitizes its own labels before adding authoritative identity.
		if !strings.HasPrefix(key, "io.cilium.k8s") {
			set[key] = value
		}
	}
	set["io.kubernetes.pod.namespace"] = pod.Namespace
	account := pod.Spec.ServiceAccountName
	if account == "" {
		account = "default"
	}
	set["io.cilium.k8s.policy.serviceaccount"] = account
	if namespace != nil {
		for key, value := range namespace.Labels {
			set[ciliumNamespaceLabels+key] = value
		}
	}
	return CiliumIdentity{set: set, namespaceKnown: namespace != nil, hostNetwork: pod.Spec.HostNetwork}
}

// CiliumSelectorMatcher preserves every AND requirement, including distinct
// source keys that normalize to the same Kubernetes key. Compile once, match
// many endpoints. Matches reports uncertainty separately from a definite miss.
type CiliumSelectorMatcher struct {
	known, namespace, nonPod labels.Selector
	unknown                  string
	invalid                  bool
	all                      bool
}

func CompileCiliumSelector(raw map[string]any) *CiliumSelectorMatcher {
	m := &CiliumSelectorMatcher{}
	var selector metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &selector); err != nil {
		m.invalid = true
		return m
	}
	var known, namespace, nonPod metav1.LabelSelector
	add := func(requirement metav1.LabelSelectorRequirement) {
		source, key, prefixed := strings.Cut(requirement.Key, ":")
		if !prefixed {
			key = requirement.Key
		}
		requirement.Key = key
		switch {
		case prefixed && source == "reserved" && ciliumNonPodIdentityKey(key):
			nonPod.MatchExpressions = append(nonPod.MatchExpressions, requirement)
		case prefixed && source != "k8s" && source != "any":
			m.unknown = "selects on a label source Radar does not model"
		case key == "io.cilium.k8s.policy.cluster":
			m.unknown = "selects on the cluster name, which Radar does not know"
		case strings.HasPrefix(key, ciliumNamespaceLabels):
			namespace.MatchExpressions = append(namespace.MatchExpressions, requirement)
		case ciliumFilteredIdentityKey(key):
			m.unknown = "selects on labels affected by Cilium's identity-label filter"
		default:
			known.MatchExpressions = append(known.MatchExpressions, requirement)
		}
	}
	for key, value := range selector.MatchLabels {
		add(metav1.LabelSelectorRequirement{Key: key, Operator: metav1.LabelSelectorOpIn, Values: []string{value}})
	}
	for _, requirement := range selector.MatchExpressions {
		add(requirement)
	}
	var err error
	m.known, err = metav1.LabelSelectorAsSelector(&known)
	if err != nil {
		m.invalid = true
	}
	m.namespace, err = metav1.LabelSelectorAsSelector(&namespace)
	if err != nil {
		m.invalid = true
	}
	m.nonPod, err = metav1.LabelSelectorAsSelector(&nonPod)
	if err != nil {
		m.invalid = true
	}
	m.all = len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 && !m.invalid
	return m
}

func (m *CiliumSelectorMatcher) Matches(id CiliumIdentity) (matches, decided bool, why string) {
	if m.invalid {
		return false, false, "has an endpoint selector Radar could not parse"
	}
	if !m.known.Matches(id.set) {
		return false, true, ""
	}
	if !m.nonPod.Empty() && id.hostNetwork {
		return false, false, "selects on a reserved identity for a host-network Pod"
	}
	if !m.nonPod.Matches(labels.Set{}) {
		return false, true, ""
	}
	if !m.namespace.Empty() {
		if id.namespaceKnown {
			if !m.namespace.Matches(id.set) {
				return false, true, ""
			}
		} else {
			return false, false, "selects on namespace labels Radar could not read here"
		}
	}
	if m.unknown != "" {
		return false, false, m.unknown
	}
	return true, true, ""
}

// These identities describe hosts, external endpoints or Cilium infrastructure,
// not ordinary Pod endpoints. Keep their sources separate from same-named Pod labels.
// Init and unmanaged identities require runtime evidence Kubernetes does not supply.
func ciliumNonPodIdentityKey(key string) bool {
	switch key {
	case "host", "world", "world-ipv4", "world-ipv6", "remote-node", "health", "ingress", "none":
		return true
	default:
		return false
	}
}

// These keys are excluded by Cilium's default identity-label filter. Its
// configuration can override that filter, so do not claim either a match or a
// miss from the Pod labels alone (especially for negative requirements).
func ciliumFilteredIdentityKey(key string) bool {
	if key == "io.kubernetes.pod.namespace" || strings.HasPrefix(key, "app.kubernetes.io") {
		return false
	}
	for _, prefix := range []string{"io.kubernetes", "kubernetes.io", "k8s.io", "topology.kubernetes.io", "annotation"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	if strings.Contains(key, "beta.kubernetes.io") {
		return true
	}
	for _, prefix := range []string{"statefulset.kubernetes.io/pod-name", "apps.kubernetes.io/pod-index", "batch.kubernetes.io/job-completion-index", "batch.kubernetes.io/controller-uid", "pod-template-generation", "pod-template-hash", "controller-revision-hash", "controller-uid", "etcd_node"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

type ciliumSpec struct {
	selector        *CiliumSelectorMatcher
	ingress, egress bool
}

// CiliumPolicyMatcher evaluates endpoint selection, not Cilium's rule effects
// or dataplane enforcement. A policy's spec and specs are alternatives.
type CiliumPolicyMatcher struct {
	specs   []ciliumSpec
	invalid bool
}

func CompileCiliumPolicy(obj *unstructured.Unstructured) *CiliumPolicyMatcher {
	m := &CiliumPolicyMatcher{}
	add := func(raw map[string]any) {
		sel, present, err := unstructured.NestedMap(raw, "endpointSelector")
		if err != nil {
			m.invalid = true
			return
		}
		if !present {
			return
		} // nodeSelector policies do not select pod endpoints.
		has := func(key string) bool { _, ok := raw[key]; return ok }
		ingress := has("ingress") || has("ingressDeny")
		egress := has("egress") || has("egressDeny")
		if !ingress && !egress {
			ingress, egress = true, true
		}
		m.specs = append(m.specs, ciliumSpec{selector: CompileCiliumSelector(sel), ingress: ingress, egress: egress})
	}
	if spec, present, err := unstructured.NestedMap(obj.Object, "spec"); err != nil {
		m.invalid = true
	} else if present {
		add(spec)
	}
	if specs, present, err := unstructured.NestedSlice(obj.Object, "specs"); err != nil {
		m.invalid = true
	} else if present {
		for _, value := range specs {
			if spec, ok := value.(map[string]any); ok {
				add(spec)
			} else {
				m.invalid = true
			}
		}
	}
	return m
}

// SelectsAll reports only an explicit, unconditional endpointSelector {}.
func (m *CiliumPolicyMatcher) SelectsAll() bool {
	for _, spec := range m.specs {
		if spec.selector.all {
			return true
		}
	}
	return false
}

// Selects accepts an empty direction for selection coverage in either
// direction. The flow evaluator supplies ingress or egress explicitly.
func (m *CiliumPolicyMatcher) Selects(id CiliumIdentity, direction Direction) (matches, decided bool, why string) {
	open := ""
	if m.invalid {
		open = "has a policy spec Radar could not parse"
	}
	for _, spec := range m.specs {
		if direction == DirectionIngress && !spec.ingress || direction == DirectionEgress && !spec.egress {
			continue
		}
		matches, decided, why := spec.selector.Matches(id)
		if matches && decided {
			return true, true, ""
		}
		if !decided && open == "" {
			open = why
		}
	}
	if open != "" {
		return false, false, open
	}
	return false, true, ""
}
