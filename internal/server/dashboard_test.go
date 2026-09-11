package server

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/helmhistory"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestDetectionToDashboardProblemPreservesUnknownOnset(t *testing.T) {
	detection := k8s.Detection{
		Kind: "Service", Namespace: "hooks", Name: "policy-webhook", Group: "", Severity: "critical",
		Reason: "Selector matches no pods", Age: "5m", AgeSeconds: 300, OnsetUnknown: true,
	}
	got := detectionToDashboardProblem(detection)
	if !got.OnsetUnknown || got.Duration != "" || got.DurationSeconds != 0 || got.Age != "5m" || got.AgeSeconds != 300 {
		t.Fatalf("dashboard problem lost unknown-onset contract: %+v", got)
	}
}

func TestDashboardHelmSummaryFromReleasesSortsAndLimits(t *testing.T) {
	releases := []helm.HelmRelease{
		{Name: "healthy-a", Namespace: "apps", Chart: "svc", ChartVersion: "1.0.0", Status: "deployed", ResourceHealth: "healthy"},
		{Name: "degraded", Namespace: "apps", Chart: "svc", ChartVersion: "1.1.0", Status: "deployed", ResourceHealth: "degraded"},
		{Name: "failed", Namespace: "ops", Chart: "svc", ChartVersion: "2.0.0", Status: "failed"},
		{Name: "pending", Namespace: "ops", Chart: "svc", ChartVersion: "2.1.0", Status: "pending-upgrade"},
		{Name: "unhealthy", Namespace: "apps", Chart: "svc", ChartVersion: "1.2.0", Status: "deployed", ResourceHealth: "unhealthy"},
		{Name: "rolled-back", Namespace: "apps", Chart: "svc", ChartVersion: "1.3.0", Status: "deployed", ResourceHealth: "healthy", LastOperation: &helm.HelmOperation{Kind: helmhistory.KindUpgradeRolledBack}},
		{Name: "healthy-b", Namespace: "ops", Chart: "svc", ChartVersion: "3.0.0", Status: "deployed", ResourceHealth: "healthy"},
		{Name: "healthy-c", Namespace: "ops", Chart: "svc", ChartVersion: "4.0.0", Status: "deployed", ResourceHealth: "healthy"},
	}

	got := dashboardHelmSummaryFromReleases(releases)
	if got.Total != len(releases) {
		t.Fatalf("Total = %d, want %d", got.Total, len(releases))
	}
	if got.Restricted {
		t.Fatal("Restricted = true, want false for a readable merged release list")
	}
	if len(got.Releases) != 6 {
		t.Fatalf("len(Releases) = %d, want 6", len(got.Releases))
	}
	wantNames := []string{"failed", "pending", "rolled-back", "unhealthy", "degraded", "healthy-a"}
	for i, want := range wantNames {
		if got.Releases[i].Name != want {
			t.Fatalf("Releases[%d].Name = %q, want %q", i, got.Releases[i].Name, want)
		}
	}
}

func TestDashboardHelmSummaryFromReleasesEmptyReadableList(t *testing.T) {
	got := dashboardHelmSummaryFromReleases(nil)
	if got.Total != 0 {
		t.Fatalf("Total = %d, want 0", got.Total)
	}
	if got.Restricted {
		t.Fatal("Restricted = true, want false for an empty readable release list")
	}
	if len(got.Releases) != 0 {
		t.Fatalf("len(Releases) = %d, want 0", len(got.Releases))
	}
}

