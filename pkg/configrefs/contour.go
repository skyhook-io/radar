package configrefs

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/skyhook-io/radar/pkg/resourceid"
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
	for _, r := range ContourSecretPathReferences(obj) {
		refs = append(refs, r.Ref)
	}
	return refs
}

// ContourSecretPathReferences is ContourSecretReferences with the field
// naming each Secret.
func ContourSecretPathReferences(obj *unstructured.Unstructured) []PathRef {
	var refs []PathRef
	for _, path := range [][]string{
		{"spec", "virtualhost", "tls", "secretName"},
		{"spec", "virtualhost", "tls", "clientValidation", "caSecret"},
		{"spec", "virtualhost", "tls", "clientValidation", "crlSecret"},
	} {
		name, _, _ := unstructured.NestedString(obj.Object, path...)
		if ref, ok := ContourSecretReference(name, obj.GetNamespace()); ok {
			refs = append(refs, PathRef{Ref: ref, Path: strings.Join(path, ".")})
		}
	}
	addValidation := func(raw map[string]any, path string) {
		name, _, _ := unstructured.NestedString(raw, "validation", "caSecret")
		if ref, ok := ContourSecretReference(name, obj.GetNamespace()); ok {
			refs = append(refs, PathRef{Ref: ref, Path: path + ".validation.caSecret"})
		}
	}
	addServices := func(raw map[string]any, path string) {
		services, _, _ := unstructured.NestedSlice(raw, "services")
		for _, service := range services {
			if m, ok := service.(map[string]any); ok {
				addValidation(m, path+".services[]")
			}
		}
	}
	routes, _, _ := unstructured.NestedSlice(obj.Object, "spec", "routes")
	for _, route := range routes {
		if m, ok := route.(map[string]any); ok {
			addServices(m, "spec.routes[]")
		}
	}
	if tcp, _, _ := unstructured.NestedMap(obj.Object, "spec", "tcpproxy"); tcp != nil {
		addServices(tcp, "spec.tcpproxy")
	}
	providers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "virtualhost", "jwtProviders")
	for _, provider := range providers {
		if m, ok := provider.(map[string]any); ok {
			if jwks, _, _ := unstructured.NestedMap(m, "remoteJWKS"); jwks != nil {
				addValidation(jwks, "spec.virtualhost.jwtProviders[].remoteJWKS")
			}
		}
	}
	return refs
}

const ContourGroup = "projectcontour.io"

// ContourBackendReferences returns the Services an HTTPProxy's routes and
// TCP proxy send to, in its namespace, and the HTTPProxies its routes and
// TCP proxy delegate to (includes, tcpproxy.include and its deprecated
// spelling includes), which may be in another namespace.
func ContourBackendReferences(obj *unstructured.Unstructured) []PathRef {
	var refs []PathRef
	addServices := func(raw map[string]any, path string) {
		services, _, _ := unstructured.NestedSlice(raw, "services")
		for _, item := range services {
			m, _ := item.(map[string]any)
			name, _ := m["name"].(string)
			if name != "" {
				refs = append(refs, PathRef{Ref: resourceid.NewRef("", "Service", obj.GetNamespace(), name), Path: path + ".services[]"})
			}
		}
	}
	routes, _, _ := unstructured.NestedSlice(obj.Object, "spec", "routes")
	for _, route := range routes {
		if m, ok := route.(map[string]any); ok {
			addServices(m, "spec.routes[]")
		}
	}
	include := func(m map[string]any, path string) {
		name, _ := m["name"].(string)
		if name == "" {
			return
		}
		ns, _ := m["namespace"].(string)
		if ns == "" {
			ns = obj.GetNamespace()
		}
		refs = append(refs, PathRef{Ref: resourceid.NewRef(ContourGroup, "HTTPProxy", ns, name), Path: path})
	}
	includes, _, _ := unstructured.NestedSlice(obj.Object, "spec", "includes")
	for _, item := range includes {
		m, _ := item.(map[string]any)
		include(m, "spec.includes[]")
	}
	tcp, _, _ := unstructured.NestedMap(obj.Object, "spec", "tcpproxy")
	addServices(tcp, "spec.tcpproxy")
	for _, field := range []string{"include", "includes"} {
		m, _, _ := unstructured.NestedMap(tcp, field)
		include(m, "spec.tcpproxy."+field)
	}
	return refs
}
