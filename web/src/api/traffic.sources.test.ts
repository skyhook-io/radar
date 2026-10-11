import { afterEach, describe, expect, it, vi } from 'vitest'
import { QueryClient } from '@tanstack/react-query'
import { detectTrafficSourcesAgain, fetchTrafficSources } from './traffic'

const found = { detected: [{ name: 'hubble', status: 'available' }], notDetected: [] }
const none = { detected: [], notDetected: [{ name: 'hubble', status: 'not_found' }] }

function serve(responses: Record<string, unknown>, delays: Record<string, number> = {}) {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.slice(url.indexOf('/traffic/'))
    calls.push(path)
    if (delays[path]) {
      await new Promise((resolve, reject) => {
        const timer = setTimeout(resolve, delays[path])
        init?.signal?.addEventListener('abort', () => { clearTimeout(timer); reject(new DOMException('aborted', 'AbortError')) })
      })
    }
    return new Response(JSON.stringify(responses[path]), { status: 200 })
  }))
  return calls
}

afterEach(() => vi.unstubAllGlobals())

describe('fetchTrafficSources', () => {
  it('reuses a recent detection that found a source', async () => {
    const calls = serve({ '/traffic/sources?recent=1': found })
    const sources = await fetchTrafficSources(new QueryClient(), false)
    expect(sources).toEqual(found)
    expect(calls).toEqual(['/traffic/sources?recent=1'])
  })

  it('detects afresh before showing a recent detection that found none', async () => {
    const calls = serve({ '/traffic/sources?recent=1': none, '/traffic/sources': found })
    const sources = await fetchTrafficSources(new QueryClient(), false)
    expect(sources).toEqual(found)
    expect(calls).toEqual(['/traffic/sources?recent=1', '/traffic/sources'])
  })
})

describe('detectTrafficSourcesAgain', () => {
  it('detects afresh even while a recent detection is being fetched', async () => {
    const calls = serve({ '/traffic/sources?recent=1': found, '/traffic/sources': none }, { '/traffic/sources?recent=1': 50 })
    const client = new QueryClient()
    void client.fetchQuery({ queryKey: ['traffic-sources'], queryFn: ({ signal }) => fetchTrafficSources(client, false, signal) }).catch(() => {})
    const sources = await detectTrafficSourcesAgain(client)
    expect(sources).toEqual(none)
    expect(calls).toContain('/traffic/sources')
    // The abandoned recent detection must not land afterwards.
    await new Promise(resolve => setTimeout(resolve, 80))
    expect(client.getQueryData(['traffic-sources'])).toEqual(none)
    expect(client.getQueryData(['traffic-sources', 'recent'])).toEqual(none)
  })
})
