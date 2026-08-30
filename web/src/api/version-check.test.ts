import { afterEach, describe, expect, it, vi } from 'vitest'
import { reportBrowserUpdateCheck, sendBrowserUpdateCheck } from './client'

function dependencies(reportDay: string | null = null) {
  const claimBrowserCheck = vi.fn(() => reportDay)
  const sendBrowserCheck = vi.fn(async () => {})
  return {
    claimBrowserCheck,
    sendBrowserCheck,
    value: { claimBrowserCheck, sendBrowserCheck },
  }
}

describe('reportBrowserUpdateCheck', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('does not add a browser check for local mode', () => {
    const deps = dependencies('2026-08-29')
    reportBrowserUpdateCheck('local', deps.value)
    expect(deps.claimBrowserCheck).not.toHaveBeenCalled()
    expect(deps.sendBrowserCheck).not.toHaveBeenCalled()
  })

  it('adds one best-effort in-cluster browser check', () => {
    const deps = dependencies('2026-08-29')
    reportBrowserUpdateCheck('in-cluster', deps.value)
    expect(deps.sendBrowserCheck).toHaveBeenCalledWith('2026-08-29')
  })

  it('does not repeat a browser check already claimed for the day', () => {
    const deps = dependencies()
    reportBrowserUpdateCheck('in-cluster', deps.value)
    expect(deps.sendBrowserCheck).not.toHaveBeenCalled()
  })

  it('swallows a browser-check failure', async () => {
    const deps = dependencies('2026-08-29')
    deps.sendBrowserCheck.mockRejectedValue(new Error('offline'))
    reportBrowserUpdateCheck('in-cluster', deps.value)
    await Promise.resolve()
  })

  it('does not emit the standalone browser check in Cloud mode', () => {
    const deps = dependencies('2026-08-29')
    reportBrowserUpdateCheck('cloud', deps.value)
    expect(deps.claimBrowserCheck).not.toHaveBeenCalled()
    expect(deps.sendBrowserCheck).not.toHaveBeenCalled()
  })

  it('sends the claimed day to the same-origin backend endpoint', async () => {
    const fetch = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetch)

    await sendBrowserUpdateCheck('2026-08-29')

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch).toHaveBeenCalledWith('/api/version-check/browser', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
      keepalive: true,
      body: JSON.stringify({ reportDay: '2026-08-29' }),
    }))
  })
})
