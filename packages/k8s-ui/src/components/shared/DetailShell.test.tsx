// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { DetailShell } from './DetailShell'
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })
it('re-measures asynchronously loaded tab badges and keeps the active tab visible', () => {
  const observers = new Set<{ callback: () => void; elements: Element[] }>()
  vi.stubGlobal('ResizeObserver', class {
    record: { callback: () => void; elements: Element[] }
    constructor(callback: () => void) { this.record = { callback, elements: [] }; observers.add(this.record) }
    observe(element: Element) { this.record.elements.push(element) }
    disconnect() { observers.delete(this.record) }
  })
  const width = (el: HTMLElement): number => el.getAttribute('role') === 'tab' ? (el.classList.contains('px-3') ? 100 : 70) + (el.textContent?.includes('mark') ? 40 : 0) : 260
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function(this: HTMLElement) { return { width: width(this), height: 20, top: 0, bottom: 20, left: 0, right: width(this), x: 0, y: 0, toJSON: () => ({}) } })
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(() => 260)
  vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockImplementation(function(this: HTMLElement) { return [...this.children].reduce((sum, child) => sum + width(child as HTMLElement), 0) })
  vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockImplementation(function(this: HTMLElement) { return width(this) })
  vi.spyOn(HTMLElement.prototype, 'offsetLeft', 'get').mockImplementation(function(this: HTMLElement) { return this.getAttribute('role') === 'tab' ? [...this.parentElement!.children].slice(0, [...this.parentElement!.children].indexOf(this)).reduce((sum, child) => sum + width(child as HTMLElement), 0) : 0 })
  let setBadge!: (value: boolean) => void
  function Badge() { const [loaded, set] = useState(false); setBadge = set; return loaded ? <span>mark</span> : null }
  const tabs = [{ id: 'overview', label: 'Overview', icon: <span>icon</span>, badge: <Badge /> }, { id: 'replication', label: 'Replication', icon: <span>icon</span> }, { id: 'yaml', label: 'YAML', icon: <span>icon</span> }]
  const host = document.createElement('div'); const root = createRoot(host)
  act(() => root.render(<DetailShell identity="Cluster" tabs={tabs} activeTab="yaml" onTabChange={() => {}}>content</DetailShell>))
  const strip = host.querySelector<HTMLElement>('[role="tablist"]')!
  expect(host.textContent).not.toContain('icon')
  expect([...observers].some((o) => o.elements.includes(strip.children[0]))).toBe(true)
  act(() => setBadge(true))
  act(() => { for (const observer of [...observers]) observer.callback() })
  // The compact strip grows from 210 to 250 px, still fitting 260 px.
  expect(host.textContent).not.toContain('icon')
  // A second mark makes the compact strip overflow; the active YAML tab remains in view.
  act(() => { strip.children[1].appendChild(document.createTextNode('mark')); for (const observer of [...observers]) observer.callback() })
  const active = strip.querySelector<HTMLElement>('[aria-selected="true"]')!
  expect(strip.scrollLeft).toBeGreaterThan(0)
  expect(active.offsetLeft + active.offsetWidth).toBeLessThanOrEqual(strip.scrollLeft + strip.clientWidth)
  strip.scrollLeft = 0
  strip.dispatchEvent(new Event('scroll'))
  act(() => root.render(<DetailShell identity="Cluster" tabs={[...tabs]} activeTab="yaml" onTabChange={() => {}}>refreshed content</DetailShell>))
  expect(strip.scrollLeft).toBe(0)
  act(() => { for (const observer of [...observers]) observer.callback() })
  expect(strip.scrollLeft).toBe(0)
  act(() => root.unmount())
})
it('switches to compact spacing when badge content grows after the initial fit', () => {
  let callback = () => {}; let badgeLoaded = false
  vi.stubGlobal('ResizeObserver', class { constructor(cb: () => void) { callback = cb } observe() {} disconnect() {} })
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(() => 220)
  vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockImplementation(() => badgeLoaded ? 250 : 200)
  const host = document.createElement('div'); const root = createRoot(host)
  const tabs = [{ id: 'overview', label: 'Overview', icon: <span>icon</span> }, { id: 'yaml', label: 'YAML', icon: <span>icon</span>, badge: <span /> }]
  act(() => root.render(<DetailShell identity="Cluster" tabs={tabs} activeTab="yaml" onTabChange={() => {}}>content</DetailShell>))
  expect(host.textContent).toContain('icon')
  act(() => { badgeLoaded = true; callback() })
  expect(host.textContent).not.toContain('icon')
  act(() => root.unmount())
})
