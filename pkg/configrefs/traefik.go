package configrefs

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Traefik's API groups. Traefik 2.10 serves both and resolves a reference
// from either, preferring TraefikGroup when both hold the name; Traefik 3
// serves only TraefikGroup.
const (
	TraefikGroup       = "traefik.io"
	TraefikLegacyGroup = "traefik.containo.us"
)

// traefikNamedRef is the object of kind that raw names by name and
// namespace, defaulting to obj's. A Traefik kind is named in TraefikGroup,
// with TraefikLegacyGroup as the alternative, whichever group obj is in.
func traefikNamedRef(obj *unstructured.Unstructured, raw map[string]any, kind, path string) (PathRef, bool) {
	name, _ := raw["name"].(string)
	if name == "" {
		return PathRef{}, false
	}
	ns, _ := raw["namespace"].(string)
	if ns == "" {
		ns = obj.GetNamespace()
	}
	if kind == "Service" {
		return PathRef{Ref: resourceid.NewRef("", kind, ns, name), Path: path}, true
	}
	return PathRef{Ref: resourceid.NewRef(TraefikGroup, kind, ns, name), Path: path, Or: []resourceid.Ref{resourceid.NewRef(TraefikLegacyGroup, kind, ns, name)}}, true
}

// TraefikBackendReferences returns the Services and TraefikServices an
// IngressRoute, IngressRouteTCP or IngressRouteUDP routes to, or a
// TraefikService balances or mirrors to. A backend kind other than Service
// or TraefikService, which Traefik's CRDs don't allow, names nothing.
func TraefikBackendReferences(obj *unstructured.Unstructured) []PathRef {
	var refs []PathRef
	add := func(raw map[string]any, path string) {
		kind, _ := raw["kind"].(string)
		switch kind {
		case "", "Service":
			kind = "Service"
		case "TraefikService":
		default:
			return
		}
		if ref, ok := traefikNamedRef(obj, raw, kind, path); ok {
			refs = append(refs, ref)
		}
	}
	addList := func(raw map[string]any, field, path string) {
		items, _, _ := unstructured.NestedSlice(raw, field)
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				add(m, path)
			}
		}
	}
	switch obj.GetKind() {
	case "IngressRoute", "IngressRouteTCP", "IngressRouteUDP":
		routes, _, _ := unstructured.NestedSlice(obj.Object, "spec", "routes")
		for _, route := range routes {
			if m, ok := route.(map[string]any); ok {
				addList(m, "services", "spec.routes[].services[]")
			}
		}
	case "TraefikService":
		for _, field := range []string{"weighted", "highestRandomWeight"} {
			raw, _, _ := unstructured.NestedMap(obj.Object, "spec", field)
			addList(raw, "services", "spec."+field+".services[]")
		}
		raw, _, _ := unstructured.NestedMap(obj.Object, "spec", "mirroring")
		add(raw, "spec.mirroring")
		addList(raw, "mirrors", "spec.mirroring.mirrors[]")
	}
	return refs
}

// TraefikRouteConfigReferences returns what configures a Traefik route: its
// Middlewares (MiddlewareTCP on an IngressRouteTCP), the ServersTransport
// (ServersTransportTCP) each Service backend dials through, resolved in the
// backend's namespace for HTTP and the route's for TCP, and its TLSOption;
// for a Middleware, the Middlewares its chain runs.
func TraefikRouteConfigReferences(obj *unstructured.Unstructured) []PathRef {
	var refs []PathRef
	add := func(raw map[string]any, kind, path string) {
		if r, ok := traefikNamedRef(obj, raw, kind, path); ok {
			refs = append(refs, r)
		}
	}
	if obj.GetKind() == "Middleware" {
		chain, _, _ := unstructured.NestedSlice(obj.Object, "spec", "chain", "middlewares")
		for _, item := range chain {
			if m, ok := item.(map[string]any); ok {
				add(m, "Middleware", "spec.chain.middlewares[]")
			}
		}
		return refs
	}
	mwKind, stKind := "Middleware", "ServersTransport"
	if obj.GetKind() == "IngressRouteTCP" {
		mwKind, stKind = "MiddlewareTCP", "ServersTransportTCP"
	}
	routes, _, _ := unstructured.NestedSlice(obj.Object, "spec", "routes")
	for _, route := range routes {
		m, _ := route.(map[string]any)
		services, _, _ := unstructured.NestedSlice(m, "services")
		for _, service := range services {
			svc, _ := service.(map[string]any)
			backend, ok := traefikNamedRef(obj, svc, "Service", "")
			kind, _ := svc["kind"].(string)
			name, _ := svc["serversTransport"].(string)
			if !ok || name == "" || (kind != "" && kind != "Service") {
				continue
			}
			ns := backend.Ref.Namespace
			if obj.GetKind() == "IngressRouteTCP" {
				ns = obj.GetNamespace()
			}
			add(map[string]any{"name": name, "namespace": ns}, stKind, "spec.routes[].services[].serversTransport")
		}
		middlewares, _, _ := unstructured.NestedSlice(m, "middlewares")
		for _, item := range middlewares {
			if mw, ok := item.(map[string]any); ok {
				add(mw, mwKind, "spec.routes[].middlewares[]")
			}
		}
	}
	if obj.GetKind() != "IngressRouteUDP" {
		options, _, _ := unstructured.NestedMap(obj.Object, "spec", "tls", "options")
		add(options, "TLSOption", "spec.tls.options")
	}
	return refs
}

