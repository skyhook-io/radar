// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'
import type { TimelineEvent } from '../../types'
import { TimelineList } from './TimelineList'

const deleted: TimelineEvent = { id: 'd', timestamp: new Date().toISOString(), source: 'informer', kind: 'Pod', namespace: 'jobs', name: 'nightly-29905990-abcde', eventType: 'delete' }

let container: HTMLDivElement
let root: Root
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  container = document.createElement('div')
  root = createRoot(container)
})
afterEach(() => { act(() => root.unmount()) })

it('renders host actions for an event', async () => {
  await act(async () => root.render(<TimelineList events={[deleted]} isLoading={false} showDeleted renderItemActions={(item) => <button type="button">Logs for {item.name}</button>} />))
  expect(container.textContent).toContain('Logs for nightly-29905990-abcde')
})

it('renders no actions when the host gives none', async () => {
  await act(async () => root.render(<TimelineList events={[deleted]} isLoading={false} showDeleted />))
  expect(container.textContent).toContain('nightly-29905990-abcde')
  expect(container.textContent).not.toContain('Logs for')
})
