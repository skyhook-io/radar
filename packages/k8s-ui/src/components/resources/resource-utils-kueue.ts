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

export function isKueueQueueResource(resource: any): boolean {
  return resource?.apiVersion === 'kueue.x-k8s.io/v1beta1' || resource?.apiVersion === 'kueue.x-k8s.io/v1beta2'
}

export function getKueueQueueCondition(resource: any): any {
  return findCondition(resource, 'Active')
}

function queueConditionStatus(resource: any): StatusBadge {
  const active = getKueueQueueCondition(resource)
  if (isKueueConditionStale(active, resource?.metadata?.generation)) {
    return { text: `${active.status === 'True' ? 'Active' : active.reason || 'Unknown'} (stale)`, color: healthColors.unknown, level: 'unknown' }
  }
  if (active?.status === 'False' && active.reason === 'Stopped') {
    return { text: 'Stopped', color: healthColors.neutral, level: 'neutral' }
  }
  return activeConditionStatus(resource)
}

type Quantity = string | number
interface QueueResourceUsage {
  name: string
  total?: Quantity
  borrowed?: Quantity
}
interface QueueFlavorUsage {
  name: string
  resources?: QueueResourceUsage[]
}
export interface KueueQuotaRow {
  flavor: string
  resource: string
  configured: boolean
  nominal?: Quantity
  borrowingLimit?: Quantity
  lendingLimit?: Quantity
  reserved?: Quantity
  used?: Quantity
  borrowedReservation?: Quantity
  borrowedUsage?: Quantity
}

export function getKueueQueueQuotaRows(resource: any, clusterQueue: boolean): KueueQuotaRow[] {
  const rows = new Map<string, KueueQuotaRow>()
  const rowFor = (flavor: string, name: string) => {
    const key = JSON.stringify([flavor, name])
    let row = rows.get(key)
    if (!row) {
      row = { flavor, resource: name, configured: false }
      rows.set(key, row)
    }
    return row
  }
  if (clusterQueue) {
    for (const group of resource?.spec?.resourceGroups ?? []) {
      for (const flavor of group.flavors ?? []) {
        for (const quota of flavor.resources ?? []) {
          Object.assign(rowFor(flavor.name, quota.name), {
            configured: true, nominal: quota.nominalQuota,
            borrowingLimit: quota.borrowingLimit, lendingLimit: quota.lendingLimit,
          })
        }
      }
    }
  }
  const reservation: QueueFlavorUsage[] = resource?.status?.flavorsReservation ?? []
  const usage: QueueFlavorUsage[] = !clusterQueue && resource?.apiVersion === 'kueue.x-k8s.io/v1beta1'
    ? resource?.status?.flavorUsage ?? []
    : resource?.status?.flavorsUsage ?? []
  for (const flavor of reservation) {
    for (const entry of flavor.resources ?? []) {
      Object.assign(rowFor(flavor.name, entry.name), { reserved: entry.total, borrowedReservation: entry.borrowed })
    }
  }
  for (const flavor of usage) {
    for (const entry of flavor.resources ?? []) {
      Object.assign(rowFor(flavor.name, entry.name), { used: entry.total, borrowedUsage: entry.borrowed })
    }
  }
  return [...rows.values()]
}

function formatWorkloadCount(value: any): string {
  return typeof value === 'number' ? String(value) : '-'
}

// ============================================================================
// KUEUE CLUSTERQUEUE UTILITIES
// ============================================================================

export function getClusterQueueStatus(resource: any): StatusBadge {
  return queueConditionStatus(resource)
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
  return queueConditionStatus(resource)
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
  const condition = findCondition(resource, 'Active')
  if (isKueueConditionStale(condition, resource?.metadata?.generation)) {
    return { text: 'Stale', color: healthColors.unknown, level: 'unknown' }
  }
  return activeConditionStatus(resource)
}

export function getAdmissionCheckControllerName(resource: any): string {
  return resource?.spec?.controllerName || '-'
}

// ============================================================================
// CLUSTER AUTOSCALER PROVISIONINGREQUEST UTILITIES
// ============================================================================

export function getProvisioningRequestStatusCondition(resource: any): any {
  for (const type of ['Failed', 'CapacityRevoked', 'BookingExpired', 'Provisioned', 'Accepted']) {
    const condition = findCondition(resource, type)
    if (condition?.status === 'True') return condition
  }
}

export function getProvisioningRequestMessage(resource: any): string | undefined {
  const condition = getProvisioningRequestStatusCondition(resource)
  if (condition?.type === 'Accepted') {
    const progress = findCondition(resource, 'Provisioned')
    if (progress?.status === 'False' && !isKueueConditionStale(progress, resource?.metadata?.generation) && progress.message) return progress.message
  }
  return condition?.message
}

export function getProvisioningRequestStatus(resource: any): StatusBadge {
  const condition = getProvisioningRequestStatusCondition(resource)
  if (isKueueConditionStale(condition, resource?.metadata?.generation)) {
    return { text: `${condition.type} (stale)`, color: healthColors.unknown, level: 'unknown' }
  }
  switch (condition?.type) {
    case 'Failed':
    case 'CapacityRevoked':
      return { text: condition.type, color: healthColors.unhealthy, level: 'unhealthy' }
    case 'BookingExpired':
      return { text: 'BookingExpired', color: healthColors.neutral, level: 'neutral' }
    case 'Provisioned':
      return { text: 'Provisioned', color: healthColors.healthy, level: 'healthy' }
    case 'Accepted':
      return { text: 'Accepted', color: healthColors.neutral, level: 'neutral' }
    default:
      return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getProvisioningRequestClassName(resource: any): string {
  return resource?.spec?.provisioningClassName || '-'
}

export function getProvisioningRequestPodSetCount(resource: any): number {
  return (resource?.spec?.podSets || []).length
}
