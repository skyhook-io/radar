// @vitest-environment jsdom
import { StrictMode, act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { TimelineEvent } from '../../types'
import { WorkloadView } from './WorkloadView'

vi.mock('../timeline/TimelineSwimlanes', () => ({ TimelineSwimlanes: () => null }))

const resource = { apiVersion: 'batch/v1', kind: 'CronJob', metadata: { name: 'nightly', namespace: 'demo', uid: 'cronjob-1' }, spec: { schedule: '0 * * * *' } }
const row = (id: string, minutesAgo: number): TimelineEvent => ({
  id, uid: 'cronjob-1', kind: 'CronJob', apiVersion: 'batch/v1', namespace: 'demo', name: 'nightly',
  source: 'k8s_event', eventType: 'Warning', reason: 'InvalidSchedule', message: `report ${id}`,
  timestamp: new Date(Date.now() - minutesAgo * 60_000).toISOString(),
})
const focus = { id: 'target' }
let root: Root
let container: HTMLDivElement
const scroll = vi.fn()
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  vi.stubGlobal('CSS', { escape: (id: string) => id })
  HTMLElement.prototype.scrollIntoView = scroll
  scroll.mockClear()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

async function render(events: TimelineEvent[], options: { truncated?: boolean; loadOlder?: () => void; loadingOlder?: boolean; error?: Error; target?: { id: string } } = {}) {
  await act(async () => root.render(<StrictMode><WorkloadView
    kind="cronjobs" namespace="demo" name="nightly" resource={resource} onBack={() => {}}
    expanded activeTab="timeline" allEvents={events} historyScoped timelineFocusEvent={options.target ?? focus}
    historyTruncated={options.truncated} onLoadOlderHistory={options.loadOlder}
    loadingOlderHistory={options.loadingOlder} olderHistoryError={options.error}
  /></StrictMode>))
}

it('scrolls to the exact report even among warnings with the same reason', async () => {
  await render([row('newest', 1), row('target', 5), row('oldest', 10)])
  const target = container.querySelector('[data-event-id="target"]')
  expect(target).not.toBeNull()
  expect(target?.textContent).toContain('report target')
  expect(target?.querySelector('.ring-2')).not.toBeNull()
  expect(scroll).toHaveBeenCalled()
  expect(container.textContent).not.toContain('not available')
})

it('loads older pages until the report appears, without duplicate requests in Strict Mode', async () => {
  const loadOlder = vi.fn()
  await render([row('newest', 1)], { truncated: true, loadOlder })
  expect(loadOlder).toHaveBeenCalledTimes(1)
  expect(container.textContent).toContain('Finding the reported error')
  await render([row('newest', 1), row('middle', 2)], { truncated: true, loadOlder })
  expect(loadOlder).toHaveBeenCalledTimes(2)
  await render([row('newest', 1), row('target', 5)], { truncated: true, loadOlder })
  expect(loadOlder).toHaveBeenCalledTimes(2)
  expect(container.querySelector('[data-event-id="target"] .ring-2')).not.toBeNull()
})

it('explains an absent report and stops when history is exhausted', async () => {
  const loadOlder = vi.fn()
  await render([row('newest', 1)], { loadOlder })
  expect(loadOlder).not.toHaveBeenCalled()
  expect(container.textContent).toContain('not available in the retained timeline history')
})

it('does not retry failed paging automatically', async () => {
  const loadOlder = vi.fn()
  await render([row('newest', 1)], { truncated: true, loadOlder, error: new Error('unavailable') })
  expect(loadOlder).not.toHaveBeenCalled()
  expect(container.textContent).toContain("some history couldn't be loaded")
})

it('lets the user select another event without a refresh forcing the original selection back', async () => {
  const events = [row('newest', 1), row('target', 5)]
  await render(events)
  await act(async () => container.querySelector<HTMLElement>('[data-event-id="newest"] .cursor-pointer')!.click())
  await render([...events, row('older', 10)])
  expect(container.querySelector('[data-event-id="newest"] .ring-2')).not.toBeNull()
  expect(container.querySelector('[data-event-id="target"] .ring-2')).toBeNull()
})

it('honors a new click on the same report', async () => {
  const events = [row('newest', 1), row('target', 5)]
  await render(events)
  await act(async () => container.querySelector<HTMLElement>('[data-event-id="newest"] .cursor-pointer')!.click())
  await render(events, { target: { id: 'target' } })
  expect(container.querySelector('[data-event-id="target"] .ring-2')).not.toBeNull()
})
