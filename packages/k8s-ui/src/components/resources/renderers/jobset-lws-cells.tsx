// LeaderWorkerSet and JobSet cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getLeaderWorkerSetStatus,
  getLeaderWorkerSetReplicas,
  getLeaderWorkerSetSize,
  getLeaderWorkerSetReady,
  getLeaderWorkerSetUpdated,
  getJobSetStatus,
  getJobSetReplicatedJobs,
  getJobSetReadyJobs,
  getJobSetSucceededJobs,
  getJobSetFailedJobs,
  getJobSetRestarts,
} from '../resource-utils-jobset-lws'

export function LeaderWorkerSetCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getLeaderWorkerSetStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'replicas': {
      const replicas = getLeaderWorkerSetReplicas(resource)
      return <span className="text-sm text-theme-text-secondary">{replicas}</span>
    }
    case 'size': {
      const size = getLeaderWorkerSetSize(resource)
      return <span className="text-sm text-theme-text-secondary">{size}</span>
    }
    case 'ready': {
      const ready = getLeaderWorkerSetReady(resource)
      return <span className="text-sm text-theme-text-secondary">{ready}</span>
    }
    case 'updated': {
      const updated = getLeaderWorkerSetUpdated(resource)
      return <span className="text-sm text-theme-text-secondary">{updated}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function JobSetCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getJobSetStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'replicatedJobs': {
      const count = getJobSetReplicatedJobs(resource)
      return <span className="text-sm text-theme-text-secondary">{count}</span>
    }
    case 'ready': {
      const ready = getJobSetReadyJobs(resource)
      return <span className="text-sm text-theme-text-secondary">{ready}</span>
    }
    case 'succeeded': {
      const succeeded = getJobSetSucceededJobs(resource)
      return <span className="text-sm text-theme-text-secondary">{succeeded}</span>
    }
    case 'failed': {
      const failed = getJobSetFailedJobs(resource)
      return <span className={clsx('text-sm', failed > 0 ? 'text-theme-text-primary' : 'text-theme-text-secondary')}>{failed}</span>
    }
    case 'restarts': {
      const restarts = getJobSetRestarts(resource)
      return <span className="text-sm text-theme-text-secondary">{restarts}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
