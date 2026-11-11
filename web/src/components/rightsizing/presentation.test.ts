import { describe, expect, it } from 'vitest'
import type { RightsizingRow } from '../../api/client'
import { getResourceRequestPresentation } from './presentation'

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

describe('rightsizing request presentation', () => {
  it('shows configured and suggested values in a consistent comparison', () => {
    expect(
      getResourceRequestPresentation(
        metric({
          fit: 'oversized',
          currentRequest: '1500m',
          currentRequestValue: 1.5,
          recommendedRequest: '750m',
          recommendedRequestValue: 0.75,
        }),
      ),
    ).toEqual({
      primary: '1500m → 750m',
      secondary: 'Reduce request',
      signals: [],
      tone: 'reduction',
    })
  })

  it('makes missing requests explicit', () => {
    expect(
      getResourceRequestPresentation(
        metric({
          fit: 'missing_request',
          recommendedRequest: '100m',
          recommendedRequestValue: 0.1,
        }),
      ),
    ).toEqual({
      primary: 'Unset → 100m',
      secondary: 'Add request',
      signals: [],
      tone: 'increase',
    })
  })

  it('keeps the configured value visible when safety signals require review', () => {
    expect(
      getResourceRequestPresentation(
        metric({
          fit: 'oversized',
          currentRequest: '200m',
          currentRequestValue: 0.2,
          recommendedRequest: '100m',
          recommendedRequestValue: 0.1,
          bursty: true,
          throttleRatio: 0.2,
        }),
      ),
    ).toEqual({
      primary: '200m current',
      secondary: 'Review before reducing',
      signals: ['bursty', 'throttling'],
      tone: 'review',
    })
  })

  it('places resource-specific safety evidence with the request it qualifies', () => {
    expect(
      getResourceRequestPresentation(
        metric({
          resource: 'memory',
          currentRequest: '128Mi',
          currentRequestValue: 128 * 1024 * 1024,
          recommendationReason: 'oom_evidence',
          windowOomEvidence: true,
        }),
      ),
    ).toEqual({
      primary: '128Mi current',
      secondary: 'Keep current request',
      signals: ['oom'],
      tone: 'review',
    })
  })

  it('keeps no-change and unavailable data neutral', () => {
    expect(getResourceRequestPresentation(metric()).tone).toBe('neutral')
    expect(getResourceRequestPresentation(metric({ queryError: 'query failed' })).tone).toBe(
      'neutral',
    )
  })
})

describe('pod-level request presentation', () => {
  it('shows the pod-level value and asks for review instead of a per-container change', () => {
    const result = getResourceRequestPresentation(
      metric({
        fit: 'pod_level_request',
        podLevelRequest: '200m',
        recommendationReason: 'pod_level_request',
      }),
    )
    expect(result.primary).toContain('200m')
    expect(result.tone).toBe('review')
  })
})

describe('pod-level limit conflict presentation', () => {
  it('points at the pod limit rather than the container limit', () => {
    const podLimit = getResourceRequestPresentation(
      metric({
        fit: 'under_requested',
        limitConflict: true,
        recommendationReason: 'requests_exceed_pod_limit',
      }),
    )
    const containerLimit = getResourceRequestPresentation(
      metric({
        fit: 'under_requested',
        limitConflict: true,
        recommendationReason: 'recommended_request_exceeds_limit',
      }),
    )
    expect(podLimit.tone).toBe('review')
    expect(podLimit.secondary).not.toBe(containerLimit.secondary)
  })
})

describe('pod-level request without usage evidence', () => {
  it('still shows the pod-level request instead of Unset', () => {
    for (const row of [
      metric({ fit: 'insufficient_history', podLevelRequest: '128Mi' }),
      metric({ fit: 'insufficient_history', podLevelRequest: '128Mi', queryError: 'usage query failed' }),
    ]) {
      expect(getResourceRequestPresentation(row).primary).toContain('128Mi')
    }
  })
})
