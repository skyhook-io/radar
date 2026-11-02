package topology

import (
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// issuanceIssuerDependency is the issuer dependency of a CertificateRequest,
// Order or Challenge. Certificates are joined by addCertificateIssuerEdges.
func issuanceIssuerDependency(obj *unstructured.Unstructured) *declaredDependency {
	if obj.GetKind() == "Certificate" {
		return nil
	}
	ref, ok := certificateIssuerDependency(obj)
	if !ok {
		return nil
	}
	return &ref
}
