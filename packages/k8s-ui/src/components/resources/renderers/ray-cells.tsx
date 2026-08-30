// KubeRay cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getRayClusterStatus,
  getRayClusterVersion,
  getRayClusterWorkers,
  getRayClusterHeadService,
  getRayJobStatus,
  getRayJobJobStatus,
  getRayJobDeploymentStatus,
  getRayJobClusterName,
  getRayServiceStatus,
  getRayServiceServiceStatus,
  getRayServiceClusters,
  getRayCronJobStatus,
  getRayCronJobSchedule,
  getRayCronJobSuspend,
  getRayCronJobLastSchedule,
} from '../resource-utils-ray'

export function RayClusterCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getRayClusterStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'rayVersion': {
      const version = getRayClusterVersion(resource)
      return <span className="text-sm text-theme-text-secondary">{version}</span>
    }
    case 'workers': {
      const workers = getRayClusterWorkers(resource)
      return <span className="text-sm text-theme-text-secondary">{workers}</span>
    }
    case 'headService': {
      const svc = getRayClusterHeadService(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{svc}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function RayJobCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getRayJobStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'jobStatus': {
      const jobStatus = getRayJobJobStatus(resource)
      return <span className="text-sm text-theme-text-secondary">{jobStatus}</span>
    }
    case 'deploymentStatus': {
      const deploymentStatus = getRayJobDeploymentStatus(resource)
      return <span className="text-sm text-theme-text-secondary">{deploymentStatus}</span>
    }
    case 'cluster': {
      const cluster = getRayJobClusterName(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{cluster}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function RayServiceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getRayServiceStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'serviceStatus': {
      const serviceStatus = getRayServiceServiceStatus(resource)
      return <span className="text-sm text-theme-text-secondary">{serviceStatus}</span>
    }
    case 'clusters': {
      const clusters = getRayServiceClusters(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{clusters}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function RayCronJobCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getRayCronJobStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'schedule': {
      const schedule = getRayCronJobSchedule(resource)
      return <span className="text-sm font-mono text-theme-text-secondary">{schedule}</span>
    }
    case 'suspend': {
      const suspended = getRayCronJobSuspend(resource)
      return <span className="text-sm text-theme-text-secondary">{suspended ? 'Yes' : 'No'}</span>
    }
    case 'lastSchedule': {
      const lastSchedule = getRayCronJobLastSchedule(resource)
      return <span className="text-sm text-theme-text-secondary">{lastSchedule}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
