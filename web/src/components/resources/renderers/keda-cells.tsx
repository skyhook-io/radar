// KEDA cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getScaledObjectStatus,
  getScaledObjectTarget,
  getScaledObjectReplicas,
  getScaledObjectTriggerTypes,
  getScaledJobStatus,
  getScaledJobTarget,
  getScaledJobStrategy,
  getScaledJobTriggerTypes,
  getTriggerAuthSecretRefCount,
  getTriggerAuthEnvCount,
  getTriggerAuthHasVault,
} from '../resource-utils-keda'

export function ScaledObjectCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getScaledObjectStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'target': {
      const target = getScaledObjectTarget(resource)
      return <span className="text-sm text-theme-text-secondary">{target}</span>
    }
    case 'replicas': {
      const replicas = getScaledObjectReplicas(resource)
      return <span className="text-sm text-theme-text-secondary">{replicas}</span>
    }
    case 'triggerTypes': {
      const types = getScaledObjectTriggerTypes(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{types}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ScaledJobCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getScaledJobStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'target': {
      const target = getScaledJobTarget(resource)
      return <span className="text-sm text-theme-text-secondary">{target}</span>
    }
    case 'strategy': {
      const strategy = getScaledJobStrategy(resource)
      return <span className="text-sm text-theme-text-secondary">{strategy}</span>
    }
    case 'triggerTypes': {
      const types = getScaledJobTriggerTypes(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{types}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function TriggerAuthenticationCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'secretTargetRef': {
      const count = getTriggerAuthSecretRefCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    case 'env': {
      const count = getTriggerAuthEnvCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    case 'hashiCorpVault': {
      const hasVault = getTriggerAuthHasVault(resource)
      return <span className="text-sm text-theme-text-secondary">{hasVault ? 'Yes' : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ClusterTriggerAuthenticationCell({ resource, column }: { resource: any; column: string }) {
  // Same rendering logic as TriggerAuthentication
  return <TriggerAuthenticationCell resource={resource} column={column} />
}
