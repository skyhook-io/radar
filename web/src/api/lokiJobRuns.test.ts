import { describe, expect, it, vi } from 'vitest'

const calls = vi.hoisted(() => ({ urls: [] as string[], fail: false, empty: false, container: 'job', notice: '', truncated: true, extra: [] as { pod: string, container: string, timestamp: string, content: string }[] }))
vi.mock('./client', async (orig) => ({
  ...(await orig<typeof import('./client')>()),
  fetchJSON: async (url: string) => {
    calls.urls.push(url)
    if (calls.fail) throw new Error('Loki is not connected')
    return {
      pods: [{ name: 'nightly-1-aaaaa', containers: [calls.container], ready: false }],
      logs: calls.empty ? [] : [{ pod: 'nightly-1-aaaaa', container: calls.container, timestamp: '2026-11-13T02:00:00.000000000Z', content: 'done' }, ...calls.extra],
      notice: calls.notice, source: 'loki', truncated: calls.truncated, limit: 5000,
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
    expect(got.notice).toMatch(/, the whole run \(the line count and time range don't apply\), the newest 5,000 lines \(the limit; older lines may be missing\)$/)
    expect(got.fixedRangeReason).toBeTruthy()
  })
  it("ends the summary before the server's own sentences", async () => {
    calls.notice = 'Selected by pod name; Loki has no Job label in this run\'s window.'
    try {
      const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true)
      expect(got.notice).toMatch(/older lines may be missing\)\. Selected by pod name; /)
    } finally {
      calls.notice = ''
    }
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
  it('keeps Kubernetes\' message when Loki has nothing for the run, and says Loki was asked', async () => {
    calls.empty = true
    try {
      const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true)
      expect(got.emptyMessage).toBe(gone.emptyMessage)
      expect(got.notice).toMatch(/^Loki has no lines for this run \(.+–.+\)\.$/)
    } finally {
      calls.empty = false
    }
  })
  it('keeps to the chosen container, asking Loki for it', async () => {
    calls.urls = []
    const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true, undefined, 'sidecar')
    expect(got.logs).toEqual([])
    expect(calls.urls).toEqual(['/loki/jobs/team/nightly-1/logs?container=sidecar'])
  })
  // A chosen container with no lines is Loki's answer for that container, not
  // the run's pods being gone: the viewer keeps the run's pods and containers.
  // The mock answer is truncated: an empty filtered read only covers the
  // newest entries Loki returned, so it must not claim the container is silent.
  it('keeps the limit in view when a truncated read has no lines for the container', async () => {
    const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true, undefined, 'sidecar')
    expect(got.emptyMessage).toBe('Loki has no lines from container sidecar among the newest 5,000 entries it returned for this run')
    expect(got.notice).toContain('the newest 5,000 lines (the limit; older lines may be missing)')
    expect(got.keepPreviousSources).toBe(true)
  })
  it('says the chosen container has no lines, keeping the run inventory', async () => {
    calls.truncated = false
    try {
      const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true, undefined, 'sidecar')
      expect(got.keepPreviousSources).toBe(true)
      expect(got.emptyReason).toBeUndefined()
      expect(got.emptyMessage).toBe('Loki has no lines from container sidecar for this run')
      expect(got.notice).not.toContain('newest')
      expect(got.fixedRangeReason).toBeTruthy()
    } finally {
      calls.truncated = true
    }
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
  it('keeps the whole-pod lines of a pod Loki can\'t split beside the chosen container', async () => {
    calls.extra = [{ pod: 'nightly-1-bbbbb', container: '(all containers)', timestamp: '2026-11-13T02:00:01.000000000Z', content: 'whole' }, { pod: 'nightly-1-aaaaa', container: 'other', timestamp: '2026-11-13T02:00:02.000000000Z', content: 'other' }]
    try {
      const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true, undefined, 'job')
      expect(got.logs.map(l => l.content)).toEqual(['done', 'whole'])
    } finally {
      calls.extra = []
    }
  })
  it('says on load that Loki has no container label, and offers no whole-pod container', async () => {
    calls.container = '(all containers)'
    try {
      const got = await withLokiRunFallback('job', 'team', 'nightly-1', gone, true)
      expect(got.notice).toContain('all containers (Loki has no container label)')
      expect(got.pods[0].containers).toEqual([])
      expect(got.logs.map(l => l.container)).toEqual([''])
    } finally {
      calls.container = 'job'
    }
  })
})
