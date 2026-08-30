// Kueue + Cluster Autoscaler ProvisioningRequest cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getClusterQueueStatus,
  getClusterQueueCohort,
  getClusterQueuePendingWorkloads,
  getClusterQueueAdmittedWorkloads,
  getClusterQueueFlavors,
  getLocalQueueStatus,
  getLocalQueueClusterQueue,
  getLocalQueuePendingWorkloads,
  getLocalQueueAdmittedWorkloads,
  getKueueWorkloadStatus,
  getKueueWorkloadQueueName,
  getKueueWorkloadAdmittedBy,
  getKueueWorkloadPriority,
  getResourceFlavorStatus,
  getResourceFlavorNodeLabelCount,
  getResourceFlavorTaintCount,
  getAdmissionCheckStatus,
  getAdmissionCheckControllerName,
  getProvisioningRequestStatus,
  getProvisioningRequestClassName,
  getProvisioningRequestPodSetCount,
} from '../resource-utils-kueue'

export function ClusterQueueCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getClusterQueueStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'cohort': {
      const cohort = getClusterQueueCohort(resource)
      return <span className="text-sm text-theme-text-secondary">{cohort}</span>
    }
    case 'pendingWorkloads': {
      const pending = getClusterQueuePendingWorkloads(resource)
      return <span className="text-sm text-theme-text-secondary">{pending}</span>
    }
    case 'admittedWorkloads': {
      const admitted = getClusterQueueAdmittedWorkloads(resource)
      return <span className="text-sm text-theme-text-secondary">{admitted}</span>
    }
    case 'flavors': {
      const flavors = getClusterQueueFlavors(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{flavors}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function LocalQueueCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getLocalQueueStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'clusterQueue': {
      const clusterQueue = getLocalQueueClusterQueue(resource)
      return <span className="text-sm text-theme-text-secondary">{clusterQueue}</span>
    }
    case 'pendingWorkloads': {
      const pending = getLocalQueuePendingWorkloads(resource)
      return <span className="text-sm text-theme-text-secondary">{pending}</span>
    }
    case 'admittedWorkloads': {
      const admitted = getLocalQueueAdmittedWorkloads(resource)
      return <span className="text-sm text-theme-text-secondary">{admitted}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function KueueWorkloadCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getKueueWorkloadStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'queueName': {
      const queueName = getKueueWorkloadQueueName(resource)
      return <span className="text-sm text-theme-text-secondary">{queueName}</span>
    }
    case 'admittedBy': {
      const admittedBy = getKueueWorkloadAdmittedBy(resource)
      return <span className="text-sm text-theme-text-secondary">{admittedBy}</span>
    }
    case 'priority': {
      const priority = getKueueWorkloadPriority(resource)
      return <span className="text-sm text-theme-text-secondary">{priority}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ResourceFlavorCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getResourceFlavorStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'nodeLabels': {
      const count = getResourceFlavorNodeLabelCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    case 'taints': {
      const count = getResourceFlavorTaintCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function AdmissionCheckCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getAdmissionCheckStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'controllerName': {
      const controllerName = getAdmissionCheckControllerName(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{controllerName}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ProvisioningRequestCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getProvisioningRequestStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'provisioningClassName': {
      const className = getProvisioningRequestClassName(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{className}</span>
    }
    case 'podSets': {
      const count = getProvisioningRequestPodSetCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
