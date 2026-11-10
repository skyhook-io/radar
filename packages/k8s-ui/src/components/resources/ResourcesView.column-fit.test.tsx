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
const pods: APIResource = { group: '', version: 'v1', kind: 'Pod', name: 'pods', namespaced: true, isCrd: false, verbs: ['get', 'list'] }
const pod = (name: string) => ({ apiVersion: 'v1', kind: 'Pod', metadata: { name, namespace: 'test', uid: name }, spec: { containers: [{ name: 'app', image: 'example' }] }, status: { phase: 'Running' } })
const longName = 'checkout-service-7c9f8d6b5-very-long-generated-name'
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement

afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.restoreAllMocks(); localStorage.clear() })

it('fits a column to its widest content on a resize-handle double-click without changing the sort', () => {
  // jsdom has no layout: a laid-out cell reports 100px, an unconstrained (max-content) one 10px per character.
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const width = this.style.width === 'max-content' ? (this.textContent?.length ?? 0) * 10 : 100
    return { width, height: 20, top: 0, left: 0, right: width, bottom: 20, x: 0, y: 0, toJSON: () => ({}) }
  })
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 40 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[pods]}
    resourceCounts={{ Pod: 2 }}
    selectedKindQuery={{ isLoading: false, data: [pod('short'), pod(longName)], resourceName: 'pods', group: '' }}
    locationPathname="/resources/pods"
    locationSearch=""
    onNavigate={() => {}}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))

  const headers = [...host.querySelectorAll<HTMLElement>('thead th[data-column-key]')]
  const nameIndex = headers.findIndex(th => th.dataset.columnKey === 'name')
  expect(host.querySelectorAll(`td[data-column-key="name"]`).length).toBeGreaterThan(0)
  // The first mousedown switches the table to fixed widths, which remounts the header, so look it up per event.
  const nameHeader = () => host.querySelector<HTMLElement>('thead th[data-column-key="name"]')!
  const sortState = () => nameHeader().querySelector('svg')?.getAttribute('class')
  const sortBefore = sortState()

  const fire = (type: string) => act(() => { nameHeader().querySelector('.cursor-col-resize')!.dispatchEvent(new MouseEvent(type, { bubbles: true, cancelable: true, detail: type === 'dblclick' ? 2 : 1 })) })
  for (const type of ['mousedown', 'mouseup', 'click', 'mousedown', 'mouseup', 'click', 'dblclick']) fire(type)

  const nameCol = host.querySelectorAll<HTMLTableColElement>('colgroup col')[nameIndex]
  expect(nameCol.style.width).toBe(`${longName.length * 10}px`)
  // Every other column switches to a fixed width from the snapshot, as a drag would.
  const otherCol = host.querySelectorAll<HTMLTableColElement>('colgroup col')[nameIndex === 0 ? 1 : 0]
  expect(otherCol.style.width).toBe('100px')
  expect(sortState()).toBe(sortBefore)
})
