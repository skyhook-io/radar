package topology

import (
	"fmt"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func addCertificateIssuerEdges(nodes []Node, edges []Edge, certificates []unstructured.Unstructured, provider DynamicProvider, opts BuildOptions) ([]Node, []Edge, []string) {
	var refs []declaredDependency
	for i := range certificates {
		cert := &certificates[i]
		if ref, ok := certificateIssuerDependency(cert); ok {
			refs = append(refs, ref)
		}
	}
	return addObservedDependencyEdges(nodes, edges, refs, provider, opts)
}

// certificateIssuerDependency follows the shared cert-manager IssuerReference
// contract. External issuers need discovery-backed scope semantics of their own.
func certificateIssuerDependency(obj *unstructured.Unstructured) (declaredDependency, bool) {
	sourceGroup := resourceid.GroupFromAPIVersion(obj.GetAPIVersion())
	switch obj.GetKind() {
	case "Certificate", "CertificateRequest":
		if sourceGroup != "cert-manager.io" {
			return declaredDependency{}, false
		}
	case "Order", "Challenge":
		if sourceGroup != "acme.cert-manager.io" {
			return declaredDependency{}, false
		}
	default:
		return declaredDependency{}, false
	}
	kind, _, _ := unstructured.NestedString(obj.Object, "spec", "issuerRef", "kind")
	group, _, _ := unstructured.NestedString(obj.Object, "spec", "issuerRef", "group")
	name, _, _ := unstructured.NestedString(obj.Object, "spec", "issuerRef", "name")
	if kind == "" {
		kind = "Issuer"
	}
	if group == "" {
		group = "cert-manager.io"
	}
	if name == "" || group != "cert-manager.io" || kind != "Issuer" && kind != "ClusterIssuer" {
		return declaredDependency{}, false
	}
	namespace := obj.GetNamespace()
	if kind == "ClusterIssuer" {
		namespace = ""
	}
	return declaredDependency{Source: resourceid.NewRef(sourceGroup, obj.GetKind(), obj.GetNamespace(), obj.GetName()), Target: resourceid.NewRef(group, kind, namespace, name), Label: "issuer"}, true
}

// addIssuanceIssuerEdges runs after owned issuance children enter the graph.
// Only represented child kinds drive cache reads, independent of child count.
func (b *Builder) addIssuanceIssuerEdges(nodes []Node, edges []Edge, opts BuildOptions) ([]Node, []Edge, []string) {
	if b.dynamic == nil {
		return nodes, edges, nil
	}
	observedKinds := map[resourceid.GroupKind]bool{}
	represented := map[string]*Node{}
	for i := range nodes {
		node := &nodes[i]
		if !opts.MatchesNamespaceFilter(nodeNamespaceFromData(node)) {
			continue
		}
		switch node.Kind {
		case "CertificateRequest":
			if nodeGroup(node) == "cert-manager.io" {
				observedKinds[resourceid.GroupKind{Group: "cert-manager.io", Kind: "CertificateRequest"}] = true
			}
		case "Order", "Challenge":
			if nodeGroup(node) == "acme.cert-manager.io" {
				observedKinds[resourceid.GroupKind{Group: "acme.cert-manager.io", Kind: string(node.Kind)}] = true
			}
		}
		for _, key := range nodeResourceKeys(node) {
			represented[key] = node
		}

	}
	var refs []declaredDependency
	var warnings []string
	for _, kind := range []resourceid.GroupKind{{Group: "cert-manager.io", Kind: "CertificateRequest"}, {Group: "acme.cert-manager.io", Kind: "Order"}, {Group: "acme.cert-manager.io", Kind: "Challenge"}} {
		if !observedKinds[kind] {
			continue
		}
		gvr, ok := b.dynamic.GetGVRWithGroup(kind.Kind, kind.Group)
		if !ok {
			continue
		}
		objects, err := b.dynamic.ListNamespaces(gvr, opts.Namespaces)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to list issuance references %s: %v", kind.String(), err))
			continue
		}
		for _, obj := range objects {
			if obj == nil || !opts.MatchesNamespaceFilter(obj.GetNamespace()) {
				continue
			}
			if ref, ok := certificateIssuerDependency(obj); ok {
				source := represented[ref.Source.Key()]
				if source == nil || source.uid != "" && source.uid != obj.GetUID() {
					continue
				}
				refs = append(refs, ref)
			}
		}
	}
	before := len(nodes)
	var targetWarnings []string
	nodes, edges, targetWarnings = addObservedDependencyEdges(nodes, edges, refs, b.dynamic, opts)
	warnings = append(warnings, targetWarnings...)
	// Repair ownership only for issuer kinds materialized by this late join.
	// A second global watched-CRD scan would waste work on unrelated resources.
	seen := map[schema.GroupVersionResource]bool{}
	var newIssuerKinds []schema.GroupVersionResource
	for i := before; i < len(nodes); i++ {
		node := &nodes[i]
		gvr, ok := b.dynamic.GetGVRWithGroup(string(node.Kind), nodeGroup(node))
		if ok && !seen[gvr] {
			seen[gvr] = true
			newIssuerKinds = append(newIssuerKinds, gvr)
		}
	}
	if len(newIssuerKinds) > 0 {
		nodes, edges = b.addGenericCRDNodesForResources(nodes, edges, opts, newIssuerKinds)
	}
	return nodes, edges, warnings
}
