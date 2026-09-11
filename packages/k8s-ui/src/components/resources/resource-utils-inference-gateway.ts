// Gateway API Inference Extension CRD utility functions.
// InferencePool exists in two shapes: v1 (inference.networking.k8s.io) and
// v1alpha2 (inference.networking.x-k8s.io); accessors handle both.

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// INFERENCEPOOL UTILITIES
// ============================================================================

// v1 serializes status.parents, v1alpha2 serializes status.parent
function getPoolParents(resource: any): any[] {
  const status = resource.status
  if (!status) return []
  if (Array.isArray(status.parents)) return status.parents
  if (Array.isArray(status.parent)) return status.parent
  return []
}

// v1alpha2 reserves a synthetic parent entry to mean "no gateway references
// this pool" — seen as parentRef {kind: Status, name: default} or as an
// empty/missing parentRef. A ref without a name can't be a real Gateway.
function isDefaultParent(parent: any): boolean {
  const ref = parent?.parentRef
  if (!ref?.name) return true
  return ref.kind === 'Status' && ref.name === 'default'
}

export function getInferencePoolStatus(resource: any): StatusBadge {
  const parents = getPoolParents(resource).filter((p: any) => !isDefaultParent(p))
  if (parents.length === 0) {
    return { text: 'Not referenced', color: healthColors.neutral, level: 'neutral' }
  }

  const conditions = parents.flatMap((p: any) => (Array.isArray(p?.conditions) ? p.conditions : []))
  if (conditions.length === 0) {
    return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }

  const resolvedRefsFailed = conditions.find((c: any) => c?.type === 'ResolvedRefs' && c.status === 'False')
  if (resolvedRefsFailed) {
    return { text: resolvedRefsFailed.reason || 'RefsNotResolved', color: healthColors.alert, level: 'alert' }
  }

  const accepted = conditions.filter((c: any) => c?.type === 'Accepted')
  const acceptedTrue = accepted.filter((c: any) => c.status === 'True')
  if (accepted.length > 0 && acceptedTrue.length === accepted.length) {
    return { text: 'Accepted', color: healthColors.healthy, level: 'healthy' }
  }
  if (acceptedTrue.length > 0) {
    return { text: 'Partially accepted', color: healthColors.degraded, level: 'degraded' }
  }
  const notAccepted = accepted.find((c: any) => c.status === 'False')
  if (notAccepted) {
    return { text: notAccepted.reason || 'NotAccepted', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getInferencePoolSelector(resource: any): string {
  const selector = resource.spec?.selector
  if (!selector || typeof selector !== 'object') return '-'
  const labels = selector.matchLabels && typeof selector.matchLabels === 'object' ? selector.matchLabels : selector
  const entries = Object.entries(labels).filter(([, v]) => typeof v === 'string' || typeof v === 'number')
  if (entries.length === 0) return '-'
  const parts = entries.map(([k, v]) => `${k}=${v}`)
  if (parts.length > 2) return `${parts.slice(0, 2).join(', ')} +${parts.length - 2}`
  return parts.join(', ')
}

export function getInferencePoolTargetPorts(resource: any): string {
  const spec = resource.spec
  if (!spec) return '-'
  if (Array.isArray(spec.targetPorts)) {
    const numbers = spec.targetPorts
      .map((p: any) => p?.number)
      .filter((n: any) => n !== undefined && n !== null)
    return numbers.length > 0 ? numbers.join(', ') : '-'
  }
  if (spec.targetPortNumber !== undefined && spec.targetPortNumber !== null) {
    return String(spec.targetPortNumber)
  }
  return '-'
}

export function getInferencePoolExtensionRef(resource: any): string {
  const ref = resource.spec?.endpointPickerRef || resource.spec?.extensionRef
  return ref?.name || '-'
}

// ============================================================================
// INFERENCEOBJECTIVE UTILITIES
// ============================================================================

export function getInferenceObjectiveStatus(resource: any): StatusBadge {
  const conditions = Array.isArray(resource.status?.conditions) ? resource.status.conditions : []
  const accepted = conditions.find((c: any) => c?.type === 'Accepted')
    || conditions.find((c: any) => c?.type === 'Ready')
  if (accepted?.status === 'True') {
    return { text: 'Accepted', color: healthColors.healthy, level: 'healthy' }
  }
  if (accepted?.status === 'False') {
    return { text: accepted.reason || 'NotAccepted', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getInferenceObjectivePoolRef(resource: any): string {
  return resource.spec?.poolRef?.name || '-'
}

// API spec: consumers treat an unset priority as 0
export function getInferenceObjectivePriority(resource: any): string {
  const priority = resource.spec?.priority
  if (priority === undefined || priority === null) return '0'
  return String(priority)
}
