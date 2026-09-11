// KAI Scheduler cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getKaiQueueStatus,
  getKaiQueueParent,
  getKaiQueuePriority,
  getKaiQueueQuota,
  getKaiQueueAllocated,
  getKaiPodGroupStatus,
  getKaiPodGroupQueue,
  getKaiPodGroupMinMember,
} from '../resource-utils-kai'

export function KaiQueueCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getKaiQueueStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'parentQueue': {
      const parent = getKaiQueueParent(resource)
      return <span className="text-sm text-theme-text-secondary">{parent}</span>
    }
    case 'priority': {
      const priority = getKaiQueuePriority(resource)
      return <span className="text-sm text-theme-text-secondary">{priority}</span>
    }
    case 'quota': {
      const quota = getKaiQueueQuota(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{quota}</span>
    }
    case 'allocated': {
      const allocated = getKaiQueueAllocated(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{allocated}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function KaiPodGroupCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getKaiPodGroupStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'queue': {
      const queue = getKaiPodGroupQueue(resource)
      return <span className="text-sm text-theme-text-secondary">{queue}</span>
    }
    case 'minMember': {
      const minMember = getKaiPodGroupMinMember(resource)
      return <span className="text-sm text-theme-text-secondary">{minMember}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
