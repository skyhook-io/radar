package relations

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var traefikGroups = []string{configrefs.TraefikGroup, configrefs.TraefikLegacyGroup}

// traefikKinds are kinds in both Traefik groups: a reference made in one
// group is read from either (configrefs.PathRef.Or).
func traefikKinds(kinds ...string) []resourceid.GroupKind {
	var out []resourceid.GroupKind
	for _, group := range traefikGroups {
		for _, kind := range kinds {
			out = append(out, resourceid.GroupKind{Group: group, Kind: kind})
		}
	}
	return out
}

// traefikSources are kinds in both Traefik groups, each declaring targets.
func traefikSources(targets []resourceid.GroupKind, kinds ...string) []Source {
	var out []Source
	for _, gk := range traefikKinds(kinds...) {
		out = append(out, Source{Kind: gk, Targets: targets})
	}
	return out
}

func traefikDeclared(extract func(*unstructured.Unstructured) []configrefs.PathRef) func(runtime.Object, func(Declaration)) error {
	return func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			for _, r := range extract(u) {
				visitPathRef(visit, r)
			}
		}
		return nil
	}
}

// TraefikBackends is the family of the Services and TraefikServices a
// Traefik route sends traffic to, or a TraefikService balances or mirrors
// to, which may be in another namespace.
var TraefikBackends = &Family{
	Name:           "traefikBackends",
	Projection:     project("exposes", false, "Backends", "Routes").whenSource(resourceid.GroupKind{Group: configrefs.TraefikGroup, Kind: "TraefikService"}, "exposes", false, "Backends", "RoutedFrom").whenSource(resourceid.GroupKind{Group: configrefs.TraefikLegacyGroup, Kind: "TraefikService"}, "exposes", false, "Backends", "RoutedFrom"),
	Sources:        traefikSources(append([]resourceid.GroupKind{kindService}, traefikKinds("TraefikService")...), "IngressRoute", "IngressRouteTCP", "IngressRouteUDP", "TraefikService"),
	Targets:        append([]resourceid.GroupKind{kindService}, traefikKinds("TraefikService")...),
	CrossNamespace: true,
	Declared:       traefikDeclared(configrefs.TraefikBackendReferences),
}

// TraefikRouteConfig is the family of what configures a Traefik route: its
// Middlewares, the ServersTransport each backend dials through and its
// TLSOption, and the Middlewares a chain Middleware runs. Each may be in
// another namespace.
var TraefikRouteConfig = &Family{
	Name:       "traefikRouteConfig",
	Projection: project("configures", true, "ConfigRefs", "Consumers"),
	Sources: append(append(append(append([]Source{},
		traefikSources(traefikKinds("Middleware", "ServersTransport", "TLSOption"), "IngressRoute")...),
		traefikSources(traefikKinds("MiddlewareTCP", "ServersTransportTCP", "TLSOption"), "IngressRouteTCP")...),
		traefikSources(traefikKinds("Middleware", "ServersTransport"), "IngressRouteUDP")...),
		traefikSources(traefikKinds("Middleware"), "Middleware")...),
	Targets:        traefikKinds("Middleware", "MiddlewareTCP", "TLSOption", "ServersTransport", "ServersTransportTCP"),
	CrossNamespace: true,
	Declared:       traefikDeclared(configrefs.TraefikRouteConfigReferences),
}

// TraefikSecrets is the family of the Secrets and ConfigMaps a Traefik
// transport, TLSOption, TLSStore or route reads, in its own namespace.
var TraefikSecrets = &Family{
	Name:       "traefikSecrets",
	Projection: project("configures", true, "ConfigRefs", "Consumers"),
	Sources: append(
		traefikSources([]resourceid.GroupKind{kindSecret, {Kind: "ConfigMap"}}, "ServersTransport", "ServersTransportTCP"),
		traefikSources([]resourceid.GroupKind{kindSecret}, "TLSOption", "TLSStore", "IngressRoute", "IngressRouteTCP")...),
	Targets:  []resourceid.GroupKind{kindSecret, {Kind: "ConfigMap"}},
	Declared: traefikDeclared(configrefs.TraefikSecretReferences),
}
