package server

import (
	"net/http"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCapacityNamespacesBypassSavedViewPreference(t *testing.T) {
	previousContext := k8s.SetTestContextName("capacity-auth-test")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })

	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha", "beta"}})
	r := requestWithUser(http.MethodGet, "/api/capacity", &auth.User{Username: "alice"})
	s.setActiveNamespaceForUser(r, []string{"alpha"})
	if got := s.getActiveNamespaceForUser(r); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("saved view preference = %v, want [alpha]", got)
	}

	if got := s.capacityNamespacesForUser(r); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("capacity namespaces = %v, want full RBAC ceiling [alpha beta]", got)
	}
}

func TestCapacityOwnerResolutionRequiresImmediateOwnerVisibility(t *testing.T) {
	controller := true
	replicaPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-abc", Controller: &controller}}}}
	secondReplicaPod := replicaPod.DeepCopy()
	jobPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "batch", OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "report-123", Controller: &controller}}}}
	directPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", Controller: &controller}}}}
	calls := []string{}
	permissions, partial := capacityOwnerResolutionPermissions([]*corev1.Pod{replicaPod, secondReplicaPod, jobPod, directPod}, func(group, resource, namespace string) bool {
		calls = append(calls, group+"/"+resource+"/"+namespace)
		return resource == "jobs"
	})

	if permissions[replicaPod] || permissions[secondReplicaPod] {
		t.Fatal("ReplicaSet top-owner resolution was allowed without ReplicaSet visibility")
	}
	if !permissions[jobPod] {
		t.Fatal("Job top-owner resolution was denied despite Job visibility")
	}
	if _, found := permissions[directPod]; found {
		t.Fatal("direct owners should use the owner reference already visible on the Pod")
	}
	if !partial || !slices.Equal(calls, []string{"apps/replicasets/shop", "batch/jobs/batch"}) {
		t.Fatalf("partial=%v calls=%v", partial, calls)
	}
}

func TestCapacityNamespacesHonorExplicitAndForcedScope(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha", "beta"}})

	r := requestWithUser(http.MethodGet, "/api/capacity?namespaces=beta,other", &auth.User{Username: "alice"})
	if got := s.capacityNamespacesForUser(r); !slices.Equal(got, []string{"beta"}) {
		t.Fatalf("explicit namespaces = %v, want [beta]", got)
	}

	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("alpha")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})
	r = requestWithUser(http.MethodGet, "/api/capacity", &auth.User{Username: "alice"})
	if got := s.capacityNamespacesForUser(r); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("forced namespaces = %v, want [alpha]", got)
	}
}

type capacityTestInformerScope struct {
	clusterWide map[string]bool
	namespaces  map[string][]string
	notReady    map[string]bool
}

func (s capacityTestInformerScope) IsKindClusterWide(resource string) bool {
	return s.clusterWide[resource]
}

func (s capacityTestInformerScope) KindNamespaces(resource string) []string {
	return append([]string(nil), s.namespaces[resource]...)
}

func (s capacityTestInformerScope) IsKindReady(resource string) bool {
	return !s.notReady[resource]
}

func TestCapacityNamespacesHonorInformerCoverage(t *testing.T) {
	clusterWide := capacityTestInformerScope{clusterWide: map[string]bool{"pods": true}}
	limited := capacityTestInformerScope{namespaces: map[string][]string{"pods": {"team-a", "team-b"}}}
	tests := []struct {
		name            string
		cache           capacityInformerScope
		requested       []string
		want            []string
		wantLimited     bool
		wantPartial     bool
		wantUnavailable bool
	}{
		{name: "cluster wide", cache: clusterWide},
		{name: "unfiltered limited cache", cache: limited, want: []string{"team-a", "team-b"}, wantLimited: true, wantPartial: true},
		{name: "covered explicit subset", cache: limited, requested: []string{"team-b"}, want: []string{"team-b"}, wantLimited: true},
		{name: "covered explicit order", cache: limited, requested: []string{"team-b", "team-a"}, want: []string{"team-b", "team-a"}, wantLimited: true},
		{name: "partial overlap", cache: limited, requested: []string{"team-b", "team-c"}, want: []string{"team-b"}, wantLimited: true, wantPartial: true},
		{name: "zero overlap", cache: limited, requested: []string{"team-c"}, want: []string{}, wantLimited: true, wantUnavailable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := capacityNamespacesWithinCache(test.cache, "pods", test.requested)
			if !slices.Equal(got.namespaces, test.want) || got.limited != test.wantLimited || got.partial != test.wantPartial || got.unavailable != test.wantUnavailable {
				t.Fatalf("cache namespaces = %#v, want namespaces=%v limited=%v partial=%v unavailable=%v", got, test.want, test.wantLimited, test.wantPartial, test.wantUnavailable)
			}
		})
	}
}

func TestCapacityOwnerResolutionHonorsInformerCoverage(t *testing.T) {
	controller := true
	replicaPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-abc", Controller: &controller}}}}
	jobPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b", OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "report", Controller: &controller}}}}
	cache := capacityTestInformerScope{namespaces: map[string][]string{
		"replicasets": {"team-a"},
		"jobs":        {"team-b"},
	}}
	permissions, partial := capacityOwnerResolutionPermissions([]*corev1.Pod{replicaPod, jobPod}, func(_ string, resource, namespace string) bool {
		return capacityCacheCoversNamespace(cache, resource, namespace)
	})
	if permissions[replicaPod] || !permissions[jobPod] || !partial {
		t.Fatalf("owner permissions = %#v partial=%v, want ReplicaSet denied and Job allowed", permissions, partial)
	}
	cache.notReady = map[string]bool{"jobs": true}
	if capacityCacheCoversNamespace(cache, "jobs", "team-b") {
		t.Fatal("owner resolution used an informer that has not synced")
	}
}

func TestCapacityWorkloadCoverageIncludesPoolAttributionSources(t *testing.T) {
	available := capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster)
	// Real partial producers always carry an item count; partial without one
	// is deliberately unobserved.
	partialCount := 1
	partial := capacityapi.NewSourceCoverage(capacityapi.CoveragePartial, capacityapi.CoverageScopeCluster)
	partial.ItemCount = &partialCount
	denied := capacityapi.NewSourceCoverage(capacityapi.CoverageDenied, capacityapi.CoverageScopeCluster)

	tests := []struct {
		name       string
		nodes      capacityapi.SourceCoverage
		claims     capacityapi.SourceCoverage
		wantStatus capacityapi.CoverageStatus
		wantReason string
	}{
		{name: "claims provide complete attribution", nodes: denied, claims: available, wantStatus: capacityapi.CoverageAvailable},
		{name: "partial inventory bounds attribution", nodes: denied, claims: partial, wantStatus: capacityapi.CoveragePartial, wantReason: "workload_pool_attribution_partial"},
		{name: "no inventory makes attribution unavailable", nodes: denied, claims: denied, wantStatus: capacityapi.CoverageUnavailable, wantReason: "workload_pool_attribution_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coverage := capacityapi.CoverageBySource{
				capacityapi.CoverageWorkloads:  available,
				capacityapi.CoverageNodes:      test.nodes,
				capacityapi.CoverageNodeClaims: test.claims,
			}
			applyCapacityPoolAttributionCoverage(coverage)
			got := coverage[capacityapi.CoverageWorkloads]
			if got.Status != test.wantStatus || got.ReasonCode != test.wantReason {
				t.Fatalf("workload coverage = %#v, want %q/%q", got, test.wantStatus, test.wantReason)
			}
		})
	}
}
