// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourcesView, getColumnMinWidth } from './ResourcesView'
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

it('gives every column an explicit width from its data and leaves the rest to a trailing spacer', () => {
  render([event(longName, 'Successfully pulled image "registry.example.com/team/app:1.2.3" in 2.1s'), event('short', 'ok')])
  const cols = [...host.querySelectorAll<HTMLTableColElement>('colgroup col')]
  const headers = host.querySelectorAll('thead th[data-column-key]').length
  expect(cols).toHaveLength(headers + 1)
  expect(cols.slice(0, -1).every(col => /^\d+px$/.test(col.style.width))).toBe(true)
  expect(cols.at(-1)!.style.width).toBe('')
  // Name fits its content: wider than its 200px floor, not the whole leftover.
  const name = parseInt(colWidth('name'), 10)
  expect(name).toBeGreaterThan(200)
  expect(name).toBeLessThanOrEqual(480)
  // Exact columns keep their curated width.
  expect(colWidth('count')).toBe(`${getColumnMinWidth({ key: 'count', label: 'Count', width: 'w-20' })}px`)
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
