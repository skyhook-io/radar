// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { TimelineEvent } from '../../types'
import { TimelineList, type TimelineListProps } from './TimelineList'

const now = Date.now()
const events: TimelineEvent[] = Array.from({ length: 50_000 }, (_, index) => ({
  id: `event-${index}`,
  timestamp: new Date(now - index).toISOString(),
  source: 'informer', kind: 'Pod', namespace: 'default', name: `pod-${index}`, eventType: 'update',
}))
let container: HTMLDivElement
let root: Root
let originalScrollTo: PropertyDescriptor | undefined
const scrollTo = vi.fn(function (this: HTMLElement, options: ScrollToOptions) {
  this.scrollTop = options.top ?? this.scrollTop
  this.dispatchEvent(new Event('scroll'))
})

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(250)
  vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(250)
  vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(5_000_100)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ top: 0, bottom: 250, height: 250 } as DOMRect)
  originalScrollTo = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollTo')
  Object.defineProperty(HTMLElement.prototype, 'scrollTo', { configurable: true, value: scrollTo })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})
afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  if (originalScrollTo) Object.defineProperty(HTMLElement.prototype, 'scrollTo', originalScrollTo)
  else Reflect.deleteProperty(HTMLElement.prototype, 'scrollTo')
  scrollTo.mockClear()
  vi.restoreAllMocks()
})

const render = (props: Partial<TimelineListProps> = {}) => act(async () => root.render(
  <VirtuosoMockContext.Provider value={{ viewportHeight: 250, itemHeight: 100 }}>
    <TimelineList compact events={events} isLoading={false} {...props} />
  </VirtuosoMockContext.Provider>,
))
const scroller = () => container.querySelector<HTMLElement>('[data-virtuoso-scroller]')!
const scroll = (top: number) => act(async () => {
  scroller().scrollTop = top
  scroller().dispatchEvent(new Event('scroll'))
  await new Promise(resolve => requestAnimationFrame(resolve))
})

it('bounds mounted cards with 50,000 events, including after scrolling', async () => {
  await render()
  expect(container.querySelectorAll('[data-event-id]').length).toBeGreaterThan(0)
  expect(container.querySelectorAll('[data-event-id]').length).toBeLessThan(20)
  await scroll(100_000)
  expect(container.querySelector('[data-event-id="event-0"]')).toBeNull()
  expect(container.querySelectorAll('[data-event-id]').length).toBeLessThan(20)
})

it('reports only viewport timestamps, excluding overscan, and clears the lens when empty', async () => {
  const onVisibleWindowChange = vi.fn()
  await render({ onVisibleWindowChange })
  expect(container.querySelectorAll('[data-event-id]').length).toBeGreaterThan(2)
  expect(onVisibleWindowChange).toHaveBeenLastCalledWith({ fromMs: now - 1, toMs: now })
  await scroll(100_000)
  expect(onVisibleWindowChange).toHaveBeenLastCalledWith({ fromMs: now - 1001, toMs: now - 998 })
  await render({ events: [], onVisibleWindowChange })
  expect(onVisibleWindowChange).toHaveBeenLastCalledWith(null)
})

it('jumps to an unmounted timestamp once and waits for loading to finish', async () => {
  await render({ isLoading: true, scrollToMs: now - 4000 })
  expect(scrollTo).not.toHaveBeenCalled()
  await render({ scrollToMs: now - 4000 })
  expect(scrollTo).toHaveBeenCalled()
  expect(container.querySelector('[data-event-id="event-4000"]')).not.toBeNull()
  scrollTo.mockClear()
  await scroll(0)
  await render({ scrollToMs: now - 4000 })
  expect(scrollTo).not.toHaveBeenCalled()
  await render({ scrollToMs: now - 6000 })
  expect(container.querySelector('[data-event-id="event-6000"]')).not.toBeNull()
})

it('scrolls an externally selected offscreen event into view and highlights it', async () => {
  await render()
  expect(container.querySelector('[data-event-id="event-4000"]')).toBeNull()
  await render({ selectedEventId: 'event-4000' })
  const selected = container.querySelector('[data-event-id="event-4000"]')
  expect(selected).not.toBeNull()
  expect(selected?.querySelector('.ring-2')).not.toBeNull()
  expect(scrollTo).toHaveBeenCalledWith(expect.objectContaining({ behavior: 'smooth' }))
})

it('keeps a fold expanded after its virtual row unmounts', async () => {
  const foldedEvents = [{ ...events[0], id: 'fold-newest', name: 'folded-pod' },
    { ...events[1], id: 'fold-older', name: 'folded-pod' }, ...events.slice(2, 100)]
  const foldPerResource = (event: TimelineEvent) => event.name === 'folded-pod'
  await render({ events: foldedEvents, foldPerResource })
  const button = () => [...container.querySelectorAll('button')].find(b => b.textContent?.includes('more on this Pod'))!
  await act(async () => button().click())
  expect(button().getAttribute('aria-expanded')).toBe('true')
  await scroll(5000)
  expect(container.querySelector('[data-event-id="fold-newest"]')).toBeNull()
  await scroll(0)
  expect(button().getAttribute('aria-expanded')).toBe('true')
  expect(container.querySelector('[data-event-id="fold-older"]')).not.toBeNull()
})

it('selects a previously folded event as its own highlighted card', async () => {
  const foldedEvents = events.slice(0, 3).map(event => ({ ...event, name: 'folded-pod' }))
  const foldPerResource = () => true
  await render({ events: foldedEvents, foldPerResource })
  expect(container.querySelector('[data-event-id="event-2"]')).toBeNull()
  await render({ events: foldedEvents, foldPerResource, selectedEventId: 'event-2' })
  expect(container.querySelector('[data-event-id="event-2"] .ring-2')).not.toBeNull()
})

it('keeps the full aggregate span in row attributes and the visible lens', async () => {
  const warnings = events.slice(0, 3).map(event => ({ ...event, name: 'api-1', eventType: 'Warning' as const, reason: 'BackOff' }))
  const onVisibleWindowChange = vi.fn()
  await render({ events: warnings, onVisibleWindowChange })
  const row = container.querySelector('[data-event-id="event-2"]')!
  expect(row.getAttribute('data-ts-from')).toBe(String(now - 2))
  expect(row.getAttribute('data-ts-to')).toBe(String(now))
  expect(container.querySelector('[aria-label="Expand occurrences"]')).not.toBeNull()
  expect(onVisibleWindowChange).toHaveBeenLastCalledWith({ fromMs: now - 2, toMs: now })
})

it('does not feed back indefinitely when the host stores the reported lens in state', async () => {
  function Host() {
    const [window, setWindow] = useState<{ fromMs: number; toMs: number } | null>(null)
    return <><output>{window?.fromMs}</output><TimelineList compact events={events} isLoading={false} onVisibleWindowChange={setWindow} /></>
  }
  await act(async () => root.render(
    <VirtuosoMockContext.Provider value={{ viewportHeight: 250, itemHeight: 100 }}><Host /></VirtuosoMockContext.Provider>,
  ))
  expect(container.querySelector('output')?.textContent).toBe(String(now - 1))
})
