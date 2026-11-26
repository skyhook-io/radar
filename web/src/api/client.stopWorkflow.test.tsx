// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isWorkflowStopTargetGone, useStopWorkflow, workflowStopErrorMessage, workflowStopOutcome, type WorkflowStopStrategy } from './client'
import { showApiNotice, showApiSuccess } from '../components/ui/Toast'

vi.mock('../components/ui/Toast', () => ({
  showApiError: vi.fn(),
  showApiSuccess: vi.fn(),
  showApiNotice: vi.fn(),
}))

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
beforeEach(() => {
  vi.mocked(showApiNotice).mockClear()
  vi.mocked(showApiSuccess).mockClear()
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

async function stop(response: { status: number; body: unknown }, strategy: WorkflowStopStrategy = 'Stop') {
  const posts: Array<{ url: string; body: unknown; contentType: string | null }> = []
  vi.stubGlobal('fetch', vi.fn<typeof fetch>(async (input, init) => {
    posts.push({ url: String(input), body: init?.body, contentType: new Headers(init?.headers).get('Content-Type') })
    return new Response(JSON.stringify(response.body), { status: response.status, headers: { 'Content-Type': 'application/json' } })
  }))
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  let mutate: (() => Promise<unknown>) | undefined
  function Probe() {
    const m = useStopWorkflow()
    mutate = () => m.mutateAsync({ namespace: 'ci', name: 'build-7', uid: 'uid-1', strategy })
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  let result: unknown
  let error: unknown
  await act(async () => { await mutate!().then(r => { result = r }, e => { error = e }) })
  act(() => root.unmount())
  return { posts, result, error, invalidated: invalidate.mock.calls.map(c => c[0]?.queryKey) }
}

describe('useStopWorkflow', () => {
  it('sends the reviewed UID and strategy, and no resourceVersion', async () => {
    const { posts } = await stop({ status: 200, body: { shutdown: 'Terminate', changed: true } }, 'Terminate')
    expect(posts).toHaveLength(1)
    expect(posts[0].url).toMatch(/\/workflows\/ci\/build-7\/stop$/)
    expect(posts[0].contentType).toBe('application/json')
    expect(JSON.parse(String(posts[0].body))).toEqual({ uid: 'uid-1', strategy: 'Terminate' })
  })

  it('reports a stored Stop as requested and refreshes the Workflow', async () => {
    const { result, invalidated } = await stop({ status: 200, body: { shutdown: 'Stop', changed: true } })
    expect(result).toEqual({ state: 'requested', shutdown: 'Stop' })
    expect(showApiSuccess).toHaveBeenCalledWith('Stop requested', expect.stringContaining('exit handlers'))
    expect(invalidated).toContainEqual(['resource', 'workflows', 'ci', 'build-7'])
  })

  it('says a run is already stopping when nothing changed', async () => {
    const { result } = await stop({ status: 200, body: { shutdown: 'Stop', changed: false } })
    expect(result).toEqual({ state: 'already', shutdown: 'Stop' })
    expect(showApiNotice).toHaveBeenCalledWith('Workflow is already stopping', undefined, 'info')
    expect(showApiSuccess).not.toHaveBeenCalled()
  })

  it('says a run is already terminating when a stored Terminate satisfied a Stop', async () => {
    await stop({ status: 200, body: { shutdown: 'Terminate', changed: false } })
    expect(showApiNotice).toHaveBeenCalledWith('Workflow is already terminating', undefined, 'info')
  })

  it('treats a finished run as an outcome, not a failure, and still refreshes', async () => {
    const { result, error, invalidated } = await stop({ status: 409, body: { error: 'the workflow has already finished', error_code: 'workflow_finished' } })
    expect(error).toBeUndefined()
    expect(result).toEqual({ state: 'finished' })
    expect(showApiNotice).toHaveBeenCalledWith('Workflow already finished', 'Nothing was stopped.', 'info')
    expect(invalidated).toContainEqual(['resource', 'workflows', 'ci', 'build-7'])
  })

  it('fails a replaced run with its own message and still refreshes', async () => {
    const { error, invalidated } = await stop({ status: 409, body: { error: 'a different workflow now has this name', error_code: 'workflow_replaced' } })
    expect((error as Error).message).toContain('A different Workflow now has this name')
    expect(invalidated).toContainEqual(['resource', 'workflows', 'ci', 'build-7'])
  })

  it('passes other refusals through unchanged', async () => {
    const { error } = await stop({ status: 403, body: { error: 'failed to stop workflow: forbidden' } })
    expect((error as Error).message).toBe('failed to stop workflow: forbidden')
    expect(isWorkflowStopTargetGone(error)).toBe(false)
  })

  it('marks a replaced or missing run as gone, so the dialog has nothing to retry', async () => {
    const replaced = await stop({ status: 409, body: { error: 'x', error_code: 'workflow_replaced' } })
    expect(isWorkflowStopTargetGone(replaced.error)).toBe(true)
    const missing = await stop({ status: 404, body: { error: 'workflow not found' } })
    expect(isWorkflowStopTargetGone(missing.error)).toBe(true)
  })

  it('warns, not "already stopping", when admission dropped the requested Stop', async () => {
    // The server compares with the value before the patch: empty then, empty now.
    const { result } = await stop({ status: 200, body: { shutdown: '', changed: false } })
    expect(result).toEqual({ state: 'notStored', shutdown: '' })
    expect(showApiNotice).toHaveBeenCalledWith('Stop not stored', expect.stringContaining('keeps running'), 'warning')
    expect(showApiNotice).not.toHaveBeenCalledWith('Workflow is already stopping', undefined, 'info')
  })

  it('warns when admission rewrote a Terminate to Stop', async () => {
    const { result } = await stop({ status: 200, body: { shutdown: 'Stop', changed: true } }, 'Terminate')
    expect(result).toEqual({ state: 'notStored', shutdown: 'Stop' })
  })
})

describe('workflowStopOutcome', () => {
  it('reads an existing Terminate as satisfying a Stop', () => {
    expect(workflowStopOutcome('Stop', { shutdown: 'Terminate', changed: false })).toEqual({ state: 'already', shutdown: 'Terminate' })
  })
  it('flags a Stop that admission stored as Terminate', () => {
    expect(workflowStopOutcome('Stop', { shutdown: 'Terminate', changed: true })).toEqual({ state: 'notStored', shutdown: 'Terminate' })
  })
  it('reads an escalation from Stop as requested', () => {
    expect(workflowStopOutcome('Terminate', { shutdown: 'Terminate', changed: true })).toEqual({ state: 'requested', shutdown: 'Terminate' })
  })
})

describe('workflowStopErrorMessage', () => {
  it('falls back to the status without a server message', () => {
    expect(workflowStopErrorMessage({}, 502)).toBe('HTTP 502')
  })
})
