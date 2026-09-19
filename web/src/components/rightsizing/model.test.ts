import { describe, expect, it } from 'vitest'
import type { RightsizingScanResponse, RightsizingRow } from '../../api/client'
import {
  calculateImpact,
  classifyRows,
  flattenScanResults,
  isActionableClass,
  scanClassCounts,
} from './model'

function metric(overrides: Partial<RightsizingRow> = {}): RightsizingRow {
  return {
    container: 'app',
    resource: 'cpu',
    fit: 'balanced',
    confidence: 'high',
    sampleCount: 2017,
    expectedSamples: 2017,
    coverage: 1,
    hpaManaged: false,
    hpaEvidenceAvailable: true,
    oomEvidenceAvailable: true,
    ...overrides,
  }
}

function response(
  rows: RightsizingRow[],
  replicas = 1,
  namespace = 'shop',
  scaledToZero = false,
): RightsizingScanResponse {
  return {
    state: 'complete',
    scannedAt: '2026-07-12T10:00:00Z',
    window: '7d',
    source: 'radar',
    coverage: {
      workloadsDiscovered: 1,
      workloadsEvaluated: 1,
      workloadsWithData: 1,
      batches: 1,
      completedBatches: 1,
    },
    workloads: [
      {
        kind: 'Deployment',
        namespace,
        name: 'api',
        replicas,
        scaledToZero,
        rows,
      },
    ],
  }
}

describe('rightsizing scan model', () => {
  it('prioritizes reliability guidance and explicit safety review', () => {
    expect(
      classifyRows([
        metric({
          fit: 'oversized',
          currentRequestValue: 0.1,
          recommendedRequestValue: 0.02,
          recommendedRequest: '20m',
        }),
        metric({
          resource: 'memory',
          fit: 'missing_request',
          recommendedRequestValue: 64 * 1024 * 1024,
          recommendedRequest: '64Mi',
        }),
      ]),
    ).toBe('increase')
    expect(
      classifyRows([
        metric({
          fit: 'oversized',
          hpaManaged: true,
          recommendationReason: 'hpa_managed',
        }),
      ]),
    ).toBe('review')
    expect(
      classifyRows([
        metric({
          fit: 'oversized',
          recommendationReason: 'hpa_evidence_unavailable',
        }),
      ]),
    ).toBe('review')
    expect(
      classifyRows([
        metric({
          fit: 'oversized',
          currentRequestValue: 0.2,
          recommendedRequestValue: 0.1,
          recommendedRequest: '100m',
          throttleRatio: 0.1,
        }),
      ]),
    ).toBe('review')
    expect(
      classifyRows([
        metric({
          fit: 'oversized',
          currentRequestValue: 0.2,
          recommendedRequestValue: 0.1,
          recommendedRequest: '100m',
          bursty: true,
        }),
      ]),
    ).toBe('review')
  })

  it('suppresses reductions that are not meaningful across replicas', () => {
    const tiny = metric({
      fit: 'oversized',
      currentRequestValue: 0.005,
      recommendedRequestValue: 0.001,
      recommendedRequest: '1m',
    })
    const meaningful = metric({
      fit: 'oversized',
      currentRequestValue: 0.1,
      recommendedRequestValue: 0.02,
      recommendedRequest: '20m',
    })
    expect(classifyRows([tiny], 9)).toBe('in_range')
    expect(classifyRows([meaningful], 1)).toBe('reduction')
  })

  it('excludes recommendations that require manual review from aggregate impact', () => {
    const cpu = metric({
      fit: 'oversized',
      currentRequestValue: 0.1,
      recommendedRequestValue: 0.05,
      recommendedRequest: '50m',
      throttleRatio: 0.2,
    })
    const memory = metric({
      resource: 'memory',
      fit: 'oversized',
      currentRequestValue: 128 * 1024 * 1024,
      recommendedRequestValue: 64 * 1024 * 1024,
      recommendedRequest: '64Mi',
    })
    expect(calculateImpact([cpu, memory], 3)).toEqual({
      replicas: 3,
      cpuChange: 0,
      memoryChange: -192 * 1024 * 1024,
    })
  })

  it('keeps incomplete history out of no-change results', () => {
    expect(classifyRows([metric({ fit: 'insufficient_history' })])).toBe('need_data')
    expect(classifyRows([metric({ queryError: 'query failed' })])).toBe('need_data')
  })

  describe.each([
    ['failed query', { queryError: 'usage query failed' }],
    ['short history', { fit: 'insufficient_history' as const }],
  ])('with %s on one resource', (_label, missing) => {
    it.each([
      [
        'increase',
        {
          fit: 'under_requested' as const,
          recommendedRequest: '1Gi',
          recommendedRequestValue: 1024 ** 3,
        },
      ],
      ['review', { currentPodOOM: true }],
      ['review', { hpaManaged: true, recommendationReason: 'hpa_managed' }],
      [
        'reduction',
        {
          fit: 'oversized' as const,
          currentRequestValue: 1024 ** 3,
          recommendedRequest: '512Mi',
          recommendedRequestValue: 512 * 1024 ** 2,
        },
      ],
      ['need_data', {}],
    ])('retains %s from the other resource', (classification, known) => {
      const cpu = metric(missing)
      const memory = metric({ resource: 'memory', ...known })
      const rows = flattenScanResults(response([cpu, memory]))
      expect(rows).toHaveLength(1)
      expect(rows[0].classification).toBe(classification)
      expect(rows[0].cpu).toBe(cpu)
      expect(rows[0].memory).toBe(memory)
      expect(scanClassCounts(rows)[rows[0].classification]).toBe(1)
      const actions = rows.filter((row) => isActionableClass(row.classification))
      expect(actions).toHaveLength(classification === 'need_data' ? 0 : 1)
    })
  })

  it('retains a known CPU increase when memory has no evidence', () => {
    const cpu = metric({
      fit: 'under_requested',
      recommendedRequest: '200m',
      recommendedRequestValue: 0.2,
      currentRequestValue: 0.1,
    })
    const memory = metric({ resource: 'memory', fit: 'insufficient_history' })
    const [row] = flattenScanResults(response([cpu, memory], 3))
    expect(row.classification).toBe('increase')
    expect(row.impact.cpuChange).toBeCloseTo(0.3)
    expect(row.impact.memoryChange).toBe(0)
    expect(row.memory).toBe(memory)
  })

  it('keeps empty and entirely unevidenced containers in need_data', () => {
    expect(classifyRows([])).toBe('need_data')
    expect(
      classifyRows([
        metric({ queryError: 'usage query failed' }),
        metric({ resource: 'memory', fit: 'insufficient_history' }),
      ]),
    ).toBe('need_data')
    expect(
      classifyRows(
        [metric({ fit: 'insufficient_history' }), metric({ resource: 'memory' })],
        0,
        true,
      ),
    ).toBe('review')
  })

  it('keeps workloads with no current replicas in review', () => {
    const oversized = flattenScanResults(
      response(
        [
          metric({
            fit: 'oversized',
            currentRequestValue: 0.2,
            recommendedRequestValue: 0.05,
            recommendedRequest: '50m',
          }),
        ],
        0,
        'shop',
        true,
      ),
    )[0]
    expect(oversized.classification).toBe('review')
    expect(oversized.impact.cpuChange).toBe(0)

    expect(classifyRows([metric()], 0, true)).toBe('review')
    expect(classifyRows([metric({ fit: 'insufficient_history' })], 0, true)).toBe('need_data')
    expect(
      classifyRows(
        [
          metric({
            fit: 'missing_request',
            recommendedRequestValue: 0.1,
            recommendedRequest: '100m',
          }),
        ],
        0,
        true,
      ),
    ).toBe('review')
    expect(
      classifyRows(
        [
          metric({
            fit: 'under_requested',
            currentRequestValue: 0.1,
            recommendedRequestValue: 0.2,
            recommendedRequest: '200m',
          }),
        ],
        0,
        true,
      ),
    ).toBe('review')
  })

  it('groups resources, calculates replica impact, and tags system workloads', () => {
    const rows = flattenScanResults(
      response(
        [
          metric({
            fit: 'under_requested',
            currentRequestValue: 0.1,
            recommendedRequestValue: 0.2,
            recommendedRequest: '200m',
            throttleRatio: 0.2,
          }),
          metric({ resource: 'memory', currentPodOOM: true }),
        ],
        3,
        'kube-system',
      ),
    )
    expect(rows).toHaveLength(1)
    expect(rows[0].impact.cpuChange).toBeCloseTo(0.3)
    expect(rows[0].system).toBe(true)
    expect(scanClassCounts(rows).increase).toBe(1)
  })
})

