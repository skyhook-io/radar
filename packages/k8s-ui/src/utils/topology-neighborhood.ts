import type { Topology, TopologyNode, TopologyEdge, EdgeType, NodeKind } from '../types/core'

// Seeded neighborhood query — the shared primitive behind the WorkloadView
// Topology tab (seed = one workload) and the Application topology (seed = the
// app's workloads). Given a full topology and a seed set, it returns the
// subgraph "everything relevant to these seeds": the seeds' ownership cores plus
// their attached context (Services, config, autoscalers, policies) — without
// letting a shared resource bridge in unrelated workloads.
//
// The traversal is the load-bearing part. Edges fall into three classes:
//
//   identity (`manages`)            — the ownerRef / controller chain
//                                     (Deployment→ReplicaSet→Pod). Walk it: a
//                                     workload's pods ARE the workload.
//   routing  (`exposes`,`routes-to`)— walk UPSTREAM from workload to Service to
//                                     Ingress/Route, then stop. Never walk back
//                                     DOWN through a shared Service into another
//                                     workload.
//   context  (`configures`,`uses`,  — a ConfigMap/Secret/HPA/PDB attached to the
//             `protects`)             workload. INCLUDE as a LEAF: a shared
//                                     ConfigMap mounted by two apps must not glue
//                                     them into one neighborhood.
//
// One more rule keeps it honest: managers reached UPWARD (a CronJob over a
// seed Job, a GitOps controller over a workload) are LEAVES — we show "managed
// by X" but never expand down to X's OTHER children (the same over-merge the
// app resolver's structuralRoot fix prevents, in graph form).
//
// The result is the raw subgraph; the caller hands it to <TopologyGraph/>.

export interface NeighborhoodSeed {
  kind: string
  group?: string
  namespace: string
  name: string
}

const IDENTITY_EDGES = new Set<EdgeType>(['manages'])
const ROUTING_EDGES = new Set<EdgeType>(['exposes', 'routes-to'])
const BATCH_RUN_FANOUT_LIMIT = 8
// context: 'configures' | 'uses' | 'protects' — everything else is leaf-attached.

// GitOps managers: included as context ("managed by"), never expanded through.
const GITOPS_MANAGER_KINDS = new Set<NodeKind>([
  'Application',
  'Kustomization',
  'HelmRelease',
  'GitRepository',
] as NodeKind[])
const SECRET_PRODUCER_KINDS = new Set<NodeKind>(['SealedSecret', 'Certificate'])

function nodeNamespace(node: TopologyNode): string {
  const ns = node.data?.namespace
  return typeof ns === 'string' ? ns : ''
}

function nodeGroup(node: TopologyNode): string {
  const apiVersion = node.data?.apiVersion
  return typeof apiVersion === 'string' && apiVersion.includes('/') ? apiVersion.split('/')[0] : ''
}

function isWorkflowTemplateKind(kind: NodeKind | string): boolean {
  return kind === 'WorkflowTemplate' || kind === 'ClusterWorkflowTemplate'
}

function isTemplateConfiguredRun(kind: NodeKind | string): boolean {
  return kind === 'Workflow' || kind === 'CronWorkflow'
}

function isTemplateToRunEdge(edge: TopologyEdge, nodeById: Map<string, TopologyNode>): boolean {
  return edge.type === 'configures'
    && isWorkflowTemplateKind(nodeById.get(edge.source)?.kind ?? '')
    && isTemplateConfiguredRun(nodeById.get(edge.target)?.kind ?? '')
}

function isBatchRunFanoutEdge(edge: TopologyEdge, nodeById: Map<string, TopologyNode>): boolean {
  const source = nodeById.get(edge.source)
  const target = nodeById.get(edge.target)
  if (!source || !target) return false
  if (edge.type === 'manages') {
    return (source.kind === 'CronJob' || source.kind === 'ScaledJob') && target.kind === 'Job'
      || source.kind === 'CronWorkflow' && target.kind === 'Workflow'
  }
  return isTemplateToRunEdge(edge, nodeById)
}

