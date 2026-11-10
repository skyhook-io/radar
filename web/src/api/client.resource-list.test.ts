import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, fetchResourceList, isListTooLargeError, isUnretryableReadError } from './client'
import { setApiBase, setAuthHeadersProvider, setCredentialsMode } from './config'

afterEach(() => {
  vi.unstubAllGlobals()
  setApiBase('/api')
  setAuthHeadersProvider(() => ({}))
  setCredentialsMode('same-origin')
})

const coverage = { status: 'complete', scope: { type: 'all_namespaces' }, read: { type: 'all_namespaces' }, gaps: [] }
const envelope = { items: [], kind: 'pods', coverage, columns: null, cells: null }

describe('resource list transport', () => {
  it('uses the configured cluster route and auth with a bounded list', async () => {
    setApiBase('/c/cluster/api')
    setAuthHeadersProvider(() => ({ Authorization: 'Bearer test' }))
    setCredentialsMode('include')
    const mock = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      expect(url).toBe('/c/cluster/api/resources/pods?namespaces=alpha&maxItems=25000')
      expect(init?.credentials).toBe('include')
      expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer test')
      return new Response(JSON.stringify(envelope), { status: 200 })
    })
    vi.stubGlobal('fetch', mock)
    expect((await fetchResourceList('pods', { namespaces: ['alpha'] })).coverage).toEqual(coverage)
    expect(mock).toHaveBeenCalledTimes(1)
  })
  it('uses the summary budget only for summary requests', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: RequestInfo | URL) => {
      expect(url).toBe('/api/resources/pods?include=summary&maxItems=50000')
      return new Response(JSON.stringify(envelope), { status: 200 })
    }))
    await fetchResourceList('pods', { summary: true })
  })
  it('reads an older Radar array, sent the same budget it ignores, without inventing complete coverage', async () => {
    const mock = vi.fn(async (url: RequestInfo | URL) => {
      expect(url).toBe('/api/resources/pods?maxItems=25000')
      return new Response('[{"metadata":{"name":"api"}}]', { status: 200 })
    })
    vi.stubGlobal('fetch', mock)
    const result = await fetchResourceList('pods')
    expect(result.items).toEqual([{ metadata: { name: 'api' } }])
    expect(result.coverage).toEqual({ status: 'unknown' })
    expect(mock).toHaveBeenCalledTimes(1)
  })
  it('preserves oversized-list errors instead of returning empty rows', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'Too many resources to load; select fewer namespaces', error_code: 'list_too_large', count: 30000, limit: 25000 }), { status: 400, headers: { 'Content-Type': 'application/json' } })))
    const error = await fetchResourceList('pods').catch((e: unknown) => e)
    expect(error).toMatchObject({ status: 400, data: { error_code: 'list_too_large', count: 30000 } })
    expect(isListTooLargeError(error)).toBe(true)
    expect(isListTooLargeError(new ApiError('failed', 400, { error: 'failed', error_code: 'kind_unknown' }))).toBe(false)
  })
})

describe('read errors that a retry cannot answer', () => {
  const failure = (status: number, error_code?: string) => new ApiError('failed', status, error_code ? { error: 'failed', error_code } : { error: 'failed' })

  it('stops on a request, access or collection problem', () => {
    expect(isUnretryableReadError(failure(400, 'list_too_large'))).toBe(true)
    expect(isUnretryableReadError(failure(400, 'kind_unknown'))).toBe(true)
    expect(isUnretryableReadError(failure(409, 'radar_namespace_not_collected'))).toBe(true)
    expect(isUnretryableReadError(failure(403, 'user_forbidden'))).toBe(true)
    expect(isUnretryableReadError(failure(503, 'kind_sync_timed_out'))).toBe(true)
    // The spellings Radar releases before the shared vocabulary sent.
    expect(isUnretryableReadError(failure(403, 'rbac_denied'))).toBe(true)
    expect(isUnretryableReadError(failure(503, 'kind_sync_failed'))).toBe(true)
  })
  it('retries what may answer on its own', () => {
    expect(isUnretryableReadError(failure(503, 'kind_sync_pending'))).toBe(false)
    expect(isUnretryableReadError(failure(500, 'read_error'))).toBe(false)
    expect(isUnretryableReadError(failure(503))).toBe(false)
    expect(isUnretryableReadError(new Error('network'))).toBe(false)
  })
})
