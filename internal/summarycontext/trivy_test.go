package summarycontext

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func scanTargetCache(t *testing.T) *k8s.ResourceCache {
	t.Helper()
	controller := true
	rs := func(name string, replicas int32) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "shop", UID: types.UID("uid-" + name),
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &controller}},
			},
			Spec: appsv1.ReplicaSetSpec{Replicas: &replicas},
		}
	}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", UID: "uid-db"}}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(rs("web-new", 3), rs("web-old", 0), sts),
		ResourceTypes: map[string]bool{k8score.ReplicaSets: true, k8score.StatefulSets: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rsList, _ := cache.ReplicaSets().List(labels.Everything())
		stsList, _ := cache.StatefulSets().List(labels.Everything())
		if len(rsList) == 2 && len(stsList) == 1 {
			return cache
		}
		if time.Now().After(deadline) {
			t.Fatal("cache never synced")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// scanReport builds a report about ownerKind/ownerName. With an owner
// reference it names the fixture's UID ("uid-<name>") unless uid overrides it.
func scanReport(ownerKind, ownerName string, withOwnerRef bool, uid ...types.UID) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "VulnerabilityReport",
		"metadata": map[string]any{
			"name":      "report",
			"namespace": "shop",
			"labels": map[string]any{
				"trivy-operator.resource.kind":  ownerKind,
				"trivy-operator.resource.name":  ownerName,
				"trivy-operator.container.name": "php",
			},
		},
	}}
	if withOwnerRef {
		controller := true
		apiVersion := "apps/v1"
		ref := types.UID("uid-" + ownerName)
		if len(uid) > 0 {
			ref = uid[0]
		}
		u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: apiVersion, Kind: ownerKind, Name: ownerName, UID: ref, Controller: &controller}})
	}
	return u
}

func TestTrivyScanTarget(t *testing.T) {
	cache := scanTargetCache(t)
	three, zero := int32(3), int32(0)
	cases := []struct {
		name          string
		report        *unstructured.Unstructured
		wantManagedBy string // kind/name, "" for none
		wantReplicas  *int32
		wantMissing   bool
	}{
		{"active ReplicaSet resolves to its Deployment", scanReport("ReplicaSet", "web-new", true), "Deployment/web", &three, false},
		{"old ReplicaSet is flagged by its zero replicas", scanReport("ReplicaSet", "web-old", true), "Deployment/web", &zero, false},
		{"labels stand in for a missing owner reference", scanReport("ReplicaSet", "web-new", false), "Deployment/web", &three, false},
		{"deleted ReplicaSet is missing", scanReport("ReplicaSet", "web-gone", true), "", nil, true},
		{"StatefulSet is its own workload", scanReport("StatefulSet", "db", true), "StatefulSet/db", nil, false},
		{"deleted StatefulSet is missing", scanReport("StatefulSet", "db-gone", true), "", nil, true},
		{"a report about a Service names no workload", scanReport("Service", "web", true), "", nil, false},
		{"a ReplicaSet recreated under the same name is not the scanned one", scanReport("ReplicaSet", "web-new", true, "uid-deleted-rs"), "", nil, true},
		{"a StatefulSet recreated under the same name is not the scanned one", scanReport("StatefulSet", "db", true, "uid-deleted-sts"), "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			managedBy, scanned := trivyScanTarget(cache, tc.report)
			got := ""
			if managedBy != nil {
				got = managedBy.Kind + "/" + managedBy.Name
			}
			if got != tc.wantManagedBy {
				t.Fatalf("managedBy = %q, want %q", got, tc.wantManagedBy)
			}
			if scanned == nil || scanned.Container != "php" || scanned.Missing != tc.wantMissing {
				t.Fatalf("scanned = %+v", scanned)
			}
			if (scanned.Replicas == nil) != (tc.wantReplicas == nil) || (scanned.Replicas != nil && *scanned.Replicas != *tc.wantReplicas) {
				t.Fatalf("replicas = %v, want %v", scanned.Replicas, tc.wantReplicas)
			}
		})
	}

	if m, s := trivyScanTarget(cache, &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "r", "namespace": "shop"}}}); m != nil || s != nil {
		t.Fatalf("a report with no owner names nothing: %+v %+v", m, s)
	}
}

func TestTrivyScanTarget_UnsyncedInformerIsNotADeletion(t *testing.T) {
	client := fake.NewClientset()
	release := make(chan struct{})
	client.PrependReactor("list", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-release
		return false, nil, nil
	})
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        client,
		ResourceTypes: map[string]bool{k8score.ReplicaSets: true},
		DeferredTypes: map[string]bool{k8score.ReplicaSets: true},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(func() { close(release); core.Stop() })
	cache := &k8s.ResourceCache{ResourceCache: core}

	managedBy, scanned := trivyScanTarget(cache, scanReport("ReplicaSet", "web-new", true))
	if scanned == nil || scanned.Missing || scanned.Replicas != nil {
		t.Fatalf("a ReplicaSet the cache hasn't loaded yet must not read as missing: %+v", scanned)
	}
	if managedBy == nil || managedBy.Kind != "ReplicaSet" {
		t.Fatalf("managedBy = %+v", managedBy)
	}
}
