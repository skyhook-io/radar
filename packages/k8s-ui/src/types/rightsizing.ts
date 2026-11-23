export type RightsizingFit =
  | 'balanced'
  | 'oversized'
  | 'under_requested'
  | 'missing_request'
  | 'insufficient_history'
  | 'pod_level_request'
export type RightsizingConfidence = 'low' | 'medium' | 'high'
export type RightsizingOwnerCoverage = 'ksm_history' | 'current_pods'

export interface RightsizingRow {
  decision?: { status: 'actionable' | 'review' | 'none'; reasons?: string[] }
  container: string
  resource: 'cpu' | 'memory'
  fit: RightsizingFit
  confidence: RightsizingConfidence
  currentRequest?: string
  currentRequestValue?: number
  currentLimit?: string
  currentLimitValue?: number
  /** The pod's fixed pod-level request (spec.resources), set when fit is pod_level_request. */
  podLevelRequest?: string
  podLevelRequestValue?: number
  observed?: {
    name: 'P95' | 'P99' | 'Max'
    value: number
    formatted: string
  }
  peak?: {
    name: 'P99'
    value: number
    formatted: string
  }
  calculatedRequest?: string
  calculatedRequestValue?: number
  calculatedAtMinimum?: boolean
  recommendedRequest?: string
  recommendedRequestValue?: number
  reductionLimited?: boolean
  bursty?: boolean
  recommendationReason?: string
  sampleCount: number
  expectedSamples: number
  coverage: number
  hpaManaged: boolean
  hpaEvidenceAvailable: boolean
  throttleAvailable?: boolean
  throttleRatio?: number
  currentPodOOM?: boolean
  windowOomEvidence?: boolean
  oomEvidenceAvailable: boolean
  /** The cluster cache would not list the workload's pods, so currentPodOOM is unknown rather than false. */
  liveInventoryUnavailable?: boolean
  limitConflict?: boolean
  queryError?: string
}

export interface WorkloadRightsizingEvidence {
  kind: string
  namespace: string
  name: string
  window: string
  replicas: number
  managedBy?: {
    tool: string
    name?: string
    namespace?: string
    group?: string
    kind?: string
    signal: string
  }
  ownerCoverage: RightsizingOwnerCoverage
  scaledToZero: boolean
  sampleAvailable: boolean
  rows: RightsizingRow[]
  reason?: string
}

export interface DatadogSizingWorkload extends WorkloadRightsizingEvidence {
  source: 'datadog'
  reviewReasons?: Record<string, Partial<Record<RightsizingRow['resource'], string[]>>>
  actionableRequests: { container: string; resource: RightsizingRow['resource'] }[]
}

export type DatadogSizingDetail =
  | { configured: false; reason?: string }
  | { configured: true; rightsizing: DatadogSizingWorkload }
