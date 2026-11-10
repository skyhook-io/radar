package server

import (
	"net/http"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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

func TestCapacityOwnerResolutionHonorsInformerCoverage(t *testing.T) {
	controller := true
	replicaPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-abc", Controller: &controller}}}}
	jobPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b", OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "report", Controller: &controller}}}}
	cache, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: fake.NewSimpleClientset(),
		ResourceScopes: map[string]k8score.ResourceScope{
			"replicasets": {Enabled: true, Namespace: "team-a"},
			"jobs":        {Enabled: true, Namespace: "team-b"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Stop()
	permissions, partial := capacityOwnerResolutionPermissions([]*corev1.Pod{replicaPod, jobPod}, func(_ string, resource, namespace string) bool {
		return cache.AssessKindRead(resource, []string{namespace}).Complete
	})
	if permissions[replicaPod] || !permissions[jobPod] || !partial {
		t.Fatalf("owner permissions = %#v partial=%v, want ReplicaSet denied and Job allowed", permissions, partial)
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
