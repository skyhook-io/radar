import { describe, expect, it } from 'vitest'
import type { CNPGRuntimeResponse } from '../../api/cnpg'
import type { CNPGClusterHistoryResponse } from '../../api/cnpg-history'
import { cacheHitSeries, chartedDatabases, historyLatest, latestRate, rateSeries, sampleFrom, sessionStateSeries, type Sample } from './trendSamples'

const sample = (t: number, metricsAt: number | undefined, over: Partial<Sample>, source = 'pg-1/uid-1'): Sample => ({
  t: t * 1000,
  metricsAt: metricsAt === undefined ? undefined : metricsAt * 1000,
  source,
  replayLag: {},
  ...over,
})

describe('rates between exporter generations', () => {
  it('uses the generation time and counts a cached reading once', () => {
    // Fetched at 0, 25 and 50 s; the exporter reran its queries at 0 and 50 s.
    const samples = [sample(0, 0, { archived: 100 }), sample(25, 0, { archived: 100 }), sample(50, 50, { archived: 150 })]
    expect(rateSeries(samples, 'archived', 60).dataPoints).toEqual([{ timestamp: 50, value: 60 }])
  })

  it('never takes a delta across a change of primary', () => {
    const samples = [sample(0, 0, { checkpointsTimed: 10 }, 'pg-1/a'), sample(30, 30, { checkpointsTimed: 1000 }, 'pg-2/b'), sample(60, 60, { checkpointsTimed: 1002 }, 'pg-2/b')]
    expect(rateSeries(samples, 'checkpointsTimed', 60).dataPoints).toEqual([
      { timestamp: 30, value: null },
      { timestamp: 60, value: 4 },
    ])
  })

  it('leaves a gap for a reset and for a sample without a source', () => {
    const samples = [sample(0, 0, { deadlocks: 5 }), sample(30, 30, { deadlocks: 2 }), sample(60, 60, { deadlocks: 3, source: undefined })]
    const pts = rateSeries(samples, 'deadlocks', 60).dataPoints
    expect(pts[0]).toEqual({ timestamp: 30, value: null })
    expect(pts[1].value).toBeNull()
  })

  it('computes cache hit ratio from deltas and never shows 100 % for no reads', () => {
    const samples = [sample(0, 0, { blksHit: 100, blksRead: 10 }), sample(30, 30, { blksHit: 190, blksRead: 20 }), sample(60, 60, { blksHit: 190, blksRead: 20 })]
    const pts = cacheHitSeries(samples).dataPoints
    expect(pts).toEqual([
      { timestamp: 30, value: 90 },
      { timestamp: 60, value: null },
    ])
  })

  it('gives the latest rate only from two generations of one source', () => {
    expect(latestRate([sample(0, 0, { commits: 10 }), sample(10, 0, { commits: 10 })], 'commits')).toBeUndefined()
    expect(latestRate([sample(0, 0, { commits: 10 }), sample(10, 10, { commits: 30 })], 'commits')).toBe(2)
  })
})

describe('sampleFrom', () => {
  const rt = (primaryMetrics: any, replicaMetrics: any = { state: 'ok', sessionsTotal: 1, sessionsByState: { active: 1 } }): CNPGRuntimeResponse => ({
    cluster: { namespace: 'db', name: 'pg', uid: 'u' },
    sampledAt: '2026-09-30T10:00:10Z',
    permission: { proxy: 'allowed' },
    instances: [
      { pod: 'pg-1', podUID: 'uid-1', role: 'primary', status: { state: 'ok' }, metrics: primaryMetrics },
      { pod: 'pg-2', podUID: 'uid-2', role: 'replica', status: { state: 'ok' }, metrics: replicaMetrics },
    ],
  })

  it('records the source Pod and the exporter generation, falling back to the fetch time as approximate', () => {
    const exact = sampleFrom(rt({ state: 'ok', capturedAt: '2026-09-30T10:00:09Z', lastUpdateTimestamp: 1790000000, xactCommitTotal: 5 }))
    expect(exact).toMatchObject({ source: 'pg-1/uid-1', metricsAt: 1790000000000, approximate: false, commits: 5 })
    const approx = sampleFrom(rt({ state: 'ok', capturedAt: '2026-09-30T10:00:09Z' }))
    expect(approx.metricsAt).toBe(Date.parse('2026-09-30T10:00:09Z'))
    expect(approx.approximate).toBe(true)
    expect(sampleFrom(rt({ state: 'denied' })).source).toBeUndefined()
  })

  it('records sessions for every instance that answered, and none for one that did not', () => {
    const s = sampleFrom(rt({ state: 'ok', sessionsTotal: 0 }, { state: 'unreachable' }))
    expect(s.instances).toEqual({ 'pg-1': { byState: {}, total: 0, waiting: undefined } })
  })

  it('keeps lock waits when the session-count query is missing, and leaves session states a gap', () => {
    const s = sampleFrom(rt({ state: 'partial', waitingBackends: 3 }, { state: 'unreachable' }))
    expect(s.instances?.['pg-1']).toEqual({ byState: undefined, total: undefined, waiting: 3 })
    const states = sessionStateSeries([s], 'pg-1')
    expect(states.every((series) => series.dataPoints[0].value === null)).toBe(true)
  })
})

