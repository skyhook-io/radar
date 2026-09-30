// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
import { ActionConfirmDialog } from './ActionConfirmDialog'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function render(props: { disabledReason?: string; incompleteReason?: string }) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  act(() => {
    root.render(
      <ActionConfirmDialog open onClose={() => {}} onConfirm={() => {}} title="Edit" subject={{ kind: 'ScheduledBackup', name: 's' }} effect="x" confirmLabel="Save" {...props} />,
    )
  })
  return root
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('ActionConfirmDialog', () => {
  it('shows an incomplete form as a quiet hint and a disabled confirm, not an alert', () => {
    const root = render({ incompleteReason: 'The schedule is unchanged' })
    expect(document.body.textContent).toContain('The schedule is unchanged')
    expect(document.body.textContent).not.toContain('This action is not available')
    const save = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Save')
    expect(save?.disabled).toBe(true)
    act(() => root.unmount())
  })
  it('keeps the alert for a real blocker', () => {
    const root = render({ disabledReason: 'Needs patch scheduledbackups in db' })
    expect(document.body.textContent).toContain('This action is not available')
    act(() => root.unmount())
  })
})
