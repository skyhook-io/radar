package topology

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestIngressTLSSecretEdgesRespectNamespaceAndVisibility(t *testing.T) {
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "a"}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{SecretName: "tls"}, {SecretName: "tls"}, {}}}}
	other := ing.DeepCopy()
	other.Namespace = "b"
	provider := &mockProvider{ingresses: []*networkingv1.Ingress{ing, other}, secrets: []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "a"}}, {ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "b"}}}}
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"a"}
	opts.IncludeSecrets = true
	graph, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, edge := range graph.Edges {
		if edge.Source == "secret/a/tls" && edge.Target == "ingress/a/entry" && edge.Type == EdgeConfigures {
			count++
		}
		if edge.Source == "secret/b/tls" {
			t.Fatalf("cross-namespace Secret edge: %+v", edge)
		}
	}
	if count != 1 {
		t.Fatalf("TLS edges: %+v", graph.Edges)
	}
	rel := GetRelationships("Ingress", "a", "entry", graph, provider, nil)
	if rel == nil || len(rel.ConfigRefs) != 1 || rel.ConfigRefs[0].Name != "tls" || rel.ConfigRefs[0].Namespace != "a" {
		t.Fatalf("Ingress refs: %+v", rel)
	}
	rel = GetRelationships("Secret", "a", "tls", graph, provider, nil)
	if rel == nil || len(rel.Consumers) != 1 || rel.Consumers[0].Kind != "Ingress" || rel.Consumers[0].Group != "networking.k8s.io" {
		t.Fatalf("reverse refs: %+v", rel)
	}
	opts.IncludeSecrets = false
	graph, err = NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.Kind == KindSecret {
			t.Fatalf("Secret visibility ignored: %+v", node)
		}
	}
	provider.secrets = nil
	opts.IncludeSecrets = true
	graph, err = NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range graph.Edges {
		if edge.Source == "secret/a/tls" {
			t.Fatalf("unobserved Secret fabricated: %+v", edge)
		}
	}
}
