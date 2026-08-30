// Volcano cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getVolcanoJobStatus,
  getVolcanoJobQueue,
  getVolcanoJobMinAvailable,
  getVolcanoJobPodCounts,
  getVolcanoQueueStatus,
  getVolcanoQueueWeight,
  getVolcanoQueueCapability,
  getVolcanoQueueAllocated,
  getVolcanoQueuePodGroupCounts,
  getVolcanoPodGroupStatus,
  getVolcanoPodGroupQueue,
  getVolcanoPodGroupMinMember,
  getJobFlowStatus,
  getJobFlowFlowCount,
  getJobTemplateStatus,
  getJobTemplateTaskCount,
} from '../resource-utils-volcano'

export function VolcanoJobCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getVolcanoJobStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'queue': {
      const queue = getVolcanoJobQueue(resource)
      return <span className="text-sm text-theme-text-secondary">{queue}</span>
    }
    case 'minAvailable': {
      const minAvailable = getVolcanoJobMinAvailable(resource)
      return <span className="text-sm text-theme-text-secondary">{minAvailable}</span>
    }
    case 'pods': {
      const counts = getVolcanoJobPodCounts(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{counts}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function VolcanoQueueCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getVolcanoQueueStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'weight': {
      const weight = getVolcanoQueueWeight(resource)
      return <span className="text-sm text-theme-text-secondary">{weight}</span>
    }
    case 'capability': {
      const capability = getVolcanoQueueCapability(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{capability}</span>
    }
    case 'allocated': {
      const allocated = getVolcanoQueueAllocated(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{allocated}</span>
    }
    case 'podGroups': {
      const counts = getVolcanoQueuePodGroupCounts(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{counts}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function VolcanoPodGroupCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getVolcanoPodGroupStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'queue': {
      const queue = getVolcanoPodGroupQueue(resource)
      return <span className="text-sm text-theme-text-secondary">{queue}</span>
    }
    case 'minMember': {
      const minMember = getVolcanoPodGroupMinMember(resource)
      return <span className="text-sm text-theme-text-secondary">{minMember}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function JobFlowCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getJobFlowStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'flows': {
      const count = getJobFlowFlowCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function JobTemplateCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getJobTemplateStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'tasks': {
      const count = getJobTemplateTaskCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