// TraefikSecretReferences returns the Secrets and ConfigMaps, in obj's
// namespace, a Traefik object reads: a ServersTransport's or
// ServersTransportTCP's CAs and client certificates, a TLSOption's client
// CAs, a TLSStore's certificates and a route's TLS Secret.
func TraefikSecretReferences(obj *unstructured.Unstructured) []PathRef {
	var refs []PathRef
	add := func(kind, name, path string) {
		if name != "" {
			refs = append(refs, PathRef{Ref: resourceid.NewRef("", kind, obj.GetNamespace(), name), Path: path})
		}
	}
	switch obj.GetKind() {
	case "ServersTransport", "ServersTransportTCP":
		base := []string{"spec"}
		if obj.GetKind() == "ServersTransportTCP" {
			base = append(base, "tls")
		}
		raw, _, _ := unstructured.NestedMap(obj.Object, base...)
		prefix := strings.Join(base, ".")
		rootCAs, _, _ := unstructured.NestedSlice(raw, "rootCAs")
		for _, item := range rootCAs {
			ca, _ := item.(map[string]any)
			secret, _ := ca["secret"].(string)
			cm, _ := ca["configMap"].(string)
			// Traefik skips entries that name both, regardless of the type comment.
			if secret != "" && cm != "" {
				continue
			}
			add("Secret", secret, prefix+".rootCAs[].secret")
			add("ConfigMap", cm, prefix+".rootCAs[].configMap")
		}
		for _, field := range []string{"rootCAsSecrets", "certificatesSecrets"} {
			names, _, _ := unstructured.NestedStringSlice(raw, field)
			for _, name := range names {
				add("Secret", name, prefix+"."+field+"[]")
			}
		}
	case "TLSOption":
		names, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "clientAuth", "secretNames")
		for _, name := range names {
			refs = append(refs, PathRef{Ref: resourceid.NewRef("", "Secret", obj.GetNamespace(), name), Path: "spec.clientAuth.secretNames[]"})
		}
	case "TLSStore":
		name, _, _ := unstructured.NestedString(obj.Object, "spec", "defaultCertificate", "secretName")
		add("Secret", name, "spec.defaultCertificate.secretName")
		certs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "certificates")
		for _, item := range certs {
			if cert, ok := item.(map[string]any); ok {
				name, _ := cert["secretName"].(string)
				add("Secret", name, "spec.certificates[].secretName")
			}
		}
	case "IngressRoute", "IngressRouteTCP":
		name, _, _ := unstructured.NestedString(obj.Object, "spec", "tls", "secretName")
		add("Secret", name, "spec.tls.secretName")
	}
	return refs
}

// TraefikTransportRefs lists the Secrets and ConfigMaps, in the transport's own
// namespace, that a Traefik ServersTransport or ServersTransportTCP reads TLS
// material from: rootCAs (a Secret or a ConfigMap each), the deprecated
// rootCAsSecrets, and certificatesSecrets. ServersTransport keeps these under
// spec; ServersTransportTCP nests them under spec.tls. Only the traefik.io
// group has rootCAs; the legacy traefik.containo.us group has the Secret lists.
func TraefikTransportRefs(obj map[string]any, tcp bool) (secrets, configMaps []string) {
	kind := "ServersTransport"
	if tcp {
		kind = "ServersTransportTCP"
	}
	u := &unstructured.Unstructured{Object: map[string]any{"kind": kind, "spec": obj["spec"]}}
	seen := map[resourceid.Ref]bool{}
	for _, r := range TraefikSecretReferences(u) {
		if seen[r.Ref] {
			continue
		}
		seen[r.Ref] = true
		if r.Ref.Kind == "Secret" {
			secrets = append(secrets, r.Ref.Name)
		} else {
			configMaps = append(configMaps, r.Ref.Name)
		}
	}
	return secrets, configMaps
}

// TraefikTLSStoreSecrets lists the Secrets a Traefik TLSStore loads
// certificates from: spec.defaultCertificate and every spec.certificates entry.
func TraefikTLSStoreSecrets(obj map[string]any) []string {
	u := &unstructured.Unstructured{Object: map[string]any{"kind": "TLSStore", "spec": obj["spec"]}}
	var names []string
	seen := map[string]bool{}
	for _, r := range TraefikSecretReferences(u) {
		if !seen[r.Ref.Name] {
			seen[r.Ref.Name] = true
			names = append(names, r.Ref.Name)
		}
	}
	return names
}
