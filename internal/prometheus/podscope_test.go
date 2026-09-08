package prometheus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
)

type scopeFakeQuerier struct {
	answers map[string]*prom.QueryResult
	err     error
	seen    []string
}

func (f *scopeFakeQuerier) Query(_ context.Context, query string) (*prom.QueryResult, error) {
	f.seen = append(f.seen, query)
	if f.err != nil {
		return nil, f.err
	}
	for needle, res := range f.answers {
		if strings.Contains(query, needle) {
			return res, nil
		}
	}
	return &prom.QueryResult{ResultType: "vector"}, nil
}

func vectorOf(labels map[string]string, value float64) *prom.QueryResult {
	return &prom.QueryResult{ResultType: "vector", Series: []prom.Series{{Labels: labels, DataPoints: []prom.DataPoint{{Timestamp: 1, Value: value}}}}}
}

func scopeTestCache(t *testing.T, objects ...runtime.Object) *k8s.ResourceCache {
	t.Helper()
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(objects...),
		ResourceTypes: map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	return &k8s.ResourceCache{ResourceCache: core}
}

func scopeFixture(t *testing.T) *k8s.ResourceCache {
	t.Helper()
	isController := true
	owner := func(kind, name string) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &isController}
	}
	return scopeTestCache(t,
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6", OwnerReferences: []metav1.OwnerReference{owner("Deployment", "api")}}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-worker-9c1", OwnerReferences: []metav1.OwnerReference{owner("Deployment", "api-worker")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6-b", OwnerReferences: []metav1.OwnerReference{owner("ReplicaSet", "api-7f6")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6-a", OwnerReferences: []metav1.OwnerReference{owner("ReplicaSet", "api-7f6")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-worker-9c1-x", OwnerReferences: []metav1.OwnerReference{owner("ReplicaSet", "api-worker-9c1")}}},
	)
}

func TestResolvePodScopePrefersOwnershipHistory(t *testing.T) {
	ResetPodScopeCache()
	t.Cleanup(ResetPodScopeCache)
	cache := scopeFixture(t)
	q := &scopeFakeQuerier{answers: map[string]*prom.QueryResult{
		"kube_pod_owner": vectorOf(nil, 3),
	}}
	scope, err := ResolvePodScope(context.Background(), q, cache, "Deployment", "shop", "api", time.Hour, 50)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if scope.Coverage != OwnerCoverageKSMHistory || scope.ObservedPods != 3 {
		t.Fatalf("coverage = %s observed=%d, want ksm_history/3", scope.Coverage, scope.ObservedPods)
	}
	if got := scope.CurrentPods; len(got) != 2 || got[0] != "api-7f6-a" || got[1] != "api-7f6-b" {
		t.Fatalf("current pods = %v, want the two api pods, never api-worker", got)
	}
	if scope.Selection.Owner == nil || scope.Selection.Owner.Kind != "Deployment" || scope.Selection.Owner.Name != "api" {
		t.Fatalf("selection = %+v, want the workload itself", scope.Selection)
	}
	// One probe over the window the caller asked about.
	if len(q.seen) != 1 || !strings.Contains(q.seen[0], "[1h]") {
		t.Fatalf("probe should be one query over the window: %v", q.seen)
	}
	query := prom.BuildScopedQuery(scope.Selection, prom.CategoryCPU, prom.AggregatePerPod, true)
	if strings.Contains(query, "api-.*") || !strings.Contains(query, "kube_replicaset_owner") {
		t.Fatalf("chart query must join through the ReplicaSet edge: %s", query)
	}
	// Cached: a second resolve within the TTL asks Prometheus nothing.
	if _, err := ResolvePodScope(context.Background(), q, cache, "Deployment", "shop", "api", time.Hour, 50); err != nil || len(q.seen) != 1 {
		t.Fatalf("second resolve queried again (%d) or failed: %v", len(q.seen), err)
	}
}

func TestResolvePodScopeFallsBackToCurrentPodsAndReportsProbeErrors(t *testing.T) {
	ResetPodScopeCache()
	t.Cleanup(ResetPodScopeCache)
	cache := scopeFixture(t)

	noHistory := &scopeFakeQuerier{}
	scope, err := ResolvePodScope(context.Background(), noHistory, cache, "deployment", "shop", "api", time.Hour, 50)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if scope.Coverage != OwnerCoverageCurrentPods || scope.ProbeErr != nil || scope.Selection.Owner != nil {
		t.Fatalf("no ownership history: scope = %+v", scope)
	}
	if q := prom.BuildScopedQuery(scope.Selection, prom.CategoryMemory, prom.AggregateTotal, true); !strings.Contains(q, `pod=~'^(api-7f6-a|api-7f6-b)$'`) {
		t.Fatalf("current-pods query = %s", q)
	}

	ResetPodScopeCache()
	broken := &scopeFakeQuerier{err: errors.New("prometheus down")}
	scope, err = ResolvePodScope(context.Background(), broken, cache, "Deployment", "shop", "api", time.Hour, 1)
	if err != nil {
		t.Fatalf("probe failure must not fail the resolve: %v", err)
	}
	if scope.Coverage != OwnerCoverageCurrentPods || scope.ProbeErr == nil || !scope.Partial() || scope.CurrentTotal != 2 || len(scope.CurrentPods) != 1 {
		t.Fatalf("probe failure with cap: scope = %+v", scope)
	}

	ResetPodScopeCache()
	scope, err = ResolvePodScope(context.Background(), noHistory, cache, "Deployment", "shop", "ghost", time.Hour, 50)
	if err != nil {
		t.Fatalf("unknown deployment: %v", err)
	}
	if scope.Coverage != OwnerCoverageNone || !scope.Selection.IsEmpty() {
		t.Fatalf("no pods anywhere: scope = %+v", scope)
	}
	if _, err := ResolvePodScope(context.Background(), noHistory, cache, "Service", "shop", "api", time.Hour, 50); !errors.Is(err, ErrPodScopeUnsupportedKind) {
		t.Fatalf("service: err = %v", err)
	}
	if _, err := ResolvePodScope(context.Background(), noHistory, &k8s.ResourceCache{}, "Deployment", "shop", "api", time.Hour, 50); !errors.Is(err, k8s.ErrWorkloadAccessDenied) {
		t.Fatalf("denied cache: err = %v", err)
	}
}

func TestResolvePodScopeDirectOwnersNeedNoHop(t *testing.T) {
	ResetPodScopeCache()
	t.Cleanup(ResetPodScopeCache)
	isController := true
	cache := scopeTestCache(t, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db-0", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "db", Controller: &isController}}}})
	q := &scopeFakeQuerier{answers: map[string]*prom.QueryResult{"kube_pod_owner": vectorOf(nil, 1)}}
	scope, err := ResolvePodScope(context.Background(), q, cache, "StatefulSet", "shop", "db", 6*time.Hour, 50)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if len(q.seen) != 1 || !strings.Contains(q.seen[0], `owner_kind='StatefulSet',owner_name='db'`) {
		t.Fatalf("statefulset should probe kube_pod_owner directly: %v", q.seen)
	}
	if scope.Coverage != OwnerCoverageKSMHistory || scope.Selection.Owner == nil || scope.Selection.Owner.Kind != "StatefulSet" {
		t.Fatalf("scope = %+v", scope)
	}
}

func TestPodScopeCacheKeysOnWindowAndQuerier(t *testing.T) {
	ResetPodScopeCache()
	t.Cleanup(ResetPodScopeCache)
	cache := scopeFixture(t)
	q := &scopeFakeQuerier{}
	if _, err := ResolvePodScope(context.Background(), q, cache, "Deployment", "shop", "api", time.Hour, 50); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := ResolvePodScope(context.Background(), q, cache, "Deployment", "shop", "api", 6*time.Hour, 50); err != nil {
		t.Fatalf("other window: %v", err)
	}
	// One probe per resolve.
	if len(q.seen) != 2 {
		t.Fatalf("a different window must re-probe: %d queries", len(q.seen))
	}
	other := &scopeFakeQuerier{}
	if _, err := ResolvePodScope(context.Background(), other, cache, "Deployment", "shop", "api", time.Hour, 50); err != nil {
		t.Fatalf("other querier: %v", err)
	}
	if len(other.seen) == 0 {
		t.Fatal("a different Prometheus connection must re-probe rather than reuse the cached scope")
	}
}

func TestPodScopeTreatsAPodAsItsOwnScope(t *testing.T) {
	ResetPodScopeCache()
	t.Cleanup(ResetPodScopeCache)
	q := &scopeFakeQuerier{}
	scope, err := ResolvePodScope(context.Background(), q, nil, "Pod", "shop", "api-7f6-a", time.Hour, 50)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if len(q.seen) != 0 {
		t.Fatalf("a Pod needs no ownership probe: %v", q.seen)
	}
	if scope.Coverage != OwnerCoverageCurrentPods || len(scope.CurrentPods) != 1 || scope.CurrentPods[0] != "api-7f6-a" {
		t.Fatalf("scope = %+v", scope)
	}
}

func TestPodScopeIsNotReusedAcrossClusters(t *testing.T) {
	ResetPodScopeCache()
	t.Cleanup(ResetPodScopeCache)
	cache := scopeFixture(t)
	q := &scopeFakeQuerier{answers: map[string]*prom.QueryResult{"kube_pod_owner": vectorOf(nil, 2)}}

	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "cluster-a"})
	t.Cleanup(func() { k8s.SetConnectionStatus(k8s.ConnectionStatus{}) })
	if _, err := ResolvePodScope(context.Background(), q, cache, "Deployment", "shop", "api", time.Hour, 50); err != nil {
		t.Fatalf("cluster-a: %v", err)
	}
	probes := len(q.seen)

	// The Prometheus client is a singleton whose pointer survives a context
	// switch, so the cluster has to be part of the identity in its own right.
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "cluster-b"})
	if _, err := ResolvePodScope(context.Background(), q, cache, "Deployment", "shop", "api", time.Hour, 50); err != nil {
		t.Fatalf("cluster-b: %v", err)
	}
	if len(q.seen) == probes {
		t.Fatal("a different cluster served the previous cluster's pod scope")
	}
}

