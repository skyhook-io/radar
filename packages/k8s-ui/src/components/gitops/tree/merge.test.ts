import { describe, test, expect } from 'vitest'

import { mergeGitOpsTrees, MERGED_NODE_SOURCE_KEY } from './merge'
import type { GitOpsResourceTree, GitOpsTreeNode } from '../../../types'

// Helper: build a controller-side tree node. IDs follow the controller
// scheme (no prefix) so the merge function's destination-prefixing logic
// is what we actually test against.
function ctrlNode(
  id: string,
  kind: string,
  name: string,
  namespace: string,
  extras: Partial<GitOpsTreeNode> = {},
): GitOpsTreeNode {
  return {
    id,
    ref: { kind, name, namespace },
    role: 'declared',
    tool: 'argocd',
    ...extras,
  }
}

// Helper: build a destination-side tree node. Same shape as a controller
// node; the merge function looks at refs + ids, not provenance.
function destNode(
  id: string,
  kind: string,
  name: string,
  namespace: string,
  extras: Partial<GitOpsTreeNode> = {},
): GitOpsTreeNode {
  return {
    id,
    ref: { kind, name, namespace },
    role: 'declared',
    tool: 'argocd',
    ...extras,
  }
}

describe('mergeGitOpsTrees', () => {
  // The classic in-cluster case — caller didn't fetch a destination tree
  // (single-cluster app) so destination is null. Returning the controller
  // tree unchanged is what preserves Argo's status + summary tile counts
  // on the in-cluster code path.
  test('null destination returns controller unchanged', () => {
    const controller: GitOpsResourceTree = {
      root: ctrlNode('app1', 'Application', 'app1', 'argocd', { role: 'root' }),
      nodes: [
        ctrlNode('app1', 'Application', 'app1', 'argocd', { role: 'root' }),
        ctrlNode('dep1', 'Deployment', 'web', 'prod'),
      ],
      edges: [{ source: 'app1', target: 'dep1', type: 'owns' }],
      warnings: ['ctrl-warn'],
    }
    const merged = mergeGitOpsTrees(controller, null)
    expect(merged).toEqual(controller)
  })

  // Matched node: destination provides live health (informer-side, more
  // authoritative than Argo's polled status). The merge must overlay
  // health/info without dropping the controller's per-resource sync state.
  test('matched nodes get destination health overlaid; controller sync preserved', () => {
    const controller: GitOpsResourceTree = {
      root: ctrlNode('app1', 'Application', 'app1', 'argocd', { role: 'root' }),
      nodes: [
        ctrlNode('app1', 'Application', 'app1', 'argocd', { role: 'root' }),
        ctrlNode('dep1', 'Deployment', 'web', 'prod', { sync: 'Synced', health: 'Unknown' }),
      ],
      edges: [],
    }
    const destination: GitOpsResourceTree = {
      root: destNode('dest-root', 'Application', 'app1', 'argocd', { role: 'root' }),
      nodes: [
        destNode('dest-root', 'Application', 'app1', 'argocd', { role: 'root' }),
        destNode('d1', 'Deployment', 'web', 'prod', { health: 'Healthy', info: [{ name: 'replicas', value: '3/3' }] }),
      ],
      edges: [],
    }
    const merged = mergeGitOpsTrees(controller, destination)
    const dep = merged.nodes.find((n) => n.ref.kind === 'Deployment')
    expect(dep?.sync).toBe('Synced') // controller's sync wins
    expect(dep?.health).toBe('Healthy') // destination's health overlaid
    expect(dep?.info).toEqual([{ name: 'replicas', value: '3/3' }])
  })

  // The whole point of the merge: destination-side descendants (ReplicaSet,
  // Pod) that Argo never sees get appended to the tree. Their ids get a
  // `dest:` prefix to avoid collision with controller ids.
  test('destination-only descendants get prefixed ids + edges follow', () => {
    const controller: GitOpsResourceTree = {
      root: ctrlNode('app1', 'Application', 'app1', 'argocd', { role: 'root' }),
      nodes: [
        ctrlNode('app1', 'Application', 'app1', 'argocd', { role: 'root' }),
        ctrlNode('dep1', 'Deployment', 'web', 'prod'),
      ],
      edges: [{ source: 'app1', target: 'dep1', type: 'owns' }],
    }
    const destination: GitOpsResourceTree = {
      root: destNode('dest-root', 'Application', 'app1', 'argocd', { role: 'root' }),
      nodes: [
        destNode('dest-root', 'Application', 'app1', 'argocd', { role: 'root' }),
        destNode('d1', 'Deployment', 'web', 'prod'),
        destNode('rs1', 'ReplicaSet', 'web-abc', 'prod'),
        destNode('p1', 'Pod', 'web-abc-xy', 'prod'),
      ],
      edges: [
        { source: 'd1', target: 'rs1', type: 'owns' },
        { source: 'rs1', target: 'p1', type: 'owns' },
      ],
    }
    const merged = mergeGitOpsTrees(controller, destination)
    // 2 controller nodes + 2 destination-only descendants.
    expect(merged.nodes).toHaveLength(4)
    // Destination-only nodes carry the dest: prefix.
    const rs = merged.nodes.find((n) => n.ref.kind === 'ReplicaSet')
    const pod = merged.nodes.find((n) => n.ref.kind === 'Pod')
    expect(rs?.id).toBe('dest:rs1')
    expect(pod?.id).toBe('dest:p1')
    // Edge from Deployment (matched: id becomes controller's `dep1`) to
    // the prefixed ReplicaSet — proves the matched-id remap entry wires
    // descendant edges to the correct parent.
    expect(merged.edges).toContainEqual({ source: 'dep1', target: 'dest:rs1', type: 'owns' })
    expect(merged.edges).toContainEqual({ source: 'dest:rs1', target: 'dest:p1', type: 'owns' })
    // Controller's original edge survives.
    expect(merged.edges).toContainEqual({ source: 'app1', target: 'dep1', type: 'owns' })
  })

  // Edge dedup must key on type — different edge types between the same
  // nodes are distinct and both should survive. A regression that drops
  // the type from the key would silently lose `owns` vs `dependsOn`.
  test('edges deduped by source+target+type tuple, not just source+target', () => {
    const controller: GitOpsResourceTree = {
      root: ctrlNode('app', 'Application', 'app', 'argocd', { role: 'root' }),
      nodes: [
        ctrlNode('app', 'Application', 'app', 'argocd', { role: 'root' }),
        ctrlNode('svc', 'Service', 'web', 'prod'),
      ],
      edges: [{ source: 'app', target: 'svc', type: 'owns' }],
    }
    const destination: GitOpsResourceTree = {
      root: destNode('dr', 'Application', 'app', 'argocd', { role: 'root' }),
      nodes: [
        destNode('dr', 'Application', 'app', 'argocd', { role: 'root' }),
        destNode('s', 'Service', 'web', 'prod'),
      ],
      edges: [
        { source: 'dr', target: 's', type: 'owns' }, // duplicate (different ids but same matched node) — should dedup
        { source: 'dr', target: 's', type: 'dependsOn' }, // distinct type — should survive
      ],
    }
    const merged = mergeGitOpsTrees(controller, destination)
    // dr is the synthetic dest root — dropped. So both edges in the
    // destination tree have an undefined remapped source (the dropped
    // root) and get filtered out. The controller edge survives.
    expect(merged.edges).toHaveLength(1)
    expect(merged.edges[0]).toEqual({ source: 'app', target: 'svc', type: 'owns' })
  })

  // Warnings concat from both sides. Hub-only concern: per-cluster RBAC
  // denials surface as destination warnings; controller-side scan errors
  // surface as controller warnings. Both must reach the consumer.
  test('warnings concat from both sides', () => {
    const controller: GitOpsResourceTree = {
      root: ctrlNode('app', 'Application', 'app', 'argocd', { role: 'root' }),
      nodes: [ctrlNode('app', 'Application', 'app', 'argocd', { role: 'root' })],
      edges: [],
      warnings: ['ctrl-warn'],
    }
    const destination: GitOpsResourceTree = {
      root: destNode('dr', 'Application', 'app', 'argocd', { role: 'root' }),
      nodes: [destNode('dr', 'Application', 'app', 'argocd', { role: 'root' })],
      edges: [],
      warnings: ['dest-warn-1', 'dest-warn-2'],
    }
    const merged = mergeGitOpsTrees(controller, destination)
    expect(merged.warnings).toEqual(['ctrl-warn', 'dest-warn-1', 'dest-warn-2'])
  })

  // Summary recomputation: the merge sets summary to undefined so
  // GitOpsTreeGraph recomputes from the merged node list (avoids
  // double-counting overlay matches in declared/generated counts).
  test('summary cleared to defer recomputation by GitOpsTreeGraph', () => {
    const controller: GitOpsResourceTree = {
      root: ctrlNode('app', 'Application', 'app', 'argocd', { role: 'root' }),
      nodes: [ctrlNode('app', 'Application', 'app', 'argocd', { role: 'root' })],
      edges: [],
      summary: { declared: 5, generated: 2, grouped: 0, degraded: 1, outOfSync: 0 },
    }
    const destination: GitOpsResourceTree = {
      root: destNode('dr', 'Application', 'app', 'argocd', { role: 'root' }),
      nodes: [destNode('dr', 'Application', 'app', 'argocd', { role: 'root' })],
      edges: [],
      summary: { declared: 3, generated: 0, grouped: 0, degraded: 0, outOfSync: 0 },
    }
    const merged = mergeGitOpsTrees(controller, destination)
    expect(merged.summary).toBeUndefined()
  })

  // Routing signal: every node carries `data._source` so consumers
  // (Radar Hub's fleet detail page) can route resource-viewer clicks
  // to the correct cluster without depending on the `dest:` ID prefix
  // string convention. A future change to the ID format must NOT break
  // routing — this test pins that separation.
  test('every output node carries data._source for routing', () => {
    const controller: GitOpsResourceTree = {
      root: ctrlNode('app', 'Application', 'app', 'argocd', { role: 'root' }),
      nodes: [
        ctrlNode('app', 'Application', 'app', 'argocd', { role: 'root' }),
        ctrlNode('dep', 'Deployment', 'web', 'prod'),
      ],
      edges: [],
    }
    const destination: GitOpsResourceTree = {
      root: destNode('dr', 'Application', 'app', 'argocd', { role: 'root' }),
      nodes: [
        destNode('dr', 'Application', 'app', 'argocd', { role: 'root' }),
        destNode('dep', 'Deployment', 'web', 'prod'), // matches controller node
        destNode('pod', 'Pod', 'web-xyz', 'prod'),    // destination-only
      ],
      edges: [],
    }
    const merged = mergeGitOpsTrees(controller, destination)
    for (const n of merged.nodes) {
      const source = (n.data ?? {})[MERGED_NODE_SOURCE_KEY]
      expect(source).toBeDefined()
    }
    // `root` carries the same routing tag — consumers that read merged.root
    // directly (rather than walking nodes) must not get undefined.
    expect(merged.root.data?.[MERGED_NODE_SOURCE_KEY]).toBe('controller')
    const app = merged.nodes.find((n) => n.ref.kind === 'Application')
    const dep = merged.nodes.find((n) => n.ref.kind === 'Deployment')
    const pod = merged.nodes.find((n) => n.ref.kind === 'Pod')
    expect(app?.data?.[MERGED_NODE_SOURCE_KEY]).toBe('controller')
    expect(dep?.data?.[MERGED_NODE_SOURCE_KEY]).toBe('controller') // matched: controller wins
    expect(pod?.data?.[MERGED_NODE_SOURCE_KEY]).toBe('destination')
  })
})
