package k8score

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReflectsIndexFilesMirrorsBySource(t *testing.T) {
	mirror := func(namespace, name, reflects string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": reflects}}}
	}
	client := fake.NewClientset(
		mirror("alpha", "registry", "platform/registry"), mirror("beta", "registry", "platform/registry"), mirror("beta", "bad", "not-a-ref"),
	)
	rc, err := NewResourceCache(CacheConfig{Client: client, ResourceTypes: map[string]bool{Secrets: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Stop()
	if got, ok := rc.ReadableIndexedObjects(Secrets, nil, ReflectsIndex, ReflectsKey("platform", "registry")); !ok || !slices.Equal(objectNames(got), []string{"registry", "registry"}) {
		t.Errorf("platform/registry's mirrors = %v, %v; want both", objectNames(got), ok)
	}
}