func TestDashboardCalicoCoverageUsesNamespaceAwareReadsAndCalicoFallback(t *testing.T) {
	deployments := []*appsv1.Deployment{
		dashboardTestDeployment("ns-a", "frontend", "frontend"),
		dashboardTestDeployment("ns-b", "backend", "backend"),
		dashboardTestDeployment("ns-c", "preview", "preview"),
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        kubernetesfake.NewSimpleClientset(deployments[0], deployments[1], deployments[2]),
		ResourceTypes: map[string]bool{k8score.Deployments: true},
		DeferredTypes: map[string]bool{},
		SyncTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	if cache.NetworkPolicies() != nil {
		t.Fatal("test cache unexpectedly has a core NetworkPolicy lister")
	}

	projectNetworkPolicy := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "networkpolicies"}
	projectGlobalNetworkPolicy := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "globalnetworkpolicies"}
	projectStagedNetworkPolicy := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "stagednetworkpolicies"}
	projectStagedGlobalNetworkPolicy := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "stagedglobalnetworkpolicies"}
	projectStagedKubernetesNetworkPolicy := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "stagedkubernetesnetworkpolicies"}
	legacyNetworkPolicy := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "networkpolicies"}
	namespacedResources := map[schema.GroupResource]bool{
		{Group: projectNetworkPolicy.Group, Resource: projectNetworkPolicy.Resource}:                                 true,
		{Group: projectStagedNetworkPolicy.Group, Resource: projectStagedNetworkPolicy.Resource}:                     true,
		{Group: projectStagedKubernetesNetworkPolicy.Group, Resource: projectStagedKubernetesNetworkPolicy.Resource}: true,
		{Group: legacyNetworkPolicy.Group, Resource: legacyNetworkPolicy.Resource}:                                   true,
	}
	resources := []k8s.APIResource{
		{Group: projectNetworkPolicy.Group, Version: projectNetworkPolicy.Version, Kind: "NetworkPolicy", Name: projectNetworkPolicy.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: projectGlobalNetworkPolicy.Group, Version: projectGlobalNetworkPolicy.Version, Kind: "GlobalNetworkPolicy", Name: projectGlobalNetworkPolicy.Resource, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: projectStagedNetworkPolicy.Group, Version: projectStagedNetworkPolicy.Version, Kind: "StagedNetworkPolicy", Name: projectStagedNetworkPolicy.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: projectStagedGlobalNetworkPolicy.Group, Version: projectStagedGlobalNetworkPolicy.Version, Kind: "StagedGlobalNetworkPolicy", Name: projectStagedGlobalNetworkPolicy.Resource, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: projectStagedKubernetesNetworkPolicy.Group, Version: projectStagedKubernetesNetworkPolicy.Version, Kind: "StagedKubernetesNetworkPolicy", Name: projectStagedKubernetesNetworkPolicy.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: legacyNetworkPolicy.Group, Version: legacyNetworkPolicy.Version, Kind: "NetworkPolicy", Name: legacyNetworkPolicy.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}
	listKinds := map[schema.GroupVersionResource]string{
		projectNetworkPolicy:                 "NetworkPolicyList",
		projectGlobalNetworkPolicy:           "GlobalNetworkPolicyList",
		projectStagedNetworkPolicy:           "StagedNetworkPolicyList",
		projectStagedGlobalNetworkPolicy:     "StagedGlobalNetworkPolicyList",
		projectStagedKubernetesNetworkPolicy: "StagedKubernetesNetworkPolicyList",
		legacyNetworkPolicy:                  "NetworkPolicyList",
	}
	objects := []runtime.Object{
		dashboardTestCalicoPolicy(projectNetworkPolicy, "NetworkPolicy", "ns-a", "frontend-policy", map[string]any{"selector": "app == 'frontend'"}),
		dashboardTestCalicoPolicy(projectGlobalNetworkPolicy, "GlobalNetworkPolicy", "", "backend-global", map[string]any{"selector": "projectcalico.org/namespace == 'ns-b'"}),
		dashboardTestCalicoPolicy(projectStagedNetworkPolicy, "StagedNetworkPolicy", "ns-b", "backend-staged", map[string]any{"selector": "app == 'backend'"}),
		dashboardTestCalicoPolicy(projectStagedGlobalNetworkPolicy, "StagedGlobalNetworkPolicy", "", "preview-staged", map[string]any{"selector": "projectcalico.org/namespace == 'not-preview'"}),
		dashboardTestCalicoPolicy(projectStagedKubernetesNetworkPolicy, "StagedKubernetesNetworkPolicy", "ns-c", "preview-kubernetes-staged", map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "preview"}},
			"policyTypes": []any{"Ingress"},
		}),
		dashboardTestCalicoPolicy(legacyNetworkPolicy, "NetworkPolicy", "ns-a", "frontend-policy", map[string]any{"selector": "app == 'not-frontend'"}),
		dashboardTestCalicoPolicy(legacyNetworkPolicy, "NetworkPolicy", "ns-c", "preview-legacy", map[string]any{"selector": "app == 'preview'"}),
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)
	dyn.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listAction, ok := action.(k8stesting.ListAction)
		if !ok || listAction.GetNamespace() != "" || !namespacedResources[action.GetResource().GroupResource()] {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), "", errors.New("cluster-wide list denied"))
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	dynamicCache := k8s.GetDynamicResourceCache()
	for gvr := range map[schema.GroupVersionResource]bool{
		projectNetworkPolicy:                 true,
		projectStagedNetworkPolicy:           true,
		projectStagedKubernetesNetworkPolicy: true,
		legacyNetworkPolicy:                  true,
	} {
		for _, namespace := range []string{"ns-a", "ns-b", "ns-c"} {
			if _, err := dynamicCache.List(gvr, namespace); err != nil {
				t.Fatalf("List(%v, %q): %v", gvr, namespace, err)
			}
			if !dynamicCache.WaitForSync(gvr, 2*time.Second) {
				t.Fatalf("%v informer did not sync for namespace %q", gvr, namespace)
			}
		}
	}
	if _, err := dynamicCache.List(projectGlobalNetworkPolicy, ""); err != nil {
		t.Fatalf("List(%v): %v", projectGlobalNetworkPolicy, err)
	}
	if !dynamicCache.WaitForSync(projectGlobalNetworkPolicy, 2*time.Second) {
		t.Fatalf("%v informer did not sync", projectGlobalNetworkPolicy)
	}
	if _, err := dynamicCache.List(projectStagedGlobalNetworkPolicy, ""); err != nil {
		t.Fatalf("List(%v): %v", projectStagedGlobalNetworkPolicy, err)
	}
	if !dynamicCache.WaitForSync(projectStagedGlobalNetworkPolicy, 2*time.Second) {
		t.Fatalf("%v informer did not sync", projectStagedGlobalNetworkPolicy)
	}

	got := (&Server{}).getDashboardNetworkPolicyCoverage(requestWithUser("GET", "/", nil), cache, []string{"ns-a", "ns-b", "ns-c"})
	if got == nil {
		t.Fatal("coverage is nil when only Calico policies are available")
	}
	if got.TotalPolicies != 6 || got.StagedPolicies != 3 {
		t.Fatalf("policy counts = %+v, want total=6 staged=3", got)
	}
	if got.TotalWorkloads != 3 || got.CoveredWorkloads != 3 || got.CoveredWorkloadsIfStaged != 3 {
		t.Fatalf("coverage = %+v, want Calico policies to cover all workloads", got)
	}

	server := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"ns-a", "ns-b", "ns-c"}}
	for _, tuple := range []struct {
		group, resource, namespace string
	}{
		{projectNetworkPolicy.Group, projectNetworkPolicy.Resource, "ns-a"},
		{projectGlobalNetworkPolicy.Group, projectGlobalNetworkPolicy.Resource, ""},
		{projectStagedNetworkPolicy.Group, projectStagedNetworkPolicy.Resource, "ns-b"},
		{projectStagedGlobalNetworkPolicy.Group, projectStagedGlobalNetworkPolicy.Resource, ""},
		{projectStagedKubernetesNetworkPolicy.Group, projectStagedKubernetesNetworkPolicy.Resource, "ns-c"},
	} {
		perms.SetCanI("list", tuple.group, tuple.resource, tuple.namespace, true)
	}
	perms.SetCanI("list", legacyNetworkPolicy.Group, legacyNetworkPolicy.Resource, "ns-c", false)
	server.permCache.Set("alice", nil, perms)
	authenticated := requestWithUser("GET", "/", &auth.User{Username: "alice"})
	filtered := server.getDashboardNetworkPolicyCoverage(authenticated, cache, []string{"ns-a", "ns-b", "ns-c"})
	if filtered == nil {
		t.Fatal("filtered coverage is nil")
	}
	if filtered.TotalPolicies != 5 || filtered.StagedPolicies != 3 {
		t.Fatalf("filtered policy counts = %+v, want total=5 staged=3", filtered)
	}
	if filtered.TotalWorkloads != 3 || filtered.CoveredWorkloads != 2 || filtered.CoveredWorkloadsIfStaged != 3 {
		t.Fatalf("filtered coverage = %+v, want denied legacy policy excluded from coverage", filtered)
	}
}

