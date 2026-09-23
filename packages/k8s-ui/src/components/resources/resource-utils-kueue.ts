// Kueue + Cluster Autoscaler ProvisioningRequest CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

const failedFinishedReasons = new Set(['Failed', 'FailedToStart', 'OutOfSync', 'OwnerNotFound'])

export function isKueueConditionStale(condition: { observedGeneration?: number } | undefined, generation?: number): boolean {
  return !!condition?.observedGeneration && !!generation && condition.observedGeneration < generation
}

export function isKueueWorkloadFailureReason(reason: string): boolean {
  return failedFinishedReasons.has(reason)
}

// ============================================================================
// SHARED HELPERS
// ============================================================================

function findCondition(resource: any, type: string): any {
  return (resource?.status?.conditions || []).find((c: any) => c?.type === type)
}

function activeConditionStatus(resource: any): StatusBadge {
  const active = findCondition(resource, 'Active')
  if (active?.status === 'True') {
    return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
  }
  if (active?.status === 'False') {
    return { text: active.reason || 'Inactive', color: healthColors.alert, level: 'alert' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

function formatWorkloadCount(value: any): string {
  return typeof value === 'number' ? String(value) : '-'
}

// ============================================================================
// KUEUE CLUSTERQUEUE UTILITIES
// ============================================================================

export function getClusterQueueStatus(resource: any): StatusBadge {
  return activeConditionStatus(resource)
}

export function getClusterQueueCohort(resource: any): string {
  // v1beta2 renamed spec.cohort to spec.cohortName
  return resource?.spec?.cohortName || resource?.spec?.cohort || '-'
}

export function getClusterQueuePendingWorkloads(resource: any): string {
  return formatWorkloadCount(resource?.status?.pendingWorkloads)
}

export function getClusterQueueAdmittedWorkloads(resource: any): string {
  return formatWorkloadCount(resource?.status?.admittedWorkloads)
}

export function getClusterQueueFlavors(resource: any): string {
  const groups = resource?.spec?.resourceGroups || []
  const flavors = [
    ...new Set(groups.flatMap((g: any) => (g?.flavors || []).map((f: any) => f?.name).filter(Boolean))),
  ] as string[]
  if (flavors.length === 0) return '-'
  if (flavors.length > 3) return `${flavors.slice(0, 3).join(', ')} +${flavors.length - 3}`
  return flavors.join(', ')
}

// ============================================================================
// KUEUE LOCALQUEUE UTILITIES
// ============================================================================

export function getLocalQueueStatus(resource: any): StatusBadge {
  return activeConditionStatus(resource)
}

export function getLocalQueueClusterQueue(resource: any): string {
  return resource?.spec?.clusterQueue || '-'
}

export function getLocalQueuePendingWorkloads(resource: any): string {
  return formatWorkloadCount(resource?.status?.pendingWorkloads)
}

export function getLocalQueueAdmittedWorkloads(resource: any): string {
  return formatWorkloadCount(resource?.status?.admittedWorkloads)
}

// ============================================================================
// KUEUE WORKLOAD UTILITIES
// ============================================================================

export function getKueueWorkloadStatusCondition(resource: any): any {
  for (const type of ['Finished', 'Evicted', 'Preempted', 'Admitted', 'QuotaReserved']) {
    const condition = findCondition(resource, type)
    if (condition?.status === 'True') return condition
  }
}

export function getKueueWorkloadStatus(resource: any): StatusBadge {
  const condition = getKueueWorkloadStatusCondition(resource)
  switch (condition?.type) {
    case 'Finished':
      return isKueueWorkloadFailureReason(condition.reason)
        ? { text: condition.reason, color: healthColors.unhealthy, level: 'unhealthy' }
        : { text: 'Finished', color: healthColors.neutral, level: 'neutral' }
    case 'Evicted':
    case 'Preempted':
      return { text: condition.type, color: healthColors.degraded, level: 'degraded' }
    case 'Admitted':
      return { text: 'Admitted', color: healthColors.healthy, level: 'healthy' }
    case 'QuotaReserved':
      return { text: 'QuotaReserved', color: healthColors.neutral, level: 'neutral' }
    default:
      return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
  }
}

export function getKueueWorkloadQueueName(resource: any): string {
  return resource?.spec?.queueName || '-'
}

export function getKueueWorkloadAdmittedBy(resource: any): string {
  return resource?.status?.admission?.clusterQueue || '-'
}

export function getKueueWorkloadPriority(resource: any): string {
  const priority = resource?.spec?.priority
  if (typeof priority === 'number') return String(priority)
  // v1beta1 uses spec.priorityClassName, v1beta2 uses spec.priorityClassRef
  return resource?.spec?.priorityClassRef?.name || resource?.spec?.priorityClassName || '-'
}

// ============================================================================
// KUEUE RESOURCEFLAVOR UTILITIES
// ============================================================================

export function getResourceFlavorStatus(_resource: any): StatusBadge {
  return { text: 'Configured', color: healthColors.neutral, level: 'neutral' }
}

export function getResourceFlavorNodeLabelCount(resource: any): number {
  return Object.keys(resource?.spec?.nodeLabels || {}).length
}

export function getResourceFlavorTaintCount(resource: any): number {
  return (resource?.spec?.nodeTaints || []).length
}

// ============================================================================
// KUEUE ADMISSIONCHECK UTILITIES
// ============================================================================

export function getAdmissionCheckStatus(resource: any): StatusBadge {
  return activeConditionStatus(resource)
}

export function getAdmissionCheckControllerName(resource: any): string {
  return resource?.spec?.controllerName || '-'
}

// ============================================================================
// CLUSTER AUTOSCALER PROVISIONINGREQUEST UTILITIES
// ============================================================================

export function getProvisioningRequestStatus(resource: any): StatusBadge {
  const failed = findCondition(resource, 'Failed')
  if (failed?.status === 'True') {
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const capacityRevoked = findCondition(resource, 'CapacityRevoked')
  if (capacityRevoked?.status === 'True') {
    return { text: 'CapacityRevoked', color: healthColors.degraded, level: 'degraded' }
  }

  const bookingExpired = findCondition(resource, 'BookingExpired')
  if (bookingExpired?.status === 'True') {
    return { text: 'BookingExpired', color: healthColors.degraded, level: 'degraded' }
  }

  const provisioned = findCondition(resource, 'Provisioned')
  if (provisioned?.status === 'True') {
    return { text: 'Provisioned', color: healthColors.healthy, level: 'healthy' }
  }

  const accepted = findCondition(resource, 'Accepted')
  if (accepted?.status === 'True') {
    return { text: 'Accepted', color: healthColors.neutral, level: 'neutral' }
  }

  return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
}

export function getProvisioningRequestClassName(resource: any): string {
  return resource?.spec?.provisioningClassName || '-'
}

export function getProvisioningRequestPodSetCount(resource: any): number {
  return (resource?.spec?.podSets || []).length
}
