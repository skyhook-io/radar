// AWS CAPI Infrastructure Provider cell components for ResourcesView table

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import { CAPACITY_TYPE_BADGE } from '../../../utils/badge-colors'
import {
  getAWSMCPStatus, getAWSMCPEKSClusterName, getAWSMCPRegion, getAWSMCPVersion,
  getAWSMMPStatus, getAWSMMPInstanceType, getAWSMMPReplicas, getAWSMMPCapacityType,
  getAWSMachineStatus, getAWSMachineInstanceType, getAWSMachineInstanceState, getAWSMachineInstanceID,
  getAWSMTInstanceType, getAWSMTCapacity,
  getAWSManagedClusterStatus, getAWSManagedClusterEndpoint,
} from '../resource-utils-aws-capi'

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

function MonoCell({ value }: { value: string }) {
  return (
    <Tooltip content={value}>
      <span className="text-xs text-theme-text-secondary font-mono truncate max-w-[180px] inline-block">{value}</span>
    </Tooltip>
  )
}

export function AWSManagedControlPlaneCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getAWSMCPStatus} />
    case 'eksCluster':
      return <TextCell value={getAWSMCPEKSClusterName(resource)} />
    case 'region':
      return <TextCell value={getAWSMCPRegion(resource)} />
    case 'version':
      return <TextCell value={getAWSMCPVersion(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function AWSManagedMachinePoolCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getAWSMMPStatus} />
    case 'instanceType':
      return <TextCell value={getAWSMMPInstanceType(resource)} />
    case 'replicas':
      return <TextCell value={getAWSMMPReplicas(resource)} />
    case 'capacityType': {
      const ct = getAWSMMPCapacityType(resource)
      return (
        <span className={clsx('badge badge-sm', ct === 'spot' ? CAPACITY_TYPE_BADGE.spot : CAPACITY_TYPE_BADGE.onDemand)}>{ct === 'onDemand' ? 'On-Demand' : ct}</span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function AWSMachineCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getAWSMachineStatus} />
    case 'instanceType':
      return <TextCell value={getAWSMachineInstanceType(resource)} />
    case 'instanceState': {
      const state = getAWSMachineInstanceState(resource)
      return (
        <span className={clsx('badge badge-sm', state === 'running'
          ? 'status-healthy'
          : state === 'pending' || state === 'stopping' || state === 'stopped'
          ? 'status-degraded'
          : state === 'terminated' || state === 'shutting-down'
          ? 'status-unhealthy'
          : 'status-neutral'
        )}>{state}</span>
      )
    }
    case 'instanceID':
      return <MonoCell value={getAWSMachineInstanceID(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function AWSMachineTemplateCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'instanceType':
      return <TextCell value={getAWSMTInstanceType(resource)} />
    case 'capacity':
      return <TextCell value={getAWSMTCapacity(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function AWSManagedClusterCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getAWSManagedClusterStatus} />
    case 'endpoint':
      return <MonoCell value={getAWSManagedClusterEndpoint(resource)} />
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
