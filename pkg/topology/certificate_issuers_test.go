package topology

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type issuerTestProvider struct {
	monitorDynamicProvider
	getCalls  int
	listCalls map[string]int
	listError error
}

func (p *issuerTestProvider) Get(schema.GroupVersionResource, string, string) (*unstructured.Unstructured, error) {
	p.getCalls++
	return nil, fmt.Errorf("unexpected object fetch")
}

func (p *issuerTestProvider) ListNamespaces(gvr schema.GroupVersionResource, namespaces []string) ([]*unstructured.Unstructured, error) {
	p.listCalls[gvr.Resource]++
	if p.listError != nil {
		return nil, p.listError
	}
	return p.monitorDynamicProvider.ListNamespaces(gvr, namespaces)
}

func TestCertificateIssuerEdgesDefaultsScopeAndCachedResolution(t *testing.T) {
	issuerGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}
	clusterGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}
	makeIssuer := func(kind, namespace, name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": kind, "metadata": map[string]any{"namespace": namespace, "name": name}}}
	}
	p := &issuerTestProvider{monitorDynamicProvider: monitorDynamicProvider{
		gvrs: map[string]schema.GroupVersionResource{"Issuer": issuerGVR, "ClusterIssuer": clusterGVR},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			issuerGVR:  {makeIssuer("Issuer", "team", "ca"), makeIssuer("Issuer", "other", "ca"), makeIssuer("Issuer", "team", "unused")},
			clusterGVR: {makeIssuer("ClusterIssuer", "", "ca")},
		}}, listCalls: map[string]int{}}
	var certs []unstructured.Unstructured
	var nodes []Node
	for i, issuerRef := range []map[string]any{
		{"name": "ca"},
		{"name": "ca", "kind": "ClusterIssuer"},
		{"name": "ca", "kind": "Issuer", "group": "external.example.com"},
		{"name": "absent"},
		{"name": "ca"},
	} {
		name := fmt.Sprintf("cert-%d", i)
		certs = append(certs, unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": map[string]any{"namespace": "team", "name": name}, "spec": map[string]any{"issuerRef": issuerRef}}})
		nodes = append(nodes, Node{ID: "certificate/team/" + name, Kind: KindCertificate, Name: name, Data: map[string]any{"namespace": "team", "apiVersion": "cert-manager.io/v1"}})
	}
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"team"}
	nodes, edges, warnings := addCertificateIssuerEdges(nodes, nil, certs, p, opts)
	if len(warnings) != 0 || len(nodes) != 7 || len(edges) != 3 {
		t.Fatalf("nodes=%d edges=%+v warnings=%v; want two referenced issuers and three joins", len(nodes), edges, warnings)
	}
	if p.getCalls != 0 || p.listCalls["issuers"] != 1 || p.listCalls["clusterissuers"] != 1 {
		t.Fatalf("lookup fan-out: gets=%d lists=%v", p.getCalls, p.listCalls)
	}
	topo := &Topology{Nodes: nodes, Edges: edges}
	rel := GetRelationships("Certificate", "team", "cert-0", topo, nil, p)
	if rel == nil || len(rel.Dependencies) != 1 || rel.Dependencies[0] != (ResourceRef{Kind: "Issuer", Group: "cert-manager.io", Namespace: "team", Name: "ca"}) || rel.ScaleTarget != nil {
		t.Fatalf("default issuer relationship = %+v", rel)
	}
	if tuples := topo.ClusterScopedDynamicRBACTuples(); len(tuples) != 1 || tuples[0].Group != "cert-manager.io" || tuples[0].Resource != "clusterissuers" {
		t.Fatalf("cluster issuer authorization tuples = %+v", tuples)
	}
	topo.StripClusterScopedDynamicExcept(nil)
	if len(topo.Nodes) != 6 || len(topo.Edges) != 2 {
		t.Fatalf("denied cluster issuer retained: nodes=%d edges=%d", len(topo.Nodes), len(topo.Edges))
	}
}

func TestCertificateIssuerEdgesReportsUnavailableLookup(t *testing.T) {
	p := &issuerTestProvider{monitorDynamicProvider: monitorDynamicProvider{
		gvrs: map[string]schema.GroupVersionResource{
			"Issuer":        {Group: "cert-manager.io", Version: "v1", Resource: "issuers"},
			"ClusterIssuer": {Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"},
		},
	}, listCalls: map[string]int{}, listError: fmt.Errorf("cache unavailable")}
	cert := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
		"metadata": map[string]any{"name": "tls", "namespace": "team"},
		"spec":     map[string]any{"issuerRef": map[string]any{"name": "ca"}},
	}}
	nodes, edges, warnings := addCertificateIssuerEdges([]Node{{ID: "certificate/team/tls", Kind: KindCertificate, Name: "tls", Data: map[string]any{"apiVersion": "cert-manager.io/v1"}}}, nil, []unstructured.Unstructured{cert}, p, DefaultBuildOptions())
	if len(nodes) != 1 || len(edges) != 0 || len(warnings) != 2 || p.getCalls != 0 {
		t.Fatalf("unavailable lookup: nodes=%d edges=%d warnings=%v gets=%d", len(nodes), len(edges), warnings, p.getCalls)
	}
}
