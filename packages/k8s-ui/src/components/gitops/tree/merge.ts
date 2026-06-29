import type { GitOpsResourceTree, GitOpsTreeEdge, GitOpsTreeNode, GitOpsTreeRef } from '../../../types/gitops-tree'

// =============================================================================
// mergeGitOpsTrees — used by Radar Hub's fleet GitOps detail page to compose
// a complete cross-cluster tree from two independent fetches:
//
//   1. The controller cluster's /api/gitops/tree/{kind}/{ns}/{name} response
//      ("controller tree"). Argo CD's view of the Application: the declared
//      resources from .status.resources + Argo's server-computed sync state
//      per resource. Truthful for "what does Argo think should exist + how
//      does it think those resources are doing", but Argo doesn't walk
//      ownerReferences past the resources it manages directly — it never
//      sees ReplicaSets, Pods, EndpointSlices, etc.
//
//   2. The destination cluster's /api/gitops/managed-resources?app=...
//      response ("destination tree"). Discovered by Argo's tracking
//      annotation in the destination cluster's actual workloads. Carries
//      the LIVE health/info from the in-cluster informer (more authoritative
//      for "is this pod actually running") AND the walked descendant
//      subtree (Deployment → ReplicaSet → Pod) that the controller can't see.
//
// Single-cluster Radar never needs this — for in-cluster apps the controller
// tree already has everything because Radar runs in the same cluster as
// the workloads. The merge is what makes Radar Hub's cross-cluster view
// strictly more complete than self-hosted Radar, not just a list of apps.
//
// Strategy: controller is the spine (preserves Argo's declared view +
// per-resource sync state); destination provides (a) live health/info
// overlay onto matching nodes, (b) descendant nodes Argo doesn't track.
// Destination's synthetic root is dropped; its non-root nodes that have
// no controller match are added with prefixed IDs to avoid ID collisions
// with the controller's node ids.
//
// Each output node carries an explicit `data._source` field set to
// `'controller'` or `'destination'`, which downstream consumers
// (Radar Hub's fleet detail page) use to route resource-viewer clicks
// to the correct cluster. The `dest:` ID prefix is preserved for graph
// rendering (duplicate IDs would break edge resolution) but NOT used as
// the routing signal — that's the explicit `data._source` field. This
// separation keeps a future ID-format change from silently breaking
// cross-cluster navigation.
// =============================================================================

// MergedNodeSource is the value of `node.data._source` after merge.
// Reading it from `data` (the documented extension point on
// GitOpsTreeNode) keeps the canonical type stable while giving
// consumers an explicit, type-checkable routing signal.
export type MergedNodeSource = 'controller' | 'destination'

// Key in node.data for the source tag. Exported so consumers don't
// hard-code the string; if it ever changes, both sides update together.
export const MERGED_NODE_SOURCE_KEY = '_source' as const

function refKey(ref: GitOpsTreeRef): string {
  return `${ref.group ?? ''}/${ref.kind}/${ref.namespace ?? ''}/${ref.name}`
}

// withSource returns a shallow copy of `node` whose data field carries
// the given source tag. Other data keys pass through unchanged.
function withSource(node: GitOpsTreeNode, source: MergedNodeSource): GitOpsTreeNode {
  return {
    ...node,
    data: { ...(node.data ?? {}), [MERGED_NODE_SOURCE_KEY]: source },
  }
}

export function mergeGitOpsTrees(
  controller: GitOpsResourceTree,
  destination: GitOpsResourceTree | null | undefined,
): GitOpsResourceTree {
  if (!destination) return controller

  // Index destination's non-root nodes by ref-key for overlay lookup.
  const destByKey = new Map<string, GitOpsTreeNode>()
  for (const n of destination.nodes) {
    if (n.role === 'root') continue
    destByKey.set(refKey(n.ref), n)
  }

  // Pass 1: walk controller nodes; for any match in destination, overlay
  // its live health, info, and topologyStatus onto the controller node.
  // Argo's per-resource sync state stays controller-side — it's
  // Argo-internal and the destination informer doesn't compute it.
  // Every controller node is tagged with _source='controller' for
  // resource-viewer routing.
  const nodes: GitOpsTreeNode[] = controller.nodes.map((n) => {
    const dest = destByKey.get(refKey(n.ref))
    const merged = dest
      ? {
          ...n,
          health: dest.health ?? n.health,
          info: dest.info ?? n.info,
          topologyStatus: dest.topologyStatus ?? n.topologyStatus,
        }
      : n
    return withSource(merged, 'controller')
  })

  // Pass 2: any destination node WITHOUT a controller match is a descendant
  // Argo doesn't track (ReplicaSet, Pod, EndpointSlice, etc.) — include
  // them with a remapped ID to avoid colliding with controller IDs.
  // destIdRemap is also used to rewrite destination edges in pass 3.
  const controllerKeys = new Set(controller.nodes.map((n) => refKey(n.ref)))
  const destOnly: GitOpsTreeNode[] = []
  const destIdRemap = new Map<string, string>()

  for (const n of destination.nodes) {
    if (n.role === 'root') continue
    const k = refKey(n.ref)
    if (controllerKeys.has(k)) {
      // Match — point destination's id at the controller's id for edge rewriting.
      const ctrlNode = nodes.find((c) => refKey(c.ref) === k)
      if (ctrlNode) destIdRemap.set(n.id, ctrlNode.id)
    } else {
      const newId = `dest:${n.id}`
      destIdRemap.set(n.id, newId)
      // dest: ID prefix is the collision-avoidance mechanism (graph
      // rendering breaks on duplicate IDs); _source='destination' is
      // the routing signal consumers actually read. Two distinct
      // concerns, two distinct fields.
      destOnly.push(withSource({ ...n, id: newId }, 'destination'))
    }
  }

  // Pass 3: edge merge. Controller edges stay as-is (they describe Argo's
  // declared topology). Destination edges between non-root nodes are
  // remapped via destIdRemap; skip any whose endpoint is the (dropped)
  // synthetic dest root, and skip duplicates of controller edges.
  const edgeKey = (e: GitOpsTreeEdge) => `${e.source}->${e.target}:${e.type}`
  const seen = new Set(controller.edges.map(edgeKey))
  const edges: GitOpsTreeEdge[] = [...controller.edges]

  for (const e of destination.edges) {
    const src = destIdRemap.get(e.source)
    const tgt = destIdRemap.get(e.target)
    if (!src || !tgt) continue // endpoint was the synthetic root — drop.
    const merged: GitOpsTreeEdge = { source: src, target: tgt, type: e.type }
    const k = edgeKey(merged)
    if (seen.has(k)) continue
    seen.add(k)
    edges.push(merged)
  }

  // Warnings concat — destination's warnings (e.g. partial RBAC denials on
  // some kinds during scan) surface alongside controller's.
  const warnings = [...(controller.warnings ?? []), ...(destination.warnings ?? [])]

  return {
    // `root` is tagged the same way nodes are — consumers reading
    // `merged.root.data._source` rely on the same contract as nodes.
    root: withSource(controller.root, 'controller'),
    nodes: [...nodes, ...destOnly],
    edges,
    warnings: warnings.length > 0 ? warnings : undefined,
    // Summary is derived UI metadata — let GitOpsTreeGraph recompute from
    // the merged node list rather than trying to add controller +
    // destination summaries (would double-count the overlay matches).
    summary: undefined,
  }
}
