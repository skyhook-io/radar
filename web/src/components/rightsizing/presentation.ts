import type { RightsizingRow } from '../../api/client'

export type ResourceSignal = 'hpa' | 'oom' | 'bursty' | 'throttling' | 'query_error'
export type RightsizingActionTone = 'reduction' | 'increase' | 'review' | 'neutral'

export const RIGHTSIZING_ACTION_SEVERITY = {
  reduction: 'info',
  increase: 'warning',
  review: 'neutral',
} as const

export interface ResourceRequestPresentation {
  primary: string
  secondary: string
  signals: ResourceSignal[]
  tone: RightsizingActionTone
}

export function getResourceRequestPresentation(
  row?: RightsizingRow,
  scaledToZero = false,
): ResourceRequestPresentation {
  if (!row) return { primary: '—', secondary: 'No result returned', signals: [], tone: 'neutral' }

  const current = row.currentRequest && row.currentRequestValue !== 0 ? row.currentRequest : 'Unset'
  const currentOnly = current === 'Unset' ? current : `${current} current`
  const signals = resourceSignals(row)
  const reason = row.recommendationReason

  if (row.queryError)
    return { primary: currentOnly, secondary: 'Metrics query failed', signals, tone: 'neutral' }
  if (row.fit === 'insufficient_history')
    return { primary: currentOnly, secondary: 'Not enough history', signals, tone: 'neutral' }
  if (scaledToZero)
    return { primary: currentOnly, secondary: 'Review before workload runs again', signals, tone: 'review' }
  if (row.hpaManaged)
    return { primary: currentOnly, secondary: 'Review with autoscaling', signals, tone: 'review' }
  if (reason === 'hpa_evidence_unavailable')
    return {
      primary: currentOnly,
      secondary: 'Review — autoscaling status unavailable',
      signals,
      tone: 'review',
    }
  if (reason === 'oom_evidence')
    return { primary: currentOnly, secondary: 'Keep current request', signals, tone: 'review' }
  if (reason === 'oom_evidence_unavailable')
    return {
      primary: currentOnly,
      secondary: 'Review — restart history incomplete',
      signals,
      tone: 'review',
    }

  const isReduction =
    row.recommendedRequestValue != null &&
    row.currentRequestValue != null &&
    row.recommendedRequestValue < row.currentRequestValue
  const comparison = row.recommendedRequest
    ? `${current} → ${row.recommendedRequest}`
    : currentOnly

  if (row.limitConflict)
    return {
      primary: comparison,
      secondary: 'Raise the limit before changing',
      signals,
      tone: 'review',
    }
  if (row.resource === 'cpu' && isReduction && (row.bursty || (row.throttleRatio ?? 0) >= 0.1))
    return { primary: currentOnly, secondary: 'Review before reducing', signals, tone: 'review' }
  if (row.recommendedRequest) {
    if (current === 'Unset')
      return { primary: comparison, secondary: 'Add request', signals, tone: 'increase' }
    const increase = (row.recommendedRequestValue ?? 0) > (row.currentRequestValue ?? 0)
    return {
      primary: comparison,
      secondary: increase ? 'Increase request' : 'Reduce request',
      signals,
      tone: increase ? 'increase' : 'reduction',
    }
  }
  return { primary: currentOnly, secondary: 'No meaningful change', signals, tone: 'neutral' }
}

function resourceSignals(row: RightsizingRow): ResourceSignal[] {
  const signals: ResourceSignal[] = []
  if (row.hpaManaged) signals.push('hpa')
  if (row.resource === 'memory' && (row.currentPodOOM || row.windowOomEvidence)) signals.push('oom')
  if (row.resource === 'cpu' && row.bursty) signals.push('bursty')
  if (row.resource === 'cpu' && (row.throttleRatio ?? 0) >= 0.1) signals.push('throttling')
  if (row.queryError) signals.push('query_error')
  return signals
}
