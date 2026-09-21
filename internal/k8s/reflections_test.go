package k8s

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReflectionLookupCrossNamespace(t *testing.T) {
	const prefix = "reflector.v1.k8s.emberstack.com/"
	client := fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "shared", ResourceVersion: "secret-rv"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mirror", Namespace: "app", Annotations: map[string]string{prefix + "reflects": "shared/source"}}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "shared", ResourceVersion: "cm-rv"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "other-name", Namespace: "app", Annotations: map[string]string{prefix + "reflects": "shared/source"}}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "dangling", Namespace: "app", Annotations: map[string]string{prefix + "reflects": "missing/source"}}},
	)
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: client, ResourceScopes: map[string]k8score.ResourceScope{string(k8score.Secrets): {Enabled: true}, string(k8score.ConfigMaps): {Enabled: true}}, DeferredTypes: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	lookup := ReflectionLookup{Cache: &ResourceCache{ResourceCache: core}}
	for _, tt := range []struct{ kind, name, version string }{{"Secret", "mirror", "secret-rv"}, {"ConfigMap", "other-name", "cm-rv"}} {
		t.Run(tt.kind, func(t *testing.T) {
			facts, err := lookup.For(tt.kind, "app", tt.name)
			if err != nil || facts.Source == nil || facts.Source.Namespace != "shared" || facts.SourceResourceVersion != tt.version {
				t.Fatalf("cross-namespace source: %+v, %v", facts, err)
			}
			facts, err = lookup.For(tt.kind, "shared", "source")
			if err != nil || len(facts.Mirrors) != 1 || facts.Mirrors[0].Name != tt.name {
				t.Fatalf("cross-namespace mirrors: %+v, %v", facts, err)
			}
		})
	}
	facts, err := lookup.For("ConfigMap", "app", "dangling")
	if err != nil || facts.Source != nil {
		t.Fatalf("invented source: %+v %v", facts, err)
	}
}
