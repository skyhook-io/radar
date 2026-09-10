package prometheus

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

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func scopeTestCache(t *testing.T, types map[string]bool, objects ...runtime.Object) *k8s.ResourceCache {
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
	return &k8s.ResourceCache{ResourceCache: core}
}

func scopeOwner(kind, name string) metav1.OwnerReference {
	isController := true
	return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &isController}
}

// Two Deployments whose names share a prefix. Membership must follow the
// ReplicaSet edge, so `api` never picks up `api-worker`'s pod.
func scopeFixture(t *testing.T) *k8s.ResourceCache {
	t.Helper()
	return scopeTestCache(t,
		map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6", OwnerReferences: []metav1.OwnerReference{scopeOwner("Deployment", "api")}}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-worker-9c1", OwnerReferences: []metav1.OwnerReference{scopeOwner("Deployment", "api-worker")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6-b", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "api-7f6")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6-a", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "api-7f6")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-worker-9c1-x", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "api-worker-9c1")}}},
	)
}

func TestResolvePodScopeNamesOnlyTheWorkloadsOwnPods(t *testing.T) {
	scope, err := ResolvePodScope(scopeFixture(t), "deployment", "shop", "api", 0)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if !reflect.DeepEqual(scope.CurrentPods, []string{"api-7f6-a", "api-7f6-b"}) {
		t.Fatalf("pods = %v, want the two api pods and not api-worker's", scope.CurrentPods)
	}
	if scope.CurrentTotal != 2 || scope.Partial() {
		t.Fatalf("total = %d partial = %v, want 2/false", scope.CurrentTotal, scope.Partial())
	}
	if got := scope.Selection.Pods; !reflect.DeepEqual(got, []string{"api-7f6-a", "api-7f6-b"}) {
		t.Fatalf("selection = %v, want the same two pods", got)
	}
}

func TestResolvePodScopeDirectOwnersNeedNoHop(t *testing.T) {
	cache := scopeTestCache(t,
		map[string]bool{k8score.Pods: true},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db-0", OwnerReferences: []metav1.OwnerReference{scopeOwner("StatefulSet", "db")}}},
	)
	scope, err := ResolvePodScope(cache, "statefulset", "shop", "db", 0)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if !reflect.DeepEqual(scope.CurrentPods, []string{"db-0"}) {
		t.Fatalf("pods = %v, want [db-0]", scope.CurrentPods)
	}
}

func TestResolvePodScopeResolvesACronJobThroughItsJobs(t *testing.T) {
	cache := scopeTestCache(t,
		map[string]bool{k8score.Pods: true, k8score.Jobs: true},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "nightly-28", OwnerReferences: []metav1.OwnerReference{scopeOwner("CronJob", "nightly")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "nightly-28-abc", OwnerReferences: []metav1.OwnerReference{scopeOwner("Job", "nightly-28")}}},
	)
	scope, err := ResolvePodScope(cache, "cronjob", "shop", "nightly", 0)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if !reflect.DeepEqual(scope.CurrentPods, []string{"nightly-28-abc"}) {
		t.Fatalf("pods = %v, want the job's pod", scope.CurrentPods)
	}
}

// A Pod names itself. Its "<name>-" prefix selects other pods, never itself,
// so there is no ownership to resolve.
func TestResolvePodScopeTreatsAPodAsItsOwnScope(t *testing.T) {
	scope, err := ResolvePodScope(nil, "pod", "shop", "api-7f6-a", 0)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if !reflect.DeepEqual(scope.CurrentPods, []string{"api-7f6-a"}) {
		t.Fatalf("pods = %v, want itself", scope.CurrentPods)
	}
}

// The cap bounds the regex a chart sends. It must say the list was cut, or a
// caller reads a short list as the whole workload.
func TestResolvePodScopeReportsACutPodList(t *testing.T) {
	scope, err := ResolvePodScope(scopeFixture(t), "deployment", "shop", "api", 1)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if len(scope.CurrentPods) != 1 || scope.CurrentTotal != 2 || !scope.Partial() {
		t.Fatalf("pods = %v total = %d partial = %v, want 1/2/true", scope.CurrentPods, scope.CurrentTotal, scope.Partial())
	}
}

func TestResolvePodScopeRefusesKindsWithNoOwnership(t *testing.T) {
	if _, err := ResolvePodScope(scopeFixture(t), "service", "shop", "api", 0); !errors.Is(err, ErrPodScopeUnsupportedKind) {
		t.Fatalf("service: err = %v, want ErrPodScopeUnsupportedKind", err)
	}
}

// A cache that cannot answer is an error, never an empty scope: "no pods" is
// only ever said when it is true.
func TestResolvePodScopeReportsAnUnreadableCache(t *testing.T) {
	noReplicaSets := scopeTestCache(t,
		map[string]bool{k8score.Pods: true},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6-a", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "api-7f6")}}},
	)
	if _, err := ResolvePodScope(noReplicaSets, "deployment", "shop", "api", 0); !errors.Is(err, k8s.ErrWorkloadAccessDenied) {
		t.Fatalf("deployment without a replicasets lister: err = %v, want ErrWorkloadAccessDenied", err)
	}
}
