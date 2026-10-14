import { describe, expect, it } from 'vitest'

import type { GitOpsResourceTree, GitOpsTreeEdge, GitOpsTreeNode } from '../../../types'
import { rankTree, renderedEdge, shortcutEdges, upstreamClosure, upstreamEdges } from './layout'

function node(id: string, kind = 'Deployment', role: GitOpsTreeNode['role'] = 'declared'): GitOpsTreeNode {
  return { id, ref: { kind, namespace: 'flux-system', name: id }, role, tool: 'fluxcd' }
}

function tree(nodes: GitOpsTreeNode[], edges: GitOpsTreeEdge[]): GitOpsResourceTree {
  return { root: nodes[0], nodes, edges }
}

describe('rankTree', () => {
  it('draws a release\'s generated chart and repository upstream, repository first', () => {
    // The release names the repository directly and through its chart; the
    // longer path wins so the chain reads repository → chart → release.
    const t = tree(
      [node('release', 'HelmRelease', 'root'), node('chart', 'HelmChart'), node('repo', 'HelmRepository'), node('deploy')],
      [
        { source: 'release', target: 'repo', type: 'source' },
        { source: 'release', target: 'chart', type: 'source' },
        { source: 'chart', target: 'repo', type: 'source' },
        { source: 'release', target: 'deploy', type: 'owns' },
      ],
    )
    const rank = rankTree(t)
    expect(rank.get('release')).toBe(0)
    expect(rank.get('chart')).toBe(-1)
    expect(rank.get('repo')).toBe(-2)
    expect(rank.get('deploy')).toBe(1)
  })

  it('keeps managed resources downstream even when they are also a source (bootstrap)', () => {
    const t = tree(
      [node('flux-system', 'Kustomization', 'root'), node('repo', 'GitRepository'), node('controller')],
      [
        { source: 'flux-system', target: 'repo', type: 'owns' },
        { source: 'flux-system', target: 'repo', type: 'source' },
        { source: 'flux-system', target: 'controller', type: 'owns' },
      ],
    )
    expect(upstreamEdges(t)).toEqual([])
    expect(rankTree(t).get('repo')).toBe(1)
  })

  it('places dependsOn targets upstream and keeps the owned subtree depth', () => {
    const t = tree(
      [node('overlay', 'Kustomization', 'root'), node('base', 'Kustomization'), node('repo', 'GitRepository'), node('deploy'), node('rs', 'ReplicaSet', 'generated'), node('pod', 'Pod', 'generated')],
      [
        { source: 'overlay', target: 'repo', type: 'source' },
        { source: 'overlay', target: 'base', type: 'dependsOn' },
        { source: 'overlay', target: 'deploy', type: 'owns' },
        { source: 'deploy', target: 'rs', type: 'owns' },
        { source: 'rs', target: 'pod', type: 'owns' },
      ],
    )
    const rank = rankTree(t)
    expect([rank.get('repo'), rank.get('base')]).toEqual([-1, -1])
    expect([rank.get('deploy'), rank.get('rs'), rank.get('pod')]).toEqual([1, 2, 3])
  })

  it('terminates on a dependency cycle and never moves the root', () => {
    const t = tree(
      [node('a', 'Kustomization', 'root'), node('b', 'Kustomization'), node('c', 'Kustomization')],
      [
        { source: 'a', target: 'b', type: 'dependsOn' },
        { source: 'b', target: 'c', type: 'dependsOn' },
        { source: 'c', target: 'b', type: 'dependsOn' },
        { source: 'c', target: 'a', type: 'dependsOn' },
      ],
    )
    const rank = rankTree(t)
    expect(rank.get('a')).toBe(0)
    expect(rank.get('b')).toBeLessThan(0)
    expect(rank.get('c')).toBeLessThan(0)
  })
})

describe('renderedEdge', () => {
  it('points a dependency edge into its consumer and leaves ownership alone', () => {
    const upstream = new Set(['repo'])
    expect(renderedEdge({ source: 'ks', target: 'repo', type: 'source' }, upstream)).toEqual({ source: 'repo', target: 'ks' })
    expect(renderedEdge({ source: 'ks', target: 'deploy', type: 'owns' }, upstream)).toEqual({ source: 'ks', target: 'deploy' })
  })
})

describe('upstreamClosure', () => {
  it('follows the chart hop so every preset keeps the whole source chain', () => {
    const t = tree(
      [node('release', 'HelmRelease', 'root'), node('chart', 'HelmChart'), node('repo', 'HelmRepository'), node('deploy')],
      [
        { source: 'release', target: 'chart', type: 'source' },
        { source: 'chart', target: 'repo', type: 'source' },
        { source: 'release', target: 'deploy', type: 'owns' },
      ],
    )
    expect([...upstreamClosure(t)].sort()).toEqual(['chart', 'repo'])
  })
})

describe('shortcutEdges', () => {
  it('drops the direct release → repository edge only when the chart path covers it', () => {
    const direct: GitOpsTreeEdge = { source: 'release', target: 'repo', type: 'source' }
    const viaChart: GitOpsTreeEdge[] = [{ source: 'release', target: 'chart', type: 'source' }, { source: 'chart', target: 'repo', type: 'source' }]
    const nodes = [node('release', 'HelmRelease', 'root'), node('chart', 'HelmChart'), node('repo', 'HelmRepository')]
    expect([...shortcutEdges(tree(nodes, [direct, ...viaChart]))]).toEqual([direct])
    // Before the chart is generated the direct edge is the only path.
    expect(shortcutEdges(tree(nodes, [direct])).size).toBe(0)
  })
})
