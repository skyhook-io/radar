// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { COLUMN_WIDTHS_VERSION, MIN_USER_COLUMN_WIDTH_PX, ResourcesView, columnSettingsKey, getColumnMinWidth, savedColumnWidths } from './ResourcesView'
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

it('keeps only Name from a record saved before columns sized themselves', () => {
  // What the first drag saved back then: every visible column, most at their defaults.
  localStorage.setItem('radar-columns-events', JSON.stringify({
    visible: ['name', 'namespace', 'type', 'reason', 'message', 'object', 'count', 'lastSeen'],
    widths: { name: 1064, namespace: 144, type: 96, message: 456, count: 80 },
  }))
  render([event(longName, 'Successfully pulled image "registry.example.com/team/app:1.2.3" in 2.1s')])
  expect(colWidth('name')).toBe('1064px')
  // The others size themselves again, the one dragged to 456 included.
  expect(colWidth('message')).not.toBe('456px')
  expect(colWidth('count')).toBe(`${getColumnMinWidth({ key: 'count', label: 'Count', width: 'w-20' })}px`)
  expect(savedColumnWidths({ name: 1064, message: 456, count: 80 }, true)).toEqual({ name: 1064 })
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

it('keeps a saved width no narrower than a user can drag a column', () => {
  localStorage.setItem('radar-columns-events', JSON.stringify({
    visible: ['name', 'namespace', 'type', 'reason', 'message', 'object', 'count', 'lastSeen'],
    widths: { name: 0.5 },
    widthsVersion: COLUMN_WIDTHS_VERSION,
  }))
  render([event(longName, 'hello')])
  expect(colWidth('name')).toBe(`${MIN_USER_COLUMN_WIDTH_PX}px`)
  expect(savedColumnWidths({ name: 0.5, count: 12 }, false)).toEqual({ name: MIN_USER_COLUMN_WIDTH_PX, count: MIN_USER_COLUMN_WIDTH_PX })
})

it('saves the dragged column when the last move and the release land in one batch', () => {
  render([event(longName, 'hello')])
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(() => ({ width: 300, height: 20, top: 0, left: 0, right: 300, bottom: 20, x: 0, y: 0, toJSON: () => ({}) }))
  const handle = () => host.querySelector<HTMLElement>('thead th[data-column-key="message"] .cursor-col-resize')!
  act(() => { handle().dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, clientX: 100 })) })
  act(() => {
    document.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, clientX: 200 }))
    document.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, clientX: 250 }))
    document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
  })
  expect(colWidth('message')).toBe('450px')
  expect(JSON.parse(localStorage.getItem('radar-columns-events')!).widths).toEqual({ message: 450 })
})

it('ignores saved widths that are not positive finite numbers', () => {
  const widths = JSON.parse('{"name":1e309,"message":-5,"object":"wide","count":90}')
  expect(savedColumnWidths(widths, false)).toEqual({ count: 90 })
  expect(savedColumnWidths({ ...widths, name: 0 }, true)).toEqual({})
  expect(savedColumnWidths(JSON.parse('{"name":1e309}'), true)).toEqual({})
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

it('lets a printer column that loads late size itself, though an older record saved a width for it', () => {
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1456)
  const widgets: APIResource = { group: 'example.com', version: 'v1', kind: 'Widget', name: 'widgets', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
  const widget = { apiVersion: 'example.com/v1', kind: 'Widget', metadata: { name: 'w1', namespace: 'test', uid: 'w1', creationTimestamp: '2026-01-01T00:00:00Z' }, spec: { description: 'A widget' } }
  const storageKey = 'radar-columns-' + columnSettingsKey('widgets', 'example.com')
  localStorage.setItem(storageKey, JSON.stringify({ visible: ['name', 'namespace', 'printer:Description', 'printer:Note', 'age'], widths: { name: 400, 'printer:Description': 160 } }))
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
  // Over to Widgets while its printer columns are still loading; the settings are saved meanwhile.
  const onWidgets = { selectedKindQuery: { isLoading: false, data: [widget], resourceName: 'widgets', group: 'example.com' }, locationPathname: '/resources/widgets', locationSearch: '?apiGroup=example.com' }
  act(() => root.render(view({ ...onWidgets, printerTable: null })))
  act(() => root.render(view({ ...onWidgets, namespaces: ['test', 'other'], printerTable: null })))
  expect(JSON.parse(localStorage.getItem(storageKey)!)).toMatchObject({ widthsVersion: COLUMN_WIDTHS_VERSION, widths: { name: 400 } })
  // Description holds a long value and Note a short one; both start at the same 160px.
  const printerTable = {
    kind: 'widgets', group: 'example.com',
    columns: [{ name: 'Description', type: 'string', jsonPath: '.spec.description' }, { name: 'Note', type: 'string', jsonPath: '.spec.note' }],
    cells: { w1: ['Replicates the widget catalogue from the primary region to every secondary region nightly', 'ok'] },
  }
  act(() => root.render(view({ ...onWidgets, namespaces: ['test', 'other'], printerTable })))
  // Description sizes itself from its value: well past the 160px saved for it,
  // and past Note, which had the same start and only a short value.
  expect(parseInt(colWidth('printer:Description'), 10)).toBeGreaterThan(parseInt(colWidth('printer:Note'), 10) + 100)
  expect(colWidth('name')).toBe('400px')
})
