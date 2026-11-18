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
const pod = (name: string, namespace: string, gpus = 0) => ({
  apiVersion: 'v1', kind: 'Pod',
  metadata: { name, namespace, uid: `${namespace}/${name}` },
  spec: { containers: [{ name: 'app', image: 'example', resources: gpus ? { limits: { 'nvidia.com/gpu': String(gpus) } } : {} }] },
  status: { phase: 'Running' },
})
let root: ReturnType<typeof createRoot> | undefined
let host: HTMLDivElement | undefined

afterEach(() => { act(() => root?.unmount()); host?.remove(); root = undefined; host = undefined; localStorage.clear() })

function render(namespaces: string[], items: any[]) {
  const view = <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 40 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={namespaces}
    apiResources={[pods]}
    resourceCounts={{ Pod: items.length }}
    selectedKindQuery={{ isLoading: false, data: items, resourceName: 'pods', group: '' }}
    locationPathname="/resources/pods"
    locationSearch=""
    onNavigate={() => {}}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>
  if (!root) {
    host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  }
  act(() => root!.render(view))
}
const headerKeys = () => [...host!.querySelectorAll<HTMLElement>('thead th[data-column-key]')].map(th => th.dataset.columnKey)

it('leaves Namespace out while one namespace is picked, without touching the saved choice', () => {
  render(['a', 'b'], [pod('p1', 'a'), pod('p2', 'b')])
  expect(headerKeys()).toContain('namespace')
  render(['a'], [pod('p1', 'a')])
  expect(headerKeys()).not.toContain('namespace')
  render([], [pod('p1', 'a'), pod('p2', 'b')])
  expect(headerKeys()).toContain('namespace')
  // Nothing about the scope was written into the saved column choice.
  const saved = localStorage.getItem('radar-columns-pods')
  expect(saved === null || JSON.parse(saved).visible.includes('namespace')).toBe(true)
})

it('shows GPU while a listed pod requests GPUs, hides it otherwise, and keeps a user’s hide', () => {
  render([], [pod('p1', 'a')])
  expect(headerKeys()).not.toContain('gpu')
  render([], [pod('p1', 'a'), pod('trainer', 'ml', 2)])
  expect(headerKeys()).toContain('gpu')
  render(['a'], [pod('p1', 'a')])
  expect(headerKeys()).not.toContain('gpu')

  // Hidden by the user: GPU pods no longer bring it back.
  render([], [pod('p1', 'a'), pod('trainer', 'ml', 2)])
  act(() => { host!.querySelector<HTMLButtonElement>('button[aria-label="Configure columns"]')!.click() })
  const gpuToggle = [...document.querySelectorAll<HTMLInputElement>('[role="region"] input[type="checkbox"]')]
    .find(input => input.closest('label, li, div')?.textContent?.includes('GPU'))!
  expect(gpuToggle.checked).toBe(true)
  act(() => { gpuToggle.click() })
  expect(headerKeys()).not.toContain('gpu')
  expect(JSON.parse(localStorage.getItem('radar-columns-pods')!).hidden).toEqual(['gpu'])

  // Turned back on while GPU pods are listed: the data decides again.
  act(() => { gpuToggleNow().click() })
  expect(headerKeys()).toContain('gpu')
  render(['a'], [pod('p1', 'a')])
  expect(headerKeys()).not.toContain('gpu')
})

const gpuToggleNow = () => [...document.querySelectorAll<HTMLInputElement>('[role="region"] input[type="checkbox"]')]
  .find(input => input.closest('label, li, div')?.textContent?.includes('GPU'))!

it('lets GPU follow the data again for settings saved by the old one-shot auto-show', () => {
  localStorage.setItem('radar-columns-pods', JSON.stringify({
    visible: ['name', 'namespace', 'containers', 'status', 'cpu', 'memory', 'restarts', 'gpu', 'node', 'age'],
    widths: {},
  }))
  render([], [pod('p1', 'a')])
  expect(headerKeys()).not.toContain('gpu')
  expect(headerKeys()).toContain('restarts')
})

it('keeps a pin on the GPU column across a reload', () => {
  localStorage.setItem('radar-columns-pods', JSON.stringify({
    visible: ['name', 'namespace', 'containers', 'status', 'cpu', 'memory', 'restarts', 'node', 'age'],
    widths: {}, hidden: [], pinned: ['gpu'], order: ['name', 'gpu', 'namespace', 'containers', 'status', 'cpu', 'memory', 'restarts', 'node', 'age'],
  }))
  render([], [pod('p1', 'a'), pod('trainer', 'ml', 2)])
  expect(headerKeys()).toContain('gpu')
  // Any change saves the record again; the pin must still be in it.
  act(() => { host!.querySelector<HTMLButtonElement>('button[aria-label="Configure columns"]')!.click() })
  const ageToggle = [...document.querySelectorAll<HTMLInputElement>('[role="region"] input[type="checkbox"]')]
    .find(input => input.closest('label, li, div')?.textContent?.trim().startsWith('Age'))!
  act(() => { ageToggle.click() })
  expect(JSON.parse(localStorage.getItem('radar-columns-pods')!).pinned).toContain('gpu')
})
