import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

describe('preference persistence by installation', () => {
  beforeEach(() => { vi.resetModules() })
  afterEach(() => { vi.unstubAllGlobals() })

  it('keeps shared OSS theme and pins in the browser without writing the server', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ preferenceStorage: 'browser' })))
    vi.stubGlobal('fetch', fetchMock)
    const { persistPreferences } = await import('./preferences')
    await persistPreferences({ theme: 'light' })
    await persistPreferences({ pinnedKinds: [{ name: 'pods', kind: 'Pod', group: '' }] })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/settings')
    expect(fetchMock.mock.calls[0][1].method).toBeUndefined()
  })

  it('preserves local and Cloud persistence through configured API helpers', async () => {
    const fetchMock = vi.fn().mockImplementation(async () => new Response('{}'))
    vi.stubGlobal('fetch', fetchMock)
    const config = await import('./config')
    config.setApiBase('/c/example/api')
    config.setAuthHeadersProvider(() => ({ Authorization: 'test-header' }))
    config.setCredentialsMode('include')
    const { persistPreferences } = await import('./preferences')
    await persistPreferences({ theme: 'light' })
    expect(fetchMock).toHaveBeenLastCalledWith('/c/example/api/settings', expect.objectContaining({
      method: 'PUT', credentials: 'include',
      headers: { 'Content-Type': 'application/json', Authorization: 'test-header' },
      body: JSON.stringify({ theme: 'light' }),
    }))
  })

  it('never assumes permission to write when the settings read fails', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('denied', { status: 403 }))
    vi.stubGlobal('fetch', fetchMock)
    const { persistPreferences } = await import('./preferences')
    await expect(persistPreferences({ theme: 'light' })).rejects.toThrow('403')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('does not send a pending change to another instance after navigation', async () => {
    let finish!: (response: Response) => void
    const fetchMock = vi.fn().mockReturnValue(new Promise<Response>((resolve) => { finish = resolve }))
    vi.stubGlobal('fetch', fetchMock)
    const config = await import('./config')
    const { persistPreferences } = await import('./preferences')
    const saving = persistPreferences({ theme: 'light' })
    config.setApiBase('/c/other/api')
    finish(new Response('{}'))
    await saving
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
