// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { TimelineEvent } from '../../types'
import { TimelineList, type TimelineListProps } from './TimelineList'
import { rowOffset } from './timeline-row-offset'

// Records the list's scrollToIndex calls. Virtuoso keeps re-aiming at that
// target while rows are measured, which fixed-height tests can't show.
const scrollToIndexCalls = vi.hoisted(() => [] as unknown[])
vi.mock('react-virtuoso', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-virtuoso')>()
  const React = await import('react')
  const Virtuoso = React.forwardRef((props: object, ref) => {
    const inner = React.useRef<Record<string, (...args: unknown[]) => unknown>>(null)
    React.useImperativeHandle(ref, () => new Proxy({}, {
      get: (_, key: string) => (...args: unknown[]) => {
        if (key === 'scrollToIndex') scrollToIndexCalls.push(args[0])
        return inner.current?.[key](...args)
      },
    }), [])
    return React.createElement(actual.Virtuoso as never, { ...props, ref: inner })
  })
  return { ...actual, Virtuoso }
})

const now = Date.now()
const events: TimelineEvent[] = Array.from({ length: 50_000 }, (_, index) => ({
  id: `event-${index}`,
  timestamp: new Date(now - index).toISOString(),
  source: 'informer', kind: 'Pod', namespace: 'default', name: `pod-${index}`, eventType: 'update',
}))
let container: HTMLDivElement
let root: Root
let originalScrollTo: PropertyDescriptor | undefined
const scrollToImpl = function (this: HTMLElement, options: ScrollToOptions) {
  this.scrollTop = options.top ?? this.scrollTop
  this.dispatchEvent(new Event('scroll'))
}
const scrollTo = vi.fn(scrollToImpl)

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
  scrollTo.mockImplementation(scrollToImpl)
  scrollToIndexCalls.length = 0
  vi.restoreAllMocks()
})

