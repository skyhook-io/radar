import { describe, expect, it } from 'vitest'
import { buildTreeFacets } from '../GitOpsGraphFilterRail'
import { matchesGitOpsTreeFilters } from './tree-helpers'
import type { GitOpsResourceTree, GitOpsTreeNode } from '../../../types'

const node = (group: string): GitOpsTreeNode => ({ id: group, ref: { kind: 'Service', group, name: 'same', namespace: 'team' }, role: 'generated', tool: 'fluxcd' })

describe('GitOps kind filter contract', () => {
  it('uses confirmed absence in health facets rather than stale health', () => {
    const missing = { ...node(''), missing: true, health: 'Healthy' as const }
    expect(matchesGitOpsTreeFilters(missing, { health: ['Missing'] })).toBe(true)
    expect(matchesGitOpsTreeFilters(missing, { health: ['Healthy'] })).toBe(false)
    expect(buildTreeFacets({ nodes: [missing] } as GitOpsResourceTree).health).toEqual([{ name: 'Missing', count: 1 }])
  })
  it('uses bare Kubernetes kinds across API groups', () => {
    const nodes = [node(''), node('serving.knative.dev')]
    expect(buildTreeFacets({ nodes } as GitOpsResourceTree).kinds).toEqual([{ name: 'Service', count: 2 }])
    for (const n of nodes) {
      expect(matchesGitOpsTreeFilters(n, { kinds: new Set(['Service']) })).toBe(true)
      expect(matchesGitOpsTreeFilters(n, { kinds: new Set(['Deployment']) })).toBe(false)
    }
  })
})
