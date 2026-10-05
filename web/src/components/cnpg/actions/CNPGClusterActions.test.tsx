// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { CNPGClusterActions } from './CNPGClusterActions'

const state = vi.hoisted(() => ({ caps: {} as any, workspace: {} as any, refetch: vi.fn() }))
vi.mock('../../../api/cnpg', () => ({ useCNPGClusterCapabilities: () => state.caps }))
vi.mock('../useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => state.workspace }))
vi.mock('./useOpenCNPGPsql', () => ({ useOpenCNPGPsql: () => vi.fn() }))
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.clearAllMocks() })
function render() {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<CNPGClusterActions namespace="db" name="pg" />))
}
function menu() { act(() => host.querySelector<HTMLButtonElement>('[aria-label="More cluster actions"]')!.click()) }
it('shows a failed capability read with Retry instead of checking forever', () => {
  state.workspace = { query: {} }
  state.caps = { error: new Error('Gateway timeout'), refetch: state.refetch, isFetching: false }
  render()
  expect(host.textContent).toContain('Actions could not be checked: Gateway timeout')
  expect(host.textContent).not.toContain('Checking permissions')
  const retry = [...host.querySelectorAll('button')].find((b) => b.textContent === 'Retry')!
  act(() => retry.click())
  expect(state.refetch).toHaveBeenCalledTimes(1)
})
it('disables header restore for known no sources and keeps unread sources enabled', () => {
  const cluster = { metadata: { name: 'pg', namespace: 'db' }, spec: {} }
  state.caps = { data: { actions: { restore: { allowed: true }, backup: { allowed: false }, switchover: { allowed: false }, psql: { allowed: false } }, facts: { maintenance: {} } } }
  state.workspace = { query: { data: { coverage: { backups: { state: 'full' } }, objects: { backups: [] } } }, fleet: { rows: [{ name: 'pg', namespace: 'db', cluster }] } }
  render(); menu()
  let restore = [...host.querySelectorAll('button')].find((b) => b.textContent?.includes('Restore to a new cluster'))!
  expect(restore.disabled).toBe(true)
  expect(host.textContent).toContain('Nothing to restore from yet')
  state.workspace.query.data.coverage.backups.state = 'denied'
  act(() => root.render(<CNPGClusterActions namespace="db" name="pg" />))
  restore = [...host.querySelectorAll('button')].find((b) => b.textContent?.includes('Restore to a new cluster'))!
  expect(restore.disabled).toBe(false)
  expect(host.textContent).not.toContain('Nothing to restore from yet')
  state.caps.isRefetchError = true
  state.caps.error = new Error('Refresh timeout')
  state.caps.dataUpdatedAt = Date.now() - 180_000
  act(() => root.render(<CNPGClusterActions namespace="db" name="pg" />))
  expect(host.textContent).toContain('Last refresh failed: Refresh timeout')
  expect(host.textContent).toContain('3m ago')
  expect(host.textContent).not.toContain('Actions could not be checked')
  expect(restore.disabled).toBe(false)
})
