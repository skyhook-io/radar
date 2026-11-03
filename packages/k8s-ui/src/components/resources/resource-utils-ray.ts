// KubeRay CRD utility functions (ray.io/v1)

import type { WorkloadPodInfo } from '../../types'
import type { StatusBadge } from './resource-utils'
import { healthColors, formatAge } from './resource-utils'

// ============================================================================
// RAYCLUSTER UTILITIES
// ============================================================================

export function rayClusterWorkerCounts(resource: any): { ready?: number; running?: number; desired?: number } {
  const status = resource.status ?? {}
  // Native integer counters omit zero; observedGeneration witnesses their shared status write.
  const count = (key: string) => status[key] ?? (status.observedGeneration > 0 ? 0 : undefined)
  return { ready: count('readyWorkerReplicas'), running: count('availableWorkerReplicas'), desired: count('desiredWorkerReplicas') }
}

export function getRayClusterStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions ?? []
  const condition = (type: string) => conditions.find((c: any) => c.type === type)
  const badge = (text: string, level: StatusBadge['level']): StatusBadge => ({ text, level, color: healthColors[level!] })
  if (isRayObservationStale(resource.metadata?.generation, resource.status?.observedGeneration)) return badge('Status stale', 'unknown')
  if (condition('RayClusterSuspending')?.status === 'True') return badge('Suspending', 'degraded')
  if (condition('RayClusterSuspended')?.status === 'True') return badge('Suspended', 'neutral')
  if (resource.spec?.suspend === true) return badge('Suspension requested', 'neutral')
  const failure = condition('RayClusterReplicaFailure')
  if (failure?.status === 'True') return badge(failure.reason || 'Replica failure', 'unhealthy')
  if (resource.status?.state === 'failed') return badge('Failed', 'unhealthy')
  const head = condition('HeadPodReady')
  if (head?.status === 'False') return badge(head.reason || 'Head not ready', 'degraded')
  if (head?.status === 'True') {
    const { ready, running, desired } = rayClusterWorkerCounts(resource)
    if (ready == null || desired == null) return badge('Head ready', 'neutral')
    if (ready >= desired && (running == null || ready >= running)) return badge('Ready', 'healthy')
    if (ready >= desired && running != null && running > ready) return badge(`${running - ready} workers unready`, 'degraded')
    return badge(`Workers ${ready}/${desired} ready`, 'degraded')
  }
  if (condition('RayClusterProvisioned')?.status === 'True') return badge('Provisioned', 'neutral')
  return badge('Unknown', 'unknown')
}

export function getRayClusterVersion(resource: any): string {
  return resource.spec?.rayVersion || '-'
}

export function getRayClusterWorkers(resource: any): string {
  const { ready, desired } = rayClusterWorkerCounts(resource)
  return `${ready ?? '?'}/${desired ?? '?'}`
}

export function getRayClusterHeadService(resource: any): string {
  return resource.status?.head?.serviceName || '-'
}

// ============================================================================
// RAYJOB UTILITIES
// ============================================================================

