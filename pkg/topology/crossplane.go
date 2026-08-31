package topology

import (
	"fmt"
	"log"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Crossplane relationships are spec-shape detected, not kind-enumerated:
// Managed Resources, Composites (XRs) and Claims each have provider-defined
// CRD kinds (one CRD per provider service), so there is no fixed kind list to
// key off. The generic owner-reference pass (addGenericCRDNodes) does not wire
// them because Claims have no ownerReference and XRs reference their Claim via
// spec.claimRef rather than an ownerReference — so the composed MRs have no
// owner node to attach to. This step adds the Claim/XR/MR nodes and the
// spec-ref-driven edges directly.
//
// Detection mirrors the frontend accessors in resource-utils-crossplane.ts and
// the audit walker in internal/audit/runner.go so all three agree on what
// counts as a Crossplane resource. v1↔v2 path handling (spec.crossplane.x
// first, fall back to spec.x) lives here.

// Data keys recording a cluster-scoped Crossplane node's exact GVR, so the
// topology RBAC strip can authorize it by (group, resource) — the same shape
// NodeClass uses. Only set on cluster-scoped (empty-namespace) nodes.
const (
	clusterScopedResourceKey = "clusterScopedResource"
	clusterScopedGroupKey    = "clusterScopedGroup"
)

// ClusterScopedDynamicRBACTuples returns the distinct (group, resource) tuples
// of cluster-scoped dynamic nodes (Crossplane XRs/MRs) present in the graph.
// These provider CRD kinds are unbounded, so they cannot be covered by the
// fixed ClusterScopedKinds denylist; the caller SARs each tuple and passes the
// authorized set to StripClusterScopedDynamicExcept.
func (t *Topology) ClusterScopedDynamicRBACTuples() []SARTuple {
	if t == nil {
		return nil
	}
	seen := make(map[SARTuple]bool)
	var tuples []SARTuple
	for i := range t.Nodes {
		tuple, ok := clusterScopedDynamicTuple(&t.Nodes[i])
		if !ok || seen[tuple] {
			continue
		}
		seen[tuple] = true
		tuples = append(tuples, tuple)
	}
	return tuples
}

// StripClusterScopedDynamicExcept removes cluster-scoped dynamic nodes whose
// (group, resource) is not in allowed. A node that records the cluster-scoped
// GVR keys but whose tuple isn't allowed is dropped (fail closed).
func (t *Topology) StripClusterScopedDynamicExcept(allowed map[SARTuple]bool) {
	if t == nil {
		return
	}
	deny := make(map[string]bool)
	for i := range t.Nodes {
		tuple, ok := clusterScopedDynamicTuple(&t.Nodes[i])
		if ok && !allowed[tuple] {
			deny[t.Nodes[i].ID] = true
		}
	}
	if len(deny) > 0 {
		t.StripNodeIDs(deny)
	}
}

// clusterScopedDynamicTuple extracts the recorded GVR tuple of a cluster-scoped
// dynamic node, or false if the node isn't one.
func clusterScopedDynamicTuple(n *Node) (SARTuple, bool) {
	if n == nil {
		return SARTuple{}, false
	}
	resource, _ := n.Data[clusterScopedResourceKey].(string)
	if resource == "" {
		return SARTuple{}, false
	}
	group, _ := n.Data[clusterScopedGroupKey].(string)
	return SARTuple{Group: group, Resource: resource, Namespace: ""}, true
}

// isCrossplaneMR reports whether u is a Managed Resource. An MR always carries
// a providerConfigRef (v1: spec.providerConfigRef, v2: spec.crossplane.providerConfigRef).
func isCrossplaneMR(u *unstructured.Unstructured) bool {
	if u == nil {
		return false
	}
	spec, ok := u.Object["spec"].(map[string]interface{})
	if !ok {
		return false
	}
	if _, ok := spec["providerConfigRef"].(map[string]interface{}); ok {
		return true
	}
	if cp, ok := spec["crossplane"].(map[string]interface{}); ok {
		if _, ok := cp["providerConfigRef"].(map[string]interface{}); ok {
			return true
		}
	}
	return false
}

// isCrossplaneComposite reports whether u is a Composite (XR) or a v1 Claim.
// XRs expose spec.resourceRefs (v1) or spec.crossplane.resourceRefs (v2); v1
// Claims expose singular spec.resourceRef + spec.compositionRef. MRs are
// excluded — they share the same CRD group set and are discriminated by shape.
func isCrossplaneComposite(u *unstructured.Unstructured) bool {
	if u == nil || isCrossplaneMR(u) {
		return false
	}
	spec, ok := u.Object["spec"].(map[string]interface{})
	if !ok {
		return false
	}
	if _, ok := spec["resourceRefs"].([]interface{}); ok {
		return true
	}
	if cp, ok := spec["crossplane"].(map[string]interface{}); ok {
		if _, ok := cp["resourceRefs"].([]interface{}); ok {
			return true
		}
	}
	if _, hasRef := spec["resourceRef"].(map[string]interface{}); hasRef {
		if _, hasComp := spec["compositionRef"].(map[string]interface{}); hasComp {
			return true
		}
	}
	return false
}

// crossplaneRef is a resolved composed-resource / bound-XR reference.
type crossplaneRef struct {
	group     string
	kind      string
	namespace string // may be empty: cluster-scoped, or "same namespace as the referrer"
	name      string
}

func groupFromAPIVersion(apiVersion string) string {
	if i := strings.IndexByte(apiVersion, '/'); i >= 0 {
		return apiVersion[:i]
	}
	return ""
}

func refFromMap(m map[string]interface{}) (crossplaneRef, bool) {
	kind, _ := m["kind"].(string)
	name, _ := m["name"].(string)
	if kind == "" || name == "" {
		return crossplaneRef{}, false
	}
	apiVersion, _ := m["apiVersion"].(string)
	namespace, _ := m["namespace"].(string)
	return crossplaneRef{group: groupFromAPIVersion(apiVersion), kind: kind, namespace: namespace, name: name}, true
}

// getBoundXRRef returns the XR a v1 Claim is bound to (spec.resourceRef,
// singular). Gated on spec.compositionRef so an unrelated CRD that happens to
// carry a resourceRef isn't treated as a Claim.
func getBoundXRRef(u *unstructured.Unstructured) (crossplaneRef, bool) {
	if u == nil {
		return crossplaneRef{}, false
	}
	spec, ok := u.Object["spec"].(map[string]interface{})
	if !ok {
		return crossplaneRef{}, false
	}
	if _, hasComp := spec["compositionRef"].(map[string]interface{}); !hasComp {
		return crossplaneRef{}, false
	}
	ref, ok := spec["resourceRef"].(map[string]interface{})
	if !ok {
		return crossplaneRef{}, false
	}
	return refFromMap(ref)
}

// getCrossplaneResourceRefs returns the composed-resource references of an XR
// (spec.crossplane.resourceRefs first, falling back to spec.resourceRefs).
func getCrossplaneResourceRefs(u *unstructured.Unstructured) []crossplaneRef {
	if u == nil {
		return nil
	}
	spec, ok := u.Object["spec"].(map[string]interface{})
	if !ok {
		return nil
	}
	raw, _ := spec["resourceRefs"].([]interface{})
	if cp, ok := spec["crossplane"].(map[string]interface{}); ok {
		if cpRefs, ok := cp["resourceRefs"].([]interface{}); ok {
			raw = cpRefs
		}
	}
	var refs []crossplaneRef
	for _, item := range raw {
		if m, ok := item.(map[string]interface{}); ok {
			if ref, ok := refFromMap(m); ok {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

// crossplaneIndexKey identifies a Crossplane node by group/kind/namespace/name.
// Namespace is part of the key so same-named resources in different namespaces
// don't collide. References often omit the namespace (a namespaced XR composes
// in its own namespace; a v1 Claim's bound XR is cluster-scoped), so lookups try
// candidate namespaces — see resolveRef.
func crossplaneIndexKey(group, kind, namespace, name string) string {
	return strings.ToLower(group) + "/" + strings.ToLower(kind) + "/" + namespace + "/" + name
}

// addCrossplaneNodes adds Managed Resource / Composite (XR) / Claim nodes and
// the manages edges between them:
//
//	Claim  --manages--> bound XR   (via spec.resourceRef)
//	XR     --manages--> composed MR (via spec.resourceRefs / spec.crossplane.resourceRefs)
//
// It runs before addGenericCRDNodes so the XR nodes exist first; nodes already
// present are not duplicated. Only resources whose informers are already
// watched are seen (same trade-off as the generic CRD pass and the audit walker).
func (b *Builder) addCrossplaneNodes(nodes []Node, edges []Edge, opts BuildOptions) ([]Node, []Edge) {
	dynamicCache := b.dynamic
	if dynamicCache == nil {
		return nodes, edges
	}

	existingIDs := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		existingIDs[n.ID] = true
	}

	type xpObject struct {
		obj    *unstructured.Unstructured
		nodeID string
	}
	// index: group/kind/name -> node ID (namespace recovered from the node ID)
	index := make(map[string]string)
	var objects []xpObject

	for _, gvr := range dynamicCache.GetWatchedResources() {
		kind := dynamicCache.GetKindForGVR(gvr)
		if kind == "" || !isCRDGVR(dynamicCache, gvr, kind) {
			continue
		}
		resources, err := dynamicCache.ListNamespaces(gvr, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list %s resources for Crossplane edges: %v", kind, err)
			continue
		}
		for _, r := range resources {
			if !isCrossplaneMR(r) && !isCrossplaneComposite(r) {
				continue
			}
			ns := r.GetNamespace()
			// Cluster-scoped resources (empty namespace) are exempt from the
			// namespace filter — a v1 XR/MR is global and would otherwise be
			// dropped whenever a filter is set, breaking the Claim->XR->composed
			// chain. Mirrors the Karpenter/CAPI cluster-scoped handling.
			if ns != "" && !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := r.GetName()
			group := groupFromAPIVersion(r.GetAPIVersion())
			nodeID := fmt.Sprintf("%s/%s/%s/%s", strings.ToLower(kind), ns, name, group)
			objects = append(objects, xpObject{obj: r, nodeID: nodeID})
			index[crossplaneIndexKey(group, kind, ns, name)] = nodeID

			if existingIDs[nodeID] {
				continue
			}
			existingIDs[nodeID] = true
			data := map[string]any{
				"namespace":  ns,
				"labels":     r.GetLabels(),
				"apiVersion": r.GetAPIVersion(),
			}
			// A cluster-scoped Crossplane resource (XR/MR) is not covered by the
			// fixed cluster-scoped RBAC denylist — provider CRD kinds are
			// unbounded. Record its exact GVR so the topology RBAC strip can SAR
			// the caller's list access and drop it when unauthorized. Mirrors how
			// NodeClass records its provider resource.
			if ns == "" {
				data[clusterScopedResourceKey] = gvr.Resource
				data[clusterScopedGroupKey] = gvr.Group
			}
			nodes = append(nodes, Node{
				ID:     nodeID,
				Kind:   NodeKind(kind),
				Name:   name,
				Status: extractGenericStatus(r),
				Data:   data,
			})
		}
	}

	edgeSeen := make(map[string]bool)
	addEdge := func(src, dst string) {
		if src == "" || dst == "" || src == dst {
			return
		}
		id := fmt.Sprintf("%s-to-%s", src, dst)
		if edgeSeen[id] {
			return
		}
		edgeSeen[id] = true
		edges = append(edges, Edge{ID: id, Source: src, Target: dst, Type: EdgeManages})
	}

	// resolveRef finds the node ID a reference points at. References usually
	// omit the namespace, so candidates are tried in order: the ref's explicit
	// namespace if set; otherwise cluster-scoped ("") — a v1 Claim's bound XR is
	// cluster-scoped — then the referrer's own namespace — a namespaced XR
	// composes in its own namespace.
	resolveRef := func(referrerNS string, ref crossplaneRef) (string, bool) {
		var candidates []string
		if ref.namespace != "" {
			candidates = []string{ref.namespace}
		} else {
			candidates = []string{"", referrerNS}
		}
		for _, ns := range candidates {
			if id, ok := index[crossplaneIndexKey(ref.group, ref.kind, ns, ref.name)]; ok {
				return id, true
			}
		}
		return "", false
	}

	for _, o := range objects {
		referrerNS := o.obj.GetNamespace()
		if ref, ok := getBoundXRRef(o.obj); ok {
			if target, ok := resolveRef(referrerNS, ref); ok {
				addEdge(o.nodeID, target)
			}
		}
		for _, ref := range getCrossplaneResourceRefs(o.obj) {
			if target, ok := resolveRef(referrerNS, ref); ok {
				addEdge(o.nodeID, target)
			}
		}
	}

	return nodes, edges
}
