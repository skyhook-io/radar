// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResourceActionsBar } from './ResourceActionsBar'
import { workflowPendingShutdown, workflowStopState } from './WorkflowStopDialog'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function workflow(overrides: { phase?: string; shutdown?: string; uid?: string; apiVersion?: string; deleting?: boolean; startedAt?: string } = {}) {
  return {
    apiVersion: overrides.apiVersion ?? 'argoproj.io/v1alpha1',
    kind: 'Workflow',
    metadata: { name: 'build-7', namespace: 'ci', uid: overrides.uid ?? 'uid-1', resourceVersion: '4242', ...(overrides.deleting ? { deletionTimestamp: '2026-11-25T00:00:00Z' } : {}) },
    spec: overrides.shutdown ? { shutdown: overrides.shutdown } : {},
    status: { phase: overrides.phase ?? 'Running', startedAt: overrides.startedAt ?? '2026-11-25T10:00:00Z' },
  }
}

const roots: Root[] = []
let root: Root | undefined
const resource = { kind: 'workflows', name: 'build-7', namespace: 'ci', group: 'argoproj.io' }

function render(props: Record<string, unknown>, data: unknown = workflow(), res: Record<string, unknown> = resource) {
  if (!root) {
    const host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    roots.push(root)
  }
  act(() => root!.render(<ResourceActionsBar resource={res as any} data={data} {...props} />))
}
// The last match: once the dialog is open its confirm button follows the bar's.
const buttons = (label: string) => [...document.querySelectorAll('button')].filter(b => b.textContent?.trim() === label)
const button = (label: string) => buttons(label).at(-1)!
const openDialog = () => document.querySelector('[role="dialog"]:not([inert] *)')
const radio = (value: string) => document.querySelector<HTMLInputElement>(`input[type="radio"][value="${value}"]`)!

afterEach(() => {
  act(() => roots.splice(0).forEach(r => r.unmount()))
  root = undefined
  document.body.innerHTML = ''
})

describe('workflowStopState', () => {
  it('offers Stop for a run that has not finished, including one not yet started', () => {
    expect(workflowStopState(workflow())).toBe('running')
    expect(workflowStopState(workflow({ phase: 'Pending' }))).toBe('running')
    expect(workflowStopState({ ...workflow(), status: {} })).toBe('running')
  })

  it('offers nothing for a finished, terminating or deleting run', () => {
    for (const phase of ['Succeeded', 'Failed', 'Error']) expect(workflowStopState(workflow({ phase }))).toBe('none')
    expect(workflowStopState(workflow({ shutdown: 'Terminate' }))).toBe('none')
    expect(workflowStopState(workflow({ deleting: true }))).toBe('none')
  })

  it('offers only Terminate once a graceful stop is under way', () => {
    expect(workflowStopState(workflow({ shutdown: 'Stop' }))).toBe('stopping')
  })

  it('ignores a same-named kind from another API group', () => {
    expect(workflowStopState(workflow({ apiVersion: 'example.com/v1' }))).toBe('none')
  })
})

describe('workflowPendingShutdown', () => {
  it('names a requested shutdown only while the run has not finished', () => {
    expect(workflowPendingShutdown(workflow())).toBeNull()
    expect(workflowPendingShutdown(workflow({ shutdown: 'Stop' }))).toBe('Stop')
    expect(workflowPendingShutdown(workflow({ shutdown: 'Terminate' }))).toBe('Terminate')
    expect(workflowPendingShutdown(workflow({ shutdown: 'Terminate', phase: 'Failed' }))).toBeNull()
  })
})

