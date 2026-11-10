// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { CNPGClusterActions, CNPGRestartReview } from './CNPGClusterActions'
import { renderToStaticMarkup } from 'react-dom/server'

const state = vi.hoisted(() => ({ caps: {} as any, workspace: {} as any, refetch: vi.fn() }))
vi.mock('../../../api/cnpg', () => ({ useCNPGClusterCapabilities: () => state.caps }))
vi.mock('../useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => state.workspace }))
vi.mock('./useOpenCNPGPsql', () => ({ useOpenCNPGPsql: () => vi.fn() }))
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.clearAllMocks(); vi.useRealTimers() })
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

it('keeps blocked header buttons focusable, explains on focus and click, and prints menu reasons', () => {
  vi.useFakeTimers()
  const cap = { allowed: false, reason: 'Configure a backup destination on orders first' }
  state.caps = { data: { actions: { backup: cap, switchover: { allowed: false, reason: 'No ready standby' }, unfence: { allowed: false, reason: 'No instances are fenced' }, psql: { allowed: false, reason: 'Needs create pods/exec' }, restore: { allowed: false, reason: 'Needs create clusters' } }, facts: { maintenance: {} } } }
  state.workspace = { query: {} }
  render()
  const backup = host.querySelector<HTMLButtonElement>('[aria-label="Back up now"]')!
  expect(backup.disabled).toBe(false)
  expect(backup.getAttribute('aria-disabled')).toBe('true')
  act(() => { backup.focus(); vi.advanceTimersByTime(350) })
  expect(document.activeElement).toBe(backup)
  expect(document.body.textContent).toContain(cap.reason)
  act(() => backup.click())
  expect(host.querySelector('[role="status"]')?.textContent).toBe(cap.reason)
  const switchover = [...host.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent === 'Switchover')!
  expect(switchover.disabled).toBe(false)
  expect(switchover.getAttribute('aria-disabled')).toBe('true')
  act(() => switchover.click())
  expect(host.querySelector('[role="status"]')?.textContent).toBe('No ready standby')
  state.caps.data.actions.switchover.allowed = true
  act(() => root.render(<CNPGClusterActions namespace="db" name="pg" />))
  expect(host.querySelector('[role="status"]')).toBeNull()
  menu()
  const dialog = document.querySelector('[role=dialog][aria-label="Cluster actions"]')!
  expect(dialog.textContent).toContain('No instances are fenced')
  expect(dialog.textContent).toContain('Needs create pods/exec')
  expect(dialog.textContent).toContain('Needs create clusters')
  expect(host.contains(dialog)).toBe(false)
  act(() => dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
  expect(host.querySelector('[aria-label="More cluster actions"]')?.getAttribute('aria-expanded')).toBe('false')
})

it('reviews each restart step with current readiness, scheduling blockers and primary downtime', () => {
  const caps = { facts: { currentPrimary: 'orders-1', instances: [{ pod: 'orders-1', ready: true, podReadable: true, podExists: true }, { pod: 'orders-2', ready: false, podReadable: true, podExists: false }] }, restartPlan: { primaryUpdateStrategy: 'unsupervised', primaryUpdateMethod: 'restart', steps: [{ instance: 'orders-2', role: 'standby', effect: 'recreate' }, { instance: 'orders-1', role: 'primary', effect: 'restart' }] } } as any
  const problem = { id: 'join', instance: 'orders-2', title: "New standby orders-2: Can't be scheduled", detail: '2 nodes insufficient pods', subject: { kind: 'Pod', name: 'orders-2-join' } } as any
  const html = renderToStaticMarkup(<CNPGRestartReview caps={caps} problems={[problem]} onOpenOverview={() => {}} />)
  for (const text of ['currently ready', 'currently absent', 'insufficient pods', 'Restarting the primary interrupts its connections.', 'The rolling restart waits for every instance to be ready.', 'Currently blocked by orders-2.', 'If the primary restarts without another ready instance, the cluster stops serving until it is back.']) expect(html).toContain(text)
  expect(html).toContain('See Overview’s problems →')
  caps.facts.instances[1].ready = true
  expect(renderToStaticMarkup(<CNPGRestartReview caps={caps} />)).not.toContain('cluster stops serving')
  caps.facts.instances[1].ready = false; caps.facts.instances[1].podReadable = false
  expect(renderToStaticMarkup(<CNPGRestartReview caps={caps} />)).toContain('service continuity is unknown')
  caps.restartPlan.steps[1].effect = 'wait_for_user'
  const supervised = renderToStaticMarkup(<CNPGRestartReview caps={caps} />)
  expect(supervised).toContain('primary waits for your manual promotion or restart')
  expect(supervised).not.toContain('Restarting the primary interrupts its connections.')
  caps.restartPlan.steps[1].effect = 'skipped_fenced'
  expect(renderToStaticMarkup(<CNPGRestartReview caps={caps} />)).toContain('fenced primary is skipped')
  caps.restartPlan.steps = [{ instance: 'orders-1', role: 'primary', effect: 'restart_only_instance' }]
  caps.facts.instances = [caps.facts.instances[0]]
  expect(renderToStaticMarkup(<CNPGRestartReview caps={caps} />)).toContain('There is no other instance: the cluster stops serving until the primary is back.')
})
it('disables header restore for known no sources and keeps unread sources enabled', () => {
  const cluster = { metadata: { name: 'pg', namespace: 'db' }, spec: {} }
  state.caps = { data: { actions: { restore: { allowed: true }, backup: { allowed: false }, switchover: { allowed: false }, psql: { allowed: false } }, facts: { maintenance: {} } } }
  state.workspace = { query: { data: { coverage: { backups: { state: 'full' } }, objects: { backups: [] } } }, fleet: { rows: [{ name: 'pg', namespace: 'db', cluster }] } }
  render(); menu()
  let restore = [...document.querySelectorAll<HTMLButtonElement>('[role=menu] button')].find((b) => b.textContent?.includes('Restore to a new cluster'))!
  expect(restore.disabled).toBe(true)
  expect(document.querySelector('[role=menu]')?.textContent).toContain('Nothing to restore from yet')
  state.workspace.query.data.coverage.backups.state = 'denied'
  act(() => root.render(<CNPGClusterActions namespace="db" name="pg" />))
  restore = [...document.querySelectorAll<HTMLButtonElement>('[role=menu] button')].find((b) => b.textContent?.includes('Restore to a new cluster'))!
  expect(restore.disabled).toBe(false)
  expect(document.querySelector('[role=menu]')?.textContent).not.toContain('Nothing to restore from yet')
  state.caps.isRefetchError = true
  state.caps.error = new Error('Refresh timeout')
  state.caps.dataUpdatedAt = Date.now() - 180_000
  act(() => root.render(<CNPGClusterActions namespace="db" name="pg" />))
  expect(host.textContent).toContain('Last refresh failed: Refresh timeout')
  expect(host.textContent).toContain('3m ago')
  expect(host.textContent).not.toContain('Actions could not be checked')
  expect(restore.disabled).toBe(false)
})

it('carries a verified join-Job blocker into the matching restart step and opens Overview', () => {
  const open = vi.fn()
  const caps = { facts: { currentPrimary: 'orders-1', instances: [{ pod: 'orders-2', ready: false, podReadable: true, podExists: false }] }, restartPlan: { steps: [{ instance: 'orders-2', role: 'standby', effect: 'recreate' }] } } as any
  const problem = { id: 'join', instance: 'orders-2', title: "New standby orders-2: Can't be scheduled", detail: 'Cannot be scheduled: both nodes have reached their Pod limit', subject: { kind: 'Pod', name: 'orders-2-join-dz4fc' } } as any
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<CNPGRestartReview caps={caps} problems={[problem]} onOpenOverview={open} />))
  const step = host.querySelector('li')!
  expect(step.textContent).toContain('orders-2 : Will recreate the standby Pod (currently absent)')
  expect(step.textContent).toContain('both nodes have reached their Pod limit')
  act(() => step.querySelector('button')!.click())
  expect(open).toHaveBeenCalledOnce()
})

