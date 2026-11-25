// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useRadarFeature, useSubmitWorkflowTemplate } from './client'
import { isRadarFeatureUnsupported } from './radarFeatures'
import { showApiSuccess } from '../components/ui/Toast'

vi.mock('../components/ui/Toast', () => ({
  showApiError: vi.fn(),
  showApiSuccess: vi.fn(),
  showApiNotice: vi.fn(),
}))

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
beforeEach(() => {
  vi.spyOn(console, 'log').mockImplementation(() => {})
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.restoreAllMocks()
  vi.mocked(showApiSuccess).mockClear()
})

const submission = {
  namespace: 'jobs',
  name: 'migrate',
  resourceVersion: '7',
  runName: 'migrate-k2x9q',
  submission: 'confirmation-0001',
  parameters: { target: 'orders', mode: 'apply' },
}

async function submit(features: Record<string, boolean>, response = new Response('{"namespace":"jobs","name":"migrate-k2x9q","uid":"run-1"}', { status: 200 })) {
  const posts: Array<{ url: string; body: unknown; contentType: string | null }> = []
  vi.stubGlobal('fetch', vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input)
    if (init?.method === 'POST') {
      posts.push({ url, body: JSON.parse(String(init.body)), contentType: new Headers(init.headers).get('Content-Type') })
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
    const m = useSubmitWorkflowTemplate()
    support = useRadarFeature('workflowTemplateSubmit').support
    mutate = () => m.mutateAsync(submission)
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  await vi.waitFor(() => expect(support).not.toBe('unknown'))
  let result: unknown
  let failure: unknown
  await act(async () => { try { result = await mutate!() } catch (err) { failure = err } })
  const invalidated = invalidate.mock.calls.map(([filters]) => JSON.stringify(filters?.queryKey))
  act(() => root.unmount())
  client.clear()
  return { posts, result, failure, invalidated }
}

it('posts the reviewed version, run name, submission and parameters, and refreshes the runs', async () => {
  const { posts, result, failure, invalidated } = await submit({ workflowTemplateSubmit: true })

  expect(failure).toBeUndefined()
  expect(result).toEqual({ namespace: 'jobs', name: 'migrate-k2x9q', uid: 'run-1' })
  expect(posts).toHaveLength(1)
  expect(posts[0].url).toMatch(/\/api\/workflowtemplates\/jobs\/migrate\/submit$/)
  expect(posts[0].contentType).toBe('application/json')
  expect(posts[0].body).toEqual({
    name: 'migrate-k2x9q',
    submission: 'confirmation-0001',
    resourceVersion: '7',
    parameters: { target: 'orders', mode: 'apply' },
  })
  expect(invalidated).toContain(JSON.stringify(['workload-runs', 'workflowtemplates', 'jobs', 'migrate']))
  expect(showApiSuccess).toHaveBeenCalledWith('Workflow submitted', 'jobs/migrate-k2x9q')
})

it('surfaces the server error', async () => {
  const { failure } = await submit({ workflowTemplateSubmit: true }, new Response('{"error":"parameter \\"target\\" is required","error_code":"invalid_parameters"}', { status: 400 }))

  expect((failure as Error).message).toBe('parameter "target" is required')
})

it('sends nothing to a Radar that predates the route', async () => {
  const { posts, failure } = await submit({ workflowStop: true })

  expect(posts).toHaveLength(0)
  expect(isRadarFeatureUnsupported(failure, 'workflowTemplateSubmit')).toBe(true)
})
