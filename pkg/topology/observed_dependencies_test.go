package topology

import (
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestObservedDependenciesReuseIdentityAndDeduplicate(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}
	issuer := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Issuer", "metadata": map[string]any{"namespace": "team", "name": "ca"}}}
	p := &issuerTestProvider{monitorDynamicProvider: monitorDynamicProvider{gvrs: map[string]schema.GroupVersionResource{"Issuer": gvr}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {issuer}}}, listCalls: map[string]int{}}
	source := resourceid.NewRef("consumer.example", "Consumer", "team", "app")
	target := resourceid.NewRef("cert-manager.io", "Issuer", "team", "ca")
	nodes := []Node{
		{ID: "consumer/team/app/consumer.example", Kind: "Consumer", Name: "app", Data: map[string]any{"namespace": "team", "apiVersion": "consumer.example/v1"}},
		{ID: "existing-issuer-id", Kind: "Issuer", Name: "ca", Data: map[string]any{"namespace": "team", "apiVersion": "cert-manager.io/v1"}},
	}
	refs := []declaredDependency{{Source: source, Target: target, Label: "declared"}, {Source: source, Target: target, Label: "declared"},
		{Source: source, Target: resourceid.NewRef("cert-manager.io", "Issuer", "other", "ca")},
		{Source: source, Target: resourceid.NewRef("cert-manager.io", "Issuer", "team", "absent")},
		{Source: resourceid.NewRef("consumer.example", "Consumer", "team", "missing"), Target: target},
	}
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"team"}
	nodes, edges, warnings := addObservedDependencyEdges(nodes, nil, refs, p, opts)
	if len(nodes) != 2 || len(edges) != 1 || len(warnings) != 0 || edges[0].Target != "existing-issuer-id" || edges[0].Label != "declared" {
		t.Fatalf("observed joins: nodes=%+v edges=%+v warnings=%v", nodes, edges, warnings)
	}
	nodes, edges, warnings = addObservedDependencyEdges(nodes, edges, refs, p, opts)
	if len(nodes) != 2 || len(edges) != 1 || len(warnings) != 0 || p.getCalls != 0 || p.listCalls["issuers"] != 2 {
		t.Fatalf("repeat/fanout: nodes=%d edges=%d warnings=%v gets=%d lists=%v", len(nodes), len(edges), warnings, p.getCalls, p.listCalls)
	}
}

func TestObservedDependenciesSkipAbsentSourcesAndUnreferencedKinds(t *testing.T) {
	p := &issuerTestProvider{listCalls: map[string]int{}}
	refs := []declaredDependency{{Source: resourceid.NewRef("consumer.example", "Consumer", "team", "absent"), Target: resourceid.NewRef("cert-manager.io", "Issuer", "team", "ca")}}
	nodes, edges, warnings := addObservedDependencyEdges(nil, nil, refs, p, DefaultBuildOptions())
	if len(nodes) != 0 || len(edges) != 0 || len(warnings) != 0 || len(p.listCalls) != 0 || p.getCalls != 0 {
		t.Fatalf("absent source started target lookup: %+v %+v %v", nodes, edges, warnings)
	}
}
