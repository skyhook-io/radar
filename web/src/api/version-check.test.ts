import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { markDailyUpdateCheckAttempt, reportBrowserUpdateCheck, utcDay } from './client'

function memoryStorage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
  }
}

describe('daily browser update checks', () => {
  it('uses UTC days', () => {
    expect(utcDay(new Date('2026-08-29T23:59:59-07:00'))).toBe('2026-08-30')
  })

  it('attempts once per API base and UTC day', () => {
    const storage = memoryStorage()
    expect(markDailyUpdateCheckAttempt(storage, '/api', new Date('2026-08-29T10:00:00Z'))).toBe(true)
    expect(markDailyUpdateCheckAttempt(storage, '/api', new Date('2026-08-29T20:00:00Z'))).toBe(false)
    expect(markDailyUpdateCheckAttempt(storage, '/api', new Date('2026-08-30T10:00:00Z'))).toBe(true)
    expect(markDailyUpdateCheckAttempt(storage, '/c/cluster-a/api', new Date('2026-08-30T10:00:00Z'))).toBe(true)
  })

  it('skips the attempt when storage is unavailable', () => {
    const storage = {
      getItem: () => null,
      setItem: () => { throw new Error('blocked') },
    }
    expect(markDailyUpdateCheckAttempt(storage, '/api', new Date('2026-08-29T10:00:00Z'))).toBe(false)
  })
})

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

  it('does not retry a failed daily attempt', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(async () => { throw new Error('offline') })
    vi.stubGlobal('fetch', fetch)

    await expect(reportBrowserUpdateCheck('in-cluster')).rejects.toThrow('offline')
    await reportBrowserUpdateCheck('in-cluster')
    expect(fetch).toHaveBeenCalledOnce()
  })
})
