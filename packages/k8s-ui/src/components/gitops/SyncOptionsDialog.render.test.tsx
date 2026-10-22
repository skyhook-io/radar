// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { SyncOptionsDialog, type ArgoSyncOpts } from './SyncOptionsDialog'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const APP_OPTIONS = ['CreateNamespace=true', 'ServerSideApply=true']
const STALE_OPTIONS_NOTICE = "The Application's sync options changed while this dialog was open. This sync uses the options shown here. Cancel and reopen to load the latest."
let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let confirmed: ArgoSyncOpts[]
let validated: ArgoSyncOpts[]

beforeEach(() => {
  confirmed = []
  validated = []
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
})

afterEach(async () => {
  await act(async () => root.unmount())
  host.remove()
  document.body.innerHTML = ''
})

async function render(props: Partial<Parameters<typeof SyncOptionsDialog>[0]> = {}) {
  await act(async () => root.render(
    <SyncOptionsDialog
      open
      appLabel="argocd/guestbook"
      applicationSyncOptions={APP_OPTIONS}
      onCancel={() => {}}
      onConfirm={(opts) => confirmed.push(opts)}
      {...props}
    />,
  ))
}

const button = (label: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)!
const toggle = (label: string) => [...document.querySelectorAll('label')].find((l) => l.textContent?.startsWith(label))!.querySelector('input')!
const click = async (el: HTMLElement) => { await act(async () => el.click()) }

it('starts with prune off and submits nothing for the options, so the Application keeps its own', async () => {
  await render()
  expect(toggle('Prune').checked).toBe(false)
  expect(toggle('Server-side apply').checked).toBe(true)
  expect(toggle('Server-side apply').disabled).toBe(false)
  expect(toggle('Replace').checked).toBe(false)
  expect(toggle('Replace').disabled).toBe(false)
  expect(document.body.textContent).not.toContain(STALE_OPTIONS_NOTICE)
  await click(button('Sync now'))
  expect(confirmed).toEqual([{ revision: undefined, prune: false, dryRun: false, force: false, applyOnly: false }])
})

it('hides the revision for multi-source Applications and syncs their target revisions', async () => {
  await render({ multiSource: true })
  expect(document.body.textContent).not.toContain('Revision (optional)')
  expect(document.body.textContent).toContain("Each source uses its own targetRevision.")
  await click(button('Sync now'))
  expect(confirmed[0].revision).toBeUndefined()
})

it('omits an entered revision if the Application becomes multi-source while open', async () => {
  await render()
  const revision = document.querySelector('input:not([type="checkbox"])') as HTMLInputElement
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(revision, 'release-1')
    revision.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await render({ multiSource: true })
  await click(button('Sync now'))
  expect(confirmed[0].revision).toBeUndefined()
})

it('explains selective sync hooks, dependencies and namespace management without denying wave ordering', async () => {
  await render({ resource: { kind: 'Deployment', group: 'apps', namespace: 'guestbook', name: 'web' } })
  expect(document.body.textContent).toContain('Syncs only the selected resources, without hooks. Dependencies outside the selection are not synced. Application namespace creation options still apply.')
  expect(document.body.textContent).not.toContain('Only this resource will be applied')
  expect(document.body.textContent).not.toContain('bypasses hooks and normal sync-wave ordering')
})

it('disables unknown sync options and omits them when confirming', async () => {
  await render({ applicationSyncOptions: undefined })
  expect(toggle('Replace').disabled).toBe(true)
  expect(toggle('Server-side apply').disabled).toBe(true)
  expect(document.body.textContent).toContain("This view can't show or change the Application's sync options, so Replace and Server-side apply are unavailable here.")
  await click(toggle('Replace'))
  await click(toggle('Server-side apply'))
  await click(toggle('Prune'))
  await click(button('Sync now'))
  expect(confirmed).toEqual([{ revision: undefined, prune: true, dryRun: false, force: false, applyOnly: false }])
})

it('omits unknown sync options for selective sync and its dry-run, even after earlier edits', async () => {
  const props = {
    resource: { kind: 'Deployment', group: 'apps', namespace: 'guestbook', name: 'web' },
    onValidate: (opts: ArgoSyncOpts) => validated.push(opts),
  }
  await render(props)
  await click(toggle('Replace'))
  await render({ ...props, applicationSyncOptions: undefined })
  expect(toggle('Replace').disabled).toBe(true)
  expect(toggle('Server-side apply').disabled).toBe(true)
  await click(button('Dry-run'))
  await click(button('Sync resource'))
  expect(validated).toEqual([{ revision: undefined, prune: false, dryRun: true, force: false, applyOnly: false }])
  expect(confirmed).toEqual([{ revision: undefined, prune: false, dryRun: false, force: false, applyOnly: false }])
})

it('keeps toggles editable when the Application has an explicitly empty option list', async () => {
  await render({ applicationSyncOptions: [] })
  expect(toggle('Replace').disabled).toBe(false)
  expect(toggle('Replace').checked).toBe(false)
  expect(toggle('Server-side apply').disabled).toBe(false)
  expect(toggle('Server-side apply').checked).toBe(false)
  expect(document.body.textContent).not.toContain("This view can't show or change")
  await click(button('Sync now'))
  expect(confirmed[0]).not.toHaveProperty('syncOptions')
  await click(toggle('Replace'))
  await click(toggle('Server-side apply'))
  await click(button('Sync now'))
  expect(confirmed[1].syncOptions).toEqual(['Replace=true', 'ServerSideApply=true'])
  await click(toggle('Replace'))
  await click(toggle('Server-side apply'))
  await click(button('Sync now'))
  expect(confirmed[2]).not.toHaveProperty('syncOptions')
})

