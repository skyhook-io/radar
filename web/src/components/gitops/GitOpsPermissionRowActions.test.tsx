// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { GitOpsRow } from '@skyhook-io/k8s-ui'
import { GitOpsPermissionRowActions } from './GitOpsPermissionRowActions'

vi.mock('../../context/RadarUpgradeHost', () => ({ useRadarUpgradeHost: () => ({}) }))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
const row = { kindName: 'applications', group: 'argoproj.io', namespace: 'argocd', name: 'demo' } as GitOpsRow
const note = "Couldn't check your permissions. You can still try."
const sync = vi.fn()
let client: QueryClient
let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let answer: () => Promise<Response>
const requests = vi.fn((input: RequestInfo | URL) => String(input).includes('/gitops/capabilities/') ? answer() : new Promise<Response>(() => {}))

beforeEach(async () => {
  vi.useFakeTimers()
  sync.mockClear()
  requests.mockClear()
  answer = () => new Promise<Response>(() => {})
  vi.stubGlobal('fetch', requests)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(['capabilities'], { features: { gitopsActionCapabilities: true } })
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  await act(async () => root.render(
    <QueryClientProvider client={client}>
      <GitOpsPermissionRowActions row={row} items={[{ key: 'sync', label: 'Sync', icon: RefreshCw, onClick: sync }]} />
    </QueryClientProvider>,
  ))
})
afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  host.remove()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})
async function openMenu() {
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="Row actions"]')!.click())
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
}
const action = () => host.querySelector<HTMLButtonElement>('[role="menuitem"]')!

it('shows the unavailable note only in the open menu after a failed check and permits the action', async () => {
  let reject: (error: Error) => void
  answer = () => new Promise((_, fail) => { reject = fail })
  expect(requests.mock.calls.some(([url]) => String(url).includes('/gitops/capabilities/'))).toBe(false)
  expect(host.textContent).not.toContain(note)
  await openMenu()
  expect(host.textContent).not.toContain(note)
  await act(async () => {
    reject!(new Error('Permission check failed'))
    await vi.advanceTimersByTimeAsync(1)
  })
  const menu = host.querySelector('[role="menu"]')!
  expect(menu.querySelector('p')!.textContent).toBe(note)
  expect(menu.querySelector('p')!.className).toContain('text-theme-text-secondary')
  expect(menu.querySelectorAll('button')).toHaveLength(1)
  expect(action().disabled).toBe(false)
  expect(action().getAttribute('aria-disabled')).not.toBe('true')
  await act(async () => action().click())
  expect(sync).toHaveBeenCalledOnce()
  await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
  expect(host.querySelector('[role="menu"]')).toBeNull()
  expect(host.textContent).not.toContain(note)
})

it('shows the unavailable note and enables an expired denial while checks remain pending', async () => {
  answer = async () => Response.json({ actions: { sync: { allowed: false, denied: [{ verb: 'patch', resource: 'applications', group: 'argoproj.io', namespace: 'argocd', name: 'demo' }] } } })
  await openMenu()
  expect(action().getAttribute('aria-disabled')).toBe('true')
  expect(host.textContent).not.toContain(note)
  answer = () => new Promise<Response>(() => {})
  await act(async () => { await vi.advanceTimersByTimeAsync(119_998) })
  expect(action().getAttribute('aria-disabled')).toBe('true')
  await act(async () => { await vi.advanceTimersByTimeAsync(2) })
  expect(host.querySelector('[role="menu"]')!.textContent).toContain(note)
  expect(action().disabled).toBe(false)
  expect(action().getAttribute('aria-disabled')).not.toBe('true')
  await act(async () => action().click())
  expect(sync).toHaveBeenCalledOnce()
})
