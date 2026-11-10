package cnpg

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// ConfigMaps, PVCs and PodDisruptionBudgets collected namespace by namespace
// have no typed lister until every namespace has loaded. A namespace that has loaded reads through its
// own store, and one still loading says so.
func TestDeferredKindsReadALoadedNamespaceWhileAnotherLoads(t *testing.T) {
	kinds := []string{"configmaps", "persistentvolumeclaims", "poddisruptionbudgets"}
	client := fake.NewClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "queries", Namespace: "alpha"}, Data: map[string]string{"query": "select 1"}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "db-1", Namespace: "alpha", Labels: map[string]string{clusterLabel: "db"}}},
		&policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "alpha", Labels: map[string]string{clusterLabel: "db"}, OwnerReferences: []metav1.OwnerReference{clusterRef("db", "db-uid")}}},
	)
	scopes, namespaces, enabled := map[string]k8score.ResourceScope{}, map[string][]string{}, map[string]bool{}
	for _, kind := range kinds {
		client.PrependReactor("list", kind, func(action ktesting.Action) (bool, runtime.Object, error) {
			if action.GetNamespace() == "gamma" {
				return true, nil, errors.New("gamma does not answer")
			}
			return false, nil, nil
		})
		scopes[kind] = k8score.ResourceScope{Enabled: true, Namespace: "alpha"}
		namespaces[kind] = []string{"alpha", "gamma"}
		enabled[kind] = true
	}
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
		Client:                  client,
		ResourceTypes:           enabled,
		ResourceScopes:          scopes,
		ResourceScopeNamespaces: namespaces,
		DeferredTypes:           enabled,
		DeferredSyncTimeout:     time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	cache := k8s.GetResourceCache()
	deadline := time.Now().Add(5 * time.Second)
	for _, kind := range kinds {
		for !cache.AssessKindRead(kind, []string{"alpha"}).Complete {
			if time.Now().After(deadline) {
				t.Fatalf("%s never loaded alpha", kind)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if cache.ConfigMaps() != nil || cache.PersistentVolumeClaims() != nil || cache.PodDisruptionBudgets() != nil {
		t.Fatal("the fixture must leave each kind loading as a whole")
	}
	ctx := context.Background()
	reader := activityReader()

	if ref := reader.operatorConfigMap(ctx, cache, "alpha", "queries", cnpgConfigPurposeMonitoring); !ref.Readable || ref.Data["query"] != "select 1" {
		t.Fatalf("ConfigMap in loaded alpha = %+v", ref.CNPGOperatorConfigMapState)
	}
	if ref := reader.operatorConfigMap(ctx, cache, "alpha", "missing", cnpgConfigPurposeMonitoring); ref.Exists == nil || *ref.Exists {
		t.Fatalf("a miss in loaded alpha is an absence: %+v", ref.CNPGOperatorConfigMapState)
	}
	if ref := reader.operatorConfigMap(ctx, cache, "gamma", "queries", cnpgConfigPurposeMonitoring); ref.Reason != "ConfigMaps are still loading" || ref.Exists != nil {
		t.Fatalf("ConfigMap in loading gamma = %+v", ref.CNPGOperatorConfigMapState)
	}

	if claims, reason := cnpgCachedClaims(cache, "alpha", labels.SelectorFromSet(labels.Set{clusterLabel: "db"})); reason != "" || len(claims) != 1 || claims[0].Name != "db-1" {
		t.Fatalf("claims in loaded alpha = %v %q", claims, reason)
	}
	if claims, reason := cnpgCachedClaims(cache, "gamma", labels.Everything()); claims != nil || reason != "Volumes unknown: Radar is still loading PersistentVolumeClaims" {
		t.Fatalf("claims in loading gamma = %v %q", claims, reason)
	}

	cluster := cnpgObj(Group+"/v1", "Cluster", "alpha", "db", nil, nil)
	cluster.SetUID(types.UID("db-uid"))
	if pdbs := reader.hAPDBs(ctx, cache, cluster); pdbs.ReadSource.State != cnpgHAStateOK || len(pdbs.Items) != 1 {
		t.Fatalf("PodDisruptionBudgets in loaded alpha = %+v", pdbs)
	}
	loading := cnpgObj(Group+"/v1", "Cluster", "gamma", "db", nil, nil)
	if pdbs := reader.hAPDBs(ctx, cache, loading); pdbs.ReadSource.State != cnpgHAStateUnavailable || pdbs.ReadSource.Reason != "Disruption budgets unknown: Radar is still loading PodDisruptionBudgets" {
		t.Fatalf("PodDisruptionBudgets in loading gamma = %+v", pdbs.ReadSource)
	}
}
