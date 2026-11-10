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

  it.each([true, false])('shows controller compatibility when either the current Application or entry uses multiple sources (%s)', async (multiSourceWarning) => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    await act(async () => root?.render(
      <RollbackDialog open appLabel="argocd/demo" revision="aaa111, bbb222" multiSourceWarning={multiSourceWarning} onCancel={() => {}} onConfirm={() => {}} />,
    ))
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
    const note = 'On Argo CD before 2.12, rolling back across a single-source/multi-source change or to a multi-source entry can deploy the wrong revisions or render nothing; with prune, it can delete resources.'
    expect(dialog.textContent?.includes(note)).toBe(multiSourceWarning)
  })

})

it('disables confirmation while support is being checked', async () => {
  const host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  const confirm = vi.fn()
  await act(async () => root?.render(<RollbackDialog open appLabel="argocd/demo" revision="old" blockedReason="Checking this cluster's Radar…" onCancel={() => {}} onConfirm={confirm} />))
  const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
  expect(dialog.textContent).toContain("Checking this cluster's Radar…")
  const button = [...dialog.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent === 'Roll back')!
  expect(button.disabled).toBe(true)
  await act(async () => button.click())
  expect(confirm).not.toHaveBeenCalled()
})
