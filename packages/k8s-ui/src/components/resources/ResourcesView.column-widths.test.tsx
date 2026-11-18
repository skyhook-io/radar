// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { COLUMN_WIDTHS_VERSION, ResourcesView, columnSettingsKey, getColumnHeaderWidth, getColumnMinWidth, withoutDefaultWidths } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
const events: APIResource = { group: '', version: 'v1', kind: 'Event', name: 'events', namespaced: true, isCrd: false, verbs: ['get', 'list'] }
const event = (name: string, message: string) => ({
  apiVersion: 'v1', kind: 'Event', type: 'Normal', reason: 'Pulled', message, count: 1,
  metadata: { name, namespace: 'test', uid: name, creationTimestamp: '2026-01-01T00:00:00Z' },
  involvedObject: { kind: 'Pod', name: 'web-1', namespace: 'test' },
})
const longName = 'checkout-service-7c9f8d6b5-very-long-generated-name.17f8a9b2c3d4e5f6'
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement

afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.restoreAllMocks(); localStorage.clear() })

function render(items: any[]) {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 40 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[events]}
    resourceCounts={{ Event: items.length }}
    selectedKindQuery={{ isLoading: false, data: items, resourceName: 'events', group: '' }}
    locationPathname="/resources/events"
    locationSearch=""
    onNavigate={() => {}}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
}

const colWidth = (key: string) => {
  const index = [...host.querySelectorAll<HTMLElement>('thead th[data-column-key]')].findIndex(th => th.dataset.columnKey === key)
  return host.querySelectorAll<HTMLTableColElement>('colgroup col')[index].style.width
}

it('gives every column an explicit width from its data', () => {
  render([event(longName, 'Successfully pulled image "registry.example.com/team/app:1.2.3" in 2.1s'), event('short', 'ok')])
  const cols = [...host.querySelectorAll<HTMLTableColElement>('colgroup col')]
  const headers = host.querySelectorAll('thead th[data-column-key]').length
  expect(cols).toHaveLength(headers)
  expect(cols.every(col => /^\d+px$/.test(col.style.width))).toBe(true)
  // The table is exactly as wide as its columns, so the browser re-shares nothing.
  const sum = cols.reduce((t, col) => t + parseInt(col.style.width, 10), 0)
  expect(host.querySelector<HTMLTableElement>('table')!.style.width).toBe(`${sum}px`)
  // Name fits its content: wider than its 200px floor, not the whole leftover.
  const name = parseInt(colWidth('name'), 10)
  expect(name).toBeGreaterThan(200)
  expect(name).toBeLessThanOrEqual(480)
  // Exact columns keep their curated width.
  expect(colWidth('count')).toBe(`${getColumnMinWidth({ key: 'count', label: 'Count', width: 'w-20' })}px`)
})

it('drops saved widths that are only a column’s default, keeping real drags and Name', () => {
  // What the first drag saved before columns sized themselves: every visible
  // column at its default width (to the sub-pixel), Name at whatever it had.
  const def = (key: string, label: string, width: string) => getColumnMinWidth({ key, label, width })
  localStorage.setItem('radar-columns-events', JSON.stringify({
    visible: ['name', 'namespace', 'type', 'reason', 'message', 'object', 'count', 'lastSeen'],
    widths: {
      name: 1064,
      namespace: def('namespace', 'Namespace', 'w-36') + 0.4,
      type: def('type', 'Type', 'w-24'),
      reason: def('reason', 'Reason', 'w-32') - 0.6,
      message: 456, // dragged
      object: def('object', 'Object', 'w-48'),
      count: def('count', 'Count', 'w-20'),
      lastSeen: def('lastSeen', 'Last Seen', 'w-28'),
    },
  }))
  render([event(longName, 'hello')])
  expect(colWidth('name')).toBe('1064px')
  expect(colWidth('message')).toBe('456px')
  expect(withoutDefaultWidths(JSON.parse(localStorage.getItem('radar-columns-events')!).widths, [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'type', label: 'Type', width: 'w-24' },
    { key: 'reason', label: 'Reason', width: 'w-32' },
    { key: 'message', label: 'Message', width: 'w-64' },
    { key: 'object', label: 'Object', width: 'w-48' },
    { key: 'count', label: 'Count', width: 'w-20' },
    { key: 'lastSeen', label: 'Last Seen', width: 'w-28' },
  ], true)).toEqual({ name: 1064, message: 456 })
})

it('keeps every width of a record saved since columns sized themselves, even one near the default', () => {
  const count = getColumnMinWidth({ key: 'count', label: 'Count', width: 'w-20' })
  localStorage.setItem('radar-columns-events', JSON.stringify({
    visible: ['name', 'namespace', 'type', 'reason', 'message', 'object', 'count', 'lastSeen'],
    widths: { count: count + 2 }, // a double-click fit, two pixels past the default
    widthsVersion: COLUMN_WIDTHS_VERSION,
  }))
  render([event(longName, 'hello')])
  expect(colWidth('count')).toBe(`${count + 2}px`)
})

