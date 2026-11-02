package k8s

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestStorageClassClaimsOutsideCollectedNamespaces(t *testing.T) {
	class := "fast"
	client := fake.NewClientset(
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: class}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "team"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class}},
	)
	scope := func(namespace string) map[string]k8score.ResourceScope {
		return map[string]k8score.ResourceScope{
			string(k8score.PersistentVolumes):      {Enabled: true},
			string(k8score.PersistentVolumeClaims): {Enabled: true, Namespace: namespace},
		}
	}
	for _, tc := range []struct {
		name      string
		namespace string
		want      resourcecontext.OmittedReason
	}{
		{"cluster-wide claims", "", ""},
		{"claims collected in one namespace", "team", resourcecontext.OmittedUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := k8score.CacheConfig{Client: client, ResourceScopes: scope(tc.namespace), DeferredTypes: map[string]bool{}}
			if tc.namespace != "" {
				cfg.ResourceScopeNamespaces = map[string][]string{string(k8score.PersistentVolumeClaims): {tc.namespace}}
			}
			core, err := k8score.NewResourceCache(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(core.Stop)
			cache := &ResourceCache{ResourceCache: core}
			deadline := time.Now().Add(2 * time.Second)
			for (cache.KindReadinessFor(string(k8score.PersistentVolumeClaims)) != k8score.KindReady || cache.KindReadinessFor(string(k8score.PersistentVolumes)) != k8score.KindReady) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			rc := resourcecontext.Build(context.Background(), &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: class}}, resourcecontext.Options{Topology: &topology.Topology{}, Provider: NewTopologyResourceProvider(cache)})
			if len(rc.Dependents) != 2 {
				t.Fatalf("class dependents = %+v", rc.Dependents)
			}
			gaps := slices.DeleteFunc(slices.Clone(rc.Omitted), func(f resourcecontext.OmittedField) bool { return f.Field != "dependents" })
			if tc.want == "" && len(gaps) != 0 || tc.want != "" && !slices.Contains(gaps, resourcecontext.OmittedField{Field: "dependents", Reason: tc.want}) {
				t.Fatalf("dependents omissions = %+v, want %q", gaps, tc.want)
			}
		})
	}
}
