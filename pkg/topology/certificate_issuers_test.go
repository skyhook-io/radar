package topology

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	"strings"
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
	nodes, edges, warnings := addCertificateIssuerEdges([]Node{{ID: "certificate/team/tls", Kind: KindCertificate, Name: "tls", Data: map[string]any{"namespace": "team", "apiVersion": "cert-manager.io/v1"}}}, nil, []unstructured.Unstructured{cert}, p, DefaultBuildOptions())
	if len(nodes) != 1 || len(edges) != 0 || len(warnings) != 1 || p.getCalls != 0 {
		t.Fatalf("unavailable lookup: nodes=%d edges=%d warnings=%v gets=%d", len(nodes), len(edges), warnings, p.getCalls)
	}
}

func TestReferencedIssuerRetainsGenericOwnerWithoutDuplicateNodesOrEdges(t *testing.T) {
	issuerGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}
	issuer := genericIdentityObject(issuerGVR, "Issuer", "team", "ca", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "operator"})
	p := &genericIdentityDynamic{watched: []schema.GroupVersionResource{issuerGVR}, kinds: map[schema.GroupVersionResource]string{issuerGVR: "Issuer"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{issuerGVR: {issuer}}, listCalls: map[schema.GroupVersionResource]int{}}
	cert := genericIdentityObject(schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1"}, "Certificate", "team", "tls")
	cert.Object["spec"] = map[string]any{"issuerRef": map[string]any{"name": "ca"}}
	nodes := []Node{
		{ID: "certificate/team/tls", Kind: KindCertificate, Name: "tls", Data: map[string]any{"namespace": "team", "apiVersion": "cert-manager.io/v1"}},
		{ID: "deployment/team/operator", Kind: KindDeployment, Name: "operator", Data: map[string]any{"namespace": "team"}},
	}
	nodes, edges, warnings := addCertificateIssuerEdges(nodes, nil, []unstructured.Unstructured{*cert}, p, DefaultBuildOptions())
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	builder := &Builder{dynamic: p}
	for i := 0; i < 2; i++ {
		nodes, edges = builder.addGenericCRDNodes(nodes, edges, DefaultBuildOptions())
		if len(nodes) != 3 || len(edges) != 2 {
			t.Fatalf("generic pass %d nodes=%d edges=%+v, want seeded issuer and unique dependency/owner", i, len(nodes), edges)
		}
	}
	rel := GetRelationshipsWithObject("Issuer", "team", "ca", issuer, &Topology{Nodes: nodes, Edges: edges}, nil, p, nil)
	if rel == nil || rel.Owner == nil || rel.Owner.Kind != "Deployment" || rel.Owner.Name != "operator" || len(rel.Dependents) != 1 {
		t.Fatalf("referenced issuer owner/dependent = %+v", rel)
	}
}

func TestIssuanceIssuerReferenceContracts(t *testing.T) {
	for _, tc := range []struct {
		group, kind string
		valid       bool
	}{
		{"cert-manager.io", "Certificate", true}, {"cert-manager.io", "CertificateRequest", true},
		{"acme.cert-manager.io", "Order", true}, {"acme.cert-manager.io", "Challenge", true},
		{"other.example", "CertificateRequest", false}, {"cert-manager.io", "Order", false}, {"acme.cert-manager.io", "Certificate", false},
	} {
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": tc.group + "/v1", "kind": tc.kind, "metadata": map[string]any{"name": "child", "namespace": "team"}, "spec": map[string]any{"issuerRef": map[string]any{"name": "ca"}}}}
		before := obj.DeepCopy()
		ref, ok := certificateIssuerDependency(obj)
		if ok != tc.valid {
			t.Fatalf("%s/%s valid=%v", tc.group, tc.kind, ok)
		}
		if ok && (ref.Source.Group != tc.group || ref.Source.Kind != tc.kind || ref.Target.Group != "cert-manager.io" || ref.Target.Kind != "Issuer" || ref.Target.Namespace != "team") {
			t.Fatalf("defaulted issuer = %+v", ref)
		}
		if !reflect.DeepEqual(obj, before) {
			t.Fatal("issuer parser mutated source")
		}
	}
}

func TestIssuanceIssuerJoinsAllObservedChildrenAndRetainsNewIssuerOwner(t *testing.T) {
	issuerGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}
	issuer := genericIdentityObject(issuerGVR, "Issuer", "team", "ca", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "operator", UID: "operator-current"})
	issuer.SetUID("issuer-current")
	resources := map[schema.GroupVersionResource][]*unstructured.Unstructured{issuerGVR: {issuer}}
	gvrs := map[string]schema.GroupVersionResource{"Issuer": issuerGVR}
	nodes := []Node{{uid: "operator-current", ID: "deployment/team/operator", Kind: KindDeployment, Name: "operator", Data: map[string]any{"namespace": "team"}}}
	for _, kind := range []string{"CertificateRequest", "Order", "Challenge"} {
		group := "acme.cert-manager.io"
		if kind == "CertificateRequest" {
			group = "cert-manager.io"
		}
		gvr := schema.GroupVersionResource{Group: group, Version: "v1", Resource: strings.ToLower(kind) + "s"}
		gvrs[kind] = gvr
		for i := 0; i < 30; i++ {
			name := fmt.Sprintf("child-%d", i)
			child := genericIdentityObject(gvr, kind, "team", name)
			child.Object["spec"] = map[string]any{"issuerRef": map[string]any{"name": "ca"}}
			resources[gvr] = append(resources[gvr], child)
			nodes = append(nodes, Node{ID: strings.ToLower(kind) + "/team/" + name + "/" + group, Kind: NodeKind(kind), Name: name, Data: map[string]any{"namespace": "team", "apiVersion": group + "/v1"}})
		}
	}
	p := &issuerTestProvider{monitorDynamicProvider: monitorDynamicProvider{gvrs: gvrs, resources: resources}, listCalls: map[string]int{}}
	nodes, edges, warnings := (&Builder{dynamic: p}).addIssuanceIssuerEdges(nodes, nil, DefaultBuildOptions())
	if len(warnings) != 0 || len(nodes) != 92 || len(edges) != 91 {
		t.Fatalf("issuance joins nodes=%d edges=%d warnings=%v", len(nodes), len(edges), warnings)
	}
	if p.getCalls != 0 || p.listCalls["issuers"] != 2 || p.listCalls["certificaterequests"] != 1 || p.listCalls["orders"] != 1 || p.listCalls["challenges"] != 1 {
		t.Fatalf("cache fanout: gets=%d lists=%v", p.getCalls, p.listCalls)
	}
	topo := &Topology{Nodes: nodes, Edges: edges}
	request := GetRelationships("CertificateRequest", "team", "child-0", topo, nil, p)
	if request == nil || len(request.Dependencies) != 1 || request.Dependencies[0].Kind != "Issuer" || request.Dependencies[0].Group != "cert-manager.io" {
		t.Fatalf("request dependencies = %+v", request)
	}
	issuerRel := GetRelationships("Issuer", "team", "ca", topo, nil, p)
	if issuerRel == nil || issuerRel.Owner == nil || issuerRel.Owner.Name != "operator" || len(issuerRel.Dependents) != 90 {
		t.Fatalf("issuer context = %+v", issuerRel)
	}
	if nodes[len(nodes)-1].uid != "issuer-current" {
		t.Fatal("late dependency lost observed UID")
	}
}

