// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { SyncOptionsDialog } from './SyncOptionsDialog'
import { RollbackDialog } from './RollbackDialog'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
it.each(['sync', 'rollback'])('shows a permission alert next to the disabled %s confirmation and keeps Cancel usable', async kind => {
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  const confirm = vi.fn()
  const cancel = vi.fn()
  const reason = "Your role can't patch Argo CD Applications in argocd."
  await act(async () => root.render(kind === 'sync'
    ? <SyncOptionsDialog open appLabel="argocd/demo" disabledReason={reason} onCancel={cancel} onConfirm={confirm} />
    : <RollbackDialog open appLabel="argocd/demo" revision="abc123" disabledReason={reason} onCancel={cancel} onConfirm={confirm} />))
  const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
  expect(dialog.textContent).toContain('Action unavailable')
  expect(dialog.textContent).toContain(reason)
  const buttons = [...dialog.querySelectorAll<HTMLButtonElement>('button')]
  expect(buttons.find(button => button.textContent === (kind === 'sync' ? 'Sync now' : 'Roll back'))!.disabled).toBe(true)
  const cancelButton = buttons.find(button => button.textContent === 'Cancel')!
  expect(cancelButton.disabled).toBe(false)
  await act(async () => cancelButton.click())
  expect(cancel).toHaveBeenCalledOnce()
  expect(confirm).not.toHaveBeenCalled()
  await act(async () => root.unmount())
  host.remove()
})