const render = (props: Partial<TimelineListProps> = {}) => act(async () => root.render(
  <VirtuosoMockContext.Provider value={{ viewportHeight: 250, itemHeight: 100 }}>
    <TimelineList events={events} isLoading={false} {...props} />
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
  await render()
  scrollTo.mockClear()
  // A refetch with a jump pending: the list is mounted and measured, then loads.
  await render({ isLoading: true, scrollToMs: now - 4000 })
  expect(container.textContent).toContain('Loading timeline')
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

it('jumps instantly to an externally selected offscreen event and highlights it', async () => {
  await render()
  expect(container.querySelector('[data-event-id="event-4000"]')).toBeNull()
  await render({ selectedEventId: 'event-4000' })
  const selected = container.querySelector('[data-event-id="event-4000"]')
  expect(selected).not.toBeNull()
  expect(selected?.querySelector('.ring-2')).not.toBeNull()
  expect(scrollTo).toHaveBeenCalledWith(expect.objectContaining({ behavior: 'auto' }))
  expect(scrollTo).not.toHaveBeenCalledWith(expect.objectContaining({ behavior: 'smooth' }))
})

it('scrolls smoothly to a selected card that is already rendered, unless motion is reduced', async () => {
  await render()
  await scroll(300)
  scrollTo.mockClear()
  await render({ selectedEventId: 'event-0' })
  expect(scrollTo).toHaveBeenCalledWith(expect.objectContaining({ behavior: 'smooth' }))
  scrollTo.mockClear()
  vi.stubGlobal('matchMedia', (query: string) => ({ matches: query.includes('reduce'), addEventListener() {}, removeEventListener() {} }))
  await render({ selectedEventId: 'event-1' })
  expect(scrollTo).toHaveBeenCalledWith(expect.objectContaining({ behavior: 'auto' }))
  vi.unstubAllGlobals()
})

it('keeps a fold expanded after its virtual row unmounts', async () => {
  const foldedEvents = [{ ...events[0], id: 'fold-newest', name: 'folded-pod' },
    { ...events[1], id: 'fold-older', name: 'folded-pod' }, ...events.slice(2, 400)]
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
    return <><output>{window?.fromMs}</output><TimelineList events={events} isLoading={false} onVisibleWindowChange={setWindow} /></>
  }
  await act(async () => root.render(
    <VirtuosoMockContext.Provider value={{ viewportHeight: 250, itemHeight: 100 }}><Host /></VirtuosoMockContext.Provider>,
  ))
  expect(container.querySelector('output')?.textContent).toBe(String(now - 1))
})

it('mounts every row of a short list and keeps them mounted while scrolling', async () => {
  const short = events.slice(0, 250)
  await render({ events: short })
  expect(container.querySelectorAll('[data-event-id]').length).toBe(250)
  await scroll(20_000)
  expect(container.querySelectorAll('[data-event-id]').length).toBe(250)
  // One header row plus 300 cards is past the threshold, so it windows, even
  // though the list was scrolled and every row was already mounted.
  await render({ events: events.slice(0, 300) })
  expect(container.querySelectorAll('[data-event-id]').length).toBeLessThan(20)
  await scroll(25_000)
  expect(container.querySelectorAll('[data-event-id]').length).toBeLessThan(20)
})

const newer = (count: number, from = 0) => Array.from({ length: count }, (_, index): TimelineEvent => ({
  ...events[0], id: `new-${from + index}`, name: `new-pod-${from + index}`, timestamp: new Date(now + 1000 * (from + count - index)).toISOString(),
}))

it('keeps the reader in place when events arrive above, and shows them at the very top', async () => {
  const list = events.slice(0, 400)
  await render({ events: list })
  await scroll(5000)
  expect(container.querySelector('[data-event-id="event-49"]')).not.toBeNull()
  await render({ events: [...newer(5), ...list] })
  // Five rows of 100px were inserted above, so the reader moves down with them.
  expect(scroller().scrollTop).toBe(5500)
  expect(container.querySelector('[data-event-id="event-49"]')).not.toBeNull()
  await scroll(0)
  scrollTo.mockClear()
  await render({ events: [...newer(3, 5), ...newer(5), ...list] })
  expect(scroller().scrollTop).toBe(0)
  expect(scrollTo).not.toHaveBeenCalled()
  expect(container.querySelector('[data-event-id="new-5"]')).not.toBeNull()
})

it('restores the reader with a plain scroll, never scrollToIndex, so their next scroll is not undone', async () => {
  const list = events.slice(0, 400)
  await render({ events: list })
  await scroll(5000)
  await render({ events: [...newer(5), ...list] })
  expect(scroller().scrollTop).toBe(5500)
  expect(scrollToIndexCalls).toEqual([])
})

it('keeps a scroll whose event has not arrived yet when events arrive above', async () => {
  const list = events.slice(0, 400)
  await render({ events: list })
  await scroll(5000)
  scroller().scrollTop = 5300
  await render({ events: [...newer(5), ...list] })
  expect(scroller().scrollTop).toBe(5800)
})

it('computes a row offset from size ranges', () => {
  const ranges = [{ startIndex: 0, endIndex: 2, size: 50 }, { startIndex: 3, endIndex: 3, size: 120 }, { startIndex: 4, endIndex: Infinity, size: 80 }]
  expect(rowOffset(ranges, 0)).toBe(0)
  expect(rowOffset(ranges, 3)).toBe(150)
  expect(rowOffset(ranges, 4)).toBe(270)
  expect(rowOffset(ranges, 6)).toBe(430)
  expect(rowOffset([], 6)).toBe(0)
})

it('keeps the reader in place when arrivals open a new time group above', async () => {
  const older = events.slice(0, 400).map(event => ({ ...event, timestamp: new Date(Date.parse(event.timestamp) - 10 * 60_000).toISOString() }))
  await render({ compact: false, events: older })
  await scroll(5000)
  await render({ compact: false, events: [...newer(5), ...older] })
  // A "Last 5 minutes" header and five cards: six rows.
  expect(scroller().scrollTop).toBe(5600)
  expect(container.textContent).not.toContain('Last 5 minutes')
})

it('jumps to a timestamp inside an open fold and mounts only nearby fold rows', async () => {
  const fold = events.slice(0, 600).map(event => ({ ...event, name: 'crash-looping-pod' }))
  await render({ events: fold, foldPerResource: () => true })
  await act(async () => [...container.querySelectorAll('button')].find(b => b.textContent?.includes('599 more'))!.click())
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 350)) })
  expect(container.querySelectorAll('[data-event-id]').length).toBeLessThan(20)
  await render({ events: fold, foldPerResource: () => true, scrollToMs: now - 300 })
  // Rows: the date header, the fold head, then its children from event-1.
  expect(scroller().scrollTop).toBe(301 * 100)
  const target = container.querySelector('[data-event-id="event-300"]')
  expect(target).not.toBeNull()
  expect(target?.getAttribute('data-ts-from')).toBe(String(now - 300))
})

