package server

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestRESTContextAndApplicationsKeepReadableCrossNamespaceFluxInventory(t *testing.T) {
	client := clusterWithDeployment("web")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-pod", Namespace: "demo", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-rs", UID: "uid-web-rs"}}}}
	stalePod := pod.DeepCopy()
	stalePod.Name = "stale-pod"
	stalePod.Labels = map[string]string{"helm.toolkit.fluxcd.io/name": "stale", "helm.toolkit.fluxcd.io/namespace": "gitops"}
	for _, obj := range []runtime.Object{pod, stalePod, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "gitops"}}} {
		if err := client.Tracker().Add(obj); err != nil {
			t.Fatal(err)
		}
	}
	useTestResourceCache(t, client)
	gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	actual := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvr.Group + "/" + gvr.Version, "kind": "HelmRelease",
		"metadata": map[string]any{"namespace": "gitops", "name": "actual"},
		"status":   map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "demo_web_apps_Deployment", "v": "v1"}}}},
	}}
	stale := actual.DeepCopy()
	stale.SetName("stale")
	stale.Object["status"] = map[string]any{"inventory": map[string]any{"entries": []any{}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "HelmReleaseList"}, actual, stale)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "HelmRelease", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	if _, err := k8s.GetDynamicResourceCache().List(gvr, ""); err != nil {
		t.Fatal(err)
	}
	if !k8s.GetDynamicResourceCache().WaitForSync(gvr, 2*time.Second) {
		t.Fatal("HelmRelease cache did not sync")
	}
	env := newAuthTestServer(t)
	shared, _, _, _, ok := env.srv.topologyForContext()
	if !ok || !env.srv.broadcaster.updateCachedTopology(shared, env.srv.broadcaster.topoEpoch.Load(), k8s.GetResourceCache().Generation()) {
		t.Fatal("could not seed the shared relationship snapshot")
	}
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"demo"}}
	perms.SetCanI("get", "apps", "deployments", "demo", true)
	perms.SetCanI("get", "apps", "replicasets", "demo", true)
	perms.SetCanI("get", gvr.Group, gvr.Resource, "gitops", false)
	env.srv.permCache.Set("limited-context", nil, perms)
	for _, user := range []string{"", "limited-context", ""} {
		r := reqAs(user)
		for _, target := range []*corev1.Pod{pod, stalePod} {
			got := env.srv.buildAIResourceContext(r, target, "Pod", "demo", target.Name)
			if user != "" {
				for _, ref := range got.ManagedBy {
					if ref.Namespace == "gitops" {
						t.Fatalf("hidden manager leaked: %+v", got)
					}
				}
			} else if len(got.ManagedBy) != 1 || got.ManagedBy[0].Name != "actual" || got.ManagedBy[0].Namespace != "gitops" {
				t.Fatalf("%s resource context=%+v", target.Name, got)
			}
			list := env.srv.newResourceSummaryContextBuilder(r, []string{"demo"})
			search := env.srv.newSearchSummaryContextBuilder(r, []string{"demo"})
			for _, summary := range []*resourcecontext.ResourceSummaryContext{
				list(target, nil, "", "Pod", "demo", target.Name),
				search(target, nil, "", "Pod", "demo", target.Name),
			} {
				if user != "" {
					if summary != nil && summary.ManagedBy != nil && summary.ManagedBy.Namespace == "gitops" {
						t.Fatalf("hidden summary manager leaked: %+v", summary)
					}
					if target.Name == "web-pod" && (summary == nil || summary.ManagedBy == nil || summary.ManagedBy.Kind != "Deployment" || summary.ManagedBy.Name != "web") {
						t.Fatalf("readable native ancestor lost: %+v", summary)
					}
				} else if summary == nil || summary.ManagedBy == nil || summary.ManagedBy.Name != "actual" {
					t.Fatalf("%s summary=%+v", target.Name, summary)
				}
			}
		}
		native := pod.DeepCopy()
		native.Annotations = map[string]string{"meta.helm.sh/release-name": "native-release", "meta.helm.sh/release-namespace": "demo"}
		got := env.srv.buildAIResourceContext(r, native, "Pod", "demo", native.Name)
		if len(got.ManagedBy) != 1 || got.ManagedBy[0].Name != "native-release" || got.ManagedBy[0].Group != "" {
			t.Fatalf("native Helm metadata was reinterpreted as Flux: %+v", got.ManagedBy)
		}
		summary := env.srv.newResourceSummaryContextBuilder(r, []string{"demo"})(native, nil, "", "Pod", "demo", native.Name)
		if summary == nil || summary.ManagedBy == nil || summary.ManagedBy.Source != "helm" || summary.ManagedBy.Name != "native-release" {
			t.Fatalf("native Helm summary=%+v", summary)
		}
	}

	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
	for _, visible := range [][]string{{"demo", "gitops"}, {"demo"}, {"demo", "gitops"}} {
		got, err := listApplicationsForRequest(context.Background(), []string{"demo"}, visible, false, func(uint64) *topology.Topology { return shared })
		if err != nil || len(got.Applications) != 1 {
			t.Fatalf("Applications: err=%v rows=%+v", err, got)
		}
		row := got.Applications[0]
		if row.Namespace != "demo" {
			t.Fatalf("namespace pick expanded workload scope: %+v", row)
		}
		if len(visible) == 1 {
			if row.SourceRef != nil && row.SourceRef.Namespace == "gitops" {
				t.Fatalf("cached hidden app source leaked: %+v", row.SourceRef)
			}
		} else if row.SourceRef == nil || row.SourceRef.Name != "actual" || row.SourceRef.Namespace != "gitops" {
			t.Fatalf("namespace pick lost readable manager: %+v", row.SourceRef)
		}
	}
}