export function getRayJobStatus(resource: any): StatusBadge {
  const jobStatus = resource.status?.jobStatus
  const deploymentStatus = resource.status?.jobDeploymentStatus

  if (deploymentStatus === 'Failed' || deploymentStatus === 'ValidationFailed') {
    return { text: deploymentStatus, color: healthColors.unhealthy, level: 'unhealthy' }
  }

  if (['Retrying', 'Suspending', 'Suspended'].includes(deploymentStatus)) {
    return deploymentStatus === 'Suspended'
      ? { text: deploymentStatus, color: healthColors.neutral, level: 'neutral' }
      : { text: deploymentStatus, color: healthColors.degraded, level: 'degraded' }
  }

  switch (jobStatus) {
    case 'SUCCEEDED':
      return { text: 'Succeeded', color: healthColors.neutral, level: 'neutral' }
    case 'RUNNING':
      return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
    case 'FAILED':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'STOPPED':
      return { text: 'Stopped', color: healthColors.neutral, level: 'neutral' }
    case 'PENDING':
      return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
  }

  switch (deploymentStatus) {
    case 'Complete':
      return { text: 'Complete', color: healthColors.neutral, level: 'neutral' }
    case 'Running':
      return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
    case 'Initializing':
    case 'Waiting':
      return { text: deploymentStatus, color: healthColors.degraded, level: 'degraded' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getRayJobJobStatus(resource: any): string {
  return resource.status?.jobStatus || '-'
}

export function getRayJobDeploymentStatus(resource: any): string {
  return resource.status?.jobDeploymentStatus || '-'
}

export function getRayJobClusterName(resource: any): string {
  return resource.status?.rayClusterName || '-'
}

// ============================================================================
// RAYSERVICE UTILITIES
// ============================================================================

export function isRayObservationStale(generation?: number, observedGeneration?: number): boolean {
  return generation != null && observedGeneration != null && observedGeneration < generation
}

// KubeRay can freeze observedGeneration during validation failures and suspension.
export function isRayServiceConditionStale(resource: any, condition: any): boolean {
  return condition?.status === 'True' && ['Ready', 'UpgradeInProgress', 'RollbackInProgress'].includes(condition.type)
    && isRayObservationStale(resource.metadata?.generation, resource.status?.observedGeneration)
}

export function getRayServiceStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions ?? []
  const labels: Record<string, string> = { Suspending: 'Suspending', Suspended: 'Suspended', RollbackInProgress: 'RollingBack', UpgradeInProgress: 'Upgrading' }
  for (const type of ['Suspending', 'Suspended', 'RollbackInProgress', 'UpgradeInProgress', 'Ready']) {
    const condition = conditions.find((c: any) => c.type === type)
    if (!condition || (type !== 'Ready' && condition.status !== 'True')) continue
    const label = type === 'Ready' ? (condition.status === 'True' ? 'Ready' : condition.status === 'False' ? condition.reason || 'NotReady' : 'Unknown') : labels[type]
    if (isRayServiceConditionStale(resource, condition)) {
      return { text: `${label} (stale)`, color: healthColors.unknown, level: 'unknown' }
    }
    if (type === 'Ready') {
      if (condition.status === 'True') return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
      if (condition.status === 'False') return { text: condition.reason || 'NotReady', color: healthColors.degraded, level: 'degraded' }
      return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
    }
    const level = type === 'Suspended' ? 'neutral' : 'degraded'
    return { text: labels[type], color: healthColors[level], level }
  }
  if (resource.spec?.suspend === true) return { text: 'Suspension requested', color: healthColors.neutral, level: 'neutral' }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getRayServiceServiceStatus(resource: any): string {
  return resource.status?.serviceStatus || '-'
}

export function getRayServiceClusters(resource: any): string {
  const active = resource.status?.activeServiceStatus?.rayClusterName
  const pending = resource.status?.pendingServiceStatus?.rayClusterName
  const parts: string[] = []
  if (active) parts.push(active)
  if (pending) parts.push(`pending: ${pending}`)
  return parts.join(' ') || '-'
}

// ============================================================================
// RAYCRONJOB UTILITIES
// ============================================================================

export function getRayCronJobStatus(resource: any): StatusBadge {
  if (resource.spec?.suspend === true) {
    return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
  }
  return { text: 'Active', color: healthColors.neutral, level: 'neutral' }
}

export function getRayCronJobSchedule(resource: any): string {
  return resource.spec?.schedule || '-'
}

// An unset timeZone does not mean UTC: KubeRay reads the schedule in the
// operator pod's own local zone, which this object cannot report.
export function getRayCronJobTimeZone(resource: any): string {
  return resource.spec?.timeZone || 'Operator local'
}

export function getRayCronJobSuspend(resource: any): boolean {
  return resource.spec?.suspend === true
}

export function getRayCronJobLastSchedule(resource: any): string {
  const lastSchedule = resource.status?.lastScheduleTime
  if (!lastSchedule) return '-'
  return formatAge(lastSchedule)
}

export function rayJobIsLocal(resource: any): boolean {
  return !resource.spec?.managedBy || resource.spec.managedBy === 'ray.io/kuberay-operator'
}

export function rayJobSubmissionMode(resource: any): string {
  return resource.spec?.submissionMode || 'K8sJobMode'
}

/** The KubeRay RayCluster Radar reads Pods for: `ray.io/v1`, or group `ray.io` before the object loads. */
export function isKubeRayClusterIdentity(group: string | undefined, apiVersion: string | undefined): boolean {
  return apiVersion ? apiVersion === 'ray.io/v1' : group === 'ray.io'
}

/** Why a Pod has no container output to read yet, or undefined when it may. */
export function podLogsUnavailableReason(pod: WorkloadPodInfo): string | undefined {
  // A native sidecar's status alone doesn't mean the Pod's own containers have one.
  if (pod.containerStatuses?.some(status => !status.sidecar)) return undefined
  if (pod.phase === 'Failed' || pod.phase === 'Succeeded') return 'Ended before starting'
  return pod.nodeName ? 'No status yet' : 'Not scheduled'
}

// The container a Pod's logs open on. Container lists put init containers
// first and only reported statuses say which those are, so with no statuses
// the viewer keeps its own default. A regular init container that isn't done
// while the Pod is Pending is what it waits on (a worker waiting for the head
// sits in KubeRay's wait-gcs-ready), though it may not have started yet; in a
// Failed Pod it is what failed, or what never ran if the Pod was evicted
// first. Native sidecars never finish, so they are never that container.
// Without sidecar marks (an older Radar) an unready init container can't be
// told from a sidecar, so one is chosen only in a Pending Pod and only when it
// is the sole unready init container.
export function logsContainer(pod: WorkloadPodInfo, sidecarsMarked: boolean): string | undefined {
  const statuses = pod.containerStatuses ?? []
  if (!statuses.length) return undefined
  const init = new Set(statuses.filter(status => status.init).map(status => status.name))
  const unfinished = pod.containers.filter(name => statuses.some(status => status.init && status.name === name && !status.ready && !(sidecarsMarked && status.sidecar)))
  const blocking = sidecarsMarked
    ? (pod.phase === 'Pending' || pod.phase === 'Failed' ? unfinished[0] : undefined)
    : (pod.phase === 'Pending' && unfinished.length === 1 ? unfinished[0] : undefined)
  return blocking ?? pod.containers.find(name => !init.has(name))
}
