package netpol

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// CiliumIdentity is the label set Cilium matches an endpointSelector against:
// the pod's labels plus the identity labels Cilium derives for it. Namespace
// labels are included only when the Namespace was readable; a selector on
// them is otherwise undecidable, which known records.
type CiliumIdentity struct {
	set     labels.Set
	nsKnown bool
}

// CiliumIdentityFor builds the pod's identity; ns may be nil when the
// Namespace isn't readable.
func CiliumIdentityFor(pod *corev1.Pod, ns *corev1.Namespace) CiliumIdentity {
	set := labels.Set{}
	for k, v := range pod.Labels {
		set[k] = v
	}
	set["io.kubernetes.pod.namespace"] = pod.Namespace
	sa := pod.Spec.ServiceAccountName
	if sa == "" {
		sa = "default"
	}
	set["io.cilium.k8s.policy.serviceaccount"] = sa
	id := CiliumIdentity{set: set, nsKnown: ns != nil}
	if ns != nil {
		for k, v := range ns.Labels {
			set["io.cilium.k8s.namespace.labels."+k] = v
		}
	}
	return id
}

// CiliumGoverns reports whether a Cilium policy selects the pod with a rule
// for dir. A policy may carry one spec or a list of specs; any one of them
// that selects the pod and has a section for the direction (allow or deny)
// means the policy governs it. A spec without any rule sections is read
// conservatively as governing both directions. decided is false when the
// selector cannot be judged from what Radar knows.
//
// The caller scopes the policies: a CiliumNetworkPolicy applies only to pods
// in its own namespace, which this function does not check.
func CiliumGoverns(obj *unstructured.Unstructured, id CiliumIdentity, dir Direction) (selects, decided bool, why string) {
	return ciliumSelects(obj, id, func(spec map[string]any) bool { return ciliumSpecCovers(spec, dir) })
}

// CiliumMayAllow is CiliumGoverns narrowed to specs that can allow traffic in
// dir: a deny-only spec can't, so it doesn't count. Same caller scoping.
func CiliumMayAllow(obj *unstructured.Unstructured, id CiliumIdentity, dir Direction) (selects, decided bool, why string) {
	return ciliumSelects(obj, id, func(spec map[string]any) bool { return ciliumSpecMayAllow(spec, dir) })
}

func ciliumSelects(obj *unstructured.Unstructured, id CiliumIdentity, covers func(map[string]any) bool) (selects, decided bool, why string) {
	var specs []map[string]any
	if spec, ok, _ := unstructured.NestedMap(obj.Object, "spec"); ok {
		specs = append(specs, spec)
	}
	if list, ok, _ := unstructured.NestedSlice(obj.Object, "specs"); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				specs = append(specs, m)
			}
		}
	}
	openWhy := ""
	for _, spec := range specs {
		selMap, ok, _ := unstructured.NestedMap(spec, "endpointSelector")
		if !ok {
			// A spec without an endpointSelector selects nodes, not pods.
			continue
		}
		if !covers(spec) {
			continue
		}
		matches, decided, why := ciliumSelectorMatches(selMap, id)
		switch {
		case decided && matches:
			// One selecting spec is enough, whatever the others left open.
			return true, true, ""
		case !decided && openWhy == "":
			openWhy = why
		}
	}
	if openWhy != "" {
		return false, false, openWhy
	}
	return false, true, ""
}

func ciliumSpecCovers(spec map[string]any, dir Direction) bool {
	has := func(key string) bool { _, ok := spec[key]; return ok }
	if !has("ingress") && !has("ingressDeny") && !has("egress") && !has("egressDeny") {
		return true
	}
	if dir == DirectionIngress {
		return has("ingress") || has("ingressDeny")
	}
	return has("egress") || has("egressDeny")
}

