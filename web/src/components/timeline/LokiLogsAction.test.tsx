import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import type { TimelineEvent } from '../../types'

const state = vi.hoisted(() => ({ ready: true, opened: [] as unknown[] }))
vi.mock('../../api/loki', () => ({ useLokiHistoryReady: () => state.ready }))
vi.mock('@skyhook-io/k8s-ui', async (orig) => ({ ...(await orig<typeof import('@skyhook-io/k8s-ui')>()), useOpenLogs: () => (o: unknown) => state.opened.push(o) }))

import { useLokiTimelineActions } from './LokiLogsAction'

const pod: TimelineEvent = { id: '1', timestamp: '2026-11-13T00:00:00Z', source: 'informer', kind: 'Pod', namespace: 'jobs', name: 'nightly-1-abcde', eventType: 'delete' }

function probe(item: TimelineEvent) {
  let out = ''
  function P() {
    const render = useLokiTimelineActions()
    out = render ? renderToStaticMarkup(<>{render(item)}</>) : 'none'
    return null
  }
  renderToStaticMarkup(<P />)
  return out
}

it('offers Loki logs on a Pod when History is available', () => {
  state.ready = true
  expect(probe(pod)).toContain('Logs (Loki)')
  expect(probe({ ...pod, kind: 'Deployment' })).toBe('')
})

it('offers nothing when History is unavailable', () => {
  state.ready = false
  expect(probe(pod)).toBe('none')
})
