package summarycontext

import (
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "web-new-a", Namespace: "shop", UID: "uid-web-new-a",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-new", UID: "uid-web-new", Controller: &controller}},
	}}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(rs("web-new", 3), rs("web-old", 0), sts, pod),
		ResourceTypes: map[string]bool{k8score.ReplicaSets: true, k8score.StatefulSets: true, k8score.Pods: true},
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
		podList, _ := cache.Pods().List(labels.Everything())
		if len(rsList) == 2 && len(stsList) == 1 && len(podList) == 1 {
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
			managedBy, scanned := trivyScanTarget(cache, tc.report, nil)
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

	if m, s := trivyScanTarget(cache, &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "r", "namespace": "shop"}}}, nil); m != nil || s != nil {
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

	managedBy, scanned := trivyScanTarget(cache, scanReport("ReplicaSet", "web-new", true), nil)
	if scanned == nil || scanned.Missing || scanned.Replicas != nil {
		t.Fatalf("a ReplicaSet the cache hasn't loaded yet must not read as missing: %+v", scanned)
	}
	if managedBy == nil || managedBy.Kind != "ReplicaSet" {
		t.Fatalf("managedBy = %+v", managedBy)
	}
}

// A row adds nothing the caller couldn't read: a scanned object they can't
// read is described only by the report's own coordinates, and a workload
// above a readable ReplicaSet is named only when they may read it too.
func TestTrivyScanTargetReadsOnlyWhatTheCallerMay(t *testing.T) {
	cache := scanTargetCache(t)
	deny := func(kinds ...string) func(string, string, string) bool {
		return func(_, kind, _ string) bool { return !slices.Contains(kinds, kind) }
	}
	managedBy, scanned := trivyScanTarget(cache, scanReport("ReplicaSet", "web-new", true), deny("ReplicaSet"))
	if managedBy == nil || managedBy.Kind != "ReplicaSet" || scanned.Replicas != nil || scanned.Missing {
		t.Fatalf("hidden ReplicaSet: managedBy %+v scanned %+v; want only the report's own coordinates", managedBy, scanned)
	}
	if _, gone := trivyScanTarget(cache, scanReport("ReplicaSet", "web-gone", true), deny("ReplicaSet")); gone.Missing {
		t.Fatalf("hidden ReplicaSet's existence leaked: %+v", gone)
	}
	managedBy, scanned = trivyScanTarget(cache, scanReport("ReplicaSet", "web-new", true), deny("Deployment"))
	if managedBy == nil || managedBy.Kind != "ReplicaSet" || scanned.Replicas == nil {
		t.Fatalf("hidden Deployment: managedBy %+v; want the readable ReplicaSet, not its hidden parent", managedBy)
	}
}

// A Pod report reaches the workload through the Pod's ReplicaSet: a caller
// who can't read that ReplicaSet doesn't get the workload it leads to.
func TestTrivyPodReportNeedsItsReplicaSetToBeReadable(t *testing.T) {
	cache := scanTargetCache(t)
	report := func() *unstructured.Unstructured {
		u := scanReport("Pod", "web-new-a", false)
		u.SetOwnerReferences(nil)
		return u
	}
	if m, _ := trivyScanTarget(cache, report(), nil); m == nil || m.Kind != "Deployment" {
		t.Fatalf("scenario: a readable chain resolves the Deployment, got %+v", m)
	}
	noRS := func(_, kind, _ string) bool { return kind != "ReplicaSet" }
	if m, _ := trivyScanTarget(cache, report(), noRS); m != nil {
		t.Fatalf("hidden ReplicaSet: managedBy %+v; want none", m)
	}
}
