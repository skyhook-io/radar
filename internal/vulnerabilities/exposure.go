package vulnerabilities

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/ingressstatus"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// Route is a way in from outside the cluster that Radar sees to a workload:
// a LoadBalancer or NodePort Service selecting its Pods, or an Ingress or
// Gateway API route with an address sending to such a Service. It is
// evidence of a configured path, not proof that traffic gets through (a
// NetworkPolicy or a firewall can still stop it).
type Route struct {
	// Via is the kind of way in: LoadBalancer, NodePort, Ingress, or the
	// route kind (HTTPRoute, GRPCRoute, TCPRoute, TLSRoute, UDPRoute).
	Via       string `json:"via"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// ExposedWorkload is a workload with a route from outside the cluster.
// Routes lists at most MaxRoutesPerWorkload of RoutesTotal in a list read.
type ExposedWorkload struct {
	WorkloadRef
	Routes      []Route `json:"routes"`
	RoutesTotal int     `json:"routesTotal"`
}

// Exposure says where Radar looked for routes from outside the cluster. Its
// presence says this Radar checks exposure at all; an older one sends none.
type Exposure struct {
	// Gaps are the route kinds Radar couldn't read, and where: workloads
	// there may have a route without being marked. A Gateway API gap
	// reaches every namespace, since a route, its Gateway and its backend
	// can each sit in another namespace. A user_forbidden gap names the
	// workload namespaces for which the caller can't read a route kind; it
	// follows their access, never what exists.
	Gaps []resourceapi.Gap `json:"gaps,omitempty"`
}

// exposingService is a Service that routes from outside reach, and what
// they are.
type exposingService struct {
	namespace string
	selection relations.Selection
	routes    []Route
	// has is routes as a set, for adding each route once.
	has map[Route]bool
	// id tells exposing Services apart in a memo key.
	id int
}

// exposureIndex finds the routes from outside to a Pod, by the Services
// that select it.
type exposureIndex struct {
	byNamespace map[string][]*exposingService
	// memo holds the answer per namespace and label set: a workload's
	// replicas share their labels, so each set is matched once.
	memo map[string][]Route
	// unknownKinds are the route kinds the caller can't read for a workload
	// namespace, from their access alone (never from what exists), and
	// hidden records them per namespace a workload runs in.
	unknownKinds func(namespace string) []resourceapi.KindRef
	hidden       map[resourceapi.KindRef]map[string]bool
	// grantGaps are the namespaces of backends whose ReferenceGrants Radar
	// couldn't read, by why.
	grantGaps map[readreason.Code]map[string]bool
}

// routeSources are the objects exposure is read from.
type routeSources struct {
	services  []*corev1.Service
	ingresses []*networkingv1.Ingress
	gateways  []*unstructured.Unstructured
	routes    []*unstructured.Unstructured
	// gatewayKinds are the Gateway API kinds the cluster serves.
	gatewayKinds []string
	// grants are the ReferenceGrants; grantsUnread says why they couldn't
	// be read ("" when they were, or the kind isn't served).
	grants       []*unstructured.Unstructured
	grantsUnread readreason.Code
}

// indexExposure joins routes to the Services they reach. may is what the
// caller may read of a kind in a namespace (nil allows all); an object the
// caller may not read is never used, so it is never named. unknown names
// the route kinds the caller can't read for a workload namespace (nil: none);
// workloads there read as unknown rather than as having no route (gaps).
func indexExposure(ctx context.Context, src routeSources, may func(group, kind, namespace string) bool, unknown func(namespace string) []resourceapi.KindRef) (exposureIndex, error) {
	allowed := func(group, kind, namespace string) bool { return may == nil || may(group, kind, namespace) }
	services := map[string]*exposingService{}
	for _, svc := range src.services {
		if err := ctx.Err(); err != nil {
			return exposureIndex{}, err
		}
		if !allowed("", "Service", svc.Namespace) {
			continue
		}
		// The Service selector rule the relations graph uses: no selector,
		// or an ExternalName Service, chooses no Pods.
		selection, ok, err := relations.ServiceSelectors.Selectors[0].Selection(svc)
		if !ok || err != nil {
			continue
		}
		s := &exposingService{namespace: svc.Namespace, selection: selection, has: map[Route]bool{}}
		if via, ok := serviceRoute(svc); ok {
			r := Route{Via: via, Namespace: svc.Namespace, Name: svc.Name}
			s.routes, s.has[r] = append(s.routes, r), true
		}
		services[svc.Namespace+"/"+svc.Name] = s
	}
	grantGaps := map[readreason.Code]map[string]bool{}
	reach := func(namespace, name string, r Route) {
		if s := services[namespace+"/"+name]; s != nil && !s.has[r] {
			s.routes, s.has[r] = append(s.routes, r), true
		}
	}
	for _, ing := range src.ingresses {
		if err := ctx.Err(); err != nil {
			return exposureIndex{}, err
		}
		// Without an address no controller has taken the Ingress, so there
		// is no evidence of a way in.
		if !allowed("networking.k8s.io", "Ingress", ing.Namespace) || !ingressstatus.HasAssignedAddress(ing) {
			continue
		}
		for _, name := range configrefs.IngressBackendServices(ing) {
			reach(ing.Namespace, name, Route{Via: "Ingress", Namespace: ing.Namespace, Name: ing.Name})
		}
	}
	addressed := map[string]bool{}
	for _, gw := range src.gateways {
		if allowed(configrefs.GatewayAPIGroup, "Gateway", gw.GetNamespace()) && gatewayHasAddress(gw) {
			addressed[gw.GetNamespace()+"/"+gw.GetName()] = true
		}
	}
	for _, route := range src.routes {
		if err := ctx.Err(); err != nil {
			return exposureIndex{}, err
		}
		if !allowed(configrefs.GatewayAPIGroup, route.GetKind(), route.GetNamespace()) || !routeAttached(route, addressed) {
			continue
		}
		r := Route{Via: route.GetKind(), Namespace: route.GetNamespace(), Name: route.GetName()}
		// The controller found a backend in another namespace that no
		// ReferenceGrant allows: such a backend gets nothing, so each one is
		// checked against the grants; where they couldn't be read, its
		// workloads' exposure is unknown.
		refused := refNotPermitted(route)
		visitForwardingBackends(route, func(namespace, name string) {
			unknown := func(code readreason.Code) {
				if allowed("", "Service", namespace) {
					if grantGaps[code] == nil {
						grantGaps[code] = map[string]bool{}
					}
					grantGaps[code][namespace] = true
				}
			}
			switch {
			case !refused || namespace == route.GetNamespace():
				reach(namespace, name, r)
			case !allowed(configrefs.GatewayAPIGroup, "ReferenceGrant", namespace):
				// The caller may not read the grants there: what they allow
				// mustn't show through, so the backend is unknown whatever
				// they say.
				unknown(readreason.UserForbidden)
			case src.grantsUnread != "":
				unknown(src.grantsUnread)
			case grantAllows(src.grants, route.GetKind(), route.GetNamespace(), namespace, name):
				reach(namespace, name, r)
			}
		})
	}
	x := exposureIndex{byNamespace: map[string][]*exposingService{}, memo: map[string][]Route{},
		unknownKinds: unknown, hidden: map[resourceapi.KindRef]map[string]bool{}, grantGaps: grantGaps}
	for _, s := range services {
		if len(s.routes) > 0 {
			s.id = len(x.byNamespace[s.namespace])
			x.byNamespace[s.namespace] = append(x.byNamespace[s.namespace], s)
		}
	}
	return x, nil
}

// routesFor are the routes from outside to a Pod the caller may see,
// sorted. It also notes the route kinds the caller can't read for the Pod's
// namespace (see gaps).
func (x exposureIndex) routesFor(ctx context.Context, pod *corev1.Pod) ([]Route, error) {
	if x.unknownKinds != nil {
		for _, kind := range x.unknownKinds(pod.Namespace) {
			if x.hidden[kind] == nil {
				x.hidden[kind] = map[string]bool{}
			}
			x.hidden[kind][pod.Namespace] = true
		}
	}
	return x.visibleRoutes(ctx, pod)
}

// gaps are where exposure is unknown: the workload namespaces whose route
// kinds the caller can't read, and the namespaces of refused backends whose
// ReferenceGrants Radar couldn't read.
func (x exposureIndex) gaps() []resourceapi.Gap {
	var out []resourceapi.Gap
	for kind, namespaces := range x.hidden {
		out = append(out, gap(kinds(kind), readreason.UserForbidden, slices.Sorted(maps.Keys(namespaces))))
	}
	for code, namespaces := range x.grantGaps {
		out = append(out, gap(referenceGrantKind, code, slices.Sorted(maps.Keys(namespaces))))
	}
	slices.SortFunc(out, func(a, b resourceapi.Gap) int {
		return cmp.Or(cmp.Compare(a.Kinds.Names[0].Kind, b.Kinds.Names[0].Kind), cmp.Compare(a.Code, b.Code))
	})
	return out
}

var referenceGrantKind = kinds(resourceapi.KindRef{Kind: "ReferenceGrant", Group: configrefs.GatewayAPIGroup})

// grantAllows is whether a ReferenceGrant in the backend's namespace lets a
// route of fromKind in fromNamespace refer to the Service name there.
func grantAllows(grants []*unstructured.Unstructured, fromKind, fromNamespace, namespace, name string) bool {
	return slices.ContainsFunc(grants, func(g *unstructured.Unstructured) bool {
		if g.GetNamespace() != namespace {
			return false
		}
		from, _, _ := unstructured.NestedSlice(g.Object, "spec", "from")
		to, _, _ := unstructured.NestedSlice(g.Object, "spec", "to")
		return slices.ContainsFunc(from, func(f any) bool {
			m, _ := f.(map[string]any)
			return m["group"] == configrefs.GatewayAPIGroup && m["kind"] == fromKind && m["namespace"] == fromNamespace
		}) && slices.ContainsFunc(to, func(t any) bool {
			m, _ := t.(map[string]any)
			group, _ := m["group"].(string)
			toName, _ := m["name"].(string)
			return group == "" && m["kind"] == "Service" && (toName == "" || toName == name)
		})
	})
}

// visibleRoutes are the routes of the exposing Services that select the
// Pod. Pods selected by the same Services share one list, whatever else
// their labels say (a StatefulSet's Pods each have their own).
func (x exposureIndex) visibleRoutes(ctx context.Context, pod *corev1.Pod) ([]Route, error) {
	services := x.byNamespace[pod.Namespace]
	if len(services) == 0 {
		return nil, nil
	}
	candidate := relations.Candidate{Labels: labels.Set(pod.Labels)}
	var chosen []*exposingService
	var key strings.Builder
	key.WriteString(pod.Namespace)
	for i, s := range services {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if ok, _ := s.selection.Matches(candidate, nil); ok {
			chosen = append(chosen, s)
			key.WriteString("\x00")
			key.WriteString(strconv.Itoa(s.id))
		}
	}
	if len(chosen) == 0 {
		return nil, nil
	}
	if routes, ok := x.memo[key.String()]; ok {
		// Shared: callers only read it.
		return routes, nil
	}
	lists := make([][]Route, len(chosen))
	for i, s := range chosen {
		lists[i] = s.routes
	}
	routes := mergeRoutes(lists)
	x.memo[key.String()] = routes
	return routes, nil
}

// mergeRoutes is the routes of several lists, each once, sorted: a list of
// its own, built in time linear in what it is given.
func mergeRoutes(lists [][]Route) []Route {
	var out []Route
	set := map[Route]bool{}
	for _, routes := range lists {
		for _, r := range routes {
			if !set[r] {
				set[r] = true
				out = append(out, r)
			}
		}
	}
	sortRoutes(out)
	return out
}

// routeUnion notes the distinct route lists a workload or target gathers,
// by identity: every replica of a workload usually brings the same shared
// slice from visibleRoutes, and that one is kept once.
type routeUnion struct {
	sources [][]Route
	taken   map[routeListID]bool
}

type routeListID struct {
	first *Route
	n     int
}

func (u *routeUnion) add(routes []Route) {
	if len(routes) == 0 {
		return
	}
	id := routeListID{&routes[0], len(routes)}
	if u.taken[id] {
		return
	}
	if u.taken == nil {
		u.taken = map[routeListID]bool{}
	}
	u.taken[id] = true
	u.sources = append(u.sources, routes)
}

// routeMerger turns routeUnions into route lists without copying what is
// already shared. A union of one list (sorted, as every source is) is that
// list; unions of the same lists, by identity, share one merged list.
// Callers only read what it returns.
type routeMerger struct {
	memo map[string][]Route
}

func (m *routeMerger) routes(u *routeUnion) []Route {
	if len(u.sources) == 1 && slices.IsSortedFunc(u.sources[0], compareRoutes) {
		return u.sources[0]
	}
	var key strings.Builder
	for _, src := range u.sources {
		fmt.Fprintf(&key, "%p/%d,", &src[0], len(src))
	}
	if routes, ok := m.memo[key.String()]; ok {
		return routes
	}
	if m.memo == nil {
		m.memo = map[string][]Route{}
	}
	routes := mergeRoutes(u.sources)
	m.memo[key.String()] = routes
	return routes
}

func sortRoutes(routes []Route) { slices.SortFunc(routes, compareRoutes) }

func compareRoutes(a, b Route) int {
	return cmp.Or(cmp.Compare(a.Via, b.Via), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
}

// serviceRoute is the way in a Service opens itself: every NodePort Service,
// and a LoadBalancer with an address or a node port (Kubernetes allocates
// node ports for a LoadBalancer unless told not to).
func serviceRoute(svc *corev1.Service) (string, bool) {
	switch svc.Spec.Type {
	case corev1.ServiceTypeNodePort:
		return "NodePort", true
	case corev1.ServiceTypeLoadBalancer:
		hasAddress := slices.ContainsFunc(svc.Status.LoadBalancer.Ingress, func(a corev1.LoadBalancerIngress) bool { return a.IP != "" || a.Hostname != "" })
		hasNodePort := slices.ContainsFunc(svc.Spec.Ports, func(p corev1.ServicePort) bool { return p.NodePort > 0 })
		return "LoadBalancer", hasAddress || hasNodePort
	}
	return "", false
}

// gatewayHasAddress is whether a Gateway reports an address to reach it at.
func gatewayHasAddress(gw *unstructured.Unstructured) bool {
	addresses, _, _ := unstructured.NestedSlice(gw.Object, "status", "addresses")
	return slices.ContainsFunc(addresses, func(a any) bool {
		m, _ := a.(map[string]any)
		v, _ := m["value"].(string)
		return v != ""
	})
}

// routeAttached is whether a route is attached to a Gateway with an address
// that hasn't rejected it: the route's status for that Gateway doesn't say
// Accepted=False on every listener it reports.
func routeAttached(route *unstructured.Unstructured, addressed map[string]bool) bool {
	statuses, _, _ := unstructured.NestedSlice(route.Object, "status", "parents")
	specParents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	attached := false
	configrefs.VisitGatewayRouteParents(route, func(p configrefs.PathRef) {
		if attached || !addressed[p.Ref.Namespace+"/"+p.Ref.Name] {
			return
		}
		reported, rejected := 0, 0
		for _, s := range statuses {
			m, _ := s.(map[string]any)
			ref, _ := m["parentRef"].(map[string]any)
			name, _ := ref["name"].(string)
			// A status entry for a listener the spec no longer names is stale.
			if ref == nil || !configrefs.GatewayParentIsGateway(ref) || name != p.Ref.Name || configrefs.GatewayRefNamespace(ref, route.GetNamespace()) != p.Ref.Namespace ||
				!configrefs.GatewayParentRefInSpec(ref, specParents, route.GetNamespace()) {
				continue
			}
			reported++
			if conditionFalse(m, "Accepted") {
				rejected++
			}
		}
		attached = reported == 0 || rejected < reported
	})
	return attached
}

// refNotPermitted is whether the route's status says a Gateway it is
// attached to refused one of its references for want of a ReferenceGrant.
func refNotPermitted(route *unstructured.Unstructured) bool {
	statuses, _, _ := unstructured.NestedSlice(route.Object, "status", "parents")
	specParents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	for _, s := range statuses {
		m, _ := s.(map[string]any)
		ref, _ := m["parentRef"].(map[string]any)
		if ref == nil || !configrefs.GatewayParentRefInSpec(ref, specParents, route.GetNamespace()) {
			continue
		}
		conditions, _ := m["conditions"].([]any)
		for _, c := range conditions {
			cm, _ := c.(map[string]any)
			if cm["type"] == "ResolvedRefs" && cm["status"] == "False" && cm["reason"] == "RefNotPermitted" {
				return true
			}
		}
	}
	return false
}

func conditionFalse(status map[string]any, conditionType string) bool {
	conditions, _ := status["conditions"].([]any)
	for _, c := range conditions {
		m, _ := c.(map[string]any)
		if m["type"] == conditionType && m["status"] == "False" {
			return true
		}
	}
	return false
}

// visitForwardingBackends visits the Services a route sends requests to:
// its Service backendRefs, except those weighted 0, which get none, and the
// backends its requestMirror filters copy requests to (unless set to copy
// none).
func visitForwardingBackends(route *unstructured.Unstructured, visit func(namespace, name string)) {
	service := func(m map[string]any) {
		name, _ := m["name"].(string)
		if m != nil && name != "" && configrefs.GatewayBackendIsService(m) {
			visit(configrefs.GatewayRefNamespace(m, route.GetNamespace()), name)
		}
	}
	mirrors := func(filters []any) {
		for _, f := range filters {
			mirror, _ := f.(map[string]any)["requestMirror"].(map[string]any)
			ref, ok := mirror["backendRef"].(map[string]any)
			// A mirror set to copy no requests sends the backend nothing.
			if percent, set := number(mirror["percent"]); !ok || (set && percent == 0) {
				continue
			}
			if fraction, _ := mirror["fraction"].(map[string]any); fraction != nil {
				if n, set := number(fraction["numerator"]); set && n == 0 {
					continue
				}
			}
			service(ref)
		}
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	for _, rule := range rules {
		r, _ := rule.(map[string]any)
		filters, _ := r["filters"].([]any)
		mirrors(filters)
		backends, _ := r["backendRefs"].([]any)
		for _, b := range backends {
			m, _ := b.(map[string]any)
			if w, ok := number(m["weight"]); ok && w == 0 {
				continue
			}
			service(m)
			backendFilters, _ := m["filters"].([]any)
			mirrors(backendFilters)
		}
	}
}

// Each exposure gap names the route kind Radar couldn't read.
var (
	serviceKind = kinds(resourceapi.KindRef{Kind: "Service"})
	ingressKind = kinds(resourceapi.KindRef{Kind: "Ingress", Group: "networking.k8s.io"})
)

// readExposure reads the routes from outside the cluster. Services and
// Ingresses are read where the workloads are (a Service selects Pods in its
// own namespace and an Ingress sends to Services in its own); Gateways and
// routes everywhere Radar holds them, since they reach across namespaces:
// indexExposure keeps the caller to what they may see. It never waits: a kind Radar hasn't
// loaded yet is a gap for this read, and its watch starts for the next one.
func readExposure(ctx context.Context, cache *k8s.ResourceCache, dynamic *k8s.DynamicResourceCache, discovery *k8s.ResourceDiscovery, workloadScope []string) (routeSources, []resourceapi.Gap) {
	var src routeSources
	var gaps []resourceapi.Gap
	if cache == nil || cache.Services() == nil {
		return src, []resourceapi.Gap{{Code: readreason.RadarKindNotCollected, Kinds: serviceKind, Namespaces: resourceapi.AllNames()}}
	}
	svcRead := cache.AssessKindRead("services", workloadScope)
	svcScope, svcGaps, ok := narrowToTyped(serviceKind, svcRead, workloadScope, nil)
	if !ok {
		// Every way in ends at a Service: without them nothing is known.
		return src, []resourceapi.Gap{kindGap(serviceKind, svcRead)}
	}
	gaps = append(gaps, svcGaps...)
	for _, ns := range scopeNamespaces(svcScope) {
		var list []*corev1.Service
		if ns == "" {
			list, _ = cache.Services().List(labels.Everything())
		} else {
			list, _ = cache.Services().Services(ns).List(labels.Everything())
		}
		src.services = append(src.services, list...)
	}

	if cache.Ingresses() == nil {
		gaps = append(gaps, resourceapi.Gap{Code: readreason.RadarKindNotCollected, Kinds: ingressKind, Namespaces: resourceapi.AllNames()})
	} else {
		ingRead := cache.AssessKindRead("ingresses", svcScope)
		ingScope, ingGaps, ok := narrowToTyped(ingressKind, ingRead, svcScope, nil)
		if !ok {
			gaps = append(gaps, kindGap(ingressKind, ingRead))
		} else {
			gaps = append(gaps, ingGaps...)
			for _, ns := range scopeNamespaces(ingScope) {
				var list []*networkingv1.Ingress
				if ns == "" {
					list, _ = cache.Ingresses().List(labels.Everything())
				} else {
					list, _ = cache.Ingresses().Ingresses(ns).List(labels.Everything())
				}
				src.ingresses = append(src.ingresses, list...)
			}
		}
	}

	if dynamic == nil || discovery == nil {
		return src, gaps
	}
	for _, kind := range append([]string{"Gateway"}, configrefs.GatewayRouteKinds...) {
		gvr, ok := discovery.GetGVRWithGroup(kind, configrefs.GatewayAPIGroup)
		if !ok {
			continue // the cluster doesn't serve it: nothing to read, not a gap
		}
		src.gatewayKinds = append(src.gatewayKinds, kind)
		items, gap := readGatewayKind(ctx, dynamic, gvr, kind, nil)
		if gap != nil {
			gaps = append(gaps, *gap)
		}
		if kind == "Gateway" {
			src.gateways = append(src.gateways, items...)
		} else {
			src.routes = append(src.routes, items...)
		}
	}
	// ReferenceGrants matter only for a route whose controller refused a
	// backend; where they can't be read, that is a gap then (indexExposure).
	if gvr, ok := discovery.GetGVRWithGroup("ReferenceGrant", configrefs.GatewayAPIGroup); ok {
		items, gap := readGatewayKind(ctx, dynamic, gvr, "ReferenceGrant", nil)
		src.grants = items
		if gap != nil {
			src.grantsUnread = gap.Code
		}
	}
	return src, gaps
}

// readGatewayKind lists a Gateway API kind where Radar has it loaded. Where
// it hasn't, the read is a gap over every namespace, and the watch starts
// in the background for the next read.
func readGatewayKind(ctx context.Context, dynamic *k8s.DynamicResourceCache, gvr schema.GroupVersionResource, kind string, scope []string) ([]*unstructured.Unstructured, *resourceapi.Gap) {
	plan := readplan.Dynamic(dynamic, gvr, scope, false)
	var items []*unstructured.Unstructured
	var missing readreason.Code
	for _, part := range plan.Partitions {
		if part.Verdict.State != readplan.Readable {
			missing = cmp.Or(missing, part.Verdict.Code)
			continue
		}
		namespaces := []string{""}
		if part.Extent == readplan.NamespaceSet {
			namespaces = part.Namespaces
		}
		for _, ns := range namespaces {
			list, err := dynamic.ListReadOnlyContext(ctx, gvr, ns)
			if err != nil {
				missing = cmp.Or(missing, readreason.ReadError)
				continue
			}
			items = append(items, list...)
		}
	}
	if len(plan.Partitions) == 0 && (scope == nil || len(scope) > 0) {
		missing = readreason.KindSyncPending
	}
	// Not watched yet, or still loading: start the watch for the next read.
	if missing == readreason.KindSyncPending || missing == readreason.RadarKindNotCollected {
		go func() { _ = dynamic.EnsureWatchingContext(context.Background(), gvr, "") }()
	}
	if plan.CollectionLimited {
		// Radar collects the kind only in some namespaces: a route elsewhere
		// can reach a Service in one it does.
		missing = cmp.Or(missing, readreason.RadarNamespaceNotCollected)
	}
	if missing == "" {
		return items, nil
	}
	return items, &resourceapi.Gap{Code: missing, Kinds: kinds(resourceapi.KindRef{Kind: kind, Group: configrefs.GatewayAPIGroup}), Namespaces: resourceapi.AllNames()}
}
