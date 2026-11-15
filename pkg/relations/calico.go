package relations

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/lru"

	"github.com/skyhook-io/radar/pkg/netpol"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Calico serves its policies through two groups that hold the same objects:
// the API server's projectcalico.org and the CRDs' crd.projectcalico.org.
var calicoGroups = []string{"projectcalico.org", "crd.projectcalico.org"}

// calicoPolicyKinds are Calico's policy kinds, as topology.CalicoPolicyKinds
// lists them (a test keeps the two equal).
var calicoPolicyKinds = []struct {
	kind       string
	namespaced bool
	staged     bool
}{
	{"NetworkPolicy", true, false},
	{"GlobalNetworkPolicy", false, false},
	{"StagedNetworkPolicy", true, true},
	{"StagedGlobalNetworkPolicy", false, true},
	{"StagedKubernetesNetworkPolicy", true, true},
}

// calicoSelectors are the policy kinds, staged or enforced, in both groups.
func calicoSelectors(staged bool) []Selector {
	var out []Selector
	for _, group := range calicoGroups {
		for _, k := range calicoPolicyKinds {
			if k.staged == staged {
				out = append(out, Selector{Kind: resourceid.GroupKind{Group: group, Kind: k.kind}, SameNamespace: k.namespaced, Selection: calicoSelection(k.namespaced, k.staged)})
			}
		}
	}
	return out
}

// calicoIdentity names a policy the same way whichever group served it.
func calicoIdentity(r resourceid.Ref) resourceid.Ref {
	if r.Group == "crd.projectcalico.org" {
		r.Group = "projectcalico.org"
	}
	return r
}

// calicoSelection is a Calico policy's selection, as Calico matches it
// (netpol.CompileCalicoPolicyMatcher): its selector over the endpoint labels
// Calico adds to a workload's pods, narrowed by its namespace and
// ServiceAccount selectors, whose labels are read (see Lookup) only once the
// selectors before them match. A
// global policy chooses in every namespace. A staged policy that previews no
// protection, as a staged deletion doesn't, chooses nothing.
func calicoSelection(namespaced, staged bool) func(runtime.Object) (Selection, bool, error) {
	return func(obj runtime.Object) (Selection, bool, error) {
		policy, ok := obj.(*unstructured.Unstructured)
		if !ok || staged && !netpol.CalicoStagedActionPreviewsProtection(policy) {
			return Selection{}, false, nil
		}
		matcher := compiledCalicoMatcher(policy)
		_, _, raw := matcher.Needs()
		return Selection{AnyNamespace: !namespaced, Matches: func(c Candidate, lookup Lookup) (bool, bool) {
			serviceAccount := c.ServiceAccount
			if serviceAccount == "" {
				serviceAccount = "default"
			}
			podLabels := c.Labels
			if !raw {
				podLabels = netpol.CalicoEndpointLabels(c.Ref.Namespace, c.Labels, serviceAccount)
			}
			namespaceLabels := func() (map[string]string, bool) {
				obj, ok := lookup(resourceid.NewRef("", "Namespace", "", c.Ref.Namespace))
				ns, isNamespace := obj.(*corev1.Namespace)
				if !ok || !isNamespace {
					return nil, false
				}
				return netpol.CalicoNamespaceLabels(ns), true
			}
			serviceAccountLabels := func() (map[string]string, bool) {
				obj, ok := lookup(resourceid.NewRef("", "ServiceAccount", c.Ref.Namespace, serviceAccount))
				sa, isServiceAccount := obj.(*corev1.ServiceAccount)
				if !ok || !isServiceAccount {
					return nil, false
				}
				return netpol.CalicoServiceAccountLabels(sa), true
			}
			return matcher.MatchesLazily(podLabels, namespaceLabels, serviceAccount, serviceAccountLabels)
		}}, true, nil
	}
}

// CalicoPolicies is the family of the workloads Calico's NetworkPolicies and
// GlobalNetworkPolicies apply to.
var CalicoPolicies = &SelectorFamily{
	Name:           "calicoPolicies",
	Selectors:      calicoSelectors(false),
	Selectables:    []Selectable{selectDeployment, selectStatefulSet, selectDaemonSet},
	CrossNamespace: true,
	Identity:       calicoIdentity,
}

// CalicoStagedPolicies is the family of the workloads Calico's staged
// policies would apply to once promoted.
var CalicoStagedPolicies = &SelectorFamily{
	Name:           "calicoStagedPolicies",
	Selectors:      calicoSelectors(true),
	Selectables:    []Selectable{selectDeployment, selectStatefulSet, selectDaemonSet},
	CrossNamespace: true,
	Identity:       calicoIdentity,
}

var calicoMatchers = lru.New(4096)

type calicoMatcherKey struct {
	uid     types.UID
	version string
	kind    string
}

func compiledCalicoMatcher(policy *unstructured.Unstructured) *netpol.CalicoPolicyMatcher {
	key := calicoMatcherKey{policy.GetUID(), policy.GetResourceVersion(), policy.GetKind()}
	if key.uid == "" || key.version == "" {
		return netpol.CompileCalicoPolicyMatcher(policy)
	}
	if cached, ok := calicoMatchers.Get(key); ok {
		return cached.(*netpol.CalicoPolicyMatcher)
	}
	matcher := netpol.CompileCalicoPolicyMatcher(policy)
	calicoMatchers.Add(key, matcher)
	return matcher
}