// ciliumSpecMayAllow reports whether a spec has allow rules for dir. An empty
// section or an empty rule (ingress: [{}]) is Cilium's deny-all form and
// allows nothing. A spec with no rule sections at all is read as possibly
// allowing.
func ciliumSpecMayAllow(spec map[string]any, dir Direction) bool {
	has := func(key string) bool { _, ok := spec[key]; return ok }
	if !has("ingress") && !has("ingressDeny") && !has("egress") && !has("egressDeny") {
		return true
	}
	key := "egress"
	if dir == DirectionIngress {
		key = "ingress"
	}
	rules, ok := spec[key].([]any)
	if !ok {
		return has(key)
	}
	for _, rule := range rules {
		if m, ok := rule.(map[string]any); !ok || len(m) > 0 {
			return true
		}
	}
	return false
}

// ciliumSelectorMatches evaluates an endpointSelector against the identity.
// Cilium label keys carry a source prefix ("k8s:", "any:"). Requirements are
// ANDed, so the ones Radar can answer are checked first: a definite miss on
// any of them means the policy does not select the pod, whatever the rest
// say. Only when every answerable requirement matches does a key Radar
// cannot reproduce — the cluster name, namespace labels when the Namespace
// was not readable — make the selector undecidable.
func ciliumSelectorMatches(selMap map[string]any, id CiliumIdentity) (matches, decided bool, why string) {
	var sel metav1.LabelSelector
	openWhy := ""
	if ml, ok, _ := unstructured.NestedMap(selMap, "matchLabels"); ok {
		sel.MatchLabels = map[string]string{}
		for k, v := range ml {
			s, ok := v.(string)
			if !ok {
				return false, false, "has a matchLabels value Radar could not read"
			}
			key, ok, why := ciliumSelectorKey(k, id)
			if !ok {
				if openWhy == "" {
					openWhy = why
				}
				continue
			}
			sel.MatchLabels[key] = s
		}
	}
	if exprs, ok, _ := unstructured.NestedSlice(selMap, "matchExpressions"); ok {
		for _, raw := range exprs {
			e, ok := raw.(map[string]any)
			if !ok {
				return false, false, "has a matchExpressions entry Radar could not read"
			}
			key, ok, why := ciliumSelectorKey(fmt.Sprint(e["key"]), id)
			if !ok {
				if openWhy == "" {
					openWhy = why
				}
				continue
			}
			req := metav1.LabelSelectorRequirement{Key: key, Operator: metav1.LabelSelectorOperator(fmt.Sprint(e["operator"]))}
			if vals, ok := e["values"].([]any); ok {
				for _, v := range vals {
					req.Values = append(req.Values, fmt.Sprint(v))
				}
			}
			sel.MatchExpressions = append(sel.MatchExpressions, req)
		}
	}
	parsed, err := metav1.LabelSelectorAsSelector(&sel)
	if err != nil {
		return false, false, "has a selector Radar could not parse"
	}
	if !parsed.Matches(id.set) {
		return false, true, ""
	}
	if openWhy != "" {
		return false, false, openWhy
	}
	return true, true, ""
}

// ciliumSelectorKey strips the label source from a selector key and says
// whether the identity can answer for it.
func ciliumSelectorKey(raw string, id CiliumIdentity) (key string, ok bool, why string) {
	source, key, hasSource := strings.Cut(raw, ":")
	if !hasSource {
		key = raw
	} else if source != "k8s" && source != "any" {
		// reserved:, cidr:, container: and the like describe endpoints that
		// are not pods, so a selector on them cannot select this pod — but
		// whether Cilium attaches such a label to a pod is not Radar's to say.
		return "", false, "selects on the " + source + " label source, which Radar does not model"
	}
	switch {
	case key == "io.cilium.k8s.policy.cluster":
		return "", false, "selects on the cluster name, which Radar does not know"
	case strings.HasPrefix(key, "io.cilium.k8s.namespace.labels.") && !id.nsKnown:
		return "", false, "selects on namespace labels Radar could not read here"
	}
	return key, true, ""
}
