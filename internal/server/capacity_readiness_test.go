package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestCapacityMixedScopeKeepsKnownExclusionsWhileUnread(t *testing.T) {
	for _, state := range []struct {
		readiness k8s.KindReadiness
		timeout   time.Duration
		status    capacityapi.CoverageStatus
	}{
		{k8s.KindPending, time.Minute, capacityapi.CoverageSyncing},
		{k8s.KindFailed, time.Millisecond, capacityapi.CoverageError},
	} {
		core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(), ResourceScopes: map[string]k8score.ResourceScope{"services": {Enabled: true}, "pods": {Enabled: true, Namespace: "alpha"}}, DeferredTypes: map[string]bool{"pods": true}, DebugSyncDelays: map[string]time.Duration{"pods": time.Hour}, DeferredSyncTimeout: state.timeout})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(core.Stop)
		waitFor(t, 5*time.Second, func() bool { return core.KindReadinessFor("pods") == state.readiness })
		cache := k8s.GetResourceCache()
		prior := cache.ResourceCache
		cache.ResourceCache = core
		meta := capacityapi.NewResponseMeta(time.Now())
		pods := (&Server{}).loadCapacityPods(httptest.NewRequest(http.MethodGet, "/api/capacity?namespaces=alpha,beta", nil), &meta, time.Now())
		cache.ResourceCache = prior
		coverage := meta.Coverage[capacityapi.CoveragePods]
		if len(pods) != 0 || coverage.Status != state.status || coverage.ReasonCode != "pod_cache_scope_partial" || !slices.Equal(coverage.Namespaces, []string{"alpha"}) || coverage.ItemCount != nil || coverage.ObservedAt != nil {
			t.Fatalf("unread mixed scope: %+v rows=%d", coverage, len(pods))
		}
	}
}

// A namespace-scoped cache serves a request for loaded namespaces while
// another namespace of the kind is stuck, for Pods and for the autoscaler
// status ConfigMap, whose lister doesn't exist until every namespace loads.
func TestCapacityReadsLoadedNamespacesWhileAnotherIsStuck(t *testing.T) {
	payload := "Cluster-autoscaler status at 2026-07-18 22:01:54.093779908 +0000 UTC:\n" +
		"Cluster-wide:\n  Health:      Healthy (ready=1 registered=1)\n"
	client := fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "alpha"}, Spec: corev1.PodSpec{NodeName: "node-1"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "gamma"}, Spec: corev1.PodSpec{NodeName: "node-1"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: autoscalerStatusConfigMapName, Namespace: autoscalerStatusNamespace}, Data: map[string]string{"status": payload}},
	)
	kinds := map[string][]string{"pods": {"alpha", "gamma"}, "configmaps": {autoscalerStatusNamespace, "gamma"}}
	scopes, enabled := map[string]k8score.ResourceScope{}, map[string]bool{}
	for kind, namespaces := range kinds {
		client.PrependReactor("list", kind, func(action ktesting.Action) (bool, runtime.Object, error) {
			if action.GetNamespace() == "gamma" {
				return true, nil, errors.New("gamma does not answer")
			}
			return false, nil, nil
		})
		scopes[kind] = k8score.ResourceScope{Enabled: true, Namespace: namespaces[0]}
		enabled[kind] = true
	}
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
		Client:                  client,
		ResourceTypes:           enabled,
		ResourceScopes:          scopes,
		ResourceScopeNamespaces: kinds,
		DeferredTypes:           enabled,
		DeferredSyncTimeout:     time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetResourceCache()
	waitFor(t, 5*time.Second, func() bool {
		return cache.AssessKindRead("pods", []string{"alpha"}).Complete && cache.AssessKindRead("configmaps", []string{autoscalerStatusNamespace}).Complete
	})
	if cache.ConfigMaps() != nil || cache.AssessKindRead("pods", nil).Readiness != k8s.KindPending {
		t.Fatal("the fixture must leave each kind loading as a whole")
	}

	meta := capacityapi.NewResponseMeta(time.Now())
	pods := (&Server{}).loadCapacityPods(httptest.NewRequest(http.MethodGet, "/api/capacity?namespaces=alpha", nil), &meta, time.Now())
	if coverage := meta.Coverage[capacityapi.CoveragePods]; len(pods) != 1 || pods[0].Name != "api" || coverage.Status != capacityapi.CoverageAvailable {
		t.Fatalf("loaded alpha: rows=%d coverage=%+v", len(pods), coverage)
	}
	for _, query := range []string{"", "?namespaces=alpha,gamma"} {
		meta := capacityapi.NewResponseMeta(time.Now())
		pods := (&Server{}).loadCapacityPods(httptest.NewRequest(http.MethodGet, "/api/capacity"+query, nil), &meta, time.Now())
		if coverage := meta.Coverage[capacityapi.CoveragePods]; len(pods) != 0 || coverage.Status != capacityapi.CoverageSyncing {
			t.Fatalf("%q with gamma loading: rows=%d coverage=%+v", query, len(pods), coverage)
		}
	}

	status, coverage, _ := capacityAutoscalerStatus(true, cache)
	if status == nil || coverage.Status != capacityapi.CoverageAvailable {
		t.Fatalf("autoscaler status in loaded kube-system: %+v", coverage)
	}
}

// On a loaded namespace-scoped cache, Capacity counts the namespaces Radar
// collects and marks what it didn't read.
func TestCapacityLoadedNamespaceScopedCache(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "alpha"}, Spec: corev1.PodSpec{NodeName: "node-1"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "beta"}, Spec: corev1.PodSpec{NodeName: "node-1"}},
	)
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query, reason string
		status        capacityapi.CoverageStatus
		namespaces    []string
		rows          int
	}{
		{"?namespaces=alpha,beta", "pod_cache_scope_partial", capacityapi.CoveragePartial, []string{"alpha"}, 1},
		{"?namespaces=beta", "pod_cache_scope_unavailable", capacityapi.CoverageUnavailable, []string{}, 0},
		{"", "pod_cache_scope_partial", capacityapi.CoveragePartial, []string{"alpha"}, 1},
	} {
		meta := capacityapi.NewResponseMeta(time.Now())
		pods := (&Server{}).loadCapacityPods(httptest.NewRequest(http.MethodGet, "/api/capacity"+tc.query, nil), &meta, time.Now())
		coverage := meta.Coverage[capacityapi.CoveragePods]
		if len(pods) != tc.rows || coverage.Status != tc.status || coverage.ReasonCode != tc.reason || coverage.Scope != capacityapi.CoverageScopeExplicitNamespaces || !slices.Equal(coverage.Namespaces, tc.namespaces) {
			t.Fatalf("%q: rows=%d coverage=%+v", tc.query, len(pods), coverage)
		}
	}
}
