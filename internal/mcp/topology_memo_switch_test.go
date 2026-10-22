package mcp

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
)

// Each test seeds summaryCtxTopoMemo through one call site while the cache
// holds the cluster the user is leaving, swaps the resource cache the way a
// context switch or namespace rescope does, then asks the same call site
// again. A call site that ignores the cache generation is handed the
// previous cluster's graph.

func useSwitchTestCluster(t *testing.T, deployment string) {
	t.Helper()
	replicas := int32(1)
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCache(fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: deployment, Namespace: "demo"},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": deployment}},
			},
		},
	)); err != nil {
		t.Fatalf("init test resource cache: %v", err)
	}
}

func freshSummaryCtxTopoMemo(t *testing.T) {
	t.Helper()
	prev := summaryCtxTopoMemo
	summaryCtxTopoMemo = topology.NewMemoizer(5 * time.Second)
	t.Cleanup(func() { summaryCtxTopoMemo = prev })
	t.Cleanup(k8s.ResetTestState)
}

func hasNode(topo *topology.Topology, name string) bool {
	if topo == nil {
		return false
	}
	for _, n := range topo.Nodes {
		if n.Name == name {
			return true
		}
	}
	return false
}

func TestSummaryContextTopologyAfterSwitch(t *testing.T) {
	freshSummaryCtxTopoMemo(t)
	useSwitchTestCluster(t, "old-cluster-web")
	if before := buildSummaryContextTopology([]string{"demo"}); !hasNode(before, "old-cluster-web") {
		t.Fatal("seed: expected old-cluster-web in the summary-context topology")
	}

	useSwitchTestCluster(t, "new-cluster-api")
	after := buildSummaryContextTopology([]string{"demo"})
	if hasNode(after, "old-cluster-web") || !hasNode(after, "new-cluster-api") {
		t.Errorf("post-switch summary-context topology still describes the previous cluster: %+v", after)
	}
}

func TestResourceContextTopologyAfterSwitch(t *testing.T) {
	freshSummaryCtxTopoMemo(t)
	useSwitchTestCluster(t, "old-cluster-web")
	if before, _, _, _, ok := mcpTopologyForContext(); !ok || !hasNode(before, "old-cluster-web") {
		t.Fatalf("seed: expected old-cluster-web in the resource-context topology, got ok=%v", ok)
	}

	useSwitchTestCluster(t, "new-cluster-api")
	after, _, _, _, ok := mcpTopologyForContext()
	if !ok {
		t.Fatal("post-switch topology unavailable")
	}
	if hasNode(after, "old-cluster-web") || !hasNode(after, "new-cluster-api") {
		t.Errorf("post-switch resource-context topology still describes the previous cluster: %+v", after.Nodes)
	}
}

func TestResourceContextUsesHostRelationshipGraph(t *testing.T) {
	freshSummaryCtxTopoMemo(t)
	useSwitchTestCluster(t, "cluster-web")
	host := &topology.Topology{Nodes: []topology.Node{{ID: "deployment/demo/host-graph", Kind: "Deployment", Name: "host-graph"}}}
	index := topology.IndexByResource(host)
	SetRelationshipTopologySource(func() (*topology.Topology, *topology.RelationshipsIndex) { return host, index })
	t.Cleanup(func() { SetRelationshipTopologySource(nil) })

	got, gotIndex, _, _, ok := mcpTopologyForContext()
	if !ok || got != host || gotIndex != index {
		t.Fatalf("resource context built its own graph instead of reusing the host's: %+v", got)
	}

	// Before the host has a graph, resource context still builds one.
	SetRelationshipTopologySource(func() (*topology.Topology, *topology.RelationshipsIndex) { return nil, nil })
	if built, _, _, _, ok := mcpTopologyForContext(); !ok || !hasNode(built, "cluster-web") {
		t.Fatalf("fallback build missing: %+v", built)
	}
}

func TestDeleteCascadePreviewTopologyAfterSwitch(t *testing.T) {
	freshSummaryCtxTopoMemo(t)
	setupMCPDynamicResource(t, schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "DeploymentList", k8s.APIResource{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true})
	preview := func(name string) map[string]any {
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": name, "namespace": "demo"}}}
		return deleteCascadePreview(context.Background(), obj, metav1.DeletePropagationBackground)
	}
	useSwitchTestCluster(t, "old-cluster-web")
	if before := preview("old-cluster-web"); before["rootResolved"] != true {
		t.Fatalf("seed: expected old-cluster-web in the delete preview topology: %+v", before)
	}

	useSwitchTestCluster(t, "new-cluster-api")
	if after := preview("new-cluster-api"); after["rootResolved"] != true {
		t.Errorf("post-switch delete preview still reads the previous cluster's topology: %+v", after)
	}
}
