import { describe, expect, it } from 'vitest'
import type { RightsizingRow } from '../../api/client'
import {
  getRightsizingExplanation,
  getRightsizingPresentation,
  podLevelOverUseNote,
  RIGHTSIZING_METHODOLOGY,
  RIGHTSIZING_DOCS_URL,
  RIGHTSIZING_SUMMARY,
} from './RightsizingStrip'

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

  it('explains why recommendations are withheld without inventing a zero-risk result', () => {
    expect(getRightsizingExplanation(row({ recommendationReason: 'hpa_managed' }))).toContain(
      'HPA manages cpu',
    )
    expect(
      getRightsizingExplanation(row({ resource: 'memory', recommendationReason: 'oom_evidence' })),
    ).toContain('OOM evidence')
    expect(
      getRightsizingExplanation(row({ recommendationReason: 'hpa_evidence_unavailable' })),
    ).toContain('could not verify HPA')
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

  it('sets the workload-level scope and methodology without promising savings or changes', () => {
    expect(RIGHTSIZING_SUMMARY).toContain('this workload')
    expect(RIGHTSIZING_SUMMARY).toContain('not a savings estimate or automatic change')
    expect(RIGHTSIZING_METHODOLOGY).toContain('CPU P95 and memory maximum')
    expect(RIGHTSIZING_METHODOLOGY).toContain('Reductions are staged')
    expect(RIGHTSIZING_METHODOLOGY).toContain('Radar does not change requests')
    expect(RIGHTSIZING_DOCS_URL).toContain('/features/rightsizing')
  })
})

describe('live pod inventory', () => {
  it('qualifies a memory row when the pod list could not be read', () => {
    const explanation = getRightsizingExplanation(
      row({ resource: 'memory', liveInventoryUnavailable: true }),
    )
    expect(explanation).toContain('could not read')
    expect(explanation).toContain('out-of-memory restarts')
  })

  it('adds the caveat to a suppression reason instead of replacing it', () => {
    const explanation = getRightsizingExplanation(
      row({ resource: 'memory', recommendationReason: 'oom_evidence', liveInventoryUnavailable: true }),
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

describe('pod-level request below one container\'s usage', () => {
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
    expect(podLevelOverUseNote({ ...podLevel(0.44, '440m'), sampleCount: 1, coverage: 0 })).toBeUndefined()
  })
})
