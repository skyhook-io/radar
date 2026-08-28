import { describe, expect, it } from 'vitest'

import type { ContextInfo } from '../types'
import { parseContextForSwitcher, visibleContextQualifier } from './context-name'

function context(name: string, originalName?: string): ContextInfo {
  return {
    name,
    originalName,
    cluster: 'cluster',
    user: 'user',
    namespace: '',
    isCurrent: false,
    source: 'eks',
  }
}

describe('parseContextForSwitcher', () => {
  it('preserves provider parsing while separating a backend qualifier', () => {
    const originalName = 'arn:aws:eks:us-east-1:123456789012:cluster/prod'
    const parsed = parseContextForSwitcher(context(`${originalName} (eks)`, originalName))

    expect(parsed.provider).toBe('EKS')
    expect(parsed.clusterName).toBe('prod')
    expect(parsed.region).toBe('us-east-1')
    expect(parsed.nameQualifier).toBe('(eks)')
  })

  it('keeps a qualifier after its colliding sibling disappears', () => {
    const parsed = parseContextForSwitcher(context('prod (secondary)', 'prod'))

    expect(parsed.raw).toBe('prod')
    expect(parsed.nameQualifier).toBe('(secondary)')
  })

  it('does not reinterpret a natural parenthesized name', () => {
    const parsed = parseContextForSwitcher(context('prod (config)', 'prod (config)'))

    expect(parsed.raw).toBe('prod (config)')
    expect(parsed.nameQualifier).toBeUndefined()
  })

  it('uses the visible name for direct single-file contexts', () => {
    const parsed = parseContextForSwitcher(context('local'))

    expect(parsed.raw).toBe('local')
    expect(parsed.nameQualifier).toBeUndefined()
  })
})

describe('visibleContextQualifier', () => {
  it('suppresses a qualifier repeated by the visible source label', () => {
    expect(visibleContextQualifier('(secondary)', 'secondary', true)).toBeUndefined()
  })

  it('retains qualifiers that distinguish identical source labels', () => {
    expect(visibleContextQualifier('(secondary #2)', 'secondary', true)).toBe('(secondary #2)')
  })

  it('retains the qualifier when the source label is hidden', () => {
    expect(visibleContextQualifier('(secondary)', 'secondary', false)).toBe('(secondary)')
  })
})