func dashboardTestDeployment(namespace, name, app string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"app": app},
		}}},
	}
}

func dashboardTestCalicoPolicy(gvr schema.GroupVersionResource, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
	metadata := map[string]any{"name": name}
	if namespace != "" {
		metadata["namespace"] = namespace
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvr.Group + "/" + gvr.Version,
		"kind":       kind,
		"metadata":   metadata,
		"spec":       spec,
	}}
}

func TestDashboardStagedDeletionRemovesProjectedCoverage(t *testing.T) {
	deployment := dashboardTestDeployment("ns-a", "frontend", "frontend")
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        kubernetesfake.NewSimpleClientset(deployment),
		ResourceTypes: map[string]bool{k8score.Deployments: true},
		DeferredTypes: map[string]bool{},
		SyncTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}

	networkPolicy := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "networkpolicies"}
	stagedNetworkPolicy := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "stagednetworkpolicies"}
	resources := []k8s.APIResource{
		{Group: networkPolicy.Group, Version: networkPolicy.Version, Kind: "NetworkPolicy", Name: networkPolicy.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: stagedNetworkPolicy.Group, Version: stagedNetworkPolicy.Version, Kind: "StagedNetworkPolicy", Name: stagedNetworkPolicy.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}
	listKinds := map[schema.GroupVersionResource]string{
		networkPolicy:       "NetworkPolicyList",
		stagedNetworkPolicy: "StagedNetworkPolicyList",
	}
	objects := []runtime.Object{
		dashboardTestCalicoPolicy(networkPolicy, "NetworkPolicy", "ns-a", "frontend-policy", map[string]any{"selector": "app == 'frontend'"}),
		// Staging a deletion of that same policy. The Calico API requires the rest
		// of the spec to be empty, so the absent selector must not be read as one
		// that selects everything.
		dashboardTestCalicoPolicy(stagedNetworkPolicy, "StagedNetworkPolicy", "ns-a", "frontend-policy", map[string]any{"stagedAction": "Delete"}),
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	dynamicCache := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{networkPolicy, stagedNetworkPolicy} {
		if _, err := dynamicCache.List(gvr, ""); err != nil {
			t.Fatalf("List(%v): %v", gvr, err)
		}
		if !dynamicCache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("%v informer did not sync", gvr)
		}
	}

	got := (&Server{}).getDashboardNetworkPolicyCoverage(requestWithUser("GET", "/", nil), cache, nil)
	if got == nil {
		t.Fatal("coverage is nil")
	}
	if got.CoveredWorkloads != 1 {
		t.Fatalf("CoveredWorkloads = %d, want the enforced policy to still count", got.CoveredWorkloads)
	}
	if got.CoveredWorkloadsIfStaged != 0 {
		t.Fatalf("CoveredWorkloadsIfStaged = %d, want 0 once the policy is staged for deletion", got.CoveredWorkloadsIfStaged)
	}
}

