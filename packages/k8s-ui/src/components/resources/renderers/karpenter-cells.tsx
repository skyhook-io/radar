// Karpenter cell components for ResourcesView table

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import {
  getNodePoolStatus,
  getNodePoolNodeClassRef,
  getNodePoolLimits,
  getNodePoolDisruptionPolicy,
  getNodeClaimStatus,
  getNodeClaimInstanceType,
  getNodeClaimNodeName,
  getNodeClaimNodePoolRef,
  getEC2NodeClassStatus,
  getEC2NodeClassAMI,
  getEC2NodeClassRole,
  getEC2NodeClassVolumeSize,
} from '../resource-utils-karpenter'

export function NodePoolCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getNodePoolStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'nodeClass': {
      const ref = getNodePoolNodeClassRef(resource)
      return <span className="text-sm text-theme-text-secondary">{ref}</span>
    }
    case 'limits': {
      const limits = getNodePoolLimits(resource)
      return (
        <Tooltip content={limits}>
          <span className="text-sm text-theme-text-secondary truncate block">{limits}</span>
        </Tooltip>
      )
    }
    case 'disruption': {
      const policy = getNodePoolDisruptionPolicy(resource)
      return <span className="text-sm text-theme-text-secondary">{policy}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function NodeClaimCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getNodeClaimStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'instanceType': {
      const instanceType = getNodeClaimInstanceType(resource)
      return <span className="text-sm text-theme-text-secondary">{instanceType}</span>
    }
    case 'nodeName': {
      const name = getNodeClaimNodeName(resource)
      return <span className="text-sm text-theme-text-secondary">{name}</span>
    }
    case 'nodePool': {
      const pool = getNodeClaimNodePoolRef(resource)
      return <span className="text-sm text-theme-text-secondary">{pool}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function EC2NodeClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getEC2NodeClassStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'ami': {
      const ami = getEC2NodeClassAMI(resource)
      return <span className="text-sm text-theme-text-secondary">{ami}</span>
    }
    case 'role': {
      const role = getEC2NodeClassRole(resource)
      return (
        <Tooltip content={role}>
          <span className="text-sm text-theme-text-secondary truncate block">{role}</span>
        </Tooltip>
      )
    }
    case 'volumeSize': {
      const size = getEC2NodeClassVolumeSize(resource)
      return <span className="text-sm text-theme-text-secondary">{size}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
