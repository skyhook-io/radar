package k8s

import (
	"errors"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
)

func controllerRef(kind, name string) metav1.OwnerReference {
	isController := true
	return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &isController}
}

func ownedPod(namespace, name string, owner *metav1.OwnerReference, podLabels map[string]string) *corev1.Pod {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: podLabels}}
	if owner != nil {
		pod.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	return pod
}

func newOwnershipCache(t *testing.T, types map[string]bool, objects ...runtime.Object) *ResourceCache {
	t.Helper()
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(objects...),
		ResourceTypes: types,
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	return &ResourceCache{ResourceCache: core}
}

func TestWorkloadPodsFollowsControllerOwnership(t *testing.T) {
	appLabels := map[string]string{"app": "api"}
	apiRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6", OwnerReferences: []metav1.OwnerReference{controllerRef("Deployment", "api")}}}
	workerRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-worker-9c1", OwnerReferences: []metav1.OwnerReference{controllerRef("Deployment", "api-worker")}}}
	rolloutRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-rollout-a1b", OwnerReferences: []metav1.OwnerReference{controllerRef("Rollout", "api")}}}
	apiOwner := controllerRef("ReplicaSet", "api-7f6")
	workerOwner := controllerRef("ReplicaSet", "api-worker-9c1")
	rolloutOwner := controllerRef("ReplicaSet", "api-rollout-a1b")
	stsOwner := controllerRef("StatefulSet", "db")
	nightlyJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "nightly-28800", OwnerReferences: []metav1.OwnerReference{controllerRef("CronJob", "nightly")}}}
	adHocJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "nightly-manual"}}
	nightlyOwner := controllerRef("Job", "nightly-28800")
	adHocOwner := controllerRef("Job", "nightly-manual")
	replicaStsOwner := controllerRef("StatefulSet", "db-replica")

	cache := newOwnershipCache(t,
		map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true, k8score.Jobs: true},
		apiRS, workerRS, rolloutRS, nightlyJob, adHocJob,
		ownedPod("shop", "api-7f6-b", &apiOwner, appLabels),
		ownedPod("shop", "api-7f6-a", &apiOwner, appLabels),
		ownedPod("shop", "api-worker-9c1-x", &workerOwner, appLabels),
		ownedPod("shop", "api-rollout-a1b-r", &rolloutOwner, appLabels),
		ownedPod("shop", "api-debug", nil, appLabels),
		ownedPod("shop", "db-0", &stsOwner, nil),
		ownedPod("shop", "db-replica-0", &replicaStsOwner, nil),
		ownedPod("shop", "nightly-28800-k", &nightlyOwner, nil),
		ownedPod("shop", "nightly-manual-m", &adHocOwner, nil),
		ownedPod("other", "api-7f6-z", &apiOwner, appLabels),
	)

	for _, tc := range []struct {
		kind, name string
		want       []string
	}{
		{"deployment", "api", []string{"api-7f6-a", "api-7f6-b"}},
		{"Deployment", "api-worker", []string{"api-worker-9c1-x"}},
		{"replicasets", "api-7f6", []string{"api-7f6-a", "api-7f6-b"}},
		{"statefulset", "db", []string{"db-0"}},
		{"StatefulSet", "db-replica", []string{"db-replica-0"}},
		{"cronjob", "nightly", []string{"nightly-28800-k"}},
		{"job", "nightly-manual", []string{"nightly-manual-m"}},
		{"daemonset", "absent", []string{}},
	} {
		t.Run(tc.kind+"/"+tc.name, func(t *testing.T) {
			got, err := WorkloadPodNames(cache, tc.kind, "shop", tc.name)
			if err != nil {
				t.Fatalf("WorkloadPodNames: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pods = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorkloadPodsReportsDeniedListersInsteadOfEmpty(t *testing.T) {
	apiOwner := controllerRef("ReplicaSet", "api-7f6")
	noReplicaSets := newOwnershipCache(t,
		map[string]bool{k8score.Pods: true},
		ownedPod("shop", "api-7f6-a", &apiOwner, nil),
	)
	if _, err := WorkloadPods(noReplicaSets, "deployment", "shop", "api"); !errors.Is(err, ErrWorkloadAccessDenied) {
		t.Fatalf("deployment without replicasets lister: err = %v, want ErrWorkloadAccessDenied", err)
	}
	if _, err := WorkloadPods(noReplicaSets, "cronjob", "shop", "nightly"); !errors.Is(err, ErrWorkloadAccessDenied) {
		t.Fatalf("cronjob without jobs lister: err = %v, want ErrWorkloadAccessDenied", err)
	}
	if _, err := WorkloadPods(&ResourceCache{}, "statefulset", "shop", "db"); !errors.Is(err, ErrWorkloadAccessDenied) {
		t.Fatalf("no pods lister: err = %v, want ErrWorkloadAccessDenied", err)
	}
	if _, err := WorkloadPods(noReplicaSets, "service", "shop", "api"); err == nil {
		t.Fatal("unsupported kind: want error")
	}
}

// newScopedOwnershipCache builds a cache whose informers are namespace-scoped,
// the shape probe-based RBAC gating produces when cluster-wide list is denied.
func newScopedOwnershipCache(t *testing.T, scopes map[string]k8score.ResourceScope, objects ...runtime.Object) *ResourceCache {
	t.Helper()
	types := map[string]bool{}
	for kind, scope := range scopes {
		if scope.Enabled {
			types[kind] = true
		}
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:         fake.NewClientset(objects...),
		ResourceTypes:  types,
		DeferredTypes:  map[string]bool{},
		ResourceScopes: scopes,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	return &ResourceCache{ResourceCache: core}
}

// An informer scoped to another namespace answers an empty list, not an error.
// Reading that as "this workload has no pods" states a fact the cache never
// established — the same mistake as trusting a denied lister, reached through
// scope instead of permission.
func TestWorkloadPodsRefuseNamespacesTheInformersDoNotCover(t *testing.T) {
	apiOwner := controllerRef("ReplicaSet", "api-7f6")
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b", Name: "api-7f6", OwnerReferences: []metav1.OwnerReference{controllerRef("Deployment", "api")}}}

	// Pods cover team-b; ReplicaSets watch only team-a. The parent lookup for
	// every team-b pod misses, which would read as a workload with no pods.
	hopElsewhere := newScopedOwnershipCache(t,
		map[string]k8score.ResourceScope{
			k8score.Pods:        {Enabled: true, Namespace: "team-b"},
			k8score.ReplicaSets: {Enabled: true, Namespace: "team-a"},
		},
		ownedPod("team-b", "api-7f6-a", &apiOwner, nil), rs,
	)
	if _, err := WorkloadPods(hopElsewhere, "deployment", "team-b", "api"); !errors.Is(err, ErrWorkloadAccessDenied) {
		t.Fatalf("replicasets scoped to another namespace: err = %v, want ErrWorkloadAccessDenied", err)
	}

	// The kinds needing no hop go through the same pod lister, so a pod
	// informer that does not reach the namespace is refused for them too.
	podsElsewhere := newScopedOwnershipCache(t,
		map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "team-a"}},
		ownedPod("team-b", "db-0", nil, nil),
	)
	if _, err := WorkloadPods(podsElsewhere, "statefulset", "team-b", "db"); !errors.Is(err, ErrWorkloadAccessDenied) {
		t.Fatalf("pods scoped to another namespace: err = %v, want ErrWorkloadAccessDenied", err)
	}

	// The namespace it does cover still answers.
	covered := newScopedOwnershipCache(t,
		map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "team-a"}},
		ownedPod("team-a", "db-0", &metav1.OwnerReference{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "db", Controller: func() *bool { b := true; return &b }()}, nil),
	)
	pods, err := WorkloadPodNames(covered, "statefulset", "team-a", "db")
	if err != nil {
		t.Fatalf("covered namespace: %v", err)
	}
	if !reflect.DeepEqual(pods, []string{"db-0"}) {
		t.Fatalf("covered namespace returned %v, want [db-0]", pods)
	}
}

// A Rollout migrating from a Deployment with workloadRef leaves both
// controllers running pods that match the same labels. Ownership is what
// separates them.
func TestWorkloadPodsSeparatesARolloutFromItsReferencedDeployment(t *testing.T) {
	appLabels := map[string]string{"app": "api"}
	deployRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-5d2", Labels: appLabels, OwnerReferences: []metav1.OwnerReference{controllerRef("Deployment", "api")}}}
	rolloutRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6", Labels: appLabels, OwnerReferences: []metav1.OwnerReference{controllerRef("Rollout", "api")}}}
	deployPodOwner := controllerRef("ReplicaSet", "api-5d2")
	rolloutPodOwner := controllerRef("ReplicaSet", "api-7f6")
	cache := newOwnershipCache(t,
		map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true},
		deployRS, rolloutRS,
		ownedPod("shop", "api-5d2-a", &deployPodOwner, appLabels),
		ownedPod("shop", "api-7f6-a", &rolloutPodOwner, appLabels),
	)

	rollout, err := WorkloadPodNames(cache, "rollout", "shop", "api")
	if err != nil {
		t.Fatalf("rollout: %v", err)
	}
	if len(rollout) != 1 || rollout[0] != "api-7f6-a" {
		t.Fatalf("rollout pods = %v, want only the Rollout's", rollout)
	}
	deployment, err := WorkloadPodNames(cache, "deployment", "shop", "api")
	if err != nil {
		t.Fatalf("deployment: %v", err)
	}
	if len(deployment) != 1 || deployment[0] != "api-5d2-a" {
		t.Fatalf("deployment pods = %v, want only the Deployment's", deployment)
	}
}

// A ReplicaSet name is unique only within its namespace, so the owner lookup
// must stay in the pod's namespace.
func TestWorkloadPodsDoNotCrossNamespaces(t *testing.T) {
	shopRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6", OwnerReferences: []metav1.OwnerReference{controllerRef("Deployment", "api")}}}
	otherRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "api-7f6", OwnerReferences: []metav1.OwnerReference{controllerRef("Deployment", "unrelated")}}}
	owner := controllerRef("ReplicaSet", "api-7f6")
	cache := newOwnershipCache(t,
		map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true},
		shopRS, otherRS,
		ownedPod("shop", "api-7f6-a", &owner, nil),
		ownedPod("other", "api-7f6-b", &owner, nil),
	)
	got, err := WorkloadPodNames(cache, "deployment", "shop", "api")
	if err != nil {
		t.Fatalf("WorkloadPodNames: %v", err)
	}
	if len(got) != 1 || got[0] != "api-7f6-a" {
		t.Fatalf("pods = %v, want only the shop namespace's", got)
	}
}
