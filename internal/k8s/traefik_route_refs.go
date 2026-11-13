package k8s

import (
	"fmt"

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
	Port      string // a Service backend's port as written
	Via       string // the chain/errors Middleware leading to a missing target
	// Refused: a Traefik that loads the route rejects this cross-namespace
	// reference, so it never routes there.
	Refused bool
	Reads   []issuesapi.EvidenceRead
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

// ParseTraefikRuleRefs returns the Services and Middlewares each
// spec.routes[i] names, with no verdict, one entry per rule (empty for a
// malformed one) so indexes line up with the rule number.
func ParseTraefikRuleRefs(route *unstructured.Unstructured) []TraefikRuleRefs {
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
	out := make([]TraefikRuleRefs, len(rules))
	ns := route.GetNamespace()
	refNamespace := func(m map[string]any) string {
		if refNs, _ := m["namespace"].(string); refNs != "" {
			return refNs
		}
		return ns
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
			v := TraefikRefVerdict{Kind: kind, Namespace: refNamespace(svc), Name: name}
			if port, ok := svc["port"]; ok && port != nil {
				v.Port = fmt.Sprint(port)
			}
			out[i].Services = append(out[i].Services, v)
		}
		for _, mw := range nestedMaps(r, "middlewares") {
			name, _ := mw["name"].(string)
			out[i].Middlewares = append(out[i].Middlewares, TraefikRefVerdict{Kind: "Middleware", Namespace: refNamespace(mw), Name: name})
		}
	}
	return out
}

// RuleRefs is ParseTraefikRuleRefs with each verdict filled in from the
// detector's evidence.
func (a *TraefikRouteAssessor) RuleRefs(route *unstructured.Unstructured) []TraefikRuleRefs {
	out := ParseTraefikRuleRefs(route)
	var loaders []traefikInstance
	if a.cache != nil && a.dynamic != nil && a.discovery != nil && resourceid.GroupFromAPIVersion(route.GetAPIVersion()) == traefikGroup {
		instances := a.scope.get("traefikInstances", func() any { return runningTraefikInstances(a.cache) }).([]traefikInstance)
		loaders = traefikLoaders(instances, route)
	}
	if len(loaders) == 0 {
		return out
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
	if a.inv == nil {
		a.inv = newTraefikInventory(a.cache, a.dynamic, a.discovery, a.scope, traefikGroup)
	}
	decide := func(v *TraefikRefVerdict, verifiable, missing bool, reads []issuesapi.EvidenceRead) {
		if verifiable {
			v.Missing = &missing
			v.Reads = append(dedupeEvidenceReads(reads), podReads...)
		}
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
	for i, raw := range rules {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for j, svc := range nestedMaps(r, "services") {
			v := &out[i].Services[j]
			if v.Namespace != ns && !allowCross {
				v.Refused = true
				continue
			}
			known, missing, _, read := a.inv.servicePresence(svc, v.Namespace)
			decide(v, known, missing, []issuesapi.EvidenceRead{read})
		}
		for j, mw := range nestedMaps(r, "middlewares") {
			v := &out[i].Middlewares[j]
			v.Refused = v.Namespace != ns && !allowCross
			known, m, missing := a.inv.middlewarePresence(mw, ns, allowCross, map[string]*traefikVisit{})
			v.Via = m.via
			decide(v, known, missing, m.reads)
		}
	}
	return out
}
