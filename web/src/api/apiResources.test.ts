import { describe, expect, it } from 'vitest'
import { hasKarpenterNodePools, hasPolicyReports } from './apiResources'
import type { APIResource } from '../types'

describe('hasKarpenterNodePools', () => {
  it('uses exact API discovery rather than top-CRD counts or kind name alone', () => {
    expect(hasKarpenterNodePools([{ name: 'nodepools', kind: 'NodePool', group: 'karpenter.sh', verbs: ['get', 'list'] } as any])).toBe(true)
    expect(hasKarpenterNodePools([{ name: 'nodepools', kind: 'NodePool', group: 'example.io', verbs: ['list'] } as any])).toBe(false)
    expect(hasKarpenterNodePools([{ name: 'nodepools', kind: 'NodePool', group: 'karpenter.sh', verbs: ['get'] } as any])).toBe(false)
    expect(hasKarpenterNodePools(undefined)).toBe(false)
  })
})

describe('hasPolicyReports', () => {
  const resource = (name: string, group: string) => ({ name, group }) as APIResource

  it('recognizes each report family by the resource names it actually serves', () => {
    expect(hasPolicyReports([resource('policyreports', 'wgpolicyk8s.io')])).toBe(true)
    expect(hasPolicyReports([resource('clusterpolicyreports', 'wgpolicyk8s.io')])).toBe(true)
    expect(hasPolicyReports([resource('reports', 'openreports.io')])).toBe(true)
    expect(hasPolicyReports([resource('clusterreports', 'openreports.io')])).toBe(true)
    expect(hasPolicyReports([resource('policyreports', 'openreports.io')])).toBe(false)
  })

  it('reads a cluster without them as having no policy engine', () => {
    expect(hasPolicyReports([resource('pods', ''), resource('policyreports', 'example.com')])).toBe(false)
    expect(hasPolicyReports(undefined)).toBe(false)
  })
})
