package tree

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/topology"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// isEnrichNotFound treats both miss shapes as "resource absent": typed-cache
// reads return apierrors NotFound, dynamic informer reads return the plain
// k8score sentinel. Absence is a normal state for tree enrichment — Missing
// resources and hub-spoke apps whose workloads live on a remote cluster —
// so neither deserves a log line.
func isEnrichNotFound(err error) bool {
	return apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound)
}

// enrichConcurrency bounds the parallel live-object enrichment fan-out per
// build. Most lookups are informer-cache hits (microseconds); the bound
// exists for the slow minority — first-touch informer warmup for CRD kinds
// and the CustomResourceDefinition/APIService direct-GET bypass — so a large
// app can't open unbounded apiserver connections.
const enrichConcurrency = 8

// ErrUnknownKindMatcher lets the builder classify provider errors without
// importing internal packages: hosts inject the sentinel their DynamicGetter
// returns for kinds missing from API discovery. Nil means no classification
// (every error is treated as ordinary).
type ErrUnknownKindMatcher func(error) bool

// DynamicGetter is the small dynamic-cache surface needed by the tree builder.
type DynamicGetter interface {
	GetDynamicWithGroup(ctx context.Context, kind string, namespace string, name string, group string) (*unstructured.Unstructured, error)
}

