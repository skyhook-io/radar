// KEDA cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getScaledObjectStatus,
  getScaledObjectTarget,
  getScaledObjectReplicas,
  getScaledObjectTriggerCount,
  getScaledJobStatus,
  getScaledJobTarget,
  getScaledJobStrategy,
  getScaledJobTriggerCount,
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
    case 'triggers': {
      const count = getScaledObjectTriggerCount(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count > 0 ? count : '-'}
        </span>
      )
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
    case 'triggers': {
      const count = getScaledJobTriggerCount(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count > 0 ? count : '-'}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
