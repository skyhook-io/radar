// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { CNPGInstanceActions } from './CNPGInstanceActions'

const state = vi.hoisted(() => ({ data: {} as any }))
vi.mock('../../../api/cnpg', () => ({ useCNPGClusterCapabilities: () => ({ data: state.data }) }))
vi.mock('./useOpenCNPGPsql', () => ({ useOpenCNPGPsql: () => vi.fn() }))
vi.mock('./CNPGClusterActions', () => ({ ClusterActionDialog: () => <div>Restart review opened</div> }))
vi.mock('./CNPGDestroyInstanceDialog', () => ({ CNPGDestroyInstanceDialog: () => null }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.useRealTimers() })
it('keeps blocked restart focusable, reveals its reason on hover, focus and click, and never opens the write dialog', () => {
  vi.useFakeTimers()
  const reason = 'A standby is still joining; wait until it is ready'
  state.data = { facts: { instances: [{ pod: 'orders-1' }], currentPrimary: 'orders-1', fencedInstances: { all: false, instances: [] } }, actions: {}, instanceActions: { 'orders-1': { restart: { allowed: false, reason }, psql: { allowed: true }, fence: { allowed: true } } } }
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<CNPGInstanceActions namespace="db" cluster="orders" pod="orders-1" />))
  const restart = [...host.querySelectorAll('button')].find((b) => b.textContent === 'Restart')!
  expect(restart.disabled).toBe(false)
  expect(restart.getAttribute('aria-disabled')).toBe('true')
  act(() => { restart.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })); vi.advanceTimersByTime(350) })
  expect(document.body.textContent).toContain(reason)
  act(() => { restart.focus(); vi.advanceTimersByTime(350) })
  expect(document.activeElement).toBe(restart)
  expect(document.body.textContent).toContain(reason)
  act(() => restart.click())
  expect(host.querySelector('[role="status"]')?.textContent).toBe(reason)
  expect(host.textContent).not.toContain('Restart review opened')
  state.data.instanceActions['orders-1'].restart = { allowed: true }
  act(() => root.render(<CNPGInstanceActions namespace="db" cluster="orders" pod="orders-1" />))
  expect(host.querySelector('[role="status"]')).toBeNull()
  act(() => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Restart')!.click())
  expect(host.textContent).toContain('Restart review opened')
})
