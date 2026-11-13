import { describe, expect, it, vi } from 'vitest'

const calls = vi.hoisted(() => ({ urls: [] as string[], fail: false, container: 'job' }))
vi.mock('./client', async (orig) => ({
  ...(await orig<typeof import('./client')>()),
  fetchJSON: async (url: string) => {
    calls.urls.push(url)
    if (calls.fail) throw new Error('Loki is not connected')
    return {
      pods: [{ name: 'nightly-1-aaaaa', containers: ['job'], ready: false }],
      logs: [{ pod: 'nightly-1-aaaaa', container: calls.container, timestamp: '2026-11-13T02:00:00.000000000Z', content: 'done' }],
      notice: '', source: 'loki', truncated: true, limit: 5000,
      window: { start: '2026-11-13T01:59:00Z', end: '2026-11-13T02:11:00Z' },
    }
  },
}))

import { withLokiRunFallback } from './loki'

const gone = { pods: [], logs: [], emptyReason: 'pods-gone', emptyMessage: 'Its pods are no longer present' }

describe('withLokiRunFallback', () => {
  it('reads a run whose pods are gone from Loki', async () => {
    calls.urls = []
    const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true)
    expect(calls.urls).toEqual(['/loki/jobs/team/nightly-1/logs'])
    expect(got.logs.map(l => l.content)).toEqual(['done'])
    expect(got.pods.map(p => p.name)).toEqual(['nightly-1-aaaaa'])
    expect(got.notice).toContain('From Loki')
    expect(got.notice).toContain('newest 5,000 lines')
  })
  it('keeps the original answer when Loki is not available', async () => {
    calls.urls = []
    expect(await withLokiRunFallback('job', 'team', 'nightly-1', gone, false)).toBe(gone)
    expect(calls.urls).toEqual([])
  })
  it('only touches Jobs whose pods are gone', async () => {
    calls.urls = []
    const live = { pods: [{ name: 'p', containers: ['c'], ready: true }], logs: [] }
    expect(await withLokiRunFallback('job', 'team', 'nightly-1', live, true)).toBe(live)
    expect(await withLokiRunFallback('deployment', 'team', 'web', gone, true)).toBe(gone)
    expect(calls.urls).toEqual([])
  })
  it('keeps the original answer when Loki fails, and says so', async () => {
    calls.fail = true
    try {
      const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true)
      expect(got.emptyMessage).toBe(gone.emptyMessage)
      expect(got.notice).toBe("Couldn't read this run from Loki: Loki is not connected")
    } finally {
      calls.fail = false
    }
  })
  it('keeps to the chosen container', async () => {
    const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true, undefined, 'sidecar')
    expect(got.logs).toEqual([])
  })
  it('shows a run Loki can\'t split by container, whatever container is chosen', async () => {
    calls.container = '(all containers)'
    try {
      const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true, undefined, 'job')
      expect(got.logs.map(l => l.content)).toEqual(['done'])
      expect(got.notice).toContain('all containers (Loki has no container label)')
    } finally {
      calls.container = 'job'
    }
  })
})
