package topology

import (
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
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

func TestIssuanceIssuerJoinsEnrolledStagesFromEnrollmentReads(t *testing.T) {
	issuerGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "operator", UID: "operator"}
	provider := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{issuerGVR},
		kinds:     map[schema.GroupVersionResource]string{issuerGVR: "Issuer"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{issuerGVR: {genericIdentityObject(issuerGVR, "Issuer", "team", "ca")}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	var stageGVRs []schema.GroupVersionResource
	for _, kind := range []string{"CertificateRequest", "Order", "Challenge"} {
		group := "acme.cert-manager.io"
		if kind == "CertificateRequest" {
			group = "cert-manager.io"
		}
		gvr := schema.GroupVersionResource{Group: group, Version: "v1", Resource: strings.ToLower(kind) + "s"}
		stageGVRs = append(stageGVRs, gvr)
		provider.watched = append(provider.watched, gvr)
		provider.kinds[gvr] = kind
		for i := 0; i < 30; i++ {
			child := genericIdentityObject(gvr, kind, "team", fmt.Sprintf("child-%d", i), owner)
			child.Object["spec"] = map[string]any{"issuerRef": map[string]any{"name": "ca"}}
			provider.resources[gvr] = append(provider.resources[gvr], child)
		}
		// Without an owner in the graph a stage is never enrolled, so its
		// issuerRef must not produce an edge either.
		orphan := genericIdentityObject(gvr, kind, "team", "orphan")
		orphan.Object["spec"] = map[string]any{"issuerRef": map[string]any{"name": "ca"}}
		provider.resources[gvr] = append(provider.resources[gvr], orphan)
	}
	nodes := []Node{{ID: "deployment/team/operator", Kind: KindDeployment, Name: "operator", Data: map[string]any{"namespace": "team"}}}
	b := &Builder{dynamic: provider}
	nodes, edges, issuance := b.enrollGenericCRDNodes(nodes, nil, DefaultBuildOptions(), nil)
	if len(issuance) != 90 {
		t.Fatalf("enrolled stages yielded %d issuer dependencies", len(issuance))
	}
	nodes, edges, warnings := addObservedDependencyEdges(nodes, edges, issuance, provider, DefaultBuildOptions())
	if len(warnings) != 0 || len(nodes) != 92 || len(edges) != 180 {
		t.Fatalf("issuance joins nodes=%d edges=%d warnings=%v", len(nodes), len(edges), warnings)
	}
	for _, gvr := range stageGVRs {
		if provider.listCalls[gvr] != 1 {
			t.Fatalf("stage kind %s listed %d times; the join must reuse enrollment's read", gvr.Resource, provider.listCalls[gvr])
		}
	}
	if provider.getCalls != 0 {
		t.Fatalf("issuer join fetched objects: %d", provider.getCalls)
	}
	topo := &Topology{Nodes: nodes, Edges: edges}
	request := GetRelationships("CertificateRequest", "team", "child-0", topo, nil, provider)
	if request == nil || len(request.Dependencies) != 1 || request.Dependencies[0].Kind != "Issuer" || request.Dependencies[0].Group != "cert-manager.io" {
		t.Fatalf("request dependencies = %+v", request)
	}
	issuerRel := GetRelationships("Issuer", "team", "ca", topo, nil, provider)
	if issuerRel == nil || len(issuerRel.Dependents) != 90 {
		t.Fatalf("issuer context = %+v", issuerRel)
	}
}

func TestIssuanceIssuerDependencyIgnoresCertificates(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	cert := genericIdentityObject(gvr, "Certificate", "team", "web")
	cert.Object["spec"] = map[string]any{"issuerRef": map[string]any{"name": "ca"}}
	if dep := issuanceIssuerDependency(cert); dep != nil {
		t.Fatalf("Certificate issuer is joined by addCertificateIssuerEdges, got %+v", dep)
	}
}

func TestLateIssuanceIssuerJoinDoesNotReopenGenericEnrollment(t *testing.T) {
	issuerGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}
	requestGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificaterequests"}
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "operator", UID: "operator"}
	var issuers []*unstructured.Unstructured
	for i := range 100 {
		issuers = append(issuers, genericIdentityObject(issuerGVR, "Issuer", "team", fmt.Sprintf("issuer-%d", i), owner))
	}
	target := genericIdentityObject(issuerGVR, "Issuer", "team", "ca", owner)
	issuers = append(issuers, target)
	request := genericIdentityObject(requestGVR, "CertificateRequest", "team", "request", owner)
	request.Object["spec"] = map[string]any{"issuerRef": map[string]any{"name": "ca"}}
	provider := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{issuerGVR, requestGVR},
		kinds:     map[schema.GroupVersionResource]string{issuerGVR: "Issuer", requestGVR: "CertificateRequest"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{issuerGVR: issuers, requestGVR: {request}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	parent := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "team", UID: "operator"}}
	topo, err := NewBuilder(&mockProvider{deployments: []*appsv1.Deployment{parent}}).WithDynamic(provider).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(topo.Nodes) != 53 || len(topo.Edges) != 52 || len(topo.Warnings) != 0 {
		t.Fatalf("late join reopened generic enrollment: nodes=%d edges=%+v warnings=%v", len(topo.Nodes), topo.Edges, topo.Warnings)
	}
	if provider.getCalls != 0 || provider.listCalls[issuerGVR] != 3 || provider.listCalls[requestGVR] != 2 {
		t.Fatalf("cache fanout: gets=%d lists=%v", provider.getCalls, provider.listCalls)
	}
	issuerRel := GetRelationships("Issuer", "team", "ca", topo, nil, provider)
	if issuerRel == nil || len(issuerRel.Dependents) != 1 || issuerRel.Dependents[0].Kind != "CertificateRequest" {
		t.Fatalf("late issuer lost its reverse dependency: %+v", issuerRel)
	}
}
