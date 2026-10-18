import { describe, expect, it } from 'vitest'
import type { ResourceReadCoverage } from '../types/resource-coverage'
import { boundedCountLabel, isEmptyCount, resourceCountLabel, resourceCoverageNotice, resourceEmptyMessage, resourceNarrowedNotice } from './resource-coverage'

const partial: ResourceReadCoverage = {
  status: 'partial', scope: { type: 'all_namespaces' },
  read: { type: 'namespaces', namespaces: { count: 1000 } },
  gaps: [{ code: 'radar_namespace_not_collected' }],
}
const complete: ResourceReadCoverage = { status: 'complete', gaps: [], scope: { type: 'all_namespaces' }, read: { type: 'all_namespaces' } }
const collected: ResourceReadCoverage = {
  status: 'complete', gaps: [], narrowedBy: ['namespace_collection'],
  scope: { type: 'namespaces', namespaces: { names: ['alpha', 'beta', 'gamma', 'ns-04', 'ns-05', 'ns-06', 'ns-07', 'ns-08', 'ns-09', 'ns-10', 'ns-11', 'ns-12'], count: 12 } },
  read: { type: 'namespaces', namespaces: { names: ['alpha', 'beta', 'gamma', 'ns-04', 'ns-05', 'ns-06', 'ns-07', 'ns-08', 'ns-09', 'ns-10', 'ns-11', 'ns-12'], count: 12 } },
}

describe('partial coverage notice', () => {
  it('counts a namespace set too large to name', () => {
    expect(resourceCoverageNotice(partial)).toContain('Radar read 1,000 namespaces;')
  })
  it('previews a named set', () => {
    expect(resourceCoverageNotice({ ...partial, read: { type: 'namespaces', namespaces: { names: ['alpha', 'beta', 'gamma', 'delta'], count: 4 } } })).toContain('alpha, beta, gamma and 1 more')
  })
  it('explains loading and collector denial without calling either absence', () => {
    expect(resourceCoverageNotice({ ...partial, gaps: [{ code: 'kind_sync_pending' }] })).toContain('still loading')
    expect(resourceCoverageNotice({ ...partial, gaps: [{ code: 'radar_forbidden' }] })).toContain("Radar's collector could not read")
    expect(resourceCoverageNotice(partial)).toContain("Radar doesn't collect this kind")
    expect(resourceCoverageNotice({ ...partial, gaps: [{ code: 'radar_namespace_not_collected' }, { code: 'kind_sync_pending' }] })).toContain('still loading')
  })
  it('says "requested" only of namespaces the request named', () => {
    expect(resourceCoverageNotice(partial)).toBe("Partial results. Radar read 1,000 namespaces; Radar doesn't collect this kind in some namespaces.")
    const named = { ...partial, scope: { type: 'namespaces' as const, namespaces: { names: ['alpha', 'zeta'], count: 2 } }, read: { type: 'namespaces' as const, namespaces: { names: ['alpha'], count: 1 } }, gaps: [{ code: 'radar_namespace_not_collected', namespaces: { names: ['zeta'], count: 1 } }] }
    expect(resourceCoverageNotice(named)).toBe("Partial results. Radar read alpha; Radar doesn't collect this kind in some requested namespaces.")
    expect(resourceCoverageNotice({ ...named, narrowedBy: ['user_access'] })).not.toContain('requested')
  })

  it('does not warn on a complete or unknown answer', () => {
    expect(resourceCoverageNotice(complete)).toBeUndefined()
    expect(resourceCoverageNotice(collected)).toBeUndefined()
    expect(resourceCoverageNotice({ status: 'unknown' })).toBeUndefined()
    expect(resourceCoverageNotice(undefined)).toBeUndefined()
  })
})

describe('narrowed coverage note', () => {
  it('names the namespaces Radar collects, bounded', () => {
    expect(resourceNarrowedNotice(collected)).toBe('Radar collects this kind only in 12 namespaces: alpha, beta, gamma and 9 more.')
    expect(resourceNarrowedNotice({ ...collected, scope: { type: 'namespaces', namespaces: { names: ['alpha'], count: 1 } } })).toBe('Radar collects this kind only in 1 namespace: alpha.')
  })
  it('is shown only for collection, which the namespace picker does not show', () => {
    for (const narrowing of ['user_access', 'namespace_pin', 'namespace_selection']) {
      expect(resourceNarrowedNotice({ ...collected, narrowedBy: [narrowing] })).toBeUndefined()
    }
    expect(resourceNarrowedNotice({ ...collected, narrowedBy: ['user_access', 'namespace_collection'] })).toBe('Radar collects this kind only in 12 of your namespaces: alpha, beta, gamma and 9 more.')
    expect(resourceNarrowedNotice(complete)).toBeUndefined()
    expect(resourceNarrowedNotice({ ...collected, status: 'partial' })).toBeUndefined()
    expect(resourceNarrowedNotice({ status: 'unknown' })).toBeUndefined()
  })
})

describe('empty lists and counts', () => {
  it('distinguishes complete, partial, unknown and filtered-empty results', () => {
    expect(resourceEmptyMessage('Pod', complete, false)).toBe('No Pod found in the requested scope')
    expect(resourceEmptyMessage('Pod', partial, false)).toContain('namespaces Radar could read')
    expect(resourceEmptyMessage('Pod', partial, true)).toContain('match the current filters')
  })
  it('keeps an older Radar\'s empty list as it read before coverage', () => {
    expect(resourceEmptyMessage('Pod', { status: 'unknown' }, false)).toBe('No Pod found')
    expect(resourceEmptyMessage('Pod', undefined, false)).toBe('No Pod found')
  })
  it('bounds partial counts and never treats a partial zero as absence', () => {
    expect(resourceCountLabel(23, partial)).toBe('≥23')
    expect(resourceCountLabel(0, partial)).toBe('–')
    expect(isEmptyCount(0, partial)).toBe(false)
  })
  it("never hides a kind whose zero covers only the namespaces Radar collects", () => {
    expect(isEmptyCount(0, collected)).toBe(false)
    expect(resourceEmptyMessage('Pod', collected, false)).toBe('No Pod found in the namespaces Radar collects')
  })
  it('shows complete and unknown counts plainly, and hides their zeros', () => {
    expect(resourceCountLabel(23, complete)).toBe('23')
    expect(resourceCountLabel(23, collected)).toBe('23')
    expect(resourceCountLabel(23, { status: 'unknown' })).toBe('23')
    expect(resourceCountLabel(0, { status: 'unknown' })).toBe('0')
    expect(resourceCountLabel(23, undefined)).toBe('23')
    expect(isEmptyCount(0, complete)).toBe(true)
    expect(isEmptyCount(0, { status: 'unknown' })).toBe(true)
    expect(isEmptyCount(0, undefined)).toBe(true)
    expect(isEmptyCount(3, complete)).toBe(false)
    expect(isEmptyCount(null, complete)).toBe(false)
  })
  it('formats large counts in either case', () => {
    expect(boundedCountLabel(12345, true)).toBe(`≥${(12345).toLocaleString()}`)
    expect(boundedCountLabel(12345, false)).toBe((12345).toLocaleString())
  })
})
