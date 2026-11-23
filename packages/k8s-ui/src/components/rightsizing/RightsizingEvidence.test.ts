import { describe, expect, it } from 'vitest'
import type { RightsizingRow } from '../../types/rightsizing'
import {
  getRightsizingExplanation,
  getRightsizingPresentation,
  podLevelOverUseNote,
} from './RightsizingEvidence'

const row = (overrides: Partial<RightsizingRow> = {}): RightsizingRow => ({
  container: 'server',
  resource: 'cpu',
  fit: 'balanced',
  confidence: 'high',
  sampleCount: 2016,
  expectedSamples: 2016,
  coverage: 1,
  hpaManaged: false,
  hpaEvidenceAvailable: true,
  oomEvidenceAvailable: true,
  throttleAvailable: true,
  ...overrides,
})

describe('rightsizing presentation', () => {
  it('keeps fit, confidence, and runtime risk as separate concepts', () => {
    expect(getRightsizingPresentation('oversized')).toEqual({
      label: 'Oversized',
      severity: 'info',
    })
    expect(getRightsizingPresentation('under_requested')).toEqual({
      label: 'Under-requested',
      severity: 'warning',
    })
    expect(getRightsizingPresentation('insufficient_history')).toEqual({
      label: 'Insufficient history',
      severity: 'neutral',
    })
  })

  it('labels query failures independently from insufficient history', () => {
    expect(getRightsizingPresentation('insufficient_history', 'usage query failed')).toEqual({
      label: 'Query failed',
      severity: 'error',
    })
  })

  it('explains a policy minimum only when it sets the suggested request', () => {
    const minimum = row({ resource: 'memory', fit: 'under_requested', calculatedAtMinimum: true, calculatedRequest: '64Mi', recommendedRequest: '64Mi' })
    expect(getRightsizingExplanation(minimum)).toBe('Radar’s minimum suggested request is 64Mi.')
    expect(getRightsizingExplanation({ ...minimum, calculatedAtMinimum: undefined })).toBeUndefined()
    expect(getRightsizingExplanation({ ...minimum, recommendedRequest: undefined })).toBeUndefined()
    expect(getRightsizingExplanation({ ...minimum, reductionLimited: true, recommendedRequest: '256Mi' })).toContain('conservative next step')
  })

  it('explains why recommendations are withheld without inventing a zero-risk result', () => {
    expect(getRightsizingExplanation(row({ recommendationReason: 'hpa_managed' }))).toContain(
      'HPA manages cpu',
    )
    expect(
      getRightsizingExplanation(row({ resource: 'memory', recommendationReason: 'oom_evidence' })),
    ).toContain('OOM evidence')
    expect(
      getRightsizingExplanation(row({ recommendationReason: 'hpa_evidence_unavailable' })),
    ).toContain('Check permission to list HorizontalPodAutoscalers')
    expect(
      getRightsizingExplanation(
        row({
          resource: 'memory',
          recommendationReason: 'oom_evidence_unavailable',
        }),
      ),
    ).toContain('could not verify recent OOM')
    expect(getRightsizingExplanation(row({ throttleAvailable: false }))).toContain(
      'throttling metrics are unavailable',
    )
  })

  it('separates the demand target from a conservative reduction step', () => {
    const explanation = getRightsizingExplanation(
      row({
        fit: 'oversized',
        currentRequest: '1',
        recommendedRequest: '500m',
        calculatedRequest: '10m',
        reductionLimited: true,
        bursty: true,
        peak: { name: 'P99', value: 0.2, formatted: '200m' },
      }),
    )
    expect(explanation).toContain('Demand-based target: 10m')
    expect(explanation).toContain('conservative next step')
    expect(explanation).toContain('CPU P99 reached 200m')
  })

  it('explains the reduction threshold relative to the current request and accounts for rounding', () => {
    const explanation = getRightsizingExplanation(row({ recommendationReason: 'request_within_fit_range' }))
    expect(explanation).toContain('at least 30% below the current request')
    expect(explanation).toContain('the rounded recommendation is lower')
  })

  it('does not revive the misleading efficiency vocabulary', () => {
    const copy = [
      getRightsizingPresentation('balanced').label,
      getRightsizingPresentation('oversized').label,
      getRightsizingExplanation(row({ recommendationReason: 'request_within_fit_range' })),
    ]
      .join(' ')
      .toLowerCase()
    expect(copy).not.toContain('efficien')
    expect(copy).not.toContain('waste')
  })
})

