// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResourceActionsBar } from './ResourceActionsBar'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const longName = 'a'.repeat(52)
const cronJob = { apiVersion: 'batch/v1', kind: 'CronJob', metadata: { name: longName, namespace: 'prod' }, spec: { schedule: '@daily' } }
const roots: Root[] = []

function render(props: Record<string, unknown>, data: unknown = cronJob) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  roots.push(root)
  act(() => root.render(<ResourceActionsBar resource={{ kind: 'cronjobs', name: longName, namespace: 'prod' }} data={data} {...props} />))
}
// The last match: once the dialog is open its Trigger follows the bar's.
const button = (label: string) => [...document.querySelectorAll('button')].filter(b => b.textContent?.trim() === label).at(-1)!
const jobNameInput = () => document.querySelector<HTMLInputElement>('input[aria-label="Job name"]')
function type(text: string) {
  const input = jobNameInput()!
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

afterEach(() => {
  act(() => roots.splice(0).forEach(root => root.unmount()))
  document.body.innerHTML = ''
})

describe('CronJob Trigger', () => {
  it('triggers at once without the Job name dialog', () => {
    const onTriggerCronJob = vi.fn()
    render({ onTriggerCronJob })
    act(() => button('Trigger').click())
    expect(onTriggerCronJob).toHaveBeenCalledWith({ namespace: 'prod', name: longName })
    expect(jobNameInput()).toBeNull()
  })

  it('leaves a failed one-click trigger to the host, without an unhandled rejection', async () => {
    // Not vi.fn's own return: it attaches handlers to track settled results.
    const calls = vi.fn()
    const onTriggerCronJob = (params: unknown) => { calls(params); return Promise.reject(new Error('boom')) }
    render({ onTriggerCronJob })
    const unhandled = vi.fn()
    process.on('unhandledRejection', unhandled)
    await act(async () => { button('Trigger').click() })
    await new Promise((resolve) => setTimeout(resolve, 10))
    process.off('unhandledRejection', unhandled)
    expect(calls).toHaveBeenCalledOnce()
    expect(unhandled).not.toHaveBeenCalled()
  })

  it('asks for a Job name, prefilled to fit 63 characters, and sends the edited one', () => {
    const onTriggerCronJob = vi.fn()
    render({ onTriggerCronJob, cronJobTriggerDialog: true })
    act(() => button('Trigger').click())
    expect(onTriggerCronJob).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('Trigger CronJob')
    const suggested = jobNameInput()!.value
    expect(suggested.length).toBeLessThanOrEqual(63)
    expect(suggested).toMatch(/-manual-[a-z0-9]{5}$/)

    type('nightly-rerun')
    act(() => button('Trigger').click())
    expect(onTriggerCronJob).toHaveBeenCalledWith({ namespace: 'prod', name: longName, jobName: 'nightly-rerun' })
  })

  it('explains an invalid name and blocks Trigger', () => {
    const onTriggerCronJob = vi.fn()
    render({ onTriggerCronJob, cronJobTriggerDialog: true })
    act(() => button('Trigger').click())
    type('Nightly_Rerun')
    expect(document.body.textContent).toContain('lowercase letters')
    expect(button('Trigger').disabled).toBe(true)
  })

  it('focuses the Job name and submits a valid one on Enter', () => {
    const onTriggerCronJob = vi.fn()
    render({ onTriggerCronJob, cronJobTriggerDialog: true })
    act(() => button('Trigger').click())
    expect(document.activeElement).toBe(jobNameInput())
    type('nightly-rerun')
    act(() => { jobNameInput()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })) })
    expect(onTriggerCronJob).toHaveBeenCalledWith({ namespace: 'prod', name: longName, jobName: 'nightly-rerun' })
  })

  it('ignores Enter on an invalid name', () => {
    const onTriggerCronJob = vi.fn()
    render({ onTriggerCronJob, cronJobTriggerDialog: true })
    act(() => button('Trigger').click())
    type('Bad_Name')
    act(() => { jobNameInput()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })) })
    expect(onTriggerCronJob).not.toHaveBeenCalled()
  })

  it('ties the validation reason to the input', () => {
    render({ onTriggerCronJob: vi.fn(), cronJobTriggerDialog: true })
    act(() => button('Trigger').click())
    type('Bad_Name')
    const describedBy = jobNameInput()!.getAttribute('aria-describedby')
    expect(describedBy).toBeTruthy()
    expect(document.getElementById(describedBy!)?.textContent).toContain('lowercase letters')
  })

  it('trims the name before checking and sending it', () => {
    const onTriggerCronJob = vi.fn()
    render({ onTriggerCronJob, cronJobTriggerDialog: true })
    act(() => button('Trigger').click())
    type('  nightly-rerun ')
    expect(button('Trigger').disabled).toBe(false)
    act(() => button('Trigger').click())
    expect(onTriggerCronJob).toHaveBeenCalledWith({ namespace: 'prod', name: longName, jobName: 'nightly-rerun' })
  })

  it('keeps the dialog and the typed name when the trigger fails', async () => {
    const calls = vi.fn()
    const onTriggerCronJob = (params: unknown) => { calls(params); return Promise.reject(new Error('already exists')) }
    const unhandled = vi.fn()
    process.on('unhandledRejection', unhandled)
    render({ onTriggerCronJob, cronJobTriggerDialog: true })
    act(() => button('Trigger').click())
    type('nightly-rerun')
    await act(async () => { button('Trigger').click() })
    await new Promise((resolve) => setTimeout(resolve, 10))
    process.off('unhandledRejection', unhandled)
    expect(calls).toHaveBeenCalledOnce()
    expect(unhandled).not.toHaveBeenCalled()
    expect(jobNameInput()?.value).toBe('nightly-rerun')
    expect(button('Trigger').disabled).toBe(false)
  })

  it('closes once the trigger succeeds', async () => {
    const onTriggerCronJob = vi.fn(() => Promise.resolve({ jobName: 'nightly-rerun' }))
    render({ onTriggerCronJob, cronJobTriggerDialog: true })
    act(() => button('Trigger').click())
    await act(async () => { button('Trigger').click() })
    await vi.waitFor(() => expect(document.body.textContent).not.toContain('Trigger CronJob'))
  })

  it('suggests a dotless name with room for the widest index of an Indexed template', () => {
    const dotted = `report.v2-${'a'.repeat(40)}`
    const indexed = { ...cronJob, metadata: { name: dotted, namespace: 'prod' }, spec: { schedule: '@daily', jobTemplate: { spec: { completionMode: 'Indexed', completions: 100 } } } }
    render({ onTriggerCronJob: vi.fn(), cronJobTriggerDialog: true }, indexed)
    act(() => button('Trigger').click())
    const suggested = jobNameInput()!.value
    expect(`${suggested}-99`.length).toBeLessThanOrEqual(63)
    expect(suggested).not.toContain('.')
    type('report.rerun')
    expect(document.body.textContent).toContain("cannot contain '.'")
  })
})

it.each([
  { name: 'second-cronjob', uid: 'first-uid' },
  { name: 'first-cronjob', uid: 'replacement-uid' },
])('discards the draft when the target changes to $name ($uid)', ({ name, uid }) => {
  const onTriggerCronJob = vi.fn()
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  roots.push(root)
  const show = (name: string, uid: string) => act(() => root.render(<ResourceActionsBar
    resource={{ kind: 'cronjobs', name, namespace: 'prod' }}
    data={{ ...cronJob, metadata: { name, namespace: 'prod', uid } }}
    onTriggerCronJob={onTriggerCronJob}
    cronJobTriggerDialog
  />))

  const open = () => act(() => [...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Trigger')!.click())

  show('first-cronjob', 'first-uid')
  open()
  type('custom-for-first')
  show(name, uid)
  expect(document.querySelector('[role="dialog"]:not([inert] *)')).toBeNull()
  expect(onTriggerCronJob).not.toHaveBeenCalled()

  open()
  expect(jobNameInput()?.value).toMatch(new RegExp(`^${name}-manual-`))
})
