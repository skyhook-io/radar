package prometheus

import (
	"context"
	"errors"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	api := "apps/v1"
	if kind == "Job" || kind == "CronJob" {
		api = "batch/v1"
	}
	return metav1.OwnerReference{APIVersion: api, Kind: kind, Name: name, UID: types.UID(name + "-uid"), Controller: &isController}
}

// Two Deployments whose names share a prefix. Membership must follow the
// ReplicaSet edge, so `api` never picks up `api-worker`'s pod.
func scopeFixture(t *testing.T) *k8s.ResourceCache {
	t.Helper()
	return scopeTestCache(t,
		map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true, k8score.Deployments: true},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api", UID: "api-uid"}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6", UID: "api-7f6-uid", OwnerReferences: []metav1.OwnerReference{scopeOwner("Deployment", "api")}}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-worker-9c1", UID: "api-worker-9c1-uid", OwnerReferences: []metav1.OwnerReference{scopeOwner("Deployment", "api-worker")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6-b", UID: "api-7f6-b-uid", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "api-7f6")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6-a", UID: "api-7f6-a-uid", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "api-7f6")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-worker-9c1-x", UID: "api-worker-9c1-x-uid", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "api-worker-9c1")}}},
	)
}

func TestResolvePodScopeNamesOnlyTheWorkloadsOwnPods(t *testing.T) {
	scope, err := ResolvePodScope(context.Background(), scopeFixture(t), "deployment", "shop", "api", 0)
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
		map[string]bool{k8score.Pods: true, k8score.StatefulSets: true},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db", UID: "db-uid"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db-0", OwnerReferences: []metav1.OwnerReference{scopeOwner("StatefulSet", "db")}}},
	)
	scope, err := ResolvePodScope(context.Background(), cache, "statefulset", "shop", "db", 0)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if !reflect.DeepEqual(scope.CurrentPods, []string{"db-0"}) {
		t.Fatalf("pods = %v, want [db-0]", scope.CurrentPods)
	}
}

func TestResolvePodScopeResolvesACronJobThroughItsJobs(t *testing.T) {
	cache := scopeTestCache(t,
		map[string]bool{k8score.Pods: true, k8score.Jobs: true, k8score.CronJobs: true},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "nightly", UID: "nightly-uid"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "nightly-28", UID: "nightly-28-uid", OwnerReferences: []metav1.OwnerReference{scopeOwner("CronJob", "nightly")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "nightly-28-abc", OwnerReferences: []metav1.OwnerReference{scopeOwner("Job", "nightly-28")}}},
	)
	scope, err := ResolvePodScope(context.Background(), cache, "cronjob", "shop", "nightly", 0)
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
	scope, err := ResolvePodScope(context.Background(), nil, "pod", "shop", "api-7f6-a", 0)
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
	scope, err := ResolvePodScope(context.Background(), scopeFixture(t), "deployment", "shop", "api", 1)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if len(scope.CurrentPods) != 1 || scope.CurrentTotal != 2 || !scope.Partial() {
		t.Fatalf("pods = %v total = %d partial = %v, want 1/2/true", scope.CurrentPods, scope.CurrentTotal, scope.Partial())
	}
}

func TestResolvePodScopeRefusesKindsWithNoOwnership(t *testing.T) {
	if _, err := ResolvePodScope(context.Background(), scopeFixture(t), "service", "shop", "api", 0); !errors.Is(err, ErrPodScopeUnsupportedKind) {
		t.Fatalf("service: err = %v, want ErrPodScopeUnsupportedKind", err)
	}
}

// A cache that cannot answer is an error, never an empty scope: "no pods" is
// only ever said when it is true.
func TestResolvePodScopeReportsAnUnreadableCache(t *testing.T) {
	noReplicaSets := scopeTestCache(t,
		map[string]bool{k8score.Pods: true, k8score.Deployments: true},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api", UID: "api-uid"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6-a", UID: "api-7f6-a-uid", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "api-7f6")}}},
	)
	if _, err := ResolvePodScope(context.Background(), noReplicaSets, "deployment", "shop", "api", 0); !errors.Is(err, k8s.ErrWorkloadAccessDenied) {
		t.Fatalf("deployment without a replicasets lister: err = %v, want ErrWorkloadAccessDenied", err)
	}
}

func TestCurrentReadersExcludePreviousControllerIncarnations(t *testing.T) {
	currentRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "active", UID: "active-uid", OwnerReferences: []metav1.OwnerReference{scopeOwner("Deployment", "api")}}}
	previousRoot := scopeOwner("Deployment", "api")
	previousRoot.UID = "previous-root"
	previousRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "old", UID: "old-uid", OwnerReferences: []metav1.OwnerReference{previousRoot}}}
	staleOwner := scopeOwner("ReplicaSet", "active")
	staleOwner.UID = "previous-rs"
	cache := scopeTestCache(t, map[string]bool{k8score.Pods: true, k8score.Deployments: true, k8score.ReplicaSets: true},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api", UID: "api-uid"}}, currentRS, previousRS,
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "current", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "active")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "previous-root", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "old")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "previous-rs", OwnerReferences: []metav1.OwnerReference{staleOwner}}},
	)
	scope, err := ResolvePodScope(context.Background(), cache, "deployment", "shop", "api", 0)
	if err != nil || !reflect.DeepEqual(scope.CurrentPods, []string{"current"}) || scope.CurrentTotal != 1 {
		t.Fatalf("metrics scope=%+v err=%v", scope, err)
	}
	pods, err := workloadPodsOnceWarm(context.Background(), cache, "deployment", "shop", "api", "api-uid")
	if err != nil || len(pods) != 1 || pods[0].Name != "current" {
		t.Fatalf("rightsizing pods=%v err=%v", pods, err)
	}
	// Rightsizing ties membership to the root it already read; a root
	// replaced after that read must not lend it the new incarnation's pods.
	pods, err = workloadPodsOnceWarm(context.Background(), cache, "deployment", "shop", "api", "previous-root")
	if err != nil || len(pods) != 1 || pods[0].Name != "previous-root" {
		t.Fatalf("rightsizing pods for the observed earlier root=%v err=%v", pods, err)
	}
}
