package mcp

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	if before, _, _, ok := mcpTopologyForContext("demo"); !ok || !hasNode(before, "old-cluster-web") {
		t.Fatalf("seed: expected old-cluster-web in the resource-context topology, got ok=%v", ok)
	}

	useSwitchTestCluster(t, "new-cluster-api")
	after, _, _, ok := mcpTopologyForContext("demo")
	if !ok {
		t.Fatal("post-switch topology unavailable")
	}
	if hasNode(after, "old-cluster-web") || !hasNode(after, "new-cluster-api") {
		t.Errorf("post-switch resource-context topology still describes the previous cluster: %+v", after.Nodes)
	}
}