func TestAbsentIssuanceKindsStartNoCacheRead(t *testing.T) {
	p := &issuerTestProvider{monitorDynamicProvider: monitorDynamicProvider{}, listCalls: map[string]int{}}
	nodes, edges, warnings := (&Builder{dynamic: p}).addIssuanceIssuerEdges(nil, nil, DefaultBuildOptions())
	if len(nodes) != 0 || len(edges) != 0 || len(warnings) != 0 || len(p.listCalls) != 0 || p.getCalls != 0 {
		t.Fatalf("unused issuance kind did work: %+v %v", p.listCalls, warnings)
	}
}

func TestIssuanceIssuerSkipsUnrepresentedAndReplacedSources(t *testing.T) {
	requestGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificaterequests"}
	obj := genericIdentityObject(requestGVR, "CertificateRequest", "team", "request")
	obj.SetUID("new-request")
	obj.Object["spec"] = map[string]any{"issuerRef": map[string]any{"name": "ca"}}
	p := &issuerTestProvider{monitorDynamicProvider: monitorDynamicProvider{gvrs: map[string]schema.GroupVersionResource{"CertificateRequest": requestGVR, "Issuer": {Group: "cert-manager.io", Version: "v1", Resource: "issuers"}}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{requestGVR: {obj}}}, listCalls: map[string]int{}}
	nodes := []Node{{uid: "old-request", ID: "certificaterequest/team/request/cert-manager.io", Kind: "CertificateRequest", Name: "request", Data: map[string]any{"namespace": "team", "apiVersion": "cert-manager.io/v1"}}}
	nodes, edges, warnings := (&Builder{dynamic: p}).addIssuanceIssuerEdges(nodes, nil, DefaultBuildOptions())
	if len(nodes) != 1 || len(edges) != 0 || len(warnings) != 0 || p.listCalls["issuers"] != 0 {
		t.Fatalf("replaced source triggered issuer lookup: %+v %v %v", nodes, p.listCalls, warnings)
	}
}
