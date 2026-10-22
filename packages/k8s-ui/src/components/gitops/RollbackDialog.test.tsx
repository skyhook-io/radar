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
  it('explains rollback risk and keeps dry-run copy distinct', async () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    const onConfirm = vi.fn()
    await act(async () => root?.render(
      <RollbackDialog open appLabel="argocd/demo" revision="abc123" historyId="0" onCancel={() => {}} onConfirm={onConfirm} />,
    ))
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
    expect(dialog.textContent).toContain('Auto-sync is off. Later changes can still cause drift. Syncing or enabling auto-sync deploys the current revision and can undo the rollback.')
    expect(dialog.textContent).toContain("Delete tracked resources that aren't in the rollback target")
    expect(dialog.textContent).toContain("Off by default. The target's resources are still applied; other tracked resources are kept.")
    expect(dialog.textContent).toContain("Uses the Application's current sync options. Sync hooks don't run.")
    const dryRun = dialog.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')[1]
    await act(async () => dryRun.click())
    expect(dialog.textContent).toContain('Argo will preview syncing this application to a previous revision without applying changes.')
    expect(dialog.textContent).not.toContain('Later changes can still cause drift.')
    const confirm = [...dialog.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent === 'Run dry-run')!
    await act(async () => confirm.click())
    expect(onConfirm).toHaveBeenCalledWith({ prune: false, dryRun: true })
  })

  it.each(['https://example.com/repo/very/long/path/to/guestbook', 'guestbook (+2 more)'])('shows the selected source with wrapping (%s)', async (source) => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    await act(async () => root?.render(
      <RollbackDialog open appLabel="argocd/demo" revision="abc123" historyId="0" source={source} onCancel={() => {}} onConfirm={() => {}} />,
    ))
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
    const label = [...dialog.querySelectorAll('dt')].find(dt => dt.textContent === 'Source')
    expect(label).toBeDefined()
    expect(label?.nextElementSibling?.textContent).toBe(source)
    expect(label?.nextElementSibling?.classList.contains('break-words')).toBe(true)
  })

  it.each([true, false])('shows controller compatibility only for multi-source entries (%s)', async (multiSource) => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    await act(async () => root?.render(
      <RollbackDialog open appLabel="argocd/demo" revision="aaa111, bbb222" multiSource={multiSource} onCancel={() => {}} onConfirm={() => {}} />,
    ))
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
    const note = 'Rolling back a multi-source Application needs Argo CD 2.12 or later. A 2.11 controller ignores the history entry’s sources and revisions and syncs the current ones.'
    expect(dialog.textContent?.includes(note)).toBe(multiSource)
  })

})
