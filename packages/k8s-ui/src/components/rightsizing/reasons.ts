import type { RightsizingRow } from '../../types/rightsizing'

const reasonText: Record<string, string> = {
  request_within_fit_range: 'Keep the current request. Radar suggests a reduction only when the demand-based target is at least 30% below the current request and the rounded recommendation is lower.',
  insufficient_history: 'At least six hours of samples are required before suggesting a request.',
  hpa_managed: 'This request affects the horizontal autoscaler.',
  hpa_evidence_unavailable:
    'Radar could not verify HPA ownership. Check permission to list HorizontalPodAutoscalers in this namespace.',
  oom_evidence: 'A lower memory request is withheld because OOM evidence exists.',
  oom_evidence_unavailable:
    'Radar could not verify recent OOM history, so it withheld a lower memory request.',
  recommended_request_exceeds_limit:
    'The evidence-based request would exceed the current limit; review the limit first.',
  requests_exceed_pod_limit:
    "Together with the other containers' requests and suggested increases, this request would take the pod above its pod-level limit (spec.resources), which Kubernetes rejects. Decide how to split the limit between containers, or raise the pod limit.",
  scaled_to_zero: 'This workload currently has no replicas.',
  cpu_peak_unavailable: 'CPU peak usage is unavailable. Review bursts before lowering the request.',
  qos_unavailable: 'Radar could not verify the QoS class of the current pods.',
  guaranteed_qos: 'At least one current Pod has Guaranteed QoS. Review requests and limits together to preserve that class.',
  low_confidence_reduction:
    'Too few measurements cover this window to recommend reducing the request.',
  bursty_reduction: 'CPU usage has short peaks.',
  throttled_reduction: 'The container is CPU throttled.',
  current_pod_oom: 'A current pod reported an out-of-memory termination.',
  oom_in_window: 'There is evidence of an out-of-memory termination during the window.',
  limit_conflict: 'The suggestion conflicts with the resource limit.',
  recommendation_withheld: 'Safety evidence is incomplete.',
  pod_level_request: 'The pod sets a shared resource request.',
}

export function rowReviewReasons(row: RightsizingRow, reasons: string[] = []): string[] {
  const limitReason = row.recommendationReason
  if (limitReason !== 'requests_exceed_pod_limit' && limitReason !== 'recommended_request_exceeds_limit')
    return reasons
  return reasons.map((reason) => reason === 'limit_conflict' ? limitReason : reason)
}

export function explainRightsizingReasons(
  reasons: string[],
  resource?: RightsizingRow['resource'],
): string {
  return [...new Set(reasons)]
    .filter((reason) => reason !== 'recommendation_withheld' || !reasons.some((r) => r === 'hpa_evidence_unavailable' || r === 'oom_evidence_unavailable'))
    .map((reason) => {
      if (resource && reason === 'hpa_managed')
        return `The HPA manages ${resource}; review its target before changing this request.`
      return reasonText[reason] ?? reason.replaceAll('_', ' ')
    })
    .join(' ')
}