it('drops a legacy width at the default a column had while its heading reserved filter room', () => {
  const containers = { key: 'containers', label: 'Containers', width: 'w-32' }
  // The default before: the declared width, or the heading with the filter button's 20px where wider.
  const before = Math.max(128, getColumnHeaderWidth(containers) + 20)
  // ...which is not today's default, or this would prove nothing.
  expect(before - getColumnMinWidth(containers)).toBeGreaterThan(4)
  expect(withoutDefaultWidths({ containers: before, name: 400 }, [{ key: 'name', label: 'Name' }, containers], true)).toEqual({ name: 400 })
  // A record saved since keeps it: that width is the user's.
  expect(withoutDefaultWidths({ containers: before }, [containers], false)).toEqual({ containers: before })
})

it('ignores saved widths that are not positive finite numbers', () => {
  const widths = JSON.parse('{"name":1e309,"message":-5,"object":"wide","count":90}')
  const columns = [{ key: 'name', label: 'Name' }, { key: 'message', label: 'Message' }, { key: 'object', label: 'Object' }, { key: 'count', label: 'Count' }]
  expect(withoutDefaultWidths(widths, columns, false)).toEqual({ count: 90 })
  expect(withoutDefaultWidths(widths, columns, true)).toEqual({ count: 90 })
})

it('saves only the dragged column, and Reset widths returns it to automatic sizing', () => {
  render([event(longName, 'hello')])
  const before = colWidth('name')
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(() => ({ width: 300, height: 20, top: 0, left: 0, right: 300, bottom: 20, x: 0, y: 0, toJSON: () => ({}) }))
  const handle = () => host.querySelector<HTMLElement>('thead th[data-column-key="message"] .cursor-col-resize')!
  act(() => { handle().dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, clientX: 100 })) })
  act(() => { document.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, clientX: 250 })) })
  act(() => { document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true })) })
  // The click a real drag ends with, which the table swallows so it doesn't sort.
  act(() => { handle().dispatchEvent(new MouseEvent('click', { bubbles: true })) })
  expect(colWidth('message')).toBe('450px')
  expect(colWidth('name')).toBe(before)
  expect(JSON.parse(localStorage.getItem('radar-columns-events')!).widths).toEqual({ message: 450 })

  act(() => { host.querySelector<HTMLButtonElement>('button[aria-label="Configure columns"]')!.click() })
  const reset = [...document.querySelectorAll('button')].find(b => b.textContent === 'Reset widths')!
  act(() => { reset.click() })
  expect(colWidth('message')).not.toBe('450px')
  expect(JSON.parse(localStorage.getItem('radar-columns-events')!).widths).toEqual({})
  expect([...document.querySelectorAll('button')].some(b => b.textContent === 'Reset widths')).toBe(false)
})

it('checks a legacy width against its column’s default once the column loads late', () => {
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1456)
  const widgets: APIResource = { group: 'example.com', version: 'v1', kind: 'Widget', name: 'widgets', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
  const widget = { apiVersion: 'example.com/v1', kind: 'Widget', metadata: { name: 'w1', namespace: 'test', uid: 'w1', creationTimestamp: '2026-01-01T00:00:00Z' }, spec: { description: 'A widget' } }
  const storageKey = 'radar-columns-' + columnSettingsKey('widgets', 'example.com')
  // Saved before columns sized themselves: Name as dragged, Description at its old default.
  localStorage.setItem(storageKey, JSON.stringify({ visible: ['name', 'namespace', 'printer:Description', 'age'], widths: { name: 400, 'printer:Description': 160 } }))
  const view = (props: Partial<React.ComponentProps<typeof ResourcesView>>) => <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 40 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[events, widgets]}
    resourceCounts={{ Event: 1, Widget: 1 }}
    selectedKindQuery={{ isLoading: false, data: [event(longName, 'hello')], resourceName: 'events', group: '' }}
    locationPathname="/resources/events"
    locationSearch=""
    onNavigate={() => {}}
    {...props}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(view({})))
  // Over to Widgets: its printer columns are still loading, and the settings are saved meanwhile.
  const onWidgets = { selectedKindQuery: { isLoading: false, data: [widget], resourceName: 'widgets', group: 'example.com' }, locationPathname: '/resources/widgets', locationSearch: '?apiGroup=example.com' }
  act(() => root.render(view({ ...onWidgets, printerTable: null })))
  act(() => root.render(view({ ...onWidgets, namespaces: ['test', 'other'], printerTable: null })))
  const midway = JSON.parse(localStorage.getItem(storageKey)!)
  expect(midway.widthsVersion).toBe(COLUMN_WIDTHS_VERSION)
  expect(midway.legacyWidths).toEqual(['printer:Description'])
  // Description arrives: its 160px is its old default, not a resize, so it sizes itself.
  const printerTable = { kind: 'widgets', group: 'example.com', columns: [{ name: 'Description', type: 'string', jsonPath: '.spec.description' }], cells: { w1: ['A widget'] } }
  act(() => root.render(view({ ...onWidgets, namespaces: ['test', 'other'], printerTable })))
  expect(parseInt(colWidth('printer:Description'), 10)).toBeGreaterThan(160)
  expect(colWidth('name')).toBe('400px')
  act(() => root.render(view({ ...onWidgets, namespaces: ['test'], printerTable })))
  expect(JSON.parse(localStorage.getItem(storageKey)!).widths).toEqual({ name: 400 })
})
