// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()

// The font watcher and the glyph cache are module state, and this file swaps
// the canvas: it runs isolated, on modules of its own (resetModules below).
// The fallback face is twice as wide as the web font that replaces it.
let charPx = 14
HTMLCanvasElement.prototype.getContext = function () {
  return { font: '', measureText: (text: string) => ({ width: text.length * charPx }) }
} as any
let fontsReady!: () => void
Object.defineProperty(document, 'fonts', { configurable: true, value: { ready: new Promise<void>(resolve => { fontsReady = resolve }) } })

const events: APIResource = { group: '', version: 'v1', kind: 'Event', name: 'events', namespaced: true, isCrd: false, verbs: ['get', 'list'] }
const event = (name: string) => ({
  apiVersion: 'v1', kind: 'Event', type: 'Normal', reason: 'Pulled', message: 'ok', count: 1,
  metadata: { name, namespace: 'test', uid: name, creationTimestamp: '2026-01-01T00:00:00Z' },
  involvedObject: { kind: 'Pod', name: 'web-1', namespace: 'test' },
})
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); localStorage.clear() })

const nameWidth = () => {
  const index = [...host.querySelectorAll<HTMLElement>('thead th[data-column-key]')].findIndex(th => th.dataset.columnKey === 'name')
  return parseInt(host.querySelectorAll<HTMLTableColElement>('colgroup col')[index].style.width, 10)
}

it('drops widths measured in the fallback font once the web font arrives, though the rows are the same', async () => {
  vi.resetModules()
  const { ResourcesView } = await import('./ResourcesView')
  const { KeyboardShortcutProvider } = await import('../../hooks/useKeyboardShortcuts')
  const items = [event('web-1.17f8a9b2c3d4e5f6abcdef12')] // 30 characters
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  // Awaited so the deferred measurement render has run before the first check.
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 40 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[events]}
    resourceCounts={{ Event: 1 }}
    selectedKindQuery={{ isLoading: false, data: items, resourceName: 'events', group: '' }}
    locationPathname="/resources/events"
    locationSearch=""
    onNavigate={() => {}}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  // 30 × 14 + the copy button and padding.
  expect(nameWidth()).toBeGreaterThan(400)
  charPx = 7
  await act(async () => { fontsReady(); await Promise.resolve() })
  // Measured again in the web font (30 × 7 + chrome), and the fallback width is not kept.
  expect(nameWidth()).toBeLessThan(300)
})
