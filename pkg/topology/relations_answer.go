package topology

import (
	"slices"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// relationEdge is how the relationship graph draws one family's
// declarations: the edge type, and whether the edge runs from what is
// declared to what declares it, as a ConfigMap configures the Pod mounting it.
type relationEdge struct {
	edge    EdgeType
	reverse bool
	label   string
	partial bool
}

// familyEdges and selectorEdges are generated from each producer's declared
// field disposition. The graph projection tests check both endpoints.
var familyEdges = func() map[string]relations.Disposition {
	out := map[string]relations.Disposition{}
	for _, f := range relations.Families("") {
		out[f.Name] = f.Projection
	}
	return out
}()
var selectorEdges = func() map[string]relations.Disposition {
	out := map[string]relations.Disposition{}
	for _, f := range relations.SelectorFamilies() {
		out[f.Name] = f.Projection
	}
	return out
}()

func projectedEdge(d relations.Disposition, source, target resourceid.Ref) (relationEdge, bool) {
	for _, r := range d.Rules {
		if r.Matches(source.GroupKind(), target.GroupKind()) {
			return relationEdge{edge: EdgeType(r.Edge), reverse: r.Reverse, label: r.Label, partial: r.Partial}, true
		}
	}
	return relationEdge{}, false
}

func edgeFor(family string, source, target resourceid.Ref) (relationEdge, bool) {
	return projectedEdge(familyEdges[family], source, target)
}

func routingDisposition(d relations.Disposition) bool {
	for _, r := range d.Rules {
		if r.Edge == string(EdgeExposes) || r.Edge == string(EdgeRoutesTo) {
			return true
		}
	}
	return false
}

func (n *neighborhood) projectFamily(session relations.Session, name string, target resourceid.Ref) bool {
	if name != relations.FluxHelmOrigins.Name {
		return true
	}
	obj, read := n.borrow(session, target)
	u, ok := obj.(*unstructured.Unstructured)
	return read.State == ownership.Present && ok && gitops.FluxTargetsLocalCluster(u) && !gitops.FluxMembersReadable(u)
}

// neighborhood is the part of a relationship graph around one subject that
// the families answer.
type neighborhood struct {
	topo     Topology
	nodes    map[resourceid.Ref]bool
	edges    map[string]bool
	coverage map[string]relations.Coverage
	access   ownership.Access
}

// familyNodeID is a node ID parseNodeID can't read, so a node's identity
// comes from its kind and data, as for a cluster-scoped graph node.
func familyNodeID(r resourceid.Ref) string {
	return "rel|" + strings.ReplaceAll(resourceid.ResourceKey(r.Group, r.Kind, r.Namespace, r.Name), "/", "|")
}

func (n *neighborhood) node(r resourceid.Ref) string {
	id := familyNodeID(r)
	if !n.nodes[r] {
		n.nodes[r] = true
		apiVersion := "v1"
		if r.Group != "" {
			apiVersion = r.Group + "/v1"
		}
		n.topo.Nodes = append(n.topo.Nodes, Node{ID: id, Kind: NodeKind(r.Kind), Name: r.Name, Data: map[string]any{"namespace": r.Namespace, "apiVersion": apiVersion}})
	}
	return id
}

func (n *neighborhood) edge(from, to resourceid.Ref, e relationEdge, partial bool) {
	if !n.allowed(from) || !n.allowed(to) {
		return
	}
	source, target := from, to
	if e.reverse {
		source, target = to, from
	}
	s, t := n.node(source), n.node(target)
	id := s + "-" + string(e.edge) + "-" + t
	if n.edges[id] {
		return
	}
	n.edges[id] = true
	n.topo.Edges = append(n.topo.Edges, Edge{ID: id, Source: s, Target: t, Type: e.edge, Label: e.label, Partial: partial || e.partial})
}

func (n *neighborhood) cover(name string, c relations.Coverage) {
	if c.Complete() && !c.Narrowed {
		return
	}
	prior := n.coverage[name]
	prior.NotChecked = append(prior.NotChecked, c.NotChecked...)
	prior.Narrowed = prior.Narrowed || c.Narrowed
	prior.Truncated = prior.Truncated || c.Truncated
	prior.Total = max(prior.Total, c.Total)
	n.coverage[name] = prior
}

// answer adds every family's answers about subject: what it declares and
// selects, and what declares and selects it. routingOnly keeps the answers
// that reach subject by routing, which is all a Service's entrypoints need.
func (n *neighborhood) answer(session relations.Session, v *relations.View, families []*relations.Family, selectors []*relations.SelectorFamily, subject resourceid.Ref, obj runtime.Object, routingOnly bool) {
	gk := subject.GroupKind()
	for _, f := range families {
		disposition := familyEdges[f.Name]
		if len(disposition.Rules) == 0 || !f.Covers(gk) || routingOnly && !routingDisposition(disposition) {
			continue
		}
		refs := v.References(f, subject, obj)
		n.cover(f.Name, refs.Coverage)
		for _, d := range refs.Declared {
			if d.State == relations.TargetPresent {
				if e, ok := edgeFor(f.Name, subject, d.Ref); ok {
					if n.projectFamily(session, f.Name, d.Ref) {
						n.edge(subject, d.Ref, e, false)
					}
				}
			}
		}
		referrers := v.Referrers(f, subject, obj)
		n.cover(f.Name, referrers.Coverage)
		for _, r := range referrers.Referrers {
			if e, ok := edgeFor(f.Name, r.Ref, subject); ok {
				if n.projectFamily(session, f.Name, subject) {
					n.edge(r.Ref, subject, e, false)
				}
			}
		}
	}
	for _, f := range selectors {
		disposition := selectorEdges[f.Name]
		if len(disposition.Rules) == 0 || !f.Covers(gk) || routingOnly && !routingDisposition(disposition) {
			continue
		}
		chosen := v.Selected(f, subject, obj)
		n.cover(f.Name, chosen.Coverage)
		for _, m := range chosen.Matches {
			if e, ok := projectedEdge(disposition, subject, m); ok {
				n.edge(subject, m, e, false)
			}
		}
		by := v.SelectedBy(f, subject, obj)
		n.cover(f.Name, by.Coverage)
		for _, m := range by.Matches {
			if e, ok := projectedEdge(disposition, m, subject); ok {
				n.edge(m, subject, e, false)
			}
		}
	}
}

// RelationsAnswer projects caller-readable family and ownership answers onto
// Related Resources. Traverse is used only for intermediate networking hops;
// all emitted references, including ownership, use access. Coverage is keyed
// by family and includes only incomplete answers.
func RelationsAnswer(session relations.Session, access, traverse ownership.Access, ov *ownership.View, subject resourceid.Ref, obj runtime.Object, istioDomain string, provider ResourceProvider, dp DynamicProvider) (*Relationships, map[string]relations.Coverage) {
	if obj == nil || access.CanRead(subject.GroupKind(), subject.Namespace) != ownership.Allowed {
		return &Relationships{}, nil
	}
	v := relations.NewView(session, access)
	n := &neighborhood{nodes: map[resourceid.Ref]bool{}, edges: map[string]bool{}, coverage: map[string]relations.Coverage{}, access: access}
	families, selectors := relations.Families(istioDomain), relations.SelectorFamilies()
	n.node(subject)
	n.answer(session, v, families, selectors, subject, obj, false)
	for _, f := range relations.Compositions() {
		joined := v.Composed(f, subject, obj)
		n.cover(f.Name, joined.Coverage)
		for _, edge := range joined.Edges {
			if e, ok := projectedEdge(f.Projection, edge.Producer, edge.Consumer); ok {
				n.edge(edge.Producer, edge.Consumer, e, false)
			}
		}
	}

	n.management(session, ov, subject)
	children := ov.Children(subject)
	for _, c := range children.Children {
		n.edge(subject, c.Ref, relationEdge{edge: EdgeManages}, false)
	}
	n.healthChecks(ov, subject, children)
	n.crossplane(session, ov, subject, obj)
	n.knativeServing(session, subject, obj)

	serviceKind := resourceid.GroupKind{Kind: "Service"}
	for _, e := range n.topo.Edges {
		if e.Type != EdgeExposes || e.Target != familyNodeID(subject) {
			continue
		}
		for r := range n.nodes {
			if familyNodeID(r) == e.Source && r.GroupKind() == serviceKind && r != subject {
				n.answer(session, v, families, selectors, r, nil, true)
				n.knativeServing(session, r, nil)
			}
		}
	}

	n.fillNodeData(session, istioDomain)
	provider = &relationDirectProvider{ResourceProvider: provider, session: session, n: n}
	rel := getRelationshipsWithObject(subject.Kind, subject.Namespace, subject.Name, obj, &n.topo, provider, dp, nil, n.allowedRef)
	if rel == nil {
		rel = &Relationships{}
	}
	n.networkEntrypoints(session, access, traverse, subject, obj, istioDomain, rel)
	if rel != nil && subject.Kind == "Deployment" && subject.Group == "apps" {
		// The graph's shortcut to a Deployment's Pods, through its ReplicaSets.
		for _, rs := range children.Children {
			if rs.Ref.Kind != "ReplicaSet" || rs.Ref.Group != "apps" {
				continue
			}
			for _, pod := range ov.Children(rs.Ref).Children {
				if pod.Ref.Kind == "Pod" && pod.Ref.Group == "" {
					rel.Pods = appendResourceRef(rel.Pods, refOf(pod.Ref))
				}
			}
		}
	}
	if m, err := apimeta.Accessor(obj); err == nil {
		if meta := ManagedByFromMeta(m); meta != nil {
			// Named by the subject's own labels and annotations, which the
			// caller reads: a declared name, like a spec reference, shown
			// whether or not the caller may read the manager itself.
			rel.ManagedBy = []ResourceRef{*meta}
		} else if top := n.topManager(session, ov, subject); top != nil {
			rel.ManagedBy = []ResourceRef{*top}
		} else {
			rel.ManagedBy = n.ownManagers(rel, ov, subject)
		}
	}
	return rel, n.coverage
}

// topManager climbs from subject while something readable is above it: the
// top of its controller chain, else an owner reference that isn't a
// controller's (a CiliumEndpoint's Pod, a CiliumNode's Node), else a
// NodeClaim's NodePool. It stops below GitOps managers: what manages a
// workload's Application is ownership.manager's answer, not this one.
func (n *neighborhood) topManager(session relations.Session, ov *ownership.View, subject resourceid.Ref) *ResourceRef {
	var last *ResourceRef
	seen := map[resourceid.Ref]bool{subject: true}
	for cur := subject; len(seen) <= 16; {
		next, ok := n.above(session, ov, cur)
		if !ok || seen[next] {
			break
		}
		r := refOf(next)
		if !n.allowedRef(r) {
			break
		}
		seen[next], last, cur = true, &r, next
	}
	return last
}

func (n *neighborhood) above(session relations.Session, ov *ownership.View, r resourceid.Ref) (resourceid.Ref, bool) {
	if top := ChainOf(ov.ControllerChain(r)).Top(); top != nil {
		if id := refIdentity(*top); id != r {
			return id, true
		}
	}
	_, owners := ov.References(r)
	var found []resourceid.Ref
	for _, owner := range owners {
		if owner.State == ownership.OwnerFound {
			found = append(found, owner.Ref)
		}
	}
	if len(found) > 0 {
		slices.SortFunc(found, func(a, b resourceid.Ref) int { return strings.Compare(a.Key(), b.Key()) })
		return found[0], true
	}
	return n.nodePool(session, r)
}

// ownManagers is what manages an object nothing owns: the controllers that
// generate it from their spec (a Certificate's Secret, a NodePool's Node),
// else the GitOps manager that records it, when exactly one does.
func (n *neighborhood) ownManagers(rel *Relationships, ov *ownership.View, subject resourceid.Ref) []ResourceRef {
	var out []ResourceRef
	if generators, _ := OtherManagingControllers(rel, ov, subject); len(generators) > 0 {
		for _, g := range generators {
			if n.allowedRef(g) {
				out = append(out, g)
			}
		}
		return out
	}
	answer := ov.Manager(subject)
	if answer.Lead == nil || answer.State != ownership.ManagerAgree && answer.State != ownership.ManagerListed {
		return nil
	}
	if lead := refOf(*answer.Lead); n.allowedRef(lead) {
		return []ResourceRef{lead}
	}
	return nil
}

func (n *neighborhood) management(session relations.Session, ov *ownership.View, r resourceid.Ref) {
	manages := relationEdge{edge: EdgeManages}
	_, owners := ov.References(r)
	for _, owner := range owners {
		if owner.State == ownership.OwnerFound {
			n.edge(owner.Ref, r, manages, false)
		}
	}
	for _, manager := range ov.RecordingManagers(r).Managers {
		n.edge(manager.Ref, r, manages, false)
	}
	if pool, ok := n.nodePool(session, r); ok {
		n.edge(pool, r, manages, false)
	}
	if r.GroupKind() == (resourceid.GroupKind{Group: karpenter.Group, Kind: "NodePool"}) {
		provider := relationDirectProvider{session: session, n: n}
		claims, _ := provider.objects(resourceid.GroupKind{Group: karpenter.Group, Kind: "NodeClaim"})
		for _, obj := range claims {
			m, err := apimeta.Accessor(obj)
			if err != nil {
				continue
			}
			claim := resourceid.NewRef(karpenter.Group, "NodeClaim", "", m.GetName())
			if pool, ok := n.nodePool(session, claim); ok && pool == r {
				n.edge(r, claim, manages, false)
			}
		}
	}
}

// nodePool is a Karpenter NodeClaim's NodePool, by karpenter's own rule: its
// owner reference, or its nodepool label.
func (n *neighborhood) nodePool(session relations.Session, r resourceid.Ref) (resourceid.Ref, bool) {
	if r.GroupKind() != (resourceid.GroupKind{Group: karpenter.Group, Kind: "NodeClaim"}) {
		return resourceid.Ref{}, false
	}
	obj, read := n.borrow(session, r)
	claim, err := apimeta.Accessor(obj)
	if read.State != ownership.Present || err != nil {
		return resourceid.Ref{}, false
	}
	pools := map[string]*unstructured.Unstructured{}
	names := []string{claim.GetLabels()[karpenter.NodePoolLabelKey]}
	for _, o := range claim.GetOwnerReferences() {
		names = append(names, o.Name)
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		if p, got := n.borrow(session, resourceid.NewRef(karpenter.Group, "NodePool", "", name)); got.State == ownership.Present {
			if u, ok := p.(*unstructured.Unstructured); ok {
				pools[name] = u
			}
		}
	}
	if pool, _ := karpenter.ResolveNodePoolForClaim(claim, pools); pool != nil {
		return resourceid.NewRef(karpenter.Group, "NodePool", "", pool.GetName()), true
	}
	return resourceid.Ref{}, false
}

// healthChecks are the MachineHealthChecks that protect a Cluster API
// Cluster, found through the owner references Cluster API sets on them.
func (n *neighborhood) healthChecks(ov *ownership.View, subject resourceid.Ref, children ownership.ChildrenAnswer) {
	const capi = "cluster.x-k8s.io"
	mhc := resourceid.GroupKind{Group: capi, Kind: "MachineHealthCheck"}
	switch subject.GroupKind() {
	case resourceid.GroupKind{Group: capi, Kind: "Cluster"}:
		for _, c := range children.Children {
			if c.Ref.GroupKind() == mhc {
				n.edge(c.Ref, subject, relationEdge{edge: EdgeProtects}, false)
			}
		}
	case mhc:
		_, owners := ov.References(subject)
		for _, o := range owners {
			if o.State == ownership.OwnerFound && o.Ref.GroupKind() == (resourceid.GroupKind{Group: capi, Kind: "Cluster"}) {
				n.edge(subject, o.Ref, relationEdge{edge: EdgeProtects}, false)
			}
		}
	}
}

// crossplane adds a Crossplane object's manages links, as addCrossplaneNodes
// draws them: a Claim's bound composite and a composite's composed
// resources, read forward from the subject; and, for a composed resource or a
// bound composite, the owner or claim that names it, read from that object.
func (n *neighborhood) crossplane(session relations.Session, ov *ownership.View, subject resourceid.Ref, obj runtime.Object) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		borrowed, read := n.borrow(session, subject)
		if read.State != ownership.Present {
			return
		}
		if u, ok = borrowed.(*unstructured.Unstructured); !ok {
			return
		}
	}
	exists := func(r resourceid.Ref) bool { return n.present(session, r) }
	resolve := func(referrerNS string, ref crossplaneRef) (resourceid.Ref, bool) {
		candidates := []string{ref.namespace}
		if ref.namespace == "" {
			candidates = []string{"", referrerNS}
		}
		for _, ns := range candidates {
			if r := resourceid.NewRef(ref.group, ref.kind, ns, ref.name); exists(r) {
				return r, true
			}
		}
		return resourceid.Ref{}, false
	}
	names := func(referrer *unstructured.Unstructured, target resourceid.Ref) bool {
		refs := getCrossplaneResourceRefs(referrer)
		if bound, ok := getBoundXRRef(referrer); ok {
			refs = append(refs, bound)
		}
		for _, ref := range refs {
			if r, ok := resolve(referrer.GetNamespace(), ref); ok && r == target {
				return true
			}
		}
		return false
	}
	manages := relationEdge{edge: EdgeManages}
	if isCrossplaneComposite(u) {
		if ref, ok := getBoundXRRef(u); ok {
			if r, ok := resolve(u.GetNamespace(), ref); ok {
				n.edge(subject, r, manages, false)
			}
		}
		for _, ref := range getCrossplaneResourceRefs(u) {
			if r, ok := resolve(u.GetNamespace(), ref); ok {
				n.edge(subject, r, manages, false)
			}
		}
	}
	if !isCrossplaneMR(u) && !isCrossplaneComposite(u) {
		return
	}
	var referrers []resourceid.Ref
	_, owners := ov.References(subject)
	for _, o := range owners {
		if o.State == ownership.OwnerFound {
			referrers = append(referrers, o.Ref)
		}
	}
	if claim, found, _ := unstructured.NestedMap(u.Object, "spec", "claimRef"); found {
		if ref, ok := refFromMap(claim); ok {
			if r, ok := resolve(u.GetNamespace(), ref); ok {
				referrers = append(referrers, r)
			}
		}
	}
	for _, r := range referrers {
		borrowed, read := n.borrow(session, r)
		if read.State != ownership.Present {
			continue
		}
		if referrer, ok := borrowed.(*unstructured.Unstructured); ok && names(referrer, subject) {
			n.edge(r, subject, manages, false)
		}
	}
}

