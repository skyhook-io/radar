// Cluster API (CAPI) cell components for ResourcesView table

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import {
  getClusterStatus, getClusterClass, getClusterVersion, getClusterCPReplicas, getClusterWorkerReplicas,
  getMachineStatus, getMachineRole, getMachineClusterName, getMachineNodeRef, getMachineVersion,
  getMachineDeploymentStatus, getMachineDeploymentReplicas, getMachineDeploymentVersion,
  getMachineSetStatus, getMachineSetReplicas,
  getMachinePoolStatus, getMachinePoolReplicas,
  getKCPStatus, getKCPReplicas, getKCPVersion, getKCPInitialized,
  getClusterClassStatus,
  getMachineHealthCheckStatus, getMachineHealthCheckHealthy, getMachineHealthCheckClusterName,
  getClusterProvider,
  getMachineDeploymentUpToDate,
} from '../resource-utils-capi'

function StatusBadge({ resource, getStatus }: { resource: any; getStatus: (r: any) => { text: string; color: string } }) {
  const status = getStatus(resource)
  return (
    <Tooltip content={status.text}>
      <span className={clsx('badge truncate max-w-[140px]', status.color)}>{status.text}</span>
    </Tooltip>
  )
}

function TextCell({ value }: { value: string }) {
  return <span className="text-sm text-theme-text-secondary">{value}</span>
}

export function CAPIClusterCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'phase':
      return <StatusBadge resource={resource} getStatus={getClusterStatus} />
    case 'provider':
      return <TextCell value={getClusterProvider(resource)} />
    case 'class':
      return <TextCell value={getClusterClass(resource)} />
    case 'cpReplicas':
      return <TextCell value={getClusterCPReplicas(resource)} />
    case 'workerReplicas':
      return <TextCell value={getClusterWorkerReplicas(resource)} />
    case 'version':
      return <TextCell value={getClusterVersion(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CAPIMachineCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'phase':
      return <StatusBadge resource={resource} getStatus={getMachineStatus} />
    case 'cluster':
      return <TextCell value={getMachineClusterName(resource)} />
    case 'role': {
      const role = getMachineRole(resource)
      return (
        <span className={clsx('badge badge-sm', role === 'Control Plane'
          ? 'bg-purple-100 text-purple-800 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40'
          : 'bg-sky-100 text-sky-700 border-sky-300 dark:bg-sky-950/50 dark:text-sky-400 dark:border-sky-700/40'
        )}>{role}</span>
      )
    }
    case 'node':
      return <TextCell value={getMachineNodeRef(resource)} />
    case 'version':
      return <TextCell value={getMachineVersion(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CAPIMachineDeploymentCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'phase':
      return <StatusBadge resource={resource} getStatus={getMachineDeploymentStatus} />
    case 'cluster':
      return <TextCell value={getMachineClusterName(resource)} />
    case 'ready':
      return <TextCell value={getMachineDeploymentReplicas(resource)} />
    case 'upToDate':
      return <TextCell value={getMachineDeploymentUpToDate(resource)} />
    case 'version':
      return <TextCell value={getMachineDeploymentVersion(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CAPIMachineSetCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'phase':
      return <StatusBadge resource={resource} getStatus={getMachineSetStatus} />
    case 'cluster':
      return <TextCell value={getMachineClusterName(resource)} />
    case 'ready':
      return <TextCell value={getMachineSetReplicas(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CAPIMachinePoolCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'phase':
      return <StatusBadge resource={resource} getStatus={getMachinePoolStatus} />
    case 'cluster':
      return <TextCell value={getMachineClusterName(resource)} />
    case 'ready':
      return <TextCell value={getMachinePoolReplicas(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CAPIKubeadmControlPlaneCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getKCPStatus} />
    case 'cluster':
      return <TextCell value={getMachineClusterName(resource)} />
    case 'ready':
      return <TextCell value={getKCPReplicas(resource)} />
    case 'initialized': {
      const init = getKCPInitialized(resource)
      return (
        <span className={clsx('badge badge-sm', init
          ? 'bg-emerald-100 text-emerald-800 border-emerald-300 dark:bg-emerald-950/50 dark:text-emerald-400 dark:border-emerald-700/40'
          : 'bg-amber-100 text-amber-800 border-amber-300 dark:bg-amber-950/50 dark:text-amber-400 dark:border-amber-700/40'
        )}>{init ? 'Yes' : 'No'}</span>
      )
    }
    case 'version':
      return <TextCell value={getKCPVersion(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CAPIClusterClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getClusterClassStatus} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CAPIMachineHealthCheckCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getMachineHealthCheckStatus} />
    case 'cluster':
      return <TextCell value={getMachineHealthCheckClusterName(resource)} />
    case 'healthy':
      return <TextCell value={getMachineHealthCheckHealthy(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
