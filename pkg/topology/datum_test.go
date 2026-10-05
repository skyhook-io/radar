package topology

import (
	"github.com/skyhook-io/radar/pkg/datum"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestDatumConfiguredGraphAndRelatedResources(t *testing.T) {
	pg := schema.GroupVersionResource{Group: datum.NetworkGroup, Version: "v1alpha", Resource: "httpproxies"}
	cg := schema.GroupVersionResource{Group: datum.NetworkGroup, Version: "v1alpha1", Resource: "connectors"}
	dg := schema.GroupVersionResource{Group: datum.NetworkGroup, Version: "v1alpha", Resource: "domains"}
	proxy := genericIdentityObject(pg, "HTTPProxy", "p", "web")
	proxy.Object["spec"] = map[string]any{"hostnames": []any{"web.example.test"}, "rules": []any{map[string]any{"backends": []any{map[string]any{"connector": map[string]any{"name": "edge"}}, map[string]any{"instance": map[string]any{"name": "origin"}}}}}}
	connector := genericIdentityObject(cg, "Connector", "p", "edge")
	domain := genericIdentityObject(dg, "Domain", "p", "domain")
	domain.Object["spec"] = map[string]any{"domainName": "example.test"}
	dp := &genericIdentityDynamic{watched: []schema.GroupVersionResource{pg, cg, dg}, kinds: map[schema.GroupVersionResource]string{pg: "HTTPProxy", cg: "Connector", dg: "Domain"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{pg: {proxy}, cg: {connector}, dg: {domain}}, listCalls: map[schema.GroupVersionResource]int{}}
	b := &Builder{dynamic: dp}
	nodes, edges := b.addDatumNodes(nil, nil, BuildOptions{})
	if len(nodes) != 4 || len(edges) != 3 {
		t.Fatalf("nodes=%+v edges=%+v", nodes, edges)
	}
	neighborhood := BuildNeighborhoodWithIndex(&Topology{Nodes: nodes, Edges: edges}, ResourceRef{Kind: "HTTPProxy", Namespace: "p", Name: "web", Group: datum.NetworkGroup}, NeighborhoodOptions{}, nil, dp)
	if neighborhood.Root.Kind != "HTTPProxy" || len(neighborhood.Edges) != 3 {
		t.Fatalf("Datum neighborhood=%+v", neighborhood)
	}
	for _, edge := range edges {
		if edge.Type != EdgeConfigures {
			t.Fatalf("configuration shown as traffic: %+v", edge)
		}
	}
	inferred := false
	for _, edge := range edges {
		inferred = inferred || edge.Label == "Inferred hostname association"
	}
	if !inferred {
		t.Fatal("inferred association not labelled")
	}
	rel := GetRelationshipsWithObject("HTTPProxy", "p", "web", proxy, &Topology{Nodes: nodes, Edges: edges}, nil, dp, nil)
	if len(rel.ConfigRefs) != 3 {
		t.Fatalf("configured references=%+v", rel)
	}
	for _, ref := range rel.ConfigRefs {
		if ref.Kind == "Domain" && !ref.Inferred {
			t.Fatal("related domain lost inferred label")
		}
		if ref.Kind == "Instance" {
			t.Fatal("backend resolved to compute Instance")
		}
	}
	connRel := GetRelationshipsWithObject("Connector", "p", "edge", connector, &Topology{Nodes: nodes, Edges: edges}, nil, dp, nil)
	if len(connRel.Consumers) != 1 || connRel.Consumers[0].Group != datum.NetworkGroup {
		t.Fatalf("connector consumers=%+v", connRel)
	}
	specific := genericIdentityObject(dg, "Domain", "p", "specific")
	specific.Object["spec"] = map[string]any{"domainName": "web.example.test"}
	dp.resources[dg] = append(dp.resources[dg], specific)
	_, moreEdges := b.addDatumNodes(nil, nil, BuildOptions{})
	inferredCount := 0
	for _, edge := range moreEdges {
		if edge.Label == "Inferred hostname association" {
			inferredCount++
			if edge.Source != "domain/p/specific/"+datum.NetworkGroup {
				t.Fatalf("selected wrong domain: %+v", edge)
			}
		}
	}
	if inferredCount != 1 {
		t.Fatalf("inferred edges=%+v", moreEdges)
	}
	nodes, _ = b.addDatumNodes(nil, nil, BuildOptions{Namespaces: []string{"other"}})
	if len(nodes) != 0 {
		t.Fatalf("cross-namespace nodes=%+v", nodes)
	}
}

func TestConfiguredEndpointIsUnknownAndNotAnInternetTrafficSource(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: datum.NetworkGroup, Version: "v1alpha", Resource: "httpproxies"}
	proxy := genericIdentityObject(gvr, "HTTPProxy", "p", "web")
	proxy.Object["spec"] = map[string]any{"rules": []any{map[string]any{"backends": []any{map[string]any{"endpoint": "https://origin.example.test"}}}}}
	dp := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "HTTPProxy"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {proxy}}, listCalls: map[schema.GroupVersionResource]int{}}
	nodes, edges := (&Builder{dynamic: dp}).addDatumNodes(nil, nil, BuildOptions{})
	if len(nodes) != 2 || nodes[1].Kind != KindConfiguredEndpoint || nodes[1].Status != StatusUnknown || nodes[1].Name != "origin.example.test" || edges[0].Type != EdgeConfigures {
		t.Fatalf("nodes=%+v edges=%+v", nodes, edges)
	}
}
