package topology

import (
	"fmt"
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func addCertificateIssuerEdges(nodes []Node, edges []Edge, certificates []unstructured.Unstructured, provider DynamicProvider, opts BuildOptions) ([]Node, []Edge, []string) {
	if provider == nil || len(certificates) == 0 {
		return nodes, edges, nil
	}
	byResource := make(map[string]string, len(nodes))
	for i := range nodes {
		for _, key := range nodeResourceKeys(&nodes[i]) {
			byResource[key] = nodes[i].ID
		}
	}
	issuers := map[string]*unstructured.Unstructured{}
	resources := map[string]string{}
	var warnings []string
	for _, kind := range []string{"Issuer", "ClusterIssuer"} {
		gvr, ok := provider.GetGVRWithGroup(kind, "cert-manager.io")
		if !ok {
			continue
		}
		namespaces := opts.Namespaces
		if kind == "ClusterIssuer" {
			namespaces = nil
		}
		objects, err := provider.ListNamespaces(gvr, namespaces)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to list cert-manager %s resources: %v", kind, err))
			continue
		}
		resources[kind] = gvr.Resource
		for _, obj := range objects {
			if obj.GetNamespace() != "" && !opts.MatchesNamespaceFilter(obj.GetNamespace()) {
				continue
			}
			key := resourceid.ResourceKey(gvr.Group, kind, obj.GetNamespace(), obj.GetName())
			issuers[key] = obj
		}
	}
	for i := range certificates {
		cert := &certificates[i]
		kind, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "kind")
		group, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "group")
		name, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
		if kind == "" {
			kind = "Issuer"
		}
		if group == "" {
			group = "cert-manager.io"
		}
		if name == "" || group != "cert-manager.io" || (kind != "Issuer" && kind != "ClusterIssuer") {
			continue
		}
		namespace := cert.GetNamespace()
		if kind == "ClusterIssuer" {
			namespace = ""
		}
		key := resourceid.ResourceKey(group, kind, namespace, name)
		issuer := issuers[key]
		if issuer == nil {
			continue
		}
		source := byResource[resourceid.ResourceKey("cert-manager.io", "Certificate", cert.GetNamespace(), cert.GetName())]
		if source == "" {
			continue
		}
		target := byResource[key]
		if target == "" {
			target = fmt.Sprintf("%s/%s/%s/%s", strings.ToLower(kind), namespace, name, group)
			data := map[string]any{"namespace": namespace, "labels": issuer.GetLabels(), "apiVersion": issuer.GetAPIVersion()}
			if namespace == "" {
				data[clusterScopedGroupKey] = group
				data[clusterScopedResourceKey] = resources[kind]
			}
			nodes = append(nodes, Node{ID: target, Kind: NodeKind(kind), Name: name, Status: extractGenericStatus(issuer), Data: data})
			byResource[key] = target
		}
		edges = append(edges, Edge{ID: source + "-to-" + target, Source: source, Target: target, Type: EdgeUses, Label: "issuer"})
	}
	return nodes, edges, warnings
}
