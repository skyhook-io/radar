package tree

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const groupThreshold = 6

// DynamicGetter is the small dynamic-cache surface needed by the tree builder.
type DynamicGetter interface {
	GetDynamicWithGroup(ctx context.Context, kind string, namespace string, name string, group string) (*unstructured.Unstructured, error)
}

// Builder constructs GitOps ownership trees from GitOps inventory and live topology ownership edges.
type Builder struct {
	dynamic DynamicGetter
	topo    *topology.Topology
}

func NewBuilder(dynamic DynamicGetter, topo *topology.Topology) *Builder {
	return &Builder{dynamic: dynamic, topo: topo}
}

func (b *Builder) Build(ctx context.Context, kind, namespace, name, group string) (*ResourceTree, error) {
	if b.dynamic == nil {
		return nil, fmt.Errorf("dynamic resource cache not available")
	}
	root, err := b.dynamic.GetDynamicWithGroup(ctx, kind, namespace, name, group)
	if err != nil {
		return nil, err
	}
	if root.GetKind() == "" {
		root.SetKind(kind)
	}

	tool := detectTool(root, group, kind)
	managed := managedResources(root, tool)
	status := rootStatus(root, tool)
	rootRef := ResourceRef{
		Group:     apiGroup(root),
		Kind:      root.GetKind(),
		Namespace: root.GetNamespace(),
		Name:      root.GetName(),
		UID:       string(root.GetUID()),
	}
	rootNode := Node{
		ID:             nodeID(rootRef),
		Ref:            rootRef,
		Role:           RoleRoot,
		Tool:           tool,
		Sync:           status.Sync,
		Health:         status.Health,
		TopologyStatus: healthToTopology(status.Health),
		Data:           map[string]any{"namespace": rootRef.Namespace, "group": rootRef.Group},
	}
	rootNode = enrichNodeFromObject(rootNode, root)

	nodes := map[string]Node{rootNode.ID: rootNode}
	edges := map[string]Edge{}
	declaredIDs := map[string]bool{}

	topoByRef := map[string]topology.Node{}
	topoByID := map[string]topology.Node{}
	for _, n := range b.topoNodes() {
		ref := refFromTopologyNode(n)
		topoByRef[refKey(ref)] = n
		topoByRef[refKey(ResourceRef{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name})] = n
		topoByID[n.ID] = n
	}
	topoIDByTreeID := map[string]string{}
	treeIDByTopoID := map[string]string{}
	if liveRoot, ok := findTopoNode(topoByRef, rootRef); ok {
		topoIDByTreeID[rootNode.ID] = liveRoot.ID
		treeIDByTopoID[liveRoot.ID] = rootNode.ID
		nodes[rootNode.ID] = mergeData(rootNode, liveRoot.Data)
	}

	for _, res := range managed {
		id := nodeID(res.Ref)
		declaredIDs[id] = true
		var obj *unstructured.Unstructured
		if res.Ref.Name != "" {
			obj, _ = b.dynamic.GetDynamicWithGroup(ctx, res.Ref.Kind, res.Ref.Namespace, res.Ref.Name, res.Ref.Group)
		}
		if live, ok := findTopoNode(topoByRef, res.Ref); ok {
			nodes[id] = mergeData(enrichNodeFromObject(nodeFromTopology(live, res.Ref, RoleDeclared, tool, res.Sync, res.Health), obj), res.Data)
			topoIDByTreeID[id] = live.ID
			treeIDByTopoID[live.ID] = id
		} else {
			nodes[id] = mergeData(enrichNodeFromObject(syntheticNode(res.Ref, RoleDeclared, tool, res.Sync, res.Health), obj), res.Data)
		}
	}

	if tool == ToolFluxCD {
		for _, res := range fluxRelatedResources(root) {
			id := nodeID(res.Ref)
			if id == rootNode.ID {
				continue
			}
			var obj *unstructured.Unstructured
			if res.Ref.Name != "" {
				obj, _ = b.dynamic.GetDynamicWithGroup(ctx, res.Ref.Kind, res.Ref.Namespace, res.Ref.Name, res.Ref.Group)
			}
			if live, ok := findTopoNode(topoByRef, res.Ref); ok {
				nodes[id] = mergeData(enrichNodeFromObject(nodeFromTopology(live, res.Ref, RoleDeclared, tool, "", ""), obj), res.Data)
				topoIDByTreeID[id] = live.ID
				treeIDByTopoID[live.ID] = id
			} else if _, exists := nodes[id]; !exists {
				nodes[id] = mergeData(enrichNodeFromObject(syntheticNode(res.Ref, RoleDeclared, tool, "", ""), obj), res.Data)
			} else {
				nodes[id] = mergeData(nodes[id], res.Data)
			}
			edges[edgeKey(rootNode.ID, id)] = Edge{Source: rootNode.ID, Target: id, Type: res.Type}
		}
	}

	adj := map[string][]topology.Edge{}
	for _, e := range b.topoEdges() {
		if e.Type != topology.EdgeManages {
			continue
		}
		adj[e.Source] = append(adj[e.Source], e)
	}

	queue := make([]string, 0, len(declaredIDs)+1)
	for id := range declaredIDs {
		queue = append(queue, id)
	}
	if len(declaredIDs) == 0 {
		queue = append(queue, rootNode.ID)
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true

		sourceTopoID := topoIDByTreeID[id]
		if sourceTopoID == "" {
			continue
		}
		for _, e := range adj[sourceTopoID] {
			targetTopo, ok := topoByID[e.Target]
			if !ok {
				continue
			}
			targetRef := refFromTopologyNode(targetTopo)
			targetID := treeIDByTopoID[targetTopo.ID]
			if targetID == "" {
				targetID = nodeID(targetRef)
				treeIDByTopoID[targetTopo.ID] = targetID
				topoIDByTreeID[targetID] = targetTopo.ID
			}
			if _, exists := nodes[targetID]; !exists {
				nodes[targetID] = nodeFromTopology(targetTopo, targetRef, RoleGenerated, tool, "", "")
			}
			edges[edgeKey(id, targetID)] = Edge{Source: id, Target: targetID, Type: "owns"}
			queue = append(queue, targetID)
		}
	}

	hasParent := map[string]bool{}
	for _, e := range edges {
		hasParent[e.Target] = true
	}
	for id := range declaredIDs {
		if id == rootNode.ID || hasParent[id] {
			continue
		}
		edges[edgeKey(rootNode.ID, id)] = Edge{Source: rootNode.ID, Target: id, Type: "owns"}
	}

	nodeList, edgeList := materialize(nodes, edges)

	summary := summarize(nodeList)
	return &ResourceTree{
		Root:     rootNode,
		Nodes:    nodeList,
		Edges:    edgeList,
		Warnings: b.topoWarnings(),
		Summary:  summary,
	}, nil
}