// knativeServing adds the Services a Knative Service is reached through, as
// the topology builder draws them: the same-named core Service its Route
// owns, which exposes it, and the private Service of each Revision its status
// sends traffic to (or its latest ready one), which it exposes. From a core
// Service's side the Knative Service is found by the Route that owns it, or
// for a private Service by its Revision's service label.
func (n *neighborhood) knativeServing(session relations.Session, subject resourceid.Ref, obj runtime.Object) {
	const serving = "serving.knative.dev"
	read := func(r resourceid.Ref) (*unstructured.Unstructured, bool) {
		o, got := n.borrow(session, r)
		u, ok := o.(*unstructured.Unstructured)
		return u, ok && got.State == ownership.Present
	}
	privateRevisions := func(ksvc *unstructured.Unstructured) []string {
		var names []string
		traffic, _, _ := unstructured.NestedSlice(ksvc.Object, "status", "traffic")
		for _, t := range traffic {
			if m, ok := t.(map[string]any); ok {
				if name, _ := m["revisionName"].(string); name != "" {
					names = append(names, name)
				}
			}
		}
		if len(traffic) == 0 {
			if latest, _, _ := unstructured.NestedString(ksvc.Object, "status", "latestReadyRevisionName"); latest != "" {
				names = append(names, latest)
			}
		}
		return names
	}
	frontDoor := func(svc resourceid.Ref) bool {
		o, got := n.borrow(session, svc)
		m, err := apimeta.Accessor(o)
		if got.State != ownership.Present || err != nil {
			return false
		}
		for _, ref := range m.GetOwnerReferences() {
			if ref.Kind == "Route" && resourceid.GroupFromAPIVersion(ref.APIVersion) == serving && ref.Name == svc.Name {
				return true
			}
		}
		return false
	}
	exposes := relationEdge{edge: EdgeExposes}
	switch subject.GroupKind() {
	case resourceid.GroupKind{Group: serving, Kind: "Service"}:
		ksvc, ok := obj.(*unstructured.Unstructured)
		if !ok {
			if ksvc, ok = read(subject); !ok {
				return
			}
		}
		if svc := resourceid.NewRef("", "Service", subject.Namespace, subject.Name); frontDoor(svc) {
			n.edge(svc, subject, exposes, false)
		}
		for _, rev := range privateRevisions(ksvc) {
			if svc := resourceid.NewRef("", "Service", subject.Namespace, rev+"-private"); n.present(session, svc) {
				n.edge(subject, svc, exposes, false)
			}
		}
	case resourceid.GroupKind{Kind: "Service"}:
		ksvcRef := resourceid.NewRef(serving, "Service", subject.Namespace, subject.Name)
		if frontDoor(subject) && n.present(session, ksvcRef) {
			n.edge(subject, ksvcRef, exposes, false)
		}
		rev, private := strings.CutSuffix(subject.Name, "-private")
		if !private {
			return
		}
		revision, ok := read(resourceid.NewRef(serving, "Revision", subject.Namespace, rev))
		if !ok {
			return
		}
		ksvcRef = resourceid.NewRef(serving, "Service", subject.Namespace, revision.GetLabels()["serving.knative.dev/service"])
		if ksvc, ok := read(ksvcRef); ok && slices.Contains(privateRevisions(ksvc), rev) {
			n.edge(ksvcRef, subject, exposes, false)
		}
	}
}

func (n *neighborhood) fillNodeData(session relations.Session, istioDomain string) {
	for i := range n.topo.Nodes {
		node := &n.topo.Nodes[i]
		ref := resourceRefForNode(node, nil)
		if ref == nil {
			continue
		}
		r := refIdentity(*ref)
		if r.Group == "networking.istio.io" && (r.Kind == "VirtualService" || r.Kind == "DestinationRule") {
			node.Data["istioServiceDomain"] = istioDomain
		}
		if r.Group == "" && (r.Kind == "ConfigMap" || r.Kind == "Secret") {
			obj, read := n.borrow(session, r)
			if read.State == ownership.Present {
				if m, err := apimeta.Accessor(obj); err == nil {
					node.Data["resourceVersion"] = m.GetResourceVersion()
				}
			}
		}
	}
}
