// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { TimelineEvent } from '../../types'
import { TimelineList } from './TimelineList'

const NOW = Date.now()
const crash = (id: string, secondsAgo: number, over: Partial<TimelineEvent> = {}): TimelineEvent => ({
  id,
  timestamp: new Date(NOW - secondsAgo * 1000).toISOString(),
  source: 'informer',
  kind: 'Pod',
  namespace: 'default',
  name: 'api-1',
  eventType: 'update',
  healthState: 'unhealthy',
  ...over,
})
const ROWS = [crash('newest', 10), crash('older-1', 20), crash('older-2', 30)]
const foldAll = () => true

let container: HTMLDivElement
let root: Root
const scrolled = vi.fn()

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  if (!globalThis.CSS?.escape) Object.assign(globalThis, { CSS: { escape: (v: string) => v.replace(/[^\w-]/g, '\\$&') } })
  Element.prototype.scrollIntoView = function (this: Element) { scrolled(this.getAttribute('data-event-id')) }
  container = document.createElement('div')
  root = createRoot(container)
})
afterEach(() => {
  act(() => root.unmount())
  scrolled.mockReset()
})

const render = (events: TimelineEvent[], selectedEventId: string | null = null) =>
  act(async () => root.render(<TimelineList events={events} isLoading={false} foldPerResource={foldAll} selectedEventId={selectedEventId} />))
const foldButton = () => [...container.querySelectorAll('button')].find((b) => /more on this Pod/.test(b.textContent ?? ''))!
const isOpen = () => foldButton().getAttribute('aria-expanded') === 'true'
const click = () => act(async () => foldButton().click())

it('opens for a selection inside it, and a click still closes it', async () => {
  await render(ROWS, 'older-1')
  expect(isOpen()).toBe(true)
  expect(scrolled).toHaveBeenCalledWith('older-1')
  await click()
  expect(isOpen()).toBe(false)
})

it('is open on the same render that scrolls to a new selection inside it', async () => {
  await render(ROWS, 'older-1')
  await click()
  expect(isOpen()).toBe(false)
  scrolled.mockReset()
  await render(ROWS, 'older-2')
  expect(isOpen()).toBe(true)
  expect(scrolled).toHaveBeenCalledWith('older-2')
})

it('stays open when the user opened it and the selection lands on its newest card', async () => {
  await render(ROWS)
  await click()
  expect(isOpen()).toBe(true)
  await render(ROWS, 'newest')
  expect(isOpen()).toBe(true)
})

it('stays open as newer rows for the same resource arrive', async () => {
  await render(ROWS)
  await click()
  await render([crash('even-newer', 1), ...ROWS])
  expect(isOpen()).toBe(true)
  expect(foldButton().textContent).toContain('3 more on this Pod')
})