describe('Workflow Stop action', () => {
  it('is hidden without the host callback, so the capability and permission gates stay with the host', () => {
    render({})
    expect(buttons('Stop…')).toHaveLength(0)
  })

  it('is hidden on a finished Workflow', () => {
    render({ onStopWorkflow: vi.fn() }, workflow({ phase: 'Succeeded' }))
    expect(buttons('Stop…')).toHaveLength(0)
    expect(buttons('Terminate…')).toHaveLength(0)
  })

  it('never renders for another kind', () => {
    render({ onStopWorkflow: vi.fn() }, { apiVersion: 'batch/v1', kind: 'Job', metadata: { name: 'x', namespace: 'ci', uid: 'u' }, status: {} }, { kind: 'jobs', name: 'x', namespace: 'ci' })
    expect(buttons('Stop…')).toHaveLength(0)
  })

  it('renders disabled with the upgrade note on a Radar without the endpoint', () => {
    render({ workflowStopUpgrade: { feature: 'Argo Workflow stop actions' } })
    const stopButton = button('Stop…')
    expect(stopButton.disabled).toBe(true)
  })

  it('explains both strategies and sends Stop with the reviewed UID by default', async () => {
    const onStopWorkflow = vi.fn(() => Promise.resolve())
    render({ onStopWorkflow })
    act(() => button('Stop…').click())
    const text = document.body.textContent ?? ''
    expect(text).toContain('Stop Workflow')
    expect(text).toContain('then runs the Workflow’s exit handlers')
    expect(text).toContain('including any exit handler')
    expect(radio('Stop').checked).toBe(true)
    await act(async () => { button('Stop').click() })
    expect(onStopWorkflow).toHaveBeenCalledWith({ namespace: 'ci', name: 'build-7', uid: 'uid-1', strategy: 'Stop' })
    await vi.waitFor(() => expect(openDialog()).toBeNull())
  })

  it('sends Terminate when chosen', async () => {
    const onStopWorkflow = vi.fn(() => Promise.resolve())
    render({ onStopWorkflow })
    act(() => button('Stop…').click())
    act(() => radio('Terminate').click())
    await act(async () => { button('Terminate').click() })
    expect(onStopWorkflow).toHaveBeenCalledWith(expect.objectContaining({ strategy: 'Terminate' }))
  })

  it('offers escalation to Terminate while a run is already stopping', async () => {
    const onStopWorkflow = vi.fn(() => Promise.resolve())
    render({ onStopWorkflow }, workflow({ shutdown: 'Stop' }))
    expect(buttons('Stop…')).toHaveLength(0)
    act(() => button('Terminate…').click())
    expect(document.body.textContent).toContain('A Stop was already requested')
    expect(document.querySelector('input[type="radio"]')).toBeNull()
    await act(async () => { button('Terminate').click() })
    expect(onStopWorkflow).toHaveBeenCalledWith(expect.objectContaining({ strategy: 'Terminate', uid: 'uid-1' }))
  })

  it('keeps the mode it opened in when a refresh shows the run stopping', () => {
    render({ onStopWorkflow: vi.fn() })
    act(() => button('Stop…').click())
    render({ onStopWorkflow: vi.fn() }, workflow({ shutdown: 'Stop' }))
    expect(radio('Stop').checked).toBe(true)
  })

  it('closes when the name now belongs to a different run', () => {
    render({ onStopWorkflow: vi.fn() })
    act(() => button('Stop…').click())
    expect(openDialog()).not.toBeNull()
    render({ onStopWorkflow: vi.fn() }, workflow({ uid: 'uid-2' }))
    expect(openDialog()).toBeNull()
  })

  it.each([
    ['finishes', workflow({ phase: 'Failed' })],
    ['is terminated by someone else', workflow({ shutdown: 'Terminate' })],
    ['starts deleting', workflow({ deleting: true })],
    // argo retry keeps the UID; a new start time marks another attempt.
    ['is retried under the same UID', workflow({ startedAt: '2026-11-25T11:00:00Z' })],
  ])('closes an unconfirmed dialog when the run %s', (_label, next) => {
    const onStopWorkflow = vi.fn()
    render({ onStopWorkflow })
    act(() => button('Stop…').click())
    expect(openDialog()).not.toBeNull()
    render({ onStopWorkflow }, next)
    expect(openDialog()).toBeNull()
    expect(onStopWorkflow).not.toHaveBeenCalled()
  })

  it('stays open with its choice after a failed request, leaving the report to the host, without an unhandled rejection', async () => {
    const onStopWorkflow = vi.fn(() => Promise.reject(new Error('forbidden')))
    render({ onStopWorkflow })
    const unhandled = vi.fn()
    process.on('unhandledRejection', unhandled)
    act(() => button('Stop…').click())
    act(() => radio('Terminate').click())
    await act(async () => { button('Terminate').click() })
    await new Promise(resolve => setTimeout(resolve, 10))
    process.off('unhandledRejection', unhandled)
    expect(unhandled).not.toHaveBeenCalled()
    expect(openDialog()).not.toBeNull()
    expect(radio('Terminate').checked).toBe(true)
    expect(button('Terminate').disabled).toBe(false)
  })

  it('keeps the dialog through a pending run starting, then closes when it restarts', () => {
    const pending = { ...workflow({ phase: 'Pending' }), status: { phase: 'Pending', startedAt: null } }
    render({ onStopWorkflow: vi.fn() }, pending)
    act(() => button('Stop…').click())
    act(() => radio('Terminate').click())
    render({ onStopWorkflow: vi.fn() }, workflow({ startedAt: '2026-11-25T10:00:00Z' }))
    expect(radio('Terminate').checked).toBe(true)
    render({ onStopWorkflow: vi.fn() }, workflow({ startedAt: '2026-11-25T11:00:00Z' }))
    expect(openDialog()).toBeNull()
  })

  it('does not take another Workflow’s start time while a request is in flight', async () => {
    let fail: () => void = () => {}
    const onStopWorkflow = vi.fn(() => new Promise<void>((_resolve, reject) => { fail = () => reject(new Error('timeout')) }))
    const pending = { ...workflow({ phase: 'Pending' }), status: { phase: 'Pending', startedAt: null } }
    render({ onStopWorkflow }, pending)
    act(() => button('Stop…').click())
    await act(async () => { button('Stop').click() })
    const other = { ...workflow({ uid: 'uid-9', startedAt: '2026-11-25T09:00:00Z' }), metadata: { name: 'other', namespace: 'ci', uid: 'uid-9' } }
    render({ onStopWorkflow }, other, { ...resource, name: 'other' })
    render({ onStopWorkflow }, pending)
    await act(async () => { fail() })
    expect(openDialog()).not.toBeNull()
  })

  it('keeps naming the reviewed run when the page moves to another Workflow mid-request', async () => {
    let settle: () => void = () => {}
    const onStopWorkflow = vi.fn(() => new Promise<void>(resolve => { settle = resolve }))
    render({ onStopWorkflow })
    act(() => button('Stop…').click())
    await act(async () => { button('Stop').click() })
    const other = { ...workflow({ uid: 'uid-9' }), metadata: { name: 'other', namespace: 'ci', uid: 'uid-9' } }
    render({ onStopWorkflow }, other, { ...resource, name: 'other' })
    expect(openDialog()?.textContent).toContain('ci/build-7')
    expect(onStopWorkflow).toHaveBeenCalledWith(expect.objectContaining({ name: 'build-7', uid: 'uid-1' }))
    await act(async () => { settle() })
    await vi.waitFor(() => expect(openDialog()).toBeNull())
  })

  it('warns that a suspended run may hold the stop until resumed', () => {
    render({ onStopWorkflow: vi.fn() }, { ...workflow(), spec: { suspend: true } })
    act(() => button('Stop…').click())
    expect(openDialog()?.textContent).toContain('suspended')
  })
})
