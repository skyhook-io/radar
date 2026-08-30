// KAITO cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getKaitoWorkspaceStatus,
  getKaitoWorkspaceInstanceType,
  getKaitoWorkspacePreset,
  getKaitoWorkspaceNodeCount,
  getRAGEngineStatus,
  getRAGEngineEmbeddingModel,
  getRAGEngineInstanceType,
} from '../resource-utils-kaito'

export function KaitoWorkspaceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getKaitoWorkspaceStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'instanceType': {
      const instanceType = getKaitoWorkspaceInstanceType(resource)
      return <span className="text-sm text-theme-text-secondary">{instanceType}</span>
    }
    case 'preset': {
      const preset = getKaitoWorkspacePreset(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{preset}</span>
    }
    case 'nodes': {
      const nodes = getKaitoWorkspaceNodeCount(resource)
      return <span className="text-sm text-theme-text-secondary">{nodes}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function RAGEngineCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getRAGEngineStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'embedding': {
      const model = getRAGEngineEmbeddingModel(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{model}</span>
    }
    case 'instanceType': {
      const instanceType = getRAGEngineInstanceType(resource)
      return <span className="text-sm text-theme-text-secondary">{instanceType}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
