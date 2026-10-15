// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RollbackDialog } from './RollbackDialog'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

let root: Root | undefined

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
  document.body.replaceChildren()
})

describe('RollbackDialog confirmation', () => {
  it('explains the persistent revision and keeps dry-run copy distinct', async () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    const onConfirm = vi.fn()
    await act(async () => root?.render(
      <RollbackDialog open appLabel="argocd/demo" revision="abc123" historyId="0" onCancel={() => {}} onConfirm={onConfirm} />,
    ))
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
    expect(dialog.textContent).toContain('Once it succeeds, the application stays on that revision with auto-sync off until you sync or turn auto-sync back on.')
    const dryRun = dialog.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')[1]
    await act(async () => dryRun.click())
    expect(dialog.textContent).toContain('Argo will preview syncing this application to a previous revision without applying changes.')
    expect(dialog.textContent).not.toContain('Once it succeeds')
    const confirm = [...dialog.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent === 'Run dry-run')!
    await act(async () => confirm.click())
    expect(onConfirm).toHaveBeenCalledWith({ prune: false, dryRun: true })
  })
})
