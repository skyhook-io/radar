package topology

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReflectionDetailEvidence(t *testing.T) {
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "app", ResourceVersion: "rv-opaque"}, Data: map[string][]byte{"password": []byte("do-not-expose")}}
	mirror := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "different", Namespace: "edge", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "app/source"}}}
	provider := &mockProvider{secrets: []*corev1.Secret{source, mirror}}
	graph, err := NewBuilder(provider).Build(BuildOptions{ViewMode: ViewModeResources, IncludeSecrets: true, ForRelationshipCache: true})
	if err != nil {
		t.Fatal(err)
	}
	rel := GetRelationships("Secret", "edge", "different", graph, provider, nil)
	if rel == nil || rel.Reflection == nil || rel.Reflection.Source == nil || rel.Reflection.Source.Name != "source" || rel.Reflection.SourceResourceVersion != "rv-opaque" {
		t.Fatalf("missing source evidence: %+v", rel)
	}
	if len(rel.ConfigRefs) != 1 {
		t.Fatal("existing API consumer lost source reference")
	}
	sourceRel := GetRelationships("Secret", "app", "source", graph, provider, nil)
	if sourceRel.Reflection == nil || len(sourceRel.Reflection.Mirrors) != 1 || sourceRel.Reflection.Mirrors[0].Name != "different" {
		t.Fatalf("missing mirrors: %+v", sourceRel)
	}
	bytes, _ := json.Marshal(graph)
	if strings.Contains(string(bytes), "do-not-expose") || strings.Contains(string(bytes), "password") {
		t.Fatal("secret payload in graph")
	}
	graph.StripSecretsExcept(map[SARTuple]bool{{Resource: "secrets", Namespace: "edge"}: true})
	restricted := GetRelationships("Secret", "edge", "different", graph, provider, nil)
	if restricted != nil && restricted.Reflection != nil {
		t.Fatalf("hidden source evidence leaked: %+v", restricted.Reflection)
	}
}

func TestSameKindConfigurationIsNotNecessarilyReflection(t *testing.T) {
	graph := &Topology{Nodes: []Node{
		{ID: "configmap/ns/a", Kind: KindConfigMap, Name: "a", Data: map[string]any{"namespace": "ns"}},
		{ID: "configmap/ns/b", Kind: KindConfigMap, Name: "b", Data: map[string]any{"namespace": "ns"}},
	}, Edges: []Edge{{Source: "configmap/ns/a", Target: "configmap/ns/b", Type: EdgeConfigures, Label: "Configures"}}}
	rel := GetRelationships("ConfigMap", "ns", "b", graph, &mockProvider{}, nil)
	if rel == nil || len(rel.ConfigRefs) != 1 || rel.Reflection != nil {
		t.Fatalf("invented reflection: %+v", rel)
	}
}
