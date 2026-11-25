// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResourceActionsBar } from './ResourceActionsBar'
import type { WorkflowTemplateSubmission } from '../../utils/workflow-template-submit'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function template(resourceVersion = '7') {
  return {
    apiVersion: 'argoproj.io/v1alpha1',
    kind: 'WorkflowTemplate',
    metadata: { name: 'migrate', namespace: 'jobs', uid: 'template-1', resourceVersion },
    spec: {
      serviceAccountName: 'migrator',
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
function rerender(props: Record<string, unknown>, data: unknown) {
  act(() => root.render(<ResourceActionsBar resource={{ kind: 'workflowtemplates', name: 'migrate', namespace: 'jobs', group: 'argoproj.io' }} data={data} {...props} />))
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

  it('prefills defaults, requires a value for each required parameter, and sends the reviewed request', async () => {
    const onSubmitWorkflowTemplate = vi.fn<(s: WorkflowTemplateSubmission) => Promise<unknown>>(() => Promise.resolve({}))
    render({ onSubmitWorkflowTemplate })
    act(() => button('Submit…')!.click())
    expect(dialogOpen()).toBe(true)
    expect(document.body.textContent).toContain('migrator')
    expect(input('batch')!.value).toBe('10')
    expect(input('Run name')!.value).toMatch(/^migrate-[a-z0-9]{5}$/)
    expect(button('Submit')!.disabled).toBe(true)
    expect(document.body.textContent).toContain('Fill in the required parameter')

    type('target', 'orders')
    type('Run name', 'migrate-orders')
    expect(button('Submit')!.disabled).toBe(false)
    await act(async () => { button('Submit')!.click() })
    expect(onSubmitWorkflowTemplate).toHaveBeenCalledOnce()
    const sent = onSubmitWorkflowTemplate.mock.calls[0][0]
    expect(sent).toMatchObject({
      namespace: 'jobs',
      name: 'migrate',
      resourceVersion: '7',
      runName: 'migrate-orders',
      parameters: { target: 'orders', batch: '10' },
    })
    expect(sent.submission).toMatch(/^[A-Za-z0-9-]{8,64}$/)
  })

  it('keeps the dialog on failure, repeats an unchanged request with the same submission, and a changed one with a new one', async () => {
    let fail = true
    const onSubmitWorkflowTemplate = vi.fn<(s: WorkflowTemplateSubmission) => Promise<unknown>>(() => (fail ? Promise.reject(new Error('timeout')) : Promise.resolve({})))
    render({ onSubmitWorkflowTemplate })
    act(() => button('Submit…')!.click())
    type('target', 'orders')
    await act(async () => { button('Submit')!.click() })
    expect(dialogOpen()).toBe(true)
    await act(async () => { button('Submit')!.click() })
    const [first, second] = onSubmitWorkflowTemplate.mock.calls.map(c => c[0])
    expect(second.submission).toBe(first.submission)
    expect(second.runName).toBe(first.runName)

    type('target', 'payments')
    fail = false
    await act(async () => { button('Submit')!.click() })
    const third = onSubmitWorkflowTemplate.mock.calls[2][0]
    expect(third.submission).not.toBe(first.submission)
    expect(third.parameters.target).toBe('payments')
    await act(async () => { await new Promise(r => setTimeout(r, 0)) })
    expect(onSubmitWorkflowTemplate).toHaveBeenCalledTimes(3)
  })

  it('refuses to submit once the template changes under the dialog', () => {
    const onSubmitWorkflowTemplate = vi.fn()
    const props = { onSubmitWorkflowTemplate }
    render(props)
    act(() => button('Submit…')!.click())
    type('target', 'orders')
    rerender(props, template('8'))
    expect(document.body.textContent).toContain('The template changed since you opened this')
    expect(button('Submit')!.disabled).toBe(true)
  })
})
