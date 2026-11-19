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
const nodes: APIResource = { group: '', version: 'v1', kind: 'Node', name: 'nodes', namespaced: false, isCrd: false, verbs: ['get', 'list'] }
// Long generated node names (Name holds on to 320px before the table scrolls)
// and GPU nodes (the GPU column shows): together a little wider than 1456px.
const node = (i: number) => ({
  apiVersion: 'v1', kind: 'Node',
  metadata: { name: `ip-10-0-${i}-17.ec2.internal-pool-general-purpose-amd64-${i}`, uid: `n${i}`, creationTimestamp: '2026-01-01T00:00:00Z', labels: { team: 'platform-infrastructure-and-reliability' } },
  status: { conditions: [{ type: 'Ready', status: 'True' }], nodeInfo: { kubeletVersion: 'v1.31.2' }, allocatable: { cpu: '4', memory: '16Gi', pods: '110', 'nvidia.com/gpu': '8' }, capacity: { 'nvidia.com/gpu': '8' } },
})
// The table's room in a maximized 1920px window with Radar's default sidebars.
const ROOM = 1456
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
const innerWidth = window.innerWidth

beforeEach(() => {
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(ROOM)
})
afterEach(() => {
  act(() => root?.unmount()); host?.remove(); vi.restoreAllMocks(); localStorage.clear()
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: innerWidth })
})

async function renderAt(windowWidth: number) {
  // Every low-priority column kept, as a user who turned them on would have
  // them: the table can't make room by leaving columns out.
  localStorage.setItem('radar-columns-nodes', JSON.stringify({
    visible: ['name', 'status', 'roles', 'cpu', 'memory', 'pods', 'conditions', 'taints', 'version', 'age', 'label:team'],
    hidden: [], kept: ['roles', 'pods', 'taints', 'version'], widthsVersion: 2,
    // A label column too, so the default columns plus it are wider than the room.
    custom: [{ source: 'label', path: 'team' }],
  }))
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: windowWidth })
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const items = Array.from({ length: 12 }, (_, i) => node(i))
  // Awaited so the deferred measurement render has run.
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 40 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={[]}
    apiResources={[nodes]}
    resourceCounts={{ Node: items.length }}
    selectedKindQuery={{ isLoading: false, data: items, resourceName: 'nodes', group: '' }}
    locationPathname="/resources/nodes"
    locationSearch=""
    onNavigate={() => {}}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  return parseInt(host.querySelector<HTMLTableElement>('table')!.style.width, 10)
}

it('applies the rule as the window crosses 1920px, without remounting', async () => {
  // The table's room stays 1456px; only the window changes.
  expect(await renderAt(1919)).toBeGreaterThan(ROOM)
  await act(async () => {
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 1920 })
    window.dispatchEvent(new Event('resize'))
  })
  expect(parseInt(host.querySelector<HTMLTableElement>('table')!.style.width, 10)).toBeLessThanOrEqual(ROOM)
})

it('does not scroll sideways in a 1920px window, where a narrower one would', async () => {
  // Below 1920 Nodes' columns keep their content and Name its protected
  // width, so the table is wider than its room...
  expect(await renderAt(1680)).toBeGreaterThan(ROOM)
  act(() => root.unmount()); host.remove()
  // ...and from 1920 up the columns give way to their headings until it fits.
  expect(await renderAt(1920)).toBeLessThanOrEqual(ROOM)
})