it("sends the Application's options with one edit when a toggle changes", async () => {
  await render()
  await click(toggle('Server-side apply'))
  await click(toggle('Prune'))
  await click(button('Sync now'))
  expect(confirmed[0]).toMatchObject({ prune: true, syncOptions: ['CreateNamespace=true'] })
})

it("drops the Application's other options only through the Keep switch", async () => {
  await render()
  expect(toggle("Keep the Application's other sync options").checked).toBe(true)
  await click(toggle("Keep the Application's other sync options"))
  await click(button('Sync now'))
  expect(confirmed[0].syncOptions).toEqual(['ServerSideApply=true'])
})

it('builds the selective sync and its dry-run from the same options', async () => {
  await render({
    resource: { kind: 'Deployment', group: 'apps', namespace: 'guestbook', name: 'web' },
    onValidate: (opts) => validated.push(opts),
  })
  await click(button('Dry-run'))
  await click(button('Sync resource'))
  expect(validated).toEqual([{ revision: undefined, prune: false, dryRun: true, force: false, applyOnly: false }])
  expect(confirmed).toEqual([{ revision: undefined, prune: false, dryRun: false, force: false, applyOnly: false }])

  await click(toggle('Replace'))
  await click(button('Dry-run'))
  await click(button('Sync resource'))
  expect(validated[1].syncOptions).toEqual(['CreateNamespace=true', 'Replace=true', 'ServerSideApply=true'])
  expect(confirmed[1].syncOptions).toEqual(['CreateNamespace=true', 'Replace=true', 'ServerSideApply=true'])
})

it('reopening resets edits to the Application’s options', async () => {
  await render()
  await click(toggle('Server-side apply'))
  await render({ open: false })
  await render()
  expect(toggle('Server-side apply').checked).toBe(true)
  await click(button('Sync now'))
  expect(confirmed[0]).not.toHaveProperty('syncOptions')
})

it("keeps the user's dry-run and revision when the Application's options change while open", async () => {
  const onValidationReset = vi.fn()
  await render({ onValidationReset })
  await click(toggle('Dry run'))
  const revision = document.querySelector('input:not([type="checkbox"])') as HTMLInputElement
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(revision, 'release-1')
    revision.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await render({ onValidationReset, applicationSyncOptions: ['CreateNamespace=true'] })
  expect(toggle('Dry run').checked).toBe(true)
  expect(toggle('Server-side apply').checked).toBe(false)
  expect(onValidationReset).toHaveBeenCalledTimes(1)
  expect(document.body.textContent).not.toContain(STALE_OPTIONS_NOTICE)
  expect(toggle("Keep the Application's other sync options").checked).toBe(true)
  await click(button('Run dry-run'))
  expect(confirmed[0]).toEqual({ revision: 'release-1', prune: false, dryRun: true, force: false, applyOnly: false })
})

it("keeps the user's option edits when the Application's options change while open", async () => {
  await render()
  await click(toggle('Replace'))
  expect(document.body.textContent).not.toContain(STALE_OPTIONS_NOTICE)
  await render({ applicationSyncOptions: [...APP_OPTIONS] })
  expect(document.body.textContent).not.toContain(STALE_OPTIONS_NOTICE)
  await render({ applicationSyncOptions: ['PruneLast=true'] })
  expect(document.body.textContent).toContain(STALE_OPTIONS_NOTICE)
  expect(toggle('Keep the other sync options shown here').checked).toBe(true)
  expect(document.body.textContent).not.toContain("Keep the Application's other sync options")
  expect(toggle('Replace').checked).toBe(true)
  expect(toggle('Server-side apply').checked).toBe(true)
  await click(button('Sync now'))
  expect(confirmed[0].syncOptions).toEqual(['CreateNamespace=true', 'Replace=true', 'ServerSideApply=true'])
})

it('sends the options it shows once they are no longer the Application’s current ones', async () => {
  const onValidationReset = vi.fn()
  await render({ onValidationReset })
  await click(toggle('Replace'))
  await click(toggle('Replace'))
  await render({ onValidationReset, applicationSyncOptions: [] })
  expect(toggle('Server-side apply').checked).toBe(true)
  await click(button('Sync now'))
  expect(confirmed[0].syncOptions).toEqual(APP_OPTIONS)
})

it('clears the stale-options notice when live options match again or the dialog reopens', async () => {
  await render()
  await click(toggle('Replace'))
  await render({ applicationSyncOptions: [] })
  expect(document.body.textContent).toContain(STALE_OPTIONS_NOTICE)
  await render()
  expect(document.body.textContent).not.toContain(STALE_OPTIONS_NOTICE)
  expect(toggle("Keep the Application's other sync options").checked).toBe(true)
  await render({ applicationSyncOptions: ['PruneLast=true'] })
  expect(document.body.textContent).toContain(STALE_OPTIONS_NOTICE)
  await render({ open: false, applicationSyncOptions: ['PruneLast=true'] })
  await render({ applicationSyncOptions: ['PruneLast=true'] })
  expect(document.body.textContent).not.toContain(STALE_OPTIONS_NOTICE)
  expect(toggle('Replace').checked).toBe(false)
  expect(toggle("Keep the Application's other sync options").checked).toBe(true)
  await click(button('Sync now'))
  expect(confirmed[0]).not.toHaveProperty('syncOptions')
})

it('shows the stale-options notice even when the snapshot has no other options', async () => {
  await render({ applicationSyncOptions: [] })
  await click(toggle('Replace'))
  await render({ applicationSyncOptions: ['PruneLast=true'] })
  expect(document.body.textContent).toContain(STALE_OPTIONS_NOTICE)
  expect(document.body.textContent).not.toContain('Keep the other sync options shown here')
  await click(button('Sync now'))
  expect(confirmed[0].syncOptions).toEqual(['Replace=true'])
})
