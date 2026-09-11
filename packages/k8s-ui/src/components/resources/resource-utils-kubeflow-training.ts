// Kubeflow training CRD utility functions
// Covers training-operator v1 (PyTorchJob/TFJob/MPIJob at kubeflow.org/v1),
// mpi-operator (MPIJob at kubeflow.org/v2beta1), and Trainer v2 (TrainJob at trainer.kubeflow.org/v1alpha1)

import type { StatusBadge } from './resource-utils'
import { healthColors, formatDuration } from './resource-utils'

// ============================================================================
// SHARED HELPERS (kubeflow.org JobCondition pattern)
// ============================================================================

const REPLICA_SPEC_KEYS = ['pytorchReplicaSpecs', 'tfReplicaSpecs', 'mpiReplicaSpecs']

function getReplicaSpecs(resource: any): Record<string, any> {
  const spec = resource.spec || {}
  for (const key of REPLICA_SPEC_KEYS) {
    if (spec[key]) return spec[key]
  }
  return {}
}

export function trainingJobStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []
  const isTrue = (type: string) => conditions.some((condition: any) => condition?.type === type && condition?.status === 'True')
  if (isTrue('Failed')) {
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (isTrue('Succeeded')) {
    return { text: 'Succeeded', color: healthColors.neutral, level: 'neutral' }
  }
  if (isTrue('Suspended')) {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }
  if (isTrue('Restarting')) {
    return { text: 'Restarting', color: healthColors.degraded, level: 'degraded' }
  }
  if (isTrue('Running')) {
    return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
  }
  return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
}

export function getTrainingJobReplicas(resource: any): string {
  const specs = getReplicaSpecs(resource)
  const statuses = resource.status?.replicaStatuses || {}
  const types = Object.keys(specs)
  for (const type of Object.keys(statuses)) {
    if (!types.includes(type)) types.push(type)
  }
  if (types.length === 0) return '-'
  return types
    .map((type) => {
      const status = statuses[type] || {}
      const progress = (status.active ?? 0) + (status.succeeded ?? 0)
      const failed = status.failed ?? 0
      const desired = specs[type]?.replicas
      // Unset spec replicas are defaulted in-controller (and the default
      // differs per operator and replica type), so omit the denominator
      // rather than guess
      const count = desired == null ? `${type} ${progress}` : `${type} ${progress}/${desired}`
      return failed > 0 ? `${count} (${failed} failed)` : count
    })
    .join(', ')
}

export function getTrainingJobElapsed(resource: any): string {
  const startTime = resource.status?.startTime
  if (!startTime) return '-'
  const start = new Date(startTime)
  const completionTime = resource.status?.completionTime
  const end = completionTime ? new Date(completionTime) : new Date()
  return formatDuration(end.getTime() - start.getTime())
}

// ============================================================================
// PYTORCHJOB / TFJOB / MPIJOB STATUS
// ============================================================================

export function getPyTorchJobStatus(resource: any): StatusBadge {
  return trainingJobStatus(resource)
}

export function getTFJobStatus(resource: any): StatusBadge {
  return trainingJobStatus(resource)
}

export function getMPIJobStatus(resource: any): StatusBadge {
  return trainingJobStatus(resource)
}

// ============================================================================
// TRAINJOB (trainer.kubeflow.org/v1alpha1)
// ============================================================================

export function getTrainJobStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []
  const isTrue = (type: string) => conditions.some((c: any) => c?.type === type && c?.status === 'True')

  if (isTrue('Failed')) {
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (isTrue('Complete')) {
    return { text: 'Complete', color: healthColors.neutral, level: 'neutral' }
  }
  if (isTrue('Suspended') || resource.spec?.suspend === true) {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }
  const jobsStatus = Array.isArray(resource.status?.jobsStatus) ? resource.status.jobsStatus : []
  if (jobsStatus.some((job: any) => Number(job?.active) > 0)) {
    return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
  }
  return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
}

export function getTrainJobRuntime(resource: any): string {
  return resource.spec?.runtimeRef?.name || '-'
}

export function getTrainJobSuspended(resource: any): boolean {
  return resource.spec?.suspend === true
}
