package topology

import (
	"fmt"
	"github.com/skyhook-io/radar/pkg/datum"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/url"
	"sort"
	"strings"
)

func (b *Builder) addDatumNodes(nodes []Node, edges []Edge, opts BuildOptions) ([]Node, []Edge) {
	if b.dynamic == nil {
		return nodes, edges
	}
	ids := map[string]string{}
	for i := range nodes {
		for _, key := range nodeResourceKeys(&nodes[i]) {
			ids[key] = nodes[i].ID
		}
	}
	var objects []*unstructured.Unstructured
	watched := b.dynamic.GetWatchedResources()
	sort.Slice(watched, func(i, j int) bool { return watched[i].String() < watched[j].String() })
	for _, gvr := range watched {
		resource, ok := datum.Lookup(gvr.Group, gvr.Resource)
		if !ok || !resource.Namespaced {
			continue
		}
		items, err := b.dynamic.ListNamespaces(gvr, opts.Namespaces)
		if err != nil {
			continue
		}
		for _, u := range items {
			if !opts.MatchesNamespaceFilter(u.GetNamespace()) {
				continue
			}
			objects = append(objects, u)
			key := resourceid.ResourceKey(gvr.Group, u.GetKind(), u.GetNamespace(), u.GetName())
			if ids[key] != "" {
				continue
			}
			id := fmt.Sprintf("%s/%s/%s/%s", strings.ToLower(u.GetKind()), u.GetNamespace(), u.GetName(), gvr.Group)
			ids[key] = id
			state := datum.State(u)
			health := StatusUnknown
			switch state {
			case "Ready":
				health = StatusHealthy
			case "Attention needed":
				health = StatusDegraded
			case "Reconciling", "Terminating":
				health = StatusNeutral
			}
			nodeKind := NodeKind(u.GetKind())
			if u.GetKind() == "HTTPProxy" {
				nodeKind = KindDatumHTTPProxy
			}
			nodes = append(nodes, Node{ID: id, Kind: nodeKind, Name: u.GetName(), Status: health, Data: map[string]any{"resourceKind": u.GetKind(), "namespace": u.GetNamespace(), "apiVersion": u.GetAPIVersion(), "labels": u.GetLabels(), "status": state, "hosts": datum.Hosts(u)}})
		}
	}
	connect := func(source, target, label string) {
		if source == "" || target == "" || source == target {
			return
		}
		id := source + "-configures-" + target + "-" + label
		for _, e := range edges {
			if e.ID == id {
				return
			}
		}
		edges = append(edges, Edge{ID: id, Source: source, Target: target, Type: EdgeConfigures, Label: label})
	}
	for _, u := range objects {
		source := ids[resourceid.ResourceKey(u.GroupVersionKind().Group, u.GetKind(), u.GetNamespace(), u.GetName())]
		for _, reference := range datum.References(u) {
			ref := reference.Ref
			if ref.Namespace == "" || !opts.MatchesNamespaceFilter(ref.Namespace) {
				continue
			}
			key := resourceid.ResourceKey(ref.Group, ref.Kind, ref.Namespace, ref.Name)
			target := ids[key]
			if target == "" && ref.Group == "discovery.k8s.io" && ref.Kind == "EndpointSlice" {
				id := fmt.Sprintf("endpointslice/%s/%s/discovery.k8s.io", ref.Namespace, ref.Name)
				target = id
				ids[key] = id
				nodes = append(nodes, Node{ID: id, Kind: NodeKind(ref.Kind), Name: ref.Name, Status: StatusUnknown, Data: map[string]any{"namespace": ref.Namespace, "apiVersion": "discovery.k8s.io/v1", "status": "Declared reference; inventory not observed"}})
			}
			if u.GetKind() == "DNSRecordSet" || u.GetKind() == "NetworkContext" || u.GetKind() == "NetworkBinding" || u.GetKind() == "Subnet" || u.GetKind() == "SubnetClaim" {
				connect(target, source, reference.Label)
			} else {
				connect(source, target, reference.Label)
			}
		}
		if u.GetKind() == "HTTPProxy" {
			for _, host := range datum.Hosts(u) {
				var best *unstructured.Unstructured
				longest := 0
				for _, d := range objects {
					if d.GetKind() != "Domain" || d.GetNamespace() != u.GetNamespace() {
						continue
					}
					for _, domain := range datum.Hosts(d) {
						if datum.HostMatches(domain, host) && len(domain) > longest {
							best = d
							longest = len(domain)
						}
					}
				}
				if best != nil {
					connect(ids[resourceid.ResourceKey(datum.NetworkGroup, "Domain", best.GetNamespace(), best.GetName())], source, "Inferred hostname association")
				}
			}

			rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
			for _, raw := range rules {
				rule, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				backends, _, _ := unstructured.NestedSlice(rule, "backends")
				for _, raw := range backends {
					backend, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					endpoint, _, _ := unstructured.NestedString(backend, "endpoint")
					if endpoint == "" {
						continue
					}
					parsed, err := url.Parse(endpoint)
					if err != nil || parsed.Hostname() == "" {
						continue
					}
					id := source + "/endpoint/" + parsed.Hostname()
					if ids[id] == "" {
						ids[id] = id
						nodes = append(nodes, Node{ID: id, Kind: KindConfiguredEndpoint, Name: parsed.Hostname(), Status: StatusUnknown, Data: map[string]any{"namespace": u.GetNamespace(), "external": true, "status": "Configured endpoint; reachability not tested"}})
					}
					connect(source, id, "Configured endpoint")
				}
			}
		}
	}
	return nodes, edges
}
