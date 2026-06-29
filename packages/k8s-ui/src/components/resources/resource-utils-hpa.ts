import type { HealthLevel, StatusBadge } from './resource-utils'
import type { HPADiagnosisState } from '../../types'

export function getHPATableState(hpa: any): HPADiagnosisState {
  // List rows only classify states worth scanning in a table; the Go diagnosis powers the full drawer context.
  const conditions = Array.isArray(hpa?.status?.conditions) ? hpa.status.conditions : []
  const current = hpa?.status?.currentReplicas ?? 0
  const desired = hpa?.status?.desiredReplicas ?? 0
  const min = hpa?.spec?.minReplicas ?? 1
  const max = hpa?.spec?.maxReplicas ?? 0

  if (conditionStatus(conditions, 'AbleToScale') === 'False') return 'unable_to_scale'
  const scalingActive = condition(conditions, 'ScalingActive')
  if (scalingActive?.status === 'False') {
    if (isScalingDisabled(scalingActive)) return 'disabled'
    return 'metrics_unavailable'
  }
  if (max > 0 && min === max && current === desired && desired === max) return 'pinned'
  if (isMaxLimited(condition(conditions, 'ScalingLimited'))) return 'limited_max'

  if (current < desired) return 'scaling_up'
  if (current > desired) return 'scaling_down'
  return 'ok'
}

export function hpaStateLabel(state: HPADiagnosisState): string {
  switch (state) {
    case 'unable_to_scale':
      return 'Unable to scale'
    case 'metrics_unavailable':
      return 'Metrics unavailable'
    case 'metrics_incomplete':
      return 'Metrics incomplete'
    case 'disabled':
      return 'Disabled'
    case 'pinned':
      return 'Pinned'
    case 'limited_max':
      return 'Maxed'
    case 'limited_min':
      return 'At min'
    case 'stale':
      return 'Stale'
    case 'stabilized':
      return 'Stabilized'
    case 'scaling_up':
      return 'Scaling Up'
    case 'scaling_down':
      return 'Scaling Down'
    case 'ok':
      return 'Stable'
    default:
      return 'Unknown'
  }
}

export function hpaStateLevel(state: HPADiagnosisState): HealthLevel {
  switch (state) {
    case 'unable_to_scale':
    case 'metrics_unavailable':
      return 'unhealthy'
    case 'limited_max':
    case 'metrics_incomplete':
      return 'degraded'
    case 'scaling_up':
    case 'scaling_down':
      return 'degraded'
    case 'limited_min':
    case 'disabled':
    case 'pinned':
    case 'stabilized':
      return 'neutral'
    case 'stale':
      return 'unknown'
    case 'ok':
      return 'healthy'
    default:
      return 'unknown'
  }
}

export function hpaStatusFromState(state: HPADiagnosisState, healthColors: Record<HealthLevel, string>): StatusBadge {
  const level = hpaStateLevel(state)
  return { text: hpaStateLabel(state), color: healthColors[level], level }
}

function conditionStatus(conditions: any[], type: string): string | undefined {
  return condition(conditions, type)?.status
}

function condition(conditions: any[], type: string): any | undefined {
  return conditions.find((item) => item?.type === type)
}

function isScalingDisabled(item: any): boolean {
  const reason = String(item?.reason ?? '').toLowerCase()
  const message = String(item?.message ?? '').toLowerCase()
  return reason === 'scalingdisabled' || message.includes('scaling is disabled')
}

function isMaxLimited(item: any): boolean {
  if (item?.status !== 'True') return false
  const reason = String(item?.reason ?? '').toLowerCase()
  const message = String(item?.message ?? '').toLowerCase()
  return reason.includes('toomany') || message.includes('maximum')
}
