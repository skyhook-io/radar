import { describe, expect, it } from 'vitest'
import { hasKarpenterNodePools } from './apiResources'

describe('hasKarpenterNodePools', () => {
  it('uses exact API discovery rather than top-CRD counts or kind name alone', () => {
    expect(hasKarpenterNodePools([{ name: 'nodepools', kind: 'NodePool', group: 'karpenter.sh', verbs: ['get', 'list'] } as any])).toBe(true)
    expect(hasKarpenterNodePools([{ name: 'nodepools', kind: 'NodePool', group: 'example.io', verbs: ['list'] } as any])).toBe(false)
    expect(hasKarpenterNodePools([{ name: 'nodepools', kind: 'NodePool', group: 'karpenter.sh', verbs: ['get'] } as any])).toBe(false)
    expect(hasKarpenterNodePools(undefined)).toBe(false)
  })
})
