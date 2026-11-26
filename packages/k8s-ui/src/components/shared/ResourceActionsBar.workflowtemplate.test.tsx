// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResourceActionsBar } from './ResourceActionsBar'
import type { WorkflowTemplateSubmission } from '../../utils/workflow-template-submit'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function template() {
  return {
    apiVersion: 'argoproj.io/v1alpha1',
    kind: 'WorkflowTemplate',
    metadata: { name: 'migrate', namespace: 'jobs', uid: 'template-1' },
    spec: {
      serviceAccountName: 'migrator',
      entrypoint: 'main',
      arguments: { parameters: [{ name: 'target' }, { name: 'batch', value: '10' }] },
    },
  }
}
const roots: Root[] = []
let root: Root

function render(props: Record<string, unknown>, data: unknown = template(), resource = { kind: 'workflowtemplates', name: 'migrate', namespace: 'jobs', group: 'argoproj.io' }) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  roots.push(root)
  act(() => root.render(<ResourceActionsBar resource={resource} data={data} {...props} />))
}
// The last match: once the dialog is open its Submit follows the bar's.
const button = (label: string) => [...document.querySelectorAll('button')].filter(b => b.textContent?.trim() === label).at(-1)
const input = (label: string) => [...document.querySelectorAll<HTMLInputElement>('input')].find(i =>
  i.getAttribute('aria-label') === label || document.querySelector(`label[for="${i.id}"]`)?.textContent?.includes(label))