func TestPodScopeReportsACutPodListUnderEitherCoverage(t *testing.T) {
	ResetPodScopeCache()
	t.Cleanup(ResetPodScopeCache)
	cache := scopeFixture(t)
	withHistory := &scopeFakeQuerier{answers: map[string]*prom.QueryResult{"kube_pod_owner": vectorOf(nil, 9)}}
	scope, err := ResolvePodScope(context.Background(), withHistory, cache, "Deployment", "shop", "api", time.Hour, 1)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	// The query covers every pod the workload owned; the list the caller
	// reports is still cut, and saying "1 pod" without saying so would be a
	// lie about the workload's size.
	if scope.Coverage != OwnerCoverageKSMHistory || !scope.Partial() || scope.CurrentTotal != 2 || len(scope.CurrentPods) != 1 {
		t.Fatalf("scope = %+v, want ksm_history with a cut list reported", scope)
	}
}

func TestPodScopeResolvesACronJobThroughItsJobs(t *testing.T) {
	ResetPodScopeCache()
	t.Cleanup(ResetPodScopeCache)
	isController := true
	owner := func(kind, name string) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: "batch/v1", Kind: kind, Name: name, Controller: &isController}
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: fake.NewClientset(
			&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "nightly-1", OwnerReferences: []metav1.OwnerReference{owner("CronJob", "nightly")}}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "nightly-1-k", OwnerReferences: []metav1.OwnerReference{owner("Job", "nightly-1")}}},
		),
		ResourceTypes: map[string]bool{k8score.Pods: true, k8score.Jobs: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}

	q := &scopeFakeQuerier{answers: map[string]*prom.QueryResult{"kube_pod_owner": vectorOf(nil, 4)}}
	scope, err := ResolvePodScope(context.Background(), q, cache, "cronjob", "shop", "nightly", 6*time.Hour, 50)
	if err != nil {
		t.Fatalf("ResolvePodScope: %v", err)
	}
	if len(scope.CurrentPods) != 1 || scope.CurrentPods[0] != "nightly-1-k" {
		t.Fatalf("current pods = %v, want the Job's pod", scope.CurrentPods)
	}
	query := prom.BuildScopedQuery(scope.Selection, prom.CategoryCPU, prom.AggregateTotal, true)
	if !strings.Contains(query, "kube_job_owner{namespace='shop',owner_kind='CronJob',owner_name='nightly'") ||
		!strings.Contains(query, "* on (job_name) group_left()") {
		t.Fatalf("a CronJob's pods are reached through its Jobs: %s", query)
	}
}
