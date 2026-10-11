// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourcesSidebar } from './ResourcesSidebar'
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove() })
it('navigates Favorites, Views and grouped kinds in their visible order', () => {
  Element.prototype.scrollIntoView = vi.fn()
  const view = vi.fn(), kind = vi.fn()
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const cluster = { group: 'postgresql.cnpg.io', version: 'v1', kind: 'Cluster', name: 'clusters', namespaced: true, isCrd: true, verbs: ['list'] }
  act(() => root.render(<ResourcesSidebar selectedKind={null} onSelectedKindChange={kind} apiResources={[cluster]} resourceCounts={{ 'postgresql.cnpg.io/Cluster': 1 }} pinned={[{ name: 'pods', kind: 'Pod', group: '' }]} categoryWorkspaces={{ CloudNativePG: { destinations: [{ id: 'overview', label: 'Clusters view', onSelect: view }], defaultKindsCollapsed: true } }} />))
  const category = [...host.querySelectorAll('button')].find((b) => b.textContent?.includes('CloudNativePG'))!
  if (category.getAttribute('aria-expanded') !== 'true') act(() => category.click())
  const input = host.querySelector('input')!
  const key = (name: string) => act(() => input.dispatchEvent(new KeyboardEvent('keydown', { key: name, bubbles: true })))
  key('ArrowDown'); key('ArrowDown'); key('Enter')
  expect(view).toHaveBeenCalledTimes(1)
  expect(kind).not.toHaveBeenCalled()
  act(() => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Resource kinds')!.click())
  key('ArrowDown'); key('ArrowDown'); key('ArrowDown'); key('Enter')
  expect(kind).toHaveBeenCalledWith(expect.objectContaining({ name: 'clusters', group: 'postgresql.cnpg.io' }))
})
it('opens a matching View with Enter when search finds no kind', () => {
  Element.prototype.scrollIntoView = vi.fn()
  const view = vi.fn()
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<ResourcesSidebar selectedKind={null} onSelectedKindChange={() => {}} apiResources={[{ group: 'postgresql.cnpg.io', version: 'v1', kind: 'Cluster', name: 'clusters', namespaced: true, isCrd: true, verbs: ['list'] }]} resourceCounts={{ 'postgresql.cnpg.io/Cluster': 1 }} categoryWorkspaces={{ CloudNativePG: { destinations: [{ id: 'operator', label: 'Operator', onSelect: view }], defaultKindsCollapsed: true } }} />))
  const input = host.querySelector('input')!
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'operator')
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  expect(host.textContent).toContain('Operator')
  expect(host.textContent).not.toContain('>Cluster<')
  act(() => input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
  expect(view).toHaveBeenCalledTimes(1)
})
