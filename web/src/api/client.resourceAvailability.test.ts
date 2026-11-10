import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, fetchResourceAvailability } from './client'

afterEach(() => vi.unstubAllGlobals())

describe('resource availability checks', () => {
  it.each([200, 404])('uses a body-free HEAD lookup for resource status %s', async status => {
    const fetch = vi.fn<typeof globalThis.fetch>(async () => new Response(null, { status, headers: { 'X-Radar-Resource-Read': 'true' } }))
    vi.stubGlobal('fetch', fetch)
    expect(await fetchResourceAvailability('secrets', 'apps', 'credentials')).toBe(status === 200 ? 'present' : 'missing')
    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0]?.[1]).toMatchObject({ method: 'HEAD' })
  })

  it.each([403, 404, 503])('does not classify an upstream HTTP %s as a missing resource', async status => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status })))
    await expect(fetchResourceAvailability('configmaps', 'apps', 'overrides')).rejects.toBeInstanceOf(ApiError)
  })

  it.each([200, 405])('keeps older Radar navigation without claiming availability for HTTP %s', async status => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status })))
    expect(await fetchResourceAvailability('secrets', 'apps', 'credentials')).toBe('unchecked')
  })
})
