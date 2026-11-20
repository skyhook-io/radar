package topology

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourceid"
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

// certificateIssuerDependency is the issuer configrefs.CertManagerIssuer
// reads, as a dependency of obj.
func certificateIssuerDependency(obj *unstructured.Unstructured) (declaredDependency, bool) {
	issuer, ok := configrefs.CertManagerIssuer(obj)
	if !ok {
		return declaredDependency{}, false
	}
	source := resourceid.NewRef(resourceid.GroupFromAPIVersion(obj.GetAPIVersion()), obj.GetKind(), obj.GetNamespace(), obj.GetName())
	return declaredDependency{Source: source, Target: issuer.Ref, Label: "issuer"}, true
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
