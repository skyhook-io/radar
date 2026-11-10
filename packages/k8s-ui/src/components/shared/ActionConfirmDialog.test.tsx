// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ActionConfirmDialog, type ActionConfirmDialogProps } from './ActionConfirmDialog'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const roots: Root[] = []
const base: ActionConfirmDialogProps = {
  open: true, onClose: () => {}, onConfirm: () => {}, title: 'Scale worker?',
  subject: { kind: 'Deployment', namespace: 'jobs', name: 'worker' },
  context: 'test-cluster', effect: 'Set the desired replicas to three.', confirmLabel: 'Scale',
}
function render(props: Partial<ActionConfirmDialogProps> = {}) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  roots.push(root)
  const update = (next: Partial<ActionConfirmDialogProps>) => act(() => root.render(<ActionConfirmDialog {...base} {...next} />))
  update(props)
  return update
}
function button(label: string) {
  return [...document.querySelectorAll('button')].find(b => b.textContent?.trim() === label)!
}
function type(text: string) {
  const input = document.querySelector('input')!
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
afterEach(() => {
  act(() => roots.splice(0).forEach(root => root.unmount()))
  document.body.innerHTML = ''
})

describe('ActionConfirmDialog', () => {
  it('shows the target and context before a reviewed write', () => {
    const confirm = vi.fn()
    render({ onConfirm: confirm })
    expect(document.body.textContent).toContain('Deployment jobs/worker · context test-cluster')
    act(() => button('Scale').click())
    expect(confirm).toHaveBeenCalledOnce()
  })
  it('shows an incomplete form as a quiet hint and a disabled confirm', () => {
    render({ incompleteReason: 'The replica count is unchanged' })
    expect(document.body.textContent).toContain('The replica count is unchanged')
    expect(document.body.textContent).not.toContain('This action is not available')
    expect(button('Scale').disabled).toBe(true)
  })
  it('keeps the alert for a real blocker', () => {
    render({ disabledReason: 'Needs patch deployments in jobs' })
    expect(document.body.textContent).toContain('This action is not available')
    expect(button('Scale').disabled).toBe(true)
  })
  it('requires typed acknowledgement and clears it after closing', () => {
    const update = render({ typedConfirmation: 'worker' })
    expect(button('Scale').disabled).toBe(true)
    type('other')
    expect(button('Scale').disabled).toBe(true)
    type('worker')
    expect(button('Scale').disabled).toBe(false)
    update({ typedConfirmation: 'worker', open: false })
    update({ typedConfirmation: 'worker' })
    expect(document.querySelector('input')?.value).toBe('')
    expect(button('Scale').disabled).toBe(true)
  })
  it('requires the host write guard even with the correct acknowledgement', () => {
    render({ typedConfirmation: 'worker', guardSatisfied: false })
    type('worker')
    expect(button('Scale').disabled).toBe(true)
  })
  it('locks resubmission for an unknown or partial result without claiming no write happened', () => {
    render({ result: { state: 'unknown', message: 'Refresh the target before another attempt.' } })
    expect(button('Scale').disabled).toBe(true)
    expect(document.body.textContent).toContain('Radar could not tell whether this took effect')
    expect(document.body.textContent).not.toContain('The write did not happen')
  })
  it('locks an unknown result even without an error string', () => {
    render({ result: { state: 'unknown' } })
    expect(button('Scale').disabled).toBe(true)
    expect(document.body.textContent).toContain('Radar could not tell whether this took effect')
    expect(document.body.textContent).toContain('Check the object’s current state')
  })
  it('allows retry after a known refusal, using neutral default error copy', () => {
    render({ result: { state: 'error', message: 'The reviewed replica count changed.' } })
    expect(button('Scale').disabled).toBe(false)
    expect(document.body.textContent).toContain('The action could not be completed')
  })
  it('locks confirm, Back and dismissal while the request is pending', () => {
    const close = vi.fn(), back = vi.fn()
    render({ isLoading: true, onClose: close, onBack: back })
    expect(button('Scale').disabled).toBe(true)
    expect(button('Back').disabled).toBe(true)
    expect(button('Cancel').disabled).toBe(true)
    act(() => button('Cancel').click())
    act(() => document.querySelector<HTMLButtonElement>('[aria-label="Close"]')!.click())
    expect(close).not.toHaveBeenCalled()
    expect(back).not.toHaveBeenCalled()
  })
  it('keeps Back and Cancel distinct', () => {
    const close = vi.fn(), back = vi.fn()
    render({ onClose: close, onBack: back })
    act(() => button('Back').click())
    expect(back).toHaveBeenCalledOnce()
    expect(close).not.toHaveBeenCalled()
    act(() => button('Cancel').click())
    expect(close).toHaveBeenCalledOnce()
  })
  it('reveals the reviewed writes through an accessible disclosure', () => {
    render({ writes: [{ summary: 'patch Deployment jobs/worker', detail: 'spec.replicas: 3' }] })
    const toggle = button('The API write')
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    act(() => toggle.click())
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(document.body.textContent).toContain('spec.replicas: 3')
  })
})

it('retains a host-owned outcome lock across dialog dismissal and remount', () => {
  function Host() {
    const [open, setOpen] = useState(true)
    const [unknown, setUnknown] = useState(true)
    return <>
      <button onClick={() => setOpen(true)}>Review action</button>
      <button onClick={() => setUnknown(false)}>Current state reviewed</button>
      {open && <ActionConfirmDialog {...base} result={unknown ? { state: 'unknown' } : undefined} onClose={() => setOpen(false)} />}
    </>
  }
  const root = createRoot(document.body.appendChild(document.createElement('div')))
  roots.push(root)
  act(() => root.render(<Host />))
  expect(button('Scale').disabled).toBe(true)
  act(() => button('Cancel').click())
  act(() => button('Review action').click())
  expect(button('Scale').disabled).toBe(true)
  act(() => button('Current state reviewed').click())
  expect(button('Scale').disabled).toBe(false)
})

it('locks a partial result and explains that changes completed', () => {
  render({ result: { state: 'partial' } })
  expect(button('Scale').disabled).toBe(true)
  expect(document.body.textContent).toContain('Some changes completed')
  expect(document.body.textContent).toContain('Check the object’s current state')
})

it('keeps an empty known-refusal message distinct from an uncertain outcome', () => {
  render({ result: { state: 'error', message: '' } })
  expect(button('Scale').disabled).toBe(false)
  expect(document.body.textContent).toContain('The action could not be completed')
  expect(document.body.textContent).not.toContain('Check the object’s current state')
})