export function batchRunParentNodes(topology: Topology, run: TopologyNode): TopologyNode[] {
  const nodeById = new Map(topology.nodes.map((node) => [node.id, node]))
  const parentEdges = topology.edges.filter((edge) => edge.target === run.id && isBatchRunFanoutEdge(edge, nodeById))
  parentEdges.sort((left, right) => Number(right.type === 'manages') - Number(left.type === 'manages'))
  return parentEdges.flatMap((edge) => {
    const parent = nodeById.get(edge.source)
    return parent ? [parent] : []
  })
}

function nodeRunTime(node: TopologyNode): number {
  const data = node.data ?? {}
  for (const key of ['startedAt', 'finishedAt', 'startTime', 'completionTime', 'creationTimestamp']) {
    const value = data[key]
    if (typeof value !== 'string' || value === '') continue
    const parsed = Date.parse(value)
    if (Number.isFinite(parsed)) return parsed
  }
  return Number.NEGATIVE_INFINITY
}

function compareRunsNewestFirst(a: TopologyNode, b: TopologyNode): number {
  const aTime = nodeRunTime(a)
  const bTime = nodeRunTime(b)
  if (bTime !== aTime) return bTime - aTime
  return b.name.localeCompare(a.name)
}

function runDisposition(node: TopologyNode): 'active' | 'failed' | 'succeeded' | 'other' {
  const data = node.data ?? {}
  const phase = typeof data.phase === 'string' ? data.phase : ''
  if (phase === 'Running' || phase === 'Pending' || Number(data.active ?? 0) > 0) return 'active'
  if (phase === 'Failed' || phase === 'Error' || node.status === 'unhealthy') return 'failed'
  if (phase === 'Succeeded' || node.status === 'healthy' || Number(data.succeeded ?? 0) > 0) return 'succeeded'
  if (node.kind === 'Job' && !data.completionTime) return 'active'
  return 'other'
}

function representativeRuns(targets: TopologyNode[]): TopologyNode[] {
  const sorted = [...targets].sort(compareRunsNewestFirst)
  const active = sorted.filter((node) => runDisposition(node) === 'active')
  const selected: TopologyNode[] = active.length > BATCH_RUN_FANOUT_LIMIT
    ? [...active.slice(0, BATCH_RUN_FANOUT_LIMIT - 1), active[active.length - 1]]
    : active
  const addLatest = (disposition: 'failed' | 'succeeded') => {
    const node = sorted.find((candidate) => runDisposition(candidate) === disposition)
    if (node && !selected.some((candidate) => candidate.id === node.id)) selected.push(node)
  }
  addLatest('failed')
  addLatest('succeeded')
  if (selected.length === 0 && sorted[0]) selected.push(sorted[0])
  return selected
}

function batchFanoutLimit(edge: TopologyEdge, nodeById: Map<string, TopologyNode>): number | null {
  return isBatchRunFanoutEdge(edge, nodeById) ? BATCH_RUN_FANOUT_LIMIT : null
}

/** The identity string for a workload/seed — `kind/namespace/name`. This format
 *  is a cross-module contract: rail rows, the `?workload=` URL param, hover
 *  focus, and the ownership stamp all compare these strings. Always construct
 *  through here; never inline the template. (Unambiguous: K8s kinds and
 *  DNS-1123 names cannot contain `/`.) */
export function workloadKey(ref: NeighborhoodSeed): string {
  return `${ref.kind}/${ref.namespace}/${ref.name}`
}

function matchSeedNode(node: TopologyNode, seeds: NeighborhoodSeed[]): boolean {
  return seeds.some((s) => {
    if (s.kind !== node.kind || s.name !== node.name || s.namespace !== nodeNamespace(node)) return false
    return !s.group || s.group === nodeGroup(node)
  })
}

/** Filter a topology to the neighborhood of `seeds`. Returns the subgraph; an
 *  empty graph (with a warning) when no seed node matches. */
