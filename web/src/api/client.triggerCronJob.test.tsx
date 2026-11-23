// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { useTriggerCronJob } from './client'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.restoreAllMocks()
})

async function trigger(params: { namespace: string; name: string; jobName?: string }) {
  const posts: Array<{ url: string; body: unknown; contentType: string | null }> = []
  vi.stubGlobal('fetch', vi.fn<typeof fetch>(async (input, init) => {
    if (init?.method === 'POST') {
      posts.push({ url: String(input), body: init.body, contentType: new Headers(init.headers).get('Content-Type') })
    }
    return new Response('{"jobName":"x"}', { status: 200 })
  }))
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  let mutate: (() => Promise<unknown>) | undefined
  function Probe() {
    const m = useTriggerCronJob()
    mutate = () => m.mutateAsync(params)
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  await act(async () => { await mutate!() })
  act(() => root.unmount())
  return posts
}

it('sends the Job name as a JSON body', async () => {
  const posts = await trigger({ namespace: 'prod', name: 'report', jobName: 'report-rerun' })
  expect(posts).toHaveLength(1)
  expect(posts[0].url).toContain('/cronjobs/prod/report/trigger')
  expect(JSON.parse(String(posts[0].body))).toEqual({ jobName: 'report-rerun' })
  expect(posts[0].contentType).toBe('application/json')
})

it('sends no body without a Job name, as an older Radar expects', async () => {
  const posts = await trigger({ namespace: 'prod', name: 'report' })
  expect(posts).toHaveLength(1)
  expect(posts[0].body).toBeUndefined()
})
