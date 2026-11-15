package configrefs

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// CertManagerGroup and CertManagerACMEGroup are cert-manager's API groups.
const (
	CertManagerGroup     = "cert-manager.io"
	CertManagerACMEGroup = "acme.cert-manager.io"
)

// CertManagerIssuer returns the Issuer or ClusterIssuer a cert-manager
// Certificate, CertificateRequest, ACME Order or Challenge names in
// spec.issuerRef, following cert-manager's defaults: an omitted kind is an
// Issuer in the object's namespace, an omitted group cert-manager's. An
// external issuer is left out: whether its kind is namespaced takes
// discovery to say.
func CertManagerIssuer(obj *unstructured.Unstructured) (PathRef, bool) {
	group := resourceid.GroupFromAPIVersion(obj.GetAPIVersion())
	switch obj.GetKind() {
	case "Certificate", "CertificateRequest":
		if group != CertManagerGroup {
			return PathRef{}, false
		}
	case "Order", "Challenge":
		if group != CertManagerACMEGroup {
			return PathRef{}, false
		}
	default:
		return PathRef{}, false
	}
	kind, _, _ := unstructured.NestedString(obj.Object, "spec", "issuerRef", "kind")
	issuerGroup, _, _ := unstructured.NestedString(obj.Object, "spec", "issuerRef", "group")
	name, _, _ := unstructured.NestedString(obj.Object, "spec", "issuerRef", "name")
	if kind == "" {
		kind = "Issuer"
	}
	if issuerGroup == "" {
		issuerGroup = CertManagerGroup
	}
	if name == "" || issuerGroup != CertManagerGroup || kind != "Issuer" && kind != "ClusterIssuer" {
		return PathRef{}, false
	}
	namespace := obj.GetNamespace()
	if kind == "ClusterIssuer" {
		namespace = ""
	}
	return PathRef{Ref: resourceid.NewRef(issuerGroup, kind, namespace, name), Path: "spec.issuerRef"}, true
}
