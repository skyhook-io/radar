package topology

import (
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/relations"
)

// ApplicationSelection retains the display producer's policy attachment rules.
// Applications does not inherit the drawer's expanded policy semantics when
// switching its inventory to families.
func ApplicationSelection(selector relations.Selector, obj runtime.Object) (relations.Selection, bool, error) {
	if np, ok := obj.(*networkingv1.NetworkPolicy); ok && np.Spec.PodSelector.Size() == 0 {
		return relations.Selection{}, false, nil
	}
	if u, ok := obj.(*unstructured.Unstructured); ok {
		if selector.Kind.Group == "cilium.io" && selector.Kind.Kind == "CiliumNetworkPolicy" {
			matchLabels, _, _ := unstructured.NestedMap(u.Object, "spec", "endpointSelector", "matchLabels")
			if len(matchLabels) == 0 {
				return relations.Selection{}, false, nil
			}
			return relations.Selection{Matches: func(c relations.Candidate, _ relations.Lookup) (bool, bool) {
				return matchesStringMap(c.Labels, matchLabels), true
			}}, true, nil
		}
		if selector.Kind.Group == "projectcalico.org" || selector.Kind.Group == "crd.projectcalico.org" {
			for _, definition := range calicoPolicyDefinitions {
				if definition.kind == selector.Kind.Kind && calicoPolicyMatchesAllWorkloads(u, definition) {
					return relations.Selection{}, false, nil
				}
			}
		}
	}
	return selector.Selection(obj)
}