export function neighborhoodFor(topology: Topology, seeds: NeighborhoodSeed[]): Topology {
  const nodeById = new Map<string, TopologyNode>()
  for (const n of topology.nodes) nodeById.set(n.id, n)

  const seedIds = new Set<string>()
  for (const n of topology.nodes) {
    if (matchSeedNode(n, seeds)) seedIds.add(n.id)
  }
  if (seedIds.size === 0) {
    return {
      ...topology,
      nodes: [],
      edges: [],
      warnings: [...(topology.warnings ?? []), 'No topology nodes matched this selection.'],
    }
  }

  // adjacency (both directions) for the bounded walk.
  const adjacency = new Map<string, TopologyEdge[]>()
  for (const e of topology.edges) {
    for (const id of [e.source, e.target]) {
      if (!adjacency.has(id)) adjacency.set(id, [])
      adjacency.get(id)!.push(e)
    }
  }

  const limitedFanouts = new Map<string, { allowed: Set<string>; total: number; kind: string; activeTotal: number; activeShown: number }>()
  for (const [sourceId, edges] of adjacency) {
    const runEdges = edges.filter((edge) => edge.source === sourceId && batchFanoutLimit(edge, nodeById) !== null)
    if (runEdges.length <= BATCH_RUN_FANOUT_LIMIT) continue
    const targets = runEdges
      .map((edge) => nodeById.get(edge.target))
      .filter((node): node is TopologyNode => !!node)
      .sort(compareRunsNewestFirst)
    const representatives = representativeRuns(targets)
    limitedFanouts.set(sourceId, {
      allowed: new Set(representatives.map((node) => node.id)),
      total: targets.length,
      kind: targets[0]?.kind === 'Job' ? 'Job' : 'Workflow',
      activeTotal: targets.filter((node) => runDisposition(node) === 'active').length,
      activeShown: representatives.filter((node) => runDisposition(node) === 'active').length,
    })
  }

  const keep = new Set(seedIds)
  // Nodes included for context but never expanded THROUGH.
  const leaf = new Set<string>()
  const queue: string[] = Array.from(seedIds)
  const cappedSources = new Set<string>()

  while (queue.length) {
    const id = queue.shift()!
    if (leaf.has(id)) continue // a leaf is a dead end — don't traverse out of it
    const currentNode = nodeById.get(id)
    for (const e of adjacency.get(id) ?? []) {
      const nextId = e.source === id ? e.target : e.source
      const nextNode = nodeById.get(nextId)
      if (!nextNode) continue
      const fanout = e.source === id ? limitedFanouts.get(id) : undefined
      if (fanout && !fanout.allowed.has(nextId)) {
        cappedSources.add(id)
        continue
      }

      let asLeaf: boolean
      if (IDENTITY_EDGES.has(e.type)) {
        // ownerRef chain: DOWNWARD (owner → child) is identity — a workload's
        // pods ARE the workload. UPWARD (child → its manager: a CronJob, a
        // GitOps controller) is context — include "managed by X" as a leaf,
        // never fan out to X's other children (a seed Job must not drag in
        // every sibling Job its CronJob owns).
        asLeaf = nextId === e.source
      } else if (ROUTING_EDGES.has(e.type)) {
        if (e.type === 'exposes' && id === e.source) {
          // We reached a Service from one of its workloads. Only continue
          // upstream to its routes; following the Service's other targets would
          // pull sibling or unrelated workloads into this neighborhood.
          continue
        }
        // A Service reached from a workload may expand once more to the
        // Ingress/Route in front of it. The entrypoint itself remains a leaf.
        asLeaf = e.type === 'routes-to' || nextNode.kind !== 'Service'
      } else if (currentNode && isTemplateToRunEdge(e, nodeById) && e.source === currentNode.id) {
        asLeaf = false
      } else {
        asLeaf = true // configures / uses / protects — leaf
      }
      if (!keep.has(nextId)) {
        keep.add(nextId)
        if (asLeaf) leaf.add(nextId)
        queue.push(nextId)
      }

      if (asLeaf && nextNode.kind === 'Secret') {
        for (const producerEdge of adjacency.get(nextId) ?? []) {
          if (producerEdge.type !== 'manages' || producerEdge.target !== nextId) continue
          const producer = nodeById.get(producerEdge.source)
          if (!producer || !SECRET_PRODUCER_KINDS.has(producer.kind)) continue
          keep.add(producer.id)
          leaf.add(producer.id)
        }
      }
    }
  }

  return {
    ...topology,
    nodes: topology.nodes.filter((n) => keep.has(n.id)),
    edges: topology.edges.filter((e) => keep.has(e.source) && keep.has(e.target)),
    warnings: [
      ...(topology.warnings ?? []),
      ...Array.from(cappedSources).map((sourceId) => {
        const source = nodeById.get(sourceId)
        const fanout = limitedFanouts.get(sourceId)
        const activeNote = fanout && fanout.activeTotal > fanout.activeShown ? ` showing the ${fanout.activeShown - 1} newest and oldest of ${fanout.activeTotal} active runs plus representative completed runs` : ' showing active and representative completed runs'
        return `Topology view:${activeNote} from ${fanout?.total ?? BATCH_RUN_FANOUT_LIMIT} retained ${fanout?.kind ?? 'batch'} runs for ${source?.kind ?? 'workload'}/${source?.name ?? sourceId}. Use Run history for the full retained set.`
      }),
    ],
  }
}

