// Azure CAPI Infrastructure Provider cell components for ResourcesView table

import { Tooltip } from '../../ui/Tooltip'
import { clsx } from 'clsx'
import { CAPACITY_TYPE_BADGE, NODEPOOL_MODE_BADGE } from '../../../utils/badge-colors'
import {
  getAzureMCPStatus, getAzureMCPLocation, getAzureMCPVersion, getAzureMCPResourceGroup,
  getAzureMMPStatus, getAzureMMPSKU, getAzureMMPMode, getAzureMMPReplicas, getAzureMMPScaleSetPriority,
  getAzureMachineStatus, getAzureMachineVMSize,
  getAzureMTVMSize,
  getAzureManagedClusterStatus,
} from '../resource-utils-azure-capi'

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

export function AzureManagedControlPlaneCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': return <StatusBadge resource={resource} getStatus={getAzureMCPStatus} />
    case 'location': return <TextCell value={getAzureMCPLocation(resource)} />
    case 'resourceGroup': return <TextCell value={getAzureMCPResourceGroup(resource)} />
    case 'version': return <TextCell value={getAzureMCPVersion(resource)} />
    default: return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function AzureManagedMachinePoolCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': return <StatusBadge resource={resource} getStatus={getAzureMMPStatus} />
    case 'sku': return <TextCell value={getAzureMMPSKU(resource)} />
    case 'mode': {
      const mode = getAzureMMPMode(resource)
      return (
        <span className={clsx('badge badge-sm', NODEPOOL_MODE_BADGE[mode] || NODEPOOL_MODE_BADGE.User)}>{mode}</span>
      )
    }
    case 'replicas': return <TextCell value={getAzureMMPReplicas(resource)} />
    case 'priority': {
      const p = getAzureMMPScaleSetPriority(resource)
      return (
        <span className={clsx('badge badge-sm', p === 'Spot' ? CAPACITY_TYPE_BADGE.spot : CAPACITY_TYPE_BADGE.regular)}>{p}</span>
      )
    }
    default: return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function AzureMachineCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': return <StatusBadge resource={resource} getStatus={getAzureMachineStatus} />
    case 'vmSize': return <TextCell value={getAzureMachineVMSize(resource)} />
    default: return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function AzureMachineTemplateCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'vmSize': return <TextCell value={getAzureMTVMSize(resource)} />
    default: return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function AzureManagedClusterCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': return <StatusBadge resource={resource} getStatus={getAzureManagedClusterStatus} />
    default: return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
