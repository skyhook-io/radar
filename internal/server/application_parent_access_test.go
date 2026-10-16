package server

import (
	"context"
	"github.com/skyhook-io/radar/pkg/topology"
	"strings"
	"testing"
)

type appParentTestChecker struct {
	allow bool
	calls int
}

func (c *appParentTestChecker) CanRead(_ context.Context, group, kind, namespace string) bool {
	c.calls++
	return group != "parents.example.io" || c.allow
}

func TestApplicationParentAccessStopsBeforeUnreadableRoot(t *testing.T) {
	parent := topology.Node{ID: "parent", Kind: "ClusterParent", Name: "private-parent", Data: map[string]any{"apiVersion": "parents.example.io/v1"}}
	workload := topology.Node{ID: "workload", Kind: topology.KindDeployment, Name: "visible-workload", Data: map[string]any{"namespace": "team", "apiVersion": "apps/v1"}}
	topo := &topology.Topology{Nodes: []topology.Node{parent, workload}, Edges: []topology.Edge{{Source: "parent", Target: "workload", Type: topology.EdgeManages}}}
	shared := &appGraph{topo: topo, idx: topology.IndexByResource(topo), byID: map[string]topology.Node{"parent": parent, "workload": workload}}
	denied := &appParentTestChecker{}
	graph, deniedKey := applicationParentAccess(context.Background(), shared, denied)
	root, ok := graph.structuralRoot("workload")
	if !ok || root.Name != "visible-workload" {
		t.Fatalf("denied root exposed: %+v", root)
	}
	allowed := &appParentTestChecker{allow: true}
	graph, allowedKey := applicationParentAccess(context.Background(), shared, allowed)
	root, ok = graph.structuralRoot("workload")
	if !ok || root.Name != "private-parent" {
		t.Fatalf("readable root lost: %+v", root)
	}
	if deniedKey == allowedKey {
		t.Fatal("different permissions share cached rows")
	}
	if shared.readableParents != nil {
		t.Fatal("shared graph mutated")
	}
	if !strings.Contains(deniedKey, "false") || denied.calls != 1 {
		t.Fatalf("unexpected permission fingerprint %s calls=%d", deniedKey, denied.calls)
	}
}

func TestApplicationParentAccessDoesNotJumpDeniedAncestor(t *testing.T) {
	nodes := []topology.Node{
		{ID: "top", Kind: topology.KindDeployment, Name: "top", Data: map[string]any{"namespace": "team", "apiVersion": "apps/v1"}},
		{ID: "denied", Kind: "ClusterParent", Name: "hidden", Data: map[string]any{"apiVersion": "parents.example.io/v1"}},
		{ID: "child", Kind: topology.KindDeployment, Name: "child", Data: map[string]any{"namespace": "team", "apiVersion": "apps/v1"}},
	}
	topo := &topology.Topology{Nodes: nodes, Edges: []topology.Edge{{Source: "top", Target: "denied", Type: topology.EdgeManages}, {Source: "denied", Target: "child", Type: topology.EdgeManages}}}
	shared := &appGraph{topo: topo, idx: topology.IndexByResource(topo), byID: map[string]topology.Node{}}
	for _, n := range nodes {
		shared.byID[n.ID] = n
	}
	visible, _ := applicationParentAccess(context.Background(), shared, &appParentTestChecker{})
	root, _ := visible.structuralRoot("child")
	if root.ID != "child" {
		t.Fatalf("jumped unreadable ancestor: %+v", root)
	}
}

func TestApplicationRootPrefersObservedController(t *testing.T) {
	controller := true
	secondary := false
	nodes := []topology.Node{
		{ID: "secondary", Kind: topology.KindConfigMap, Name: "secondary", Data: map[string]any{"namespace": "team"}},
		{ID: "controller", Kind: topology.KindDeployment, Name: "controller", Data: map[string]any{"namespace": "team"}},
		{ID: "child", Kind: topology.KindDeployment, Name: "child", Data: map[string]any{"namespace": "team"}},
	}
	topo := &topology.Topology{Nodes: nodes, Edges: []topology.Edge{
		{Source: "secondary", Target: "child", Type: topology.EdgeManages, OwnerController: &secondary},
		{Source: "controller", Target: "child", Type: topology.EdgeManages, OwnerController: &controller},
	}}
	graph := &appGraph{topo: topo, idx: topology.IndexByResource(topo), byID: map[string]topology.Node{}}
	for _, n := range nodes {
		graph.byID[n.ID] = n
	}
	root, ok := graph.structuralRoot("child")
	if !ok || root.ID != "controller" {
		t.Fatalf("controller not selected: %+v", root)
	}
}
