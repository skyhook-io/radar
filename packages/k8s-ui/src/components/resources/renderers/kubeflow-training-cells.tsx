// Kubeflow training cell components for ResourcesView table

import { clsx } from 'clsx'
import type { StatusBadge } from '../resource-utils'
import {
  getPyTorchJobStatus,
  getTFJobStatus,
  getMPIJobStatus,
  getTrainingJobReplicas,
  getTrainingJobElapsed,
  getTrainJobStatus,
  getTrainJobRuntime,
  getTrainJobSuspended,
} from '../resource-utils-kubeflow-training'

function TrainingOperatorJobCell({ resource, column, getStatus }: { resource: any; column: string; getStatus: (resource: any) => StatusBadge }) {
  switch (column) {
    case 'status': {
      const status = getStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'replicas': {
      const replicas = getTrainingJobReplicas(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{replicas}</span>
    }
    case 'elapsed': {
      const elapsed = getTrainingJobElapsed(resource)
      return <span className="text-sm text-theme-text-secondary">{elapsed}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function PyTorchJobCell({ resource, column }: { resource: any; column: string }) {
  return <TrainingOperatorJobCell resource={resource} column={column} getStatus={getPyTorchJobStatus} />
}

export function TFJobCell({ resource, column }: { resource: any; column: string }) {
  return <TrainingOperatorJobCell resource={resource} column={column} getStatus={getTFJobStatus} />
}

export function MPIJobCell({ resource, column }: { resource: any; column: string }) {
  return <TrainingOperatorJobCell resource={resource} column={column} getStatus={getMPIJobStatus} />
}

export function TrainJobCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getTrainJobStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'runtime': {
      const runtime = getTrainJobRuntime(resource)
      return <span className="text-sm text-theme-text-secondary">{runtime}</span>
    }
    case 'suspended': {
      const suspended = getTrainJobSuspended(resource)
      return <span className="text-sm text-theme-text-secondary">{suspended ? 'Yes' : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