it('states restart intentions separately from observations and folds the raw update policy', () => {
  const caps = { facts: { currentPrimary: 'orders-1', instances: [{ pod: 'orders-1', ready: true, podReadable: true, podExists: true }, { pod: 'orders-2', podReadable: true, podExists: false }] }, restartPlan: { primaryUpdateStrategy: 'unsupervised', primaryUpdateMethod: 'restart', steps: [{ instance: 'orders-2', role: 'standby', effect: 'recreate' }, { instance: 'orders-1', role: 'primary', effect: 'restart' }] } } as any
  const html = renderToStaticMarkup(<CNPGRestartReview caps={caps} />)
  expect(html).toContain('Will recreate the standby Pod')
  expect(html).toContain('(currently absent)')
  expect(html).toContain('Will restart the primary in place')
  expect(html).toContain('(currently ready)')
  expect(html).toContain('The operator updates the primary automatically after the standbys')
  expect(html).toContain('Operator update settings')
  expect(html).toContain('aria-expanded="false"')
})

it.each(['skipped_fenced', 'restart_only_instance', 'wait_for_user'])('explains the %s plan without promising a configured switchover', (effect) => {
  const caps = { facts: { currentPrimary: 'orders-1', instances: [{ pod: 'orders-1', ready: true, podReadable: true, podExists: true }] }, restartPlan: { primaryUpdateStrategy: 'unsupervised', primaryUpdateMethod: 'switchover', steps: [{ instance: 'orders-1', role: 'primary', effect }] } } as any
  const html = renderToStaticMarkup(<CNPGRestartReview caps={caps} />)
  expect(html).not.toContain('updates the primary automatically after the standbys')
  expect(html).not.toContain('it promotes an updated standby')
  expect(html).toContain(effect === 'skipped_fenced' ? 'The fenced primary is skipped.' : effect === 'restart_only_instance' ? 'There is no other instance' : 'The primary waits for your manual promotion or restart.')
})
it('explains an automatic switchover from the planned effect rather than the raw method', () => {
  const caps = { facts: { currentPrimary: 'orders-1', instances: [] }, restartPlan: { primaryUpdateStrategy: 'unsupervised', primaryUpdateMethod: 'restart', steps: [{ instance: 'orders-1', role: 'primary', effect: 'switchover' }] } } as any
  expect(renderToStaticMarkup(<CNPGRestartReview caps={caps} />)).toContain('it promotes an updated standby, then recreates the former primary Pod.')
})
