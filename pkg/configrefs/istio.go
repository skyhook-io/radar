package configrefs

import (
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

const IstioNetworkingGroup = "networking.istio.io"

// IstioServiceDomain is the domain Istio's service registry builds FQDNs
// with: pilot-discovery's --domain, read from the discovery container of the
// istiod Deployments, not a suffix inferred from a destination. Conflicting
// revisions or an unreadable argument leave it unknown ("").
func IstioServiceDomain(deployments []*appsv1.Deployment) string {
	domain, observed := "", false
	for _, deployment := range deployments {
		if deployment.Labels["app"] != "istiod" && deployment.Labels["istio"] != "pilot" {
			continue
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != "discovery" {
				continue
			}
			configured := "cluster.local" // pilot-discovery's documented default.
			args := append(append([]string(nil), container.Command...), container.Args...)
			for i := 0; i < len(args); i++ {
				arg := args[i]
				if arg == "--domain" {
					if i+1 == len(args) {
						return ""
					}
					i++
					configured = args[i]
				} else if strings.HasPrefix(arg, "--domain=") {
					configured = strings.TrimPrefix(arg, "--domain=")
				}
			}
			if len(validation.IsDNS1123Subdomain(configured)) != 0 || configured == "" {
				return ""
			}
			if observed && domain != configured {
				return ""
			}
			domain, observed = configured, true
		}
	}
	if !observed {
		return "cluster.local"
	}
	return domain
}

// IstioHostService returns the Service an Istio host names: a bare name in
// defaultNamespace, or name.namespace.svc.<domain>. Any other host, or an
// FQDN when the domain is unknown, names none.
func IstioHostService(host, defaultNamespace, domain string) (name, namespace string) {
	parts := strings.Split(host, ".")
	if len(parts) == 1 && len(validation.IsDNS1123Label(host)) == 0 {
		return host, defaultNamespace
	}
	if domain == "" || !strings.HasSuffix(host, ".svc."+domain) {
		return "", ""
	}
	service := strings.TrimSuffix(host, ".svc."+domain)
	parts = strings.Split(service, ".")
	if len(parts) == 2 && len(validation.IsDNS1123Label(parts[0])) == 0 && len(validation.IsDNS1123Label(parts[1])) == 0 {
		return parts[0], parts[1]
	}
	return "", ""
}

// IstioVirtualServiceGateways returns the Istio Gateways a VirtualService
// binds to, as namespace/name or a name in its own namespace. The mesh
// keyword binds sidecars, not a Gateway.
func IstioVirtualServiceGateways(vs *unstructured.Unstructured) []PathRef {
	gateways, _, _ := unstructured.NestedStringSlice(vs.Object, "spec", "gateways")
	var refs []PathRef
	for _, gw := range gateways {
		if gw == "mesh" {
			continue
		}
		namespace, name := vs.GetNamespace(), gw
		if ns, n, qualified := strings.Cut(gw, "/"); qualified {
			namespace, name = ns, n
		}
		if name == "" {
			continue
		}
		refs = append(refs, PathRef{Ref: resourceid.NewRef(IstioNetworkingGroup, "Gateway", namespace, name), Path: "spec.gateways[]"})
	}
	return refs
}

// IstioVirtualServiceDestinations returns the Services a VirtualService's
// HTTP, TCP and TLS routes send to, in that order, resolving hosts with the
// mesh's domain (see IstioHostService).
func IstioVirtualServiceDestinations(vs *unstructured.Unstructured, domain string) []PathRef {
	var refs []PathRef
	for _, protocol := range []string{"http", "tcp", "tls"} {
		routes, _, _ := unstructured.NestedSlice(vs.Object, "spec", protocol)
		for _, route := range routes {
			r, _ := route.(map[string]any)
			destinations, _, _ := unstructured.NestedSlice(r, "route")
			for _, d := range destinations {
				dm, _ := d.(map[string]any)
				host, _, _ := unstructured.NestedString(dm, "destination", "host")
				if host == "" {
					continue
				}
				if name, namespace := IstioHostService(host, vs.GetNamespace(), domain); name != "" {
					refs = append(refs, PathRef{Ref: resourceid.NewRef("", "Service", namespace, name), Path: "spec." + protocol + "[].route[].destination.host"})
				}
			}
		}
	}
	return refs
}

// IstioDestinationRuleService returns the Service a DestinationRule's host
// names (see IstioHostService).
func IstioDestinationRuleService(dr *unstructured.Unstructured, domain string) (PathRef, bool) {
	host, _, _ := unstructured.NestedString(dr.Object, "spec", "host")
	if host == "" {
		return PathRef{}, false
	}
	name, namespace := IstioHostService(host, dr.GetNamespace(), domain)
	if name == "" {
		return PathRef{}, false
	}
	return PathRef{Ref: resourceid.NewRef("", "Service", namespace, name), Path: "spec.host"}, true
}
