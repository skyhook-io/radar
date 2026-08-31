import { afterEach, describe, expect, it, vi } from 'vitest'
import { reportBrowserUpdateCheck, sendBrowserUpdateCheck } from './client'

function dependencies(reportDay: string | null = null) {
  const markBrowserUpdateCheckAttempt = vi.fn<(installScope?: string) => string | null>(() => reportDay)
  const sendBrowserCheck = vi.fn(async () => {})
  return {
    markBrowserUpdateCheckAttempt,
    sendBrowserCheck,
    value: { markBrowserUpdateCheckAttempt, sendBrowserCheck },
  }
}

describe('reportBrowserUpdateCheck', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('does not add a browser check for local mode', () => {
    const deps = dependencies('2026-08-29')
    reportBrowserUpdateCheck('local', undefined, deps.value)
    expect(deps.markBrowserUpdateCheckAttempt).not.toHaveBeenCalled()
    expect(deps.sendBrowserCheck).not.toHaveBeenCalled()
  })

  it('adds one best-effort in-cluster browser check', () => {
    const deps = dependencies('2026-08-29')
    reportBrowserUpdateCheck('in-cluster', '1700000000', deps.value)
    expect(deps.markBrowserUpdateCheckAttempt).toHaveBeenCalledWith('1700000000')
    expect(deps.sendBrowserCheck).toHaveBeenCalledOnce()
  })

  it('does not repeat a browser check already claimed for the day', () => {
    const deps = dependencies()
    reportBrowserUpdateCheck('in-cluster', undefined, deps.value)
    expect(deps.sendBrowserCheck).not.toHaveBeenCalled()
  })

  it('swallows a browser-check failure', async () => {
    const deps = dependencies('2026-08-29')
    deps.sendBrowserCheck.mockRejectedValue(new Error('offline'))
    reportBrowserUpdateCheck('in-cluster', undefined, deps.value)
    await Promise.resolve()
  })

  it('does not emit the standalone browser check in Cloud mode', () => {
    const deps = dependencies('2026-08-29')
    reportBrowserUpdateCheck('cloud', undefined, deps.value)
    expect(deps.markBrowserUpdateCheckAttempt).not.toHaveBeenCalled()
    expect(deps.sendBrowserCheck).not.toHaveBeenCalled()
  })

  it('sends an identity-free attempt to the same-origin backend endpoint', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetch)

    await sendBrowserUpdateCheck()

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch).toHaveBeenCalledWith('/api/version-check/browser', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
      keepalive: true,
    }))
    expect(fetch.mock.calls[0][1]).not.toHaveProperty('body')
  })

  it('surfaces backend rejection without retrying', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(async () => new Response(null, { status: 429 }))
    vi.stubGlobal('fetch', fetch)

    await expect(sendBrowserUpdateCheck()).rejects.toThrow('Browser update check failed: 429')
    expect(fetch).toHaveBeenCalledOnce()
  })
})
