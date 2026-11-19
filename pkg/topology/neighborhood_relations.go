package topology

import (
	"cmp"
	"context"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// WalkNeighborhood asks the relation families only about the admitted frontier.
// Allow applies to family reads as well as admission: a hidden intermediate
// cannot supply a composition, a PodGroup member, or a budget charge.
func WalkNeighborhood(ctx context.Context, session relations.Session, root ResourceRef, opts NeighborhoodOptions, provider ResourceProvider, dp DynamicProvider) (*Subgraph, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sub := &Subgraph{Root: root, Nodes: []Node{}, Edges: []Edge{}}
	access := &neighborhoodAccess{catalog: session.Catalog(), allow: opts.Allow, decisions: map[resourceid.GroupKind]map[string]ownership.Decision{}}
	requestSession := &neighborhoodSession{Session: session, ctx: ctx, access: access, objects: map[resourceid.Ref]neighborhoodObject{}, indexes: map[string]relations.IndexedRead{}}
	session = requestSession
	candidates := neighborhoodRoots(session.Catalog(), root, dp)
	var subject resourceid.Ref
	var object runtime.Object
	var unavailable []readreason.Code
	for _, r := range candidates {
		if !access.allowed(r) {
			continue
		}
		obj, read := session.Borrow(r)
		if read.State == ownership.Unavailable {
			unavailable = append(unavailable, read.Gap.Reason)
			if read.Gap.Reason == readreason.RadarKindNotCollected {
				sub.UncollectedRoots = append(sub.UncollectedRoots, r)
			}
		}
		if read.State != ownership.Present {
			continue
		}
		if object != nil && !isCalicoAPIGroup(r.Group) {
			sub.AmbiguousRoot = true
			return sub, nil
		}
		subject, object = r, obj
	}
	// Every read refuses once ctx ends, so a root read past the deadline
	// would look absent.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if object == nil {
		if len(unavailable) > 0 {
			sub.RootUnavailable = readreason.Primary(unavailable)
		}
		return sub, nil
	}
	sub.Root = refOf(subject)
	hops := opts.Hops
	if hops <= 0 {
		hops = neighborhoodDefaultHops
	}
	hops = min(hops, neighborhoodMaxHops)
	limit := opts.MaxNodes
	if limit <= 0 {
		limit = neighborhoodDefaultMaxNodes
	}
	limit = min(limit, 200)
	rootNode := access.node(subject)
	allowed := edgeTypesForProfile(opts.Profile, rootNode.Kind)
	rootOnly := rootOnlyEdgeTypes(opts.Profile, rootNode.Kind)
	families := []*relations.Family{}
	domain := ""
	// The domain is read from every cached Deployment; only Istio's service
	// hosts need it.
	if provider != nil && neighborhoodDisposition(relations.IstioServiceHosts("").Projection, allowed) {
		domain = NewBuilder(provider).istioServiceDomain()
	}
	for _, f := range relations.Families(domain) {
		if neighborhoodDisposition(f.Projection, allowed) {
			families = append(families, f)
		}
	}
	selectors := []*relations.SelectorFamily{}
	for _, f := range relations.SelectorFamilies() {
		if neighborhoodDisposition(f.Projection, allowed) {
			selectors = append(selectors, f)
		}
	}
	v := relations.NewView(session, access).WithAuxiliarySession(neighborhoodAuxiliarySession{requestSession}).WithLimit(0)
	ov := ownership.NewView(session, access)
	objects := map[resourceid.Ref]runtime.Object{subject: object}
	order := []resourceid.Ref{subject}
	included := map[resourceid.Ref]bool{subject: true}
	for _, alias := range access.aliases(subject) {
		included[alias] = true
	}
	frontier := []resourceid.Ref{subject}
	graph := &neighborhood{nodes: map[resourceid.Ref]bool{}, edges: map[string]bool{}, coverage: map[string]relations.Coverage{}, access: access}
	groups := map[string]bool{}
	grouped := map[resourceid.Ref]string{}
	jobUIDs := map[string]types.UID{}

	// unanswered is the last hop's admissions, which no hop asks about.
	var unanswered []resourceid.Ref
	for hop := 0; hop < hops; hop++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unanswered = nil
		for _, r := range frontier {
			obj := objects[r]
			graph.answer(session, v, families, selectors, r, obj, false)
			for _, f := range relations.Compositions() {
				if !neighborhoodDisposition(f.Projection, allowed) {
					continue
				}
				for _, e := range v.Composed(f, r, obj).Edges {
					if edge, ok := projectedEdge(f.Projection, e.Producer, e.Consumer); ok {
						graph.edge(e.Producer, e.Consumer, edge)
					}
				}
			}
			if allowed[EdgeManages] {
				graph.management(session, ov, r)
				for _, c := range ov.Children(r).Children {
					graph.edge(r, c.Ref, relationEdge{edge: EdgeManages})
				}
				for _, m := range ov.Members(r).Members {
					graph.edge(r, m.Ref, relationEdge{edge: EdgeManages})
				}
				graph.crossplane(session, ov, r, obj)
			}
			if allowed[EdgeProtects] {
				graph.healthChecks(ov, r, ov.Children(r))
			}
			if allowed[EdgeExposes] {
				graph.knativeServing(session, r, obj)
			}
		}
		refs := map[string]resourceid.Ref{}
		for r := range graph.nodes {
			refs[familyNodeID(r)] = r
		}
		type candidate struct {
			ref      resourceid.Ref
			priority int
		}
		pending := map[resourceid.Ref]int{}
		frontierSet := make(map[resourceid.Ref]bool, len(frontier))
		for _, r := range frontier {
			frontierSet[r] = true
		}
		for _, e := range graph.topo.Edges {
			if !allowed[e.Type] {
				continue
			}
			source, target := refs[e.Source], refs[e.Target]
			for _, r := range []resourceid.Ref{source, target} {
				if !frontierSet[r] {
					continue
				}
				if rootOnly[e.Type] && r != subject {
					continue
				}
				other := target
				if source != r {
					if target != r {
						continue
					}
					other = source
				}
				if included[other] || !access.allowed(other) {
					continue
				}
				priority := neighborhoodPriority(e.Type, other, target == other)
				if prior, ok := pending[other]; !ok || priority < prior {
					pending[other] = priority
				}
			}
		}
		sorted := make([]candidate, 0, len(pending))
		for r, priority := range pending {
			sorted = append(sorted, candidate{r, priority})
		}
		slices.SortFunc(sorted, func(a, b candidate) int {
			return cmp.Or(cmp.Compare(a.priority, b.priority), strings.Compare(a.ref.Key(), b.ref.Key()))
		})
		var pods []*corev1.Pod
		for _, c := range sorted {
			if c.ref.GroupKind() != (resourceid.GroupKind{Kind: "Pod"}) {
				continue
			}
			obj, read := session.Borrow(c.ref)
			if pod, ok := obj.(*corev1.Pod); ok && read.State == ownership.Present {
				pods = append(pods, pod)
			}
		}
		hopGroups := GroupPods(pods, PodGroupingOptions{JobUIDs: requestSession.jobUIDs(pods, jobUIDs)})
		podGroups := map[resourceid.Ref]*PodGroup{}
		for _, g := range hopGroups.Groups {
			if len(g.Pods) <= DefaultBuildOptions().MaxIndividualPods && !groups[g.Key] {
				continue
			}
			for _, pod := range g.Pods {
				podGroups[resourceid.NewRef("", "Pod", pod.Namespace, pod.Name)] = g
			}
		}
		var next []resourceid.Ref
		for _, c := range sorted {
			r := c.ref
			if included[r] {
				continue
			}
			obj, read := session.Borrow(r)
			if read.State != ownership.Present {
				continue
			}
			if IsCalicoPolicyKind(access.node(r).Kind) && read.Object.Ref != r {
				r = read.Object.Ref
				if included[r] {
					continue
				}
			}
			g := podGroups[r]
			groupIncluded := g != nil && groups[g.Key]
			if len(order)+len(groups) >= limit && !groupIncluded {
				sub.Truncated = true
				continue
			}
			included[r], objects[r] = true, obj
			for _, alias := range access.aliases(r) {
				included[alias] = true
			}
			if g != nil {
				if !groupIncluded {
					groups[g.Key] = true
				}
				grouped[r] = g.Key
			} else {
				order = append(order, r)
			}
			next = append(next, r)
		}
		if len(next) == 0 {
			break
		}
		frontier, unanswered = next, next
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if allowed[EdgeManages] {
		graph.ownerEdges(unanswered, objects)
	}
	displayObjects := make(map[resourceid.Ref]runtime.Object, len(order))
	for _, r := range order {
		displayObjects[r] = objects[r]
	}
	nodes, err := neighborhoodDisplayNodes(displayObjects, provider, dp)
	if err != nil {
		return nil, err
	}
	ids := map[resourceid.Ref]string{}
	for _, r := range order {
		node, ok := nodes[r]
		if !ok {
			node = neighborhoodStandaloneNode(r, objects[r], access.node(r))
		}
		if IsCalicoPolicyKind(node.Kind) {
			node.Data["apiVersions"] = access.node(r).Data["apiVersions"]
		}
		delete(node.Data, "policyCoverageWorkloads")
		delete(node.Data, "policyStatus")
		sub.Nodes = append(sub.Nodes, node)
		ids[r] = node.ID
	}
	var groupedPods []*corev1.Pod
	for r := range grouped {
		if pod, ok := objects[r].(*corev1.Pod); ok {
			groupedPods = append(groupedPods, pod)
		}
	}
	slices.SortFunc(groupedPods, func(a, b *corev1.Pod) int { return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name) })
	displayGroups := GroupPods(groupedPods, PodGroupingOptions{JobUIDs: requestSession.jobUIDs(groupedPods, jobUIDs)}).Groups
	keys := make([]string, 0, len(displayGroups))
	for key := range displayGroups {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		g := displayGroups[key]
		node := CreatePodGroupNode(g, provider)
		sub.Nodes = append(sub.Nodes, node)
		for _, p := range g.Pods {
			ids[resourceid.NewRef("", "Pod", p.Namespace, p.Name)] = node.ID
		}
	}
	for r, id := range ids {
		for _, alias := range access.aliases(r) {
			ids[alias] = id
		}
	}

	seen := map[string]bool{}
	podSources := map[resourceid.Ref][]string{}
	refs := map[string]resourceid.Ref{}
	for r := range graph.nodes {
		refs[familyNodeID(r)] = r
	}
	for _, e := range graph.topo.Edges {
		if !allowed[e.Type] {
			continue
		}
		source, sok := ids[refs[e.Source]]
		target, tok := ids[refs[e.Target]]
		if !sok || !tok || source == target {
			continue
		}
		if r := refs[e.Target]; grouped[r] != "" {
			podSources[r] = append(podSources[r], source)
		}
		e.Source, e.Target = source, target
		e.ID = source + "-" + string(e.Type) + "-" + target
		if !seen[e.ID] {
			seen[e.ID] = true
			sub.Edges = append(sub.Edges, e)
		}
	}
	for _, node := range sub.Nodes {
		// A custom resource of kind PodGroup (a batch scheduler's) has no
		// member Pods; only Radar's own groups do.
		pods, ok := node.Data["pods"].([]map[string]any)
		if node.Kind != KindPodGroup || !ok {
			continue
		}
		sources := make([][]string, len(pods))
		mixed := false
		for i, pod := range pods {
			ns, _ := pod["namespace"].(string)
			name, _ := pod["name"].(string)
			r := resourceid.NewRef("", "Pod", ns, name)
			ids := podSources[r]
			slices.Sort(ids)
			sources[i] = slices.Compact(ids)
			if i > 0 && !slices.Equal(sources[0], sources[i]) {
				mixed = true
			}
		}
		if mixed {
			// Without per-member sources, expanding a mixed group connects
			// every Pod to every source, including another Pod's controller.
			for i, pod := range pods {
				pod["ownerIds"] = append([]string{}, sources[i]...)
			}
		}
	}
	slices.SortFunc(sub.Edges, func(a, b Edge) int { return strings.Compare(a.ID, b.ID) })
	sub.Nodes = stampAuditKeys(sub.Nodes)
	if access.denied {
		sub.RBACDenied = 1
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sub, nil
}

