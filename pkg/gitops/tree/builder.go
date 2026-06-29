package tree

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/pkg/topology"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DynamicGetter is the small dynamic-cache surface needed by the tree builder.
type DynamicGetter interface {
	GetDynamicWithGroup(ctx context.Context, kind string, namespace string, name string, group string) (*unstructured.Unstructured, error)
}

// Builder constructs GitOps ownership trees from GitOps inventory and live topology ownership edges.
type Builder struct {
	dynamic           DynamicGetter
	topo              *topology.Topology
	allowedNamespaces []string
}

func NewBuilder(dynamic DynamicGetter, topo *topology.Topology) *Builder {
	return &Builder{dynamic: dynamic, topo: topo}
}

// WithAllowedNamespaces limits live object enrichment to namespaces the caller
// is allowed to inspect. nil means all namespaces; an empty slice means none.
func (b *Builder) WithAllowedNamespaces(namespaces []string) *Builder {
	b.allowedNamespaces = namespaces
	return b
}

// Build constructs the GitOps resource tree for the named root. Returns the
// live root object alongside the tree so callers (e.g. the insights handler)
// can derive additional views without re-fetching from the cache.
func (b *Builder) Build(ctx context.Context, kind, namespace, name, group string) (*ResourceTree, *unstructured.Unstructured, error) {
	if b.dynamic == nil {
		return nil, nil, fmt.Errorf("dynamic resource cache not available")
	}
	root, err := b.dynamic.GetDynamicWithGroup(ctx, kind, namespace, name, group)
	if err != nil {
		return nil, nil, err
	}
	if root.GetKind() == "" {
		root.SetKind(kind)
	}

	tool := detectTool(root, group, kind)
	managed := managedResources(root, tool)
	// HelmRelease has no status.inventory; recover its managed set from live
	// topology by Helm's recommended labels so the resource tree isn't empty.
	if tool == ToolFluxCD && strings.EqualFold(root.GetKind(), "HelmRelease") && len(managed) == 0 {
		managed = fluxHelmReleaseManaged(root, b.topoNodes())
	}
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
		obj := b.getAllowedObject(ctx, res.Ref)
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
			obj := b.getAllowedObject(ctx, res.Ref)
			// Derive sync/health from the related CR's own Ready/Reconciling/
			// Stalled conditions. Without this, source CRs render with empty
			// Health and the frontend falls back to the generic topology
			// builder — which derives Healthy for GitRepository but Unknown
			// for HelmRepository, producing inconsistent badges. Computing
			// from conditions here makes every Flux CR with Ready=True
			// render Healthy uniformly.
			sync, health := "", ""
			if obj != nil {
				s := rootStatus(obj, ToolFluxCD)
				sync, health = s.Sync, s.Health
			}
			if live, ok := findTopoNode(topoByRef, res.Ref); ok {
				nodes[id] = mergeData(enrichNodeFromObject(nodeFromTopology(live, res.Ref, RoleDeclared, tool, sync, health), obj), res.Data)
				topoIDByTreeID[id] = live.ID
				treeIDByTopoID[live.ID] = id
			} else if _, exists := nodes[id]; !exists {
				nodes[id] = mergeData(enrichNodeFromObject(syntheticNode(res.Ref, RoleDeclared, tool, sync, health), obj), res.Data)
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
			edges[edgeKey(id, targetID)] = Edge{Source: id, Target: targetID, Type: EdgeOwns}
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
		edges[edgeKey(rootNode.ID, id)] = Edge{Source: rootNode.ID, Target: id, Type: EdgeOwns}
	}

	nodeList, edgeList := materialize(nodes, edges)

	summary := summarize(nodeList)
	// Use the merged-in-the-nodes-map version of root so callers reading
	// tree.Root see the same enriched data (live status, topology metadata)
	// that any consumer iterating tree.Nodes would see. Without this, the
	// initial rootNode struct goes back unchanged while nodes[rootNode.ID]
	// has been mergeData'd with topology — two different views of the same
	// node, divergent silently.
	mergedRoot := rootNode
	if r, ok := nodes[rootNode.ID]; ok {
		mergedRoot = r
	}
	return &ResourceTree{
		Root:     mergedRoot,
		Nodes:    nodeList,
		Edges:    edgeList,
		Warnings: b.topoWarnings(),
		Summary:  summary,
	}, root, nil
}

func (b *Builder) getAllowedObject(ctx context.Context, ref ResourceRef) *unstructured.Unstructured {
	if ref.Name == "" || !b.canEnrich(ref) {
		return nil
	}
	obj, err := b.dynamic.GetDynamicWithGroup(ctx, ref.Kind, ref.Namespace, ref.Name, ref.Group)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Printf("[gitops/tree] enrich %s/%s %s/%s failed: %v", ref.Group, ref.Kind, ref.Namespace, ref.Name, err)
		}
		return nil
	}
	return obj
}

func (b *Builder) canEnrich(ref ResourceRef) bool {
	if b.allowedNamespaces == nil {
		return true
	}
	if ref.Namespace == "" {
		return false
	}
	for _, namespace := range b.allowedNamespaces {
		if namespace == ref.Namespace {
			return true
		}
	}
	return false
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
