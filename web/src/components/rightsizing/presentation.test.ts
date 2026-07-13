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
