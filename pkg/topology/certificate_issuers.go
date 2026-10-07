package topology

import (
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func addCertificateIssuerEdges(nodes []Node, edges []Edge, certificates []unstructured.Unstructured, provider DynamicProvider, opts BuildOptions) ([]Node, []Edge, []string) {
	var refs []declaredDependency
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
		if name == "" || group != "cert-manager.io" || kind != "Issuer" && kind != "ClusterIssuer" {
			continue
		}
		namespace := cert.GetNamespace()
		if kind == "ClusterIssuer" {
			namespace = ""
		}
		refs = append(refs, declaredDependency{
			Source: resourceid.NewRef("cert-manager.io", "Certificate", cert.GetNamespace(), cert.GetName()),
			Target: resourceid.NewRef(group, kind, namespace, name), Label: "issuer",
		})
	}
	return addObservedDependencyEdges(nodes, edges, refs, provider, opts)
}