// ownerEdges links objects to the admitted owners their owner references name,
// UID included, from the objects already read. It asks about nothing, so it
// can draw the structure among the last hop's objects, which are never
// answered.
func (n *neighborhood) ownerEdges(refs []resourceid.Ref, objects map[resourceid.Ref]runtime.Object) {
	for _, r := range refs {
		m, err := apimeta.Accessor(objects[r])
		if err != nil {
			continue
		}
		for _, ref := range m.GetOwnerReferences() {
			group := resourceid.GroupFromAPIVersion(ref.APIVersion)
			// A namespaced object's owner may be cluster-scoped.
			for _, ns := range slices.Compact([]string{r.Namespace, ""}) {
				owner := resourceid.NewRef(group, ref.Kind, ns, ref.Name)
				if o, ok := objects[owner]; ok {
					if om, err := apimeta.Accessor(o); err == nil && om.GetUID() == ref.UID {
						n.edge(owner, r, relationEdge{edge: EdgeManages})
					}
					break
				}
			}
		}
	}
}

func neighborhoodDisposition(d relations.Disposition, allowed map[EdgeType]bool) bool {
	for _, r := range d.Rules {
		if allowed[EdgeType(r.Edge)] {
			return true
		}
	}
	return false
}

// neighborhoodPriority orders a hop's candidates for the node budget: what
// manages the frontier object, what exposes or routes to it, its policies, its
// other attachments, then what it manages, and replicas last. downstream: r is
// the edge's target. A controller or GitOps manager can own hundreds of
// objects; they must not crowd out its parent, Service, policy or source.
func neighborhoodPriority(edge EdgeType, r resourceid.Ref, downstream bool) int {
	if r.Group == "" && r.Kind == "Pod" || r.Group == "apps" && r.Kind == "ReplicaSet" && downstream {
		return 5
	}
	switch edge {
	case EdgeManages:
		if downstream {
			return 4
		}
		return 0
	case EdgeExposes, EdgeRoutesTo:
		return 1
	case EdgeProtects:
		return 2
	default:
		return 3
	}
}

