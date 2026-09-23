import { afterEach, describe, expect, it, vi } from 'vitest'
import { useQuery } from '@tanstack/react-query'
import { useResourceAudit, useResourceEvents } from './client'

vi.mock('@tanstack/react-query', async (original) => ({
  ...await original<typeof import('@tanstack/react-query')>(),
  useQuery: vi.fn(() => ({})),
}))

afterEach(() => { vi.clearAllMocks(); vi.unstubAllGlobals() })

describe('resource history identity', () => {
  it('separates same-name core and Volcano history in requests and cache keys', async () => {
    const urls: URL[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      urls.push(new URL(input, 'http://localhost'))
      return new Response('[]', {status: 200})
    }))
    useResourceEvents('jobs', 'ml', 'shared', 'batch')
    useResourceEvents('jobs', 'ml', 'shared', 'batch.volcano.sh')
    const queries = vi.mocked(useQuery).mock.calls.map(call => call[0])
    expect(queries[0].queryKey).not.toEqual(queries[2].queryKey)
    expect(queries[1].queryKey).not.toEqual(queries[3].queryKey)
    for (const query of queries) await (query.queryFn as () => Promise<unknown>)()
    expect(urls.map(url => url.searchParams.get('group'))).toEqual(['batch', 'batch', 'batch.volcano.sh', 'batch.volcano.sh'])
    expect(urls.every(url => url.searchParams.get('kind') === 'Job')).toBe(true)
  })

  it('does not query an unresolved custom identity', () => {
    useResourceEvents('Widget', 'ml', 'shared')
    expect(vi.mocked(useQuery).mock.calls.every(call => call[0].enabled === false)).toBe(true)
  })

  it('preserves a core group and an exact audit group', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('[]', {status: 200}))
    vi.stubGlobal('fetch', fetchMock)
    useResourceEvents('pods', 'ml', 'shared', '')
    await (vi.mocked(useQuery).mock.calls[0][0].queryFn as () => Promise<unknown>)()
    expect(new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost').searchParams.get('group')).toBe('')
    useResourceAudit('IngressRoute', 'ml', 'shared', 'traefik.io')
    await (vi.mocked(useQuery).mock.calls.at(-1)![0].queryFn as () => Promise<unknown>)()
    expect(fetchMock).toHaveBeenLastCalledWith('/api/audit/resource/IngressRoute/ml/shared?group=traefik.io', expect.anything())
  })
})
