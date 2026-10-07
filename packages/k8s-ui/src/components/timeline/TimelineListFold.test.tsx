// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'
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

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  container = document.createElement('div')
  root = createRoot(container)
})
afterEach(() => {
  act(() => root.unmount())
})

const render = (events: TimelineEvent[]) =>
  act(async () => root.render(<TimelineList events={events} isLoading={false} foldPerResource={foldAll} />))
const foldButton = () => [...container.querySelectorAll('button')].find((b) => /more on this Pod/.test(b.textContent ?? ''))!
const isOpen = () => foldButton().getAttribute('aria-expanded') === 'true'
const click = () => act(async () => foldButton().click())

it('starts closed and toggles on click', async () => {
  await render(ROWS)
  expect(isOpen()).toBe(false)
  await click()
  expect(isOpen()).toBe(true)
  await click()
  expect(isOpen()).toBe(false)
})

it('stays open as newer rows for the same resource arrive', async () => {
  await render(ROWS)
  await click()
  await render([crash('even-newer', 1), ...ROWS])
  expect(isOpen()).toBe(true)
  expect(foldButton().textContent).toContain('3 more on this Pod')
})
