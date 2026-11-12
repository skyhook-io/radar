import { describe, expect, it } from 'vitest'
import { toPlanNodes } from './UpgradePlanRenderer'

const node = { metadata: { name: 'n1', labels: { role: 'cp' } } }

describe('toPlanNodes', () => {
  it('passes Nodes as name and labels', () => {
    expect(toPlanNodes({ data: [node], coverage: { status: 'complete' } as any, error: null, isLoading: false })).toEqual({ nodes: [{ name: 'n1', labels: { role: 'cp' } }], loading: false })
  })
  it('treats a denied or partial Node read as unreadable', () => {
    expect(toPlanNodes({ data: undefined, error: new Error('403'), isLoading: false }).nodes).toBeNull()
    expect(toPlanNodes({ data: [node], coverage: { status: 'partial' } as any, error: null, isLoading: false }).nodes).toBeNull()
  })
  it('accepts a list from an older Radar that reports no coverage', () => {
    expect(toPlanNodes({ data: [node], coverage: { status: 'unknown' } as any, error: null, isLoading: false }).nodes).toHaveLength(1)
  })
  it('waits while Nodes load', () => {
    expect(toPlanNodes({ data: undefined, error: null, isLoading: true })).toEqual({ nodes: undefined, loading: true })
  })
})
