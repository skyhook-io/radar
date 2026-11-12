package k8s

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// TraefikRefVerdict answers whether one Service or Middleware an IngressRoute
// rule names exists, with the same evidence the missing-reference detector
// uses. Missing is nil when that evidence can't decide: no running Traefik v3
// loads the route, the inventory isn't synced cluster-wide, the reference is
// refused across namespaces or provider-qualified, or the route is in the
// legacy traefik.containo.us group the detector doesn't model.
type TraefikRefVerdict struct {
	Kind      string // "Service" | "TraefikService" | "Middleware"
	Namespace string
	Name      string
	Missing   *bool
	Via       string // the chain/errors Middleware leading to a missing target
	Reads     []issuesapi.EvidenceRead
}

// TraefikRuleRefs holds the verdicts for one spec.routes[i], in spec order.
type TraefikRuleRefs struct {
	Services    []TraefikRefVerdict
	Middlewares []TraefikRefVerdict
}

// TraefikRouteAssessor shares one detection scope (pod scan and inventories)
// across the routes of one request.
type TraefikRouteAssessor struct {
	cache     *ResourceCache
	dynamic   *DynamicResourceCache
	discovery *ResourceDiscovery
	scope     *DetectScope
	inv       *traefikInventory
}

func NewTraefikRouteAssessor(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery) *TraefikRouteAssessor {
	return &TraefikRouteAssessor{cache: cache, dynamic: dynamicCache, discovery: discovery, scope: NewDetectScope()}
}

// RuleRefs returns one TraefikRuleRefs per spec.routes[i] (empty for a
// malformed entry) so indexes line up with the rule number.
func (a *TraefikRouteAssessor) RuleRefs(route *unstructured.Unstructured) []TraefikRuleRefs {
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
	out := make([]TraefikRuleRefs, len(rules))
	var loaders []traefikInstance
	if a.cache != nil && a.dynamic != nil && a.discovery != nil && resourceid.GroupFromAPIVersion(route.GetAPIVersion()) == traefikGroup {
		instances := a.scope.get("traefikInstances", func() any { return runningTraefikInstances(a.cache) }).([]traefikInstance)
		loaders = traefikLoaders(instances, route)
	}
	ns := route.GetNamespace()
	allowCross := false
	podNamespaces := map[string]bool{}
	for _, inst := range loaders {
		allowCross = allowCross || inst.allowCrossNamespace
		podNamespaces[inst.podNamespace] = true
	}
	var podReads []issuesapi.EvidenceRead
	for _, podNs := range sortedSetKeys(podNamespaces) {
		podReads = append(podReads, issuesapi.EvidenceRead{Resource: "pods", Namespace: podNs, Verb: "list"})
	}
	if len(loaders) > 0 && a.inv == nil {
		a.inv = newTraefikInventory(a.cache, a.dynamic, a.discovery, a.scope, traefikGroup)
	}
	verdict := func(v TraefikRefVerdict, verifiable, missing bool, reads []issuesapi.EvidenceRead) TraefikRefVerdict {
		if verifiable {
			v.Missing = &missing
			v.Reads = append(dedupeEvidenceReads(reads), podReads...)
		}
		return v
	}
	for i, raw := range rules {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, svc := range nestedMaps(r, "services") {
			name, _ := svc["name"].(string)
			kind, _ := svc["kind"].(string)
			if kind == "" {
				kind = "Service"
			}
			svcNs := ns
			refNs, _ := svc["namespace"].(string)
			if refNs != "" {
				svcNs = refNs
			}
			v := TraefikRefVerdict{Kind: kind, Namespace: svcNs, Name: name}
			if len(loaders) > 0 && (refNs == "" || refNs == ns || allowCross) {
				known, missing, _, read := a.inv.servicePresence(svc, svcNs)
				v = verdict(v, known, missing, []issuesapi.EvidenceRead{read})
			}
			out[i].Services = append(out[i].Services, v)
		}
		for _, mw := range nestedMaps(r, "middlewares") {
			name, _ := mw["name"].(string)
			mwNs := ns
			if refNs, _ := mw["namespace"].(string); refNs != "" {
				mwNs = refNs
			}
			v := TraefikRefVerdict{Kind: "Middleware", Namespace: mwNs, Name: name}
			if len(loaders) > 0 {
				known, m, missing := a.inv.middlewarePresence(mw, ns, allowCross, map[string]bool{})
				v.Via = m.via
				v = verdict(v, known, missing, m.reads)
			}
			out[i].Middlewares = append(out[i].Middlewares, v)
		}
	}
	return out
}