describe('live pod inventory', () => {
  it('keeps the missing OOM check alongside a manual staged reduction', () => {
    const note = getRightsizingExplanation(row({
      resource: 'memory', fit: 'oversized', liveInventoryUnavailable: true, reductionLimited: true,
      calculatedRequest: '128Mi', recommendedRequest: '256Mi',
    }), true)
    expect(note).toContain('Review the staged change')
    expect(note).toContain('could not check them for out-of-memory restarts')
  })

  it('qualifies a memory row when the pod list could not be read', () => {
    const explanation = getRightsizingExplanation(
      row({ resource: 'memory', liveInventoryUnavailable: true }),
    )
    expect(explanation).toContain('could not read')
    expect(explanation).toContain('out-of-memory restarts')
  })

  it('adds the caveat to a suppression reason instead of replacing it', () => {
    const explanation = getRightsizingExplanation(
      row({
        resource: 'memory',
        recommendationReason: 'oom_evidence',
        liveInventoryUnavailable: true,
      }),
    )
    expect(explanation).toContain('OOM evidence exists')
    expect(explanation).toContain('could not read')
  })

  it('keeps the caveat on a staged reduction, where an unchecked pod matters most', () => {
    const explanation = getRightsizingExplanation(
      row({
        resource: 'memory',
        liveInventoryUnavailable: true,
        reductionLimited: true,
        calculatedRequest: '256Mi',
        recommendedRequest: '384Mi',
      }),
    )
    expect(explanation).toContain('conservative next step')
    expect(explanation).toContain('could not read')
  })

  it('says nothing when the pods were read', () => {
    expect(getRightsizingExplanation(row({ resource: 'memory' })) ?? '').not.toContain(
      'could not read',
    )
  })
})

describe('pod-level request rows', () => {
  it('explains the pod-level value instead of promising a per-container verdict', () => {
    const message = getRightsizingExplanation(
      row({
        fit: 'pod_level_request',
        recommendationReason: 'pod_level_request',
        podLevelRequest: '128Mi',
        resource: 'memory',
      }),
    )
    expect(message).toContain('128Mi')
  })

  it('labels a fit this frontend does not know instead of rendering nothing', () => {
    const unknownFit = 'future_fit' as unknown as Parameters<typeof getRightsizingPresentation>[0]
    expect(getRightsizingPresentation(unknownFit).label).toBe('future_fit')
  })
})

describe("pod-level request below one container's usage", () => {
  const podLevel = (observed: number, formatted: string) =>
    row({
      fit: 'pod_level_request',
      recommendationReason: 'pod_level_request',
      podLevelRequest: '200m',
      podLevelRequestValue: 0.2,
      observed: { name: 'P95', value: observed, formatted },
    })

  it('names the overuse when one container alone used more than the pod request', () => {
    const message = getRightsizingExplanation(podLevel(0.44, '440m'))
    expect(message).toContain('440m')
    expect(podLevelOverUseNote(podLevel(0.44, '440m'))).toBe(message)
  })

  it('says nothing about overuse when the container stayed under the pod request', () => {
    expect(podLevelOverUseNote(podLevel(0.15, '150m'))).toBeUndefined()
  })

  it('does not ask to raise the request when an HPA scales on it', () => {
    const note = podLevelOverUseNote({ ...podLevel(0.44, '440m'), hpaManaged: true })
    expect(note).toContain('440m')
    expect(note).toContain('HPA')
  })

  it('asks for an autoscaling check when Radar could not read HPAs', () => {
    const note = podLevelOverUseNote({ ...podLevel(0.44, '440m'), hpaEvidenceAvailable: false })
    expect(note).toContain('440m')
    expect(note).toContain('HPA')
  })

  it('stays quiet before six hours of samples', () => {
    expect(
      podLevelOverUseNote({ ...podLevel(0.44, '440m'), sampleCount: 1, coverage: 0 }),
    ).toBeUndefined()
  })
})
