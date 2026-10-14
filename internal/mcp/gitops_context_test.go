package mcp

import (
	"context"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	topo "github.com/skyhook-io/radar/pkg/topology"
)

func TestMCPContextKeepsReadableCrossNamespaceFluxInventory(t *testing.T) {
	freshSummaryCtxTopoMemo(t)
	uid := types.UID("web")
	replicas := int32(1)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo", UID: uid}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-rs", Namespace: "demo", UID: "web-rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: uid}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-pod", Namespace: "demo", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-rs", UID: rs.UID}}}}
	stalePod := pod.DeepCopy()
	stalePod.Name = "stale-pod"
	stalePod.Labels = map[string]string{"helm.toolkit.fluxcd.io/name": "stale", "helm.toolkit.fluxcd.io/namespace": "gitops"}
	if err := k8s.InitTestResourceCache(fake.NewClientset(dep, rs, pod, stalePod,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "gitops"}},
	)); err != nil {
		t.Fatal(err)
	}
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

	limited := withTestUserPerms(t, "limited-context", nil, []string{"demo"})
	perms := getPermCache().Get("limited-context", nil)
	perms.SetCanI("get", "apps", "deployments", "demo", true)
	perms.SetCanI("get", "apps", "replicasets", "demo", true)
	perms.SetCanI("get", gvr.Group, gvr.Resource, "gitops", false)
	for _, ctx := range []context.Context{context.Background(), limited, context.Background()} {
		for _, target := range []*corev1.Pod{pod, stalePod} {
			got := buildMCPResourceContext(ctx, target, "Pod", "demo", target.Name, resourcecontext.TierBasic, nil)
			if ctx == limited {
				for _, ref := range got.ManagedBy {
					if ref.Namespace == "gitops" {
						t.Fatalf("hidden manager leaked: %+v", got)
					}
				}
			} else if len(got.ManagedBy) != 1 || got.ManagedBy[0].Name != "actual" || got.ManagedBy[0].Namespace != "gitops" {
				t.Fatalf("%s resource context=%+v", target.Name, got)
			}
			list := newResourceSummaryContextBuilder(ctx, []string{"demo"})
			search := newSearchSummaryContextBuilder(ctx, []string{"demo"})
			for _, summary := range []*resourcecontext.ResourceSummaryContext{
				list(target, nil, "", "Pod", "demo", target.Name),
				search(target, nil, "", "Pod", "demo", target.Name),
			} {
				if ctx == limited {
					if summary != nil && summary.ManagedBy != nil && summary.ManagedBy.Namespace == "gitops" {
						t.Fatalf("hidden summary manager leaked: %+v", summary)
					}
					continue
				}
				if summary == nil || summary.ManagedBy == nil || summary.ManagedBy.Name != "actual" {
					t.Fatalf("%s summary=%+v", target.Name, summary)
				}
			}
		}
		native := pod.DeepCopy()
		native.Annotations = map[string]string{"meta.helm.sh/release-name": "native-release", "meta.helm.sh/release-namespace": "demo"}
		got := buildMCPResourceContext(ctx, native, "Pod", "demo", native.Name, resourcecontext.TierBasic, nil)
		if len(got.ManagedBy) != 1 || got.ManagedBy[0].Name != "native-release" || got.ManagedBy[0].Group != "" {
			t.Fatalf("native Helm metadata was reinterpreted as Flux: %+v", got.ManagedBy)
		}
		summary := newResourceSummaryContextBuilder(ctx, []string{"demo"})(native, nil, "", "Pod", "demo", native.Name)
		if summary == nil || summary.ManagedBy == nil || summary.ManagedBy.Source != "helm" || summary.ManagedBy.Name != "native-release" {
			t.Fatalf("native Helm summary=%+v", summary)
		}
	}
}