func TestApplicationsGraphCacheDoesNotCrossSources(t *testing.T) {
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
	useTestResourceCache(t, clusterWithDeployment("old-web"))
	before := cachedAppGraph(k8s.GetResourceCache(), []string{"demo"}, nil)
	useTestResourceCache(t, clusterWithDeployment("new-web"))
	after := cachedAppGraph(k8s.GetResourceCache(), []string{"demo"}, nil)
	if after.byID["deployment/demo/new-web"].Name != "new-web" || after.byID["deployment/demo/old-web"].Name != "" || before.byID["deployment/demo/old-web"].Name != "old-web" {
		t.Fatal("graph cache crossed source generations or mutated the old snapshot")
	}
}

func TestApplicationsReuseSharedGraphWithoutMutatingVisibility(t *testing.T) {
	client := clusterWithDeployment("web")
	useTestResourceCache(t, client)
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
	shared := &topology.Topology{Nodes: []topology.Node{
		{ID: "deployment/demo/web", Kind: topology.KindDeployment, Name: "web", Data: map[string]any{"namespace": "demo"}},
		{ID: "helmrelease/gitops/release", Kind: topology.KindHelmRelease, Name: "release", Data: map[string]any{"namespace": "gitops", "apiVersion": "helm.toolkit.fluxcd.io/v2"}},
	}, Edges: []topology.Edge{{ID: "manager-to-web", Source: "helmrelease/gitops/release", Target: "deployment/demo/web", Type: topology.EdgeManages}}}
	full := cachedAppGraph(k8s.GetResourceCache(), nil, func(uint64) *topology.Topology { return shared })
	limited := cachedAppGraph(k8s.GetResourceCache(), []string{"demo"}, func(uint64) *topology.Topology { return shared })
	root, _ := full.structuralRoot("deployment/demo/web")
	if root.Kind != topology.KindHelmRelease {
		t.Fatal("shared-only ownership edge was not reused")
	}
	root, _ = limited.structuralRoot("deployment/demo/web")
	if root.Kind != topology.KindDeployment {
		t.Fatal("hidden manager was not removed")
	}
	if len(shared.Nodes) != 2 || len(shared.Edges) != 1 {
		t.Fatal("scope filtering mutated shared graph")
	}
	if cachedAppGraph(k8s.GetResourceCache(), nil, func(uint64) *topology.Topology { t.Fatal("cache hit rebuilt broadcaster"); return nil }) != full {
		t.Fatal("full scope cache was contaminated")
	}
}

func TestApplicationsRejectsPreviousClusterSnapshotBeforeCallbacks(t *testing.T) {
	useTestResourceCache(t, clusterWithDeployment("web"))
	b := NewSSEBroadcaster()
	old := &topology.Topology{Nodes: []topology.Node{
		{ID: "deployment/demo/web", Kind: topology.KindDeployment, Name: "web", Data: map[string]any{"namespace": "demo"}},
		{ID: "helmrelease/demo/previous-cluster", Kind: topology.KindHelmRelease, Name: "previous-cluster", Data: map[string]any{"namespace": "demo", "apiVersion": "helm.toolkit.fluxcd.io/v2"}},
	}, Edges: []topology.Edge{{ID: "old-owner", Source: "helmrelease/demo/previous-cluster", Target: "deployment/demo/web", Type: topology.EdgeManages}}}
	b.updateCachedTopology(old, b.topoEpoch.Load(), k8s.GetResourceCache().Generation())
	if b.GetCachedTopology() != old {
		t.Fatal("current source snapshot rejected")
	}
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCache(clusterWithDeployment("web")); err != nil {
		t.Fatal(err)
	}
	// Deliberately do not run switch callbacks: this is their ordering window.
	if got := b.GetCachedTopology(); got != nil {
		t.Fatal("previous source snapshot accepted")
	}
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
	graph := cachedAppGraph(k8s.GetResourceCache(), nil, b.GetCachedTopologyForGeneration)
	root, ok := graph.structuralRoot("deployment/demo/web")
	if !ok || root.Kind != topology.KindDeployment {
		t.Fatalf("previous cluster owner was cached: %+v", root)
	}
}

func TestSharedSnapshotRejectsDifferentRequestSource(t *testing.T) {
	useTestResourceCache(t, clusterWithDeployment("web"))
	previous := k8s.GetResourceCache().Generation()
	useTestResourceCache(t, clusterWithDeployment("web"))
	b := NewSSEBroadcaster()
	snapshot := &topology.Topology{}
	current := k8s.GetResourceCache().Generation()
	b.updateCachedTopology(snapshot, b.topoEpoch.Load(), current)
	if b.GetCachedTopologyForGeneration(previous) != nil || b.GetCachedTopologyForGeneration(current) != snapshot {
		t.Fatal("request source was ignored")
	}
}
