// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
const pods: APIResource = { group: '', version: 'v1', kind: 'Pod', name: 'pods', namespaced: true, isCrd: false, verbs: ['get', 'list'] }
const pod = (name: string) => ({ apiVersion: 'v1', kind: 'Pod', metadata: { name, namespace: 'test', uid: name }, spec: { containers: [{ name: 'app', image: 'example' }] }, status: { phase: 'Running' } })
const VIEWPORT = 2600
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement

beforeEach(() => {
  // jsdom has no layout: every element reports the table's viewport width, and
  // a cell measured unconstrained 10px per character.
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(VIEWPORT)
  vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(VIEWPORT)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const width = this.style.width === 'max-content' ? (this.textContent?.length ?? 0) * 10 : 100
    return { width, height: 20, top: 0, left: 0, right: width, bottom: 20, x: 0, y: 0, toJSON: () => ({}) }
  })
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
})
afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.restoreAllMocks(); localStorage.clear() })

function render(coveredRight?: number) {
  act(() => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 40 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={[]}
    apiResources={[pods]}
    resourceCounts={{ Pod: 2 }}
    selectedKindQuery={{ isLoading: false, data: [pod('short'), pod('checkout-service-7c9f8d6b5-x2v9q')], resourceName: 'pods', group: '' }}
    locationPathname="/resources/pods"
    locationSearch=""
    onNavigate={() => {}}
    coveredRight={coveredRight}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
}

function layout() {
  const keys = [...host.querySelectorAll<HTMLElement>('thead th[data-column-key]')].map(th => th.dataset.columnKey!)
  const cols = [...host.querySelectorAll<HTMLTableColElement>('colgroup col')].map(col => parseFloat(col.style.width))
  const table = host.querySelector<HTMLTableElement>('table')!
  return { keys, columns: cols.slice(0, keys.length), trailing: cols.slice(keys.length), tableWidth: parseFloat(table.style.width) }
}
const sum = (values: number[]) => values.reduce((a, b) => a + b, 0)

it('lays the columns out for the uncovered width and leaves the covered width as scroll room', () => {
  render()
  const full = layout()
  expect(full.trailing).toEqual([])
  expect(sum(full.columns)).toBe(VIEWPORT)
  expect(full.tableWidth).toBe(VIEWPORT)

  render(600)
  const beside = layout()
  expect(beside.keys).toEqual(full.keys)
  expect(sum(beside.columns)).toBe(VIEWPORT - 600)
  expect(beside.trailing).toEqual([600])
  // The table still exactly fills the scroller: nothing to scroll while the columns fit.
  expect(beside.tableWidth).toBe(VIEWPORT)

  // Columns that don't fit beside it give way, then scroll; the scroll room
  // lets the last column out from under the cover.
  render(VIEWPORT - 800)
  const tight = layout()
  expect(sum(tight.columns)).toBeGreaterThan(800)
  expect(tight.trailing).toEqual([VIEWPORT - 800])
  expect(tight.tableWidth).toBe(sum(tight.columns) + VIEWPORT - 800)

  // Closing the cover restores the full-width layout.
  render(0)
  expect(layout()).toEqual(full)
})

it('renders the same table with the prop absent or 0', () => {
  render()
  const absent = host.querySelector('table')!.outerHTML
  act(() => root.unmount())
  root = createRoot(host)
  render(0)
  expect(host.querySelector('table')!.outerHTML).toBe(absent)
})

it('ignores a cover that leaves the table no room', () => {
  render()
  const full = layout()
  render(VIEWPORT)
  expect(layout()).toEqual(full)
})
