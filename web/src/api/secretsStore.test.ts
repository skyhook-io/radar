import { describe, expect, it } from 'vitest'
import { ApiError } from './client'
import { toSecretsStoreRead } from './secretsStore'

const complete = { status: 'complete' as const, scope: { type: 'namespaces' as const }, gaps: [] }
const partial = (code: string) => ({ status: 'partial' as const, scope: { type: 'namespaces' as const }, gaps: [{ code, action: 'retry' }] })

describe('toSecretsStoreRead', () => {
  it('trusts absence only from a complete answer', () => {
    expect(toSecretsStoreRead({ data: [], coverage: complete, error: null, isLoading: false })).toEqual({ state: 'ok', items: [], complete: true })
    expect(toSecretsStoreRead({ data: [{}], coverage: partial('radar_forbidden') as any, error: null, isLoading: false })).toEqual({ state: 'ok', items: [{}], complete: false })
  })

  it('does not trust absence from a list without coverage', () => {
    expect(toSecretsStoreRead({ data: [], coverage: { status: 'unknown' }, error: null, isLoading: false })).toEqual({ state: 'ok', items: [], complete: false })
    expect(toSecretsStoreRead({ data: [], error: null, isLoading: false })).toEqual({ state: 'ok', items: [], complete: false })
  })

  it('keeps a denial apart from an empty list', () => {
    expect(toSecretsStoreRead({ error: new ApiError('forbidden', 403), isLoading: false })).toEqual({ state: 'forbidden' })
    expect(toSecretsStoreRead({ data: [], coverage: partial('user_forbidden') as any, error: null, isLoading: false })).toEqual({ state: 'forbidden' })
  })

  it('reports an unserved API as unavailable, not missing', () => {
    const notServed = new ApiError('unknown resource kind: secretproviderclasses', 400, { error_code: 'kind_unknown' } as any)
    expect(toSecretsStoreRead({ error: notServed, isLoading: false }).state).toBe('unavailable')
  })

  it('stays loading while the kind syncs', () => {
    expect(toSecretsStoreRead({ isLoading: true, error: null })).toEqual({ state: 'loading' })
    expect(toSecretsStoreRead({ data: [], coverage: partial('kind_sync_pending') as any, error: null, isLoading: false })).toEqual({ state: 'loading' })
  })
})
