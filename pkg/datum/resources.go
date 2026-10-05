package datum

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const DNSGroup = "dns.networking.miloapis.com"
const NetworkGroup = "networking.datumapis.com"
const ComputeGroup = "compute.datumapis.com"
const ResourceManagerGroup = "resourcemanager.miloapis.com"

type Resource struct {
	Group, Version, Kind, Plural string
	Namespaced                   bool
}

var Resources = []Resource{
	{DNSGroup, "v1alpha1", "DNSZone", "dnszones", true}, {DNSGroup, "v1alpha1", "DNSRecordSet", "dnsrecordsets", true}, {DNSGroup, "v1alpha1", "DNSZoneClass", "dnszoneclasses", false},
	{NetworkGroup, "v1alpha", "Domain", "domains", true}, {NetworkGroup, "v1alpha", "HTTPProxy", "httpproxies", true},
	{NetworkGroup, "v1alpha1", "Connector", "connectors", true}, {NetworkGroup, "v1alpha1", "ConnectorClass", "connectorclasses", false}, {NetworkGroup, "v1alpha1", "ConnectorAdvertisement", "connectoradvertisements", true},
	{NetworkGroup, "v1alpha", "Network", "networks", true}, {NetworkGroup, "v1alpha", "NetworkContext", "networkcontexts", true}, {NetworkGroup, "v1alpha", "NetworkBinding", "networkbindings", true}, {NetworkGroup, "v1alpha", "Subnet", "subnets", true}, {NetworkGroup, "v1alpha", "SubnetClaim", "subnetclaims", true}, {NetworkGroup, "v1alpha", "NetworkService", "networkservices", true},
	{ComputeGroup, "v1alpha", "Instance", "instances", true}, {ComputeGroup, "v1alpha", "Workload", "workloads", true},
	{ResourceManagerGroup, "v1alpha1", "Project", "projects", false}, {ResourceManagerGroup, "v1alpha1", "Organization", "organizations", false},
}

func Lookup(group, kind string) (Resource, bool) {
	for _, r := range Resources {
		if r.Group == group && (strings.EqualFold(r.Kind, kind) || r.Plural == kind) {
			return r, true
		}
	}
	return Resource{}, false
}

type Reference struct {
	Ref      resourceid.Ref
	Label    string
	Inferred bool
}

func References(u *unstructured.Unstructured) []Reference {
	if _, ok := Lookup(u.GroupVersionKind().Group, u.GetKind()); !ok {
		return nil
	}
	var out []Reference
	add := func(group, kind, namespace, name, label string) {
		if name != "" {
			out = append(out, Reference{Ref: resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}, Label: label})
		}
	}
	str := func(path ...string) string { v, _, _ := unstructured.NestedString(u.Object, path...); return v }
	ns := u.GetNamespace()
	switch u.GetKind() {
	case "DNSZone":
		add(DNSGroup, "DNSZoneClass", "", str("spec", "dnsZoneClassName"), "Zone class")
		add(NetworkGroup, "Domain", ns, str("status", "domainRef", "name"), "Controller domain reference")
	case "DNSRecordSet":
		add(DNSGroup, "DNSZone", ns, str("spec", "dnsZoneRef", "name"), "Records in zone")
	case "HTTPProxy":
		rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
		for _, raw := range rules {
			rule, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			backends, _, _ := unstructured.NestedSlice(rule, "backends")
			for _, raw := range backends {
				b, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				for _, ref := range []struct{ field, kind, group string }{{"connector", "Connector", NetworkGroup}, {"instance", "EndpointSlice", "discovery.k8s.io"}, {"networkService", "NetworkService", NetworkGroup}} {
					name, _, _ := unstructured.NestedString(b, ref.field, "name")
					add(ref.group, ref.kind, ns, name, "Configured backend")
				}
			}
		}
	case "Connector":
		add(NetworkGroup, "ConnectorClass", "", str("spec", "connectorClassName"), "Connector class")
	case "ConnectorAdvertisement":
		add(NetworkGroup, "Connector", ns, str("spec", "connectorRef", "name"), "Advertises endpoints")
	case "NetworkContext":
		add(NetworkGroup, "Network", ns, str("spec", "network", "name"), "Network context")
	case "NetworkBinding":
		n := str("spec", "network", "namespace")
		if n == "" {
			n = ns
		}
		add(NetworkGroup, "Network", n, str("spec", "network", "name"), "Network binding")
		n = str("status", "networkContextRef", "namespace")
		if n == "" {
			n = ns
		}
		add(NetworkGroup, "NetworkContext", n, str("status", "networkContextRef", "name"), "Resolved network context")
	case "Subnet", "SubnetClaim":
		add(NetworkGroup, "NetworkContext", ns, str("spec", "networkContext", "name"), "Network context")
		if u.GetKind() == "SubnetClaim" {
			add(NetworkGroup, "Subnet", ns, str("status", "subnetRef", "name"), "Allocated subnet")
		}
	}
	return out
}
