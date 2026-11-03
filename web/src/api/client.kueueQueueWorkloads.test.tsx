// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { useKueueQueueWorkloads } from './client'
import { setApiBase } from './config'

afterEach(() => {
  setApiBase('/api')
  vi.unstubAllGlobals()
})

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
const page = (names: string[], next?: string) => ({
  queue: { kind: 'ClusterQueue', name: 'cq', uid: 'cq-uid' },
  workloads: {
    items: names.map(name => ({ namespace: 'team-a', name, uid: name, apiVersion: 'kueue.x-k8s.io/v1beta2', createdAt: null, deleting: false, membership: ['reservation'] })),
    matching: 3, counts: { members: 3, submission: 0, reservation: 3 }, next,
    coverage: { status: 'complete', scope: { type: 'all_namespaces' }, read: { type: 'all_namespaces' }, gaps: [] },
  },
})

it.each(['supported', 'older'] as const)('reads queue Workloads page by page only from a Radar that serves them: %s', async (radar) => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  setApiBase('/c/review/api')
  const requests: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'http://localhost')
    requests.push(url.pathname + url.search)
    if (url.pathname.endsWith('/capabilities')) return json({ features: radar === 'supported' ? { kueueQueueWorkloads: true } : {} })
    if (url.pathname.endsWith('/version')) return json({ currentVersion: 'v1.20.0' })
    if (radar === 'older') return new Response('404 page not found\n', { status: 404, headers: { 'Content-Type': 'text/plain' } })
    return json(url.searchParams.get('cursor') ? page(['c']) : page(['a', 'b'], 'next-1'))
  }))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  let result!: ReturnType<typeof useKueueQueueWorkloads>
  function Probe() {
    result = useKueueQueueWorkloads({ kind: 'ClusterQueue', name: 'cq', uid: 'cq-uid' }, 'reservation')
    return null
  }
  const host = document.createElement('div')
  const root = createRoot(host)
  const endpoint = (path: string) => path.startsWith('/c/review/api/kueue/')
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
    if (radar === 'older') {
      // Before capabilities answer, one probe may reach the older Radar; once
      // they do, it is never asked again and the upgrade note replaces the list.
      await vi.waitFor(() => expect(result.upgrade).not.toBeNull())
      const asked = requests.filter(endpoint).length
      expect(asked).toBeLessThanOrEqual(1)
      await act(async () => { await result.refetch() })
      expect(requests.filter(endpoint).length).toBe(asked)
      expect(result.data).toBeUndefined()
      return
    }
    await vi.waitFor(() => expect(result.data?.pages).toHaveLength(1))
    expect(result.hasNextPage).toBe(true)
    await act(async () => { await result.fetchNextPage() })
    await vi.waitFor(() => expect(result.data?.pages).toHaveLength(2))
    expect(result.hasNextPage).toBe(false)
    expect(requests.filter(endpoint)).toEqual([
      '/c/review/api/kueue/clusterqueues/cq/workloads?membership=reservation',
      '/c/review/api/kueue/clusterqueues/cq/workloads?membership=reservation&cursor=next-1',
    ])
  } finally {
    act(() => root.unmount())
    client.clear()
  }
})
