// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
const nodes: APIResource = { group: '', version: 'v1', kind: 'Node', name: 'nodes', namespaced: false, isCrd: false, verbs: ['get', 'list'] }
const node = (i: number) => ({
  apiVersion: 'v1', kind: 'Node',
  metadata: { name: `ip-10-0-${i}-17.ec2.internal`, uid: `n${i}`, creationTimestamp: '2026-01-01T00:00:00Z', labels: { 'node-role.kubernetes.io/worker': '' } },
  spec: { taints: [{ key: 'dedicated', value: 'gpu', effect: 'NoSchedule' }] },
  status: { conditions: [{ type: 'Ready', status: 'True' }], nodeInfo: { kubeletVersion: 'v1.31.2' }, allocatable: { cpu: '4', memory: '16Gi', pods: '110' } },
})
let room = 1400
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement

afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.restoreAllMocks(); localStorage.clear() })

async function render() {
  room = 1400
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(() => room)
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const items = Array.from({ length: 6 }, (_, i) => node(i))
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 40 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={[]}
    apiResources={[nodes]}
    resourceCounts={{ Node: items.length }}
    selectedKindQuery={{ isLoading: false, data: items, resourceName: 'nodes', group: '' }}
    locationPathname="/resources/nodes"
    locationSearch=""
    onNavigate={() => {}}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
}
const headers = () => [...host.querySelectorAll<HTMLElement>('thead th[data-column-key]')].map(th => th.dataset.columnKey)
const tableWidth = () => parseInt(host.querySelector<HTMLTableElement>('table')!.style.width, 10)
async function resize(width: number) {
  room = width
  await act(async () => { window.dispatchEvent(new Event('resize')) })
}
const pickerRow = (key: string) => document.querySelector<HTMLElement>(`[data-column-row][data-column-key="${key}"]`)!

it('leaves low-priority columns out while the table is short of room, lowest first, and brings them back', async () => {
  await render()
  expect(headers()).toEqual(expect.arrayContaining(['taints', 'roles', 'version', 'pods', 'conditions']))
  // Short of room: Roles goes first (it reads "worker" on most nodes), then
  // Taints, Version and Pods, in that order; Conditions never drops.
  await resize(1000)
  expect(headers()).not.toContain('roles')
  expect(headers()).toContain('conditions')
  expect(tableWidth()).toBeLessThanOrEqual(1000)
  const order = ['roles', 'taints', 'version', 'pods']
  const dropped = order.filter(key => !headers().includes(key))
  expect(dropped).toEqual(order.slice(0, dropped.length))
  // At the widest room where anything is left out, it is Roles, alone
  // (found by halving between a room that drops and one that doesn't).
  let [drops, keeps] = [900, 1600]
  while (keeps - drops > 4) {
    const mid = Math.round((drops + keeps) / 2)
    await resize(mid)
    if (order.some(key => !headers().includes(key))) drops = mid
    else keeps = mid
  }
  await resize(drops)
  expect(order.filter(key => !headers().includes(key))).toEqual(['roles'])
  // Room again: every column is back.
  await resize(1600)
  expect(headers()).toEqual(expect.arrayContaining(['taints', 'roles', 'version', 'pods']))
})

it('shows a column hidden to fit in the picker, and keeps it once the user turns it on', async () => {
  await render()
  await resize(1000)
  expect(headers()).not.toContain('roles')
  act(() => { host.querySelector<HTMLButtonElement>('button[aria-label="Configure columns"]')!.click() })
  const row = pickerRow('roles')
  expect(row.textContent).toContain('Hidden to fit')
  const box = row.querySelector<HTMLInputElement>('input[type="checkbox"]')!
  expect(box.checked).toBe(false)
  await act(async () => { box.click() })
  expect(headers()).toContain('roles')
  expect(JSON.parse(localStorage.getItem('radar-columns-nodes')!).kept).toEqual(['roles'])
  // Turning it off hides it, as any column.
  await act(async () => { pickerRow('roles').querySelector<HTMLInputElement>('input[type="checkbox"]')!.click() })
  expect(headers()).not.toContain('roles')
  expect(JSON.parse(localStorage.getItem('radar-columns-nodes')!).kept).toEqual([])
  expect(JSON.parse(localStorage.getItem('radar-columns-nodes')!).visible).not.toContain('roles')
})

it('never leaves out a column the table is sorted by', async () => {
  await render()
  await resize(800)
  // Without a sort, this little room leaves Version out...
  expect(headers()).not.toContain('version')
  await resize(1400)
  await act(async () => { host.querySelector<HTMLElement>('thead th[data-column-key="version"]')!.click() })
  await resize(800)
  // ...but not while the rows are sorted by it.
  expect(headers()).toContain('version')
})
