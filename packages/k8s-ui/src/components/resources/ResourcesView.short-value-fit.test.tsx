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
const services: APIResource = { group: '', version: 'v1', kind: 'Service', name: 'services', namespaced: true, isCrd: false, verbs: ['get', 'list'] }
const svc = (name: string, type: string) => ({ apiVersion: 'v1', kind: 'Service', metadata: { name, namespace: 'test', uid: name, creationTimestamp: '2026-01-01T00:00:00Z' }, spec: { type, ports: [{ port: 80 }] } })
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.restoreAllMocks(); localStorage.clear() })

async function typeWidth(room: number, type: string) {
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(room)
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const items = [svc('a', type), svc('b', type)]
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 40 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[services]}
    resourceCounts={{ Service: items.length }}
    selectedKindQuery={{ isLoading: false, data: items, resourceName: 'services', group: '' }}
    locationPathname="/resources/services"
    locationSearch=""
    onNavigate={() => {}}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  const index = [...host.querySelectorAll<HTMLElement>('thead th[data-column-key]')].findIndex(th => th.dataset.columnKey === 'type')
  const width = parseInt(host.querySelectorAll<HTMLTableColElement>('colgroup col')[index].style.width, 10)
  act(() => root.unmount()); host.remove()
  return width
}

it('sizes a short-value badge column to its longest badge, and lets it give way only to what it needs', async () => {
  const curated = getColumnMinWidth({ key: 'type', label: 'Type', width: 'w-28', sizing: 'fit' })
  // A LoadBalancer badge is wider than the curated width: with room it gets it.
  expect(await typeWidth(2000, 'LoadBalancer')).toBeGreaterThan(curated)
  // Short of room, a NodePort badge (narrower than the curated width) gives way
  // below it, to what the badge and the heading need.
  expect(await typeWidth(300, 'NodePort')).toBeLessThan(curated)
})
