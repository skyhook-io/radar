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
	for _, node := range graph.Nodes {
		if node.Data["auditKey"] != "|Secret|"+node.Data["namespace"].(string)+"|"+node.Name {
			t.Fatalf("missing audit identity: %+v", node)
		}
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

func TestReflectionPromotesObservedStubEndpoints(t *testing.T) {
	for _, kind := range []string{"Secret", "ConfigMap"} {
		t.Run(kind, func(t *testing.T) {
			sourceMeta := metav1.ObjectMeta{Name: "source", Namespace: "app", ResourceVersion: "source-version", Labels: map[string]string{"app": "certificates"}}
			mirrorMeta := metav1.ObjectMeta{Name: "mirror", Namespace: "edge", ResourceVersion: "mirror-version", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "app/source"}}
			provider := &mockProvider{}
			nodeKind := KindConfigMap
			if kind == "Secret" {
				nodeKind = KindSecret
				provider.secrets = []*corev1.Secret{{ObjectMeta: sourceMeta, Data: map[string][]byte{"tls.crt": []byte("private-fixture")}}, {ObjectMeta: mirrorMeta}}
			} else {
				provider.configMaps = []*corev1.ConfigMap{{ObjectMeta: sourceMeta, Data: map[string]string{"config": "fixture"}}, {ObjectMeta: mirrorMeta}}
			}
			sourceID, mirrorID := strings.ToLower(kind)+"/app/source", strings.ToLower(kind)+"/edge/mirror"
			graph := &Topology{Nodes: []Node{
				{ID: sourceID, Kind: nodeKind, Name: "source", Status: StatusUnknown, Data: map[string]any{"namespace": "app"}},
				{ID: mirrorID, Kind: nodeKind, Name: "mirror", Status: StatusUnknown, Data: map[string]any{"namespace": "edge"}},
				{ID: "ingress/app/tls", Kind: KindIngress, Name: "tls", Data: map[string]any{"namespace": "app"}},
			}, Edges: []Edge{{ID: "tls-reference", Source: sourceID, Target: "ingress/app/tls", Type: EdgeConfigures}}}
			opts := DefaultBuildOptions()
			opts.IncludeSecrets = true
			NewBuilder(provider).addReflectionRelationships(graph, opts)
			if len(graph.Nodes) != 3 || len(graph.Edges) != 2 || graph.Edges[0].ID != "tls-reference" {
				t.Fatalf("lost or duplicated graph entries: %+v", graph)
			}
			if graph.Nodes[0].Data["resourceVersion"] != "source-version" || graph.Nodes[1].Data["resourceVersion"] != "mirror-version" || graph.Nodes[0].Data["keys"] != 1 {
				t.Fatalf("observed metadata missing: %+v", graph.Nodes)
			}
			for _, node := range graph.Nodes[:2] {
				if node.Data["auditKey"] != "|"+kind+"|"+node.Data["namespace"].(string)+"|"+node.Name {
					t.Fatalf("lost audit identity: %+v", node)
				}
			}
			rel := GetRelationships(kind, "edge", "mirror", graph, provider, nil)
			if rel == nil || rel.Reflection == nil || rel.Reflection.SourceResourceVersion != "source-version" {
				t.Fatalf("lost source evidence: %+v", rel)
			}
			encoded, _ := json.Marshal(graph)
			if strings.Contains(string(encoded), "private-fixture") || strings.Contains(string(encoded), "tls.crt") {
				t.Fatal("secret contents in metadata graph")
			}
		})
	}
}

func TestReflectionDoesNotPromoteUnobservedStub(t *testing.T) {
	mirror := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mirror", Namespace: "edge", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "app/source"}}}
	provider := &mockProvider{secrets: []*corev1.Secret{mirror}}
	graph := &Topology{Nodes: []Node{{ID: "secret/app/source", Kind: KindSecret, Name: "source", Status: StatusUnknown, Data: map[string]any{"namespace": "app"}}}}
	opts := DefaultBuildOptions()
	opts.IncludeSecrets = true
	NewBuilder(provider).addReflectionRelationships(graph, opts)
	if len(graph.Nodes) != 1 || len(graph.Edges) != 0 || graph.Nodes[0].Data["resourceVersion"] != nil || graph.Nodes[0].Status != StatusUnknown {
		t.Fatalf("invented observation: %+v", graph)
	}
}