it('ranks known safety risks and increases above savings, while routine reviews stay lower', () => {
  const make = (name: string, rows: RightsizingRow[], scaledToZero = false) => ({
    ...response(rows, scaledToZero ? 0 : 1, 'shop', scaledToZero).workloads[0],
    name,
  })
  const cut = metric({
    fit: 'oversized',
    currentRequestValue: 10,
    recommendedRequestValue: 1,
    recommendedRequest: '1',
  })
  const grow = metric({
    fit: 'under_requested',
    currentRequestValue: 0.1,
    recommendedRequestValue: 0.2,
    recommendedRequest: '200m',
  })
  const risk = metric({ resource: 'memory', currentPodOOM: true })
  const workloads = [
    make('cut', [cut]),
    make('grow', [grow]),
    make('hpa', [metric({ hpaManaged: true })]),
    make('oom', [risk]),
    make('limit', [metric({ limitConflict: true })]),
    make('bursty', [{ ...cut, bursty: true }]),
    make('throttled', [{ ...cut, throttleRatio: 0.2 }]),
    make('idle', [risk], true),
    make('failed-risk', [metric({ queryError: 'failed', currentPodOOM: true })]),
    make('partial-grow', [grow, metric({ resource: 'memory', queryError: 'failed' })]),
    make('steady', [metric()]),
  ]
  const rows = flattenScanResults({ ...response([]), workloads })
  const names = rows.map((row) => row.name)
  expect(new Set(names.slice(0, 4))).toEqual(new Set(['oom', 'limit', 'bursty', 'throttled']))
  expect(names.slice(4, 6)).toEqual(['grow', 'partial-grow'])
  expect(names[6]).toBe('cut')
  expect(names.slice(7, 9)).toEqual(['hpa', 'idle'])
  expect(names.slice(9)).toEqual(['failed-risk', 'steady'])
})
