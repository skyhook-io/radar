import { describe, expect, it } from 'vitest'
import { ApiError } from '../../api/client'
import { relatedReadFrom, relatedReadInList } from './useRelatedReads'

const fail = (status: number, error_code?: string) => new ApiError('failed', status, error_code ? { error_code } : {})

describe('relatedReadFrom', () => {
  it('reports absence only when the server proved it', () => {
    expect(relatedReadFrom({ error: fail(404, 'object_not_found') })).toEqual({ state: 'not_found' })
    expect(relatedReadFrom({ error: fail(404) })).toEqual({ state: 'unconfirmed' })
    expect(relatedReadFrom({ error: fail(404, 'namespace_not_found') })).toMatchObject({ state: 'unavailable', code: 'namespace_not_found' })
  })

  it('keeps denials and collector limits apart', () => {
    expect(relatedReadFrom({ error: fail(403, 'user_forbidden') })).toMatchObject({ state: 'unavailable', code: 'user_forbidden' })
    expect(relatedReadFrom({ error: fail(403) })).toMatchObject({ state: 'unavailable', code: undefined })
    expect(relatedReadFrom({ error: fail(503, 'radar_forbidden') })).toMatchObject({ state: 'unavailable', code: 'radar_forbidden' })
    expect(relatedReadFrom({ error: fail(503, 'kind_sync_timed_out') })).toMatchObject({ state: 'unavailable', code: 'kind_sync_timed_out' })
    expect(relatedReadFrom({ error: fail(403, 'rbac_denied') })).toMatchObject({ code: 'user_forbidden' })
  })

  it('stays loading while the kind or connection is warming up', () => {
    expect(relatedReadFrom({ error: fail(503, 'kind_sync_pending') })).toEqual({ state: 'loading' })
    expect(relatedReadFrom({ error: fail(503, 'cluster_connecting') })).toEqual({ state: 'loading' })
    expect(relatedReadFrom({ error: null })).toEqual({ state: 'loading' })
  })

  it('lets a failed refresh outrank data kept from an earlier read', () => {
    const object = { metadata: { name: 'cq' } }
    expect(relatedReadFrom({ data: { resource: object }, error: null })).toEqual({ state: 'found', object })
    expect(relatedReadFrom({ data: { resource: object }, error: fail(403, 'user_forbidden') })).toMatchObject({ state: 'unavailable' })
    expect(relatedReadFrom({ data: { resource: object }, error: fail(404, 'object_not_found') })).toEqual({ state: 'not_found' })
  })
})

describe('relatedReadInList', () => {
  const items = [{ metadata: { name: 'small' } }]
  it('finds a name, and calls it missing only from a complete list', () => {
    expect(relatedReadInList({ data: items, coverage: { status: 'complete', scope: { type: 'cluster' } }, error: null }, 'small')).toEqual({ state: 'found', object: items[0] })
    expect(relatedReadInList({ data: items, coverage: { status: 'complete', scope: { type: 'cluster' } }, error: null }, 'gone')).toEqual({ state: 'not_found' })
    expect(relatedReadInList({ data: items, coverage: { status: 'partial', gaps: [{ code: 'radar_namespace_not_collected' }] }, error: null }, 'gone')).toMatchObject({ state: 'unavailable', code: 'radar_namespace_not_collected' })
    // An older Radar sends a bare array, read as unknown coverage.
    expect(relatedReadInList({ data: items, coverage: { status: 'unknown' }, error: null }, 'gone')).toEqual({ state: 'unconfirmed' })
    expect(relatedReadInList({ data: items, coverage: { status: 'unknown' }, error: null }, 'small').state).toBe('found')
  })
  it('proves absence only within a scope it understands and that holds the asked namespace', () => {
    expect(relatedReadInList({ data: items, coverage: { status: 'complete', scope: { type: 'namespaces', namespaces: { names: ['team'] } } }, error: null, namespace: 'team' }, 'gone')).toEqual({ state: 'not_found' })
    expect(relatedReadInList({ data: items, coverage: { status: 'complete', scope: { type: 'namespaces', namespaces: { names: [] } } }, error: null, namespace: 'team' }, 'gone')).toEqual({ state: 'unconfirmed' })
    expect(relatedReadInList({ data: items, coverage: { status: 'complete', scope: { type: 'galaxy' } }, error: null }, 'gone')).toEqual({ state: 'unconfirmed' })
  })

  it('doesn’t prove absence from a list cached before the view mounted', () => {
    expect(relatedReadInList({ data: items, coverage: { status: 'complete', scope: { type: 'cluster' } }, error: null, readBeforeMount: true }, 'gone')).toEqual({ state: 'loading' })
    expect(relatedReadInList({ data: items, coverage: { status: 'complete', scope: { type: 'cluster' } }, error: null, readBeforeMount: true }, 'small').state).toBe('found')
  })

  it('lets a list error outrank the items it kept', () => {
    expect(relatedReadInList({ data: items, coverage: { status: 'complete', scope: { type: 'cluster' } }, error: fail(403, 'user_forbidden') }, 'small')).toMatchObject({ state: 'unavailable', code: 'user_forbidden' })
    expect(relatedReadInList({ error: null }, 'small')).toEqual({ state: 'loading' })
  })
})
