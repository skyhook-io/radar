import { currentPodEvidenceLabel, explainRightsizingReasons } from './reasons'
import type { RightsizingFit, RightsizingRow } from '../../types/rightsizing'

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
  review?: string,
): ResourceRequestPresentation {
  if (!row) return { primary: '—', secondary: 'No result returned', signals: [], tone: 'neutral' }

  const current = row.currentRequest ?? 'Unset'
  // A pod-level request is what the pod reserves, whether or not usage evidence arrived.
  const currentOnly = row.podLevelRequest
    ? `${row.podLevelRequest} for the pod`
    : current === 'Unset'
      ? current
      : `${current} current`
  const signals = resourceSignals(row)
  const reason = row.recommendationReason

  if (row.queryError)
    return { primary: currentOnly, secondary: 'Metrics query failed', signals, tone: 'neutral' }
  const missing = currentPodEvidenceLabel(reason)
  if (missing) return { primary: currentOnly, secondary: missing, signals, tone: 'neutral' }
  if (row.decision?.status === 'none') return {
    primary: currentOnly, secondary: row.fit === 'insufficient_history' ? 'Not enough history' : 'No meaningful change', signals, tone: 'neutral',
  }
  if (row.decision?.status === 'review' || review) return {
    primary: row.decision?.status === 'review' && row.recommendedRequest ? `${current} → ${row.recommendedRequest}` : currentOnly,
    secondary: 'Review before changing', signals, tone: 'review',
  }
  const comparison = row.recommendedRequest ? `${current} → ${row.recommendedRequest}` : currentOnly
  const proposedRequest = (): ResourceRequestPresentation => {
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
  if (row.decision?.status === 'actionable') return proposedRequest()
  if (row.fit === 'pod_level_request')
    return {
      primary: currentOnly,
      secondary: 'Review the pod-level request',
      signals,
      tone: 'review',
    }
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


  if (row.limitConflict)
    return {
      primary: comparison,
      secondary:
        reason === 'requests_exceed_pod_limit'
          ? 'Review against the pod limit'
          : 'Raise the limit before changing',
      signals,
      tone: 'review',
    }
  if (row.resource === 'cpu' && isReduction && (row.bursty || (row.throttleRatio ?? 0) >= 0.1))
    return { primary: currentOnly, secondary: 'Review before reducing', signals, tone: 'review' }
  if (row.recommendedRequest) return proposedRequest()
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

export function getRightsizingPresentation(fit: RightsizingFit, queryError?: string, recommendationReason?: string) {
  if (queryError) return { label: 'Query failed', severity: 'error' as const }
  const labels: Record<RightsizingFit, string> = {
    balanced: 'Balanced',
    oversized: 'Oversized',
    under_requested: 'Under-requested',
    missing_request: 'Missing request',
    insufficient_history: currentPodEvidenceLabel(recommendationReason) ?? 'Insufficient history',
    pod_level_request: 'Pod-level request',
  }
  const severities: Record<RightsizingFit, 'success' | 'info' | 'warning' | 'neutral'> = {
    balanced: 'success',
    oversized: 'info',
    under_requested: 'warning',
    missing_request: 'warning',
    insufficient_history: 'neutral',
    pod_level_request: 'neutral',
  }
  // An older frontend can meet a fit added by a newer agent.
  return { label: labels[fit] ?? fit, severity: severities[fit] ?? ('neutral' as const) }
}

export function getRightsizingExplanation(
  row: RightsizingRow,
  manualReview = false,
): string | undefined {
  // A denied pod read leaves currentPodOOM unknown rather than false, so it
  // qualifies whatever else is said instead of replacing it — including a
  // staged reduction, which is exactly where an unchecked pod matters most.
  const inventoryNote =
    row.liveInventoryUnavailable && row.resource === 'memory'
      ? "Radar could not read this workload's pods, so it could not check them for out-of-memory restarts."
      : undefined
  const throttleNote =
    row.resource === 'cpu' && !row.throttleAvailable
      ? 'CPU throttling metrics are unavailable; no throttling conclusion was drawn.'
      : undefined
  const qualified = (base?: string) =>
    [base, inventoryNote, throttleNote].filter(Boolean).join(' ') || undefined
  if (row.fit === 'pod_level_request' && row.podLevelRequest && !row.queryError)
    return qualified(podLevelRequestExplanation(row, row.podLevelRequest))
  if (row.reductionLimited && row.calculatedRequest && row.recommendedRequest) {
    if (manualReview)
      return qualified(
        `Demand-based target: ${row.calculatedRequest}. Review the staged change before editing.`,
      )
    const burstNote = row.bursty && row.peak ? ` CPU P99 reached ${row.peak.formatted}.` : ''
    return qualified(
      `Demand-based target: ${row.calculatedRequest}. Radar suggests ${row.recommendedRequest} as a conservative next step; observe another full window before reducing further.${burstNote}`,
    )
  }
  const message = row.queryError
    ? `Partial result: ${row.queryError}.`
    : row.recommendationReason &&
      explainRightsizingReasons([row.recommendationReason], row.resource)
  if (!message && row.calculatedAtMinimum && !row.reductionLimited && row.recommendedRequest)
    return qualified(`Radar’s minimum suggested request is ${row.recommendedRequest}.`)
  return qualified(message)
}

function podLevelRequestExplanation(row: RightsizingRow, podLevelRequest: string): string {
  const overUse = podLevelOverUseNote(row)
  if (overUse) return overUse
  const resource = row.resource === 'cpu' ? 'CPU' : 'memory'
  const own = row.currentRequest
    ? ` This container's own request (${row.currentRequest}) no longer decides what is reserved.`
    : ''
  return `This pod sets one ${resource} request for all its containers: ${podLevelRequest} in spec.resources. The scheduler reserves that instead of container requests, so Radar doesn't suggest a request per container; check the pod-level request against the usage of its containers.${own}`
}

/**
 * One container's usage above the pod's whole request is a sound lower bound on
 * the pod's own use. Adding containers up would overstate it, so only this case
 * is called out.
 */
export function podLevelOverUseNote(row: RightsizingRow): string | undefined {
  if (
    row.fit !== 'pod_level_request' ||
    row.sampleCount < RIGHTSIZING_MIN_SAMPLES ||
    !row.observed ||
    !row.podLevelRequest ||
    row.podLevelRequestValue == null ||
    row.observed.value <= row.podLevelRequestValue
  )
    return undefined
  const resource = row.resource === 'cpu' ? 'CPU' : 'memory'
  const finding = `This container alone used ${row.observed.formatted} (${row.observed.name}), more than the pod's whole ${row.podLevelRequest} ${resource} request. Radar can't size pod-level requests yet.`
  // An HPA scaling on this resource divides usage by the pod request, so
  // raising it changes scaling; Radar never suggests that unreviewed.
  if (row.hpaManaged)
    return `${finding} The HPA scales on this request, so review its target before raising spec.resources.requests.${row.resource}.`
  if (!row.hpaEvidenceAvailable)
    return `${finding} Radar couldn't check for an HPA on this resource, so check autoscaling before raising spec.resources.requests.${row.resource}.`
  return `${finding} Consider raising spec.resources.requests.${row.resource}.`
}

// Mirrors rightsizingMinSamples (six hours of 5-minute samples) in
// internal/prometheus/rightsizing.go: below it Radar draws no conclusion.
const RIGHTSIZING_MIN_SAMPLES = 72