// ─── Workload ownership tagging ──────────────────────────────────────────────
//
// For the application graph (seeds = the app's workloads) we want to show which
// resources belong to which workload. A resource is "owned" by a workload when
// it belongs to that workload ALONE — its pods (manages-descendants), and the
// Service/config/policy attached to exactly one workload. Anything attached to
// two or more workloads (a shared ConfigMap, a GitOps manager) stays NEUTRAL, as
// does anything attached to none. This is the visual twin of the leaf rule: the
// graph already refuses to bridge through shared resources, and here they
// refuse to claim a color.

/** What `tagWorkloadOwnership` stamps into each node's `data`:
 *   - `ownerWorkloadId` + `ownerColorIndex` — the EXCLUSIVE owner, for the color
 *     wash. Shared nodes are null (neutral).
 *   - `focusWorkloadIds` — every workload whose neighborhood includes this node,
 *     for hover-focus. A shared ConfigMap belongs to all workloads that use it,
 *     so focusing any of them lights it up (matching the single-workload
 *     topology), even though it stays neutral-colored.
 *  Consumers MUST read via `ownershipOf` — never cast the raw data keys. */
export interface OwnershipStamp {
  ownerWorkloadId: string | null
  ownerColorIndex: number | null
  focusWorkloadIds: string[]
}

/** The single audited reader for the ownership stamp. Tolerates untagged nodes
 *  (plain topologies) by returning the neutral stamp. */
export function ownershipOf(data: Record<string, unknown> | undefined): OwnershipStamp {
  return {
    ownerWorkloadId: typeof data?.ownerWorkloadId === 'string' ? data.ownerWorkloadId : null,
    ownerColorIndex: typeof data?.ownerColorIndex === 'number' ? data.ownerColorIndex : null,
    focusWorkloadIds: Array.isArray(data?.focusWorkloadIds) ? (data.focusWorkloadIds as string[]) : [],
  }
}

export interface WorkloadOwnership {
  /** The neighborhood subgraph, each node's `data` carrying an OwnershipStamp. */
  topology: Topology
  /** Color index per workload key (see `workloadKey`) — for rail swatches. */
  colorByWorkload: Map<string, number>
}

/** Run the neighborhood query for `seeds`, then tag each node with the workload
 *  that exclusively owns it (or neutral). Returns the tagged subgraph plus the
 *  color + node-ownership maps the application rail needs. */
