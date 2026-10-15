// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { SyncOptionsDialog, type ArgoSyncOpts } from './SyncOptionsDialog'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const APP_OPTIONS = ['CreateNamespace=true', 'ServerSideApply=true']
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
  expect(toggle('Replace').checked).toBe(false)
  await click(button('Sync now'))
  expect(confirmed).toEqual([{ revision: undefined, prune: false, dryRun: false, force: false, applyOnly: false }])
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
  await click(button('Run dry-run'))
  expect(confirmed[0]).toEqual({ revision: 'release-1', prune: false, dryRun: true, force: false, applyOnly: false })
})

it("keeps the user's option edits when the Application's options change while open", async () => {
  await render()
  await click(toggle('Replace'))
  await render({ applicationSyncOptions: ['PruneLast=true'] })
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
