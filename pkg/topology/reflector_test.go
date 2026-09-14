package topology

import (
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"slices"
	"testing"
)

func TestReflectionTopologyAndVisibility(t *testing.T) {
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "app"}}
	mirror := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mirror", Namespace: "edge", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "app/source"}}}
	builder := NewBuilder(&mockProvider{secrets: []*corev1.Secret{source, mirror}})
	for _, tc := range []struct {
		name       string
		enabled    bool
		namespaces []string
		nodes      int
	}{{"hidden", false, nil, 0}, {"both", true, nil, 2}, {"source view", true, []string{"app"}, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			graph := &Topology{}
			opts := DefaultBuildOptions()
			opts.IncludeSecrets = tc.enabled
			opts.Namespaces = tc.namespaces
			builder.addReflectionRelationships(graph, opts)
			if len(graph.Nodes) != tc.nodes {
				t.Fatalf("nodes=%d want %d", len(graph.Nodes), tc.nodes)
			}
			for _, edge := range graph.Edges {
				if edge.Type == EdgeManages {
					t.Fatal("cascade ownership")
				}
			}
			graph.StripSecretsExcept(map[SARTuple]bool{{Resource: "secrets", Namespace: "app"}: true})
			if len(graph.Edges) != 0 {
				t.Fatal("dangling edge")
			}
			for _, n := range graph.Nodes {
				if nodeNamespaceFromData(&n) == "edge" {
					t.Fatal("denied Secret")
				}
			}
		})
	}
}

func TestLargeClusterRetainsReflectionRelationships(t *testing.T) {
	source := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "app"}}
	mirror := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "mirror", Namespace: "edge", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "app/source"}}}
	provider := &mockProvider{configMaps: []*corev1.ConfigMap{source, mirror}}
	for i := 0; i < LargeClusterThreshold; i++ {
		provider.deployments = append(provider.deployments, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("app-%d", i), Namespace: "app"}})
	}
	builder := NewBuilder(provider)
	opts := DefaultBuildOptions()
	opts.ForRelationshipCache = true
	graph, err := builder.Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, edge := range graph.Edges {
		if edge.Label == "Reflects to" {
			found = true
		}
	}
	if !found || slices.Contains(graph.HiddenKinds, "ConfigMap") {
		t.Fatal("large relationship cache dropped reflection")
	}
	opts = DefaultBuildOptions()
	_, hidden, _ := builder.detectLargeClusterAndOptimize(&opts)
	if opts.IncludeConfigMaps || !slices.Contains(hidden, "ConfigMap") {
		t.Fatal("visible topology lost large-cluster optimization")
	}
	opts = DefaultBuildOptions()
	opts.ForRelationshipCache = true
	builder.detectLargeClusterAndOptimize(&opts)
	if !opts.IncludeConfigMaps || !opts.IncludePVCs {
		t.Fatal("display optimizations removed relationship inventories")
	}
}
