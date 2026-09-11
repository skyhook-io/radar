// NVIDIA NIM Operator cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getNIMServiceStatus,
  getNIMServiceModel,
  getNIMServiceReplicas,
  getNIMCacheStatus,
  getNIMCacheSourceType,
  getNIMCacheModelSource,
  getNIMCacheStorageSize,
  getNIMPipelineStatus,
  getNIMPipelineServiceCount,
} from '../resource-utils-nim'

export function NIMServiceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getNIMServiceStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'model': {
      const model = getNIMServiceModel(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{model}</span>
    }
    case 'replicas': {
      const replicas = getNIMServiceReplicas(resource)
      return <span className="text-sm text-theme-text-secondary">{replicas}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function NIMCacheCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getNIMCacheStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'source': {
      const type = getNIMCacheSourceType(resource)
      const model = getNIMCacheModelSource(resource)
      const text = type !== '-' && model !== '-' ? `${type}: ${model}` : model
      return <span className="text-sm text-theme-text-secondary truncate block">{text}</span>
    }
    case 'storage': {
      const size = getNIMCacheStorageSize(resource)
      return <span className="text-sm text-theme-text-secondary">{size}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function NIMPipelineCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getNIMPipelineStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'services': {
      const count = getNIMPipelineServiceCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
