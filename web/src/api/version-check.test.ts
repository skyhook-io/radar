import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reportBrowserUpdateCheck } from './client'

describe('reportBrowserUpdateCheck', () => {
  beforeEach(() => {
    const values = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it.each(['local', 'cloud', undefined] as const)('does not report in %s mode', async (mode) => {
    const fetch = vi.fn<typeof globalThis.fetch>(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetch)
    await reportBrowserUpdateCheck(mode)
    expect(fetch).not.toHaveBeenCalled()
  })

  it('sends one bodyless in-cluster attempt per day', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetch)

    await reportBrowserUpdateCheck('in-cluster')
    await reportBrowserUpdateCheck('in-cluster')

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch).toHaveBeenCalledWith('/api/version-check/browser', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
      keepalive: true,
    }))
    expect(fetch.mock.calls[0][1]).not.toHaveProperty('body')
  })

  it('does not retry a failed attempt', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(async () => { throw new Error('offline') })
    vi.stubGlobal('fetch', fetch)

    await expect(reportBrowserUpdateCheck('in-cluster')).rejects.toThrow('offline')
    expect(fetch).toHaveBeenCalledOnce()
  })
})
