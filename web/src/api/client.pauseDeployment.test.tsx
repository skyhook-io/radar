// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { usePauseDeployment, useRadarFeature, useResumeDeployment } from './client'
import { isRadarFeatureUnsupported } from './radarFeatures'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
beforeEach(() => {
  vi.spyOn(console, 'log').mockImplementation(() => {})
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.restoreAllMocks()
})

const target = { kind: 'deployments', namespace: 'prod', name: 'web' }

async function run(action: 'pause' | 'resume', features: Record<string, boolean>, response = new Response('{"message":"ok"}', { status: 200 })) {
  const posts: string[] = []
  vi.stubGlobal('fetch', vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input)
    if (init?.method === 'POST') {
      posts.push(url)
      return response
    }
    if (url.includes('/capabilities')) return new Response(JSON.stringify({ features }), { status: 200 })
    return new Response('{}', { status: 200 })
  }))

  const client = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } })
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  let mutate: (() => Promise<unknown>) | undefined
  let support = ''
  function Probe() {
    const pause = usePauseDeployment()
    const resume = useResumeDeployment()
    support = useRadarFeature('deploymentPause').support
    mutate = () => (action === 'pause' ? pause : resume).mutateAsync(target)
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  await vi.waitFor(() => expect(support).not.toBe('unknown'))

  let failure: unknown
  await act(async () => { try { await mutate!() } catch (err) { failure = err } })
  const invalidated = invalidate.mock.calls.map(([filters]) => JSON.stringify(filters?.queryKey))
  act(() => root.unmount())
  client.clear()
  return { posts, failure, invalidated }
}

it.each(['pause', 'resume'] as const)('%s posts to its route and refreshes the open Deployment', async (action) => {
  const { posts, failure, invalidated } = await run(action, { deploymentPause: true })

  expect(failure).toBeUndefined()
  expect(posts).toHaveLength(1)
  expect(posts[0]).toContain(`/api/workloads/deployments/prod/web/${action}`)
  expect(invalidated).toContain(JSON.stringify(['resource', 'deployments', 'prod', 'web']))
  expect(invalidated).toContain(JSON.stringify(['resources', 'deployments']))
})

it('surfaces the server error', async () => {
  const { failure } = await run('pause', { deploymentPause: true }, new Response('{"error":"nope"}', { status: 403 }))

  expect((failure as Error).message).toBe('nope')
})

it('sends nothing to a Radar that predates the routes', async () => {
  const { posts, failure } = await run('pause', { workloadImages: true })

  expect(posts).toHaveLength(0)
  expect(isRadarFeatureUnsupported(failure, 'deploymentPause')).toBe(true)
})