it('closes a fold after its rows animate shut', async () => {
  const fold = events.slice(0, 3).map(event => ({ ...event, name: 'crash-looping-pod' }))
  await render({ events: fold, foldPerResource: () => true })
  const button = () => [...container.querySelectorAll('button')].find(b => b.textContent?.includes('2 more'))!
  await act(async () => button().click())
  expect(container.querySelector('[data-event-id="event-2"]')).not.toBeNull()
  await act(async () => button().click())
  expect(button().getAttribute('aria-expanded')).toBe('false')
  expect(container.querySelector('[data-event-id="event-2"]')).not.toBeNull()
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 350)) })
  expect(container.querySelector('[data-event-id="event-2"]')).toBeNull()
})

it('reports the lens only once the jumped-to rows render, never a stale or empty window', async () => {
  const onVisibleWindowChange = vi.fn()
  await render({ onVisibleWindowChange })
  onVisibleWindowChange.mockClear()
  // As in a browser, the scroll event follows the position change a frame later.
  scrollTo.mockImplementation(function (this: HTMLElement, options: ScrollToOptions) {
    this.scrollTop = options.top ?? this.scrollTop
    requestAnimationFrame(() => this.dispatchEvent(new Event('scroll')))
  })
  await render({ onVisibleWindowChange, scrollToMs: now - 4000 })
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
  expect(onVisibleWindowChange).toHaveBeenCalled()
  expect(onVisibleWindowChange).not.toHaveBeenCalledWith(null)
  for (const [window] of onVisibleWindowChange.mock.calls) expect(window.fromMs).toBeLessThanOrEqual(now - 3999)
  expect(onVisibleWindowChange).toHaveBeenLastCalledWith(expect.objectContaining({ toMs: now - 3999 }))
})

it('keeps focus in the list when the focused card scrolls out of the mounted range', async () => {
  await render()
  const button = container.querySelector<HTMLButtonElement>('[data-event-id="event-0"] button')!
  button.focus()
  expect(document.activeElement).toBe(button)
  await scroll(100_000)
  await act(async () => { await new Promise(resolve => requestAnimationFrame(resolve)) })
  expect(button.isConnected).toBe(false)
  expect(document.activeElement).toBe(scroller())
})

it('leaves focus alone once the user has moved it out of the list', async () => {
  await render()
  const button = container.querySelector<HTMLButtonElement>('[data-event-id="event-0"] button')!
  button.focus()
  button.blur()
  await act(async () => { await new Promise(resolve => requestAnimationFrame(resolve)) })
  await scroll(100_000)
  await act(async () => { await new Promise(resolve => requestAnimationFrame(resolve)) })
  expect(document.activeElement).toBe(document.body)
})

it('mounts every row in compact mode, whose hosts have no search box, however long the list', async () => {
  await render({ compact: true, events: events.slice(0, 400) })
  expect(container.querySelectorAll('[data-event-id]').length).toBe(400)
  await scroll(20_000)
  expect(container.querySelectorAll('[data-event-id]').length).toBe(400)
})

