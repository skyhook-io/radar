import { describe, expect, it } from 'vitest'
import type { RightsizingScanResponse, RightsizingRow } from '../../api/client'
import { classifyRows, flattenScanResults, scanClassCounts } from './model'

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

  it('keeps incomplete history out of no-change results', () => {
    expect(classifyRows([metric({ fit: 'insufficient_history' })])).toBe('need_data')
    expect(classifyRows([metric({ queryError: 'query failed' })])).toBe('need_data')
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
    expect(oversized.signals).toContain('scaled_zero')

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
    expect(rows[0].signals).toEqual(new Set(['oom', 'throttling']))
    expect(scanClassCounts(rows).increase).toBe(1)
  })
})