func TestDashboardStagedIgnoreLeavesCoverageAlone(t *testing.T) {
	deployment := dashboardTestDeployment("ns-a", "frontend", "frontend")
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        kubernetesfake.NewSimpleClientset(deployment),
		ResourceTypes: map[string]bool{k8score.Deployments: true},
		DeferredTypes: map[string]bool{},
		SyncTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}

	networkPolicy := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "networkpolicies"}
	stagedNetworkPolicy := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "stagednetworkpolicies"}
	resources := []k8s.APIResource{
		{Group: networkPolicy.Group, Version: networkPolicy.Version, Kind: "NetworkPolicy", Name: networkPolicy.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: stagedNetworkPolicy.Group, Version: stagedNetworkPolicy.Version, Kind: "StagedNetworkPolicy", Name: stagedNetworkPolicy.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}
	listKinds := map[schema.GroupVersionResource]string{
		networkPolicy:       "NetworkPolicyList",
		stagedNetworkPolicy: "StagedNetworkPolicyList",
	}
	objects := []runtime.Object{
		dashboardTestCalicoPolicy(networkPolicy, "NetworkPolicy", "ns-a", "frontend-policy", map[string]any{"selector": "app == 'frontend'"}),
		// Ignore means this staged policy is skipped. It previews no protection,
		// but it does not take the enforced policy of the same name away either.
		dashboardTestCalicoPolicy(stagedNetworkPolicy, "StagedNetworkPolicy", "ns-a", "frontend-policy", map[string]any{
			"stagedAction": "Ignore",
			"selector":     "app == 'frontend'",
		}),
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	dynamicCache := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{networkPolicy, stagedNetworkPolicy} {
		if _, err := dynamicCache.List(gvr, ""); err != nil {
			t.Fatalf("List(%v): %v", gvr, err)
		}
		if !dynamicCache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("%v informer did not sync", gvr)
		}
	}

	got := (&Server{}).getDashboardNetworkPolicyCoverage(requestWithUser("GET", "/", nil), cache, nil)
	if got == nil {
		t.Fatal("coverage is nil")
	}
	if got.CoveredWorkloads != 1 || got.CoveredWorkloadsIfStaged != 1 {
		t.Fatalf("coverage = %d/%d covered/projected, want 1/1 — an ignored staged policy removes nothing", got.CoveredWorkloads, got.CoveredWorkloadsIfStaged)
	}
}

func TestDashboardCoverageProjectionSerializesWhenZero(t *testing.T) {
	// The frontend falls back to the enforced figure when this field is absent,
	// so an omitempty here would silently hide a projected drop to zero — the
	// exact case the projection exists to show.
	encoded, err := json.Marshal(DashboardNetworkPolicyCoverage{TotalPolicies: 1, CoveredWorkloads: 3, TotalWorkloads: 5})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"coveredWorkloadsIfStaged":0`) {
		t.Fatalf("coverage JSON = %s, want an explicit zero projection", encoded)
	}
}
