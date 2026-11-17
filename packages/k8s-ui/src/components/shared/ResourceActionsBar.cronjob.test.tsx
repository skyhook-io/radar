// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResourceActionsBar } from './ResourceActionsBar'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const longName = 'a'.repeat(52)
const cronJob = { apiVersion: 'batch/v1', kind: 'CronJob', metadata: { name: longName, namespace: 'prod' }, spec: { schedule: '@daily' } }
const roots: Root[] = []

function render(props: Record<string, unknown>) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  roots.push(root)
  act(() => root.render(<ResourceActionsBar resource={{ kind: 'cronjobs', name: longName, namespace: 'prod' }} data={cronJob} {...props} />))
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
})