export function tagWorkloadOwnership(topology: Topology, seeds: NeighborhoodSeed[]): WorkloadOwnership {
  const sub = neighborhoodFor(topology, seeds)

  // Stable color per workload: order of `seeds` (matches the rail's order).
  const colorByWorkload = new Map<string, number>()
  for (const s of seeds) {
    const k = workloadKey(s)
    if (!colorByWorkload.has(k)) colorByWorkload.set(k, colorByWorkload.size)
  }

  // The seed nodes present in the subgraph, by their workload key.
  const seedKeyById = new Map<string, string>()
  const subNodeById = new Map(sub.nodes.map((node) => [node.id, node]))
  for (const n of sub.nodes) {
    if (matchSeedNode(n, seeds)) {
      seedKeyById.set(n.id, workloadKey({ kind: n.kind, namespace: nodeNamespace(n), name: n.name }))
    }
  }

  // manages-DOWN children, template-to-run provenance, and undirected neighbors.
  const nodeById = new Map<string, TopologyNode>()
  for (const n of sub.nodes) nodeById.set(n.id, n)
  const managedChildren = new Map<string, string[]>()
  const templateRunChildren = new Map<string, string[]>()
  const neighbors = new Map<string, Set<string>>()
  for (const e of sub.edges) {
    if (e.type === 'manages') {
      if (!managedChildren.has(e.source)) managedChildren.set(e.source, [])
      managedChildren.get(e.source)!.push(e.target)
    } else if (isTemplateToRunEdge(e, nodeById)) {
      if (!templateRunChildren.has(e.source)) templateRunChildren.set(e.source, [])
      templateRunChildren.get(e.source)!.push(e.target)
    }
    for (const [a, b] of [[e.source, e.target], [e.target, e.source]] as const) {
      if (!neighbors.has(a)) neighbors.set(a, new Set())
      neighbors.get(a)!.add(b)
    }
  }

  // Scheduler/controller ownership is authoritative. Claim every seed and its
  // manages descendants first, then let template seeds claim only unowned runs
  // and their descendants. A shared template remains provenance when a
  // CronWorkflow in the same app already owns the Workflow.
  const coreOwner = new Map<string, string>()
  for (const [seedId, key] of seedKeyById) {
    const queue = [seedId]
    while (queue.length) {
      const id = queue.shift()!
      if (coreOwner.has(id)) continue
      coreOwner.set(id, key)
      for (const c of managedChildren.get(id) ?? []) if (!coreOwner.has(c)) queue.push(c)
    }
  }
  for (const [seedId, key] of seedKeyById) {
    const seed = nodeById.get(seedId)
    if (!seed || !isWorkflowTemplateKind(seed.kind)) continue
    const queue = [...(templateRunChildren.get(seedId) ?? [])]
    while (queue.length) {
      const id = queue.shift()!
      if (coreOwner.has(id)) continue
      coreOwner.set(id, key)
      for (const c of managedChildren.get(id) ?? []) if (!coreOwner.has(c)) queue.push(c)
    }
  }

  // For each node, figure out which workloads it belongs to. A core node (the
  // workload itself + its manages-descendants) belongs to its own workload. Any
  // other node belongs to every workload-core it touches: that's its focus set.
  // The color owner is the EXCLUSIVE case only — a node touching exactly one
  // workload and not a GitOps manager (managers are context, never owned).
  const nodes = sub.nodes.map((n) => {
    const core = coreOwner.get(n.id) ?? null
    let focusWorkloadIds: string[]
    let owner: string | null
    if (core) {
      focusWorkloadIds = [core]
      owner = core
    } else {
      const related = new Set<string>()
      for (const nb of neighbors.get(n.id) ?? []) {
        const o = coreOwner.get(nb)
        if (o) related.add(o)
        const neighborNode = subNodeById.get(nb)
        if (neighborNode?.kind === 'Service') {
          for (const serviceNeighbor of neighbors.get(nb) ?? []) {
            const serviceOwner = coreOwner.get(serviceNeighbor)
            if (serviceOwner) related.add(serviceOwner)
          }
        }
        if (SECRET_PRODUCER_KINDS.has(n.kind) && neighborNode?.kind === 'Secret') {
          for (const secretNeighbor of neighbors.get(nb) ?? []) {
            const secretOwner = coreOwner.get(secretNeighbor)
            if (secretOwner) related.add(secretOwner)
          }
        }
      }
      focusWorkloadIds = [...related]
      owner = related.size === 1 && !GITOPS_MANAGER_KINDS.has(n.kind) ? [...related][0] : null
    }
    const stamp: OwnershipStamp = {
      ownerWorkloadId: owner,
      ownerColorIndex: owner ? colorByWorkload.get(owner) ?? null : null,
      focusWorkloadIds,
    }
    return { ...n, data: { ...n.data, ...stamp } }
  })

  return { topology: { ...sub, nodes }, colorByWorkload }
}

/** The set of node IDs that are the seeds themselves — handy for the caller to
 *  pass `focusNodeId` (pan/zoom to the workload) into <TopologyGraph/>. */
export function seedNodeIds(topology: Topology, seeds: NeighborhoodSeed[]): string[] {
  return topology.nodes.filter((n) => matchSeedNode(n, seeds)).map((n) => n.id)
}
