import { describe, expect, it } from 'vitest'
import type { RightsizingRow } from '../../api/client'
import {
  getRightsizingExplanation,
  getRightsizingPresentation,
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