describe('sessionStateSeries', () => {
  it('groups states and gaps a sample where the instance did not answer', () => {
    const samples = [
      sample(0, 0, { instances: { 'pg-2': { byState: { active: 2, 'idle in transaction': 1, 'idle in transaction (aborted)': 1, idle: 5, 'fastpath function call': 1 }, total: 10 } } }),
      sample(5, 5, { instances: {} }),
    ]
    const series = sessionStateSeries(samples, 'pg-2')
    expect(series.map((x) => [x.labels.series, x.dataPoints[0].value])).toEqual([
      ['active', 2],
      ['idle', 5],
      ['idle in transaction', 2],
      ['other', 1],
    ])
    expect(series.every((x) => x.dataPoints[1].value === null)).toBe(true)
  })
})

describe('chartedDatabases', () => {
  const samples = [
    sample(0, 0, { dbSizes: { a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, tiny: 0.5 } }),
    sample(5, 5, { dbSizes: { a: 1, b: 2, c: 3, d: 4, e: 5, f: 6 } }),
  ]
  it('defaults to the five largest and follows any database the viewer picks', () => {
    expect(chartedDatabases(samples, null).shown).toEqual(['f', 'e', 'd', 'c', 'b'])
    expect(chartedDatabases(samples, ['tiny', 'a']).shown).toEqual(['a', 'tiny'])
    expect(chartedDatabases(samples, 'all').shown).toHaveLength(7)
  })
})

describe('historyLatest', () => {
  const history = (over: Partial<CNPGClusterHistoryResponse> = {}, chartState = 'ok') =>
    ({
      source: 'prometheus',
      state: 'ok',
      charts: [
        {
          id: 'tps',
          seriesBy: 'series',
          state: chartState,
          source: 'rate of xact_commit',
          series: [{ labels: { series: 'commits' }, dataPoints: [{ timestamp: 1, value: 4 }, { timestamp: 2, value: 7.5 }, { timestamp: 3, value: null }] }],
        },
      ],
      ...over,
    }) as unknown as CNPGClusterHistoryResponse

  it('takes the newest recorded point of the named series', () => {
    expect(historyLatest(history({ end: new Date(3 * 1000).toISOString(), stepSeconds: 1 }), 'tps', 'commits')).toEqual({
      value: 7.5,
      at: 2,
      source: 'rate of xact_commit',
      stale: false,
    })
    expect(historyLatest(history(), 'tps', 'rollbacks')).toBeUndefined()
  })
  it('marks a value stale when the series stopped more than two steps before the end', () => {
    const got = historyLatest(history({ end: new Date(600 * 1000).toISOString(), stepSeconds: 60 }), 'tps', 'commits')
    expect(got).toMatchObject({ value: 7.5, stale: true })
  })
  it('has nothing without Prometheus history', () => {
    expect(historyLatest(history({ source: 'none' }), 'tps', 'commits')).toBeUndefined()
    expect(historyLatest(history({}, 'noSeries'), 'tps', 'commits')).toBeUndefined()
    expect(historyLatest(undefined, 'tps', 'commits')).toBeUndefined()
  })
})