type neighborhoodAccess struct {
	catalog   *ownership.Catalog
	allow     func(*Node) bool
	decisions map[resourceid.GroupKind]map[string]ownership.Decision
	denied    bool
}

func (a *neighborhoodAccess) Namespaces() ([]string, bool) { return nil, true }
func (a *neighborhoodAccess) CanRead(gk resourceid.GroupKind, ns string) ownership.Decision {
	if ns == "" {
		if namespaced, known := a.catalog.Scope(gk); known && namespaced {
			return ownership.Undecided
		}
	}
	if a.decisions[gk] == nil {
		a.decisions[gk] = map[string]ownership.Decision{}
	}
	if d, ok := a.decisions[gk][ns]; ok {
		return d
	}
	d := ownership.Allowed
	node := a.node(resourceid.NewRef(gk.Group, gk.Kind, ns, ""))
	if a.allow != nil && !a.allow(&node) {
		d = ownership.Denied
		a.denied = true
	}
	a.decisions[gk][ns] = d
	return d
}
func (a *neighborhoodAccess) allowed(r resourceid.Ref) bool {
	return a.CanRead(r.GroupKind(), r.Namespace) == ownership.Allowed
}
func (a *neighborhoodAccess) node(r resourceid.Ref) Node {
	apiVersion := "v1"
	resource := ""
	if k, ok := a.catalog.Kind(r.GroupKind()); ok {
		resource = k.Resource
		if len(k.Versions) > 0 {
			apiVersion = k.Versions[0]
		}
	} else if b, ok := resourceid.BuiltinForKind(r.Kind); ok && b.Group == r.Group {
		// The typed caches read built-ins discovery doesn't list, and a
		// per-kind gate needs the resource to check.
		resource = b.Resource
		if b.Version != "" {
			apiVersion = b.Version
		}
	}
	if r.Group != "" {
		apiVersion = r.Group + "/" + apiVersion
	}
	kind := NodeKind(pseudoKindFor(r.Kind, r.Group))
	if relations.NodeClasses.IsTarget(r.GroupKind()) {
		kind = KindNodeClass
	}
	n := Node{ID: familyNodeID(r), Kind: kind, Name: r.Name, Status: StatusUnknown, Data: map[string]any{"namespace": r.Namespace, "apiVersion": apiVersion, "resourceKind": r.Kind, "resource": resource}}
	if IsCalicoPolicyKind(kind) {
		var versions []string
		for _, group := range CalicoAPIGroups() {
			if k, ok := a.catalog.Kind(resourceid.GroupKind{Group: group, Kind: r.Kind}); ok {
				for _, v := range k.Versions {
					versions = append(versions, group+"/"+v)
				}
			}
		}
		n.Data["apiVersions"] = versions
	}
	return n
}
func neighborhoodRoots(c *ownership.Catalog, root ResourceRef, dp DynamicProvider) []resourceid.Ref {
	kind := normalizeKindWithGroup(root.Kind, root.Group, dp)
	builtinGroup, builtin := resourceid.BuiltinGroup(kind)
	if b, ok := resourceid.BuiltinForName(kind); ok {
		kind, builtinGroup, builtin = b.Kind, b.Group, true
	}
	// A kind whose scope can't hold the root's namespace is no candidate: its
	// read would be a scope error, not an answer about the root.
	namespaced := root.Namespace != ""
	var out []resourceid.Ref
	if b, ok := resourceid.BuiltinForName(root.Kind); ok && (root.Group == "" || root.Group == b.Group) && b.Namespaced == namespaced {
		out = append(out, resourceid.NewRef(b.Group, b.Kind, root.Namespace, root.Name))
	}
	a := &neighborhoodAccess{catalog: c}
	for _, k := range c.DependentKinds() {
		if root.Group != "" && k.Group != root.Group || k.Namespaced != namespaced {
			continue
		}
		n := a.node(resourceid.NewRef(k.Group, k.Kind, root.Namespace, root.Name))
		matches := strings.EqualFold(k.Kind, kind) || strings.EqualFold(k.Resource, root.Kind) || strings.EqualFold(string(n.Kind), kind)
		if !matches || root.Group == "" && builtin && k.Group != builtinGroup {
			continue
		}
		ref := resourceid.NewRef(k.Group, k.Kind, root.Namespace, root.Name)
		if !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}

func (a *neighborhoodAccess) aliases(r resourceid.Ref) []resourceid.Ref {
	if !IsCalicoPolicyKind(NodeKind(pseudoKindFor(r.Kind, r.Group))) {
		return nil
	}
	var out []resourceid.Ref
	for _, group := range CalicoAPIGroups() {
		alias := resourceid.NewRef(group, r.Kind, r.Namespace, r.Name)
		out = append(out, alias)
	}
	return out
}
