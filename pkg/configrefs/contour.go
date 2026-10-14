package configrefs

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
)

// ContourSecretRef resolves a Secret reference in an HTTPProxy, such as
// spec.virtualhost.tls.secretName, the way Contour does. "namespace/name"
// names a Secret another namespace delegates with a TLSCertificateDelegation;
// a bare name, or an empty namespace, is in the proxy's own namespace.
func ContourSecretRef(proxyNamespace, ref string) (namespace, name string) {
	namespace, name, found := strings.Cut(ref, "/")
	if !found {
		return proxyNamespace, ref
	}
	if namespace == "" {
		namespace = proxyNamespace
	}
	return namespace, name
}

// ContourSecretReference reads HTTPProxy's local name or delegated
// namespace/name. This is a declared reference, not proof TLS delegation is
// authorized. Invalid qualified names must not become phantom local Secrets.
func ContourSecretReference(raw, defaultNamespace string) (resourceid.Ref, bool) {
	namespace, name := ContourSecretRef(defaultNamespace, raw)
	if namespace == "" || name == "" || len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
		return resourceid.Ref{}, false
	}
	return resourceid.NewRef("", "Secret", namespace, name), true
}

// ContourSecretReferences includes server credentials, downstream CA/CRL and
// upstream TLS/JWKS validation bundles using the same delegated-name contract.
func ContourSecretReferences(obj *unstructured.Unstructured) []resourceid.Ref {
	var refs []resourceid.Ref
	for _, path := range [][]string{
		{"spec", "virtualhost", "tls", "secretName"},
		{"spec", "virtualhost", "tls", "clientValidation", "caSecret"},
		{"spec", "virtualhost", "tls", "clientValidation", "crlSecret"},
	} {
		name, _, _ := unstructured.NestedString(obj.Object, path...)
		if ref, ok := ContourSecretReference(name, obj.GetNamespace()); ok {
			refs = append(refs, ref)
		}
	}
	addValidation := func(raw map[string]any) {
		name, _, _ := unstructured.NestedString(raw, "validation", "caSecret")
		if ref, ok := ContourSecretReference(name, obj.GetNamespace()); ok {
			refs = append(refs, ref)
		}
	}
	addServices := func(raw map[string]any) {
		services, _, _ := unstructured.NestedSlice(raw, "services")
		for _, service := range services {
			if m, ok := service.(map[string]any); ok {
				addValidation(m)
			}
		}
	}
	routes, _, _ := unstructured.NestedSlice(obj.Object, "spec", "routes")
	for _, route := range routes {
		if m, ok := route.(map[string]any); ok {
			addServices(m)
		}
	}
	if tcp, _, _ := unstructured.NestedMap(obj.Object, "spec", "tcpproxy"); tcp != nil {
		addServices(tcp)
	}
	providers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "virtualhost", "jwtProviders")
	for _, provider := range providers {
		if m, ok := provider.(map[string]any); ok {
			if jwks, _, _ := unstructured.NestedMap(m, "remoteJWKS"); jwks != nil {
				addValidation(jwks)
			}
		}
	}
	return refs
}
