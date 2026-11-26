// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { TimelineEvent } from '../../types'
import { TimelineSwimlanes } from './TimelineSwimlanes'

const now = Date.now()
const events: TimelineEvent[] = Array.from({ length: 40 }, (_, i) => ({
  id: `ev-${i}`, timestamp: new Date(now - (i + 1) * 15_000).toISOString(), source: 'informer',
  kind: 'Deployment', namespace: 'shop', name: `app-${i}`, eventType: 'update',
}))

let host: HTMLDivElement
let root: Root
let scrolled: Element[]

beforeEach(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  scrolled = []
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver
  Element.prototype.scrollIntoView = vi.fn(function (this: Element) { scrolled.push(this) })
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})

afterEach(() => {
  act(() => root.unmount())
  host.remove()
})

it('scrolls the marker of an event opened from a link into view', () => {
  act(() => root.render(
    <TimelineSwimlanes events={events} grouping="flat" selectedEventId="ev-39" onSelectedEventChange={() => {}} />,
  ))
  expect(scrolled).toHaveLength(1)
  expect(scrolled[0].hasAttribute('data-selected-event')).toBe(true)
})

it('does not scroll when the user opens an event by clicking its marker', () => {
  act(() => root.render(
    <TimelineSwimlanes events={events} grouping="flat" selectedEventId={null} onSelectedEventChange={() => {}} />,
  ))
  const marker = host.querySelector<HTMLButtonElement>('button[aria-label^="● Modified"]')
  expect(marker).not.toBeNull()
  act(() => marker!.click())
  expect(host.querySelector('[data-selected-event]')).not.toBeNull()
  expect(scrolled).toHaveLength(0)
})