function type(label: string, text: string) {
  const el = input(label)!
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(el, text)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
const dialogOpen = () => document.body.textContent?.includes('Submit workflow template') === true

afterEach(() => {
  act(() => roots.splice(0).forEach(r => r.unmount()))
  document.body.innerHTML = ''
})

describe('WorkflowTemplate Submit', () => {
  it('is hidden without the callback, which the host withholds without permission', () => {
    render({})
    expect(button('Submit…')).toBeUndefined()
  })

  it('is not offered on other kinds', () => {
    render({ onSubmitWorkflowTemplate: vi.fn() }, { apiVersion: 'argoproj.io/v1alpha1', kind: 'Workflow', metadata: { name: 'migrate-1', namespace: 'jobs' } }, { kind: 'workflows', name: 'migrate-1', namespace: 'jobs', group: 'argoproj.io' })
    expect(button('Submit…')).toBeUndefined()
  })

  it('is disabled with the upgrade note on a Radar that predates the endpoint', () => {
    render({ workflowTemplateSubmitUpgrade: { feature: 'Argo WorkflowTemplate submissions' } })
    expect(button('Submit…')?.disabled).toBe(true)
  })

  it('prefills defaults, leaves run name optional, and sends the reviewed request', async () => {
    const onSubmitWorkflowTemplate = vi.fn<(s: WorkflowTemplateSubmission) => Promise<unknown>>(() => Promise.resolve({}))
    render({ onSubmitWorkflowTemplate })
    act(() => button('Submit…')!.click())
    expect(dialogOpen()).toBe(true)
    expect(document.body.textContent).toContain('migrator')
    expect(input('batch')!.value).toBe('10')
    expect(input('Run name')!.value).toBe('')
    expect(button('Submit')!.disabled).toBe(true)
    expect(document.body.textContent).toContain('Fill in the required parameter')

    type('target', 'orders')
    expect(button('Submit')!.disabled).toBe(false)
    await act(async () => { button('Submit')!.click() })
    expect(onSubmitWorkflowTemplate).toHaveBeenCalledOnce()
    expect(onSubmitWorkflowTemplate.mock.calls[0][0]).toEqual({
      namespace: 'jobs',
      name: 'migrate',
      parameters: { target: 'orders', batch: '10' },
    })
  })

  it('sends an explicit run name when entered', async () => {
    const onSubmitWorkflowTemplate = vi.fn<(s: WorkflowTemplateSubmission) => Promise<unknown>>(() => Promise.resolve({}))
    render({ onSubmitWorkflowTemplate })
    act(() => button('Submit…')!.click())
    type('target', 'orders')
    type('Run name', 'migrate-orders')
    await act(async () => { button('Submit')!.click() })
    expect(onSubmitWorkflowTemplate.mock.calls[0][0]).toEqual({
      namespace: 'jobs',
      name: 'migrate',
      runName: 'migrate-orders',
      parameters: { target: 'orders', batch: '10' },
    })
  })

  it('locks submit and warns when a network error or 5xx leaves the outcome ambiguous', async () => {
    for (const err of [
      new TypeError('Failed to fetch'),
      Object.assign(new Error('could not create the workflow'), { status: 500 }),
      Object.assign(new Error('Kubernetes timed out'), { status: 504 }),
    ]) {
      const onSubmitWorkflowTemplate = vi.fn<(s: WorkflowTemplateSubmission) => Promise<unknown>>(() => Promise.reject(err))
      render({ onSubmitWorkflowTemplate })
      act(() => button('Submit…')!.click())
      type('target', 'orders')
      await act(async () => { button('Submit')!.click() })
      expect(dialogOpen()).toBe(true)
      expect(document.body.textContent).toContain('The run may have started. Check the runs below before submitting again.')
      expect(button('Submit')!.disabled).toBe(true)
      act(() => roots.splice(0).forEach(r => r.unmount()))
      document.body.innerHTML = ''
    }
  })

  it('keeps the form submittable without the ambiguous-failure message on 400 or 409', async () => {
    for (const status of [400, 409]) {
      const onSubmitWorkflowTemplate = vi.fn<(s: WorkflowTemplateSubmission) => Promise<unknown>>(() =>
        Promise.reject(Object.assign(new Error('refused'), { status })),
      )
      render({ onSubmitWorkflowTemplate })
      act(() => button('Submit…')!.click())
      type('target', 'orders')
      await act(async () => { button('Submit')!.click() })
      expect(dialogOpen()).toBe(true)
      expect(document.body.textContent).not.toContain('The run may have started.')
      expect(button('Submit')!.disabled).toBe(false)
      act(() => roots.splice(0).forEach(r => r.unmount()))
      document.body.innerHTML = ''
    }
  })

  it('explains a template without an entrypoint instead of submitting it', () => {
    const data = template()
    delete (data.spec as { entrypoint?: string }).entrypoint
    render({ onSubmitWorkflowTemplate: vi.fn() }, data)
    act(() => button('Submit…')!.click())
    type('target', 'orders')
    expect(document.body.textContent).toContain('has no entrypoint')
    expect(button('Submit')!.disabled).toBe(true)
  })

  it('renders parameters named like Object.prototype members', () => {
    const data = template()
    data.spec.arguments.parameters = [{ name: '__proto__', value: 'a' }, { name: 'constructor', value: 'b' }, { name: 'target' }] as typeof data.spec.arguments.parameters
    const onSubmitWorkflowTemplate = vi.fn<(s: WorkflowTemplateSubmission) => Promise<unknown>>(() => Promise.resolve({}))
    render({ onSubmitWorkflowTemplate }, data)
    act(() => button('Submit…')!.click())
    expect(input('constructor')!.value).toBe('b')
    expect(input('constructor')!.getAttribute('aria-invalid')).toBe('false')
    type('target', 'orders')
    act(() => button('Submit')!.click())
    expect(Object.entries(onSubmitWorkflowTemplate.mock.calls[0][0].parameters)).toEqual([['__proto__', 'a'], ['constructor', 'b'], ['target', 'orders']])
  })

  it('names the controller instance the run is labelled for', () => {
    const data = template()
    ;(data.metadata as Record<string, unknown>).labels = { 'workflows.argoproj.io/controller-instanceid': 'ci' }
    render({ onSubmitWorkflowTemplate: vi.fn() }, data)
    act(() => button('Submit…')!.click())
    expect(document.body.textContent).toContain('instance ID ci')
  })
})