func TestMCPContextWithNoNamespacesKeepsClusterResources(t *testing.T) {
	freshSummaryCtxTopoMemo(t)
	setupFakeCacheForFilterTests(t)
	gvr := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1", "kind": "NodeClaim",
		"metadata": map[string]any{"name": "claim"},
		"status":   map[string]any{"nodeName": "node-1"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "NodeClaimList"}, claim)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "NodeClaim", Name: gvr.Resource, Namespaced: false, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	if _, err := k8s.GetDynamicResourceCache().List(gvr, ""); err != nil {
		t.Fatal(err)
	}
	if !k8s.GetDynamicResourceCache().WaitForSync(gvr, 2*time.Second) {
		t.Fatal("NodeClaim cache did not sync")
	}
	ctx := withTestUserPerms(t, "cluster-only-context", nil, []string{})
	grantClusterRead(t, "cluster-only-context", "/nodes", "karpenter.sh/nodeclaims")
	graph, _, _, ok := mcpTopologyForContext(ctx, scopedNamespacesForUser(ctx, nil))
	if !ok || !hasNode(graph, "node-1") || hasNode(graph, "alpha-pod") {
		t.Fatalf("cluster-only context scope=%+v", graph)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	got := buildMCPResourceContext(ctx, node, "Node", "", node.Name, resourcecontext.TierBasic, nil)
	if len(got.ManagedBy) != 1 || got.ManagedBy[0].Name != "claim" || got.ManagedBy[0].Group != "karpenter.sh" {
		t.Fatalf("authorized cluster owner lost: %+v", got)
	}
}

func TestHTTPContextReusesSourceSnapshotWithinReadableScope(t *testing.T) {
	freshSummaryCtxTopoMemo(t)
	useSwitchTestCluster(t, "web")
	shared := &topo.Topology{Nodes: []topo.Node{
		{ID: "readable", Data: map[string]any{"namespace": "demo"}},
		{ID: "hidden", Data: map[string]any{"namespace": "private"}},
		{ID: "cluster", Data: map[string]any{"namespace": ""}},
	}, Edges: []topo.Edge{{Source: "readable", Target: "hidden"}, {Source: "readable", Target: "cluster"}}}
	calls := 0
	ctx := context.WithValue(context.Background(), topologySnapshotKey{}, topologySnapshotGetter(func(generation uint64) *topo.Topology {
		calls++
		if generation != k8s.GetResourceCache().Generation() {
			t.Fatal("wrong source")
		}
		return shared
	}))
	graph, _, _, ok := mcpTopologyForContext(ctx, []string{"demo"})
	if !ok || len(graph.Nodes) != 2 || len(graph.Edges) != 1 {
		t.Fatal("HTTP snapshot not scoped/reused", graph)
	}
	mcpTopologyForContext(ctx, []string{"demo"})
	if calls != 1 || len(shared.Nodes) != 3 {
		t.Fatal("memo miss or shared mutation")
	}
	summaryCtxTopoMemo = topo.NewMemoizer(5 * time.Second)
	ctx = context.WithValue(context.Background(), topologySnapshotKey{}, topologySnapshotGetter(func(uint64) *topo.Topology { return nil }))
	fallback, _, _, ok := mcpTopologyForContext(ctx, []string{"demo"})
	if !ok || !hasNode(fallback, "web") {
		t.Fatal("standalone/cold fallback lost", fallback)
	}
}

func TestHTTPToolReceivesSharedSnapshotProvider(t *testing.T) {
	freshSummaryCtxTopoMemo(t)
	useSwitchTestCluster(t, "web")
	var calls atomic.Int32
	handler := WithTopologySnapshot(NewHandler(), func(generation uint64) *topo.Topology { calls.Add(1); return &topo.Topology{} })
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "snapshot-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "get_resource", Arguments: map[string]any{"kind": "Deployment", "group": "apps", "namespace": "demo", "name": "web"}})
	if err != nil || res.IsError {
		t.Fatalf("tool: %v %+v", err, res)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP MCP did not use shared provider: %d", calls.Load())
	}
}
