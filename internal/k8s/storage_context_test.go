package k8s

import (
	"context"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestStorageClassContextRetainsNamespaceLimitedClaims(t *testing.T) {
	class := "static"
	client := fake.NewClientset(
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "visible", Namespace: "team"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "hidden", Namespace: "other"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: class}},
	)
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        client,
		ResourceTypes: map[string]bool{k8score.PersistentVolumeClaims: true, k8score.PersistentVolumes: true},
		DeferredTypes: map[string]bool{},
		ResourceScopes: map[string]k8score.ResourceScope{
			k8score.PersistentVolumeClaims: {Enabled: true, Namespace: "team"},
			k8score.PersistentVolumes:      {Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	provider := NewTopologyResourceProvider(&ResourceCache{ResourceCache: core})
	obj := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: class}}
	rel := topology.GetRelationshipsWithObject("StorageClass", "", class, obj, &topology.Topology{}, provider, nil, nil)
	if rel == nil || len(rel.Warnings) != 1 || len(rel.Dependents) != 1 || rel.Dependents[0].Name != "visible" {
		t.Fatalf("namespace-limited store lost visible claims or implied completeness: %+v", rel)
	}
	got := resourcecontext.Build(context.Background(), obj, resourcecontext.Options{Relationships: rel})
	if len(got.Dependents) != 2 || !slices.Contains(got.Dependents, resourcecontext.ContextRef{Kind: "PersistentVolumeClaim", Namespace: "team", Name: "visible"}) || !slices.Contains(got.Dependents, resourcecontext.ContextRef{Kind: "PersistentVolume", Name: "disk"}) {
		t.Fatalf("context discarded visible storage evidence: %+v", got)
	}
	if !slices.Contains(got.Omitted, resourcecontext.OmittedField{Field: "relationships", Reason: resourcecontext.OmittedUnavailable}) {
		t.Fatalf("partial inventory appeared complete: %+v", got.Omitted)
	}
}
