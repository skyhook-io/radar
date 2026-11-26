import { describe, expect, it } from 'vitest'
import type { ResourceCoverage } from '@skyhook-io/k8s-ui'
import { emptyNamespaceHint } from './namespaceHint'

const complete: ResourceCoverage = { status: 'complete', scope: { type: 'all_namespaces' }, gaps: [] }
const partial: ResourceCoverage = { status: 'partial', scope: { type: 'all_namespaces' }, gaps: [] }
const narrowed: ResourceCoverage = {
  status: 'complete',
  scope: { type: 'all_namespaces' },
  narrowedBy: ['user_access'],
  gaps: [],
}

describe('emptyNamespaceHint', () => {
  it('counts what exists in other namespaces', () => {
    expect(emptyNamespaceHint(15, complete)).toEqual({ type: 'elsewhere', count: 15, atLeast: false })
  })

  it('treats a partial or access-narrowed count as a lower bound', () => {
    expect(emptyNamespaceHint(15, partial)).toEqual({ type: 'elsewhere', count: 15, atLeast: true })
    expect(emptyNamespaceHint(15, narrowed)).toEqual({ type: 'elsewhere', count: 15, atLeast: true })
    expect(emptyNamespaceHint(15, undefined)).toEqual({ type: 'elsewhere', count: 15, atLeast: true })
  })

  it('says none elsewhere only when the count covers every namespace', () => {
    expect(emptyNamespaceHint(0, complete)).toEqual({ type: 'none-elsewhere' })
    expect(emptyNamespaceHint(0, partial)).toBeNull()
    expect(emptyNamespaceHint(0, narrowed)).toBeNull()
  })

  it('says nothing without a count', () => {
    expect(emptyNamespaceHint(undefined, complete)).toBeNull()
  })
})