it('lists only the first rows of a fold while it opens, so later cards never show ahead of the reveal', async () => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  try {
    const fold = events.slice(0, 600).map(event => ({ ...event, name: 'crash-looping-pod' }))
    await render({ events: fold, foldPerResource: () => true })
    await act(async () => [...container.querySelectorAll('button')].find(b => b.textContent?.includes('599 more'))!.click())
    // The header, the fold head and its first twenty children.
    expect(container.querySelector('[data-event-id="event-20"]')).not.toBeNull()
    expect(container.querySelector('[data-event-id="event-21"]')).toBeNull()
    await act(async () => { vi.advanceTimersByTime(300) })
    await scroll(2100)
    expect(container.querySelector('[data-event-id="event-21"]')).not.toBeNull()
  } finally {
    vi.useRealTimers()
  }
})

it('reports no lens until a jump requested at mount has landed', async () => {
  const onVisibleWindowChange = vi.fn()
  await render({ onVisibleWindowChange, scrollToMs: now - 4000 })
  expect(onVisibleWindowChange).toHaveBeenCalled()
  expect(onVisibleWindowChange).not.toHaveBeenCalledWith(null)
  for (const [window] of onVisibleWindowChange.mock.calls) expect(window.fromMs).toBeLessThanOrEqual(now - 3999)
  // New data arriving together with a new target waits for that jump too.
  onVisibleWindowChange.mockClear()
  await render({ onVisibleWindowChange, events: [...newer(5), ...events], scrollToMs: now - 6000 })
  for (const [window] of onVisibleWindowChange.mock.calls) expect(window.fromMs).toBeLessThanOrEqual(now - 5999)
})

it('windows again when clearing a search grows the list, keeping the reader on their card', async () => {
  const list = events.slice(0, 1000)
  await render({ events: list, search: 'pod-1' })
  // event-1, event-10…19, event-100…199: short enough to mount in full.
  expect(container.querySelectorAll('[data-event-id]').length).toBe(111)
  // Row 30 at the top: the 30th match, event-118.
  await scroll(3000)
  await render({ events: list, search: '' })
  expect(container.querySelectorAll('[data-event-id]').length).toBeLessThan(20)
  // event-118 is row 119 of the full list, still at the top.
  expect(scroller().scrollTop).toBe(11_900)
  expect(container.querySelector('[data-event-id="event-118"]')).not.toBeNull()
  await scroll(20_000)
  expect(container.querySelectorAll('[data-event-id]').length).toBeLessThan(20)
})

it('keeps focus on the same card when its row is re-rendered on leaving full-mount mode', async () => {
  const list = events.slice(0, 1000)
  await render({ events: list, search: 'pod-1' })
  await scroll(3000)
  container.querySelector<HTMLButtonElement>('[data-event-id="event-118"] button')!.focus()
  await render({ events: list, search: '' })
  await act(async () => { await new Promise(resolve => requestAnimationFrame(resolve)) })
  expect(document.activeElement?.closest('[data-event-id]')?.getAttribute('data-event-id')).toBe('event-118')
})

it('holds a jump requested while a fold opens until all its rows are listed', async () => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  try {
    const fold = events.slice(0, 600).map(event => ({ ...event, name: 'crash-looping-pod' }))
    await render({ events: fold, foldPerResource: () => true })
    await act(async () => [...container.querySelectorAll('button')].find(b => b.textContent?.includes('599 more'))!.click())
    await render({ events: fold, foldPerResource: () => true, scrollToMs: now - 120 })
    expect(container.querySelector('[data-event-id="event-120"]')).toBeNull()
    await act(async () => { vi.advanceTimersByTime(300) })
    // Rows: the header, the fold head, then its children from event-1.
    expect(scroller().scrollTop).toBe(121 * 100)
    expect(container.querySelector('[data-event-id="event-120"]')).not.toBeNull()
  } finally {
    vi.useRealTimers()
  }
})