func detectTool(root *unstructured.Unstructured, group, kind string) Tool {
	if group == "argoproj.io" || strings.EqualFold(root.GetKind(), "Application") || strings.Contains(strings.ToLower(kind), "application") {
		return ToolArgoCD
	}
	return ToolFluxCD
}

func managedResources(root *unstructured.Unstructured, tool Tool) []managedResource {
	if tool == ToolArgoCD {
		return parseArgoManagedResources(root)
	}
	return parseFluxManagedResources(root)
}

func (b *Builder) topoNodes() []topology.Node {
	if b.topo == nil {
		return nil
	}
	return b.topo.Nodes
}

func (b *Builder) topoEdges() []topology.Edge {
	if b.topo == nil {
		return nil
	}
	return b.topo.Edges
}

func (b *Builder) topoWarnings() []string {
	if b.topo == nil {
		return nil
	}
	return b.topo.Warnings
}

func findTopoNode(nodes map[string]topology.Node, ref ResourceRef) (topology.Node, bool) {
	if n, ok := nodes[refKey(ref)]; ok {
		return n, true
	}
	n, ok := nodes[refKey(ResourceRef{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name})]
	return n, ok
}

func materialize(nodes map[string]Node, edges map[string]Edge) ([]Node, []Edge) {
	nodeList := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		nodeList = append(nodeList, n)
	}
	sort.Slice(nodeList, func(i, j int) bool {
		if nodeList[i].Role != nodeList[j].Role {
			return rolePriority(nodeList[i].Role) < rolePriority(nodeList[j].Role)
		}
		if p := kindPriority(nodeList[i].Ref.Kind) - kindPriority(nodeList[j].Ref.Kind); p != 0 {
			return p < 0
		}
		return refKey(nodeList[i].Ref) < refKey(nodeList[j].Ref)
	})

	edgeList := make([]Edge, 0, len(edges))
	for _, e := range edges {
		edgeList = append(edgeList, e)
	}
	sort.Slice(edgeList, func(i, j int) bool {
		if edgeList[i].Source != edgeList[j].Source {
			return edgeList[i].Source < edgeList[j].Source
		}
		return edgeList[i].Target < edgeList[j].Target
	})
	return nodeList, edgeList
}
