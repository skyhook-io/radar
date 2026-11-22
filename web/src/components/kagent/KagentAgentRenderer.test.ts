import { describe, expect, it } from 'vitest'
import { ApiError } from '../../api/client'
import { kagentServersToRead, toRelatedRead } from './KagentAgentRenderer'

describe('toRelatedRead', () => {
  it('names why a referenced object was not read, never treating it as absent', () => {
    expect(toRelatedRead({ error: null, isLoading: false }, false)).toBeUndefined()
    expect(toRelatedRead({ data: { spec: {} }, error: null, isLoading: false }, true)).toEqual({ state: 'read', object: { spec: {} } })
    expect(toRelatedRead({ error: new ApiError('gone', 404), isLoading: false }, true)).toEqual({ state: 'notFound' })
    expect(toRelatedRead({ error: new ApiError('no', 403), isLoading: false }, true)).toEqual({ state: 'denied' })
    expect(toRelatedRead({ error: new ApiError('boom', 500), isLoading: false }, true)).toMatchObject({ state: 'failed', message: 'boom' })
    expect(toRelatedRead({ error: null, isLoading: true }, true)).toEqual({ state: 'loading' })
  })
})

describe('kagentServersToRead', () => {
  it('reads each kagent RemoteMCPServer once, skips other kinds, and stops at the drawer’s cap', () => {
    const ref = (name: string, kind = 'RemoteMCPServer', group = 'api.kagent.dev') => ({ kind, group, namespace: 'agents', name })
    expect(kagentServersToRead([ref('a'), ref('a'), ref('b', 'Server', 'example.com'), undefined]).map(r => r.name)).toEqual(['a'])
    expect(kagentServersToRead(Array.from({ length: 12 }, (_, i) => ref(`s${i}`)))).toHaveLength(8)
  })
})