// Builder constructs GitOps ownership trees from GitOps inventory and live topology ownership edges.
type Builder struct {
	dynamic           DynamicGetter
	topo              *topology.Topology
	allowedNamespaces []string
	isUnknownKind     ErrUnknownKindMatcher
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

// WithUnknownKindMatcher wires the host's unknown-kind sentinel classifier
// (typically errors.Is against the dynamic cache's ErrUnknownDynamicKind).
// When set, refs whose kind is missing from API discovery are fetched once
// per kind instead of once per resource, logged once per kind per process,
// and surfaced as a single response warning.
func (b *Builder) WithUnknownKindMatcher(m ErrUnknownKindMatcher) *Builder {
	b.isUnknownKind = m
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

	var fluxRelated []relatedResource
	if tool == ToolFluxCD {
		fluxRelated = fluxRelatedResources(root)
	}
	enrichRefs := make([]ResourceRef, 0, len(managed)+len(fluxRelated))
	for _, res := range managed {
		enrichRefs = append(enrichRefs, res.Ref)
	}
	for _, res := range fluxRelated {
		enrichRefs = append(enrichRefs, res.Ref)
	}
	objects, unknownKinds := b.prefetchObjects(ctx, enrichRefs)

	for _, res := range managed {
		id := nodeID(res.Ref)
		declaredIDs[id] = true
		obj := objects[refKey(res.Ref)]
		if live, ok := findTopoNode(topoByRef, res.Ref); ok {
			nodes[id] = mergeData(enrichNodeFromObject(nodeFromTopology(live, res.Ref, RoleDeclared, tool, res.Sync, res.Health), obj), res.Data)
			topoIDByTreeID[id] = live.ID
			treeIDByTopoID[live.ID] = id
		} else {
			nodes[id] = mergeData(enrichNodeFromObject(syntheticNode(res.Ref, RoleDeclared, tool, res.Sync, res.Health), obj), res.Data)
		}
	}

	if tool == ToolFluxCD {
		for _, res := range fluxRelated {
			id := nodeID(res.Ref)
			if id == rootNode.ID {
				continue
			}
			obj := objects[refKey(res.Ref)]
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
	warnings := b.topoWarnings()
	if w := unknownKindsWarning(unknownKinds); w != "" {
		warnings = append(append([]string{}, warnings...), w)
	}
	return &ResourceTree{
		Root:     mergedRoot,
		Nodes:    nodeList,
		Edges:    edgeList,
		Warnings: warnings,
		Summary:  summary,
	}, root, nil
}

// unknownKindLog dedupes the "kind unavailable in discovery" log line
// process-wide: the GitOps detail page rebuilds the tree on every poll tick,
// so per-build logging still floods at 30 lines/min per absent kind. The
// response Warning (per build) is the user-facing signal; the log is for
// operators. Reset on kubeconfig context switch — a kind absent in one
// cluster may be absent in the next for a different reason, and that is
// worth one fresh line.
var unknownKindLog = struct {
	mu   sync.Mutex
	seen map[string]struct{}
}{seen: map[string]struct{}{}}

func logUnknownKindOnce(kind, group string) {
	key := kind + "|" + group
	unknownKindLog.mu.Lock()
	defer unknownKindLog.mu.Unlock()
	if _, ok := unknownKindLog.seen[key]; ok {
		return
	}
	unknownKindLog.seen[key] = struct{}{}
	log.Printf("[gitops/tree] kind %s (group %q) is unavailable in this cluster's API discovery; skipping live enrichment for its resources", kind, group)
}

// ResetUnknownKindLogDedup clears the process-wide unknown-kind log dedup.
// Hosts call it when the connected cluster changes.
func ResetUnknownKindLogDedup() {
	unknownKindLog.mu.Lock()
	defer unknownKindLog.mu.Unlock()
	unknownKindLog.seen = map[string]struct{}{}
}

// prefetchObjects resolves the live object for every enrichable ref with
// bounded parallelism, returning them keyed by refKey. Kinds the matcher
// classifies as unknown-to-discovery are negative-cached (best-effort — see
// the worker loop) so an absent CRD skips the bulk of its refs instead of
// paying one lookup per resource, and are returned (sorted) for the
// caller's response warning.
//
// Enrichment stays best-effort: per-ref failures nil out that ref's entry
// exactly like the serial loop did. Determinism of the final tree is owned
// by materialize's sort, so fetch-completion order is irrelevant.
func (b *Builder) prefetchObjects(ctx context.Context, refs []ResourceRef) (map[string]*unstructured.Unstructured, []string) {
	targets := make([]ResourceRef, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Name == "" || !b.canEnrich(ref) {
			continue
		}
		key := refKey(ref)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, ref)
	}
	if len(targets) == 0 {
		return map[string]*unstructured.Unstructured{}, nil
	}

	var (
		mu           sync.Mutex
		objects      = make(map[string]*unstructured.Unstructured, len(targets))
		unknownKinds = map[string]struct{}{}
	)
	kindKey := func(ref ResourceRef) string { return ref.Kind + "|" + ref.Group }

	// Fixed worker pool, not goroutine-per-ref: a 10k-resource app must cost
	// enrichConcurrency goroutines, not 10k queued behind a semaphore.
	workers := enrichConcurrency
	if len(targets) < workers {
		workers = len(targets)
	}
	work := make(chan ResourceRef)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ref := range work {
				mu.Lock()
				_, skip := unknownKinds[kindKey(ref)]
				mu.Unlock()
				if skip {
					continue
				}

				obj, err := b.dynamic.GetDynamicWithGroup(ctx, ref.Kind, ref.Namespace, ref.Name, ref.Group)
				if err != nil {
					if b.isUnknownKind != nil && b.isUnknownKind(err) {
						// Best-effort collapse: in-flight workers on the same
						// kind may still complete their lookup, but an
						// unknown-kind miss is a local discovery-map lookup —
						// no API round-trip — so the cache's job is skipping
						// the remaining queue and deduping the log, not
						// enforcing exactly-once.
						mu.Lock()
						unknownKinds[kindKey(ref)] = struct{}{}
						mu.Unlock()
						logUnknownKindOnce(ref.Kind, ref.Group)
					} else if !isEnrichNotFound(err) {
						log.Printf("[gitops/tree] enrich %s/%s %s/%s failed: %v", ref.Group, ref.Kind, ref.Namespace, ref.Name, err)
					}
					continue
				}
				mu.Lock()
				objects[refKey(ref)] = obj
				mu.Unlock()
			}
		}()
	}
	for _, ref := range targets {
		work <- ref
	}
	close(work)
	wg.Wait()

	if len(unknownKinds) == 0 {
		return objects, nil
	}
	labels := make([]string, 0, len(unknownKinds))
	for k := range unknownKinds {
		parts := strings.SplitN(k, "|", 2)
		label := parts[0]
		if len(parts) == 2 && parts[1] != "" {
			label = parts[0] + " (" + parts[1] + ")"
		}
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return objects, labels
}

// unknownKindsWarning renders the response warning for kinds that are absent
// from the cluster's API discovery. Deliberately says "unavailable" rather
// than "not installed": incomplete discovery, RBAC-limited discovery, and a
// remote-cluster Argo destination all produce the same miss.
func unknownKindsWarning(kinds []string) string {
	if len(kinds) == 0 {
		return ""
	}
	noun := "kind is"
	if len(kinds) > 1 {
		noun = "kinds are"
	}
	return fmt.Sprintf("%d resource %s unavailable in this cluster's API discovery (%s); those nodes reflect controller status only.", len(kinds), noun, strings.Join(kinds, ", "))
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
